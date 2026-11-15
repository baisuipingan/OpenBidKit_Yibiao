package handler

import (
	"bytes"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/events"
	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

// Health 提供健康检查。
type Health struct {
	pool    *pgxpool.Pool
	bus     *events.Bus
	storage storage.Backend
	version string
	started time.Time
}

// NewHealth 创建 handler。
func NewHealth(pool *pgxpool.Pool, bus *events.Bus, store storage.Backend, version string) *Health {
	return &Health{pool: pool, bus: bus, storage: store, version: version, started: time.Now()}
}

// Register 注册路由（公开）。
func (h *Health) Register(e *echo.Echo) {
	e.GET("/health", h.Live)
	e.GET("/health/ready", h.Ready)
}

// Live 存活探针：进程在就算活。
func (h *Health) Live(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"status":  "ok",
		"version": h.version,
		"uptime":  time.Since(h.started).Round(time.Second).String(),
	})
}

// Ready 就绪探针：数据库可用才算就绪。
func (h *Health) Ready(c echo.Context) error {
	ctx := c.Request().Context()

	dbOK := true
	dbErr := ""
	if err := h.pool.Ping(ctx); err != nil {
		dbOK = false
		dbErr = err.Error()
	}

	subs, dropped := h.bus.Stats()

	// 存储可用性：写入一个探针对象再删除
	storeOK, storeErr := true, ""
	probeKey := "health/probe-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	probeBody := []byte("ok")
	if _, err := h.storage.Put(ctx, probeKey, bytes.NewReader(probeBody), int64(len(probeBody)), "text/plain"); err != nil {
		storeOK, storeErr = false, err.Error()
	} else {
		_ = h.storage.Delete(ctx, probeKey)
	}

	status := http.StatusOK
	body := map[string]any{
		"status": "ready",
		"checks": map[string]any{
			"database": map[string]any{"ok": dbOK, "error": dbErr},
			"storage":  map[string]any{"ok": storeOK, "error": storeErr},
			"events":   map[string]any{"subscribers": subs, "dropped": dropped},
		},
	}
	if !dbOK || !storeOK {
		status = http.StatusServiceUnavailable
		body["status"] = "not_ready"
	}
	return c.JSON(status, body)
}

// Version 暴露版本信息。
func NewRoot(version string) *Root {
	return &Root{version: version}
}

// Root 处理 ROOT 层接口（对应 bridge 的 window.yibiao.xxx）。
type Root struct {
	version string
}

// Register 注册路由。
func (h *Root) Register(g *echo.Group) {
	g.POST("/root/get-version", h.GetVersion)
}

// GetVersion 对应 bridge 的 window.yibiao.getVersion()。
func (h *Root) GetVersion(c echo.Context) error {
	return httpx.OK(c, h.version)
}
