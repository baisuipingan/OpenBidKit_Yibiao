// Package service 承载业务逻辑。
//
// 分层约定：
//
//	handler  只做参数解析与响应封装
//	service  业务规则、事务边界、跨表协调
//	store    sqlc 生成的 SQL 查询（无业务逻辑）
package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/store"
)

// AuthService 处理登录、刷新、登出。
type AuthService struct {
	pool *pgxpool.Pool
	q    *store.Queries
	mgr  *auth.Manager
}

// NewAuthService 创建认证服务。
func NewAuthService(pool *pgxpool.Pool, mgr *auth.Manager) *AuthService {
	return &AuthService{pool: pool, q: store.New(pool), mgr: mgr}
}

// LoginResult 是登录成功后的返回。
type LoginResult struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	User         UserInfo  `json:"user"`
}

// UserInfo 是前端需要的用户信息，不含敏感字段。
type UserInfo struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"displayName"`
	Role               string `json:"role"`
	TenantID           string `json:"tenantId"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

// Login 校验凭据并签发令牌。
func (s *AuthService) Login(ctx context.Context, tenantID, email, password, userAgent string, ip netip.Addr) (*LoginResult, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("缺少租户标识")
	}

	u, err := s.q.GetUserByEmail(ctx, store.GetUserByEmailParams{
		TenantID:   mustUUID(tenantID),
		EmailLower: strings.ToLower(email),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 不区分「用户不存在」和「密码错误」，避免枚举
			return nil, auth.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	if err := s.mgr.VerifyPassword(u.PasswordHash, password); err != nil {
		return nil, err
	}

	switch u.Status {
	case "disabled":
		return nil, auth.ErrAccountDisabled
	case "pending":
		return nil, auth.ErrAccountPending
	}

	return s.issueTokens(ctx, u, userAgent, ip)
}

// Refresh 用刷新令牌换新的访问令牌。
func (s *AuthService) Refresh(ctx context.Context, refreshToken, userAgent string, ip netip.Addr) (*LoginResult, error) {
	sess, err := s.q.GetSessionByTokenHash(ctx, auth.HashToken(refreshToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrTokenInvalid
		}
		return nil, fmt.Errorf("查询会话失败: %w", err)
	}

	u, err := s.q.GetUserByIDOnly(ctx, sess.UserID)
	if err != nil {
		return nil, auth.ErrTokenInvalid
	}
	if u.Status != "active" {
		return nil, auth.ErrAccountDisabled
	}

	// 轮换：旧的刷新令牌立即失效，防重放
	if err := s.q.RevokeSession(ctx, sess.ID); err != nil {
		return nil, fmt.Errorf("撤销旧会话失败: %w", err)
	}

	return s.issueTokens(ctx, u, userAgent, ip)
}

// Logout 撤销会话。
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	sess, err := s.q.GetSessionByTokenHash(ctx, auth.HashToken(refreshToken))
	if err != nil {
		// 已失效视为登出成功，幂等
		return nil
	}
	return s.q.RevokeSession(ctx, sess.ID)
}

// Me 返回当前登录用户信息。
func (s *AuthService) Me(ctx context.Context, userID, tenantID string) (*UserInfo, error) {
	u, err := s.q.GetUserByID(ctx, store.GetUserByIDParams{
		ID:       mustUUID(userID),
		TenantID: mustUUID(tenantID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrTokenInvalid
		}
		return nil, err
	}
	return toUserInfo(u), nil
}

// issueTokens 签发访问 + 刷新令牌并落库会话。
func (s *AuthService) issueTokens(ctx context.Context, u store.User, userAgent string, ip netip.Addr) (*LoginResult, error) {
	access, exp, err := s.mgr.IssueAccessToken(u.ID.String(), u.TenantID.String(), u.Role)
	if err != nil {
		return nil, err
	}

	refreshPlain, refreshHash, err := s.mgr.NewRefreshToken()
	if err != nil {
		return nil, err
	}

	_, err = s.q.CreateSession(ctx, store.CreateSessionParams{
		UserID:    u.ID,
		TokenHash: refreshHash,
		UserAgent: userAgent,
		Ip:        addrPtr(ip),
		ExpiresAt: time.Now().Add(s.mgr.RefreshTokenTTL()),
	})
	if err != nil {
		return nil, fmt.Errorf("创建会话失败: %w", err)
	}

	// 异步更新最后登录时间，失败不影响登录
	go func() {
		_ = s.q.UpdateUserLastLogin(context.WithoutCancel(ctx), u.ID)
	}()

	return &LoginResult{
		AccessToken:  access,
		RefreshToken: refreshPlain,
		ExpiresAt:    exp,
		User:         *toUserInfo(u),
	}, nil
}

func toUserInfo(u store.User) *UserInfo {
	return &UserInfo{
		ID:                 u.ID.String(),
		Email:              u.Email,
		DisplayName:        u.DisplayName,
		Role:               u.Role,
		TenantID:           u.TenantID.String(),
		MustChangePassword: u.MustChangePassword,
	}
}

// addrPtr 把 netip.Addr 转成 *netip.Addr。
// 零值（解析失败）返回 nil，数据库存 NULL。
func addrPtr(a netip.Addr) *netip.Addr {
	if !a.IsValid() {
		return nil
	}
	return &a
}

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}
