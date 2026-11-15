package handler

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/events"
	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/bidanalysis"
	"github.com/patrick/yibiao-web/internal/service/agent/content"
	"github.com/patrick/yibiao-web/internal/service/agent/outline"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/settings"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Task 实现前端 `tasks.*` 命名空间的接口。
//
// 设计背景：
//
//	桌面版的前端调用 `tasks.startOutlineGeneration()` 启动任务，
//	然后订阅 `tasks.onTaskEvent()` 接收进度。
//	Web 版必须提供同名接口，前端才不用改。
//
// 职责：
//
//	把桥接层（前端熟悉的 tasks.* 语义）翻译成具体的 Agent 任务。
type Task struct {
	planSvc   *service.TechnicalPlanService
	outline   *outline.Runner
	bidRunner *bidanalysis.Runner
	content   *content.Runner
	tpSvc     *service.TechnicalPlanService
	settings  *settings.Service
	artifacts *artifact.Service
	bus       *events.Bus
	logger    *slog.Logger
	toolDecls []any

	mu    sync.RWMutex
	tasks map[string]*taskRecord
}

// taskRecord 记录一个运行中的任务。
type taskRecord struct {
	TaskID    string
	Type      string
	Status    string
	Events    []json.RawMessage
	StartedAt time.Time
	Done      bool
	Error     string
	// Result 是任务产物的结构化结果（如目录节点）
	Result any
}

// NewTask 创建 handler。
func NewTask(
	planSvc *service.TechnicalPlanService,
	outlineRunner *outline.Runner,
	bidRunner *bidanalysis.Runner,
	contentRunner *content.Runner,
	settingsSvc *settings.Service,
	artifactSvc *artifact.Service,
	bus *events.Bus,
) *Task {
	return &Task{
		planSvc:   planSvc,
		outline:   outlineRunner,
		bidRunner: bidRunner,
		content:   contentRunner,
		tpSvc:     planSvc,
		settings:  settingsSvc,
		artifacts: artifactSvc,
		bus:       bus,
		logger:    slog.Default(),
		tasks:     make(map[string]*taskRecord),
	}
}

// SetTools 注入工具声明。
func (t *Task) SetTools(decls []any) {
	t.toolDecls = decls
}

// Register 注册路由。
func (t *Task) Register(g *echo.Group) {
	g.POST("/tasks/start-outline-generation", t.StartOutlineGeneration)
	g.POST("/tasks/start-bid-analysis", t.StartBidAnalysis)
	g.POST("/tasks/start-content-generation", t.StartContentGeneration)
	// 手动重同步：把某次任务的产物重新回写进 meta。
	// 用途：产物已入库但 meta 没同步（或改了映射规则要重跑）时，
	// 不用重跑 AI（省钱省时间）。
	g.POST("/tasks/resync-analysis", t.ResyncAnalysis)
	g.POST("/tasks/resync-outline", t.ResyncOutline)
	g.POST("/tasks/resync-content", t.ResyncContent)
	g.POST("/tasks/get-active", t.GetActive)
	// 前端（桌面版命名）用的是 get-active-tasks，这里做别名
	g.POST("/tasks/get-active-tasks", t.GetActive)
	g.POST("/tasks/events", t.Events)
}

// startOutlineRequest 是前端传来的参数。
//
// 字段名与桌面版一致（snake_case），前端零改动。
type startOutlineRequest struct {
	// NoTechnicalScoreMode 为 true 时不按技术评分分组
	NoTechnicalScoreMode bool `json:"no_technical_score_mode"`
	// Provider 模型配置（Web 版新增，桌面版从配置读）
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// TenderMarkdownPath 招标文件路径（Web 版新增）
	// 不传时从资产库取最新的 tender
	TenderMarkdownPath string `json:"tenderMarkdownPath"`
	MaxLevels          int    `json:"maxLevels"`
}

