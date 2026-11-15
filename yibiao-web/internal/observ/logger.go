// Package observ 提供日志与可观测性基础。
//
// 用标准库 slog，不引入第三方日志框架：
//   - dev 环境输出可读文本
//   - prod 环境输出 JSON，便于采集
package observ

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// ctxKey 是私有的 context key 类型，避免与其他包的 key 冲突。
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

// NewLogger 按环境创建 logger。
func NewLogger(env, level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: lv,
		// 开发环境把源码位置带上，方便定位
		AddSource: env == "dev",
	}

	var h slog.Handler
	if env == "dev" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// WithLogger 把 logger 放进 context。
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// LoggerFrom 从 context 取 logger，取不到时返回默认 logger。
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithRequestID 把请求 ID 放进 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFrom 从 context 取请求 ID。
func RequestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return id
	}
	return ""
}
