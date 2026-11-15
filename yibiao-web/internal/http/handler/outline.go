package handler

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/outline"
)

// Outline 处理目录生成接口。
type Outline struct {
	runner *outline.Runner
	// planSvc 用于把目录落库
	planSvc   *service.TechnicalPlanService
	toolDecls []any

	mu    sync.RWMutex
	tasks map[string]*outlineTaskState
}

type outlineTaskState struct {
	TaskID    string
	Status    string
	Events    []json.RawMessage
	Nodes     []*outline.Node
	Issues    []string
	StartedAt time.Time
	Done      bool
	Error     string
}

// NewOutline 创建 handler。
func NewOutline(runner *outline.Runner, planSvc *service.TechnicalPlanService) *Outline {
	return &Outline{runner: runner, planSvc: planSvc, tasks: make(map[string]*outlineTaskState)}
}

// SetTools 注入工具声明。
func (h *Outline) SetTools(decls []any) {
	h.toolDecls = decls
}

// Register 注册路由。
func (h *Outline) Register(g *echo.Group) {
	g.POST("/outline/generate", h.Generate)
	g.POST("/outline/events", h.Events)
	g.POST("/outline/result", h.Result)
	g.POST("/outline/save", h.Save)
}

// GenerateRequest 是生成目录的请求。
type GenerateRequest struct {
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	TenderMarkdownPath string `json:"tenderMarkdownPath"`
	ProjectName        string `json:"projectName"`
	MaxLevels          int    `json:"maxLevels"`
}

// Generate 启动目录生成。
func (h *Outline) Generate(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req GenerateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.Provider == nil || req.Provider.BaseURL == "" || req.Provider.APIKey == "" {
		return httpx.Fail(c, "MISSING_PROVIDER", "缺少模型配置")
	}
	if req.TenderMarkdownPath == "" {
		return httpx.Fail(c, "MISSING_TENDER", "缺少招标文件路径")
	}

	// 收集事件
	var (
		mu     sync.Mutex
		events []json.RawMessage
		owner  string
	)

	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.Data)
		if len(events) > 1500 {
			events = events[len(events)-1000:]
		}
	}

	result, err := h.runner.Start(c.Request().Context(), outline.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: req.TenderMarkdownPath,
		Provider: agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		},
		Model:       agent.ModelConfig{ContextLengthLimit: 128000},
		ToolDecls:   h.toolDecls,
		ProjectName: req.ProjectName,
		MaxLevels:   req.MaxLevels,
		OnEvent:     onEvent,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	owner = result.TaskID
	mu.Lock()
	buffered := events
	mu.Unlock()

	h.mu.Lock()
	h.tasks[owner] = &outlineTaskState{
		TaskID:    owner,
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	return httpx.OK(c, map[string]any{
		"taskId": result.TaskID,
	})
}

// EventsRequest 是订阅事件的请求。
type EventsRequest struct {
	TaskID string `json:"taskId"`
	Since  int    `json:"since"`
}

// Events 以 SSE 推送目录生成进度。
func (h *Outline) Events(c echo.Context) error {
	var req EventsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.TaskID == "" {
		return httpx.BadRequest("缺少 taskId")
	}

	res := c.Response()
	res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	res.Header().Set("Cache-Control", "no-cache, no-transform")
	res.Header().Set("Connection", "keep-alive")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(200)

	writeSSE := func(event string, data any) {
		b, err := json.Marshal(data)
		if err != nil {
			b = []byte("{}")
		}
		fmt.Fprintf(res, "event: %s\ndata: %s\n\n", event, b)
		res.Flush()
	}

	sent := req.Since
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request().Context().Done():
			return nil

		case <-heartbeat.C:
			fmt.Fprint(res, ": ping\n\n")
			res.Flush()

		case <-ticker.C:
			h.mu.Lock()
			st, ok := h.tasks[req.TaskID]
			if !ok {
				h.mu.Unlock()
				writeSSE("error", map[string]string{"message": "任务不存在"})
				return nil
			}

			var pending []json.RawMessage
			if sent < len(st.Events) {
				pending = append(pending, st.Events[sent:]...)
			}
			done := st.Done
			status := st.Status
			errMsg := st.Error
			h.mu.Unlock()

			for _, ev := range pending {
				writeSSE("agent", json.RawMessage(ev))
				sent++

				// 检测 task_end 并解析产物
				var head struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(ev, &head) == nil && head.Type == "task_end" {
					h.finalizeTask(req.TaskID)
				}
			}

			if done {
				payload := map[string]any{
					"status":     status,
					"eventCount": sent,
				}
				if errMsg != "" {
					payload["error"] = errMsg
				}
				writeSSE("done", payload)
				return nil
			}
		}
	}
}

// finalizeTask 任务结束时解析产物。
func (h *Outline) finalizeTask(taskID string) {
	h.mu.RLock()
	st, ok := h.tasks[taskID]
	if !ok || st.Done {
		h.mu.RUnlock()
		return
	}
	h.mu.RUnlock()

	h.mu.Lock()
	defer h.mu.Unlock()

	st = h.tasks[taskID]
	if st == nil || st.Done {
		return
	}

	// 找 workspace 路径（从 session context 拿不到，用约定路径）
	// 简化：产物解析由 Result 接口按需触发
	st.Done = true
	st.Status = "finished"
}

// SaveRequest 是把目录落库的请求。
type SaveRequest struct {
	WorkspaceDir    string `json:"workspaceDir"`
	MaxLevels       int    `json:"maxLevels"`
	ProjectName     string `json:"projectName"`
	ProjectOverview string `json:"projectOverview"`
}

// Save 解析 Agent 产物并落库。
//
// 这是「Agent 产出 → 业务数据」的关键一步。
// 流程：读 outline.json → 分配稳定 ID → 写 technical_plan_outline_nodes
func (h *Outline) Save(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req SaveRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.WorkspaceDir == "" {
		return httpx.Fail(c, "MISSING_WORKSPACE", "需要 workspaceDir")
	}

	nodes, err := outline.ParseOutline(req.WorkspaceDir, req.MaxLevels)
	if err != nil {
		return httpx.Fail(c, "PARSE_FAILED", err.Error())
	}

	issues := outline.ValidateOutline(nodes)
	if len(issues) > 0 {
		return httpx.FailWithDetail(c, "INVALID_OUTLINE", "目录结构校验失败", issues)
	}

	if err := h.planSvc.SaveOutline(c.Request().Context(), tenantID, projectID, nodes, req.ProjectName, req.ProjectOverview); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]any{
		"nodeCount": outline.CountNodes(nodes),
		"saved":     true,
	})
}

// ResultRequest 是查询结果的请求。
type ResultRequest struct {
	TaskID string `json:"taskId"`
	// WorkspaceDir 可选：显式指定工作区（调试用）
	WorkspaceDir string `json:"workspaceDir"`
	MaxLevels    int    `json:"maxLevels"`
}

// Result 解析并返回目录。
func (h *Outline) Result(c echo.Context) error {
	var req ResultRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.WorkspaceDir == "" {
		return httpx.Fail(c, "MISSING_WORKSPACE", "需要 workspaceDir 才能解析产物")
	}

	nodes, err := outline.ParseOutline(req.WorkspaceDir, req.MaxLevels)
	if err != nil {
		return httpx.Fail(c, "PARSE_FAILED", err.Error())
	}

	issues := outline.ValidateOutline(nodes)

	return httpx.OK(c, map[string]any{
		"nodes":     nodes,
		"issues":    issues,
		"nodeCount": outline.CountNodes(nodes),
		"valid":     len(issues) == 0,
	})
}