// StartOutlineGeneration 启动目录生成任务。
//
// 对应前端 `window.yibiao.tasks.startOutlineGeneration()`。
func (t *Task) StartOutlineGeneration(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req startOutlineRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 模型配置：优先用请求里的，否则从租户配置读。
	// 前端调用 tasks.startOutlineGeneration() 时不会传 provider
	// （桌面版从本地配置读），Web 版必须由服务端解决。
	var provider agent.Provider
	var modelCfg agent.ModelConfig

	if req.Provider != nil && req.Provider.BaseURL != "" && req.Provider.APIKey != "" {
		provider = agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		}
		modelCfg = agent.ModelConfig{ContextLengthLimit: 128000}
	} else {
		p, mc, err := t.settings.Provider(c.Request().Context(), tenantID)
		if err != nil {
			return httpx.Fail(c, "MISSING_PROVIDER", err.Error())
		}
		provider, modelCfg = p, mc
	}

	// 招标文件：物化到临时文件（agent 需要真实路径）。
	// 前端不传路径时，从资产库取出 tender.md 写临时文件。
	tenderPath, cleanup, err := t.materializeTenderMarkdown(c, tenantID, projectID, req.TenderMarkdownPath)
	if err != nil {
		return err
	}
	defer cleanup()

	// 检查是否已有运行中的目录任务
	t.mu.RLock()
	for _, rec := range t.tasks {
		if rec.Type == "outline-generation" && !rec.Done {
			t.mu.RUnlock()
			return httpx.Conflict("TASK_RUNNING", "已有目录生成任务在运行")
		}
	}
	t.mu.RUnlock()

	// 收集事件
	var (
		mu     sync.Mutex
		events []json.RawMessage
		owner  string
	)

	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		events = append(events, ev.Data)
		if len(events) > 1500 {
			events = events[len(events)-1000:]
		}
		tid := owner
		mu.Unlock()
		if tid == "" {
			return
		}
		// 原始事件附带 technicalPlanPatch，否则 TechnicalPlanHome 会直接忽略
		t.publishTask(tenantID, userID, projectID, map[string]any{
			"task": map[string]any{
				"task_id":  tid,
				"type":     "outline-generation",
				"status":   "running",
				"progress": 15,
			},
			"technicalPlanPatch": map[string]any{
				"outlineGenerationTask": map[string]any{
					"task_id":  tid,
					"type":     "outline-generation",
					"status":   "running",
					"progress": 15,
					"logs":     []string{"目录生成进行中"},
				},
			},
			"agentEvent": json.RawMessage(ev.Data),
		})
	}

	result, err := t.outline.Start(c.Request().Context(), outline.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: tenderPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          t.toolDecls,
		MaxLevels:          req.MaxLevels,
		OnEvent:            onEvent,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	mu.Lock()
	owner = result.TaskID
	buffered := append([]json.RawMessage(nil), events...)
	mu.Unlock()

	t.mu.Lock()
	t.tasks[owner] = &taskRecord{
		TaskID:    owner,
		Type:      "outline-generation",
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	t.mu.Unlock()

	if persistErr := t.tpSvc.PersistOutlineGenerationStart(
		c.Request().Context(), tenantID, projectID, result.TaskID,
	); persistErr != nil && t.logger != nil {
		t.logger.Warn("写入目录启动状态失败: " + persistErr.Error())
	}

	// 同步更新技术方案步骤，让前端知道进入了目录阶段
	if err := t.planSvc.UpdateStep(c.Request().Context(), tenantID, projectID, "outline-generation"); err != nil {
		// 步骤更新失败不影响任务本身
		c.Logger().Warnf("更新步骤失败: %v", err)
	}

	runningTask := map[string]any{
		"task_id":  result.TaskID,
		"type":     "outline-generation",
		"status":   "running",
		"progress": 5,
		"logs":     []string{"目录生成任务已启动"},
	}
	t.publishTask(tenantID, userID, projectID, map[string]any{
		"task": runningTask,
		"technicalPlanPatch": map[string]any{
			"outlineGenerationTask": runningTask,
		},
	})

	return httpx.OK(c, map[string]any{
		"taskId": result.TaskID,
		"type":   "outline-generation",
		"status": "running",
	})
}

// GetActive 返回运行中的任务。
//
// 对应前端 `window.yibiao.tasks.getActiveTasks()`。
// 前端页面挂载时先读快照，再订阅事件，最后再调这个接口回放活动任务
// （见 AGENTS.md 的任务恢复约定）。
func (t *Task) GetActive(c echo.Context) error {
	_, projectID, err := t.scopeOf(c)
	if err != nil {
		return err
	}
	_ = projectID

	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]map[string]any, 0, 4)
	for _, rec := range t.tasks {
		if rec.Done {
			continue
		}
		out = append(out, map[string]any{
			"task_id":    rec.TaskID,
			"type":       rec.Type,
			"status":     rec.Status,
			"started_at": rec.StartedAt,
		})
	}
	return httpx.OK(c, out)
}

