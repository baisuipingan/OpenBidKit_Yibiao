package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/events"
	"github.com/patrick/yibiao-web/internal/observ"
)

// Event 处理 SSE 推送。
//
// 关键设计（见 docs/web-migration/01-bridge-contract.md §4）：
//   - 桌面版有 21 个 ipcRenderer.on，Web 版合并成 1 条 SSE
//   - 按 topic 分发，前端 web.ts 内部再分发给各自的回调
//   - 必须按 tenant/user 隔离，不能广播
type Event struct {
	bus *events.Bus
}

// NewEvent 创建 handler。
func NewEvent(bus *events.Bus) *Event {
	return &Event{bus: bus}
}

// Register 注册路由。已登录。
func (h *Event) Register(g *echo.Group) {
	g.GET("/events", h.Stream)
}

// Stream 是 SSE 长连接。
//
// 用法：GET /api/events?topics=task,agent,export&access_token=xxx
// EventSource 不能设置请求头，所以令牌走 query 参数（见 middleware.bearerToken）。
func (h *Event) Stream(c echo.Context) error {
	ctx := c.Request().Context()
	log := observ.LoggerFrom(ctx)

	id := auth.IdentityFrom(ctx)
	if id.UserID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "未登录")
	}

	topics := parseTopics(c.QueryParam("topics"))
	if len(topics) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "缺少 topics 参数")
	}

	scope := events.Scope{
		TenantID:  id.TenantID,
		UserID:    id.UserID,
		ProjectID: c.Request().Header.Get("X-Project-Id"),
	}

	subID, ch := h.bus.Subscribe(ctx, scope, topics)
	defer h.bus.Unsubscribe(subID)

	res := c.Response()
	res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	res.Header().Set("Cache-Control", "no-cache, no-transform")
	res.Header().Set("Connection", "keep-alive")
	// 告诉 nginx 不要缓冲，否则事件会被攒着一起发
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)

	// 先发一个 ready 事件，前端据此确认连接建立
	writeSSE(res, "ready", 0, map[string]any{
		"topics": topics,
		"at":     time.Now().UTC().Format(time.RFC3339),
	})

	log.Info("SSE 已连接", "topics", strings.Join(topics, ","))

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("SSE 断开")
			return nil

		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			writeSSE(res, ev.Topic, ev.Seq, json.RawMessage(ev.Data))

		case <-heartbeat.C:
			// SSE 注释行，客户端忽略，但能保活并探测断连
			fmt.Fprint(res, ": ping\n\n")
			res.Flush()
		}
	}
}

// writeSSE 写一条 SSE 消息并立即 flush。
func writeSSE(res *echo.Response, topic string, seq uint64, data any) {
	if seq > 0 {
		fmt.Fprintf(res, "id: %d\n", seq)
	}
	fmt.Fprintf(res, "event: %s\n", topic)

	var payload []byte
	switch v := data.(type) {
	case json.RawMessage:
		payload = v
	case []byte:
		payload = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			payload = []byte("{}")
		} else {
			payload = b
		}
	}

	// SSE 要求 data 里不能有裸换行，多行要拆成多个 data:
	for _, line := range strings.Split(string(payload), "\n") {
		fmt.Fprintf(res, "data: %s\n", line)
	}
	fmt.Fprint(res, "\n")
	res.Flush()
}

func parseTopics(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
