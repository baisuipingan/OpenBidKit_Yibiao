package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/patrick/yibiao-web/internal/service/workspace"
)

// TaskSpec 描述一个要执行的 Agent 任务。
type TaskSpec struct {
	TenantID  string
	ProjectID string
	// OwnerID 操作者 user_id，记入资产归属与工具调用上下文
	OwnerID string
	Type    string // bid-analysis | outline | global-facts | content | ...

	// Provider 模型配置。生产环境从租户配置解密得到。
	Provider Provider
	Model    ModelConfig

	// Stages 阶段定义，由业务层构造（含 Prompt）
	Stages []Stage

	// WorkspaceInstructions 工作区约定（AGENTS.md 内容）
	WorkspaceInstructions string

	// ActiveTools 初始工具集
	ActiveTools []string

	// PrepareFiles 在创建会话前写入工作区。
	// 键是相对路径（如 "tender.md"），值是源文件绝对路径。
	// 必须在会话开始前就位，否则 Agent 第一次读文件会失败。
	PrepareFiles map[string]string

	// PrepareContent 在创建会话前写入工作区（直接给内容而非文件路径）。
	PrepareContent map[string][]byte

	// CustomTools 业务工具声明。由上层从 agenttool.Registry 取。
	// sidecar 会把它转成 HTTP 回调工具交给 Agent。
	CustomTools []any

	// OnComplete 任务结束后回调，用于把产物提交到资产库。
	// 参数：workspaceDir（产物所在目录）、succeeded（是否成功）。
	//
	// 这是「沙箱 → 资产库」的唯一出口。调研结论：
	// Agent 写文件 ≠ 用户能拿到文件，必须有显式的登记步骤。
	OnComplete func(ctx context.Context, workspaceDir string, taskID string, succeeded bool) error

	// ResumeFrom 从旧任务恢复时指定。
	//
	// **恢复的本质是复用工作区**，不是接着旧会话跑 ——
	// sidecar 会话在服务重启后已失效，但工作区里的产物都还在磁盘上。
	//
	// 所以恢复的做法是：
	//   1. 复用旧任务的目录（不重新 Create）
	//   2. 保留旧 taskID（产物路径保持一致）
	//   3. 业务层把"已完成的小节"写进 Prompt，让 Agent 跳过
	//
	// 非空时：workspaceDir 指向旧目录，taskID 继承。
	ResumeFrom *ResumeSpec

	Timeout time.Duration
}

// ResumeSpec 是任务恢复参数。
type ResumeSpec struct {
	// TaskID 旧任务 ID（工作区目录名就是它）
	TaskID string
	// WorkspaceDir 旧工作区绝对路径
	WorkspaceDir string
}

// TaskHandle 是运行中任务的句柄。
type TaskHandle struct {
	TaskID    string
	SessionID string
	// WorkspaceDir 工作区路径（调用方可能需要直接读产物）
	WorkspaceDir string
	Cancel       context.CancelFunc
}

// TaskEvent 是转发给业务层的事件。
type TaskEvent struct {
	Type string
	Data json.RawMessage
}

// SessionContext 记录一个 Agent 会话的归属信息。
//
// 用途：sidecar 回调工具时，通过 sessionId 反查租户/项目/工作区。
// 这样工具 handler 不信任请求体里的 scope 字段，防越权。
type SessionContext struct {
	TenantID     string
	ProjectID    string
	UserID       string
	TaskID       string
	SessionID    string
	WorkspaceDir string
}

// Service 编排 Agent 任务。
type Service struct {
	client  *Client
	manager *Manager
	log     *slog.Logger
	// ws 管理临时工作区（任务结束即回收）
	ws *workspace.Manager

	mu       sync.RWMutex
	sessions map[string]*SessionContext

	// taskRuns 是任务运行记录（可为 nil，表示不做持久化）
	taskRuns TaskRunRecorder

	// bus 是事件总线（可为 nil）。用于把 sidecar 的提问推给前端。
	bus EventPublisher
}

// EventPublisher 是事件发布接口。
//
// 定义在 agent 包是为了避免依赖 events 包的具体实现。
type EventPublisher interface {
	Publish(topic string, scope EventScope, payload any)
}

// EventScope 是事件作用域。
type EventScope struct {
	TenantID  string
	UserID    string
	ProjectID string
}

// SetEventPublisher 注入事件发布器。
func (s *Service) SetEventPublisher(bus EventPublisher) {
	s.bus = bus
}