// EventsRequest 是订阅任务的请求。
type taskEventsRequest struct {
	TaskID string `json:"taskId"`
	Since  int    `json:"since"`
}

// Events 推送任务事件（SSE）。
//
// 对应前端 `window.yibiao.tasks.onTaskEvent()`。
func (t *Task) Events(c echo.Context) error {
	var req taskEventsRequest
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
			t.mu.Lock()
			rec, ok := t.tasks[req.TaskID]
			if !ok {
				t.mu.Unlock()
				writeSSE("error", map[string]string{"message": "任务不存在"})
				return nil
			}

			var pending []json.RawMessage
			if sent < len(rec.Events) {
				pending = append(pending, rec.Events[sent:]...)
			}
			done := rec.Done
			status := rec.Status
			errMsg := rec.Error
			t.mu.Unlock()

			for _, ev := range pending {
				writeSSE("task", json.RawMessage(ev))
				sent++

				// task_end 时标记完成
				var head struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				}
				if json.Unmarshal(ev, &head) == nil && head.Type == "task_end" {
					t.MarkDone(req.TaskID, head.Status)
				}
			}

			if done {
				payload := map[string]any{"status": status, "eventCount": sent}
				if errMsg != "" {
					payload["error"] = errMsg
				}
				writeSSE("done", payload)
				return nil
			}
		}
	}
}

// MarkDone 标记内存任务完成。
//
// 必须在 runner 完成回调里调用，不能只依赖 /tasks/events SSE：
// 没有订阅者或未收到 task_end 时，findRunning 会一直把已结束任务当成 running，
// 导致下一次 start-* 误报 TASK_RUNNING。
func (t *Task) MarkDone(taskID, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.tasks[taskID]
	if rec == nil || rec.Done {
		return
	}
	rec.Done = true
	if status == "success" {
		rec.Status = "success"
	} else {
		rec.Status = "error"
	}
}

// scopeOf 取当前租户与项目。
func (t *Task) scopeOf(c echo.Context) (string, string, error) {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	return tenantID, projectID, err
}

// startBidAnalysisRequest 对齐桌面版 tasks.startBidAnalysis 载荷。
type startBidAnalysisRequest struct {
	Mode               string   `json:"mode"`
	SelectedTaskIDs    []string `json:"selected_task_ids"`
	TaskIDs            []string `json:"task_ids"`
	ForceRerun         bool     `json:"force_rerun"`
	TenderMarkdownPath string   `json:"tenderMarkdownPath"`
	Items              []bidanalysis.Item `json:"items"`
	Provider           *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
}

