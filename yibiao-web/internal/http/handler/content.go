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
	"github.com/patrick/yibiao-web/internal/service/agent/content"
	"github.com/patrick/yibiao-web/internal/service/imageplan"
	"github.com/patrick/yibiao-web/internal/service/knowledge"
	"github.com/patrick/yibiao-web/internal/service/settings"
	"github.com/patrick/yibiao-web/internal/service/taskrun"
)

// Content 处理正文生成接口。
type Content struct {
	runner    *content.Runner
	planSvc   *service.TechnicalPlanService
	settings  *settings.Service
	toolDecls []any

	mu    sync.RWMutex
	tasks map[string]*contentTaskState

	// taskRunLister 查任务记录（恢复时要拿旧工作区路径）
	taskRunLister TaskRunLister

	// matcher 为章节匹配知识库素材
	matcher *knowledge.Matcher
	// useKnowledge 是否启用知识库（租户级开关）
	useKnowledge bool
}

// SetKnowledge 注入知识库匹配器。
func (h *Content) SetKnowledge(matcher *knowledge.Matcher, enabled bool) {
	h.matcher = matcher
	h.useKnowledge = enabled
}

// TaskRunLister 按项目列任务记录。
type TaskRunLister interface {
	ListByProject(ctx context.Context, tenantID, projectID string, limit int32) ([]taskrun.View, error)
}

type contentTaskState struct {
	TaskID    string
	Status    string
	Events    []json.RawMessage
	StartedAt time.Time
	Done      bool
	Error     string
}

// NewContent 创建 handler。
func NewContent(runner *content.Runner, planSvc *service.TechnicalPlanService, settingsSvc *settings.Service) *Content {
	return &Content{
		runner:   runner,
		planSvc:  planSvc,
		settings: settingsSvc,
		tasks:    make(map[string]*contentTaskState),
	}
}

// SetTaskRunLister 注入任务记录查询（用于恢复）。
func (h *Content) SetTaskRunLister(l TaskRunLister) {
	h.taskRunLister = l
}

// SetTools 注入工具声明。
func (h *Content) SetTools(decls []any) {
	h.toolDecls = decls
}

// Register 注册路由。
func (h *Content) Register(g *echo.Group) {
	g.POST("/content/generate", h.Generate)
	g.POST("/content/events", h.Events)
	g.POST("/content/result", h.Result)
	g.POST("/content/preview-image-plan", h.PreviewImagePlan)
	// 恢复中断的正文生成
	g.POST("/content/resume", h.Resume)
	// 前端通过 tasks 命名空间调用（与 startContentGeneration 保持一致）
	g.POST("/tasks/resume-content-generation", h.Resume)
}

// generateRequest 是生成正文的请求。
type generateRequest struct {
	// === 知识库引用 ===
	// DisableKnowledge 关闭知识库素材引用
	DisableKnowledge bool `json:"disableKnowledge"`
	// KnowledgePerSection 每节最多引用几条（默认 3）
	KnowledgePerSection int `json:"knowledgePerSection"`
	// ManualKnowledgeOnly 只用用户手动关联的知识点，不做自动匹配
	ManualKnowledgeOnly bool `json:"manualKnowledgeOnly"`
	// KnowledgeDocumentIDs 限定参考文档范围（空=全部）
	KnowledgeDocumentIDs []string `json:"knowledgeDocumentIds"`

	// Provider 可选：不传从配置读
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// TenderMarkdownPath 招标文件路径
	TenderMarkdownPath string `json:"tenderMarkdownPath"`
	// TargetWordsPerSection 每节目标字数（0 表示按目录自动分配）
	TargetWordsPerSection int `json:"targetWordsPerSection"`
	// TotalWords 全文字数下限
	TotalWords int `json:"totalWords"`
	// NodeIDs 指定只生成某些节（空表示全部叶子节点）
	NodeIDs []string `json:"nodeIds"`
	// Force 为 true 时重新生成已有正文的节
	Force bool `json:"force"`

	// ===== 配图编排 =====
	// TotalImages 期望配图总数（0 = 自动估算）
	TotalImages int `json:"totalImages"`
	// MaxImagesPerSection 单节最多几张（默认 4）
	MaxImagesPerSection int `json:"maxImagesPerSection"`
	// EnabledImageKinds 可用的生成方式，空表示全部
	EnabledImageKinds []string `json:"enabledImageKinds"`
	// AIPercent AI 图片目标占比
	AIPercent int `json:"aiPercent"`
	// DisableImages 为 true 时不配图
	DisableImages bool `json:"disableImages"`
}