// TaskRunRecorder 是任务记录的接口。
//
// 定义在 agent 包是为了避免循环依赖：
//
//	taskrun 依赖 store，agent 不直接依赖 taskrun 实现。
type TaskRunRecorder interface {
	Create(ctx context.Context, params TaskRunCreate) error
	Finish(ctx context.Context, taskID string, success bool, errMsg string)
	UpdateProgress(ctx context.Context, taskID string, progress map[string]any)
}

// TaskRunCreate 是创建任务记录的参数。
type TaskRunCreate struct {
	TenantID     string
	ProjectID    string
	TaskID       string
	Kind         string
	SessionID    string
	WorkspaceDir string
	OwnerID      string
}

// SetTaskRunRecorder 注入任务记录器。
func (s *Service) SetTaskRunRecorder(rec TaskRunRecorder) {
	s.taskRuns = rec
}

// NewService 创建服务。
func NewService(client *Client, wsMgr *workspace.Manager, log *slog.Logger) *Service {
	return &Service{
		client:   client,
		manager:  NewManager(client),
		log:      log,
		ws:       wsMgr,
		sessions: make(map[string]*SessionContext),
	}
}

// LookupSession 按 sessionId 反查会话上下文。
func (s *Service) LookupSession(sessionID string) (*SessionContext, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx, ok := s.sessions[sessionID]
	return ctx, ok
}

// LookupTask 按 taskId 反查会话上下文。
func (s *Service) LookupTask(taskID string) (*SessionContext, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ctx := range s.sessions {
		if ctx.TaskID == taskID {
			return ctx, true
		}
	}
	return nil, false
}

func (s *Service) registerSession(ctx *SessionContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[ctx.SessionID] = ctx
}

// truncateErr 截断错误信息，避免数据库里存超长文本。
func truncateErr(msg string) string {
	const limit = 500
	if len(msg) <= limit {
		return msg
	}
	return msg[:limit] + "…"
}

