package bidanalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

// Runner 编排招标文件分析任务。
type Runner struct {
	// onAnalysisDone 任务结束后的回调（用于把产物回写进业务表）。
	// 由上层注入，避免 runner 直接依赖 service 层（会循环引用）。
	onAnalysisDone func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)

	agentSvc  *agent.Service
	artifacts *artifact.Service
	log       *slog.Logger
}

// NewRunner 创建编排器。
func NewRunner(agentSvc *agent.Service, artifacts *artifact.Service, log *slog.Logger) *Runner {
	return &Runner{agentSvc: agentSvc, artifacts: artifacts, log: log}
}

// StartOptions 是启动分析的参数。
type StartOptions struct {
	TenantID  string
	ProjectID string
	// OwnerID 操作者 user_id，记入资产归属
	OwnerID string
	// ToolDecls 可用的业务工具声明。由上层从工具注册表取。
	ToolDecls []any
	// TenderMarkdownPath 是招标文件转成的 Markdown 绝对路径
	TenderMarkdownPath string
	Provider           agent.Provider
	Model              agent.ModelConfig
	Items              []Item
	// OnEvent 事件回调（推给前端）
	OnEvent func(agent.TaskEvent)
}

// Result 是分析任务的结果。
type Result struct {
	TaskID  string         `json:"taskId"`
	Items   map[string]any `json:"items"`
	Issues  []Issue        `json:"issues"`
	Success bool           `json:"success"`
}

// Start 启动招标文件分析任务。
//
// 流程：
//  1. 把招标 Markdown 复制到 Agent 工作目录
//  2. 构造多阶段任务：
//     - 阶段 1：Agent 提取全部解析项，写成文件
//     - 校验回调由本包的 ValidateResult 承担
//  3. Agent 完成或放弃后，读取产物返回给业务层
func (r *Runner) Start(ctx context.Context, opts StartOptions) (*Result, error) {
	if opts.TenderMarkdownPath == "" {
		return nil, fmt.Errorf("缺少招标文件路径")
	}
	if _, err := os.Stat(opts.TenderMarkdownPath); err != nil {
		return nil, fmt.Errorf("招标文件不存在: %w", err)
	}

	items := opts.Items
	if len(items) == 0 {
		items = DefaultItems
	}

	// 构造 Agent 提示词（含全部解析项定义）
	prompt := BuildAgentPrompt("tender.md", items)

	stages := []agent.Stage{
		{
			ID:         "extract-items",
			Prompt:     prompt,
			Tools:      []string{"read", "write", "edit", "bash", "grep", "ls", "find"},
			MaxRetries: 2,
			// 校验由 Agent 侧的 validate 钩子完成（通过回调机制）
			// 这里不设 OutputFile，因为产物是多个文件
		},
	}

	// 任务结束：把产物提交为资产库。
	//
	// 只提交 items/** 和 summary.json，不提交 tender.md（那是输入，
	// 输入应该由调用方提前放进资产库）。
	onComplete := func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
		if !succeeded {
			// 失败任务也提交产物，便于排查「Agent 到底写了什么」
			r.log.Info("任务失败，仍提交产物用于排查", slog.String("task_id", taskID))
		}

		result, err := r.artifacts.CommitWorkspace(cctx, storage.Scope{
			TenantID:  opts.TenantID,
			ProjectID: opts.ProjectID,
		}, opts.OwnerID, workspaceDir, artifact.CommitRules{
			TaskID:        taskID,
			Kind:          artifact.KindBidItems,
			Include:       []string{"items/**", "summary.json"},
			MaxTotalBytes: 50 << 20, // 50 MB 上限
			Source:        "agent",
		})
		if err != nil {
			return fmt.Errorf("提交分析产物失败: %w", err)
		}

		r.log.Info("分析产物已入库",
			slog.String("task_id", taskID),
			slog.Int("count", len(result.Assets)),
			slog.Int64("bytes", result.TotalBytes),
			slog.Int("skipped", len(result.Skipped)),
		)

		// 产物入库后立刻回写进 technical_plan_meta。
		//
		// 必须在这里做而不是 handler 里：Runner.Start 是异步的，
		// handler 拿到 taskId 就返回了，那时产物还没写盘。
		// onComplete 是任务真正结束的唯一时机。
		if r.onAnalysisDone != nil {
			r.onAnalysisDone(cctx, opts.TenantID, opts.ProjectID, taskID, succeeded)
		}

		return nil
	}

	handle, err := r.agentSvc.StartTask(ctx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		Type:                  "bid-analysis",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: buildWorkspaceInstructions(opts.ToolDecls),
		ActiveTools:           []string{"read", "write", "edit", "bash", "grep", "ls", "find"},
		Stages:                stages,
		Timeout:               45 * time.Minute,
		// 招标文件必须在会话创建前就位
		PrepareFiles: map[string]string{"tender.md": opts.TenderMarkdownPath},
		CustomTools:  opts.ToolDecls,
		OnComplete:   onComplete,
	}, opts.OnEvent)
	if err != nil {
		return nil, err
	}

	r.log.Info("招标分析任务已启动",
		slog.String("task_id", handle.TaskID),
		slog.String("project_id", opts.ProjectID),
		slog.Int("item_count", len(items)),
	)

	return &Result{TaskID: handle.TaskID}, nil
}

