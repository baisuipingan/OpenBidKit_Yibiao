package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/store"
)

// DemoIdentity 是演示旁路使用的固定单用户身份。
//
// 不删除登录/租户体系，只在 DEMO_AUTH_BYPASS 打开时
// 把未认证请求映射到默认管理员，方便演示核心投标流程。
type DemoIdentity struct {
	UserID   string
	TenantID string
	Role     string
	Email    string
}

// 与 bootstrap 默认账号保持一致，便于演示环境可预期。
const (
	DemoTenantSlug = "default"
	DemoAdminEmail = "admin@local"
)

// ResolveDemoIdentity 从库中解析默认租户与管理员。
//
// 调用方应在服务启动时解析一次；账号不存在时返回错误，
// 避免旁路静默落到空身份。
func ResolveDemoIdentity(ctx context.Context, pool *pgxpool.Pool) (*DemoIdentity, error) {
	q := store.New(pool)

	tenant, err := q.GetTenantBySlug(ctx, DemoTenantSlug)
	if err != nil {
		return nil, fmt.Errorf("演示旁路找不到默认租户 %q: %w", DemoTenantSlug, err)
	}

	user, err := q.GetUserByEmail(ctx, store.GetUserByEmailParams{
		TenantID:   tenant.ID,
		EmailLower: DemoAdminEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("演示旁路找不到默认管理员 %q: %w", DemoAdminEmail, err)
	}
	if user.Status != "active" {
		return nil, fmt.Errorf("演示旁路默认管理员状态异常: %s", user.Status)
	}

	return &DemoIdentity{
		UserID:   user.ID.String(),
		TenantID: tenant.ID.String(),
		Role:     user.Role,
		Email:    user.Email,
	}, nil
}