func (s *Service) unregisterSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// StartTask 启动任务并返回句柄。事件通过 onEvent 回调推送。
//
// 生命周期：
//  1. 建工作目录
//  2. 建 sidecar 会话
//  3. 后台 goroutine 订阅事件流并转发
//  4. 启动任务
//  5. 任务结束时清理
func (s *Service) StartTask(ctx context.Context, spec TaskSpec, onEvent func(TaskEvent)) (*TaskHandle, error) {
	if spec.Provider.BaseURL == "" || spec.Provider.APIKey == "" {
		return nil, fmt.Errorf("未配置模型服务，请先在设置中填写 API Key")
	}
	if len(spec.Stages) == 0 {
		return nil, fmt.Errorf("任务至少需要一个阶段")
	}

	// 恢复模式：复用旧工作区和旧 taskID
	var taskID string
	var workspaceDir string
	var err error

	if spec.ResumeFrom != nil && spec.ResumeFrom.TaskID != "" {
		taskID = spec.ResumeFrom.TaskID
		workspaceDir = spec.ResumeFrom.WorkspaceDir
		if workspaceDir == "" {
			return nil, fmt.Errorf("恢复任务缺少工作区路径")
		}
		// 校验目录还在（可能被清理任务回收了）
		if _, statErr := os.Stat(workspaceDir); statErr != nil {
			return nil, fmt.Errorf("恢复失败：工作区不存在（%s）", workspaceDir)
		}
		s.log.Info("从旧工作区恢复任务",
			slog.String("task_id", taskID),
			slog.String("workspace", workspaceDir),
		)
	} else {
		taskID = uuid.NewString()
		// 工作区是临时的：<wsRoot>/<taskID>/
		// 生命周期由 workspace.Manager 管，任务结束标记回收
		workspaceDir, err = s.ws.Create(taskID)
		if err != nil {
			return nil, fmt.Errorf("创建工作区失败: %w", err)
		}
	}

	// 关键时序：必须在创建会话之前把输入文件放进工作区，
	// 否则 Agent 第一轮读文件就会失败。
	//
	// 恢复模式下只补缺失的输入文件（如 tender.md 还可能被清理），
	// **不覆盖已有的产物**（sections/*.md、manifest）——
	// 覆盖就等于把上次的成果清零了。
	if err := s.prepareInputs(workspaceDir, spec, spec.ResumeFrom != nil); err != nil {
		return nil, fmt.Errorf("准备工作区文件失败: %w", err)
	}

	// 会话持久化目录（跨重启恢复用）
	// 放在工作区根目录下的 _sessions，与临时工作区区分开
	sessionsDir := filepath.Join(s.ws.Root(), "_sessions", spec.TenantID)

	summary := false
	created, err := s.client.CreateSession(ctx, CreateSessionRequest{
		WorkspaceDir:          workspaceDir,
		SessionsDir:           sessionsDir,
		Provider:              spec.Provider,
		ModelConfig:           spec.Model,
		WorkspaceInstructions: spec.WorkspaceInstructions,
		ActiveTools:           spec.ActiveTools,
		CustomTools:           spec.CustomTools,
		SummaryEnabled:        &summary,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Agent 会话失败: %w", err)
	}

	// 注册会话上下文：供工具回调反查
	s.registerSession(&SessionContext{
		TenantID:     spec.TenantID,
		ProjectID:    spec.ProjectID,
		UserID:       spec.OwnerID,
		TaskID:       taskID,
		SessionID:    created.SessionID,
		WorkspaceDir: workspaceDir,
	})

	// 持久化任务记录（失败不影响任务本身）
	if s.taskRuns != nil {
		if err := s.taskRuns.Create(ctx, TaskRunCreate{
			TenantID:     spec.TenantID,
			ProjectID:    spec.ProjectID,
			TaskID:       taskID,
			Kind:         spec.Type,
			SessionID:    created.SessionID,
			WorkspaceDir: workspaceDir,
			OwnerID:      spec.OwnerID,
		}); err != nil {
			s.log.Warn("写任务记录失败", slog.String("error", err.Error()))
		}
	}

	s.log.Info("Agent 会话已创建",
		slog.String("task_id", taskID),
		slog.String("session_id", created.SessionID),
		slog.String("type", spec.Type),
		slog.String("workspace", workspaceDir),
	)

	// 任务级 context：取消时同时停事件流和任务
	taskCtx, cancel := context.WithCancel(context.Background())
	if spec.Timeout > 0 {
		taskCtx, cancel = context.WithTimeout(context.Background(), spec.Timeout)
	}

	handle := &TaskHandle{
		TaskID:       taskID,
		SessionID:    created.SessionID,
		WorkspaceDir: workspaceDir,
		Cancel:       cancel,
	}
	s.manager.Register(taskID, cancel)

	// 任务成功标志：由事件流里的 task_end 设置
	var successFlag int32
	// 失败原因：从 stage_failed / task_end.error / 事件流异常里收集
	var failureReason string

	// 后台订阅事件流
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.client.StreamEvents(taskCtx, created.SessionID, func(raw json.RawMessage) {
			// 解析类型
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &head)

			// 转发给业务层
			if onEvent != nil {
				onEvent(TaskEvent{Type: head.Type, Data: raw})
			}

			// 任务结束：记录成败并触发清理
			if head.Type == "task_end" {
				var tail struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				}
				_ = json.Unmarshal(raw, &tail)
				if tail.Status == "success" {
					atomic.StoreInt32(&successFlag, 1)
				} else if tail.Error != "" && failureReason == "" {
					failureReason = truncateErr(tail.Error)
				}
				cancel()
			}

			// 提问：推给前端（SSE topic agent.question）
			//
			// 前端 AgentQuestionDialogProvider 订阅这个 topic，
			// 收到后弹窗让用户回答。
			if head.Type == "user_question" && s.bus != nil {
				var q struct {
					QuestionID    string   `json:"questionId"`
					SessionID     string   `json:"sessionId"`
					Question      string   `json:"question"`
					Options       []string `json:"options"`
					DefaultAnswer string   `json:"defaultAnswer"`
					Reason        string   `json:"reason"`
				}
				_ = json.Unmarshal(raw, &q)

				// 转成前端 AgentQuestion 结构
				opts := make([]map[string]any, 0, len(q.Options))
				for i, o := range q.Options {
					opts = append(opts, map[string]any{
						"id":          fmt.Sprintf("opt-%d", i+1),
						"label":       o,
						"description": "",
						"recommended": i == 0,
						"custom":      false,
					})
				}
				if len(opts) == 0 {
					opts = append(opts, map[string]any{
						"id": "custom", "label": "自行输入",
						"description": "", "recommended": false, "custom": true,
					})
				}

				s.bus.Publish("agent.question", EventScope{
					TenantID:  spec.TenantID,
					UserID:    spec.OwnerID,
					ProjectID: spec.ProjectID,
				}, map[string]any{
					"question_id": q.QuestionID,
					"task_id":     taskID,
					"session_id":  q.SessionID,
					"task_title":  spec.Type,
					"question":    q.Question,
					"options":     opts,
					"asked_at":    time.Now().Format(time.RFC3339),
					"is_primary":  true,
				})
			}

			// 阶段失败：记下原因
			if head.Type == "stage_failed" && failureReason == "" {
				var sf struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(raw, &sf)
				if sf.Error != "" {
					failureReason = truncateErr(sf.Error)
				}
			}

			// 空闲超时：记下原因
			if head.Type == "idle_timeout" && failureReason == "" {
				failureReason = "任务卡死（长时间无活动）"
			}
		})
		if err != nil && taskCtx.Err() == nil {
			if failureReason == "" {
				failureReason = truncateErr("事件流异常: " + err.Error())
			}
			s.log.Error("事件流异常",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()),
			)
			if onEvent != nil {
				errEvent, _ := json.Marshal(map[string]any{
					"type":  "stream_error",
					"error": err.Error(),
				})
				onEvent(TaskEvent{Type: "stream_error", Data: errEvent})
			}
		}
	}()

	// 启动任务
	runReq := RunRequest{
		TaskID: taskID,
		Stages: spec.Stages,
	}
	if spec.Timeout > 0 {
		runReq.TimeoutMs = spec.Timeout.Milliseconds()
	}

	if _, err := s.client.Run(ctx, created.SessionID, runReq); err != nil {
		cancel()
		s.manager.Unregister(taskID)
		// 清理已创建的会话
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = s.client.Delete(cleanupCtx, created.SessionID)
		return nil, fmt.Errorf("启动 Agent 任务失败: %w", err)
	}

	// 任务结束时：提交产物 → 清理会话 → 标记工作区可回收
	go func() {
		<-taskCtx.Done()
		wg.Wait()
		s.manager.Unregister(taskID)

		// 判断任务是否成功（由 onEvent 记录，见下方 succeeded 变量）
		succeeded := atomic.LoadInt32(&successFlag) == 1

		// 持久化任务终态（用独立 ctx，主 ctx 可能已取消）
		if s.taskRuns != nil {
			// 用真实失败原因，不要笼统写「任务失败」
			// 此时 wg.Wait() 已返回，failureReason 无并发写
			errMsg := failureReason
			if !succeeded && errMsg == "" {
				errMsg = "任务未成功完成（原因未知）"
			}
			s.taskRuns.Finish(context.Background(), taskID, succeeded, errMsg)
		}

		// 关键：先把产物提交到资产库，再清理。
		// 顺序不能反，否则产物就丢了。
		if spec.OnComplete != nil {
			commitCtx, commitCancel := context.WithTimeout(context.Background(), 3*time.Minute)
			if err := spec.OnComplete(commitCtx, workspaceDir, taskID, succeeded); err != nil {
				s.log.Error("提交产物失败",
					slog.String("task_id", taskID),
					slog.String("error", err.Error()),
				)
			}
			commitCancel()
		}

		// 清理 sidecar 会话
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := s.client.Delete(cleanupCtx, created.SessionID); err != nil {
			s.log.Warn("清理会话失败",
				slog.String("session_id", created.SessionID),
				slog.String("error", err.Error()),
			)
		}

		// 注销会话上下文
		s.unregisterSession(created.SessionID)

		// 标记工作区可回收（不立即删除，留时间排查）
		if err := s.ws.Release(taskID, succeeded); err != nil {
			s.log.Warn("标记工作区回收失败",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()),
			)
		}

		s.log.Info("Agent 任务已清理", slog.String("task_id", taskID))
	}()

	return handle, nil
}

