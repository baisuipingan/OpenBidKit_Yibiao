// Package secret 提供密钥的加密存储。
//
// 设计依据（见 02-postgres-schema.md）：
//   - API Key 绝不返回前端明文（前端只看到 hasTextKey: true）
//   - 服务端用 AES-256-GCM 加密后入库
//   - 主密钥来自环境变量 SECRETS_KEY
//
// 为什么用 AES-GCM 而不是 bcrypt：
//
//	bcrypt 是单向的，适合密码；API Key 需要解密回明文去调 API。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cipher 是加解密器。
type Cipher struct {
	aead cipher.AEAD
}

// 错误。
var (
	ErrNotConfigured = errors.New("未配置 SECRETS_KEY，无法加密存储密钥")
	ErrDecryptFailed = errors.New("密钥解密失败（SECRETS_KEY 可能已变更）")
)

// NewCipher 从主密钥创建加解密器。
//
// key 会被 SHA-256 派生成 32 字节，允许传入任意长度的字符串。
func NewCipher(masterKey string) (*Cipher, error) {
	if masterKey == "" {
		return nil, ErrNotConfigured
	}

	// 派生固定长度密钥
	sum := sha256.Sum256([]byte(masterKey))

	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("初始化加密器失败: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}

	return &Cipher{aead: aead}, nil
}

// Encrypt 加密明文，返回 nonce||ciphertext。
func (c *Cipher) Encrypt(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}

	// Seal 会把 nonce 附加在密文前？不会，需要手动拼
	sealed := c.aead.Seal(nil, nonce, []byte(plain), nil)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// Decrypt 解密 Encrypt 的输出。
func (c *Cipher) Decrypt(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	nonceSize := c.aead.NonceSize()
	if len(data) < nonceSize {
		return "", ErrDecryptFailed
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plain, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plain), nil
}

// MaskKey 生成密钥的掩码显示（前 4 后 4）。
//
// 用于日志和前端展示，例如 "sk-1***********ef12"。
func MaskKey(key string) string {
	if len(key) <= 8 {
		if key == "" {
			return ""
		}
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// 未配置时的降级：开发环境允许明文存储并给出警告。
//
// 生产环境必须配置 SECRETS_KEY，否则拒绝启动（见 config.validate）。

// PlainCipher 是明文"加密"实现，仅用于开发环境无 SECRETS_KEY 时。
//
// 用 base64 包装，避免让人误以为是真加密。
type PlainCipher struct{}

// Encrypt 实现 Cipher 接口。
func (PlainCipher) Encrypt(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(plain))), nil
}

// Decrypt 实现 Cipher 接口。
func (PlainCipher) Decrypt(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	out, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(out), nil
}
