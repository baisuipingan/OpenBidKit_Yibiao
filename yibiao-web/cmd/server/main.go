// 标书工作台 Web 版服务入口。
//
// 启动顺序：
//  1. 加载配置
//  2. 初始化日志
//  3. 连接数据库
//  4. 执行迁移
//  5. 确保默认租户与管理员存在
//  6. 启动 HTTP 服务
//  7. 等待信号，优雅退出
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/patrick/yibiao-web/internal/bootstrap"
	"github.com/patrick/yibiao-web/internal/config"
	"github.com/patrick/yibiao-web/internal/cron"
	"github.com/patrick/yibiao-web/internal/db"
	"github.com/patrick/yibiao-web/internal/events"
	"github.com/patrick/yibiao-web/internal/http/server"
	"github.com/patrick/yibiao-web/internal/observ"
	"github.com/patrick/yibiao-web/internal/service/workspace"
)

// version 由构建时注入：-ldflags "-X main.version=xxx"
var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate-only", false, "只执行数据库迁移然后退出")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := observ.NewLogger(cfg.Env, cfg.LogLevel)
	slog.SetDefault(logger)

	logger.Info("启动中",
		slog.String("version", version),
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTP.Addr),
	)

	// 根 context：收到 SIGINT/SIGTERM 时取消
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- 数据库 ---
	pool, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("数据库已连接")

	if err := db.Migrate(ctx, cfg.DB); err != nil {
		return err
	}
	logger.Info("数据库迁移完成")

	if *migrateOnly {
		logger.Info("--migrate-only 已指定，退出")
		return nil
	}

	// --- 初始化默认租户与管理员 ---
	if err := bootstrap.EnsureDefaults(ctx, pool, cfg, logger); err != nil {
		return err
	}

	// --- 事件总线 ---
	bus := events.NewBus()

	// --- 定时任务 ---
	wsMgr, err := workspace.NewManager(cfg.Agent.WorkspaceRoot, logger)
	if err != nil {
		return fmt.Errorf("初始化工作区管理器失败: %w", err)
	}
	cronSched := cron.NewScheduler(wsMgr, logger)
	cronSched.Start(ctx)

	// --- HTTP ---
	e := server.New(server.Deps{
		Config:  cfg,
		Logger:  logger,
		Pool:    pool,
		Bus:     bus,
		Version: version,
	})

	srv := &http.Server{
		Addr:         cfg.HTTP.Addr,
		Handler:      e,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout, // 0 = 不限制，SSE 需要
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 已就绪", slog.String("addr", cfg.HTTP.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常: %w", err)
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭超时", slog.Any("error", err))
		return err
	}

	logger.Info("已退出")
	return nil
}
