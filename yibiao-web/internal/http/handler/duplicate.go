package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/duplicate"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// Duplicate 处理标书查重接口。
type Duplicate struct {
	q       *store.Queries
	svc     *duplicate.Service
	uploads *upload.Service
}

// NewDuplicate 创建 handler。
func NewDuplicate(q *store.Queries, svc *duplicate.Service, uploads *upload.Service) *Duplicate {
	return &Duplicate{q: q, svc: svc, uploads: uploads}
}

// Register 注册路由。
func (h *Duplicate) Register(g *echo.Group) {
	g.POST("/duplicate-check/load-state", h.LoadState)
	g.POST("/duplicate-check/save-files", h.SaveFiles)
	g.POST("/duplicate-check/save-ui-state", h.SaveUIState)
	g.POST("/duplicate-check/clear", h.Clear)
	g.POST("/duplicate-check/export-excel", h.ExportExcel)
	// 触发分析（前端没显式调用，但保存文件后自动跑）
	g.POST("/duplicate-check/analyze", h.Analyze)
}

// fileSelection 文件选择（对齐前端 LocalFileSelection）。
type fileSelection struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// analysisView 分析维度视图。
type analysisView struct {
	Status       string          `json:"status"`
	Progress     int             `json:"progress"`
	Message      string          `json:"message"`
	Error        string          `json:"error,omitempty"`
	TenderCount  int             `json:"tender_count"`
	MatchedCount int             `json:"matched_count"`
	TotalCount   int             `json:"total_count"`
	Detail       json.RawMessage `json:"detail,omitempty"`
	StartedAt    string          `json:"started_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

// workspaceState 查重工作区状态（对齐前端 DuplicateCheckWorkspaceState）。
type workspaceState struct {
	TenderFile        *fileSelection  `json:"tenderFile"`
	TenderFiles       []fileSelection `json:"tenderFiles"`
	BidFiles          []fileSelection `json:"bidFiles"`
	Step              string          `json:"step"`
	ActiveAnalysisTab string          `json:"activeAnalysisTab"`
	MetadataAnalysis  *analysisView   `json:"metadataAnalysis,omitempty"`
	OutlineAnalysis   *analysisView   `json:"outlineAnalysis,omitempty"`
	ContentAnalysis   *analysisView   `json:"contentAnalysis,omitempty"`
	ImageAnalysis     *analysisView   `json:"imageAnalysis,omitempty"`
}

// LoadState 返回查重工作区状态。
func (h *Duplicate) LoadState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	check, err := h.q.GetDuplicateCheck(ctx, store.GetDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		// 没有工作区 → 返回空状态（前端会展示上传界面）
		return httpx.OK(c, workspaceState{
			TenderFiles:       []fileSelection{},
			BidFiles:          []fileSelection{},
			Step:              "upload",
			ActiveAnalysisTab: "metadata",
		})
	}

	state := workspaceState{
		TenderFiles:       toFileSelections(check.TenderFileNames),
		BidFiles:          toFileSelections(check.BidFileNames),
		Step:              check.Step,
		ActiveAnalysisTab: check.ActiveTab,
	}
	if len(state.TenderFiles) > 0 {
		f := state.TenderFiles[0]
		state.TenderFile = &f
	}

	// 读四个维度的分析结果
	analyses, err := h.q.ListDuplicateAnalyses(ctx, check.ID)
	if err == nil {
		for _, a := range analyses {
			view := toAnalysisView(a)
			switch a.Dimension {
			case duplicate.DimMetadata:
				state.MetadataAnalysis = view
			case duplicate.DimOutline:
				state.OutlineAnalysis = view
			case duplicate.DimContent:
				state.ContentAnalysis = view
			case duplicate.DimImage:
				state.ImageAnalysis = view
			}
		}
	}

	return httpx.OK(c, state)
}

// saveFilesRequest 保存文件清单。
type saveFilesRequest struct {
	TenderFile        *fileSelection  `json:"tenderFile"`
	TenderFiles       []fileSelection `json:"tenderFiles"`
	BidFiles          []fileSelection `json:"bidFiles"`
	Step              string          `json:"step"`
	ActiveAnalysisTab string          `json:"activeAnalysisTab"`
}

// SaveFiles 保存文件清单并触发解析。
func (h *Duplicate) SaveFiles(c echo.Context) error {
	tenantStr, userID := identity(c)
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveFilesRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()

	tenderNames := make([]string, 0, len(req.TenderFiles))
	for _, f := range req.TenderFiles {
		tenderNames = append(tenderNames, f.Name)
	}
	bidNames := make([]string, 0, len(req.BidFiles))
	for _, f := range req.BidFiles {
		bidNames = append(bidNames, f.Name)
	}

	// upsert 工作区
	check, err := h.q.GetDuplicateCheck(ctx, store.GetDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		check, err = h.q.CreateDuplicateCheck(ctx, store.CreateDuplicateCheckParams{
			TenantID:        tenantID,
			ProjectID:       projectID,
			TenderFileNames: tenderNames,
			BidFileNames:    bidNames,
			Step:            firstNonEmptyStr(req.Step, "analysis"),
			ActiveTab:       firstNonEmptyStr(req.ActiveAnalysisTab, "metadata"),
		})
		if err != nil {
			return httpx.Internal(fmt.Errorf("创建工作区失败: %w", err))
		}
	} else {
		if err := h.q.UpdateDuplicateCheckFiles(ctx, store.UpdateDuplicateCheckFilesParams{
			TenantID: tenantID, ProjectID: projectID,
			TenderFileNames: tenderNames, BidFileNames: bidNames,
		}); err != nil {
			return httpx.Internal(err)
		}
	}

	// 解析文件并切句（异步，避免请求超时）
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		// 解析招标文件
		tenderPaths := h.resolvePaths(req.TenderFiles)
		if len(tenderPaths) > 0 {
			if _, err := h.svc.ParseAndStore(bgCtx, check.ID, "tender", tenderPaths); err != nil {
				h.logf(c, "解析招标文件失败: %v", err)
			}
		}

		// 解析投标文件
		bidPaths := h.resolvePaths(req.BidFiles)
		if len(bidPaths) > 0 {
			if _, err := h.svc.ParseAndStore(bgCtx, check.ID, "bid", bidPaths); err != nil {
				h.logf(c, "解析投标文件失败: %v", err)
			}
		}

		// 跑分析
		if err := h.svc.Analyze(bgCtx, duplicate.AnalyzeOptions{
			TenantID:  tenantStr,
			ProjectID: projectID.String(),
			OwnerID:   userID,
		}); err != nil {
			h.logf(c, "查重分析失败: %v", err)
		}
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始分析",
	})
}

// resolvePaths 把伪路径解析成真实路径。
func (h *Duplicate) resolvePaths(files []fileSelection) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if real, err := h.uploads.Resolve(f.Path); err == nil {
			out = append(out, real)
		}
	}
	return out
}

// SaveUIState 保存界面状态（步骤/页签）。
func (h *Duplicate) SaveUIState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req struct {
		Step              string `json:"step"`
		ActiveAnalysisTab string `json:"activeAnalysisTab"`
	}
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, err := h.q.GetDuplicateCheck(ctx, store.GetDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		// 没工作区就算了（UI 状态不值得报错）
		return httpx.OK(c, map[string]any{"success": true})
	}

	step := req.Step
	if step == "" {
		step = check.Step
	}
	tab := req.ActiveAnalysisTab
	if tab == "" {
		tab = check.ActiveTab
	}

	if err := h.q.UpdateDuplicateCheckUI(ctx, store.UpdateDuplicateCheckUIParams{
		TenantID: tenantID, ProjectID: projectID,
		Step: step, ActiveTab: tab,
	}); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// Clear 清空查重工作区。
func (h *Duplicate) Clear(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	if err := h.q.DeleteDuplicateCheck(c.Request().Context(), store.DeleteDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]any{"success": true, "message": "已清空"})
}

// Analyze 手动触发分析。
func (h *Duplicate) Analyze(c echo.Context) error {
	tenantStr, userID := identity(c)
	projectStr, err := projectID(c)
	if err != nil {
		return err
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_ = h.svc.Analyze(ctx, duplicate.AnalyzeOptions{
			TenantID:  tenantStr,
			ProjectID: projectStr,
			OwnerID:   userID,
		})
	}()

	return httpx.OK(c, map[string]any{"success": true, "message": "已开始分析"})
}

// ExportExcel 导出查重报告。
//
// 简化实现：只导出漏项清单（最核心的交付物）。
func (h *Duplicate) ExportExcel(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, err := h.q.GetDuplicateCheck(ctx, store.GetDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.NotFound("查重工作区不存在")
	}

	// 取正文维度的未覆盖要求
	rows, err := h.q.ListMatchesWithSentences(ctx, store.ListMatchesWithSentencesParams{
		CheckID:   check.ID,
		Dimension: duplicate.DimContent,
		Limit:     2000,
		Offset:    0,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	// 生成 CSV（Excel 可直接打开）
	var sb []byte
	sb = append(sb, []byte("\xEF\xBB\xBF")...) // UTF-8 BOM（Excel 识别中文）
	sb = append(sb, []byte("序号,招标要求,响应状态,投标对应内容,匹配方式,相似度\n")...)

	seq := 0
	for _, r := range rows {
		seq++
		status := "已响应"
		if !r.BidText.Valid {
			status = "漏项"
		}
		bidText := ""
		if r.BidText.Valid {
			bidText = r.BidText.String
		}
		sb = append(sb, []byte(fmt.Sprintf("%d,%s,%s,%s,%s,%.2f\n",
			seq, csvEscape(r.TenderText), status, csvEscape(bidText),
			r.Reason, r.Similarity))...)
	}

	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="duplicate-check-report.csv"`)
	return c.Blob(200, "text/csv; charset=utf-8", sb)
}

