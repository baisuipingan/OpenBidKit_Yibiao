package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/docparse"
	"github.com/patrick/yibiao-web/internal/service/rejection"
	"github.com/patrick/yibiao-web/internal/service/settings"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// Rejection 处理废标项检查接口。
type Rejection struct {
	q        *store.Queries
	svc      *rejection.Service
	uploads  *upload.Service
	settings *settings.Service
}

// NewRejection 创建 handler。
func NewRejection(q *store.Queries, svc *rejection.Service, uploads *upload.Service, settingsSvc *settings.Service) *Rejection {
	return &Rejection{q: q, svc: svc, uploads: uploads, settings: settingsSvc}
}

// Register 注册路由。
func (h *Rejection) Register(g *echo.Group) {
	g.POST("/rejection-check/load-state", h.LoadState)
	g.POST("/rejection-check/import-document", h.ImportDocument)
	g.POST("/rejection-check/remove-document", h.RemoveDocument)
	g.POST("/rejection-check/save-ui-state", h.SaveUIState)
	g.POST("/rejection-check/update-state", h.UpdateState)
	g.POST("/rejection-check/export-excel", h.ExportExcel)
	g.POST("/rejection-check/clear", h.Clear)
	// 抽取 + 检查（前端通过 tasks 命名空间调用）
	g.POST("/rejection-check/extract", h.Extract)
	g.POST("/rejection-check/start-check", h.StartCheck)
}

// rejectionDocView 文档视图。
type rejectionDocView struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
	Content  string `json:"content"`
	// Source 来源：upload | technical-plan（前端签名的一部分）
	Source string `json:"source,omitempty"`
}