// Generate 启动正文生成。
//
// 从已落库的目录树里提取叶子节点作为待生成小节。
func (h *Content) Generate(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req generateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 模型配置
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
		p, mc, err := h.settings.Provider(c.Request().Context(), tenantID)
		if err != nil {
			return httpx.Fail(c, "MISSING_PROVIDER", err.Error())
		}
		provider, modelCfg = p, mc
	}

	if req.TenderMarkdownPath == "" {
		return httpx.Fail(c, "MISSING_TENDER", "缺少招标文件，请先导入招标文件")
	}

	// 从目录树提取待生成小节
	sections, err := h.collectSections(c.Request().Context(), tenantID, projectID, req)
	if err != nil {
		return httpx.Fail(c, "NO_SECTIONS", err.Error())
	}
	if len(sections) == 0 {
		return httpx.Fail(c, "NO_SECTIONS", "目录为空，请先生成目录")
	}

	// 检查是否有运行中的任务
	h.mu.RLock()
	for _, rec := range h.tasks {
		if !rec.Done {
			h.mu.RUnlock()
			return httpx.Conflict("TASK_RUNNING", "已有正文生成任务在运行")
		}
	}
	h.mu.RUnlock()

	var (
		mu     sync.Mutex
		events []json.RawMessage
		owner  string
	)

	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.Data)
		if len(events) > 3000 {
			events = events[len(events)-2000:]
		}
	}

	// 知识库素材匹配
	//
	// 为什么在配图之前做：如果匹配失败不该影响配图，
	// 而且匹配是"增强"不是"必需"，失败就静默跳过。
	if h.matcher != nil && h.useKnowledge && !req.DisableKnowledge {
		matchCtx, matchCancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
		if matchErr := h.matcher.FillSections(matchCtx, tenantID, projectID, sections, knowledge.MatchOptions{
			MaxPerSection: req.KnowledgePerSection,
			ManualOnly:    req.ManualKnowledgeOnly,
			DocumentIDs:   req.KnowledgeDocumentIDs,
		}); matchErr != nil {
			c.Logger().Warnf("知识库匹配失败，本次不引用素材: %v", matchErr)
		}
		matchCancel()
	}

	// 配图编排
	var imagePlanText string
	var plan *imageplan.Plan
	if !req.DisableImages {
		planSections := make([]imageplan.Section, 0, len(sections))
		for _, sec := range sections {
			planSections = append(planSections, imageplan.Section{
				NodeID:      sec.NodeID,
				Title:       sec.Title,
				Level:       sec.Level,
				Description: sec.Description,
				TargetWords: sec.TargetWords,
			})
		}

		var planErr error
		plan, planErr = imageplan.Build(imageplan.Options{
			Sections:      planSections,
			TotalImages:   req.TotalImages,
			EnabledKinds:  req.EnabledImageKinds,
			AIPercent:     req.AIPercent,
			MaxPerSection: req.MaxImagesPerSection,
		})
		if planErr != nil {
			c.Logger().Warnf("配图编排失败，本次不配图: %v", planErr)
		} else {
			imagePlanText = plan.FormatPrompt()
		}
	}

	result, err := h.runner.Start(c.Request().Context(), content.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: req.TenderMarkdownPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          h.toolDecls,
		Sections:           sections,
		MinWords:           req.TotalWords,
		ImagePlan:          imagePlanText,
		OnEvent:            onEvent,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	owner = result.TaskID
	mu.Lock()
	buffered := events
	mu.Unlock()

	h.mu.Lock()
	h.tasks[owner] = &contentTaskState{
		TaskID:    owner,
		Status:    "running",
		Events:    buffered,
		StartedAt: time.Now(),
	}
	h.mu.Unlock()

	// 更新步骤
	if err := h.planSvc.UpdateStep(c.Request().Context(), tenantID, projectID, "content-edit"); err != nil {
		c.Logger().Warnf("更新步骤失败: %v", err)
	}

	resp := map[string]any{
		"taskId":       result.TaskID,
		"sectionCount": result.SectionCount,
		"status":       "running",
	}
	if plan != nil {
		resp["imagePlan"] = plan
	}

	return httpx.OK(c, resp)
}