// StartBidAnalysis 启动招标文件分析任务。
//
// 对应前端 `tasks.startBidAnalysis()`。
// 产出：项目概况、技术要求、评分项等结构化要点，落进 technical_plan_meta。
func (t *Task) StartBidAnalysis(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req startBidAnalysisRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 模型配置：同 StartOutlineGeneration，一次 Bind，避免 Echo 二次读 body。
	var provider agent.Provider
	var modelCfg agent.ModelConfig
	if req.Provider != nil && req.Provider.BaseURL != "" && req.Provider.APIKey != "" {
		provider = agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		}
		modelCfg = agent.ModelConfig{ContextLengthLimit: 128000}
	} else {
		p, mc, pErr := t.settings.Provider(c.Request().Context(), tenantID)
		if pErr != nil {
			return httpx.Fail(c, "MISSING_PROVIDER", pErr.Error())
		}
		provider, modelCfg = p, mc
	}

	// 招标文件：物化到临时文件（agent 需要真实路径）
	tenderPath, cleanup, err := t.materializeTenderMarkdown(c, tenantID, projectID, req.TenderMarkdownPath)
	if err != nil {
		return err
	}
	defer cleanup()

	// 已有任务在跑就拒绝（避免重复消耗 token）
	if running := t.findRunning("bid-analysis"); running != "" {
		return httpx.Conflict("TASK_RUNNING", "已有招标分析任务在运行")
	}

	items := req.Items
	if len(items) == 0 {
		items = selectBidAnalysisItems(req.SelectedTaskIDs, req.TaskIDs)
	}

	uiItems := make([]map[string]any, 0, len(req.SelectedTaskIDs)+len(items))
	seenUI := map[string]bool{}
	for _, id := range append(append([]string{}, req.SelectedTaskIDs...), req.TaskIDs...) {
		if id == "" || seenUI[id] {
			continue
		}
		seenUI[id] = true
		uiItems = append(uiItems, map[string]any{
			"id":    id,
			"label": bidAnalysisUILabel(id),
		})
	}
	if len(uiItems) == 0 {
		for _, it := range items {
			uiItems = append(uiItems, map[string]any{"id": it.ID, "label": it.Label})
		}
	}

	var (
		mu       sync.Mutex
		evBuffer []json.RawMessage
	)
	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		evBuffer = append(evBuffer, ev.Data)
		if len(evBuffer) > 1500 {
			evBuffer = evBuffer[len(evBuffer)-1000:]
		}
		// 原始 sidecar 事件也转发给订阅了 task 的客户端（诊断用）；
		// 业务进度主要靠 technicalPlanPatch。
		t.publishTask(tenantID, userID, projectID, map[string]any{
			"task": map[string]any{
				"type":   "bid-analysis",
				"status": "running",
			},
			"agentEvent": json.RawMessage(ev.Data),
		})
	}

	result, err := t.bidRunner.Start(c.Request().Context(), bidanalysis.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: tenderPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          t.toolDecls,
		Items:              items,
		OnEvent:            onEvent,
	})
	if err != nil {
		return httpx.Fail(c, "TASK_FAILED", err.Error())
	}

	mu.Lock()
	buffered := append([]json.RawMessage(nil), evBuffer...)
	mu.Unlock()
	t.mu.Lock()
	t.tasks[result.TaskID] = &taskRecord{
		TaskID:    result.TaskID,
		Type:      "bid-analysis",
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	t.mu.Unlock()

	selectedIDs := req.SelectedTaskIDs
	if len(selectedIDs) == 0 {
		for _, it := range uiItems {
			if id, _ := it["id"].(string); id != "" {
				selectedIDs = append(selectedIDs, id)
			}
		}
	}
	if persistErr := t.tpSvc.PersistBidAnalysisStart(
		c.Request().Context(), tenantID, projectID, result.TaskID, req.Mode, selectedIDs, uiItems,
	); persistErr != nil && t.logger != nil {
		t.logger.Warn("写入招标分析启动状态失败: " + persistErr.Error())
	}

	pendingTasks := map[string]any{}
	for _, it := range uiItems {
		id, _ := it["id"].(string)
		pendingTasks[id] = map[string]any{
			"id": id, "label": it["label"], "status": "pending", "content": "",
		}
	}
	t.publishTask(tenantID, userID, projectID, map[string]any{
		"task": map[string]any{
			"task_id": result.TaskID,
			"type":    "bid-analysis",
			"status":  "running",
			"progress": 0,
		},
		"technicalPlanPatch": map[string]any{
			"bidAnalysisTask": map[string]any{
				"task_id": result.TaskID,
				"type":    "bid-analysis",
				"status":  "running",
				"progress": 0,
			},
			"bidAnalysisTasks":    pendingTasks,
			"bidAnalysisProgress": 0,
			"bidAnalysisMode":     req.Mode,
			"bidAnalysisSelectedTaskIds": selectedIDs,
		},
	})

	return httpx.OK(c, map[string]any{
		"success": true,
		"taskId":  result.TaskID,
		"type":    "bid-analysis",
		"status":  "running",
	})
}

