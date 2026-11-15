package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/docparse"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// Feasibility 处理可行性研究报告接口。
//
// 流程（6 步）：项目资料 → 资料文件 → 资料分析 → 报告目录 → 关键参数 → 正文生成
//
// 与技术方案的区别：可研报告只有一份固定大纲（无 AI 生成目录），
// 分析/参数/正文由 AI 按提示词生成，这里只负责状态存取。
type Feasibility struct {
	q       *store.Queries
	uploads *upload.Service
}

// NewFeasibility 创建 handler。
func NewFeasibility(q *store.Queries, uploads *upload.Service) *Feasibility {
	return &Feasibility{q: q, uploads: uploads}
}

// Register 注册路由。
func (h *Feasibility) Register(g *echo.Group) {
	g.POST("/feasibility-report/load-state", h.LoadState)
	g.POST("/feasibility-report/import-source-documents", h.ImportSources)
	g.POST("/feasibility-report/remove-source-document", h.RemoveSource)
	g.POST("/feasibility-report/read-source-markdown", h.ReadSource)
	g.POST("/feasibility-report/read-combined-source-markdown", h.ReadCombined)
	g.POST("/feasibility-report/update-step", h.UpdateStep)
	g.POST("/feasibility-report/save-project-info", h.SaveProjectInfo)
	g.POST("/feasibility-report/save-analysis", h.SaveAnalysis)
	g.POST("/feasibility-report/save-outline-config", h.SaveOutlineConfig)
	g.POST("/feasibility-report/save-outline", h.SaveOutline)
	g.POST("/feasibility-report/save-key-parameters", h.SaveParameters)
	g.POST("/feasibility-report/save-chapter-content", h.SaveChapterContent)
	g.POST("/feasibility-report/clear", h.Clear)
}

// feasibilityProjectInfo 项目基本信息（10 个字段）。
type feasibilityProjectInfo struct {
	ProjectName             string `json:"projectName"`
	ProjectType             string `json:"projectType"`
	Industry                string `json:"industry"`
	ConstructionUnit        string `json:"constructionUnit"`
	Location                string `json:"location"`
	ConstructionContent     string `json:"constructionContent"`
	ConstructionPeriodYears string `json:"constructionPeriodYears"`
	OperationPeriodYears    string `json:"operationPeriodYears"`
	TotalInvestment         string `json:"totalInvestment"`
	FundingSource           string `json:"fundingSource"`
}

// feasibilitySourceView 资料源文件视图。
type feasibilitySourceView struct {
	ID            string  `json:"id"`
	FileName      string  `json:"fileName"`
	MarkdownPath  string  `json:"markdownPath"`
	MarkdownChars int     `json:"markdownChars"`
	ContentHash   string  `json:"contentHash"`
	ParserLabel   *string `json:"parserLabel"`
	ImportedAt    string  `json:"importedAt"`
}

// feasibilityStateView 报告状态（对齐前端 FeasibilityReportState）。
type feasibilityStateView struct {
	Step                  string                  `json:"step"`
	ProjectInfo           feasibilityProjectInfo  `json:"projectInfo"`
	SourceFiles           []feasibilitySourceView `json:"sourceFiles"`
	AnalysisMarkdown      string                  `json:"analysisMarkdown"`
	OutlineTemplate       string                  `json:"outlineTemplate"`
	TargetWords           int                     `json:"targetWords"`
	ReferenceDocumentIDs  []string                `json:"referenceDocumentIds"`
	KeyParametersMarkdown string                  `json:"keyParametersMarkdown"`
	OutlineData           json.RawMessage         `json:"outlineData"`
	Tasks                 map[string]any          `json:"-"`
}

// LoadState 返回报告状态（首次访问自动初始化）。
func (h *Feasibility) LoadState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	row, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("初始化可研报告失败: %w", err))
	}

	return httpx.OK(c, h.buildState(ctx, row))
}

