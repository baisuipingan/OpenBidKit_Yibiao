// Package auth 提供密码哈希与 JWT 签发/校验。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/patrick/yibiao-web/internal/config"
)

// 认证相关错误。handler 层据此映射 HTTP 状态码。
var (
	ErrInvalidCredentials = errors.New("邮箱或密码错误")
	ErrTokenInvalid       = errors.New("令牌无效或已过期")
	ErrTokenExpired       = errors.New("令牌已过期")
	ErrAccountDisabled    = errors.New("账号已被禁用")
	ErrAccountPending     = errors.New("账号待审批")
)

// Claims 是 JWT 载荷。
type Claims struct {
	UserID   string `json:"uid"`
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Manager 负责密码与令牌。
type Manager struct {
	cfg config.AuthConfig
}

// NewManager 创建认证管理器。
func NewManager(cfg config.AuthConfig) *Manager {
	return &Manager{cfg: cfg}
}

// HashPassword 用 bcrypt 生成密码哈希。
func (m *Manager) HashPassword(plain string) (string, error) {
	if len(plain) < 8 {
		return "", fmt.Errorf("密码至少 8 位")
	}
	// cost 12 在服务端约 200-300ms，兼顾安全与体验
	b, err := bcrypt.GenerateFromPassword([]byte(plain), 12)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(b), nil
}

// VerifyPassword 校验密码。
func (m *Manager) VerifyPassword(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// IssueAccessToken 签发访问令牌。
func (m *Manager) IssueAccessToken(userID, tenantID, role string) (string, time.Time, error) {
	exp := time.Now().Add(m.cfg.AccessTokenTTL)
	claims := Claims{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-30 * time.Second)), // 容忍时钟偏差
			Issuer:    "yibiao-web",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(m.cfg.JWTSecret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("签发令牌失败: %w", err)
	}
	return signed, exp, nil
}

// ParseAccessToken 校验并解析访问令牌。
func (m *Manager) ParseAccessToken(raw string) (*Claims, error) {
	tok, err := jwt.ParseWithClaims(raw, &Claims{}, func(t *jwt.Token) (any, error) {
		// 强制校验算法，防 alg=none 攻击
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("不支持的签名算法: %s", t.Method.Alg())
		}
		return []byte(m.cfg.JWTSecret), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}

	claims, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

// NewRefreshToken 生成刷新令牌明文与其哈希。
//
// 明文只返回给客户端一次，服务端只存哈希。
func (m *Manager) NewRefreshToken() (plain string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("生成随机令牌失败: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken 计算令牌哈希。用 SHA-256 足够：
// 令牌本身是高熵随机串，不需要慢哈希。
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// RefreshTokenTTL 暴露刷新令牌有效期给上层使用。
func (m *Manager) RefreshTokenTTL() time.Duration {
	return m.cfg.RefreshTokenTTL
}
