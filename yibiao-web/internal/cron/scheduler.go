// Package cron 提供定时后台任务。
//
// 当前只有工作区清理，后续可扩展：
//   - 过期资产清理（软删超过 N 天的物理删除）
//   - 用量统计归档
//   - 会话过期清理
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/patrick/yibiao-web/internal/service/workspace"
)

// Scheduler 管理定时任务。
type Scheduler struct {
	wsMgr *workspace.Manager
	log   *slog.Logger
	// interval 是清理检查间隔
	interval time.Duration
}

// NewScheduler 创建调度器。
func NewScheduler(wsMgr *workspace.Manager, log *slog.Logger) *Scheduler {
	return &Scheduler{
		wsMgr:    wsMgr,
		log:      log,
		interval: 10 * time.Minute,
	}
}

// Start 启动调度器。ctx 取消时退出。
//
// 用 ticker 而不是 cron 表达式：任务简单，且避免引入额外依赖。
// 首次执行延迟 1 分钟，避开服务启动时的繁忙期。
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		// 启动后先等一会再跑第一次
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}

		s.runOnce(ctx)

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				s.log.Info("定时任务已停止")
				return
			case <-ticker.C:
				s.runOnce(ctx)
			}
		}
	}()

	s.log.Info("定时任务已启动", slog.Duration("interval", s.interval))
}

// runOnce 执行一轮清理。
func (s *Scheduler) runOnce(ctx context.Context) {
	defer func() {
		// 清理失败不能影响服务
		if r := recover(); r != nil {
			s.log.Error("定时清理 panic", slog.Any("panic", r))
		}
	}()

	// 先统计，便于观察趋势
	stats, err := s.wsMgr.GetStats()
	if err != nil {
		s.log.Warn("读取工作区统计失败", slog.String("error", err.Error()))
	} else if stats.Count > 0 {
		s.log.Debug("工作区当前占用",
			slog.Int("count", stats.Count),
			slog.Int64("bytes", stats.TotalBytes),
		)
	}

	removed, err := s.wsMgr.Cleanup(ctx)
	if err != nil {
		s.log.Warn("工作区清理失败", slog.String("error", err.Error()))
		return
	}

	if removed > 0 {
		s.log.Info("工作区已清理", slog.Int("removed", removed))

		// 清理后再统计一次
		if after, err := s.wsMgr.GetStats(); err == nil {
			s.log.Info("清理后工作区",
				slog.Int("count", after.Count),
				slog.Int64("bytes", after.TotalBytes),
			)
		}
	}
}