// rejectionFindingView 风险项视图。
type rejectionFindingView struct {
	ID            string `json:"id"`
	BidDocumentID string `json:"bidDocumentId"`
	Type          string `json:"type"`
	Severity      string `json:"severity"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	Requirement   string `json:"requirement"`
	BidEvidence   string `json:"bidEvidence"`
	RiskReason    string `json:"riskReason"`
	Suggestion    string `json:"suggestion"`

	// 错别字检查字段
	WrongText       string `json:"wrongText,omitempty"`
	CorrectText     string `json:"correctText,omitempty"`
	OriginalExcerpt string `json:"originalExcerpt,omitempty"`
	Reason          string `json:"reason,omitempty"`

	// 逻辑谬误检查字段（前端 normalizeLogicFindingState 要求）
	// 注意：缺 fallacyReason 会被前端直接丢弃，整条不显示！
	OriginalText  string `json:"originalText,omitempty"`
	LocationHint  string `json:"locationHint,omitempty"`
	FallacyReason string `json:"fallacyReason,omitempty"`
}

// rejectionResultState 检查结果状态（对齐前端）。
type rejectionResultState struct {
	Status          string                 `json:"status"`
	Findings        []rejectionFindingView `json:"findings"`
	InputSignature  string                 `json:"inputSignature,omitempty"`
	ProgressMessage string                 `json:"progressMessage,omitempty"`
	UpdatedAt       string                 `json:"updatedAt,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

// rejectionExtractionState 抽取状态（对齐前端）。
type rejectionExtractionState struct {
	Status          string `json:"status"`
	Content         string `json:"content"`
	Source          string `json:"source"`
	TenderSignature string `json:"tenderSignature,omitempty"`
	Error           string `json:"error,omitempty"`
}

// rejectionCheckOptions 检查开关。
type rejectionCheckOptions struct {
	RejectionCheck bool `json:"rejectionCheck"`
	TypoCheck      bool `json:"typoCheck"`
	LogicCheck     bool `json:"logicCheck"`
}

// rejectionWorkspace 工作区状态（对齐前端 RejectionCheckWorkspaceState）。
type rejectionWorkspace struct {
	TenderDocument              *rejectionDocView         `json:"tenderDocument"`
	TenderDocuments             []rejectionDocView        `json:"tenderDocuments"`
	BidDocuments                []rejectionDocView        `json:"bidDocuments"`
	ActiveDocumentTab           string                    `json:"activeDocumentTab"`
	Step                        string                    `json:"step"`
	ActiveResultTab             string                    `json:"activeResultTab"`
	ActiveCheckResultTab        string                    `json:"activeCheckResultTab"`
	InvalidBidAndRejectionItems *rejectionExtractionState `json:"invalidBidAndRejectionItems,omitempty"`
	CustomCheckItems            string                    `json:"customCheckItems"`
	CheckOptions                rejectionCheckOptions     `json:"rejectionCheckOptions"`
	RejectionCheckResult        *rejectionResultState     `json:"rejectionCheckResult,omitempty"`
	TypoCheckResult             *rejectionResultState     `json:"typoCheckResult,omitempty"`
	LogicCheckResult            *rejectionResultState     `json:"logicCheckResult,omitempty"`
}

// LoadState 返回工作区状态。
func (h *Rejection) LoadState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	check, err := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.OK(c, emptyRejectionWorkspace())
	}

	state := rejectionWorkspace{
		TenderDocuments:      []rejectionDocView{},
		BidDocuments:         []rejectionDocView{},
		ActiveDocumentTab:    "bid",
		Step:                 check.Step,
		ActiveResultTab:      check.ActiveResultTab,
		ActiveCheckResultTab: "rejection",
		CustomCheckItems:     check.CustomCheckItems,
		CheckOptions: rejectionCheckOptions{
			RejectionCheck: check.EnableRejection,
			TypoCheck:      check.EnableTypo,
			LogicCheck:     check.EnableLogic,
		},
	}

	// 招标文档
	if check.TenderFileName != "" {
		content := h.readDocumentContent(ctx, tenantID, projectID, "tender", check.TenderFileName)
		doc := rejectionDocView{
			ID:       "tender-1",
			FileName: check.TenderFileName,
			Content:  content,
		}
		state.TenderDocument = &doc
		state.TenderDocuments = []rejectionDocView{doc}
	}

	// 投标文档
	for i, name := range check.BidFileNames {
		state.BidDocuments = append(state.BidDocuments, rejectionDocView{
			ID:       fmt.Sprintf("bid-%d", i+1),
			FileName: name,
			Content:  h.readDocumentContent(ctx, tenantID, projectID, "bid", name),
		})
	}

	// 抽取状态
	if check.InvalidItems != "" || check.ExtractionStatus != "pending" {
		state.InvalidBidAndRejectionItems = &rejectionExtractionState{
			Status:          check.ExtractionStatus,
			Content:         check.InvalidItems,
			Source:          "ai",
			TenderSignature: check.ExtractionSignature,
		}
		if check.ExtractionError.Valid {
			state.InvalidBidAndRejectionItems.Error = check.ExtractionError.String
		}
	}

	// 三类检查结果
	runs, err := h.q.ListRejectionRuns(ctx, check.ID)
	if err == nil {
		for _, r := range runs {
			findings := h.loadFindingsWithDocs(ctx, check.ID, r.Kind, state.BidDocuments)
			// 签名按检查类型区分：
			//   废标项 → 投标文件 + 废标条款 + 自定义项（改条款要重查）
			//   错别字/逻辑 → 只看投标文件（改条款不影响）
			var sig string
			if r.Kind == rejection.KindRejection {
				sig = h.inputSignature(state.BidDocuments, check.InvalidItems, check.CustomCheckItems)
			} else {
				sig = h.bidSignature(state.BidDocuments)
			}

			rs := &rejectionResultState{
				Status:          r.Status,
				Findings:        findings,
				InputSignature:  sig,
				ProgressMessage: r.Message,
				UpdatedAt:       r.UpdatedAt.Format(time.RFC3339),
			}
			if r.Error.Valid {
				rs.Error = r.Error.String
			}

			switch r.Kind {
			case rejection.KindRejection:
				state.RejectionCheckResult = rs
			case rejection.KindTypo:
				state.TypoCheckResult = rs
			case rejection.KindLogic:
				state.LogicCheckResult = rs
			}
		}
	}

	return httpx.OK(c, state)
}

// readDocumentContent 读文档的解析后内容。
//
// 文档内容存在 upload 目录里的 .md 文件。
func (h *Rejection) readDocumentContent(ctx context.Context, tenantID, projectID uuid.UUID, role, fileName string) string {
	// 按上传目录约定查：<uploadRoot>/<role>/<fileName>.md
	marker := h.cachePath(tenantID, projectID, role, fileName)
	raw, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	return string(raw)
}

