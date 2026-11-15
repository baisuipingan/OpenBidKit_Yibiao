// Package middleware 提供 HTTP 中间件。
package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/http/httpx"
)

// Context key。用 string 常量而非自定义类型，
// 是为了能在 echo 的 map 里直接读写，同时集中定义避免拼写错误。
const (
	CtxUserID   = "auth.user_id"
	CtxTenantID = "auth.tenant_id"
	CtxRole     = "auth.role"
	CtxProject  = "req.project_id"
)

// Auth 校验 Bearer 令牌并把身份写入 context。
//
// 关键：tenant_id 只从令牌里取，绝不从请求头/请求体取。
// 这是租户隔离的第一道闸——即使前端传了别的 tenant_id 也不认。
func Auth(mgr *auth.Manager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw := bearerToken(c)
			if raw == "" {
				return httpx.Unauthorized("缺少访问令牌")
			}

			claims, err := mgr.ParseAccessToken(raw)
			if err != nil {
				return err
			}

			c.Set(CtxUserID, claims.UserID)
			c.Set(CtxTenantID, claims.TenantID)
			c.Set(CtxRole, claims.Role)

			// 同步到 request context，供下游 service 用 context 取值
			ctx := auth.WithIdentity(c.Request().Context(), claims.UserID, claims.TenantID, claims.Role)
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}

// RequireRole 限制角色。必须在 Auth 之后使用。
func RequireRole(roles ...string) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, _ := c.Get(CtxRole).(string)
			if _, ok := allowed[role]; !ok {
				return httpx.Forbidden("需要更高权限")
			}
			return next(c)
		}
	}
}

// ProjectScope 从 X-Project-Id 或路径参数读取项目 ID，写入 context。
//
// 只做「取出来」这一件事，归属校验由 service 层查库完成
// （必须验证该项目属于当前租户，否则是越权）。
func ProjectScope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			pid := c.Request().Header.Get("X-Project-Id")
			if pid == "" {
				pid = c.Param("projectId")
			}
			if pid != "" {
				c.Set(CtxProject, pid)
				ctx := auth.WithProject(c.Request().Context(), pid)
				c.SetRequest(c.Request().WithContext(ctx))
			}
			return next(c)
		}
	}
}

// bearerToken 从 Authorization 头提取 Bearer 令牌。
func bearerToken(c echo.Context) string {
	h := c.Request().Header.Get("Authorization")
	if h == "" {
		// 兼容 SSE：EventSource 不能设置请求头，退化为 query 参数
		return c.QueryParam("access_token")
	}
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}
