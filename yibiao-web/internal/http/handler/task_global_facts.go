package handler

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/globalfacts"
)

// startGlobalFactsRequest 对齐桌面 tasks.startGlobalFactsGeneration 载荷。
type startGlobalFactsRequest struct {
	GlobalFactsMode    string `json:"globalFactsMode"`
	GlobalFactsModeAlt string `json:"global_facts_mode"`
	TenderMarkdownPath string `json:"tenderMarkdownPath"`
	Provider           *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
}

// StartGlobalFactsGeneration 启动全局事实生成。
//
// 对应前端 `window.yibiao.tasks.startGlobalFactsGeneration({ globalFactsMode })`。
func (t *Task) StartGlobalFactsGeneration(c echo.Context) error {
	if t.globalFacts == nil {
		return httpx.Fail(c, "NOT_CONFIGURED", "全局事实任务未装配")
	}

	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req startGlobalFactsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	mode := req.GlobalFactsMode
	if mode == "" {
		mode = req.GlobalFactsModeAlt
	}

	state, loadErr := t.tpSvc.LoadState(c.Request().Context(), tenantID, projectID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}
	if mode == "" && state != nil && state.GlobalFactsMode != "" {
		mode = state.GlobalFactsMode
	}
	mode = globalfacts.NormalizeMode(mode)

	if state == nil || state.OutlineData == nil || len(state.OutlineData.Outline) == 0 {
		return httpx.Fail(c, "MISSING_OUTLINE", "请先生成目录，再生成全局事实")
	}

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

	tenderPath, cleanup, err := t.materializeTenderMarkdown(c, tenantID, projectID, req.TenderMarkdownPath)
	if err != nil {
		return err
	}
	defer cleanup()

	if running := t.findRunning(globalfacts.TaskType); running != "" {
		return httpx.Conflict("TASK_RUNNING", "已有全局事实生成任务在运行")
	}

	var (
		mu       sync.Mutex
		evBuffer []json.RawMessage
		owner    string
	)
	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		evBuffer = append(evBuffer, ev.Data)
		if len(evBuffer) > 1500 {
			evBuffer = evBuffer[len(evBuffer)-1000:]
		}
		tid := owner
		mu.Unlock()
		if tid == "" {
			return
		}
		t.publishTask(tenantID, userID, projectID, map[string]any{
			"task": map[string]any{
				"task_id":  tid,
				"type":     globalfacts.TaskType,
				"status":   "running",
				"progress": 15,
			},
			"technicalPlanPatch": map[string]any{
				"globalFactsTask": map[string]any{
					"task_id":  tid,
					"type":     globalfacts.TaskType,
					"status":   "running",
					"progress": 15,
					"logs":     []string{"全局事实生成进行中"},
				},
			},
			"agentEvent": json.RawMessage(ev.Data),
		})
	}

	result, err := t.globalFacts.Start(c.Request().Context(), globalfacts.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: tenderPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          t.toolDecls,
		GlobalFactsMode:    mode,
		PrepareContent:     service.BuildGlobalFactsPrepareContent(state),
		OnEvent:            onEvent,
	})
	if err != nil {
		return httpx.Fail(c, "TASK_FAILED", err.Error())
	}

	mu.Lock()
	owner = result.TaskID
	buffered := append([]json.RawMessage(nil), evBuffer...)
	mu.Unlock()

	t.mu.Lock()
	t.tasks[result.TaskID] = &taskRecord{
		TaskID:    result.TaskID,
		Type:      globalfacts.TaskType,
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	t.mu.Unlock()

	if persistErr := t.tpSvc.PersistGlobalFactsGenerationStart(
		c.Request().Context(), tenantID, projectID, result.TaskID, mode,
	); persistErr != nil && t.logger != nil {
		t.logger.Warn("写入全局事实启动状态失败: " + persistErr.Error())
	}

	if err := t.planSvc.UpdateStep(c.Request().Context(), tenantID, projectID, "global-facts"); err != nil {
		c.Logger().Warnf("更新步骤失败: %v", err)
	}

	runningTask := map[string]any{
		"task_id":  result.TaskID,
		"type":     globalfacts.TaskType,
		"status":   "running",
		"progress": 5,
		"logs":     []string{"全局事实设定任务已启动"},
		"mode":     mode,
	}
	t.publishTask(tenantID, userID, projectID, map[string]any{
		"task": runningTask,
		"technicalPlanPatch": map[string]any{
			"globalFactsTask": runningTask,
			"globalFacts":     []any{},
		},
	})

	return httpx.OK(c, map[string]any{
		"success": true,
		"taskId":  result.TaskID,
		"type":    globalfacts.TaskType,
		"status":  "running",
	})
}
