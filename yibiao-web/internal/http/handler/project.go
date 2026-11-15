package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/store"
)

// Project 处理项目管理。
//
// 为什么需要：
//
//	Web 版是多项目环境，前端所有业务接口都需要 X-Project-Id。
//	桌面版单机单项目，没有这个概念。
//
//	前端启动时调 /projects/current 拿到当前项目（没有就自动创建），
//	之后所有请求都带上这个 ID。
type Project struct {
	q *store.Queries
}

// NewProject 创建 handler。
func NewProject(q *store.Queries) *Project {
	return &Project{q: q}
}

// Register 注册路由。
func (p *Project) Register(g *echo.Group) {
	g.POST("/projects/current", p.Current)
	g.POST("/projects/list", p.List)
	g.POST("/projects/create", p.Create)
	g.POST("/projects/select", p.Select)
	g.POST("/projects/rename", p.Rename)
	g.POST("/projects/delete", p.Delete)
	// 前端方法名是 projects.remove，bridge 会映射成 /projects/remove
	g.POST("/projects/remove", p.Delete)
}

// projectView 是项目视图。
//
// 带进度摘要：多项目场景下用户需要一眼看出「哪个项目在做什么、
// 做到哪一步了」，否则只能靠项目名猜。
type projectView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TenantID  string    `json:"tenantId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// 进度摘要
	TenderFileName   string `json:"tenderFileName,omitempty"`
	OutlineNodeCount int    `json:"outlineNodeCount"`
	ContentCount     int    `json:"contentCount"`
	// Step 当前步骤（document-analysis / outline-generation / content-edit …）
	Step string `json:"step,omitempty"`
	// RunningTasks 运行中的任务数
	RunningTasks int `json:"runningTasks"`
}

// Current 返回当前项目。
//
// 逻辑：
//  1. 请求里带了 projectId → 用它
//  2. 该租户有项目 → 用最新的
//  3. 都没有 → 自动创建一个「默认项目」
//
// 这样前端启动就能拿到可用项目，不需要额外的引导流程。
func (p *Project) Current(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	// 请求指定了项目
	var req struct {
		ProjectID string `json:"projectId"`
	}
	_ = c.Bind(&req)
	if req.ProjectID != "" {
		pid, parseErr := uuid.Parse(req.ProjectID)
		if parseErr == nil {
			if proj, getErr := p.q.GetProject(c.Request().Context(), store.GetProjectParams{
				ID: pid, TenantID: tenantID,
			}); getErr == nil {
				return httpx.OK(c, p.toProjectViewWithProgress(c.Request().Context(), proj))
			}
		}
	}

	// 取该租户最新的项目
	list, err := p.q.ListProjectsByTenant(c.Request().Context(), store.ListProjectsByTenantParams{
		TenantID: tenantID,
		Limit:    20,
		Offset:   0,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("查询项目失败: %w", err))
	}
	if len(list) > 0 {
		return httpx.OK(c, p.toProjectViewWithProgress(c.Request().Context(), list[0]))
	}

	// 没有任何项目 → 自动创建
	name := fmt.Sprintf("默认项目 %s", time.Now().Format("2006-01-02"))
	created, err := p.q.CreateProject(c.Request().Context(), store.CreateProjectParams{
		TenantID: tenantID,
		Name:     name,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("创建默认项目失败: %w", err))
	}

	return httpx.OK(c, p.toProjectViewWithProgress(c.Request().Context(), created))
}

// List 列出该租户的全部项目。
func (p *Project) List(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	list, err := p.q.ListProjectsByTenant(c.Request().Context(), store.ListProjectsByTenantParams{
		TenantID: tenantID,
		Limit:    50,
		Offset:   0,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("查询项目失败: %w", err))
	}

	items := make([]projectView, 0, len(list))
	for _, proj := range list {
		items = append(items, p.toProjectViewWithProgress(c.Request().Context(), proj))
	}

	return httpx.OK(c, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// createProjectRequest 是创建项目的请求。
type createProjectRequest struct {
	Name string `json:"name"`
}

// Create 创建项目。
func (p *Project) Create(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req createProjectRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("项目 %s", time.Now().Format("2006-01-02 15:04"))
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}

	created, err := p.q.CreateProject(c.Request().Context(), store.CreateProjectParams{
		TenantID: tenantID,
		Name:     name,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("创建项目失败: %w", err))
	}

	return httpx.OK(c, toProjectView(created))
}

// selectProjectRequest 是选择项目的请求。
type selectProjectRequest struct {
	ProjectID string `json:"projectId"`
}

// Select 校验项目存在并返回（前端拿到后写入 localStorage）。
func (p *Project) Select(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req selectProjectRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	pid, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return httpx.BadRequest("项目标识非法")
	}

	proj, err := p.q.GetProject(c.Request().Context(), store.GetProjectParams{
		ID: pid, TenantID: tenantID,
	})
	if err != nil {
		return httpx.NotFound("项目不存在")
	}

	return httpx.OK(c, toProjectView(proj))
}

// renameProjectRequest 是重命名请求。
type renameProjectRequest struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
}

// Rename 重命名项目。
func (p *Project) Rename(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req renameProjectRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.ProjectID == "" {
		return httpx.BadRequest("缺少 projectId")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return httpx.BadRequest("项目名不能为空")
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}

	pid, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return httpx.BadRequest("项目标识非法")
	}

	// 先读原值，保留 code / description（UpdateProject 要求全字段）
	proj, err := p.q.GetProject(c.Request().Context(), store.GetProjectParams{
		ID: pid, TenantID: tenantID,
	})
	if err != nil {
		return httpx.NotFound("项目不存在")
	}

	if err := p.q.UpdateProject(c.Request().Context(), store.UpdateProjectParams{
		ID:          pid,
		TenantID:    tenantID,
		Name:        name,
		Code:        proj.Code,
		Description: proj.Description,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("重命名失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"renamed": true, "name": name})
}

// deleteProjectRequest 是删除请求。
type deleteProjectRequest struct {
	ProjectID string `json:"projectId"`
}

// Delete 删除项目（软删除）。
//
// 只标记删除，不物理清除数据 —— 误删还能恢复。
// 物理清理交给定时任务（按软删除时间超过 N 天）。
func (p *Project) Delete(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req deleteProjectRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	pid, err := uuid.Parse(req.ProjectID)
	if err != nil {
		return httpx.BadRequest("项目标识非法")
	}

	// 不允许删最后一个项目（否则前端没有可用项目）
	count, err := p.q.CountProjectsByTenant(c.Request().Context(), tenantID)
	if err == nil && count <= 1 {
		return httpx.Fail(c, "LAST_PROJECT", "不能删除最后一个项目")
	}

	if err := p.q.SoftDeleteProject(c.Request().Context(), store.SoftDeleteProjectParams{
		ID: pid, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("删除失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"deleted": true})
}

// toProjectView 转视图（不带进度，列表批量查询时用）。
func toProjectView(p store.Project) projectView {
	return projectView{
		ID:        p.ID.String(),
		Name:      p.Name,
		TenantID:  p.TenantID.String(),
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// toProjectViewWithProgress 转视图并附带进度摘要。
//
// 每个项目要查两次（目录节点、运行中任务），
// 列表接口限制 50 个项目，开销可接受。
func (p *Project) toProjectViewWithProgress(ctx context.Context, proj store.Project) projectView {
	v := toProjectView(proj)
	projectID := proj.ID

	// 目录节点数 + 正文数
	if nodes, err := p.q.ListOutlineNodes(ctx, store.ListOutlineNodesParams{
		ProjectID: projectID,
		TenantID:  proj.TenantID,
	}); err == nil {
		v.OutlineNodeCount = len(nodes)
		for _, n := range nodes {
			if n.Content != "" {
				v.ContentCount++
			}
		}
	}

	// 技术方案元信息（招标文件名 + 当前步骤）
	if meta, err := p.q.GetTechnicalPlanMeta(ctx, store.GetTechnicalPlanMetaParams{
		ProjectID: projectID,
		TenantID:  proj.TenantID,
	}); err == nil {
		if meta.Step != "" {
			v.Step = meta.Step
		}
		v.TenderFileName = firstTenderFile(meta.TenderFiles)
	}

	// 运行中任务
	if n, err := p.q.CountRunningTasksByProject(ctx, store.CountRunningTasksByProjectParams{
		ProjectID: projectID,
		TenantID:  proj.TenantID,
	}); err == nil {
		v.RunningTasks = int(n)
	}

	return v
}

// 确保 context 被使用
var _ = context.Background

// firstTenderFile 从招标文件 JSON 取第一个文件名。
//
// 数据库存的是 [{"fileName":"x.docx","chars":1234,...}]。
// 这里只取名字用于列表展示，不做完整解析。
func firstTenderFile(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []struct {
		FileName string `json:"fileName"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return ""
	}
	return arr[0].FileName
}
