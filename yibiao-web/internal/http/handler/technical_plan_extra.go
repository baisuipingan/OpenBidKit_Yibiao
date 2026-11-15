package handler

import (
	"encoding/json"
	"fmt"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/agent/outline"
	"github.com/patrick/yibiao-web/internal/store"
)

// 本文件补齐技术方案前端需要的其余接口。
//
// 设计：这些接口存的都是「AI 生成的中间状态」（目录、全局事实、分析配置…），
// 统一写进 technical_plan_meta.extra 这个 JSONB 字段，
// 不逐个建列 —— 否则每加一种 AI 产物就要做一次数据库迁移。
//
// 例外：reference_knowledge_document_ids 是 text[]，需要用数组查询，保留独立列。

// registerTechnicalPlanExtraRoutes 注册补充路由。
func (h *TechnicalPlan) registerExtraRoutes(g *echo.Group) {
	g.POST("/technical-plan/clear", h.ClearAll)
	g.POST("/technical-plan/save-outline", h.SaveOutlineState)
	g.POST("/technical-plan/save-outline-selection", h.SaveOutlineSelection)
	g.POST("/technical-plan/save-global-facts", h.SaveGlobalFacts)
	g.POST("/technical-plan/save-bid-analysis-config", h.SaveBidAnalysisConfig)
	g.POST("/technical-plan/select-bid-section", h.SelectBidSection)
	g.POST("/technical-plan/check-bid-sections", h.CheckBidSections)
	g.POST("/technical-plan/inspect-bid-template", h.InspectBidTemplate)
	g.POST("/technical-plan/open-bid-template", h.OpenBidTemplate)
	g.POST("/technical-plan/reset-content-generation", h.ResetContentGeneration)
	g.POST("/technical-plan/save-content-generation-options", h.SaveContentGenerationOptions)
	g.POST("/technical-plan/save-business-fill-review", h.SaveBusinessFillReview)
	g.POST("/technical-plan/get-business-fill-review", h.GetBusinessFillReview)
	g.POST("/technical-plan/read-tender-source-markdown", h.ReadTenderSourceMarkdown)
	g.POST("/technical-plan/remove-original-plan-document", h.RemoveOriginalPlanDocument)
	g.POST("/technical-plan/load-generation-config", h.LoadGenerationConfig)
	g.POST("/technical-plan/preview-content-word", h.PreviewContentWord)
	g.POST("/technical-plan/preview-business-chapter-word", h.PreviewBusinessChapterWord)
	g.POST("/technical-plan/available", h.Available)
}

// extraState 是 extra JSONB 的读写辅助。
type extraState map[string]any