// collectSections 从目录树提取待生成小节。
//
// 只取叶子节点（无子节点的），因为父节点是章节标题，不需要正文。
func (h *Content) collectSections(
	ctx context.Context,
	tenantID, projectID string,
	req generateRequest,
) ([]content.Section, error) {
	state, err := h.planSvc.LoadState(ctx, tenantID, projectID)
	if err != nil {
		return nil, fmt.Errorf("读取技术方案状态失败: %w", err)
	}
	if len(state.Outline) == 0 {
		return nil, fmt.Errorf("目录为空，请先生成目录")
	}

	// 建父子关系表，找出叶子节点
	hasChild := make(map[string]bool)
	for _, n := range state.Outline {
		if n.ParentNodeID != nil {
			hasChild[*n.ParentNodeID] = true
		}
	}

	// 指定了 NodeIDs 就只生成那些
	wanted := make(map[string]bool, len(req.NodeIDs))
	for _, id := range req.NodeIDs {
		wanted[id] = true
	}

	// 计算每节目标字数
	targetPerSection := req.TargetWordsPerSection
	if targetPerSection <= 0 && req.TotalWords > 0 {
		// 按叶子数量平均分配
		leafCount := 0
		for _, n := range state.Outline {
			if !hasChild[n.NodeID] {
				leafCount++
			}
		}
		if leafCount > 0 {
			targetPerSection = req.TotalWords / leafCount
			if targetPerSection < 300 {
				targetPerSection = 300
			}
		}
	}
	if targetPerSection <= 0 {
		targetPerSection = 800 // 默认每节 800 字
	}

	// 构造路径（如「第一章 项目总述 > 1.1 项目背景」）
	titleByID := make(map[string]string, len(state.Outline))
	parentByID := make(map[string]*string, len(state.Outline))
	for _, n := range state.Outline {
		titleByID[n.NodeID] = n.Title
		parentByID[n.NodeID] = n.ParentNodeID
	}

	buildPath := func(nodeID string) string {
		var parts []string
		cur := nodeID
		// 最多 5 层，防环
		for i := 0; i < 5 && cur != ""; i++ {
			if t, ok := titleByID[cur]; ok {
				parts = append([]string{t}, parts...)
			}
			next, ok := parentByID[cur]
			if !ok || next == nil {
				break
			}
			cur = *next
		}
		return joinPath(parts)
	}

	out := make([]content.Section, 0, 32)
	for _, n := range state.Outline {
		if hasChild[n.NodeID] {
			continue // 跳过分支节点
		}
		if len(wanted) > 0 && !wanted[n.NodeID] {
			continue
		}

		// 已有正文的跳过（支持断点续生成）
		if !req.Force && n.Content != "" && len([]rune(n.Content)) > 50 {
			continue
		}

		out = append(out, content.Section{
			NodeID:      n.NodeID,
			Title:       n.Title,
			Path:        buildPath(n.NodeID),
			Description: n.Description,
			TargetWords: targetPerSection,
			Level:       int(n.Level),
		})
	}

	return out, nil
}

// joinPath 用「 > 」连接路径片段。
func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " > "
		}
		out += p
	}
	return out
}

// previewPlanRequest 是配图编排预览请求。
type previewPlanRequest struct {
	TotalImages           int      `json:"totalImages"`
	MaxImagesPerSection   int      `json:"maxImagesPerSection"`
	EnabledImageKinds     []string `json:"enabledImageKinds"`
	AIPercent             int      `json:"aiPercent"`
	NodeIDs               []string `json:"nodeIds"`
	TargetWordsPerSection int      `json:"targetWordsPerSection"`
}

// PreviewImagePlan 预览配图编排结果（不启动生成）。
//
// 用途：让用户在生成正文前先看「打算怎么配图」，
// 不满意可以调参数重试。
func (h *Content) PreviewImagePlan(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req previewPlanRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	sections, err := h.collectSections(c.Request().Context(), tenantID, projectID, generateRequest{
		NodeIDs:               req.NodeIDs,
		TargetWordsPerSection: req.TargetWordsPerSection,
	})
	if err != nil {
		return httpx.Fail(c, "NO_SECTIONS", err.Error())
	}

	planSections := make([]imageplan.Section, 0, len(sections))
	for _, sec := range sections {
		planSections = append(planSections, imageplan.Section{
			NodeID:      sec.NodeID,
			Title:       sec.Title,
			Level:       sec.Level,
			Description: sec.Description,
			TargetWords: sec.TargetWords,
		})
	}

	plan, err := imageplan.Build(imageplan.Options{
		Sections:      planSections,
		TotalImages:   req.TotalImages,
		EnabledKinds:  req.EnabledImageKinds,
		AIPercent:     req.AIPercent,
		MaxPerSection: req.MaxImagesPerSection,
	})
	if err != nil {
		return httpx.Fail(c, "PLAN_FAILED", err.Error())
	}

	return httpx.OK(c, map[string]any{
		"plan":       plan,
		"promptText": plan.FormatPrompt(),
	})
}

