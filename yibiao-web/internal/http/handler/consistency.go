package handler

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/consistency"
	"github.com/patrick/yibiao-web/internal/service/settings"
)

// Consistency 处理一致性检查接口。
type Consistency struct {
	runner   *consistency.Runner
	planSvc  *service.TechnicalPlanService
	settings *settings.Service
}

// NewConsistency 创建 handler。
func NewConsistency(
	runner *consistency.Runner,
	planSvc *service.TechnicalPlanService,
	settingsSvc *settings.Service,
) *Consistency {
	return &Consistency{runner: runner, planSvc: planSvc, settings: settingsSvc}
}

// Register 注册路由。
func (h *Consistency) Register(g *echo.Group) {
	g.POST("/consistency/check", h.Check)
	g.POST("/consistency/repair", h.Repair)
}

// consistencyCheckRequest 是检查请求。
type consistencyCheckRequest struct {
	// Provider 可选：不传从配置读
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// GlobalFacts 全局事实（可选，传入则检查与全局事实的冲突）
	GlobalFacts string `json:"globalFacts"`
	// NodeIDs 只检查指定章节（空表示全部有正文的）
	NodeIDs []string `json:"nodeIds"`
	// MaxWordsPerBatch 单批最大字数（默认 60000）
	MaxWordsPerBatch int `json:"maxWordsPerBatch"`
}

// Check 执行一致性检查。
//
// 这是同步接口（不是 SSE）：因为它是纯分析任务，
// 用户点「检查」后等待 10-60 秒拿结果，比看事件流更直接。
func (h *Consistency) Check(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req consistencyCheckRequest
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

	// 读正文
	state, err := h.planSvc.LoadState(c.Request().Context(), tenantID, projectID)
	if err != nil {
		return httpx.Internal(err)
	}

	sections := collectContentSections(state.Outline, req.NodeIDs)
	if len(sections) == 0 {
		return httpx.Fail(c, "NO_CONTENT",
			"没有任何章节有正文内容，请先生成正文")
	}

	report, err := h.runner.Check(c.Request().Context(), consistency.Options{
		TenantID:         tenantID,
		ProjectID:        projectID,
		Sections:         sections,
		GlobalFacts:      req.GlobalFacts,
		Provider:         provider,
		Model:            modelCfg,
		MaxWordsPerBatch: req.MaxWordsPerBatch,
	})
	if err != nil {
		return httpx.Fail(c, "CHECK_FAILED", err.Error())
	}

	return httpx.OK(c, report)
}

// repairRequest 是修复请求。
type repairRequest struct {
	// Provider 可选
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// Issues 要修复的问题（通常是 check 返回的子集）
	Issues []consistency.Issue `json:"issues"`
	// Facts 事实台账（可选，帮助模型理解上下文）
	Facts []consistency.Fact `json:"facts"`
	// Apply 为 true 时把修复结果写回数据库
	Apply bool `json:"apply"`
	// Concurrency 并发数
	Concurrency int `json:"concurrency"`
}

// Repair 修复一致性问题。
//
// 流程：check 拿到 issues → 用户确认要修哪些 → repair 批量修 → 可选回写
func (h *Consistency) Repair(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req repairRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if len(req.Issues) == 0 {
		return httpx.BadRequest("没有要修复的问题")
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

	// 读正文
	state, err := h.planSvc.LoadState(c.Request().Context(), tenantID, projectID)
	if err != nil {
		return httpx.Internal(err)
	}

	sections := collectContentSections(state.Outline, nil)

	result, err := h.runner.Repair(c.Request().Context(), consistency.RepairOptions{
		TenantID:    tenantID,
		ProjectID:   projectID,
		Sections:    sections,
		Issues:      req.Issues,
		Facts:       req.Facts,
		Provider:    provider,
		Model:       modelCfg,
		Concurrency: req.Concurrency,
	})
	if err != nil {
		return httpx.Fail(c, "REPAIR_FAILED", err.Error())
	}

	// 回写
	applied := 0
	if req.Apply {
		for _, rep := range result.Repairs {
			if rep.Error != "" || !rep.Changed || rep.Revised == "" {
				continue
			}
			if err := h.planSvc.SaveSectionContent(c.Request().Context(),
				tenantID, projectID, rep.NodeID, rep.Revised); err != nil {
				c.Logger().Warnf("回写小节 %s 失败: %v", rep.Title, err)
				continue
			}
			applied++
		}
	}

	return httpx.OK(c, map[string]any{
		"repairs":      result.Repairs,
		"fixedCount":   result.FixedCount,
		"failedCount":  result.FailedCount,
		"appliedCount": applied,
		"durationMs":   result.DurationMs,
		"applied":      req.Apply,
	})
}

// collectContentSections 收集有正文的章节。
//
// 按目录顺序（深度优先），跳过空章节。
func collectContentSections(nodes []service.OutlineNode, nodeIDs []string) []consistency.Section {
	wanted := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		wanted[id] = true
	}

	// 建父子索引并按 sortOrder 排序
	children := make(map[string][]service.OutlineNode)
	var roots []service.OutlineNode
	for _, n := range nodes {
		if n.ParentNodeID == nil || *n.ParentNodeID == "" {
			roots = append(roots, n)
		} else {
			children[*n.ParentNodeID] = append(children[*n.ParentNodeID], n)
		}
	}
	sortOutlineNodes(roots)

	var out []consistency.Section
	var walk func(list []service.OutlineNode, level int)
	walk = func(list []service.OutlineNode, level int) {
		for _, n := range list {
			content := strings.TrimSpace(n.Content)
			// 只收有实质内容的
			if len([]rune(content)) >= 20 {
				if len(wanted) == 0 || wanted[n.NodeID] {
					out = append(out, consistency.Section{
						NodeID:  n.NodeID,
						Title:   n.Title,
						Level:   level,
						Content: content,
					})
				}
			}
			if kids, ok := children[n.NodeID]; ok && len(kids) > 0 {
				sortOutlineNodes(kids)
				walk(kids, level+1)
			}
		}
	}
	walk(roots, 1)

	return out
}

// sortOutlineNodes 按 sortOrder 排序。
func sortOutlineNodes(nodes []service.OutlineNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j].SortOrder < nodes[j-1].SortOrder; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
}