// cachePath 文档缓存路径。
func (h *Rejection) cachePath(tenantID, projectID uuid.UUID, role, fileName string) string {
	root := h.uploads.Root()
	return filepath.Join(root, "_rejection",
		tenantID.String()[:8], projectID.String()[:8], role, textutil.SafeFileName(fileName)+".md")
}

// loadFindings 读某类的风险项。
func (h *Rejection) loadFindingsWithDocs(ctx context.Context, checkID uuid.UUID, kind string, bidDocs []rejectionDocView) []rejectionFindingView {
	rows, err := h.q.ListFindingsByKind(ctx, store.ListFindingsByKindParams{
		CheckID: checkID, Kind: kind,
	})
	if err != nil {
		return []rejectionFindingView{}
	}

	out := make([]rejectionFindingView, 0, len(rows))
	for _, r := range rows {
		v := rejectionFindingView{
			ID:            r.ID.String(),
			BidDocumentID: "",
			Type:          r.FindingType,
			Severity:      r.Severity,
			Title:         r.Title,
			Summary:       r.Summary,
			Requirement:   r.Requirement,
			BidEvidence:   r.BidEvidence,
			RiskReason:    r.RiskReason,
			Suggestion:    r.Suggestion,
		}

		switch kind {
		case rejection.KindTypo:
			// 前端 TypoCheckFinding: wrongText / correctText / originalExcerpt / reason / locationHint
			v.OriginalExcerpt = r.BidEvidence
			v.Reason = r.RiskReason
			v.LocationHint = r.Summary
			if parts := strings.SplitN(r.Title, " → ", 2); len(parts) == 2 {
				v.WrongText = parts[0]
				v.CorrectText = parts[1]
			}

		case rejection.KindLogic:
			// 前端 LogicCheckFinding: title / originalText / locationHint / fallacyReason / suggestion
			//
			// **fallacyReason 必须填** —— 前端 normalizeLogicFindingState
			// 里 `if (!title || !fallacyReason) return null`，
			// 缺这个字段整条发现会被丢弃，页面显示"0 个逻辑问题"。
			v.OriginalText = r.BidEvidence
			v.FallacyReason = r.RiskReason
			v.LocationHint = r.Summary
		}

		out = append(out, v)
	}

	// 按投标文件 ID 关联。
	//
	// **必须用前端认识的 ID**（bid-1 / bid-2），
	// 前端是用 finding.bidDocumentId === document.id 匹配分组的。
	// 用文件名做 ID 会导致所有风险项都分不到任何文件的组里，
	// 页面上就是"全部3个但列表空白"。
	for i := range out {
		out[i].BidDocumentID = h.matchBidDocID(rows[i].BidFileName, bidDocs)
	}

	return out
}

// loadFindings 是旧签名（导出用，无文档列表时）。
func (h *Rejection) loadFindings(ctx context.Context, checkID uuid.UUID, kind string) []rejectionFindingView {
	return h.loadFindingsWithDocs(ctx, checkID, kind, nil)
}

// matchBidDocID 把文件名映射成前端用的 documentId（bid-1 / bid-2 ...）。
func (h *Rejection) matchBidDocID(fileName string, bidDocs []rejectionDocView) string {
	if fileName == "" {
		if len(bidDocs) > 0 {
			return bidDocs[0].ID
		}
		return "bid-1"
	}

	for _, d := range bidDocs {
		if d.FileName == fileName {
			return d.ID
		}
	}

	// 找不到就退回第一个（避免风险项从列表中消失）
	if len(bidDocs) > 0 {
		return bidDocs[0].ID
	}
	return "bid-1"
}

// importDocumentRequest 导入文档请求。
type importDocumentRequest struct {
	Role      string   `json:"role"`
	FilePaths []string `json:"filePaths"`
}

