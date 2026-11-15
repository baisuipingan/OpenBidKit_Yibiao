package handler

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/store"
)

// Public 提供无需登录的公开信息。
type Public struct {
	q              *store.Queries
	demoAuthBypass bool
}

// NewPublic 创建 handler。
func NewPublic(pool *pgxpool.Pool, demoAuthBypass bool) *Public {
	return &Public{q: store.New(pool), demoAuthBypass: demoAuthBypass}
}

// Register 注册公开路由（无需登录）。
func (h *Public) Register(g *echo.Group) {
	g.GET("/public/bootstrap", h.Bootstrap)
}

// BootstrapInfo 是登录页需要的前置信息。
type BootstrapInfo struct {
	// TenantID 单租户部署时直接给出，前端不必手填。
	// 多租户部署为空，登录页需用户输入租户标识。
	TenantID   string `json:"tenantId"`
	TenantName string `json:"tenantName"`
	// MultiTenant 为 true 表示需要用户指定租户
	MultiTenant bool `json:"multiTenant"`
	// AppName 站点名称，便于白标部署
	AppName string `json:"appName"`
	// DemoAuthBypass 为 true 时，前端应跳过登录页，直接进入工作台。
	// 由服务端 DEMO_AUTH_BYPASS 控制；关掉后恢复登录门禁。
	DemoAuthBypass bool `json:"demoAuthBypass"`
}

// Bootstrap 返回登录页所需信息。
//
// 设计取舍：单租户部署时直接返回租户 ID，省掉用户手填的麻烦。
// 这不是安全风险——租户 ID 本身不是凭据，登录仍需邮箱密码。
func (h *Public) Bootstrap(c echo.Context) error {
	ctx := c.Request().Context()

	count, err := h.q.CountActiveTenants(ctx)
	if err != nil {
		return httpx.Internal(err)
	}

	info := BootstrapInfo{
		MultiTenant:    count > 1,
		AppName:        "标书工作台",
		DemoAuthBypass: h.demoAuthBypass,
	}

	if count == 1 {
		t, err := h.q.GetSoleTenant(ctx)
		if err == nil {
			info.TenantID = t.ID.String()
			info.TenantName = t.Name
		}
	}

	return httpx.OK(c, info)
}
