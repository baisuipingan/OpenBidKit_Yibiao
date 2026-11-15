// Package events 提供进程内事件总线与 SSE 推送。
//
// 设计要点：
//   - 桌面版是单进程内存事件，Web 版多用户必须按用户隔离，不能广播
//   - 每个订阅者只收到自己有权接收的事件
//   - 单实例内存实现；将来多实例时换成 PG LISTEN/NOTIFY 或 Redis，
//     接口不变
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Event 是一条推送给客户端的事件。
type Event struct {
	Seq   uint64          `json:"seq"`
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
	At    time.Time       `json:"at"`
}

// Scope 决定一条事件该发给谁。
type Scope struct {
	TenantID string
	UserID   string
	// ProjectID 为空表示租户级事件（如知识库变更）
	ProjectID string
}

// subscriber 是一个 SSE 连接的订阅状态。
type subscriber struct {
	id      uint64
	scope   Scope
	topics  map[string]struct{}
	ch      chan Event
	dropped atomic.Int64
}

// Bus 是进程内事件总线。
type Bus struct {
	mu     sync.RWMutex
	subs   map[uint64]*subscriber
	nextID atomic.Uint64
	seq    atomic.Uint64
	// 缓冲区大小。满了丢弃最旧事件而不是阻塞发布者。
	bufSize int
}

// NewBus 创建事件总线。
func NewBus() *Bus {
	return &Bus{
		subs:    make(map[uint64]*subscriber),
		bufSize: 64,
	}
}

// Subscribe 注册一个订阅，返回订阅 ID 和事件通道。
//
// 调用方必须在结束时调用 Unsubscribe，否则会泄漏。
func (b *Bus) Subscribe(ctx context.Context, scope Scope, topics []string) (uint64, <-chan Event) {
	id := b.nextID.Add(1)

	topicSet := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		topicSet[t] = struct{}{}
	}

	sub := &subscriber{
		id:     id,
		scope:  scope,
		topics: topicSet,
		ch:     make(chan Event, b.bufSize),
	}

	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()

	// context 取消时自动清理，避免调用方忘记
	go func() {
		<-ctx.Done()
		b.Unsubscribe(id)
	}()

	return id, sub.ch
}

// Unsubscribe 注销订阅并关闭通道。
func (b *Bus) Unsubscribe(id uint64) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
	}
	b.mu.Unlock()

	if ok {
		close(sub.ch)
	}
}

// Publish 发布一条事件给匹配的订阅者。
//
// 匹配规则：
//   - tenant 必须一致（强制）
//   - 事件带 UserID 时，只发给同一用户
//   - 事件带 ProjectID 时，只发给订阅了该项目的连接
//   - topic 必须被订阅
func (b *Bus) Publish(topic string, scope Scope, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		// 序列化失败是编程错误，不发事件但不 panic
		return
	}

	ev := Event{
		Seq:   b.seq.Add(1),
		Topic: topic,
		Data:  data,
		At:    time.Now(),
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	delivered := 0
	for _, sub := range b.subs {
		if !matchScope(sub.scope, scope) {
			continue
		}
		if _, ok := sub.topics[topic]; !ok {
			continue
		}
		select {
		case sub.ch <- ev:
			delivered++
		default:
			// 客户端消费太慢：丢弃并计数，不阻塞发布者。
			// SSE 端会在下一次心跳里提示客户端刷新全量状态。
			sub.dropped.Add(1)
		}
	}

	// 诊断：仅在开启 EVENT_DEBUG 时输出（生产默认关闭）
	if os.Getenv("EVENT_DEBUG") == "1" {
		slog.Debug("事件已发布",
			slog.String("topic", topic),
			slog.Int("subs", len(b.subs)),
			slog.Int("delivered", delivered),
		)
	}
}

// matchScope 判断订阅者是否应收到该事件。
func matchScope(sub, ev Scope) bool {
	if sub.TenantID == "" || ev.TenantID == "" {
		return false
	}
	if sub.TenantID != ev.TenantID {
		return false
	}
	// 事件指定了用户 → 只发给该用户
	if ev.UserID != "" && sub.UserID != ev.UserID {
		return false
	}
	// 事件指定了项目 → 订阅者也必须限定该项目
	if ev.ProjectID != "" && sub.ProjectID != "" && sub.ProjectID != ev.ProjectID {
		return false
	}
	return true
}

// Stats 返回当前订阅数与活跃度，用于监控。
func (b *Bus) Stats() (subscribers int, dropped int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var d int64
	for _, s := range b.subs {
		d += s.dropped.Load()
	}
	return len(b.subs), d
}
