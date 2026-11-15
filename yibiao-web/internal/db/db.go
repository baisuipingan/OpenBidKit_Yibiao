// Package db 负责数据库连接池与迁移。
package db

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/patrick/yibiao-web/internal/config"
)

// 迁移文件放在本包内，因为 go:embed 不能跨目录引用（不支持 ../）。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open 建立连接池并做一次连通性检查。
func Open(ctx context.Context, cfg config.DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串失败: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库连接失败: %w", err)
	}

	return pool, nil
}

// Migrate 执行全部未应用的迁移。
//
// goose v3 的原生接口基于 database/sql，这里用 pgx 的 stdlib 适配层
// 临时开一个 *sql.DB 专供迁移使用，完成后立即关闭。
// 业务查询走 pgxpool 原生接口，不经过 database/sql。
func Migrate(ctx context.Context, cfg config.DBConfig) error {
	connCfg, err := pgx.ParseConfig(cfg.URL)
	if err != nil {
		return fmt.Errorf("解析数据库连接串失败: %w", err)
	}

	sqlDB := stdlib.OpenDB(*connCfg)
	defer sqlDB.Close()

	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetConnMaxLifetime(time.Minute)

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("迁移连接数据库失败: %w", err)
	}

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("设置迁移方言失败: %w", err)
	}
	goose.SetLogger(goose.NopLogger())

	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("执行迁移失败: %w", err)
	}
	return nil
}