// buildState 把数据库记录转成前端视图。
func (h *Feasibility) buildState(ctx context.Context, row store.FeasibilityReport) feasibilityStateView {
	view := feasibilityStateView{
		Step:                  row.Step,
		OutlineTemplate:       row.OutlineTemplate,
		TargetWords:           int(row.TargetWords),
		AnalysisMarkdown:      row.AnalysisMarkdown,
		KeyParametersMarkdown: row.KeyParametersMarkdown,
		ReferenceDocumentIDs:  row.ReferenceDocumentIds,
		SourceFiles:           []feasibilitySourceView{},
		OutlineData:           json.RawMessage("null"),
	}
	if view.ReferenceDocumentIDs == nil {
		view.ReferenceDocumentIDs = []string{}
	}

	// 项目信息
	if len(row.ProjectInfo) > 0 {
		_ = json.Unmarshal(row.ProjectInfo, &view.ProjectInfo)
	}

	// 目录树
	if len(row.OutlineData) > 0 {
		view.OutlineData = json.RawMessage(row.OutlineData)
	}

	// 资料源
	sources, err := h.q.ListFeasibilitySources(ctx, row.ID)
	if err == nil {
		for _, s := range sources {
			v := feasibilitySourceView{
				ID:            s.ID.String(),
				FileName:      s.FileName,
				MarkdownChars: int(s.MarkdownChars),
				ContentHash:   s.ContentHash,
				ImportedAt:    s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
				// markdownPath 是桌面版概念（本地文件路径），
				// Web 版内容在库里，用 report/source 概念路径占位
				MarkdownPath: fmt.Sprintf("db://feasibility/%s/%s", row.ID, s.ID),
			}
			if s.ParserLabel.Valid {
				label := s.ParserLabel.String
				v.ParserLabel = &label
			}
			view.SourceFiles = append(view.SourceFiles, v)
		}
	}

	return view
}

// importSourcesRequest 导入资料请求。
type importSourcesRequest struct {
	FilePaths []string `json:"filePaths"`
}

// ImportSources 导入资料文件（解析成 markdown 存库）。
func (h *Feasibility) ImportSources(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req importSourcesRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if len(req.FilePaths) == 0 {
		return httpx.BadRequest("没有可导入的文件")
	}

	ctx := c.Request().Context()
	row, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	added := 0
	failures := make([]string, 0)

	for _, pseudo := range req.FilePaths {
		real, resolveErr := h.uploads.Resolve(pseudo)
		if resolveErr != nil {
			failures = append(failures, fmt.Sprintf("%s: 文件已过期", pseudo))
			continue
		}

		result, parseErr := docparse.ToMarkdown(ctx, real)
		if parseErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", fileBaseName(real), parseErr))
			continue
		}

		hash := sha256.Sum256([]byte(result.Markdown))
		hashHex := hex.EncodeToString(hash[:])

		// 去重：同一份内容不重复导入
		if n, _ := h.q.CountFeasibilitySourcesByHash(ctx, store.CountFeasibilitySourcesByHashParams{
			ReportID: row.ID, ContentHash: hashHex,
		}); n > 0 {
			failures = append(failures, fmt.Sprintf("%s: 内容重复，已跳过", fileBaseName(real)))
			continue
		}

		count, _ := h.q.ListFeasibilitySources(ctx, row.ID)
		parserLabel := result.ParserLabel

		if _, createErr := h.q.CreateFeasibilitySource(ctx, store.CreateFeasibilitySourceParams{
			ReportID:      row.ID,
			FileName:      fileBaseName(real),
			Markdown:      result.Markdown,
			MarkdownChars: int32(len([]rune(result.Markdown))),
			ContentHash:   hashHex,
			ParserLabel:   pgTextOrNull(parserLabel),
			SortOrder:     int32(len(count)),
		}); createErr != nil {
			failures = append(failures, fmt.Sprintf("%s: 保存失败", fileBaseName(real)))
			continue
		}
		added++
	}

	msg := fmt.Sprintf("已导入 %d 个文件", added)
	if len(failures) > 0 {
		msg += fmt.Sprintf("，%d 个失败", len(failures))
	}

	updated, _ := h.q.GetFeasibilityReport(ctx, store.GetFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})

	return httpx.OK(c, map[string]any{
		"success":     added > 0,
		"message":     msg,
		"errors":      failures,
		"sourceFiles": h.buildState(ctx, updated).SourceFiles,
	})
}

// removeSourceRequest 移除资料请求。
type feasibilityIDRequest struct {
	SourceID string `json:"sourceId"`
}

// RemoveSource 移除一个资料文件。
func (h *Feasibility) RemoveSource(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req feasibilityIDRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	sourceID, parseErr := uuid.Parse(req.SourceID)
	if parseErr != nil {
		return httpx.BadRequest("资料标识非法")
	}

	ctx := c.Request().Context()
	row, err := h.q.GetFeasibilityReport(ctx, store.GetFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.NotFound("报告不存在")
	}

	if err := h.q.DeleteFeasibilitySource(ctx, store.DeleteFeasibilitySourceParams{
		ID: sourceID, ReportID: row.ID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("移除资料失败: %w", err))
	}

	return h.LoadState(c)
}

