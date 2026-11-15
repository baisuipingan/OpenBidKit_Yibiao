// Package taskrun 管理任务的运行记录。
//
// 解决的问题：
//
//	当前任务状态全在内存（Service.sessions、各 handler 的 tasks map），
//	服务一重启就全丢：
//	  - 用户看到「任务消失了」，不知道该重试还是等待
//	  - 工作区还占着磁盘，定时清理任务找不到它
//	  - 无法统计「跑过多少任务、成功率多少」
//
// 设计取舍：
//   - **不记录事件流**：事件太多（一个任务几千条），重启后也没意义
//   - **只记任务级状态**：kind / status / sessionId / workspace / progress
//   - **心跳机制**：定期更新 heartbeat_at，用于判定僵死
package taskrun

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/store"
)

// 状态枚举。
const (
	StatusRunning     = "running"
	StatusSuccess     = "success"
	StatusError       = "error"
	StatusInterrupted = "interrupted"
)

// Service 管理任务运行记录。
type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger
}

// NewService 创建服务。
func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), log: log}
}

// Create 创建任务记录。
//
// 在工作区创建后、sidecar 会话创建后调用（需要 sessionId）。
func (s *Service) Create(ctx context.Context, params CreateParams) error {
	tid, err := uuid.Parse(params.TenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}
	pid, err := uuid.Parse(params.ProjectID)
	if err != nil {
		return fmt.Errorf("项目标识非法")
	}
	taskID, err := uuid.Parse(params.TaskID)
	if err != nil {
		return fmt.Errorf("任务标识非法")
	}

	var ownerID *uuid.UUID
	if params.OwnerID != "" {
		if oid, err := uuid.Parse(params.OwnerID); err == nil {
			ownerID = &oid
		}
	}

	if _, err := s.q.CreateTaskRun(ctx, store.CreateTaskRunParams{
		TenantID:     tid,
		ProjectID:    pid,
		TaskID:       taskID,
		Kind:         params.Kind,
		SessionID:    params.SessionID,
		WorkspaceDir: params.WorkspaceDir,
		OwnerID:      ownerID,
	}); err != nil {
		return fmt.Errorf("创建任务记录失败: %w", err)
	}

	s.log.Debug("任务记录已创建",
		slog.String("task_id", params.TaskID),
		slog.String("kind", params.Kind),
	)
	return nil
}

// CreateParams 是创建参数。
type CreateParams struct {
	TenantID     string
	ProjectID    string
	TaskID       string
	Kind         string
	SessionID    string
	WorkspaceDir string
	OwnerID      string
}

// Finish 标记任务结束。
func (s *Service) Finish(ctx context.Context, taskID string, success bool, errMsg string) {
	status := StatusSuccess
	if !success {
		status = StatusError
	}

	tid, err := uuid.Parse(taskID)
	if err != nil {
		return
	}

	// 用独立 context：即使主 ctx 已取消也要写进去
	ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx
	_ = tid

	if err := s.q.UpdateTaskRunStatus(ctx2, store.UpdateTaskRunStatusParams{
		TaskID: tid,
		Status: status,
		Error:  errMsg,
	}); err != nil {
		s.log.Warn("更新任务状态失败",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()),
		)
	}
}

// UpdateProgress 更新进度。
func (s *Service) UpdateProgress(ctx context.Context, taskID string, progress map[string]any) {
	tid, err := uuid.Parse(taskID)
	if err != nil {
		return
	}

	data, err := json.Marshal(progress)
	if err != nil {
		return
	}

	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx

	if err := s.q.UpdateTaskRunProgress(ctx2, store.UpdateTaskRunProgressParams{
		TaskID:   tid,
		Progress: data,
	}); err != nil {
		s.log.Debug("更新任务进度失败", slog.String("error", err.Error()))
	}
}

// Heartbeat 更新心跳。
func (s *Service) Heartbeat(ctx context.Context, taskID string) {
	tid, err := uuid.Parse(taskID)
	if err != nil {
		return
	}
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx
	_ = s.q.HeartbeatTaskRun(ctx2, tid)
}

