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
	"github.com/patrick/yibiao-web/internal/service/blind"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// Blind 处理暗标检查接口。
type Blind struct {
	q       *store.Queries
	svc     *blind.Service
	uploads *upload.Service
	// workDir 存放待检 docx 和修复产物
	workDir string
}

// NewBlind 创建 handler。
func NewBlind(q *store.Queries, svc *blind.Service, uploads *upload.Service, workDir string) *Blind {
	return &Blind{q: q, svc: svc, uploads: uploads, workDir: workDir}
}

// Register 注册路由。
func (h *Blind) Register(g *echo.Group) {
	g.POST("/blind-check/load-state", h.LoadState)
	g.POST("/blind-check/import-file", h.ImportFile)
	g.POST("/blind-check/remove-file", h.RemoveFile)
	g.POST("/blind-check/save-ui-state", h.SaveUIState)
	g.POST("/blind-check/list-presets", h.ListPresets)
	g.POST("/blind-check/select-preset", h.SelectPreset)
	g.POST("/blind-check/set-use-credential-words", h.SetUseCredentialWords)
	g.POST("/blind-check/get-credential-words", h.GetCredentialWords)
	g.POST("/blind-check/get-input-signature", h.GetInputSignature)
	g.POST("/blind-check/save-fix-rules", h.SaveFixRules)
	g.POST("/blind-check/save-fixed-document", h.SaveFixedDocument)
	g.POST("/blind-check/clear", h.Clear)
	// 触发检查
	g.POST("/blind-check/start-check", h.StartCheck)
	// 自动修复
	g.POST("/blind-check/start-fix", h.StartFix)
}

