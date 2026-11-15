// Package content 实现技术方案正文生成。
//
// 这是整个产品最核心的能力，也是最复杂的一块。
//
// **与桌面版的关系**：
//
//	桌面版 contentGenerationTask.cjs 有 3,113 行，配套 6 类业务工具
//	（layout / image / table / word / consistency / edit），
//	涉及受限 HTML 规范、配图编排、表格去留、字数控制、一致性检查。
//
//	直接移植不现实。这里的策略是**分层实现**：
//	  第一层（本文件）：按目录逐节生成 Markdown 正文，带字数控制
//	  第二层（待补）：受限 HTML 结构、配图占位
//	  第三层（待补）：图片生成与回填、表格转换、一致性检查
//
//	先让「生成 → 落库 → 可读」这条链路通，再叠加复杂度。
//
// **为什么用逐节生成而不是一次生成**：
//
//	标书正文动辄十几万字，远超单次上下文。
//	逐节生成 + 每节独立校验，是唯一可行的方式。
package content

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/service/textutil"
)

// 文件名约定。
const (
	// ManifestFile 是生成清单（记录每节状态）
	ManifestFile = "content-manifest.json"
	// SectionsDir 存放各节正文
	SectionsDir = "sections"
)

// Section 是一个待生成的正文小节。
type Section struct {
	// NodeID 对应目录节点的稳定 ID
	NodeID string `json:"nodeId"`
	// Title 章节标题
	Title string `json:"title"`
	// Path 完整路径（如「第一章 > 1.1 项目背景」）
	Path string `json:"path"`
	// Description 本章要写什么（来自目录）
	Description string `json:"description"`
	// TargetWords 目标字数
	TargetWords int `json:"targetWords"`
	// Level 层级
	Level int `json:"level"`

	// KnowledgeRefs 知识库参考素材。
	//
	// 来源：目录节点上挂的 knowledge_item_ids（用户手动关联），
	// 或系统自动匹配（按章节标题检索知识库）。
	//
	// 为什么要给参考素材：让 Agent 有"本单位真实做过什么"的依据，
	// 而不是全靠编 —— 这是知识库存在的全部意义。
	KnowledgeRefs []KnowledgeRef `json:"knowledgeRefs,omitempty"`
}

// KnowledgeRef 是一条知识库参考素材。
type KnowledgeRef struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Resume  string `json:"resume"`
	Content string `json:"content"`
	// Source 来源文件名（让 Agent 知道出处）
	Source string `json:"source,omitempty"`
}

// Manifest 是生成清单。
type Manifest struct {
	TaskID string `json:"taskId"`
	// Generated 每节的生成状态
	Generated []SectionState `json:"generated"`
	// TotalWords 已完成小节的总字数
	TotalWords int    `json:"totalWords"`
	UpdatedAt  string `json:"updatedAt"`
}

// SectionState 是单节状态。
type SectionState struct {
	NodeID string `json:"nodeId"`
	Title  string `json:"title"`
	File   string `json:"file"`
	// Path 是相对工作区的完整路径（Agent 写入时会填）
	Path   string `json:"path,omitempty"`
	Words  int    `json:"words"`
	Status string `json:"status"` // pending | running | done | error
	Error  string `json:"error,omitempty"`
}

// Runner 编排正文生成。
type Runner struct {
	// onContentDone 正文产物入库后的回调（用于写进节点正文）。
	onContentDone func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)

	agentSvc  *agent.Service
	artifacts *artifact.Service
	log       *slog.Logger
}

// NewRunner 创建编排器。
func NewRunner(agentSvc *agent.Service, artifacts *artifact.Service, log *slog.Logger) *Runner {
	return &Runner{agentSvc: agentSvc, artifacts: artifacts, log: log}
}

// StartOptions 是启动参数。
type StartOptions struct {
	TenantID  string
	ProjectID string
	OwnerID   string
	// TenderMarkdownPath 招标文件（Agent 要读它对齐要求）
	TenderMarkdownPath string
	Provider           agent.Provider
	Model              agent.ModelConfig
	ToolDecls          []any
	// Sections 待生成的小节列表（来自目录树的叶子节点）
	Sections []Section
	// GlobalFacts 全局事实（人员、金额、型号等，保证全文一致）
	GlobalFacts string
	// MinWords 全文字数下限（0 表示不限）
	MinWords int
	// ImagePlan 配图编排结果（格式化文本），空表示不配图
	ImagePlan string

	// Resume 恢复参数（nil 表示全新任务）。
	//
	// **恢复的语义**：复用旧工作区，跳过已完成的小节。
	// 不依赖 sidecar 会话（重启后已失效），只看磁盘上的 manifest。
	Resume *ResumeState

	OnEvent func(agent.TaskEvent)
	// OnProgress 工作区小节进度回调（增量，可在整任务完成前多次触发）。
	OnProgress func(ProgressSnapshot)
}