// publishTask 推送桌面形状的 tasks.onTaskEvent 载荷。
func (t *Task) publishTask(tenantID, userID, projectID string, payload map[string]any) {
	if t.bus == nil {
		return
	}
	t.bus.Publish("task", events.Scope{
		TenantID:  tenantID,
		UserID:    userID,
		ProjectID: projectID,
	}, payload)
}

// selectBidAnalysisItems 把前端 selected_task_ids 映射成 Agent 解析项。
func selectBidAnalysisItems(selected, taskIDs []string) []bidanalysis.Item {
	ids := append([]string{}, selected...)
	ids = append(ids, taskIDs...)
	if len(ids) == 0 {
		return bidanalysis.DefaultItems
	}

	// 前端别名 → Agent 文件名
	alias := map[string]string{
		"projectOverview":  "projectBackground",
		"techRequirements": "technicalRequirements",
		"businessScoring":  "scoringCriteria",
		"discardedBids":    "invalidBidAndRejectionItems",
	}
	want := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		want[id] = true
		if mapped, ok := alias[id]; ok {
			want[mapped] = true
		}
	}

	out := make([]bidanalysis.Item, 0, len(want))
	for _, item := range bidanalysis.DefaultItems {
		if want[item.ID] {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return bidanalysis.DefaultItems
	}
	return out
}

func bidAnalysisUILabel(id string) string {
	labels := map[string]string{
		"projectOverview":                "项目概述",
		"techRequirements":               "技术评分要求",
		"projectInfo":                    "项目信息",
		"partAInfo":                      "甲方信息",
		"deliveryAndServiceRequirements": "交货和服务要求",
		"responseFileRequirements":       "响应文件要求",
		"scoringCriteria":                "评分标准",
		"technicalRequirements":          "技术需求",
		"invalidBidAndRejectionItems":    "无效投标与废标项",
		"qualificationRequirements":      "资质要求",
		"businessScoring":                "商务评分要求",
		"discardedBids":                  "废标项",
	}
	if v := labels[id]; v != "" {
		return v
	}
	return id
}

// StartContentGeneration 启动正文生成任务。
//
// 对应前端 `tasks.startContentGeneration()`。
// 按目录叶子节点逐节生成，结果写进 technical_plan_outline_nodes.content。
func (t *Task) StartContentGeneration(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req struct {
		TenderMarkdownPath string            `json:"tenderMarkdownPath"`
		Sections           []content.Section `json:"sections"`
		GlobalFacts        string            `json:"globalFacts"`
		MinWords           int               `json:"minWords"`
	}
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	provider, modelCfg, err := t.resolveProvider(c, tenantID)
	if err != nil {
		return err
	}

	// 招标文件物化（正文生成也要读招标要求）
	tenderPath, cleanup, err := t.materializeTenderMarkdown(c, tenantID, projectID, req.TenderMarkdownPath)
	if err != nil {
		return err
	}
	defer cleanup()

	// 未传 sections 时从目录树取叶子节点
	sections := req.Sections
	if len(sections) == 0 {
		state, loadErr := t.tpSvc.LoadState(c.Request().Context(), tenantID, projectID)
		if loadErr != nil {
			return httpx.Internal(loadErr)
		}
		if len(state.Outline) == 0 {
			return httpx.Fail(c, "NO_OUTLINE", "请先生成目录")
		}
		for _, node := range state.Outline {
			// 只取叶子（没有子节点的）
			if nodeHasChildren(state.Outline, node.NodeID) {
				continue
			}
			sections = append(sections, content.Section{
				NodeID: node.NodeID,
				Title:  node.Title,
			})
		}
	}

	if len(sections) == 0 {
		return httpx.Fail(c, "NO_SECTIONS", "没有可生成的章节")
	}

	if running := t.findRunning("content-generation"); running != "" {
		return httpx.Conflict("TASK_RUNNING", "已有正文生成任务在运行")
	}

	var (
		mu     sync.Mutex
		events []json.RawMessage
	)
	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.Data)
		if len(events) > 1500 {
			events = events[len(events)-1000:]
		}
	}

	result, err := t.content.Start(c.Request().Context(), content.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: tenderPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          t.toolDecls,
		Sections:           sections,
		GlobalFacts:        req.GlobalFacts,
		MinWords:           req.MinWords,
		OnEvent:            onEvent,
	})
	if err != nil {
		return httpx.Fail(c, "TASK_FAILED", err.Error())
	}

	mu.Lock()
	buffered := append([]json.RawMessage(nil), events...)
	mu.Unlock()
	t.mu.Lock()
	t.tasks[result.TaskID] = &taskRecord{
		TaskID:    result.TaskID,
		Type:      "content-generation",
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	t.mu.Unlock()

	runningTask := map[string]any{
		"task_id":  result.TaskID,
		"type":     "content-generation",
		"status":   "running",
		"progress": 5,
		"logs":     []string{"正文生成任务已启动"},
	}
	if persistErr := t.tpSvc.PersistContentGenerationStart(
		c.Request().Context(), tenantID, projectID, result.TaskID,
	); persistErr != nil && t.logger != nil {
		t.logger.Warn("写入正文启动状态失败: " + persistErr.Error())
	}
	t.publishTask(tenantID, userID, projectID, map[string]any{
		"task": runningTask,
		"technicalPlanPatch": map[string]any{
			"contentGenerationTask": runningTask,
		},
	})

	return httpx.OK(c, map[string]any{
		"success":      true,
		"taskId":       result.TaskID,
		"sectionCount": result.SectionCount,
		"type":         "content-generation",
		"status":       "running",
	})
}