// ReadSource 读取单个资料文件的 markdown。
func (h *Feasibility) ReadSource(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req feasibilityIDRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	sourceID, parseErr := uuid.Parse(req.SourceID)
	if parseErr != nil {
		return httpx.BadRequest("资料标识非法")
	}

	ctx := c.Request().Context()
	row, err := h.q.GetFeasibilityReport(ctx, store.GetFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.NotFound("报告不存在")
	}

	sources, _ := h.q.ListFeasibilitySources(ctx, row.ID)
	for _, s := range sources {
		if s.ID == sourceID {
			return httpx.OK(c, s.Markdown)
		}
	}
	return httpx.NotFound("资料不存在")
}

// ReadCombined 读取所有资料的合并 markdown。
func (h *Feasibility) ReadCombined(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	row, err := h.q.GetFeasibilityReport(ctx, store.GetFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.NotFound("报告不存在")
	}

	sources, _ := h.q.ListFeasibilitySources(ctx, row.ID)
	var sb strings.Builder
	for i, s := range sources {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## ")
		sb.WriteString(s.FileName)
		sb.WriteString("\n\n")
		sb.WriteString(s.Markdown)
	}

	return httpx.OK(c, sb.String())
}

// updateStepRequest 更新步骤请求。
type feasibilityStepRequest struct {
	Step string `json:"step"`
}