// ImportDocument 导入招标/投标文档。
func (h *Rejection) ImportDocument(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req importDocumentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	if req.Role != "tender" && req.Role != "bid" {
		return httpx.BadRequest("role 必须是 tender 或 bid")
	}
	if len(req.FilePaths) == 0 {
		return httpx.BadRequest("没有指定文件")
	}

	ctx := c.Request().Context()

	// 解析文件并缓存 markdown
	type parsed struct {
		name    string
		content string
	}
	results := make([]parsed, 0, len(req.FilePaths))

	for _, pseudo := range req.FilePaths {
		real, resolveErr := h.uploads.Resolve(pseudo)
		if resolveErr != nil {
			continue
		}

		result, parseErr := docparse.ToMarkdown(ctx, real)
		if parseErr != nil {
			h.logf(c, "解析 %s 失败: %v", filepath.Base(real), parseErr)
			continue
		}

		name := filepath.Base(real)
		cachePath := h.cachePath(tenantID, projectID, req.Role, name)
		if mkErr := os.MkdirAll(filepath.Dir(cachePath), 0o755); mkErr == nil {
			_ = os.WriteFile(cachePath, []byte(result.Markdown), 0o644)
		}

		results = append(results, parsed{name: name, content: result.Markdown})
	}

	if len(results) == 0 {
		return httpx.Fail(c, "PARSE_FAILED", "文件解析失败，请检查文件格式")
	}

	// 更新工作区记录
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		tenderName := ""
		bidNames := []string{}
		if req.Role == "tender" {
			tenderName = results[0].name
		} else {
			for _, r := range results {
				bidNames = append(bidNames, r.name)
			}
		}
		_, err = h.q.CreateRejectionCheck(ctx, store.CreateRejectionCheckParams{
			TenantID:       tenantID,
			ProjectID:      projectID,
			TenderFileName: tenderName,
			BidFileNames:   bidNames,
			Step:           "upload",
		})
		if err != nil {
			return httpx.Internal(fmt.Errorf("创建工作区失败: %w", err))
		}
	} else {
		tenderName := check.TenderFileName
		bidNames := append([]string{}, check.BidFileNames...)
		if req.Role == "tender" {
			tenderName = results[0].name
		} else {
			for _, r := range results {
				if !containsStr(bidNames, r.name) {
					bidNames = append(bidNames, r.name)
				}
			}
		}
		if err := h.q.UpdateRejectionFiles(ctx, store.UpdateRejectionFilesParams{
			TenantID: tenantID, ProjectID: projectID,
			TenderFileName: tenderName, BidFileNames: bidNames,
		}); err != nil {
			return httpx.Internal(fmt.Errorf("保存文件清单失败: %w", err))
		}
	}

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": fmt.Sprintf("已导入 %d 个文件", len(results)),
	})
}

// removeDocumentRequest 删除文档请求。
type removeDocumentRequest struct {
	Role       string `json:"role"`
	DocumentID string `json:"documentId"`
}