// CollectResult 在任务结束后收集产物。
func (r *Runner) CollectResult(workspace string, items []Item) (*Result, error) {
	if len(items) == 0 {
		items = DefaultItems
	}

	report, err := ValidateResult(workspace, items)
	if err != nil {
		return nil, err
	}

	got, _ := report.Value.(map[string]any)

	return &Result{
		Items:   got,
		Issues:  report.Issues,
		Success: countBlocking(report.Issues) == 0,
	}, nil
}

// LoadFromWorkspace 从已有工作区读取分析结果（任务恢复时用）。
func (r *Runner) LoadFromWorkspace(workspace string, items []Item) (*Result, error) {
	return r.CollectResult(workspace, items)
}

// buildWorkspaceInstructions 构造招标分析的工作区约定。
//
// 明确列出业务工具，避免 Agent 反复试探「有没有这个工具」，
// 也防止它去翻软件自身源码。
func buildWorkspaceInstructions(toolDecls []any) string {
	var sb strings.Builder
	sb.WriteString("你是投标文件分析专家。用工具读取招标文件，逐项提取信息写入文件。\n\n")

	sb.WriteString("## 可用业务工具\n\n")
	if len(toolDecls) == 0 {
		sb.WriteString("（本会话未注册业务工具，用原生 read/write 即可）\n")
	} else {
		for _, d := range toolDecls {
			b, err := json.Marshal(d)
			if err != nil {
				continue
			}
			var m struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if json.Unmarshal(b, &m) == nil && m.Name != "" {
				sb.WriteString(fmt.Sprintf("- `%s`：%s\n", m.Name, m.Description))
			}
		}
	}

	sb.WriteString("\n## 工作要求\n\n")
	sb.WriteString("1. 工作区里已有 `tender.md`（招标文件），直接读取即可\n")
	sb.WriteString("2. 需要统计字数用 `count_words`，不要自己估算\n")
	sb.WriteString("3. **不要读取软件自身的源码或安装目录**，只操作当前工作区\n")
	sb.WriteString("4. 全部完成后不要输出总结，直接结束\n")
	return sb.String()
}

// ---------- 内部工具 ----------

func countBlocking(issues []Issue) int {
	n := 0
	for _, i := range issues {
		if i.Severity == "blocking" {
			n++
		}
	}
	return n
}

// 用于清洗 Agent 输出里的非 JSON 内容
var (
	jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*\\n(.*?)\\n```")
	firstBrace  = regexp.MustCompile(`(?s)\{.*\}`)
)

// CleanJSON 尽力从 Agent 输出里提取 JSON。
//
// Agent 常见问题：加了说明文字、用代码块包裹、结尾多出逗号。
// 这个函数做基础清洗，失败时返回原内容让校验器报错。
func CleanJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if json.Valid([]byte(s)) {
		return s
	}
	// 尝试提取代码块
	if m := jsonBlockRe.FindStringSubmatch(s); len(m) > 1 {
		if candidate := strings.TrimSpace(m[1]); json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	// 尝试提取第一个 {...}
	if m := firstBrace.FindString(s); m != "" {
		if json.Valid([]byte(m)) {
			return m
		}
	}
	return s
}

// SetOnAnalysisDone 注入任务完成回调。
//
// 参数是函数而非接口，避免 runner 反向依赖业务 service。
func (r *Runner) SetOnAnalysisDone(fn func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)) {
	r.onAnalysisDone = fn
}