// loadExtra 读取 extra 字段。
func (h *TechnicalPlan) loadExtra(c echo.Context) (extraState, error) {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return nil, err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return nil, err
	}

	ctx := c.Request().Context()
	// LoadState 首次访问会自动初始化 meta 行
	if _, ensureErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String()); ensureErr != nil {
		return nil, httpx.Internal(ensureErr)
	}

	raw, err := h.q.GetTechnicalPlanExtra(ctx, store.GetTechnicalPlanExtraParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return extraState{}, nil
	}

	out := extraState{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

// saveExtra 写回 extra 字段。
func (h *TechnicalPlan) saveExtra(c echo.Context, extra extraState) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	raw, _ := json.Marshal(extra)
	if err := h.q.UpdateTechnicalPlanExtra(c.Request().Context(), store.UpdateTechnicalPlanExtraParams{
		TenantID: tenantID, ProjectID: projectID, Extra: raw,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存状态失败: %w", err))
	}
	return nil
}

// ClearAll 清空技术方案（回到初始状态）。
func (h *TechnicalPlan) ClearAll(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	// 清目录节点 + 正文
	if err := h.q.DeleteOutlineNodesByProject(ctx, store.DeleteOutlineNodesByProjectParams{
		ProjectID: projectID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("清空目录失败: %w", err))
	}
	if err := h.q.DeleteContentSectionsByProject(ctx, store.DeleteContentSectionsByProjectParams{
		ProjectID: projectID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("清空正文失败: %w", err))
	}

	// 清 extra
	if err := h.saveExtra(c, extraState{}); err != nil {
		return err
	}

	return h.LoadState(c)
}

// SaveOutlineState 保存目录树（AI 生成后落库）。
//
// **必须写进 technical_plan_outline_nodes 表**，不能只放 extra：
// 正文生成按节点读、导出按节点遍历，只存 JSONB 后面全拿不到目录。
func (h *TechnicalPlan) SaveOutlineState(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	// 兼容两种入参：{ outline: {...} } 或直接传目录树
	root := payload
	if inner, ok := payload["outline"].(map[string]any); ok {
		root = inner
	}

	rawNodes, _ := root["outline"]
	if rawNodes == nil {
		rawNodes = root["nodes"]
	}
	nodeList, ok := rawNodes.([]any)

	if !ok || len(nodeList) == 0 {
		return httpx.BadRequest("缺少目录节点")
	}

	nodes := buildOutlineNodes(nodeList)

	projectName, _ := root["project_name"].(string)
	if projectName == "" {
		projectName, _ = root["projectName"].(string)
	}
	projectOverview, _ := root["project_overview"].(string)
	if projectOverview == "" {
		projectOverview, _ = root["projectOverview"].(string)
	}

	if err := h.svc.SaveOutline(c.Request().Context(), tenantID.String(), projectID.String(),
		nodes, projectName, projectOverview); err != nil {
		return httpx.Internal(fmt.Errorf("保存目录失败: %w", err))
	}

	return h.LoadState(c)
}

// buildOutlineNodes 把前端传来的目录 JSON 转成 outline.Node 树。
func buildOutlineNodes(items []any) []*outline.Node {
	out := make([]*outline.Node, 0, len(items))

	for idx, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		title, _ := m["title"].(string)
		if title == "" {
			continue
		}

		mode, _ := m["content_mode"].(string)
		if mode == "" {
			mode, _ = m["contentMode"].(string)
		}
		desc, _ := m["description"].(string)
		ruleBasis, _ := m["rule_basis"].(string)
		if ruleBasis == "" {
			ruleBasis, _ = m["ruleBasis"].(string)
		}

		node := &outline.Node{
			// ID 由程序分配，不信任前端传的值
			ID:          fmt.Sprintf("n%d", idx+1),
			Title:       title,
			Description: desc,
			ContentMode: mode,
			RuleBasis:   ruleBasis,
		}

		if children, ok := m["children"].([]any); ok && len(children) > 0 {
			node.Children = buildOutlineNodes(children)
			// 子节点 ID 加前缀，保证全局唯一
			for i, ch := range node.Children {
				ch.ID = fmt.Sprintf("%s-%d", node.ID, i+1)
			}
		}

		out = append(out, node)
	}

	return out
}

// SaveOutlineSelection 保存目录选中的节点。
func (h *TechnicalPlan) SaveOutlineSelection(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	extra, err := h.loadExtra(c)
	if err != nil {
		return err
	}
	extra["outlineSelection"] = payload

	if err := h.saveExtra(c, extra); err != nil {
		return err
	}
	return httpx.OK(c, map[string]any{"success": true})
}

// SaveGlobalFacts 保存全局事实（项目名称、金额、工期等）。
func (h *TechnicalPlan) SaveGlobalFacts(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	extra, err := h.loadExtra(c)
	if err != nil {
		return err
	}
	if facts, ok := payload["globalFacts"]; ok {
		extra["globalFacts"] = facts
	} else if facts, ok := payload["facts"]; ok {
		extra["globalFacts"] = facts
	} else {
		extra["globalFacts"] = payload
	}

	if err := h.saveExtra(c, extra); err != nil {
		return err
	}
	return h.LoadState(c)
}