// RemoveDocument 删除文档。
func (h *Rejection) RemoveDocument(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req removeDocumentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.OK(c, map[string]any{"success": true})
	}

	tenderName := check.TenderFileName
	bidNames := make([]string, 0, len(check.BidFileNames))

	if req.Role == "tender" {
		tenderName = ""
	} else {
		for _, n := range check.BidFileNames {
			if req.DocumentID == "" || !strings.Contains(req.DocumentID, textutil.SafeFileName(n)) {
				bidNames = append(bidNames, n)
			}
		}
	}

	if err := h.q.UpdateRejectionFiles(ctx, store.UpdateRejectionFilesParams{
		TenantID: tenantID, ProjectID: projectID,
		TenderFileName: tenderName, BidFileNames: bidNames,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("移除文件失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// saveUIStateRequest 保存界面状态。
type saveUIStateRequest struct {
	Step                 string                 `json:"step"`
	ActiveDocumentTab    string                 `json:"activeDocumentTab"`
	ActiveResultTab      string                 `json:"activeResultTab"`
	ActiveCheckResultTab string                 `json:"activeCheckResultTab"`
	CustomCheckItems     *string                `json:"customCheckItems"`
	CheckOptions         *rejectionCheckOptions `json:"rejectionCheckOptions"`
}

// SaveUIState 保存界面状态。
func (h *Rejection) SaveUIState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveUIStateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.OK(c, map[string]any{"success": true})
	}

	step := firstNonEmptyStr(req.Step, check.Step)
	tab := firstNonEmptyStr(req.ActiveResultTab, check.ActiveResultTab)
	custom := check.CustomCheckItems
	if req.CustomCheckItems != nil {
		custom = *req.CustomCheckItems
	}

	_ = h.q.UpdateRejectionUI(ctx, store.UpdateRejectionUIParams{
		TenantID: tenantID, ProjectID: projectID,
		Step: step, ActiveResultTab: tab, CustomCheckItems: custom,
	})

	if req.CheckOptions != nil {
		_ = h.q.UpdateRejectionOptions(ctx, store.UpdateRejectionOptionsParams{
			TenantID: tenantID, ProjectID: projectID,
			EnableRejection: req.CheckOptions.RejectionCheck,
			EnableTypo:      req.CheckOptions.TypoCheck,
			EnableLogic:     req.CheckOptions.LogicCheck,
		})
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// UpdateState 更新状态（前端 patch 语义）。
//
// 桌面版这个接口接受任意 patch。Web 版按字段分发到具体接口。
func (h *Rejection) UpdateState(c echo.Context) error {
	// 复用 SaveUIState 的逻辑
	return h.SaveUIState(c)
}

// StartCheck 执行检查。
func (h *Rejection) StartCheck(c echo.Context) error {
	tenantStr, userID := identity(c)
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.BadRequest("请先导入文件")
	}

	provider, modelCfg, cfgErr := h.settings.Provider(ctx, tenantStr)
	if cfgErr != nil {
		return httpx.Fail(c, "MISSING_PROVIDER", cfgErr.Error())
	}

	// 组装输入
	bidContents := make(map[string]string, len(check.BidFileNames))
	for _, name := range check.BidFileNames {
		bidContents[name] = h.readDocumentContent(ctx, tenantID, projectID, "bid", name)
	}
	input := h.svc.BuildInput(check, bidContents)

	if len(input.BidDocuments) == 0 {
		return httpx.Fail(c, "EMPTY_BID", "投标文件内容为空，请检查文件是否解析成功")
	}

	// 至少启用一种检查
	enableRejection := check.EnableRejection
	enableTypo := check.EnableTypo
	enableLogic := check.EnableLogic

	var req struct {
		CheckOptions *rejectionCheckOptions `json:"checkOptions"`
	}
	_ = c.Bind(&req)
	if req.CheckOptions != nil {
		enableRejection = req.CheckOptions.RejectionCheck
		enableTypo = req.CheckOptions.TypoCheck
		enableLogic = req.CheckOptions.LogicCheck
	}

	if !enableRejection && !enableTypo && !enableLogic {
		return httpx.BadRequest("请至少启用一种检查")
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		_ = h.svc.Run(bgCtx, check.ID, rejection.RunOptions{
			TenantID:        tenantStr,
			ProjectID:       projectID.String(),
			OwnerID:         userID,
			Provider:        provider,
			Model:           modelCfg,
			EnableRejection: enableRejection,
			EnableTypo:      enableTypo,
			EnableLogic:     enableLogic,
		}, input)
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始检查",
	})
}

// ExportExcel 导出检查报告。
func (h *Rejection) ExportExcel(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.NotFound("检查工作区不存在")
	}

	// 汇总三类检查的结果
	var sb []byte
	sb = append(sb, []byte("\xEF\xBB\xBF")...)
	sb = append(sb, []byte("序号,检查类型,严重程度,问题,招标要求,投标原文,风险说明,修复建议\n")...)

	seq := 0
	for _, kind := range []string{rejection.KindRejection, rejection.KindTypo, rejection.KindLogic} {
		findings := h.loadFindings(ctx, check.ID, kind)
		for _, f := range findings {
			seq++
			sb = append(sb, []byte(fmt.Sprintf("%d,%s,%s,%s,%s,%s,%s,%s\n",
				seq, kindLabel(kind), severityLabel(f.Severity),
				csvEscape(f.Title), csvEscape(f.Requirement),
				csvEscape(f.BidEvidence), csvEscape(f.RiskReason),
				csvEscape(f.Suggestion)))...)
		}
	}

	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="rejection-check-report.csv"`)
	return c.Blob(200, "text/csv; charset=utf-8", sb)
}

// Clear 清空工作区。
func (h *Rejection) Clear(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	if err := h.q.DeleteRejectionCheck(c.Request().Context(), store.DeleteRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("清空失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true, "message": "已清空"})
}

func kindLabel(kind string) string {
	switch kind {
	case rejection.KindRejection:
		return "废标项"
	case rejection.KindTypo:
		return "错别字"
	case rejection.KindLogic:
		return "逻辑问题"
	}
	return kind
}

func severityLabel(s string) string {
	switch s {
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	}
	return s
}

// Extract 抽取招标文件的无效与废标项。
func (h *Rejection) Extract(c echo.Context) error {
	tenantStr, userID := identity(c)
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetRejectionCheck(ctx, store.GetRejectionCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil || check.TenderFileName == "" {
		return httpx.BadRequest("请先导入招标文件")
	}

	provider, modelCfg, cfgErr := h.settings.Provider(ctx, tenantStr)
	if cfgErr != nil {
		return httpx.Fail(c, "MISSING_PROVIDER", cfgErr.Error())
	}

	tenderContent := h.readDocumentContent(ctx, tenantID, projectID, "tender", check.TenderFileName)
	if strings.TrimSpace(tenderContent) == "" {
		return httpx.Fail(c, "EMPTY_TENDER", "招标文件内容为空，请检查文件是否解析成功")
	}

	// 标记为运行中
	_ = h.svc.SetExtractionState(ctx, tenantID, projectID, "running", 10, "正在解析废标条款", "", "")

	// 异步跑 AI 抽取
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		content, extractErr := h.runExtraction(bgCtx, tenantStr, projectID.String(), userID, provider, modelCfg, tenderContent)
		if extractErr != nil {
			h.logf(c, "[rejection] 抽取失败: %v", extractErr)
			if setErr := h.svc.SetExtractionState(bgCtx, tenantID, projectID, "error", 100,
				"解析失败", "", extractErr.Error()); setErr != nil {
				h.logf(c, "[rejection] 保存失败状态出错: %v", setErr)
			}
			return
		}

		h.logf(c, "[rejection] 抽取成功，%d 字", len([]rune(content)))
		if setErr := h.svc.SetExtractionState(bgCtx, tenantID, projectID, "success", 100,
			"解析完成", content, ""); setErr != nil {
			h.logf(c, "[rejection] 保存成功状态出错: %v", setErr)
		}

		// 记录签名：前端用它与当前招标文件比对，
		// 不匹配就显示"等待解析"（防止换了招标文件还用旧解析结果）
		doc := rejectionDocView{ID: "tender-1", FileName: check.TenderFileName, Content: tenderContent}
		sig := createTenderSignature(doc)
		if sigErr := h.svc.SetExtractionSignature(bgCtx, tenantID, projectID, sig); sigErr != nil {
			h.logf(c, "[rejection] 保存签名出错: %v", sigErr)
		}
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始解析废标条款",
	})
}

// runExtraction 用 AI 抽取废标条款。
func (h *Rejection) runExtraction(
	ctx context.Context,
	tenantID, projectID, ownerID string,
	provider agent.Provider,
	modelCfg agent.ModelConfig,
	tenderContent string,
) (string, error) {
	return h.svc.ExtractItems(ctx, rejection.ExtractOptions{
		TenantID:      tenantID,
		ProjectID:     projectID,
		OwnerID:       ownerID,
		Provider:      provider,
		Model:         modelCfg,
		TenderContent: tenderContent,
	})
}

func (h *Rejection) logf(c echo.Context, format string, args ...any) {
	c.Logger().Warnf(format, args...)
}

func emptyRejectionWorkspace() rejectionWorkspace {
	return rejectionWorkspace{
		TenderDocuments:      []rejectionDocView{},
		BidDocuments:         []rejectionDocView{},
		ActiveDocumentTab:    "bid",
		Step:                 "upload",
		ActiveResultTab:      "overview",
		ActiveCheckResultTab: "rejection",
		CheckOptions: rejectionCheckOptions{
			RejectionCheck: true,
		},
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var _ = json.Marshal
var _ = pgtype.Text{}

// createTenderSignature 复刻前端的 createDocumentSignature。
//
// **必须与前端算法完全一致**，否则签名比对永远不匹配，
// 页面会一直显示"等待解析"。
//
// 前端算法（RejectionCheckPage.tsx:305）：
//
//	[signatureId, source, fileName, content.length,
//	 content.slice(0, 800), content.slice(-800)]
//	  .join('\n---yibiao-rejection-signature---\n')
func createTenderSignature(doc rejectionDocView) string {
	content := strings.TrimSpace(doc.Content)
	if content == "" {
		return ""
	}

	// 前端：role === 'bid' && id === 'bid-1' ? 'bid' : id || role
	// 招标文档 id 是 'tender-1'，所以 signatureId = 'tender-1'
	signatureID := doc.ID
	if signatureID == "" {
		signatureID = "tender"
	}

	// 注意：不能给 source 填默认值。
	// 前端 tenderDocument.source 是 undefined（我没返回这个字段），
	// JS 里 undefined 拼进数组会变成空字符串。
	// 如果我填 "upload"，签名就永远对不上。
	source := doc.Source

	runes := []rune(content)
	n := len(runes)
	head := content
	if n > 800 {
		head = string(runes[:800])
	}
	tail := content
	if n > 800 {
		tail = string(runes[n-800:])
	}

	// 注意：前端用的是 content.length（JS 字符串长度 = UTF-16 码元数），
	// 中文在 JS 里 length 等于字符数（BMP 范围），
	// Go 里用 rune 数量能对齐。
	return strings.Join([]string{
		signatureID,
		source,
		doc.FileName,
		fmt.Sprintf("%d", n),
		head,
		tail,
	}, "\n---yibiao-rejection-signature---\n")
}

// bidSignature 复刻前端的 createBidDocumentsSignature。
//
// **错别字检查和逻辑检查用的是这个签名，不是 inputSignature**。
// 因为这两类只依赖投标文件内容，与废标条款无关 ——
// 改了废标条款不应该导致错别字检查结果失效。
//
// 前端算法（RejectionCheckPage.tsx:363）：
//
//	bidDocuments.map(createDocumentSignature)
//	  .filter(Boolean)
//	  .join('\n---yibiao-rejection-bid-signature---\n')
func (h *Rejection) bidSignature(bidDocs []rejectionDocView) string {
	sigs := make([]string, 0, len(bidDocs))
	for _, d := range bidDocs {
		if s := createTenderSignature(d); s != "" {
			sigs = append(sigs, s)
		}
	}
	return strings.Join(sigs, "\n---yibiao-rejection-bid-signature---\n")
}

// inputSignature 复刻前端的 createRejectionCheckInputSignature。
//
// **必须与前端完全一致**：前端用它判断"当前检查结果是否对应当前输入"，
// 不匹配就显示"待检查"（防止改了投标文件还用旧结果）。
//
// 前端算法（RejectionCheckPage.tsx:322）：
//
//	bidSignature = 各投标文件签名用分隔符连接
//	[bidSignature, analysis.length, analysis[0:800], analysis[-800:],
//	 custom.length, custom[0:800], custom[-800:]]
//	  .join('\n---yibiao-rejection-check-input---\n')
func (h *Rejection) inputSignature(bidDocs []rejectionDocView, invalidItems, customItems string) string {
	sigs := make([]string, 0, len(bidDocs))
	for _, d := range bidDocs {
		if s := createTenderSignature(d); s != "" {
			sigs = append(sigs, s)
		}
	}
	bidSignature := strings.Join(sigs, "\n---yibiao-rejection-bid-document---\n")

	analysis := strings.TrimSpace(invalidItems)
	if bidSignature == "" || analysis == "" {
		return ""
	}

	custom := strings.TrimSpace(customItems)

	return strings.Join([]string{
		bidSignature,
		fmt.Sprintf("%d", len([]rune(analysis))),
		sliceRunes(analysis, 0, 800),
		sliceRunesTail(analysis, 800),
		fmt.Sprintf("%d", len([]rune(custom))),
		sliceRunes(custom, 0, 800),
		sliceRunesTail(custom, 800),
	}, "\n---yibiao-rejection-check-input---\n")
}

// sliceRunes 取前 n 个字符。
func sliceRunes(s string, start, n int) string {
	runes := []rune(s)
	if start >= len(runes) {
		return ""
	}
	end := start + n
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

// sliceRunesTail 取后 n 个字符。
func sliceRunesTail(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}