// fileView 待检文件视图。
type blindFileView struct {
	ID         string `json:"id"`
	FileName   string `json:"fileName"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	ImportedAt string `json:"importedAt"`
}

// findingView 检查发现视图（对齐前端 BlindCheckFindingGroup）。
type blindFindingView struct {
	RuleID    string        `json:"ruleId"`
	RuleTitle string        `json:"ruleTitle"`
	Category  string        `json:"category"`
	Severity  string        `json:"severity"`
	Message   string        `json:"message"`
	Count     int           `json:"count"`
	Candidate bool          `json:"candidate"`
	Samples   []blindSample `json:"samples"`
	Items     []blindItem   `json:"items"`
}

type blindSample struct {
	Where  string `json:"where"`
	Text   string `json:"text"`
	Detail string `json:"detail"`
	Origin string `json:"origin"`
	Hint   string `json:"hint"`
}

type blindItem struct {
	Where   string `json:"where"`
	Text    string `json:"text"`
	Detail  string `json:"detail"`
	Locator string `json:"locator"`
}

// resultView 检查结果视图。
type blindResultView struct {
	Status         string             `json:"status"`
	InputSignature string             `json:"inputSignature,omitempty"`
	PresetName     string             `json:"presetName,omitempty"`
	Summary        *json.RawMessage   `json:"summary"`
	Stats          *json.RawMessage   `json:"stats"`
	RuleErrors     []string           `json:"ruleErrors,omitempty"`
	Error          string             `json:"error,omitempty"`
	UpdatedAt      string             `json:"updatedAt,omitempty"`
	Groups         []blindFindingView `json:"groups"`
}

// issueView 最终问题清单（前端 issues 字段）。
type blindIssueView struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	RuleID       string            `json:"ruleId"`
	Category     string            `json:"category"`
	Severity     string            `json:"severity"`
	Message      string            `json:"message"`
	Count        int               `json:"count"`
	Uncertain    bool              `json:"uncertain"`
	Requirements []blindReq        `json:"requirements"`
	Explanation  string            `json:"explanation"`
	Hint         string            `json:"hint"`
	Occurrences  []blindOccurrence `json:"occurrences"`
	More         int               `json:"more"`
}

type blindReq struct {
	Text       string `json:"text"`
	Unverified bool   `json:"unverified"`
}

type blindOccurrence struct {
	Where   string `json:"where"`
	Text    string `json:"text"`
	Detail  string `json:"detail"`
	Locator string `json:"locator"`
}

// workspaceView 工作区视图。
type blindWorkspaceView struct {
	Step               string           `json:"step"`
	PresetID           string           `json:"presetId"`
	PresetTab          string           `json:"presetTab"`
	ResultTab          string           `json:"resultTab"`
	UseCredentialWords bool             `json:"useCredentialWords"`
	AIReviewEnabled    bool             `json:"aiReviewEnabled"`
	FixAIEnabled       bool             `json:"fixAiEnabled"`
	File               *blindFileView   `json:"file"`
	Result             blindResultView  `json:"result"`
	Issues             []blindIssueView `json:"issues"`
	FixRules           []string         `json:"fixRules"`
	FixResult          map[string]any   `json:"fixResult"`
	Review             map[string]any   `json:"review"`
}

// LoadState 返回工作区状态。
func (h *Blind) LoadState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	check, err := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return httpx.OK(c, emptyBlindWorkspace())
	}

	view := blindWorkspaceView{
		Step:               check.Step,
		PresetID:           check.PresetID,
		PresetTab:          check.PresetTab,
		ResultTab:          check.ResultTab,
		UseCredentialWords: check.UseCredentialWords,
		AIReviewEnabled:    check.AiReviewEnabled,
		FixAIEnabled:       check.FixAiEnabled,
		FixRules:           normalizeFixRulesOrDefault(check.FixRules),
		Issues:             []blindIssueView{},
		FixResult:          map[string]any{"status": "idle", "findings": []any{}},
		Review:             map[string]any{"status": "idle"},
		Result:             blindResultView{Status: "idle", Groups: []blindFindingView{}},
	}

	if check.FileName != "" {
		view.File = &blindFileView{
			ID:       "blind-1",
			FileName: check.FileName,
			Size:     check.FileSize,
		}
	}

	// 加载最近的检查结果
	result, err := h.q.LatestBlindCheckResult(ctx, check.ID)
	if err == nil {
		view.Result = h.buildResultView(ctx, result)
		// 有成功结果才生成 issues
		if result.Status == blind.StatusSuccess {
			view.Issues = buildIssues(view.Result.Groups)
		}
	}

	// 修复结果
	fix, fixErr := h.q.LatestBlindFix(ctx, check.ID)
	if fixErr == nil {
		fixView := map[string]any{
			"status":     fix.Status,
			"progress":   int(fix.Progress),
			"message":    fix.ProgressMessage,
			"fixRules":   fix.FixRules,
			"outputPath": fix.OutputFileName,
			"groups":     []any{},
			// inputSignature：前端用它判断修复结果是否对应当前输入
			// （文件/预设/敏感词任一变化就该失效）
			"inputSignature": blind.InputSignature(
				check.FileName, check.FileSize, check.PresetID,
				check.ManualWords, check.UseCredentialWords,
			),
		}
		if fix.Error.Valid {
			fixView["error"] = fix.Error.String
		}
		if len(fix.Validation) > 0 {
			var v map[string]any
			if json.Unmarshal(fix.Validation, &v) == nil {
				// 生成 items：前端用 item.fixed 求和显示「已修复 N 处」
				fixView["items"] = buildFixItems(v, fix.FixRules)
				// 展开成前端要的字段
				if fixes, ok := v["fixes"]; ok {
					fixView["fixes"] = fixes
				}
				if notes, ok := v["notes"]; ok {
					fixView["notes"] = notes
				}
				if before, ok := v["beforeSummary"]; ok {
					fixView["beforeSummary"] = before
				}
				for src, dst := range map[string]string{
					"sourceBytes": "sourceBytes",
					"outputBytes": "outputBytes",
					"durationMs":  "durationMs",
				} {
					if val, ok := v[src]; ok {
						fixView[dst] = val
					}
				}
				// validation 子对象
				fixView["validation"] = map[string]any{
					"checked":        v["checked"],
					"skippedReason":  v["skippedReason"],
					"originalErrors": v["originalErrors"],
					"fixedErrors":    v["fixedErrors"],
					"newErrors":      v["newErrors"],
				}
			}
		}
		view.FixResult = fixView
	}

	return httpx.OK(c, view)
}

// buildResultView 把数据库记录转成前端视图。
func (h *Blind) buildResultView(ctx context.Context, r store.BlindCheckResult) blindResultView {
	v := blindResultView{
		Status:         r.Status,
		InputSignature: r.InputSignature,
		PresetName:     r.PresetName,
		UpdatedAt:      r.CreatedAt.Format(time.RFC3339),
		Groups:         []blindFindingView{},
	}
	if r.Error.Valid {
		v.Error = r.Error.String
	}
	if len(r.Summary) > 0 {
		raw := json.RawMessage(r.Summary)
		v.Summary = &raw
	}
	if len(r.Stats) > 0 {
		raw := json.RawMessage(r.Stats)
		v.Stats = &raw
	}
	if len(r.RuleErrors) > 0 {
		var errs []string
		if json.Unmarshal(r.RuleErrors, &errs) == nil {
			v.RuleErrors = errs
		}
	}

	rows, err := h.q.ListBlindFindings(ctx, r.ID)
	if err != nil {
		return v
	}

	for _, row := range rows {
		f := blindFindingView{
			RuleID:    row.RuleID,
			RuleTitle: row.RuleTitle,
			Category:  row.Category,
			Severity:  row.Severity,
			Message:   row.Message,
			Count:     int(row.Count),
			Candidate: row.Candidate,
			Samples:   []blindSample{},
			Items:     []blindItem{},
		}
		if len(row.Samples) > 0 {
			_ = json.Unmarshal(row.Samples, &f.Samples)
		}
		if len(row.Items) > 0 {
			_ = json.Unmarshal(row.Items, &f.Items)
		}
		v.Groups = append(v.Groups, f)
	}

	return v
}

// buildIssues 把规则分组转成前端的问题清单。
//
// 为什么要转换：前端的 issue 结构更扁平（一个 issue 一个条目），
// 而 helper 返回的是按规则分组（一组可能包含多处）。
// 这里展开成逐个问题，便于用户逐条处理。
func buildIssues(groups []blindFindingView) []blindIssueView {
	out := make([]blindIssueView, 0, len(groups))
	for i, g := range groups {
		occurrences := make([]blindOccurrence, 0, len(g.Items))
		for _, it := range g.Items {
			occurrences = append(occurrences, blindOccurrence{
				Where:   it.Where,
				Text:    it.Text,
				Detail:  it.Detail,
				Locator: it.Locator,
			})
		}
		if len(occurrences) == 0 {
			for _, s := range g.Samples {
				occurrences = append(occurrences, blindOccurrence{
					Where:  s.Where,
					Text:   s.Text,
					Detail: s.Detail,
				})
			}
		}

		hint := ""
		explanation := ""
		if len(g.Samples) > 0 {
			hint = g.Samples[0].Hint
			explanation = g.Samples[0].Detail
		}

		out = append(out, blindIssueView{
			ID:           fmt.Sprintf("issue-%d", i+1),
			Kind:         "rule",
			RuleID:       g.RuleID,
			Category:     g.Category,
			Severity:     g.Severity,
			Message:      g.Message,
			Count:        g.Count,
			Uncertain:    g.Candidate,
			Requirements: []blindReq{},
			Explanation:  explanation,
			Hint:         hint,
			Occurrences:  occurrences,
			More:         0,
		})
	}
	return out
}

// importFileRequest 导入文件请求。
type importFileRequest struct {
	Path string `json:"path"`
}

// ImportFile 导入待检 docx。
func (h *Blind) ImportFile(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req importFileRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.Path == "" {
		return httpx.BadRequest("没有指定文件")
	}

	real, resolveErr := h.uploads.Resolve(req.Path)
	if resolveErr != nil {
		return httpx.BadRequest("文件不存在或已过期")
	}

	// 只接受 docx
	if !strings.HasSuffix(strings.ToLower(real), ".docx") {
		return httpx.Fail(c, "INVALID_FORMAT", "暗标检查只支持 .docx 文件")
	}

	info, statErr := os.Stat(real)
	if statErr != nil {
		return httpx.BadRequest("读取文件失败")
	}

	// 拷进工作区（helper 需要文件）
	dir := filepath.Join(h.workDir, tenantID.String()[:8], projectID.String()[:8])
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return httpx.Internal(mkErr)
	}
	fileName := filepath.Base(real)
	dst := filepath.Join(dir, textutil.SafeFileName(fileName))
	if copyErr := copyFile(real, dst); copyErr != nil {
		return httpx.Internal(fmt.Errorf("保存文件失败: %w", copyErr))
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		_, err = h.q.CreateBlindCheck(ctx, store.CreateBlindCheckParams{
			TenantID:  tenantID,
			ProjectID: projectID,
			FileName:  fileName,
			FileSize:  info.Size(),
			Step:      "preset",
		})
		if err != nil {
			return httpx.Internal(fmt.Errorf("创建工作区失败: %w", err))
		}
		_ = check
	} else if err := h.q.UpdateBlindCheckFile(ctx, store.UpdateBlindCheckFileParams{
		TenantID: tenantID, ProjectID: projectID,
		FileName: fileName, FileSize: info.Size(),
	}); err != nil {
		// 不能吞：更新失败还返回成功，前端会显示旧文件名
		return httpx.Internal(fmt.Errorf("保存文件信息失败: %w", err))
	}

	return h.LoadState(c)
}

// RemoveFile 移除待检文件。
func (h *Blind) RemoveFile(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	if err := h.q.UpdateBlindCheckFile(c.Request().Context(), store.UpdateBlindCheckFileParams{
		TenantID: tenantID, ProjectID: projectID,
		FileName: "", FileSize: 0,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("移除文件失败: %w", err))
	}

	return h.LoadState(c)
}

// saveUIStateRequest 保存界面状态。
type blindUIStateRequest struct {
	Step      string `json:"step"`
	PresetTab string `json:"presetTab"`
	ResultTab string `json:"resultTab"`
}

// SaveUIState 保存界面状态。
func (h *Blind) SaveUIState(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req blindUIStateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.OK(c, map[string]any{"success": true})
	}

	_ = h.q.UpdateBlindCheckUI(ctx, store.UpdateBlindCheckUIParams{
		TenantID: tenantID, ProjectID: projectID,
		Step:      firstNonEmptyStr(req.Step, check.Step),
		PresetTab: firstNonEmptyStr(req.PresetTab, check.PresetTab),
		ResultTab: firstNonEmptyStr(req.ResultTab, check.ResultTab),
	})

	return httpx.OK(c, map[string]any{"success": true})
}

// ListPresets 返回内置预设列表。
//
// **每个预设的 profile 都要经过 ResolveProfile 补全**，
// 不能直接返回 preset.Profile —— 那只是"与默认值的差异"，
// 前端渲染预设编辑界面时需要完整结构。
func (h *Blind) ListPresets(c echo.Context) error {
	presets := blind.BuiltinPresets()
	out := make([]map[string]any, 0, len(presets))
	for _, p := range presets {
		profile, _, err := blind.ResolveProfile(p.ID, nil)
		if err != nil {
			profile = map[string]any{}
		}
		out = append(out, map[string]any{
			"id":          p.ID,
			"name":        p.Name,
			"description": p.Description,
			"profile":     profile,
			// builtin：前端用它区分「内置预设」和「我的预设」，
			// 缺这个字段会导致 filter(preset.builtin) 结果为空，
			// 预设选择器里一个选项都没有。
			"builtin": true,
		})
	}
	return httpx.OK(c, out)
}

// selectPresetRequest 选择预设请求。
type selectPresetRequest struct {
	PresetID string `json:"presetId"`
}

// SelectPreset 选择格式预设。
func (h *Blind) SelectPreset(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req selectPresetRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.BadRequest("请先导入文件")
	}

	if err := h.q.UpdateBlindCheckPreset(ctx, store.UpdateBlindCheckPresetParams{
		TenantID: tenantID, ProjectID: projectID,
		PresetID:           req.PresetID,
		CustomProfile:      check.CustomProfile,
		ManualWords:        check.ManualWords,
		UseCredentialWords: check.UseCredentialWords,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("切换预设失败: %w", err))
	}

	return h.LoadState(c)
}

// setCredentialWordsRequest 设置是否用资信库。
type setCredentialWordsRequest struct {
	Enabled bool `json:"enabled"`
}

// SetUseCredentialWords 设置是否使用资信库的公司名/人名。
func (h *Blind) SetUseCredentialWords(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req setCredentialWordsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.BadRequest("请先导入文件")
	}

	if err := h.q.UpdateBlindCheckPreset(ctx, store.UpdateBlindCheckPresetParams{
		TenantID: tenantID, ProjectID: projectID,
		PresetID:           check.PresetID,
		CustomProfile:      check.CustomProfile,
		ManualWords:        check.ManualWords,
		UseCredentialWords: req.Enabled,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("更新敏感词设置失败: %w", err))
	}

	return h.LoadState(c)
}

// GetCredentialWords 返回资信库里的公司名/人名。
//
// 简化实现：从租户设置里读企业名称，以及 credential 相关表。
// 暂时只返回企业名称（人员库未实现）。
func (h *Blind) GetCredentialWords(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	out := make([]map[string]any, 0, 8)

	// 企业名称从 setting.extra 里读（键名 companyName）
	//
	// 说明：租户设置表还没有独立的 company_name 列，
	// 企业信息存在 extra JSONB 里。资信库（资质、人员、业绩）
	// 是独立模块，未实现前这里只返回企业名称。
	if settings, sErr := h.q.GetTenantSettings(ctx, tenantID); sErr == nil && len(settings.Extra) > 0 {
		var extra map[string]any
		if json.Unmarshal(settings.Extra, &extra) == nil {
			if name, ok := extra["companyName"].(string); ok && strings.TrimSpace(name) != "" {
				out = append(out, map[string]any{
					"label": "企业名称",
					"text":  name,
				})
			}
			if persons, ok := extra["personNames"].([]any); ok {
				for _, p := range persons {
					if name, ok := p.(string); ok && strings.TrimSpace(name) != "" {
						out = append(out, map[string]any{
							"label": "人员姓名",
							"text":  name,
						})
					}
				}
			}
		}
	}

	return httpx.OK(c, out)
}

// GetInputSignature 返回当前输入签名。
func (h *Blind) GetInputSignature(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil || check.FileName == "" {
		return httpx.OK(c, nil)
	}

	sig := blind.InputSignature(check.FileName, check.FileSize, check.PresetID, check.ManualWords, check.UseCredentialWords)
	return httpx.OK(c, sig)
}

// saveFixRulesRequest 保存修复规则。
type saveFixRulesRequest struct {
	Rules []string `json:"rules"`
}

// SaveFixRules 保存勾选的可修复规则。
//
// 前端在「修复项」清单里勾选时逐个调用，结果要持久化 ——
// 启动修复时前端只传 { ai }，规则由后端从库里读。
func (h *Blind) SaveFixRules(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req saveFixRulesRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := c.Request().Context()
	if _, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	}); getErr != nil {
		return httpx.BadRequest("请先导入文件")
	}

	// 过滤掉不可修复的规则
	rules := blind.NormalizeFixRules(req.Rules)
	if err := h.q.UpdateBlindCheckFixRules(ctx, store.UpdateBlindCheckFixRulesParams{
		TenantID: tenantID, ProjectID: projectID,
		FixRules: rules,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存修复项失败: %w", err))
	}

	return h.LoadState(c)
}

// SaveFixedDocument 下载修复后的文档。
func (h *Blind) SaveFixedDocument(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil {
		return httpx.NotFound("工作区不存在")
	}

	fix, fixErr := h.q.LatestBlindFix(ctx, check.ID)
	if fixErr != nil || fix.OutputFileName == "" {
		return httpx.NotFound("没有可下载的修复文档")
	}

	path := filepath.Join(h.workDir, tenantID.String()[:8], projectID.String()[:8], textutil.SafeFileName(fix.OutputFileName))
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return httpx.NotFound("修复文档已不存在")
	}

	c.Response().Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fix.OutputFileName))
	return c.Blob(200, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", data)
}

// Clear 清空工作区。
func (h *Blind) Clear(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	if err := h.q.DeleteBlindCheck(c.Request().Context(), store.DeleteBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("清空失败: %w", err))
	}

	return httpx.OK(c, emptyBlindWorkspace())
}

// StartCheck 执行检查。
func (h *Blind) StartCheck(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil || check.FileName == "" {
		return httpx.BadRequest("请先导入待检文件")
	}

	docxPath := filepath.Join(h.workDir, tenantID.String()[:8], projectID.String()[:8], textutil.SafeFileName(check.FileName))
	if _, statErr := os.Stat(docxPath); statErr != nil {
		return httpx.BadRequest("待检文件已不存在，请重新导入")
	}

	// 建结果记录
	sig := blind.InputSignature(check.FileName, check.FileSize, check.PresetID, check.ManualWords, check.UseCredentialWords)

	var customProfile map[string]any
	if len(check.CustomProfile) > 0 {
		_ = json.Unmarshal(check.CustomProfile, &customProfile)
	}
	_, presetName, _ := blind.ResolveProfile(check.PresetID, customProfile)

	result, err := h.q.CreateBlindCheckResult(ctx, store.CreateBlindCheckResultParams{
		CheckID:         check.ID,
		Status:          blind.StatusRunning,
		Progress:        10,
		ProgressMessage: "正在检查格式",
		InputSignature:  sig,
		PresetName:      presetName,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	// 收集资信库敏感词
	credWords := h.collectCredentialWords(ctx, tenantID)

	// 异步检查
	docxPathCopy := docxPath
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		report, inspectErr := h.svc.Inspect(bgCtx, blind.InspectOptions{
			TenantID:           tenantStr,
			ProjectID:          projectID.String(),
			DocxPath:           docxPathCopy,
			PresetID:           check.PresetID,
			CustomProfile:      customProfile,
			ManualWords:        check.ManualWords,
			CredentialWords:    credWords,
			UseCredentialWords: check.UseCredentialWords,
		})

		if inspectErr != nil {
			_ = h.q.UpdateBlindResultStatus(bgCtx, store.UpdateBlindResultStatusParams{
				ID:              result.ID,
				Status:          blind.StatusError,
				Progress:        100,
				ProgressMessage: "检查失败",
				Error:           pgtype.Text{String: inspectErr.Error(), Valid: true},
			})
			return
		}

		if saveErr := h.svc.SaveReport(bgCtx, result.ID, report); saveErr != nil {
			_ = h.q.UpdateBlindResultStatus(bgCtx, store.UpdateBlindResultStatusParams{
				ID:              result.ID,
				Status:          blind.StatusError,
				Progress:        100,
				ProgressMessage: "保存结果失败",
				Error:           pgtype.Text{String: saveErr.Error(), Valid: true},
			})
		}
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始检查",
	})
}

// startFixRequest 是修复请求。
type startFixRequest struct {
	// FixRules 勾选要修的规则 ID
	FixRules []string `json:"fixRules"`
}

// StartFix 执行自动修复。
//
// 流程：
//  1. 建修复记录（status=running）
//  2. 异步调 C# helper 的 blind-check-fix
//  3. 修复报告落库，产物放在工作区供下载
//
// **校验机制**：如果 helper 发现修复引入了新错误，
// 会把 new_errors 填上 —— 此时产物不该交付给用户。
func (h *Blind) StartFix(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}
	projectID, err := projectUUID(c)
	if err != nil {
		return err
	}

	var req startFixRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	// 规则优先用库里的（前端只传 { ai }，规则由 SaveFixRules 存好）
	fixRules := req.FixRules
	if len(fixRules) == 0 {
		fixRules = nil
	}

	ctx := c.Request().Context()
	check, getErr := h.q.GetBlindCheck(ctx, store.GetBlindCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if getErr != nil || check.FileName == "" {
		return httpx.BadRequest("请先导入待检文件")
	}

	dir := filepath.Join(h.workDir, tenantID.String()[:8], projectID.String()[:8])
	sourcePath := filepath.Join(dir, textutil.SafeFileName(check.FileName))
	if _, statErr := os.Stat(sourcePath); statErr != nil {
		return httpx.BadRequest("待检文件已不存在，请重新导入")
	}

	// 修复产物文件名：原文件名 + -fixed
	base := strings.TrimSuffix(check.FileName, filepath.Ext(check.FileName))
	outputName := base + "-fixed.docx"
	outputPath := filepath.Join(dir, textutil.SafeFileName(outputName))

	var customProfile map[string]any
	if len(check.CustomProfile) > 0 {
		_ = json.Unmarshal(check.CustomProfile, &customProfile)
	}

	// 建修复记录
	// 后端存的规则 + 请求里的规则，合并后过滤
	fixRules = blind.NormalizeFixRules(append(check.FixRules, fixRules...))
	if len(fixRules) == 0 {
		return httpx.BadRequest("没有可修复的规则，请先在修复项里勾选")
	}

	fix, err := h.q.CreateBlindFix(ctx, store.CreateBlindFixParams{
		CheckID:         check.ID,
		Status:          blind.StatusRunning,
		Progress:        10,
		ProgressMessage: "正在修复",
		FixRules:        fixRules,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	credWords := h.collectCredentialWords(ctx, tenantID)

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		report, fixErr := h.svc.Fix(bgCtx, blind.FixOptions{
			TenantID:           tenantStr,
			ProjectID:          projectID.String(),
			SourcePath:         sourcePath,
			OutputPath:         outputPath,
			PresetID:           check.PresetID,
			CustomProfile:      customProfile,
			ManualWords:        check.ManualWords,
			CredentialWords:    credWords,
			UseCredentialWords: check.UseCredentialWords,
			FixRules:           fixRules,
		})

		if fixErr != nil {
			_ = h.q.UpdateBlindFix(bgCtx, store.UpdateBlindFixParams{
				ID:              fix.ID,
				Status:          blind.StatusError,
				Progress:        100,
				ProgressMessage: "修复失败",
				Error:           pgtype.Text{String: fixErr.Error(), Valid: true},
				Validation:      nil,
				OutputFileName:  "",
			})
			return
		}

		// 校验失败：修复引入新错误，不交付产物
		if blind.FailedFixValidation(report) {
			validationJSON, _ := json.Marshal(report.Validation)
			_ = h.q.UpdateBlindFix(bgCtx, store.UpdateBlindFixParams{
				ID:              fix.ID,
				Status:          blind.StatusError,
				Progress:        100,
				ProgressMessage: fmt.Sprintf("修复引入了 %d 个新错误，未交付", len(report.Validation.NewErrors)),
				Error: pgtype.Text{
					String: "修复校验未通过：" + strings.Join(report.Validation.NewErrors, "；"),
					Valid:  true,
				},
				Validation:     validationJSON,
				OutputFileName: "",
			})
			// 删掉坏产物
			_ = os.Remove(outputPath)
			return
		}

		validationJSON, _ := json.Marshal(map[string]any{
			"checked":        report.Validation.Checked,
			"skippedReason":  report.Validation.SkippedReason,
			"originalErrors": report.Validation.OriginalErrors,
			"fixedErrors":    report.Validation.FixedErrors,
			"newErrors":      report.Validation.NewErrors,
			"fixes":          report.Fixes,
			"notes":          report.Notes,
			"sourceBytes":    report.SourceBytes,
			"outputBytes":    report.OutputBytes,
			"durationMs":     report.DurationMs,
			"beforeSummary":  report.After.Summary,
		})

		_ = h.q.UpdateBlindFix(bgCtx, store.UpdateBlindFixParams{
			ID:              fix.ID,
			Status:          blind.StatusSuccess,
			Progress:        100,
			ProgressMessage: fmt.Sprintf("修复完成，共处理 %d 类问题", len(report.Fixes)),
			Error:           pgtype.Text{},
			Validation:      validationJSON,
			OutputFileName:  outputName,
		})
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始修复",
	})
}

// collectCredentialWords 收集资信库里的敏感词。
func (h *Blind) collectCredentialWords(ctx context.Context, tenantID uuid.UUID) []string {
	out := make([]string, 0, 8)
	settings, err := h.q.GetTenantSettings(ctx, tenantID)
	if err != nil || len(settings.Extra) == 0 {
		return out
	}

	var extra map[string]any
	if json.Unmarshal(settings.Extra, &extra) != nil {
		return out
	}

	if name, ok := extra["companyName"].(string); ok && strings.TrimSpace(name) != "" {
		out = append(out, name)
	}
	if persons, ok := extra["personNames"].([]any); ok {
		for _, p := range persons {
			if name, ok := p.(string); ok && strings.TrimSpace(name) != "" {
				out = append(out, name)
			}
		}
	}

	return out
}

func emptyBlindWorkspace() blindWorkspaceView {
	return blindWorkspaceView{
		Step:               "upload",
		PresetID:           "builtin-general",
		PresetTab:          "builtin",
		ResultTab:          "all",
		UseCredentialWords: true,
		File:               nil,
		Result:             blindResultView{Status: "idle", Groups: []blindFindingView{}},
		Issues:             []blindIssueView{},
		FixRules:           blind.DefaultFixRules(),
		FixResult:          map[string]any{"status": "idle", "findings": []any{}},
		Review:             map[string]any{"status": "idle"},
	}
}

// copyFile 复制文件。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// normalizeFixRulesOrDefault 库里没存过用默认勾选，存过就用库里的。
func normalizeFixRulesOrDefault(rules []string) []string {
	if len(rules) == 0 {
		return blind.DefaultFixRules()
	}
	return blind.NormalizeFixRules(rules)
}

// buildFixItems 从修复报告生成前端要的 items。
//
// 前端 FixPanel 用 items[].fixed 求和显示「已修复 N 处」，
// 用 items[].pending 显示「待人工 N 处」。
//
// 数据来源：报告里的 fixes 是 {规则ID: 修了几处}，
// 这里按规则展开成条目。
func buildFixItems(validation map[string]any, fixRules []string) []map[string]any {
	items := make([]map[string]any, 0, 16)

	fixes, _ := validation["fixes"].(map[string]any)
	notes, _ := validation["notes"].([]any)

	// 有修复的规则 → fixed > 0
	for ruleID, countVal := range fixes {
		count := 0
		switch n := countVal.(type) {
		case float64:
			count = int(n)
		case int:
			count = n
		}
		if count == 0 {
			continue
		}

		items = append(items, map[string]any{
			"id":                 ruleID,
			"category":           ruleCategory(ruleID),
			"severity":           "medium",
			"message":            ruleMessage(ruleID),
			"count":              count,
			"fixed":              count,
			"pending":            0,
			"isNew":              false,
			"reason":             "",
			"suggestion":         "",
			"pendingOccurrences": []any{},
			"edits":              []any{},
		})
	}

	// 有说明的（修不了的）→ pending
	for _, noteVal := range notes {
		note, ok := noteVal.(string)
		if !ok || note == "" {
			continue
		}
		items = append(items, map[string]any{
			"id":                 "note-" + fmt.Sprintf("%d", len(items)),
			"category":           "other",
			"severity":           "low",
			"message":            note,
			"count":              1,
			"fixed":              0,
			"pending":            1,
			"isNew":              false,
			"reason":             note,
			"suggestion":         "需在 Word 中手动处理",
			"pendingOccurrences": []any{},
			"edits":              []any{},
		})
	}

	return items
}

// ruleCategory 从规则 ID 前缀推断分类。
func ruleCategory(ruleID string) string {
	switch {
	case strings.HasPrefix(ruleID, "page."):
		return "page"
	case strings.HasPrefix(ruleID, "para."):
		return "paragraph"
	case strings.HasPrefix(ruleID, "font."):
		return "font"
	case strings.HasPrefix(ruleID, "table."):
		return "table"
	case strings.HasPrefix(ruleID, "text."):
		return "text"
	case strings.HasPrefix(ruleID, "image."):
		return "image"
	case strings.HasPrefix(ruleID, "consistency."):
		return "consistency"
	case strings.HasPrefix(ruleID, "struct."), strings.HasPrefix(ruleID, "props."), strings.HasPrefix(ruleID, "privacy."):
		return "structure"
	}
	return "other"
}

// ruleMessage 给规则 ID 一个人话描述。
func ruleMessage(ruleID string) string {
	labels := map[string]string{
		"font.chinese":            "中文字体",
		"font.size":               "字号",
		"font.western":            "西文字体",
		"font.bold":               "加粗",
		"font.color":              "字体颜色",
		"page.paper_size":         "纸张大小",
		"page.margins":            "页边距",
		"page.columns":            "分栏",
		"para.line_spacing":       "行距",
		"para.first_line_indent":  "首行缩进",
		"para.space_before_after": "段前段后间距",
		"para.alignment":          "段落对齐",
		"struct.doc_props":        "文档属性",
		"struct.comment":          "批注",
		"struct.revision":         "修订痕迹",
		"text.hidden":             "隐藏文字",
		"struct.header_footer":    "页眉页脚",
		"struct.hyperlink":        "外部超链接",
		"struct.field":            "身份类域",
		"struct.bookmark":         "书签",
		"image.meta":              "图片元数据",
		"table.font":              "表格字体",
		"table.size":              "表格字号",
	}
	if label, ok := labels[ruleID]; ok {
		return label
	}
	return ruleID
}