// ProgressSnapshot 是正文生成中的增量进度。
type ProgressSnapshot struct {
	TaskID     string
	DoneCount  int
	TotalCount int
	Progress   int
	Sections   map[string]any
}

// ResumeState 是恢复状态。
type ResumeState struct {
	// TaskID 旧任务 ID
	TaskID string
	// WorkspaceDir 旧工作区绝对路径
	WorkspaceDir string
	// Completed 已完成的小节（nodeId）
	Completed map[string]bool
	// CompletedCount 已完成数量（用于日志与进度显示）
	CompletedCount int
}

// Start 启动正文生成任务。
//
// 阶段设计（单阶段多轮）：
//
//	一个 Agent 任务里逐节生成，每节写完就落盘。
//	这样即使中途失败，已完成的节不会丢。
func (r *Runner) Start(ctx context.Context, opts StartOptions) (*Result, error) {
	if len(opts.Sections) == 0 {
		return nil, fmt.Errorf("没有待生成的正文小节")
	}

	prompt := BuildPrompt(PromptOptions{
		TenderFile:  "tender.md",
		Sections:    opts.Sections,
		GlobalFacts: opts.GlobalFacts,
		MinWords:    opts.MinWords,
		ImagePlan:   opts.ImagePlan,
		// 恢复时告诉 Agent 哪些已经完成，让它跳过
		CompletedSections: completedSectionList(opts.Resume),
	})

	// 预置清单文件，让 Agent 有明确的进度记录位置。
	//
	// 恢复时不重写清单（prepareInputs 会跳过已存在文件），
	// 但仍要在内存里标记已完成状态，供 Prompt 使用。
	manifest := &Manifest{
		Generated: make([]SectionState, 0, len(opts.Sections)),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	for _, s := range opts.Sections {
		status := "pending"
		if opts.Resume != nil && opts.Resume.Completed[s.NodeID] {
			status = "done"
		}
		manifest.Generated = append(manifest.Generated, SectionState{
			NodeID: s.NodeID,
			Title:  s.Title,
			File:   sectionFileName(s),
			Status: status,
		})
	}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")

	onComplete := func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
		result, err := r.artifacts.CommitWorkspace(cctx, storage.Scope{
			TenantID:  opts.TenantID,
			ProjectID: opts.ProjectID,
		}, opts.OwnerID, workspaceDir, artifact.CommitRules{
			TaskID:        taskID,
			Kind:          artifact.KindContent,
			Include:       []string{SectionsDir + "/**", ManifestFile},
			MaxTotalBytes: 100 << 20,
			Source:        "agent",
		})
		if err != nil {
			return fmt.Errorf("提交正文产物失败: %w", err)
		}
		r.log.Info("正文产物已入库",
			slog.String("task_id", taskID),
			slog.Int("count", len(result.Assets)),
			slog.Int64("bytes", result.TotalBytes),
		)

		// 产物入库后写进节点正文，否则前端看不到生成的内容。
		if r.onContentDone != nil {
			r.onContentDone(cctx, opts.TenantID, opts.ProjectID, taskID, succeeded)
		}

		return nil
	}

	handle, err := r.agentSvc.StartTask(ctx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  "content-generation",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: BuildWorkspaceInstructions(opts.ToolDecls, opts.MinWords),
		ActiveTools:           []string{"read", "write", "edit", "bash", "grep", "ls", "find"},
		CustomTools:           opts.ToolDecls,
		Stages: []agent.Stage{
			{
				ID:     "generate-content",
				Prompt: prompt,
				Tools:  append([]string{"read", "write", "edit", "bash", "grep", "ls", "find"}, toolNames(opts.ToolDecls)...),
				// 清单文件必须存在（Agent 要更新进度）
				OutputFile: ManifestFile,
				// 正文生成耗时长，允许更多重试
				MaxRetries: 1,
			},
		},
		Timeout:      2 * time.Hour,
		PrepareFiles: map[string]string{"tender.md": opts.TenderMarkdownPath},
		PrepareContent: map[string][]byte{
			ManifestFile: manifestJSON,
		},
		ResumeFrom: resumeSpec(opts.Resume),
		OnComplete: onComplete,
	}, opts.OnEvent)
	if err != nil {
		return nil, err
	}

	if opts.OnProgress != nil && handle.WorkspaceDir != "" {
		go r.pollWorkspaceProgress(handle.TaskID, handle.WorkspaceDir, opts.Sections, opts.OnProgress)
	}

	return &Result{
		TaskID:       handle.TaskID,
		SectionCount: len(opts.Sections),
		WorkspaceDir: handle.WorkspaceDir,
	}, nil
}

