package handler

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/http/middleware"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/store"
)

// Auth 处理登录相关接口。
type Auth struct {
	svc *service.AuthService
	// defaultTenantID 单租户阶段允许登录时不传租户。
	defaultTenantID string
	// publicQ 用于在未显式配置默认租户时自动查找唯一租户。
	publicQ *store.Queries
}

// NewAuth 创建 handler。
func NewAuth(svc *service.AuthService, defaultTenantID string, pool *pgxpool.Pool) *Auth {
	return &Auth{
		svc:             svc,
		defaultTenantID: defaultTenantID,
		publicQ:         store.New(pool),
	}
}

// Register 注册公开路由（无需登录）。
func (h *Auth) Register(g *echo.Group) {
	g.POST("/login", h.Login)
	g.POST("/refresh", h.Refresh)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TenantSlug 多租户时必填；单租户可用默认值
	TenantSlug string `json:"tenantSlug"`
	TenantID   string `json:"tenantId"`
}

// Login 邮箱密码登录。
func (h *Auth) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		return httpx.BadRequest("邮箱和密码不能为空")
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = h.defaultTenantID
	}
	// 单租户部署：自动用唯一租户，用户不必知道租户 ID
	if tenantID == "" {
		if t, err := h.autoTenant(c.Request().Context()); err == nil {
			tenantID = t
		}
	}
	if tenantID == "" {
		return httpx.BadRequest("缺少租户标识，请在请求中指定 tenantId")
	}

	result, err := h.svc.Login(
		c.Request().Context(),
		tenantID,
		req.Email,
		req.Password,
		c.Request().UserAgent(),
		clientIP(c),
	)
	if err != nil {
		return err
	}
	return httpx.OK(c, result)
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// Refresh 用刷新令牌换新令牌。
func (h *Auth) Refresh(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.RefreshToken == "" {
		return httpx.BadRequest("缺少刷新令牌")
	}

	result, err := h.svc.Refresh(
		c.Request().Context(),
		req.RefreshToken,
		c.Request().UserAgent(),
		clientIP(c),
	)
	if err != nil {
		return err
	}
	return httpx.OK(c, result)
}

// Me 返回当前用户。需登录。
func (h *Auth) Me(c echo.Context) error {
	userID, _ := c.Get(middleware.CtxUserID).(string)
	tenantID, _ := c.Get(middleware.CtxTenantID).(string)

	info, err := h.svc.Me(c.Request().Context(), userID, tenantID)
	if err != nil {
		return err
	}
	return httpx.OK(c, info)
}

// Logout 撤销当前会话。需登录。
func (h *Auth) Logout(c echo.Context) error {
	var req refreshRequest
	// 登出允许空 body
	_ = c.Bind(&req)

	if err := h.svc.Logout(c.Request().Context(), req.RefreshToken); err != nil {
		return err
	}
	return httpx.OK(c, map[string]bool{"success": true})
}

// autoTenant 在部署只有一个租户时返回它。
// 多租户部署返回空串，调用方需回退到「要求前端指定」。
func (h *Auth) autoTenant(ctx context.Context) (string, error) {
	count, err := h.publicQ.CountActiveTenants(ctx)
	if err != nil || count != 1 {
		return "", fmt.Errorf("租户不唯一")
	}
	t, err := h.publicQ.GetSoleTenant(ctx)
	if err != nil {
		return "", err
	}
	return t.ID.String(), nil
}

// clientIP 取真实客户端 IP。信任反代的 X-Forwarded-For 第一段。
//
// 解析失败返回零值 Addr，数据库里 inet 列会存 NULL。
func clientIP(c echo.Context) netip.Addr {
	if xff := c.Request().Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			if ip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
				return ip
			}
		}
	}
	host := c.Request().RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip
	}
	return netip.Addr{}
}