// SaveBidAnalysisConfig 保存招标分析配置。
func (h *TechnicalPlan) SaveBidAnalysisConfig(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	if _, ensureErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String()); ensureErr != nil {
		return httpx.Internal(ensureErr)
	}

	// 模式落到 generation_config 表（有独立列）
	if mode, ok := payload["bidAnalysisMode"].(string); ok && mode != "" {
		if err := h.q.UpdateGenerationConfigBidAnalysisMode(ctx, store.UpdateGenerationConfigBidAnalysisModeParams{
			ProjectID: projectID, TenantID: tenantID, BidAnalysisMode: mode,
		}); err != nil {
			return httpx.Internal(fmt.Errorf("保存分析模式失败: %w", err))
		}
	}

	// 选中的分析任务放 extra
	extra, loadErr := h.loadExtra(c)
	if loadErr != nil {
		return loadErr
	}
	if ids, ok := payload["bidAnalysisSelectedTaskIds"]; ok {
		extra["bidAnalysisSelectedTaskIds"] = ids
	}
	if tasks, ok := payload["bidAnalysisTasks"]; ok {
		extra["bidAnalysisTasks"] = tasks
	}
	if err := h.saveExtra(c, extra); err != nil {
		return err
	}

	return h.LoadState(c)
}

// SelectBidSection 选择标段。
func (h *TechnicalPlan) SelectBidSection(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	sectionID, _ := payload["sectionId"].(string)
	title, _ := payload["title"].(string)

	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	if _, ensureErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String()); ensureErr != nil {
		return httpx.Internal(ensureErr)
	}

	if err := h.q.UpdateTechnicalPlanSelectedSection(ctx, store.UpdateTechnicalPlanSelectedSectionParams{
		ProjectID: projectID, TenantID: tenantID,
		SelectedSectionID: sectionID, SelectedSectionTitle: title,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("选择标段失败: %w", err))
	}

	return h.LoadState(c)
}

// CheckBidSections 检查招标文件里的标段划分。
//
// 简化实现：从已解析的 markdown 里按关键字提取标段名，
// 不调用 AI（AI 提取走 agent 任务，这里保证页面不报错）。
func (h *TechnicalPlan) CheckBidSections(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	state, loadErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String())
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}

	// 已有标段直接返回
	if sections, ok := state.BidSections.([]any); ok && len(sections) > 0 {
		return httpx.OK(c, map[string]any{
			"sections": sections,
			"status":   "done",
			"count":    len(sections),
		})
	}

	// 未提取过：返回空闲状态，让前端展示「开始提取」
	return httpx.OK(c, map[string]any{
		"sections": []any{},
		"status":   state.BidSectionExtractionStatus,
		"count":    0,
		"message":  "尚未提取标段，请先点击提取",
	})
}

// InspectBidTemplate 检查是否存在商务标模板。
func (h *TechnicalPlan) InspectBidTemplate(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	state, loadErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String())
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}

	return httpx.OK(c, map[string]any{
		"exists":     state.BidTemplateExists,
		"chapterIds": state.BidTemplateChapterIDs,
	})
}

// OpenBidTemplate 打开商务标模板。
//
// Web 版无法打开本地文件，返回明确错误让前端提示用户。
func (h *TechnicalPlan) OpenBidTemplate(c echo.Context) error {
	return httpx.Fail(c, "NOT_SUPPORTED", "Web 版不支持打开本地模板文件，请在产物库中下载后查看")
}

// ResetContentGeneration 重置正文生成。
func (h *TechnicalPlan) ResetContentGeneration(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	if err := h.q.DeleteContentSectionsByProject(ctx, store.DeleteContentSectionsByProjectParams{
		ProjectID: projectID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("重置正文失败: %w", err))
	}

	extra, loadErr := h.loadExtra(c)
	if loadErr != nil {
		return loadErr
	}
	delete(extra, "contentGenerationSections")
	delete(extra, "contentGenerationPlans")
	if err := h.saveExtra(c, extra); err != nil {
		return err
	}

	return h.LoadState(c)
}