// Result 是生成结果。
type Result struct {
	TaskID       string `json:"taskId"`
	SectionCount int    `json:"sectionCount"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
}

// pollWorkspaceProgress 定时扫描工作区，把已完成小节推给业务层。
func (r *Runner) pollWorkspaceProgress(
	taskID, workspaceDir string,
	sections []Section,
	onProgress func(ProgressSnapshot),
) {
	if onProgress == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastDone := -1
	deadline := time.Now().Add(2*time.Hour + 5*time.Minute)
	for time.Now().Before(deadline) {
		<-ticker.C
		if _, err := os.Stat(workspaceDir); err != nil {
			return
		}
		items, done := ScanWorkspaceProgress(workspaceDir, sections)
		if done == lastDone {
			continue
		}
		lastDone = done
		total := len(sections)
		progress := 10
		if total > 0 {
			progress = 10 + (done * 80 / total)
			if progress > 95 {
				progress = 95
			}
		}
		onProgress(ProgressSnapshot{
			TaskID:     taskID,
			DoneCount:  done,
			TotalCount: total,
			Progress:   progress,
			Sections:   ProgressMap(items),
		})
		if done >= total && total > 0 {
			return
		}
	}
}

// CollectResult 收集生成结果。
//
// 读取清单和各节正文，返回结构化结果供业务层落库。
func CollectResult(workspaceDir string) (*CollectedResult, error) {
	manifestPath := filepath.Join(workspaceDir, ManifestFile)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("读取清单失败: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(cleanJSON(data), &manifest); err != nil {
		return nil, fmt.Errorf("解析清单失败: %w", err)
	}

	out := &CollectedResult{
		Sections:   make([]SectionContent, 0, len(manifest.Generated)),
		TotalWords: 0,
	}

	for _, st := range manifest.Generated {
		// 尝试多种文件名（Agent 可能改写文件名）
		content, err := readSectionFile(workspaceDir, st)
		if err != nil {
			out.Sections = append(out.Sections, SectionContent{
				NodeID: st.NodeID,
				Title:  st.Title,
				Error:  err.Error(),
			})
			continue
		}

		words := textutil.CountReadableWords(content)
		out.TotalWords += words

		out.Sections = append(out.Sections, SectionContent{
			NodeID:  st.NodeID,
			Title:   st.Title,
			File:    st.File,
			Content: content,
			Words:   words,
		})
	}

	return out, nil
}

// CollectedResult 是收集到的全部正文。
type CollectedResult struct {
	Sections   []SectionContent `json:"sections"`
	TotalWords int              `json:"totalWords"`
}

// SectionContent 是单节内容。
type SectionContent struct {
	NodeID  string `json:"nodeId"`
	Title   string `json:"title"`
	File    string `json:"file"`
	Content string `json:"content"`
	Words   int    `json:"words"`
	Error   string `json:"error,omitempty"`
}

// readSectionFile 读单节正文。
func readSectionFile(workspaceDir string, st SectionState) (string, error) {
	candidates := []string{
		filepath.Join(workspaceDir, SectionsDir, st.File),
		filepath.Join(workspaceDir, st.File),
		// 用 NodeID 兜底
		filepath.Join(workspaceDir, SectionsDir, st.NodeID+".md"),
	}

	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("正文文件不存在")
}

// sectionFileName 生成小节文件名。
//
// 用 NodeID 保证唯一，用标题前缀便于人阅读。
func sectionFileName(s Section) string {
	// 标题里的非安全字符转下划线
	safe := make([]rune, 0, 40)
	for _, r := range s.Title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			safe = append(safe, r)
		case r >= 0x4e00 && r <= 0x9fff: // 中文保留
			safe = append(safe, r)
		default:
			if len(safe) > 0 && safe[len(safe)-1] != '_' {
				safe = append(safe, '_')
			}
		}
	}
	base := strings.Trim(string(safe), "_")
	if len([]rune(base)) > 30 {
		base = string([]rune(base)[:30])
	}
	if base == "" {
		base = "section"
	}
	return fmt.Sprintf("%s_%s.md", s.NodeID, base)
}

// cleanJSON 清理可能被代码块包裹的 JSON。
func cleanJSON(data []byte) []byte {
	return []byte(textutil.StripCodeFence(string(data)))
}

// toolNames 从工具声明里提取名字。
func toolNames(decls []any) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		b, err := json.Marshal(d)
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &m) == nil && m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out
}

// 确保 sync 被使用（并发相关工具函数预留）
var _ = sync.Mutex{}

// resumeSpec 把业务层的 ResumeState 转成 agent 层参数。
func resumeSpec(state *ResumeState) *agent.ResumeSpec {
	if state == nil || state.TaskID == "" {
		return nil
	}
	return &agent.ResumeSpec{
		TaskID:       state.TaskID,
		WorkspaceDir: state.WorkspaceDir,
	}
}

// completedSectionList 把已完成集合转成有序列表（给 Prompt 用）。
//
// 排序是为了 Prompt 稳定 —— 同样的输入应该生成同样的 Prompt，
// 否则前缀缓存失效，每次都要重新计费。
func completedSectionList(state *ResumeState) []string {
	if state == nil || len(state.Completed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(state.Completed))
	for id := range state.Completed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SetOnContentDone 注入正文产物入库后的回调。
func (r *Runner) SetOnContentDone(fn func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)) {
	r.onContentDone = fn
}