// materializeTenderMarkdown 把招标文件 markdown 落到临时文件，返回路径。
//
// 为什么需要：agent 层用 os.Stat / os.ReadFile 直接读文件，
// 而 Web 版的 markdown 存在 artifact 存储里（数据库/对象存储）。
// 两者之间必须有一次「物化」。
//
// 调用方负责删除临时文件。
func (t *Task) materializeTenderMarkdown(c echo.Context, tenantID, projectID, explicit string) (string, func(), error) {
	noop := func() {}

	// 已给路径且文件存在 → 直接用
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, noop, nil
		}
	}

	if t.artifacts == nil {
		return "", noop, httpx.Fail(c, "MISSING_TENDER", "缺少招标文件路径")
	}

	// 从 artifact 取最新的 tender.md
	scope := storage.Scope{TenantID: tenantID, ProjectID: projectID}
	rc, _, err := t.artifacts.Get(c.Request().Context(), scope, "tender.md", 0)
	if err != nil {
		return "", noop, httpx.Fail(c, "MISSING_TENDER", "未找到招标文件，请先导入")
	}
	defer func() { _ = rc.Close() }()

	dir, err := os.MkdirTemp("", "yb-tender-*")
	if err != nil {
		return "", noop, httpx.Internal(err)
	}

	dst := filepath.Join(dir, "tender.md")
	f, err := os.Create(dst)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", noop, httpx.Internal(err)
	}

	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(dir)
		return "", noop, httpx.Internal(err)
	}
	_ = f.Close()

	cleanup := func() { _ = os.RemoveAll(dir) }
	return dst, cleanup, nil
}

// resolveProvider 取模型配置：请求里带就用，否则从租户配置读。
func (t *Task) resolveProvider(c echo.Context, tenantID string) (agent.Provider, agent.ModelConfig, error) {
	var req struct {
		Provider *struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		} `json:"provider"`
	}
	_ = c.Bind(&req) // 失败也无所谓，用租户配置

	if req.Provider != nil && req.Provider.BaseURL != "" && req.Provider.APIKey != "" {
		return agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		}, agent.ModelConfig{ContextLengthLimit: 128000}, nil
	}

	p, mc, err := t.settings.Provider(c.Request().Context(), tenantID)
	if err != nil {
		return agent.Provider{}, agent.ModelConfig{}, httpx.Fail(c, "MISSING_PROVIDER", err.Error())
	}
	return p, mc, nil
}

// findRunning 返回运行中任务的 ID（没有则空串）。
func (t *Task) findRunning(taskType string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for id, rec := range t.tasks {
		if rec.Type == taskType && !rec.Done {
			return id
		}
	}
	return ""
}

