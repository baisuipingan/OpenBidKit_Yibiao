package handler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/store"
)

// Template 处理导出模板接口。
type Template struct {
	q *store.Queries
}

// NewTemplate 创建 handler。
func NewTemplate(q *store.Queries) *Template {
	return &Template{q: q}
}

// Register 注册路由。
func (t *Template) Register(g *echo.Group) {
	g.POST("/templates/list", t.List)
	g.POST("/templates/get", t.Get)
	g.POST("/templates/create", t.Create)
	g.POST("/templates/update", t.Update)
	g.POST("/templates/delete", t.Delete)
	g.POST("/templates/duplicate", t.Duplicate)
	// 预览：返回生成的 docx 字节
	g.POST("/templates/render-preview", t.RenderPreview)
}

// templateView 对齐前端 ExportTemplateRecord。
type templateView struct {
	TemplateID   string          `json:"template_id"`
	TemplateName string          `json:"template_name"`
	Config       json.RawMessage `json:"config"`
	IsSystem     bool            `json:"is_system"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

func toTemplateView(row store.ExportTemplate) templateView {
	cfg := json.RawMessage(row.Config)
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	return templateView{
		TemplateID:   row.ID.String(),
		TemplateName: row.Name,
		Config:       cfg,
		IsSystem:     row.IsSystem,
		CreatedAt:    row.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    row.UpdatedAt.Format(time.RFC3339),
	}
}

// List 返回模板列表。
func (t *Template) List(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	rows, err := t.q.ListExportTemplates(c.Request().Context(), tenantID)
	if err != nil {
		return httpx.Internal(err)
	}

	out := make([]templateView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTemplateView(r))
	}
	return httpx.OK(c, out)
}

// getRequest 单个模板请求。
type templateIDRequest struct {
	TemplateID string `json:"templateId"`
	// 兼容蛇形
	TemplateIDAlt string `json:"template_id"`
}

func (r templateIDRequest) id() string {
	if r.TemplateID != "" {
		return r.TemplateID
	}
	return r.TemplateIDAlt
}

// Get 返回单个模板。
func (t *Template) Get(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	var req templateIDRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	id, parseErr := uuid.Parse(req.id())
	if parseErr != nil {
		return httpx.BadRequest("模板标识非法")
	}

	row, err := t.q.GetExportTemplate(c.Request().Context(), store.GetExportTemplateParams{
		ID: id, TenantID: tenantID,
	})
	if err != nil {
		// 前端 get 用于"找不到就新建"，返回 null 而不是错误
		return httpx.OK(c, nil)
	}
	return httpx.OK(c, toTemplateView(row))
}

// createRequest 创建模板请求。
type createTemplateRequest struct {
	Config json.RawMessage `json:"config"`
	// 兼容 name 字段
	Name string `json:"name"`
}

// Create 创建模板。
func (t *Template) Create(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	var req createTemplateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if len(req.Config) == 0 {
		return httpx.BadRequest("缺少排版配置")
	}

	name := req.Name
	if name == "" {
		name = extractTemplateName(req.Config)
	}
	if name == "" {
		name = "未命名模板"
	}

	ctx := c.Request().Context()

	// 同名就加序号（避免唯一索引冲突导致用户操作失败）
	finalName := name
	for i := 2; i <= 50; i++ {
		_, err := t.q.CreateExportTemplate(ctx, store.CreateExportTemplateParams{
			TenantID: tenantID,
			Name:     finalName,
			Config:   req.Config,
			IsSystem: false,
		})
		if err == nil {
			rows, listErr := t.q.ListExportTemplates(ctx, tenantID)
			if listErr != nil {
				return httpx.OK(c, map[string]any{"success": true})
			}
			for _, r := range rows {
				if r.Name == finalName {
					return httpx.OK(c, toTemplateView(r))
				}
			}
			return httpx.OK(c, map[string]any{"success": true})
		}
		finalName = fmt.Sprintf("%s (%d)", name, i)
	}

	return httpx.Fail(c, "TEMPLATE_NAME_CONFLICT", "模板名称重复过多，请换个名字")
}

// extractTemplateName 从配置里取模板名。
func extractTemplateName(raw json.RawMessage) string {
	var cfg struct {
		TemplateName string `json:"template_name"`
	}
	if json.Unmarshal(raw, &cfg) == nil {
		return cfg.TemplateName
	}
	return ""
}

// updateRequest 更新模板请求。
type updateTemplateRequest struct {
	TemplateID string          `json:"templateId"`
	Config     json.RawMessage `json:"config"`
	Name       string          `json:"name"`
}

// Update 更新模板。
func (t *Template) Update(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	var req updateTemplateRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	id, parseErr := uuid.Parse(req.TemplateID)
	if parseErr != nil {
		return httpx.BadRequest("模板标识非法")
	}

	ctx := c.Request().Context()

	row, getErr := t.q.GetExportTemplate(ctx, store.GetExportTemplateParams{
		ID: id, TenantID: tenantID,
	})
	if getErr != nil {
		return httpx.NotFound("模板不存在")
	}
	if row.IsSystem {
		return httpx.Fail(c, "SYSTEM_TEMPLATE_READONLY", "系统预设模板不可编辑，请先复制一份")
	}

	name := req.Name
	if name == "" {
		name = extractTemplateName(req.Config)
	}
	if name == "" {
		name = row.Name
	}

	cfg := req.Config
	if len(cfg) == 0 {
		cfg = row.Config
	}

	if err := t.q.UpdateExportTemplate(ctx, store.UpdateExportTemplateParams{
		ID: id, TenantID: tenantID, Name: name, Config: cfg,
	}); err != nil {
		return httpx.Internal(err)
	}

	updated, err := t.q.GetExportTemplate(ctx, store.GetExportTemplateParams{
		ID: id, TenantID: tenantID,
	})
	if err != nil {
		return httpx.OK(c, map[string]any{"success": true})
	}
	return httpx.OK(c, toTemplateView(updated))
}

// Delete 删除模板。
func (t *Template) Delete(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	var req templateIDRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	id, parseErr := uuid.Parse(req.id())
	if parseErr != nil {
		return httpx.BadRequest("模板标识非法")
	}

	if err := t.q.DeleteExportTemplate(c.Request().Context(), store.DeleteExportTemplateParams{
		ID: id, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]any{"success": true})
}

// Duplicate 复制模板（系统预设也允许复制）。
func (t *Template) Duplicate(c echo.Context) error {
	tenantID, err := tenantUUID(c)
	if err != nil {
		return err
	}

	var req templateIDRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	id, parseErr := uuid.Parse(req.id())
	if parseErr != nil {
		return httpx.BadRequest("模板标识非法")
	}

	ctx := c.Request().Context()
	src, getErr := t.q.GetExportTemplate(ctx, store.GetExportTemplateParams{
		ID: id, TenantID: tenantID,
	})
	if getErr != nil {
		return httpx.NotFound("模板不存在")
	}

	name := src.Name + " 副本"

	// 重名就加序号
	var created *store.ExportTemplate
	for i := 1; i <= 50; i++ {
		tryName := name
		if i > 1 {
			tryName = fmt.Sprintf("%s (%d)", name, i)
		}
		row, createErr := t.q.CreateExportTemplate(ctx, store.CreateExportTemplateParams{
			TenantID: tenantID,
			Name:     tryName,
			Config:   src.Config,
			IsSystem: false,
		})
		if createErr == nil {
			created = &row
			break
		}
	}
	if created == nil {
		return httpx.Fail(c, "DUPLICATE_FAILED", "复制失败，请稍后重试")
	}

	return httpx.OK(c, toTemplateView(*created))
}

// RenderPreview 渲染模板样张。
//
// 简化实现：Web 版暂不生成预览 docx，返回空结果让前端走降级路径。
// 完整实现需要把 ExportFormatConfig 转成 HTML 再走 openxml 渲染，
// 工作量较大且对用户体验影响有限（用户可以导出真实文档看效果）。
func (t *Template) RenderPreview(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"key":   "",
		"bytes": nil,
		"roles": []string{},
	})
}
