// Package outline 实现技术方案目录生成。
//
// 这是标书垂类 Agent 的第二个核心能力。
//
// 与桌面版的差异：
//
//	桌面版是多阶段流程（技术评分分组 → 初始目录 → 子目录 → 叶子调整 → 复核），
//	总计 1806 行代码、5 个阶段。
//
//	Web 版先做单阶段版本（分析评分要求 + 生成目录），
//	验证 Agent 能力后再逐步补多阶段。
//
//	这样做的理由：单阶段已经能产出可用目录，
//	而多阶段的收益（更精细的层级控制）需要真实标书样本才能验证。
package outline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

// 文件名约定。
const (
	// OutlineFile 是 Agent 产出的目录文件
	OutlineFile = "outline.json"
	// AnalysisFile 是技术评分分析中间产物
	AnalysisFile = "score-analysis.md"
)

// Node 是目录节点。
//
// 字段与桌面版保持一致，便于前端复用类型定义。
type Node struct {
	// ID 由程序分配（不信任 Agent 生成的 ID）
	ID string `json:"id"`
	// Title 章节标题
	Title string `json:"title"`
	// Description 本章要写什么（给正文生成阶段的指引）
	Description string `json:"description"`
	// ContentMode 正文生成方式：technical（技术方案）| business（商务响应）
	ContentMode string `json:"content_mode,omitempty"`
	// RuleBasis 应答依据：technical | business | both（仅规则页码叶子）
	RuleBasis string `json:"rule_basis,omitempty"`
	// Children 子节点
	Children []*Node `json:"children,omitempty"`
}

// Result 是目录生成结果。
type Result struct {
	TaskID string  `json:"taskId"`
	Nodes  []*Node `json:"nodes"`
	// Analysis 是技术评分分析（Markdown）
	Analysis string `json:"analysis,omitempty"`
}

// Runner 编排目录生成任务。
type Runner struct {
	// onOutlineDone 目录产物入库后的回调（用于写进节点表）。
	onOutlineDone func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)

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
	// TenderMarkdownPath 招标文件路径（Agent 要读它分析评分要求）
	TenderMarkdownPath string
	Provider           agent.Provider
	Model              agent.ModelConfig
	ToolDecls          []any
	// ProjectName 项目名称（用于目录标题参考）
	ProjectName string
	// MaxLevels 目录最大层级，0 表示不限制
	MaxLevels int
	OnEvent   func(agent.TaskEvent)
}

// Start 启动目录生成任务。
func (r *Runner) Start(ctx context.Context, opts StartOptions) (*Result, error) {
	if opts.TenderMarkdownPath == "" {
		return nil, fmt.Errorf("缺少招标文件路径")
	}

	prompt := BuildPrompt(PromptOptions{
		TenderFile:  "tender.md",
		ProjectName: opts.ProjectName,
		MaxLevels:   opts.MaxLevels,
	})

	onComplete := func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
		result, err := r.artifacts.CommitWorkspace(cctx, storage.Scope{
			TenantID:  opts.TenantID,
			ProjectID: opts.ProjectID,
		}, opts.OwnerID, workspaceDir, artifact.CommitRules{
			TaskID:        taskID,
			Kind:          artifact.KindOutline,
			Include:       []string{OutlineFile, AnalysisFile},
			MaxTotalBytes: 20 << 20,
			Source:        "agent",
		})
		if err != nil {
			return fmt.Errorf("提交目录产物失败: %w", err)
		}
		r.log.Info("目录产物已入库",
			slog.String("task_id", taskID),
			slog.Int("count", len(result.Assets)),
		)

		// 产物入库后立刻写进节点表，否则前端读不到目录。
		if r.onOutlineDone != nil {
			r.onOutlineDone(cctx, opts.TenantID, opts.ProjectID, taskID, succeeded)
		}

		return nil
	}

	handle, err := r.agentSvc.StartTask(ctx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  "outline-generation",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: BuildWorkspaceInstructions(opts.ToolDecls),
		ActiveTools:           []string{"read", "write", "edit", "bash", "grep", "ls", "find"},
		CustomTools:           opts.ToolDecls,
		Stages: []agent.Stage{
			{
				ID:     "outline",
				Prompt: prompt,
				Tools:  append([]string{"read", "write", "edit", "bash", "grep", "ls", "find"}, toolNames(opts.ToolDecls)...),
				// 强制产出 outline.json，Agent 没写就重试
				OutputFile: OutlineFile,
				MaxRetries: 2,
			},
		},
		Timeout:      45 * time.Minute,
		PrepareFiles: map[string]string{"tender.md": opts.TenderMarkdownPath},
		OnComplete:   onComplete,
	}, opts.OnEvent)
	if err != nil {
		return nil, err
	}

	return &Result{TaskID: handle.TaskID}, nil
}

// ParseOutline 解析 Agent 产出的 outline.json。
//
// 关键：程序给每个节点分配稳定 ID，不信任 Agent 生成的 ID。
// 原因（来自桌面版踩坑）：Agent 可能生成重复或非法的 ID，
// 而 ID 是目录树的关联键，必须由程序保证唯一性。
func ParseOutline(workspaceDir string, maxLevels int) ([]*Node, error) {
	data, err := readWorkspaceFile(workspaceDir, OutlineFile)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Outline []rawNode `json:"outline"`
	}
	if err := json.Unmarshal(cleanJSON(data), &raw); err != nil {
		return nil, fmt.Errorf("解析 outline.json 失败: %w", err)
	}
	if len(raw.Outline) == 0 {
		return nil, fmt.Errorf("目录为空")
	}

	counter := &idCounter{}
	nodes := convertNodes(raw.Outline, 1, maxLevels, counter)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("目录解析后为空")
	}
	return nodes, nil
}