// View 是任务记录视图。
type View struct {
	TaskID       string         `json:"taskId"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	SessionID    string         `json:"sessionId,omitempty"`
	WorkspaceDir string         `json:"workspaceDir,omitempty"`
	Error        string         `json:"error,omitempty"`
	Progress     map[string]any `json:"progress,omitempty"`
	StartedAt    time.Time      `json:"startedAt"`
	HeartbeatAt  time.Time      `json:"heartbeatAt"`
	FinishedAt   *time.Time     `json:"finishedAt,omitempty"`
	// Stale 表示心跳超时（可能僵死）
	Stale bool `json:"stale"`
}

// ListRunning 列出运行中的任务。
func (s *Service) ListRunning(ctx context.Context) ([]View, error) {
	rows, err := s.q.ListRunningTaskRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询运行中任务失败: %w", err)
	}

	out := make([]View, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return out, nil
}

// ListByProject 列出项目的任务历史。
func (s *Service) ListByProject(ctx context.Context, tenantID, projectID string, limit int32) ([]View, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("租户标识非法")
	}
	pid, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("项目标识非法")
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.q.ListTaskRunsByProject(ctx, store.ListTaskRunsByProjectParams{
		ProjectID: pid,
		TenantID:  tid,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("查询任务历史失败: %w", err)
	}

	out := make([]View, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return out, nil
}

// Get 查单个任务。
func (s *Service) Get(ctx context.Context, taskID string) (*View, error) {
	tid, err := uuid.Parse(taskID)
	if err != nil {
		return nil, fmt.Errorf("任务标识非法")
	}
	row, err := s.q.GetTaskRun(ctx, tid)
	if err != nil {
		return nil, err
	}
	v := toView(row)
	return &v, nil
}

// RecoverOnStartup 服务启动时清理僵死任务。
//
// 做什么：
//
//	把所有「运行中但心跳超时」的任务标为 interrupted。
//	为什么不恢复：sidecar 会话在服务重启时已丢失，
//	无法凭 sessionId 恢复（除非 sidecar 也做了持久化，目前没有）。
//
// 后续可做：如果 sidecar 支持查会话状态，可以尝试真正恢复。
func (s *Service) RecoverOnStartup(ctx context.Context, staleSeconds int32) (int64, error) {
	if staleSeconds <= 0 {
		// 默认 5 分钟无心跳算僵死
		staleSeconds = 300
	}

	n, err := s.q.MarkStaleTaskRunsInterrupted(ctx, staleSeconds)
	if err != nil {
		return 0, fmt.Errorf("清理僵死任务失败: %w", err)
	}

	if n > 0 {
		s.log.Warn("已将僵死任务标为中断",
			slog.Int64("count", n),
			slog.Int("stale_seconds", int(staleSeconds)),
		)
	}

	return n, nil
}

// toView 转视图。
func toView(r store.TaskRun) View {
	v := View{
		TaskID:       r.TaskID.String(),
		Kind:         r.Kind,
		Status:       r.Status,
		SessionID:    r.SessionID,
		WorkspaceDir: r.WorkspaceDir,
		Error:        r.Error,
		StartedAt:    r.StartedAt,
		HeartbeatAt:  r.HeartbeatAt,
	}

	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time
		v.FinishedAt = &t
	}

	if len(r.Progress) > 0 {
		var p map[string]any
		if json.Unmarshal(r.Progress, &p) == nil {
			v.Progress = p
		}
	}

	// 心跳超过 2 分钟算僵死
	v.Stale = v.Status == StatusRunning && time.Since(v.HeartbeatAt) > 2*time.Minute

	return v
}

// CreateFromAgent 适配 agent.TaskRunCreate。
//
// 放在 taskrun 包而不是 agent 包，避免 agent 依赖 store。
func (s *Service) CreateFromAgent(ctx context.Context, p CreateParams) error {
	return s.Create(ctx, p)
}

// CreateAdapter 返回一个符合 agent.TaskRunRecorder 的适配器。
//
// 为什么需要适配：agent 包定义的接口用了 agent.TaskRunCreate，
// 而 taskrun 用自己的 CreateParams，字段相同但类型不同。
func (s *Service) CreateAdapter(p CreateParams) CreateParams {
	return p
}