// UpdateStep 更新当前步骤。
func (h *Feasibility) UpdateStep(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req feasibilityStepRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	valid := map[string]bool{
		"materials": true, "sources": true, "analysis": true,
		"outline": true, "parameters": true, "content": true,
	}
	if !valid[req.Step] {
		return httpx.BadRequest("无效的步骤")
	}

	ctx := c.Request().Context()
	if _, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(err)
	}

	if err := h.q.UpdateFeasibilityStep(ctx, store.UpdateFeasibilityStepParams{
		TenantID: tenantID, ProjectID: projectID, Step: req.Step,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("更新步骤失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// SaveProjectInfo 保存项目基本信息。
func (h *Feasibility) SaveProjectInfo(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var info feasibilityProjectInfo
	if err := c.Bind(&info); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	raw, _ := json.Marshal(info)
	ctx := c.Request().Context()

	if _, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(err)
	}

	if err := h.q.UpdateFeasibilityProjectInfo(ctx, store.UpdateFeasibilityProjectInfoParams{
		TenantID: tenantID, ProjectID: projectID, ProjectInfo: raw,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存项目信息失败: %w", err))
	}

	return h.LoadState(c)
}

// saveAnalysisRequest 保存分析结果。
type saveAnalysisRequest struct {
	Markdown string `json:"markdown"`
}

// SaveAnalysis 保存资料分析结果。
func (h *Feasibility) SaveAnalysis(c echo.Context) error {
	return h.saveMarkdownField(c, "analysis")
}

// SaveParameters 保存关键参数。
func (h *Feasibility) SaveParameters(c echo.Context) error {
	return h.saveMarkdownField(c, "parameters")
}

// saveMarkdownField 保存 markdown 字段（分析 / 关键参数共用）。
func (h *Feasibility) saveMarkdownField(c echo.Context, field string) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveAnalysisRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	if _, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(err)
	}

	switch field {
	case "analysis":
		err = h.q.UpdateFeasibilityAnalysis(ctx, store.UpdateFeasibilityAnalysisParams{
			TenantID: tenantID, ProjectID: projectID, AnalysisMarkdown: req.Markdown,
		})
	case "parameters":
		err = h.q.UpdateFeasibilityParameters(ctx, store.UpdateFeasibilityParametersParams{
			TenantID: tenantID, ProjectID: projectID, KeyParametersMarkdown: req.Markdown,
		})
	}
	if err != nil {
		return httpx.Internal(fmt.Errorf("保存失败: %w", err))
	}

	return h.LoadState(c)
}

// saveOutlineConfigRequest 保存目录配置。
type saveOutlineConfigRequest struct {
	OutlineTemplate      string   `json:"outlineTemplate"`
	TargetWords          int      `json:"targetWords"`
	ReferenceDocumentIDs []string `json:"referenceDocumentIds"`
}

// SaveOutlineConfig 保存目录配置。
func (h *Feasibility) SaveOutlineConfig(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveOutlineConfigRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	row, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	// 空值保留原值（前端可能只传部分字段）
	tpl := req.OutlineTemplate
	if tpl == "" {
		tpl = row.OutlineTemplate
	}
	words := req.TargetWords
	if words <= 0 {
		words = int(row.TargetWords)
	}
	refs := req.ReferenceDocumentIDs
	if refs == nil {
		refs = row.ReferenceDocumentIds
	}

	if err := h.q.UpdateFeasibilityOutlineConfig(ctx, store.UpdateFeasibilityOutlineConfigParams{
		TenantID: tenantID, ProjectID: projectID,
		OutlineTemplate: tpl, TargetWords: int32(words), ReferenceDocumentIds: refs,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存目录配置失败: %w", err))
	}

	return h.LoadState(c)
}

// saveOutlineRequest 保存目录树。
type saveOutlineRequest struct {
	OutlineData json.RawMessage `json:"outlineData"`
}

// SaveOutline 保存目录树。
func (h *Feasibility) SaveOutline(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveOutlineRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if len(req.OutlineData) == 0 {
		return httpx.BadRequest("缺少目录数据")
	}

	ctx := c.Request().Context()
	if _, err := h.q.EnsureFeasibilityReport(ctx, store.EnsureFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(err)
	}

	if err := h.q.UpdateFeasibilityOutline(ctx, store.UpdateFeasibilityOutlineParams{
		TenantID: tenantID, ProjectID: projectID, OutlineData: req.OutlineData,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存目录失败: %w", err))
	}

	return h.LoadState(c)
}

// saveChapterRequest 保存章节正文。
type saveChapterRequest struct {
	NodeID  string `json:"nodeId"`
	Content string `json:"content"`
}

// SaveChapterContent 保存某一章的正文。
//
// 目录树整体存在 outline_data 里，这里按 nodeId 定位后更新该节点的 content。
func (h *Feasibility) SaveChapterContent(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveChapterRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.NodeID == "" {
		return httpx.BadRequest("缺少章节标识")
	}

	ctx := c.Request().Context()
	row, err := h.q.GetFeasibilityReport(ctx, store.GetFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.NotFound("报告不存在")
	}

	// 解析目录树并更新目标节点
	var outline map[string]any
	if len(row.OutlineData) > 0 {
		if err := json.Unmarshal(row.OutlineData, &outline); err != nil {
			return httpx.Internal(fmt.Errorf("目录数据损坏: %w", err))
		}
	} else {
		return httpx.BadRequest("请先生成目录")
	}

	updated := updateOutlineNodeContent(outline, req.NodeID, req.Content)
	if !updated {
		return httpx.NotFound("未找到该章节")
	}

	newData, _ := json.Marshal(outline)
	if err := h.q.UpdateFeasibilityOutline(ctx, store.UpdateFeasibilityOutlineParams{
		TenantID: tenantID, ProjectID: projectID, OutlineData: newData,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存正文失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// updateOutlineNodeContent 在目录树里找到 nodeId 对应的节点并写入 content。
//
// 目录树结构：{children: [{nodeId, content, children: [...]}]}
// 递归查找，找到就改并返回 true。
func updateOutlineNodeContent(node map[string]any, nodeID, content string) bool {
	children, ok := node["children"].([]any)
	if !ok {
		// 根节点自身也可能就是目标
		if id, _ := node["nodeId"].(string); id == nodeID {
			node["content"] = content
			return true
		}
		return false
	}

	for _, child := range children {
		childMap, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := childMap["nodeId"].(string); id == nodeID {
			childMap["content"] = content
			return true
		}
		if updateOutlineNodeContent(childMap, nodeID, content) {
			return true
		}
	}

	// 根节点自己
	if id, _ := node["nodeId"].(string); id == nodeID {
		node["content"] = content
		return true
	}
	return false
}

// Clear 清空报告。
func (h *Feasibility) Clear(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	if err := h.q.DeleteFeasibilityReport(c.Request().Context(), store.DeleteFeasibilityReportParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("清空失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true, "message": "已清空"})
}

// fileBaseName 取文件名。
func fileBaseName(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return textutil.SafeFileName(path[i+1:])
	}
	return textutil.SafeFileName(path)
}

// pgTextOrNull 转可空文本。
func pgTextOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