// nodeHasChildren 判断节点是否有子节点。
func nodeHasChildren(all []service.OutlineNode, nodeID string) bool {
	for _, n := range all {
		if n.ParentNodeID != nil && *n.ParentNodeID == nodeID {
			return true
		}
	}
	return false
}

// ResyncAnalysis 手动把任务产物重新回写进 meta。
//
// 场景：产物已经在资产库里，但因为映射规则调整或回写时机错过，
// meta 里没有数据。重跑 AI 既慢又花钱，直接重放回写即可。
func (t *Task) ResyncAnalysis(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req struct {
		TaskID string `json:"taskId"`
	}
	_ = c.Bind(&req)

	// 没指定 taskId 时，用资产库里最近一次 bid-items 任务的
	taskID := req.TaskID
	if taskID == "" {
		taskID = t.latestBidAnalysisTask(c, tenantID)
		if taskID == "" {
			return httpx.Fail(c, "NO_TASK", "找不到可用的分析任务")
		}
	}

	n, syncErr := t.tpSvc.SyncBidAnalysisFromArtifacts(
		c.Request().Context(), tenantID, projectID, taskID, t.artifacts, t.logger,
	)
	if syncErr != nil {
		return httpx.Internal(syncErr)
	}

	return httpx.OK(c, map[string]any{
		"success":   true,
		"taskId":    taskID,
		"itemCount": n,
		"message":   fmt.Sprintf("已回写 %d 项分析结果", n),
	})
}

// latestBidAnalysisTask 找最近一次有产物的分析任务。
func (t *Task) latestBidAnalysisTask(c echo.Context, tenantID string) string {
	if t.artifacts == nil {
		return ""
	}
	// 资产表里 kind=bid-items 的 task_id，按时间倒序第一条
	ids, err := t.artifacts.LatestTaskIDs(c.Request().Context(), tenantID, "bid-items", 10)
	if err != nil || len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// ResyncOutline 手动把目录产物重新写进节点表。
//
// 同 ResyncAnalysis：产物已在资产库，只是没进节点表时，
// 重放回写即可，不用重跑 AI。
func (t *Task) ResyncOutline(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req struct {
		TaskID string `json:"taskId"`
	}
	_ = c.Bind(&req)

	taskID := req.TaskID
	if taskID == "" {
		ids, listErr := t.artifacts.LatestTaskIDs(c.Request().Context(), tenantID, "outline", 10)
		if listErr != nil || len(ids) == 0 {
			return httpx.Fail(c, "NO_TASK", "找不到可用的目录任务")
		}
		taskID = ids[0]
	}

	n, syncErr := t.tpSvc.SyncOutlineFromArtifacts(
		c.Request().Context(), tenantID, projectID, taskID, t.artifacts, t.logger,
	)
	if syncErr != nil {
		return httpx.Internal(syncErr)
	}

	return httpx.OK(c, map[string]any{
		"success":   true,
		"taskId":    taskID,
		"nodeCount": n,
		"message":   fmt.Sprintf("已回写 %d 个目录节点", n),
	})
}

// ResyncContent 手动把正文产物重新写进节点表。
func (t *Task) ResyncContent(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req struct {
		TaskID string `json:"taskId"`
	}
	_ = c.Bind(&req)

	taskID := req.TaskID
	if taskID == "" {
		ids, listErr := t.artifacts.LatestTaskIDs(c.Request().Context(), tenantID, "content", 10)
		if listErr != nil || len(ids) == 0 {
			return httpx.Fail(c, "NO_TASK", "找不到可用的正文任务")
		}
		taskID = ids[0]
	}

	n, syncErr := t.tpSvc.SyncContentFromArtifacts(
		c.Request().Context(), tenantID, projectID, taskID, t.artifacts, t.logger,
	)
	if syncErr != nil {
		return httpx.Internal(syncErr)
	}

	return httpx.OK(c, map[string]any{
		"success": true,
		"taskId":  taskID,
		"written": n,
		"message": fmt.Sprintf("已回写 %d 个章节正文", n),
	})
}
