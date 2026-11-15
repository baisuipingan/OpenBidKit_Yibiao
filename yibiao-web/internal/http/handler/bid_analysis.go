package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/bidanalysis"
)

// BidAnalysis 处理招标文件分析接口。
//
// 这是第一个真实的标书垂类 Agent 功能，验证整条链路：
//
//	前端 → Go → Agent sidecar → 模型 → 工具 → 产物 → 前端
type BidAnalysis struct {
	runner  *bidanalysis.Runner
	metrics *service.UsageRecorder
	// toolDecls 由上层注入，避免 handler 依赖工具注册表
	toolDecls []any

	// 任务事件缓冲：taskID → 事件列表
	// 用途：前端断线重连时能回放；任务结束后短期内仍可查
	mu    sync.RWMutex
	tasks map[string]*taskState
}

type taskState struct {
	TaskID    string
	Status    string
	Events    []json.RawMessage
	StartedAt time.Time
	Done      bool
	Result    *bidanalysis.Result
}

// NewBidAnalysis 创建 handler。
func NewBidAnalysis(runner *bidanalysis.Runner, metrics *service.UsageRecorder) *BidAnalysis {
	return &BidAnalysis{
		runner:  runner,
		metrics: metrics,
		tasks:   make(map[string]*taskState),
	}
}

// SetTools 注入可用工具声明。由路由装配时调用。
func (h *BidAnalysis) SetTools(decls []any) {
	h.toolDecls = decls
}

// Register 注册路由。
func (h *BidAnalysis) Register(g *echo.Group) {
	g.POST("/bid-analysis/start", h.Start)
	g.POST("/bid-analysis/events", h.StreamEvents)
	g.POST("/bid-analysis/status", h.Status)
	g.POST("/bid-analysis/items", h.ListItems)
}

// StartRequest 是启动分析的请求。
type StartRequest struct {
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// TenderMarkdownPath 招标文件 Markdown 路径（服务端路径）
	TenderMarkdownPath string `json:"tenderMarkdownPath"`
	// Items 自定义解析项，不传用默认 18 项
	Items []bidanalysis.Item `json:"items"`
}

// Start 启动招标分析。
//
// 返回 taskId 后前端订阅 /bid-analysis/events 获取进度。
func (h *BidAnalysis) Start(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req StartRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	if req.Provider == nil || req.Provider.BaseURL == "" || req.Provider.APIKey == "" {
		return httpx.Fail(c, "MISSING_PROVIDER", "缺少模型配置（provider.baseUrl / apiKey / model）")
	}
	if req.TenderMarkdownPath == "" {
		return httpx.Fail(c, "MISSING_TENDER", "缺少招标文件路径（tenderMarkdownPath）")
	}

	// 事件记录：不依赖 taskID 从事件里反推（事件里的 taskId 由 sidecar 填，
	// 时序上可能晚于首个事件到达），改为按 runner 内部生成的 taskID 归档。
	//
	// 注意：runner.Start 是同步的，它在内部创建 Agent 任务后才返回 taskID，
	// 而事件是异步来的。所以这里先用一个「挂起缓冲」暂存，拿到 taskID 后归属。
	var (
		pendingMu  sync.Mutex
		pendingEvs []json.RawMessage
		ownerID    string
	)

	record := func(taskID string, raw json.RawMessage) {
		h.mu.Lock()
		defer h.mu.Unlock()

		state, ok := h.tasks[taskID]
		if !ok {
			state = &taskState{TaskID: taskID, Status: "running", StartedAt: time.Now()}
			h.tasks[taskID] = state
		}
		state.Events = append(state.Events, raw)
		if len(state.Events) > 1500 {
			state.Events = state.Events[len(state.Events)-1000:]
		}

		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &head)
		if head.Type == "task_end" {
			state.Done = true
			state.Status = "finished"
		}
	}

	onEvent := func(ev agent.TaskEvent) {
		pendingMu.Lock()
		if ownerID == "" {
			// 还没拿到 taskID，先暂存
			pendingEvs = append(pendingEvs, ev.Data)
			pendingMu.Unlock()
			return
		}
		id := ownerID
		pendingMu.Unlock()

		record(id, ev.Data)
	}

	result, err := h.runner.Start(c.Request().Context(), bidanalysis.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: req.TenderMarkdownPath,
		Provider: agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		},
		Model:     agent.ModelConfig{ContextLengthLimit: 128000},
		Items:     req.Items,
		ToolDecls: h.toolDecls,
		OnEvent:   onEvent,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	// 创建任务状态并回灌暂存事件
	h.mu.Lock()
	h.tasks[result.TaskID] = &taskState{
		TaskID:    result.TaskID,
		Status:    "running",
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	pendingMu.Lock()
	ownerID = result.TaskID
	buffered := pendingEvs
	pendingEvs = nil
	pendingMu.Unlock()

	for _, raw := range buffered {
		record(result.TaskID, raw)
	}

	return httpx.OK(c, map[string]any{
		"taskId":    result.TaskID,
		"itemCount": len(req.Items),
	})
}

// StreamEventsRequest 是订阅事件的请求。
type StreamEventsRequest struct {
	TaskID string `json:"taskId"`
	// Since 从第几个事件开始（断线重连用）
	Since int `json:"since"`
}

// StreamEvents 以 SSE 推送任务事件。
func (h *BidAnalysis) StreamEvents(c echo.Context) error {
	var req StreamEventsRequest
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
	ticker := time.NewTicker(500 * time.Millisecond)
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
			h.mu.RLock()
			state, ok := h.tasks[req.TaskID]
			if !ok {
				h.mu.RUnlock()
				writeSSE("error", map[string]string{"message": "任务不存在"})
				return nil
			}
			// 复制待发事件
			var pending []json.RawMessage
			if sent < len(state.Events) {
				pending = append(pending, state.Events[sent:]...)
			}
			done := state.Done
			h.mu.RUnlock()

			for _, ev := range pending {
				writeSSE("agent", json.RawMessage(ev))
				sent++
			}

			if done {
				writeSSE("done", map[string]any{"eventCount": sent})
				return nil
			}
		}
	}
}

// StatusRequest 是查询任务状态的请求。
type StatusRequest struct {
	TaskID string `json:"taskId"`
}

// Status 查询任务状态。
func (h *BidAnalysis) Status(c echo.Context) error {
	var req StatusRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	h.mu.RLock()
	state, ok := h.tasks[req.TaskID]
	if ok {
		defer h.mu.RUnlock()
		return httpx.OK(c, map[string]any{
			"taskId":     state.TaskID,
			"status":     state.Status,
			"eventCount": len(state.Events),
			"done":       state.Done,
			"startedAt":  state.StartedAt,
		})
	}
	h.mu.RUnlock()

	return httpx.Fail(c, "TASK_NOT_FOUND", "任务不存在")
}

// ListItems 返回默认解析项清单（供前端展示）。
func (h *BidAnalysis) ListItems(c echo.Context) error {
	items := bidanalysis.DefaultItems
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"id":          it.ID,
			"label":       it.Label,
			"output":      it.Output,
			"required":    it.Required,
			"description": it.Description,
		})
	}
	return httpx.OK(c, map[string]any{
		"items": out,
		"total": len(out),
	})
}

// 确保 context 包被使用（SSE 里用到 Request().Context()）
var _ = context.Background
