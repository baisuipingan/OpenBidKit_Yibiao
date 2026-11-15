package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/http/middleware"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// TechnicalPlan 处理技术方案接口。
//
// 路由命名刻意与 bridge 方法对齐（kebab-case），
// 这样前端 web.ts 的 Proxy 适配层可以直接映射，无需映射表。
// 见 docs/web-migration/01-bridge-contract.md。
type TechnicalPlan struct {
	svc       *service.TechnicalPlanService
	uploadSvc *upload.Service
	q         *store.Queries
}

// SetQueries 注入 SQL 查询（用于少量直查）。
func (h *TechnicalPlan) SetQueries(q *store.Queries) {
	h.q = q
}

// NewTechnicalPlan 创建 handler。
func NewTechnicalPlan(svc *service.TechnicalPlanService, uploadSvc *upload.Service) *TechnicalPlan {
	return &TechnicalPlan{svc: svc, uploadSvc: uploadSvc}
}

// Register 注册路由。已登录 + 项目作用域。
func (h *TechnicalPlan) Register(g *echo.Group) {
	g.POST("/technical-plan/load-state", h.LoadState)
	g.POST("/technical-plan/update-step", h.UpdateStep)
	g.POST("/technical-plan/import-tender-document", h.ImportTenderDocument)
	g.POST("/technical-plan/read-tender-markdown", h.ReadTenderMarkdown)
	g.POST("/technical-plan/remove-tender-document", h.RemoveTenderDocument)
	g.POST("/technical-plan/import-original-plan-document", h.ImportOriginalPlanDocument)
	// 保存单节正文（手工编辑 / 测试用）
	g.POST("/technical-plan/save-section-content", h.SaveSectionContent)
	g.POST("/technical-plan/save-reference-knowledge", h.SaveReferenceKnowledge)
	h.registerExtraRoutes(g)
}

// LoadState 对应 bridge 的 technicalPlan.loadState()。
func (h *TechnicalPlan) LoadState(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	state, err := h.svc.LoadState(c.Request().Context(), tenantID, projectID)
	if err != nil {
		return err
	}
	return httpx.OK(c, state)
}

type updateStepRequest struct {
	Step string `json:"step"`
}

// UpdateStep 对应 bridge 的 technicalPlan.updateStep(step)。
func (h *TechnicalPlan) UpdateStep(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req updateStepRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	if err := h.svc.UpdateStep(c.Request().Context(), tenantID, projectID, req.Step); err != nil {
		return err
	}
	return httpx.OK(c, map[string]bool{"success": true})
}

// --- 公共小工具 ---

func identity(c echo.Context) (tenantID, userID string) {
	t, _ := c.Get(middleware.CtxTenantID).(string)
	u, _ := c.Get(middleware.CtxUserID).(string)
	return t, u
}

func projectID(c echo.Context) (string, error) {
	p, _ := c.Get(middleware.CtxProject).(string)
	if p == "" {
		// 允许放在 body 里，兼容 bridge 的调用形态
		var body struct {
			ProjectID string `json:"projectId"`
		}
		_ = c.Bind(&body)
		p = body.ProjectID
	}
	if p == "" {
		return "", httpx.BadRequest("缺少项目标识（X-Project-Id 或 projectId）")
	}
	return p, nil
}

// ImportTenderRequest 是导入招标文件的请求。
//
// 前端传的是路径数组（桌面版是本地绝对路径，Web 版是 upload:// 伪路径）。
type ImportTenderRequest struct {
	// Paths 为兼容前端 importTenderDocument(filePaths) 的调用形态，
	// 可能是 ["path1"] 或直接被展开成 {"0":"path1"}（取决于序列化）
	Paths []string `json:"paths"`
	// 兼容：前端只传一个路径时
	SinglePath string `json:"singlePath"`
}

// ImportTenderDocument 导入招标文件。
//
// 对应前端 `technicalPlan.importTenderDocument()`。
//
// 流程：
//  1. 把 upload:// 伪路径解析成真实路径
//  2. 解析文档（docx/pdf → markdown）
//  3. 保存为资产
//  4. 更新 technical_plan_meta 的引用
func (h *TechnicalPlan) ImportTenderDocument(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	// 前端可能传数组也可能传单个字符串，用通用方式接收
	body := c.Request().Body
	raw, readErr := io.ReadAll(io.LimitReader(body, 1<<20))
	if readErr != nil {
		return httpx.BadRequest("读取请求失败")
	}

	paths := parseImportPaths(raw)
	if len(paths) == 0 {
		return httpx.Fail(c, "MISSING_FILE", "未选择招标文件")
	}

	resolved, err := h.resolveUploadPaths(paths)
	if err != nil {
		return httpx.Fail(c, "UPLOAD_PENDING", err.Error())
	}

	result, err := h.svc.ImportTender(c.Request().Context(), tenantID, projectID, userID, resolved)
	if err != nil {
		return httpx.Fail(c, "IMPORT_FAILED", err.Error())
	}

	return httpx.OK(c, result)
}

