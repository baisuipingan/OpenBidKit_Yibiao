package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalFS 是基于本地文件系统的存储后端。
//
// 适用：开发环境、单机部署。
// 路径结构：<root>/<key>
//
// 安全措施：
//   - 所有 key 经 ValidateKey 校验
//   - 解析后的绝对路径必须仍在 root 之下（二次校验，防软链接逃逸）
type LocalFS struct {
	root string
}

// NewLocalFS 创建本地存储。
//
// root 会被转成绝对路径；不存在时自动创建。
func NewLocalFS(root string) (*LocalFS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析存储根目录失败: %w", err)
	}
	// 消除符号链接，避免后续路径比对被绕过
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建存储根目录失败: %w", err)
	}
	return &LocalFS{root: abs}, nil
}

// Root 返回存储根目录的绝对路径。
func (l *LocalFS) Root() string { return l.root }

// Put 写入对象。
func (l *LocalFS) Put(ctx context.Context, key string, r io.Reader, size int64, mime string) (int64, error) {
	full, err := l.resolve(key)
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, fmt.Errorf("创建目录失败: %w", err)
	}

	// 先写临时文件再 rename，保证原子性
	// 避免写一半被读到，或进程崩溃留下半个文件
	tmp := full + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("创建文件失败: %w", err)
	}

	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("写入失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("关闭文件失败: %w", closeErr)
	}

	// size 已知时校验实际写入量
	if size > 0 && n != size {
		_ = os.Remove(tmp)
		return n, fmt.Errorf("写入字节数不符：期望 %d，实际 %d", size, n)
	}

	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("重命名失败: %w", err)
	}

	return n, nil
}

// Get 读取对象。
func (l *LocalFS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	full, err := l.resolve(key)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	return f, nil
}

// Stat 查询对象信息。
func (l *LocalFS) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	full, err := l.resolve(key)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取文件信息失败: %w", err)
	}

	return &ObjectInfo{
		Key:      key,
		Size:     fi.Size(),
		Modified: fi.ModTime(),
	}, nil
}

// Delete 删除对象。
func (l *LocalFS) Delete(ctx context.Context, key string) error {
	full, err := l.resolve(key)
	if err != nil {
		return err
	}

	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			return nil // 幂等
		}
		return fmt.Errorf("删除失败: %w", err)
	}

	// 顺带清理空目录，避免目录越积越多
	l.pruneEmptyDirs(filepath.Dir(full))
	return nil
}

// Exists 检查对象是否存在。
func (l *LocalFS) Exists(ctx context.Context, key string) (bool, error) {
	full, err := l.resolve(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// resolve 把 key 解析成绝对路径，并做二次安全校验。
func (l *LocalFS) resolve(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	full := filepath.Join(l.root, filepath.FromSlash(key))

	// 二次校验：解析后的路径必须仍在 root 之下。
	// 这一步防的是符号链接逃逸：别人在 root 里放一个指向 /etc 的软链。
	//
	// 注意：目标文件可能还不存在（Put 场景），所以校验它的父目录。
	checkPath := full
	if _, err := os.Lstat(full); os.IsNotExist(err) {
		checkPath = filepath.Dir(full)
	}

	// 找最近一个存在的祖先目录做真实路径解析
	resolvedPath := checkPath
	for {
		if _, err := os.Lstat(resolvedPath); err == nil {
			if rp, err := filepath.EvalSymlinks(resolvedPath); err == nil {
				resolvedPath = rp
			}
			break
		}
		parent := filepath.Dir(resolvedPath)
		if parent == resolvedPath {
			break
		}
		resolvedPath = parent
	}

	if !isWithin(l.root, resolvedPath) {
		return "", fmt.Errorf("%w: 路径逃逸到根目录之外", ErrInvalidKey)
	}

	return full, nil
}

// pruneEmptyDirs 向上清理空目录，直到 root。
func (l *LocalFS) pruneEmptyDirs(dir string) {
	for {
		if dir == l.root || !isWithin(l.root, dir) || dir == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// isWithin 判断 target 是否在 root 之下（含等于）。
func isWithin(root, target string) bool {
	if root == target {
		return true
	}
	// 统一用分隔符结尾，避免 /data/foo 匹配 /data/foobar
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

// 编译期断言：LocalFS 实现 Backend。
var _ Backend = (*LocalFS)(nil)

// 确保 time 包被使用（ObjectInfo 里用到，这里保留导入）
var _ = time.Time{}