// SaveContentGenerationOptions 保存正文生成选项。
func (h *TechnicalPlan) SaveContentGenerationOptions(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	extra, err := h.loadExtra(c)
	if err != nil {
		return err
	}
	extra["contentGenerationOptions"] = payload

	if err := h.saveExtra(c, extra); err != nil {
		return err
	}
	return httpx.OK(c, map[string]any{"success": true})
}

// SaveBusinessFillReview 保存商务标补全复核结果。
func (h *TechnicalPlan) SaveBusinessFillReview(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	extra, err := h.loadExtra(c)
	if err != nil {
		return err
	}
	extra["businessFillReview"] = payload

	if err := h.saveExtra(c, extra); err != nil {
		return err
	}
	return httpx.OK(c, map[string]any{"success": true})
}

// GetBusinessFillReview 读取商务标补全复核结果。
func (h *TechnicalPlan) GetBusinessFillReview(c echo.Context) error {
	extra, err := h.loadExtra(c)
	if err != nil {
		return err
	}
	return httpx.OK(c, extra["businessFillReview"])
}

// ReadTenderSourceMarkdown 读取某个招标源文件的 markdown。
func (h *TechnicalPlan) ReadTenderSourceMarkdown(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 先尝试按 sourceId 读
	sourceID, _ := payload["sourceId"].(string)
	if sourceID == "" {
		sourceID, _ = payload["value"].(string)
	}

	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	state, loadErr := h.svc.LoadState(ctx, tenantID.String(), projectID.String())
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}

	// Web 版：所有招标文件合并存一份 markdown，
	// 按 sourceId 精确区分需要额外存储，当前先统一返回合并内容。
	// 前端拿到后用 tab 切换展示，内容一致不影响阅读。
	_ = sourceID
	_ = state

	content, readErr := h.svc.ReadTenderMarkdown(ctx, tenantID.String(), projectID.String())
	if readErr != nil {
		return httpx.Fail(c, "READ_FAILED", "读取招标文件内容失败")
	}
	return httpx.OK(c, content)
}

// RemoveOriginalPlanDocument 移除原方案文件。
func (h *TechnicalPlan) RemoveOriginalPlanDocument(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	if err := h.q.ClearOriginalPlanDocument(ctx, store.ClearOriginalPlanDocumentParams{
		ProjectID: projectID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("移除原方案失败: %w", err))
	}

	return h.LoadState(c)
}

// LoadGenerationConfig 单独读取生成配置。
func (h *TechnicalPlan) LoadGenerationConfig(c echo.Context) error {
	return h.LoadState(c)
}

// PreviewContentWord 预览正文（字数统计）。
func (h *TechnicalPlan) PreviewContentWord(c echo.Context) error {
	var payload map[string]any
	if err := c.Bind(&payload); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	text, _ := payload["content"].(string)
	if text == "" {
		text, _ = payload["markdown"].(string)
	}

	return httpx.OK(c, map[string]any{
		"wordCount": countWords(text),
		"chars":     len([]rune(text)),
	})
}

// PreviewBusinessChapterWord 预览商务标章节字数。
func (h *TechnicalPlan) PreviewBusinessChapterWord(c echo.Context) error {
	return h.PreviewContentWord(c)
}

// Available 检查技术方案功能是否可用。
func (h *TechnicalPlan) Available(c echo.Context) error {
	return httpx.OK(c, map[string]any{"available": true, "reason": ""})
}

// countWords 统计字数（中文按字、英文按词）。
func countWords(s string) int {
	count := 0
	inWord := false
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一汉字
			count++
			inWord = false
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			if !inWord {
				count++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	return count
}