// 辅助函数

func tenantUUID(c echo.Context) (uuid.UUID, error) {
	tenantStr, _ := identity(c)
	id, err := uuid.Parse(tenantStr)
	if err != nil {
		return uuid.Nil, httpx.Unauthorized("租户标识非法")
	}
	return id, nil
}

func projectUUID(c echo.Context) (uuid.UUID, error) {
	projectStr, err := projectID(c)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(projectStr)
	if err != nil {
		return uuid.Nil, httpx.BadRequest("项目标识非法")
	}
	return id, nil
}

func toFileSelections(names []string) []fileSelection {
	out := make([]fileSelection, 0, len(names))
	for _, n := range names {
		out = append(out, fileSelection{Name: n, Path: ""})
	}
	return out
}

func toAnalysisView(a store.DuplicateAnalysis) *analysisView {
	v := &analysisView{
		Status:       a.Status,
		Progress:     int(a.Progress),
		Message:      a.Message,
		TenderCount:  int(a.TenderCount),
		MatchedCount: int(a.MatchedCount),
		TotalCount:   int(a.TotalCount),
		UpdatedAt:    a.UpdatedAt.Format(time.RFC3339),
	}
	if a.Error.Valid {
		v.Error = a.Error.String
	}
	if a.StartedAt.Valid {
		v.StartedAt = a.StartedAt.Time.Format(time.RFC3339)
	}
	if len(a.Detail) > 0 {
		v.Detail = json.RawMessage(a.Detail)
	}
	return v
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func csvEscape(s string) string {
	needQuote := false
	for _, r := range s {
		if r == ',' || r == '"' || r == '\n' || r == '\r' {
			needQuote = true
			break
		}
	}
	if !needQuote {
		return s
	}
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		if r == '"' {
			out = append(out, '"', '"')
			continue
		}
		out = append(out, []byte(string(r))...)
	}
	out = append(out, '"')
	return string(out)
}

// logf 简单日志（避免引入 logger 依赖）。
func (h *Duplicate) logf(c echo.Context, format string, args ...any) {
	c.Logger().Warnf(format, args...)
}