// EventsRequest 是订阅正文生成事件的请求。
type contentEventsRequest struct {
	TaskID string `json:"taskId"`
	Since  int    `json:"since"`
}

// Events 推送正文生成事件。
func (h *Content) Events(c echo.Context) error {
	var req contentEventsRequest
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
			h.mu.Lock()
			rec, ok := h.tasks[req.TaskID]
			if !ok {
				h.mu.Unlock()
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
			h.mu.Unlock()

			for _, ev := range pending {
				writeSSE("agent", json.RawMessage(ev))
				sent++

				var head struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				}
				if json.Unmarshal(ev, &head) == nil && head.Type == "task_end" {
					h.mu.Lock()
					if r := h.tasks[req.TaskID]; r != nil {
						r.Done = true
						if head.Status == "success" {
							r.Status = "success"
						} else {
							r.Status = "error"
						}
					}
					h.mu.Unlock()
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

// contentResultRequest 是查询结果的请求。
type contentResultRequest struct {
	WorkspaceDir string `json:"workspaceDir"`
	// Save 为 true 时把结果写入数据库
	Save bool `json:"save"`
}

// Result 收集并可选地保存正文。
func (h *Content) Result(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req contentResultRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.WorkspaceDir == "" {
		return httpx.Fail(c, "MISSING_WORKSPACE", "需要 workspaceDir")
	}

	collected, err := content.CollectResult(req.WorkspaceDir)
	if err != nil {
		return httpx.Fail(c, "PARSE_FAILED", err.Error())
	}

	saved := 0
	if req.Save {
		for _, sec := range collected.Sections {
			if sec.Error != "" || sec.Content == "" {
				continue
			}
			if err := h.planSvc.SaveSectionContent(c.Request().Context(), tenantID, projectID, sec.NodeID, sec.Content); err != nil {
				c.Logger().Warnf("保存小节 %s 失败: %v", sec.Title, err)
				continue
			}
			saved++
		}
	}

	return httpx.OK(c, map[string]any{
		"sections":     collected.Sections,
		"totalWords":   collected.TotalWords,
		"sectionCount": len(collected.Sections),
		"savedCount":   saved,
	})
}

// resumeRequest 是恢复请求。
type resumeRequest struct {
	// TaskID 要恢复的旧任务
	TaskID string `json:"taskId"`
	// 以下可选，不传则读旧任务记录的配置
	TenderMarkdownPath    string `json:"tenderMarkdownPath"`
	TargetWordsPerSection int    `json:"targetWordsPerSection"`
	TotalWords            int    `json:"totalWords"`
	// 配图参数（沿用原任务的编排）
	TotalImages         int      `json:"totalImages"`
	MaxImagesPerSection int      `json:"maxImagesPerSection"`
	EnabledImageKinds   []string `json:"enabledImageKinds"`
	DisableImages       bool     `json:"disableImages"`
}

// Resume 从中断处继续生成正文。
//
// **恢复原理**：
//
//	不依赖 sidecar 会话（重启后已失效），而是复用旧工作区 ——
//	Agent 上次写的 sections/*.md 和 content-manifest.json 都还在磁盘上。
//
//	步骤：
//	  1. 从任务记录找到旧工作区（task_runs.workspace_dir）
//	  2. 扫描 manifest，算出已完成小节（校验文件真实存在）
//	  3. 用同一工作区重启，Prompt 列出已完成小节让 Agent 跳过
//
// **为什么 Agent 会跳过**：Prompt 里有「恢复任务」段落明确列出
// 已完成的 nodeId；manifest 也不会被覆盖（prepareInputs 在
// resume 模式跳过已存在文件）。
func (h *Content) Resume(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req resumeRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.TaskID == "" {
		return httpx.BadRequest("缺少 taskId")
	}

	// 1. 找旧任务的工作区
	if h.taskRunLister == nil {
		return httpx.Fail(c, "NOT_ENABLED", "任务记录未启用，无法恢复")
	}
	runs, err := h.taskRunLister.ListByProject(c.Request().Context(), tenantID, projectID, 200)
	if err != nil {
		return httpx.Internal(err)
	}

	var workspaceDir string
	var oldStatus string
	for _, run := range runs {
		if run.TaskID == req.TaskID {
			workspaceDir = run.WorkspaceDir
			oldStatus = run.Status
			break
		}
	}
	if workspaceDir == "" {
		return httpx.NotFound("找不到该任务的工作区")
	}

	// 2. 扫描断点
	scan, err := h.runner.ScanResumeState(workspaceDir)
	if err != nil {
		return httpx.Fail(c, "SCAN_FAILED", "扫描进度失败："+err.Error())
	}
	if len(scan.Completed) == 0 {
		return httpx.Fail(c, "NOTHING_TO_RESUME", "该任务没有已完成的章节，请重新开始生成")
	}

	// 3. 加载当前目录的小节
	sections, err := h.collectSections(c.Request().Context(), tenantID, projectID, generateRequest{
		TargetWordsPerSection: req.TargetWordsPerSection,
	})
	if err != nil {
		return httpx.Fail(c, "NO_SECTIONS", err.Error())
	}

	// 4. 只保留未完成的小节
	pending := make([]content.Section, 0, len(sections))
	for _, sec := range sections {
		if scan.Completed[sec.NodeID] {
			continue
		}
		pending = append(pending, sec)
	}

	if len(pending) == 0 {
		return httpx.OK(c, map[string]any{
			"taskId":         req.TaskID,
			"status":         "already-complete",
			"message":        "该任务所有章节都已完成，无需恢复",
			"resumed":        false,
			"completedCount": len(scan.Completed),
		})
	}

	// 5. 配图编排（只给待办小节）
	var imagePlanText string
	if !req.DisableImages {
		planSections := make([]imageplan.Section, 0, len(pending))
		for _, sec := range pending {
			planSections = append(planSections, imageplan.Section{
				NodeID:      sec.NodeID,
				Title:       sec.Title,
				Level:       sec.Level,
				Description: sec.Description,
				TargetWords: sec.TargetWords,
			})
		}
		if plan, planErr := imageplan.Build(imageplan.Options{
			Sections:      planSections,
			TotalImages:   req.TotalImages,
			EnabledKinds:  req.EnabledImageKinds,
			MaxPerSection: req.MaxImagesPerSection,
		}); planErr == nil {
			imagePlanText = plan.FormatPrompt()
		}
	}

	// 6. 事件缓冲（与 Generate 一致）
	var (
		mu     sync.Mutex
		events []json.RawMessage
	)
	onEvent := func(ev agent.TaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.Data)
		if len(events) > 3000 {
			events = events[len(events)-2000:]
		}
	}

	// 恢复时用租户当前配置（不支持请求级覆盖 —— 恢复的是旧任务，
	// 模型应该沿用现在配置的，而不是旧任务当时的）
	provider, modelCfg, err := h.settings.Provider(c.Request().Context(), tenantID)
	if err != nil {
		return httpx.Fail(c, "MISSING_PROVIDER", err.Error())
	}

	// 7. 启动恢复任务
	result, err := h.runner.Start(c.Request().Context(), content.StartOptions{
		TenantID:           tenantID,
		ProjectID:          projectID,
		OwnerID:            userID,
		TenderMarkdownPath: req.TenderMarkdownPath,
		Provider:           provider,
		Model:              modelCfg,
		ToolDecls:          h.toolDecls,
		Sections:           pending,
		MinWords:           req.TotalWords,
		ImagePlan:          imagePlanText,
		Resume: &content.ResumeState{
			TaskID:         scan.TaskID,
			WorkspaceDir:   workspaceDir,
			Completed:      scan.Completed,
			CompletedCount: len(scan.Completed),
		},
		OnEvent: onEvent,
	})
	if err != nil {
		return httpx.Fail(c, "RESUME_FAILED", err.Error())
	}

	// 8. 登记事件缓冲（沿用同一个 taskId，前端可继续订阅）
	h.mu.Lock()
	if st, ok := h.tasks[result.TaskID]; ok {
		mu.Lock()
		st.Events = append(st.Events, events...)
		mu.Unlock()
	} else {
		mu.Lock()
		h.tasks[result.TaskID] = &contentTaskState{
			TaskID:    result.TaskID,
			Status:    "running",
			Events:    events,
			StartedAt: time.Now(),
		}
		mu.Unlock()
	}
	h.mu.Unlock()

	return httpx.OK(c, map[string]any{
		"taskId":         result.TaskID,
		"resumed":        true,
		"previousStatus": oldStatus,
		"completedCount": len(scan.Completed),
		"pendingCount":   len(pending),
		"status":         "running",
	})
}
