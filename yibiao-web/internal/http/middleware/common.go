package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/observ"
)

// RequestID 为每个请求生成 ID，并写入响应头与 context。
func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get("X-Request-Id")
			if id == "" {
				id = newID()
			}
			c.Response().Header().Set("X-Request-Id", id)
			ctx := observ.WithRequestID(c.Request().Context(), id)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// Logger 记录访问日志，并把请求级 logger 放进 context。
func Logger(base *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			req := c.Request()

			reqLog := base.With(
				slog.String("request_id", observ.RequestIDFrom(req.Context())),
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
			)

			// 放进 context，业务代码用 observ.LoggerFrom(ctx) 取
			c.SetRequest(req.WithContext(observ.WithLogger(req.Context(), reqLog)))

			err := next(c)
			if err != nil {
				c.Error(err)
			}

			status := c.Response().Status
			dur := time.Since(start)

			attrs := []any{
				slog.Int("status", status),
				slog.Duration("duration", dur),
				slog.Int64("bytes", c.Response().Size),
			}
			// 登录后再补身份，便于按用户排查
			if uid, ok := c.Get(CtxUserID).(string); ok && uid != "" {
				attrs = append(attrs, slog.String("user_id", uid))
			}
			if status >= 500 {
				reqLog.Error("请求失败", attrs...)
			} else if status >= 400 {
				reqLog.Warn("请求异常", attrs...)
			} else {
				reqLog.Info("请求完成", attrs...)
			}
			return nil
		}
	}
}

// Recover 捕获 panic，避免单个请求崩掉整个进程。
func Recover(base *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			defer func() {
				if r := recover(); r != nil {
					observ.LoggerFrom(c.Request().Context()).Error("panic 已恢复",
						slog.Any("panic", r),
						slog.String("path", c.Request().URL.Path),
					)
					c.Error(echo.NewHTTPError(500, "服务内部错误"))
				}
			}()
			return next(c)
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
