// Package storage 提供后端无关的对象存储。
//
// 设计依据见 docs/web-migration/08-storage-architecture.md。
//
// 核心原则（调研自 Manus / Vercel Sandbox / Google ADK）：
//  1. 存储与计算分离：沙箱是临时的，存储是持久的
//  2. Key 内嵌 scope：<tenant>/<project>/<kind>/<asset_id>/v<version>/<name>
//  3. 业务层不碰路径：只拿 asset_id，通过 Artifact Service 访问
//
// 两个实现：
//   - LocalFS：开发与单机部署
//   - S3：生产多实例（预留接口，暂不实现）
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// 通用错误。
var (
	// ErrNotFound 对象不存在。
	ErrNotFound = errors.New("对象不存在")
	// ErrInvalidKey key 非法（含 .. 或绝对路径）。
	ErrInvalidKey = errors.New("非法的存储键")
)

// ObjectInfo 是对象元信息。
type ObjectInfo struct {
	Key      string
	Size     int64
	Modified time.Time
	// Hash 是内容的 SHA-256（若后端支持计算）
	Hash string
}

// Backend 是存储后端接口。
//
// 所有实现必须：
//   - 拒绝含 ".." 的 key（防路径穿越）
//   - 拒绝绝对路径
//   - 幂等：相同 key 重复 Put 应覆盖
type Backend interface {
	// Put 写入对象。返回写入的字节数。
	//
	// size 为 -1 时表示未知，实现需自行读取。
	// 若内容超过预期大小，实现应报错。
	Put(ctx context.Context, key string, r io.Reader, size int64, mime string) (int64, error)

	// Get 读取对象。调用方负责关闭。
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Stat 查询对象信息。
	Stat(ctx context.Context, key string) (*ObjectInfo, error)

	// Delete 物理删除对象。不存在时返回 nil（幂等）。
	Delete(ctx context.Context, key string) error

	// Exists 检查对象是否存在。
	Exists(ctx context.Context, key string) (bool, error)
}

// Scope 决定对象的归属，用于生成 key。
//
// 关键：这两个字段必须由服务端从登录态填充，
// 绝不能来自请求参数，否则就是越权漏洞。
type Scope struct {
	TenantID string
	// ProjectID 为空表示用户级资产（如知识库文档、个人素材）
	ProjectID string
}

// Key 构造一个存储键。
//
// 格式：<tenant>/<project|_user>/<kind>/<assetID>/v<version>/<safeName>
//
// 用 assetID 做目录而不是 name，原因：
//   - name 可能含中文、斜杠、特殊字符
//   - name 可能含 "../" 造成穿越
//   - 同一 name 的不同版本要放在独立目录
func Key(scope Scope, kind, assetID string, version int, name string) string {
	owner := scope.ProjectID
	if owner == "" {
		owner = "_user"
	}
	return fmt.Sprintf("%s/%s/%s/%s/v%d/%s",
		scope.TenantID,
		owner,
		sanitizeSegment(kind),
		sanitizeSegment(assetID),
		version,
		sanitizeName(name),
	)
}

// ValidateKey 校验 key 是否安全。
//
// 所有 Backend 实现都应在入口调用它。
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: 空 key", ErrInvalidKey)
	}
	// 绝对路径
	if key[0] == '/' {
		return fmt.Errorf("%w: 不允许绝对路径", ErrInvalidKey)
	}
	// Windows 盘符
	if len(key) >= 2 && key[1] == ':' {
		return fmt.Errorf("%w: 不允许盘符", ErrInvalidKey)
	}
	// 路径穿越：逐段检查
	for _, seg := range splitPath(key) {
		if seg == ".." {
			return fmt.Errorf("%w: 不允许 .. 路径段", ErrInvalidKey)
		}
		if seg == "." {
			return fmt.Errorf("%w: 不允许 . 路径段", ErrInvalidKey)
		}
	}
	return nil
}

// HashBytes 计算内容的 SHA-256。
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashReader 计算 Reader 内容的 SHA-256，同时返回读到的字节。
//
// 用于需要「先算 hash 再决定是否上传」的场景（秒传）。
func HashReader(r io.Reader) (string, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", nil, err
	}
	return HashBytes(data), data, nil
}

// --- 内部工具 ---

// sanitizeSegment 清理路径段，只保留安全字符。
func sanitizeSegment(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_':
			out = append(out, r)
		default:
			// 其他字符统一替换为下划线，避免中文/特殊字符进路径
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

// sanitizeName 清理文件名，保留扩展名。
//
// 中文等非 ASCII 字符替换为下划线，但扩展名保留（如 .md / .json）。
// name 可能带子目录（"items/projectInfo.json"），逐段清理。
func sanitizeName(name string) string {
	if name == "" {
		return "_"
	}

	// 提取扩展名（只认最后一个点，且点后无分隔符）
	base, ext := name, ""
	if i := lastIndexByte(name, '.'); i > 0 && i < len(name)-1 {
		// 确认点后没有路径分隔符
		tail := name[i+1:]
		noSep := true
		for j := 0; j < len(tail); j++ {
			if tail[j] == '/' || tail[j] == '\\' {
				noSep = false
				break
			}
		}
		if noSep {
			base, ext = name[:i], name[i:]
		}
	}

	// 逐段清理文件名主体
	segments := splitPath(base)
	cleaned := make([]string, 0, len(segments))
	for _, seg := range segments {
		cleaned = append(cleaned, sanitizeSegment(seg))
	}

	out := joinPath(cleaned)
	if out == "" {
		out = "_"
	}

	// 扩展名单独清理（去掉前导点后清理，再拼回）
	if ext != "" {
		out += "." + sanitizeSegment(ext[1:])
	}
	return out
}

// lastIndexByte 返回 c 在 s 中最后一次出现的字节位置。
// 注意：扩展名分隔符 '.' 是单字节，按字节找是安全的。
func lastIndexByte(s string, c byte) int {
	return strings.LastIndexByte(s, c)
}

// splitPath 按路径分隔符切分。
//
// 必须用 strings.FieldsFunc（按 rune 处理），
// 不能用按字节遍历：中文是多字节 UTF-8，逐字节拼会得到乱码。
func splitPath(p string) []string {
	parts := strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func joinPath(segs []string) string {
	return strings.Join(segs, "/")
}