// rawNode 是 Agent 输出的原始节点结构。
type rawNode struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	ContentMode string    `json:"content_mode"`
	RuleBasis   string    `json:"rule_basis"`
	Children    []rawNode `json:"children"`
}

// idCounter 生成稳定 ID。
//
// 用前缀 + 递增序号，形如 n1、n2、n1-1、n1-2。
// 不用 UUID 是为了让人能看懂层级关系（排查问题时有用）。
type idCounter struct {
	seq int
}

func (c *idCounter) next(parentID string) string {
	c.seq++
	if parentID == "" {
		return fmt.Sprintf("n%d", c.seq)
	}
	return fmt.Sprintf("%s-%d", parentID, c.seq)
}

// convertNodes 递归转换并分配 ID。
func convertNodes(raws []rawNode, level, maxLevels int, counter *idCounter) []*Node {
	out := make([]*Node, 0, len(raws))

	for _, raw := range raws {
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			// 空标题节点直接丢弃，避免目录里出现无名章节
			continue
		}

		node := &Node{
			ID:          counter.next(""),
			Title:       title,
			Description: strings.TrimSpace(raw.Description),
			ContentMode: normalizeContentMode(raw.ContentMode),
			RuleBasis:   normalizeRuleBasis(raw.RuleBasis),
		}

		// 超过最大层级时，子节点提升为当前层（不丢内容）
		if maxLevels > 0 && level >= maxLevels {
			node.Children = nil
		} else if len(raw.Children) > 0 {
			node.Children = convertNodesWithParent(raw.Children, level+1, maxLevels, counter, node.ID)
		}

		out = append(out, node)
	}

	return out
}

// convertNodesWithParent 转换子节点，ID 带父前缀。
func convertNodesWithParent(raws []rawNode, level, maxLevels int, counter *idCounter, parentID string) []*Node {
	out := make([]*Node, 0, len(raws))

	for _, raw := range raws {
		title := strings.TrimSpace(raw.Title)
		if title == "" {
			continue
		}

		node := &Node{
			ID:          counter.next(parentID),
			Title:       title,
			Description: strings.TrimSpace(raw.Description),
			ContentMode: normalizeContentMode(raw.ContentMode),
			RuleBasis:   normalizeRuleBasis(raw.RuleBasis),
		}

		if maxLevels > 0 && level >= maxLevels {
			node.Children = nil
		} else if len(raw.Children) > 0 {
			node.Children = convertNodesWithParent(raw.Children, level+1, maxLevels, counter, node.ID)
		}

		out = append(out, node)
	}

	return out
}

// normalizeContentMode 规范化正文方式。
func normalizeContentMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "technical":
		return "technical"
	case "business":
		return "business"
	case "":
		return ""
	default:
		return ""
	}
}

// normalizeRuleBasis 规范化应答依据。
func normalizeRuleBasis(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "technical", "business", "both":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

// FlattenNodes 把目录树拍平成带层级的有序列表。
//
// 用途：正文生成阶段按顺序遍历，前端渲染列表。
func FlattenNodes(nodes []*Node) []FlatNode {
	out := make([]FlatNode, 0, 32)
	var walk func(list []*Node, level int)
	walk = func(list []*Node, level int) {
		for _, n := range list {
			out = append(out, FlatNode{Node: n, Level: level})
			if len(n.Children) > 0 {
				walk(n.Children, level+1)
			}
		}
	}
	walk(nodes, 1)
	return out
}

// FlatNode 是拍平后的节点。
type FlatNode struct {
	Node  *Node
	Level int
}

// ValidateOutline 校验目录结构完整性。
//
// 检查项：
//   - 标题不为空
//   - ID 唯一
//   - 至少有一个节点
//   - 层级不超过限制
func ValidateOutline(nodes []*Node) []string {
	var issues []string

	if len(nodes) == 0 {
		return []string{"目录为空"}
	}

	ids := make(map[string]bool)
	var walk func(list []*Node, path string)
	walk = func(list []*Node, path string) {
		for _, n := range list {
			cur := path + "/" + n.Title

			if strings.TrimSpace(n.Title) == "" {
				issues = append(issues, fmt.Sprintf("%s：标题为空", path))
			}
			if ids[n.ID] {
				issues = append(issues, fmt.Sprintf("%s：ID 重复（%s）", cur, n.ID))
			}
			ids[n.ID] = true

			if len(n.Children) > 0 {
				walk(n.Children, cur)
			}
		}
	}
	walk(nodes, "")

	return issues
}

// CountNodes 统计节点总数。
func CountNodes(nodes []*Node) int {
	n := 0
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, node := range list {
			n++
			if len(node.Children) > 0 {
				walk(node.Children)
			}
		}
	}
	walk(nodes)
	return n
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

// SetOnOutlineDone 注入目录产物入库后的回调。
func (r *Runner) SetOnOutlineDone(fn func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)) {
	r.onOutlineDone = fn
}
