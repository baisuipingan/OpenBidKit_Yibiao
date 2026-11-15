package globalfacts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

const (
	// OutputFile 是 Agent 必须写出的产物名。
	OutputFile = "global-facts.json"
	// TaskType 与前端 / SSE / findRunning 对齐。
	TaskType = "global-facts-generation"
)

// Runner 编排全局事实生成。
type Runner struct {
	onDone    func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)
	agentSvc  *agent.Service
	artifacts *artifact.Service
	log       *slog.Logger
}

// NewRunner 创建编排器。
func NewRunner(agentSvc *agent.Service, artifacts *artifact.Service, log *slog.Logger) *Runner {
	return &Runner{agentSvc: agentSvc, artifacts: artifacts, log: log}
}

// SetOnDone 注入完成后回写回调（MarkDone + persist + SSE）。
func (r *Runner) SetOnDone(fn func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool)) {
	r.onDone = fn
}

// StartOptions 启动参数。
type StartOptions struct {
	TenantID           string
	ProjectID          string
	OwnerID            string
	TenderMarkdownPath string
	Provider           agent.Provider
	Model              agent.ModelConfig
	ToolDecls          []any
	GlobalFactsMode    string
	// PrepareContent 额外工作区文件（项目概述、目录、解析结果等）。
	PrepareContent map[string][]byte
	OnEvent        func(agent.TaskEvent)
}

// Result 启动结果。
type Result struct {
	TaskID string `json:"taskId"`
}

// Start 启动全局事实生成任务。
func (r *Runner) Start(ctx context.Context, opts StartOptions) (*Result, error) {
	if opts.TenderMarkdownPath == "" {
		return nil, fmt.Errorf("缺少招标文件路径")
	}
	if _, err := os.Stat(opts.TenderMarkdownPath); err != nil {
		return nil, fmt.Errorf("招标文件不存在: %w", err)
	}

	mode := NormalizeMode(opts.GlobalFactsMode)
	catalog := DefaultFileCatalog()
	prompt := BuildPrompt(mode, catalog)

	onComplete := func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
		_, err := r.artifacts.CommitWorkspace(cctx, storage.Scope{
			TenantID:  opts.TenantID,
			ProjectID: opts.ProjectID,
		}, opts.OwnerID, workspaceDir, artifact.CommitRules{
			TaskID:        taskID,
			Kind:          artifact.KindGlobalFacts,
			Include:       []string{OutputFile},
			MaxTotalBytes: 20 << 20,
			Source:        "agent",
		})
		if err != nil {
			return fmt.Errorf("提交全局事实产物失败: %w", err)
		}
		r.log.Info("全局事实产物已入库", slog.String("task_id", taskID))

		if r.onDone != nil {
			r.onDone(cctx, opts.TenantID, opts.ProjectID, taskID, succeeded)
		}
		return nil
	}

	prepareContent := map[string][]byte{}
	for k, v := range opts.PrepareContent {
		prepareContent[k] = v
	}
	if _, ok := prepareContent["材料说明.md"]; !ok {
		prepareContent["材料说明.md"] = []byte("本次任务实际提供的材料如下。只使用这些文件，不要猜测未提供的材料。\n\n" + catalog)
	}

	handle, err := r.agentSvc.StartTask(ctx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  TaskType,
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: BuildWorkspaceInstructions(),
		ActiveTools:           []string{"read", "write", "edit", "bash", "grep", "ls", "find"},
		CustomTools:           opts.ToolDecls,
		Stages: []agent.Stage{
			{
				ID:         "global-facts",
				Prompt:     prompt,
				Tools:      append([]string{"read", "write", "edit", "bash", "grep", "ls", "find"}, toolNames(opts.ToolDecls)...),
				OutputFile: OutputFile,
				MaxRetries: 2,
			},
		},
		Timeout:        45 * time.Minute,
		PrepareFiles:   map[string]string{"tender.md": opts.TenderMarkdownPath},
		PrepareContent: prepareContent,
		OnComplete:     onComplete,
	}, opts.OnEvent)
	if err != nil {
		return nil, err
	}

	r.log.Info("全局事实任务已启动",
		slog.String("task_id", handle.TaskID),
		slog.String("project_id", opts.ProjectID),
		slog.String("mode", mode),
	)
	return &Result{TaskID: handle.TaskID}, nil
}

func toolNames(decls []any) []string {
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}