// prepareInputs 把任务所需的输入文件写入工作区。
func (s *Service) prepareInputs(workspace string, spec TaskSpec, resume bool) error {
	for rel, src := range spec.PrepareFiles {
		dst := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}

		// 恢复模式：已存在的文件不覆盖。
		// tender.md 等输入文件如果还在，内容是一样的，不必重写；
		// 如果真的丢了（被清理），这里会补上。
		if resume {
			if _, statErr := os.Stat(dst); statErr == nil {
				continue
			}
		}

		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", dst, err)
		}
	}

	for rel, content := range spec.PrepareContent {
		dst := filepath.Join(workspace, rel)

		// 恢复模式：**绝不覆盖已存在的进度文件**。
		// content-manifest.json 记录着上次生成到哪一节，
		// 覆盖就等于把断点信息清零，恢复也就没意义了。
		if resume {
			if _, statErr := os.Stat(dst); statErr == nil {
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", dst, err)
		}
	}
	return nil
}

// CancelTask 取消任务。
func (s *Service) CancelTask(taskID string) bool {
	return s.manager.Cancel(taskID)
}

// Healthy 检查 sidecar 是否可用。
func (s *Service) Healthy(ctx context.Context) error {
	return s.client.Health(ctx)
}

// WorkspacePath 返回某任务的绝对工作目录。
func (s *Service) WorkspacePath(taskID string) string {
	return s.ws.Path(taskID)
}

// Workspaces 暴露工作区管理器（供清理任务使用）。
func (s *Service) Workspaces() *workspace.Manager { return s.ws }
