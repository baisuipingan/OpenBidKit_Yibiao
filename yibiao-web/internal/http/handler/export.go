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

// NewExport 创建 handler。svc 可为 nil（未部署 OpenXmlHelper 时仍注册路由，避免 404）。
func NewExport(svc *export.Service, planSvc *service.TechnicalPlanService) *Export {
	return &Export{svc: svc, planSvc: planSvc}
}

// Register 注册路由。
//
// 同时挂 /export/word（文档约定）与 /export/export-word（前端 auto-kebab
// `export.exportWord`），两者共用同一处理函数。
func (h *Export) Register(g *echo.Group) {
	g.POST("/export/word", h.ExportWord)
	g.POST("/export/export-word", h.ExportWord)
}

// exportRequest 是导出请求。
type exportRequest struct {
	// TemplatePath 模板路径（服务端路径）。不传时用项目保存的模板/内置 blank.docx。
	TemplatePath string `json:"templatePath"`
	// TemplateID 导出模板 ID（前端 generation config 的 exportTemplateId）
	TemplateID string `json:"templateId"`
	// TargetID 模板里正文占位符的 tag（默认 technical-plan）
	TargetID string `json:"targetId"`
	// NodeIDs 只导出指定节点（空表示全部有正文的）
	NodeIDs []string `json:"nodeIds"`
	// SkipLayoutCheck 跳过格式自检（调试用）
	SkipLayoutCheck bool `json:"skipLayoutCheck"`
	// RequestID / Source 对齐桌面版 payload（可忽略）
	RequestID string `json:"requestId"`
	Source    string `json:"source"`
}

// wordExportResponse 对齐桌面 WordExportResult，并附带 Web 下载字段。
type wordExportResponse struct {
	Success     bool     `json:"success"`
	Path        string   `json:"path"`
	Message     string   `json:"message"`
	Warnings    []string `json:"warnings,omitempty"`
	AssetID     string   `json:"assetId,omitempty"`
	FileName    string   `json:"fileName,omitempty"`
	SizeBytes   int64    `json:"sizeBytes,omitempty"`
	DownloadURL string   `json:"downloadUrl,omitempty"`
}

// ExportWord 导出技术方案为 Word。
//
// 这是整条链路的终点：目录 + 正文 → 排版 → docx。
func (h *Export) ExportWord(c echo.Context) error {
	if h.svc == nil {
		return httpx.Fail(c, "EXPORT_FAILED",
			"Word 导出助手未配置（缺少 OPENXML_BINARY / OpenXmlHelper），请部署后再试")
	}

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

	// 转成导出用的 Sections（只取有正文的；必要时回填 contentGenerationSections）
	flat := flattenOutline(state.Outline)
	enrichFlatFromSections(flat, state.ContentGenerationSections)
	sections := filterSections(flat, req.NodeIDs)
	if len(sections) == 0 {
		return httpx.Fail(c, "NO_CONTENT",
			"没有任何章节有正文内容，请先生成正文")
	}

	templatePath := strings.TrimSpace(req.TemplatePath)
	// 请求未带本地模板路径时走内置 blank.docx；保存的 exportTemplateId 是版式配置，不是 .docx 路径。
	_ = firstNonEmpty(req.TemplateID, state.ExportTemplateID)

	result, err := h.svc.ExportWord(c.Request().Context(), export.Request{
		TenantID:     tenantID,
		ProjectID:    projectID,
		OwnerID:      userID,
		ProjectName:  firstNonEmpty(state.OutlineProjectName, state.TenderFileName, "技术方案"),
		Sections:     sections,
		TemplatePath: templatePath,
		TargetID:     req.TargetID,
	})
	if err != nil {
		return httpx.Fail(c, "EXPORT_FAILED", err.Error())
	}

	downloadURL := result.DownloadURL
	if downloadURL == "" && result.AssetID != "" {
		downloadURL = "/api/assets/" + result.AssetID + "/download"
	}
	msg := "Word 已导出，请打开文档核对图片、表格和版式。"
	var warnings []string
	if result.Warning != "" {
		warnings = []string{result.Warning}
	}

	return httpx.OK(c, wordExportResponse{
		Success:     true,
		Path:        downloadURL,
		Message:     msg,
		Warnings:    warnings,
		AssetID:     result.AssetID,
		FileName:    result.FileName,
		SizeBytes:   result.SizeBytes,
		DownloadURL: downloadURL,
	})
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

// enrichFlatFromSections 用 contentGenerationSections 回填空节点正文。
func enrichFlatFromSections(flat []flatEntry, sections map[string]any) {
	if len(sections) == 0 {
		return
	}
	for i := range flat {
		if len([]rune(strings.TrimSpace(flat[i].Content))) >= 10 {
			continue
		}
		raw, ok := sections[flat[i].NodeID]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, _ := m["content"].(string)
		if len([]rune(strings.TrimSpace(content))) >= 10 {
			flat[i].Content = content
		}
	}
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
