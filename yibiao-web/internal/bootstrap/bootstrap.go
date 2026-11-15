// Package bootstrap 负责首次启动时的初始化数据。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/config"
	"github.com/patrick/yibiao-web/internal/store"
)

// 默认数据常量。
const (
	DefaultTenantSlug = "default"
	DefaultTenantName = "默认租户"
	// 默认管理员凭据。首次登录后必须修改。
	DefaultAdminEmail    = "admin@local"
	DefaultAdminPassword = "admin12345"
	DefaultAdminName     = "管理员"
)

// EnsureDefaults 确保默认租户与管理员存在。
//
// 幂等：已存在则跳过，不会重置密码。
// 单租户阶段用它省掉租户管理界面；多租户上线后仍可保留作为兜底。
func EnsureDefaults(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) error {
	q := store.New(pool)

	tenant, err := q.GetTenantBySlug(ctx, DefaultTenantSlug)
	created := false
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("查询默认租户失败: %w", err)
		}
		tenant, err = q.CreateTenant(ctx, store.CreateTenantParams{
			Slug:     DefaultTenantSlug,
			Name:     DefaultTenantName,
			Settings: []byte(`{}`),
		})
		if err != nil {
			return fmt.Errorf("创建默认租户失败: %w", err)
		}
		created = true
		log.Info("已创建默认租户", slog.String("tenant_id", tenant.ID.String()))
	}

	// 配置里显式指定了默认租户则以其为准（防止指向已删除的租户）
	if cfg.Auth.DefaultTenantID != "" && cfg.Auth.DefaultTenantID != tenant.ID.String() {
		log.Warn("DEFAULT_TENANT_ID 与默认租户不一致，以配置为准",
			slog.String("config", cfg.Auth.DefaultTenantID),
			slog.String("actual", tenant.ID.String()),
		)
	}

	_, err = q.GetUserByEmail(ctx, store.GetUserByEmailParams{
		TenantID:   tenant.ID,
		EmailLower: DefaultAdminEmail,
	})
	if err == nil {
		// 已存在，不动
		if created {
			log.Info("默认租户已就绪，管理员账号沿用现有")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("查询默认管理员失败: %w", err)
	}

	mgr := auth.NewManager(cfg.Auth)
	hash, err := mgr.HashPassword(DefaultAdminPassword)
	if err != nil {
		return fmt.Errorf("生成管理员密码失败: %w", err)
	}

	admin, err := q.CreateUser(ctx, store.CreateUserParams{
		TenantID:           tenant.ID,
		Email:              DefaultAdminEmail,
		DisplayName:        DefaultAdminName,
		PasswordHash:       hash,
		Role:               "owner",
		Status:             "active",
		MustChangePassword: true,
	})
	if err != nil {
		return fmt.Errorf("创建默认管理员失败: %w", err)
	}

	log.Warn("已创建默认管理员，请立即修改密码",
		slog.String("tenant_id", tenant.ID.String()),
		slog.String("email", admin.Email),
		slog.String("default_password", DefaultAdminPassword),
	)
	return nil
}
