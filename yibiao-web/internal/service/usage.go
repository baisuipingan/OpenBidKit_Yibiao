package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// UsageRecorder 记录模型用量。
//
// 当前是内存实现，只记录到日志。阶段 5 接入计费时替换为写
// usage_records + credit_ledgers 表。
//
// 设计意图：现在就把「每次调用都要记用量」的钩子埋好，
// 将来接计费不用改业务代码。
type UsageRecorder struct {
	log *slog.Logger

	mu sync.Mutex
	// 按租户累计本期用量
	totals map[string]*TenantUsage
}

// TenantUsage 是租户的累计用量。
type TenantUsage struct {
	TextRequests  int64
	PromptTokens  int64
	OutputTokens  int64
	ImageRequests int64
	LastAt        time.Time
}

// NewUsageRecorder 创建记录器。
func NewUsageRecorder(log *slog.Logger) *UsageRecorder {
	return &UsageRecorder{
		log:    log,
		totals: make(map[string]*TenantUsage),
	}
}

// RecordText 记录一次文本调用。
func (r *UsageRecorder) RecordText(ctx context.Context, tenantID, model string, promptTokens, outputTokens int) {
	r.mu.Lock()
	u, ok := r.totals[tenantID]
	if !ok {
		u = &TenantUsage{}
		r.totals[tenantID] = u
	}
	u.TextRequests++
	u.PromptTokens += int64(promptTokens)
	u.OutputTokens += int64(outputTokens)
	u.LastAt = time.Now()
	snapshot := *u
	r.mu.Unlock()

	r.log.Info("模型用量",
		slog.String("tenant_id", tenantID),
		slog.String("model", model),
		slog.Int("prompt_tokens", promptTokens),
		slog.Int("output_tokens", outputTokens),
		slog.Int64("total_text_requests", snapshot.TextRequests),
	)
}

// RecordImage 记录一次生图调用。
func (r *UsageRecorder) RecordImage(ctx context.Context, tenantID, model string) {
	r.mu.Lock()
	u, ok := r.totals[tenantID]
	if !ok {
		u = &TenantUsage{}
		r.totals[tenantID] = u
	}
	u.ImageRequests++
	u.LastAt = time.Now()
	r.mu.Unlock()
}

// Get 读取某租户的用量快照。
func (r *UsageRecorder) Get(tenantID string) TenantUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.totals[tenantID]; ok {
		return *u
	}
	return TenantUsage{}
}