// parseImportPaths 从请求体里提取文件路径数组。
//
// 兼容三种形态：
//   - ["a", "b"]                     标准数组
//   - {"paths": ["a"]}               带包装
//   - "a"                            单个字符串
func parseImportPaths(raw []byte) []string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return nil
	}

	// 尝试数组
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr
	}

	// 尝试带包装的对象
	var obj struct {
		Paths      []string `json:"paths"`
		FilePaths  []string `json:"filePaths"`
		SinglePath string   `json:"path"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err == nil {
		if len(obj.Paths) > 0 {
			return obj.Paths
		}
		if len(obj.FilePaths) > 0 {
			return obj.FilePaths
		}
		if obj.SinglePath != "" {
			return []string{obj.SinglePath}
		}
	}

	// 尝试单个字符串
	var str string
	if err := json.Unmarshal([]byte(s), &str); err == nil && str != "" {
		return []string{str}
	}

	return nil
}

// resolveUploadPaths 把 upload:// 伪路径解析成真实路径。
func (h *TechnicalPlan) resolveUploadPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		real, err := h.uploadSvc.Resolve(p)
		if err != nil {
			return nil, fmt.Errorf("文件 %s 尚不可用: %w", p, err)
		}
		out = append(out, real)
	}
	return out, nil
}

// SetUploadService 注入上传服务。
func (h *TechnicalPlan) SetUploadService(svc *upload.Service) {
	h.uploadSvc = svc
}

// ReadTenderMarkdown 读取招标文件正文。
//
// 对应前端 `technicalPlan.readTenderMarkdown()`。
func (h *TechnicalPlan) ReadTenderMarkdown(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	content, err := h.svc.ReadTenderMarkdown(c.Request().Context(), tenantID, projectID)
	if err != nil {
		return httpx.Fail(c, "READ_FAILED", err.Error())
	}

	return httpx.OK(c, map[string]any{"markdown": content})
}

// ImportOriginalPlanDocument 导入原方案（扩写模式用）。
func (h *TechnicalPlan) ImportOriginalPlanDocument(c echo.Context) error {
	tenantID, userID := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	raw, readErr := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if readErr != nil {
		return httpx.BadRequest("读取请求失败")
	}

	paths := parseImportPaths(raw)
	if len(paths) == 0 {
		return httpx.Fail(c, "MISSING_FILE", "未选择原方案文件")
	}

	resolved, err := h.resolveUploadPaths(paths)
	if err != nil {
		return httpx.Fail(c, "UPLOAD_PENDING", err.Error())
	}

	result, err := h.svc.ImportOriginalPlan(c.Request().Context(), tenantID, projectID, userID, resolved)
	if err != nil {
		return httpx.Fail(c, "IMPORT_FAILED", err.Error())
	}

	return httpx.OK(c, result)
}

// saveSectionContentRequest 是保存单节正文的请求。
type saveSectionContentRequest struct {
	NodeID  string `json:"nodeId"`
	Content string `json:"content"`
}

// SaveSectionContent 保存单节正文。
//
// 用途：手工编辑正文、或者测试时注入内容。
// 正文生成任务不走这个接口（它用 SaveSectionContent 的内部实现）。
func (h *TechnicalPlan) SaveSectionContent(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req saveSectionContentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.NodeID == "" {
		return httpx.BadRequest("缺少 nodeId")
	}

	if err := h.svc.SaveSectionContent(c.Request().Context(), tenantID, projectID, req.NodeID, req.Content); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]any{"success": true, "chars": len([]rune(req.Content))})
}

// RemoveTenderDocument 移除招标文件。
func (h *TechnicalPlan) RemoveTenderDocument(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	if err := h.svc.RemoveTender(c.Request().Context(), tenantID, projectID); err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, map[string]bool{"success": true})
}

// saveReferenceKnowledgeRequest 保存参考知识库文档。
type saveReferenceKnowledgeRequest struct {
	// DocumentIDs 勾选的文档 ID 列表
	DocumentIDs []string `json:"documentIds"`
}

// SaveReferenceKnowledge 保存「正文生成参考哪些知识库文档」。
//
// 用户可能在设置页勾选特定文档作为素材来源（而不是用全部）。
// 这个选择在全项目范围内生效（目录生成和正文生成共用）。
func (h *TechnicalPlan) SaveReferenceKnowledge(c echo.Context) error {
	tenantStr, _ := identity(c)
	projectStr, err := projectID(c)
	if err != nil {
		return err
	}

	var req saveReferenceKnowledgeRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	tenantUUID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}
	projectUUID, err := uuid.Parse(projectStr)
	if err != nil {
		return httpx.BadRequest("项目标识非法")
	}

	if err := h.q.SetReferenceKnowledgeDocs(c.Request().Context(), store.SetReferenceKnowledgeDocsParams{
		ProjectID:                     projectUUID,
		TenantID:                      tenantUUID,
		ReferenceKnowledgeDocumentIds: req.DocumentIDs,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存失败: %w", err))
	}

	return httpx.OK(c, map[string]any{
		"success": true,
		"count":   len(req.DocumentIDs),
	})
}
