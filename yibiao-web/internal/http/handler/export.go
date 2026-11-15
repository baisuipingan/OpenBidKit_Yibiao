package handler

import (
	"context"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/export"
)

// Export 处理 Word 导出接口。
type Export struct {
	svc     *export.Service
	planSvc *service.TechnicalPlanService
}

// NewExport 创建 handler。
func NewExport(svc *export.Service, planSvc *service.TechnicalPlanService) *Export {
	return &Export{svc: svc, planSvc: planSvc}
}

// Register 注册路由。
func (h *Export) Register(g *echo.Group) {
	g.POST("/export/word", h.ExportWord)
}

// exportRequest 是导出请求。
type exportRequest struct {
	// TemplatePath 模板路径（服务端路径）。不传用内置模板。
	TemplatePath string `json:"templatePath"`
	// TargetID 模板里正文占位符的 tag（默认 technical-plan）
	TargetID string `json:"targetId"`
	// NodeIDs 只导出指定节点（空表示全部有正文的）
	NodeIDs []string `json:"nodeIds"`
	// SkipLayoutCheck 跳过格式自检（调试用）
	SkipLayoutCheck bool `json:"skipLayoutCheck"`
}

// ExportWord 导出技术方案为 Word。
//
// 这是整条链路的终点：目录 + 正文 → 排版 → docx。
func (h *Export) ExportWord(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req exportRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 读技术方案状态（目录 + 正文）
	state, err := h.planSvc.LoadState(c.Request().Context(), tenantID, projectID)
	if err != nil {
		return httpx.Internal(err)
	}

	// 目录为空
	if len(state.Outline) == 0 {
		return httpx.Fail(c, "NO_OUTLINE", "目录为空，请先生成目录")
	}

	// 转成导出用的 Sections（只取有正文的）
	flat := flattenOutline(state.Outline)
	sections := filterSections(flat, req.NodeIDs)
	if len(sections) == 0 {
		return httpx.Fail(c, "NO_CONTENT",
			"没有任何章节有正文内容，请先生成正文")
	}

	result, err := h.svc.ExportWord(c.Request().Context(), export.Request{
		TenantID:     tenantID,
		ProjectID:    projectID,
		OwnerID:      userID,
		ProjectName:  firstNonEmpty(state.OutlineProjectName, state.TenderFileName, "技术方案"),
		Sections:     sections,
		TemplatePath: req.TemplatePath,
		TargetID:     req.TargetID,
	})
	if err != nil {
		return httpx.Fail(c, "EXPORT_FAILED", err.Error())
	}

	return httpx.OK(c, result)
}

// flatEntry 是拍平后的目录项。
type flatEntry struct {
	NodeID  string
	Title   string
	Content string
	Level   int
	Sort    int32
}

// flattenOutline 把目录树按深度优先拍平。
func flattenOutline(nodes []service.OutlineNode) []flatEntry {
	// 先建父子索引
	children := make(map[string][]service.OutlineNode)
	var roots []service.OutlineNode
	for _, n := range nodes {
		if n.ParentNodeID == nil || *n.ParentNodeID == "" {
			roots = append(roots, n)
		} else {
			children[*n.ParentNodeID] = append(children[*n.ParentNodeID], n)
		}
	}

	// 按 sortOrder 排序
	sortNodes(roots)

	var out []flatEntry
	var walk func(list []service.OutlineNode, level int)
	walk = func(list []service.OutlineNode, level int) {
		for i := range list {
			n := list[i]
			out = append(out, flatEntry{
				NodeID:  n.NodeID,
				Title:   n.Title,
				Content: n.Content,
				Level:   level,
				Sort:    n.SortOrder,
			})

			if kids, ok := children[n.NodeID]; ok && len(kids) > 0 {
				sortNodes(kids)
				walk(kids, level+1)
			}
		}
	}
	walk(roots, 1)
	return out
}

// sortNodes 按 sortOrder 排序（插入排序，目录通常不大）。
func sortNodes(nodes []service.OutlineNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j].SortOrder < nodes[j-1].SortOrder; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
}

// filterSections 过滤出需要的且有正文的节。
func filterSections(flat []flatEntry, nodeIDs []string) []export.Section {
	wanted := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		wanted[id] = true
	}

	out := make([]export.Section, 0, len(flat))
	for _, e := range flat {
		// 只导出有正文的（空章节不产生内容）
		if len([]rune(strings.TrimSpace(e.Content))) < 10 {
			continue
		}
		if len(wanted) > 0 && !wanted[e.NodeID] {
			continue
		}
		out = append(out, export.Section{
			NodeID:  e.NodeID,
			Title:   e.Title,
			Level:   e.Level,
			Content: e.Content,
		})
	}
	return out
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// 确保 context 被使用
var _ = context.Background
