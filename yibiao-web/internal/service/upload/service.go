// Package upload 处理前端文件上传。
//
// 设计背景（见 docs/web-migration/01-bridge-contract.md §2.17）：
//
//	桌面版前端用 `window.yibiao.file.getPathForFile(file)` 拿本地路径，
//	再传给 `technicalPlan.importTenderDocument(paths)`。
//
//	Web 版没有本地路径概念，所以：
//	1. `getPathForFile` 返回 `upload://<uuid>` 伪路径
//	2. 同时后台上传文件到服务端
//	3. 业务接口收到 `upload://` 开头的路径时，查上传表拿真实文件
//
// 这样前端 12 处 `getPathForFile` 调用零改动。
package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 常量。
const (
	// Scheme 是伪路径的协议前缀
	Scheme = "upload://"
	// TTL 是上传文件的保留时间（超时清理）
	TTL = 24 * time.Hour
)

// 错误。
var (
	ErrNotFound = errors.New("上传文件不存在或已过期")
	ErrPending  = errors.New("文件仍在上传中，请稍候重试")
)

// Entry 是一个上传记录。
type Entry struct {
	ID       string
	FileName string
	Size     int64
	MimeType string
	// Path 是落盘的绝对路径
	Path string
	// Ready 表示上传完成
	Ready bool
	// Err 上传失败的原因
	Err       string
	CreatedAt time.Time
}

// Service 管理上传文件。
type Service struct {
	root string
	log  *slog.Logger

	mu      sync.RWMutex
	entries map[string]*Entry
}

// NewService 创建服务。
func NewService(root string, log *slog.Logger) (*Service, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析上传目录失败: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建上传目录失败: %w", err)
	}
	return &Service{root: abs, log: log, entries: make(map[string]*Entry)}, nil
}

// Root 返回上传根目录。
func (s *Service) Root() string { return s.root }

// NewPending 创建一个待上传记录，返回伪路径。
//
// 前端调 getPathForFile 时立即调用，先拿到 ID，
// 再异步上传。这样前端不用等待。
func (s *Service) NewPending(fileName string) (id string, pseudoPath string) {
	id = newID()
	s.mu.Lock()
	s.entries[id] = &Entry{
		ID:        id,
		FileName:  fileName,
		CreatedAt: time.Now(),
	}
	s.mu.Unlock()
	return id, Scheme + id
}

// NewPendingWithID 用指定 ID 预登记（客户端已生成 ID 时用）。
//
// 为空时自动生成。ID 冲突时复用已有条目（重复调用不会出问题）。
func (s *Service) NewPendingWithID(id, fileName string) (string, string) {
	if id == "" {
		return s.NewPending(fileName)
	}

	s.mu.Lock()
	if _, exists := s.entries[id]; !exists {
		s.entries[id] = &Entry{
			ID:        id,
			FileName:  fileName,
			CreatedAt: time.Now(),
		}
	}
	s.mu.Unlock()

	return id, Scheme + id
}

// Save 保存上传内容。
func (s *Service) Save(ctx context.Context, id, fileName, mimeType string, r io.Reader, maxBytes int64) (*Entry, error) {
	s.mu.Lock()
	entry, ok := s.entries[id]
	if !ok {
		entry = &Entry{ID: id, FileName: fileName, CreatedAt: time.Now()}
		s.entries[id] = entry
	}
	s.mu.Unlock()

	// 按 ID 分目录，避免单目录文件过多
	dir := filepath.Join(s.root, id[:2], id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	// 保留原始文件名（清理路径分隔符）
	safeName := textutil.SafeFileName(fileName)
	dst := filepath.Join(dir, safeName)

	f, err := os.Create(dst)
	if err != nil {
		s.markFailed(id, err.Error())
		return nil, fmt.Errorf("创建文件失败: %w", err)
	}

	n, copyErr := io.Copy(f, io.LimitReader(r, maxBytes+1))
	closeErr := f.Close()

	if copyErr != nil {
		_ = os.Remove(dst)
		s.markFailed(id, copyErr.Error())
		return nil, fmt.Errorf("写入失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		s.markFailed(id, closeErr.Error())
		return nil, fmt.Errorf("关闭文件失败: %w", closeErr)
	}
	if n > maxBytes {
		_ = os.Remove(dst)
		msg := fmt.Sprintf("文件超过上限 %d MB", maxBytes>>20)
		s.markFailed(id, msg)
		return nil, errors.New(msg)
	}

	s.mu.Lock()
	entry.Path = dst
	entry.Size = n
	entry.MimeType = mimeType
	entry.Ready = true
	s.mu.Unlock()

	s.log.Info("文件已上传",
		slog.String("id", id),
		slog.String("name", safeName),
		slog.Int64("bytes", n),
	)

	return entry, nil
}

// markFailed 标记上传失败。
func (s *Service) markFailed(id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		e.Err = reason
	}
}

// Resolve 把伪路径解析成真实文件路径。
//
// 输入：
//   - "upload://<id>" → 查上传记录
//   - 其他（普通路径）→ 原样返回（兼容直接传真实路径的场景）
func (s *Service) Resolve(pseudoPath string) (string, error) {
	if !strings.HasPrefix(pseudoPath, Scheme) {
		return pseudoPath, nil
	}

	id := strings.TrimPrefix(pseudoPath, Scheme)
	if id == "" {
		return "", fmt.Errorf("非法上传路径")
	}

	s.mu.RLock()
	entry, ok := s.entries[id]
	var ready bool
	var errMsg string
	var path string
	if ok {
		ready = entry.Ready
		errMsg = entry.Err
		path = entry.Path
	}
	s.mu.RUnlock()

	if !ok {
		return "", ErrNotFound
	}
	if errMsg != "" {
		return "", fmt.Errorf("上传失败: %s", errMsg)
	}
	if !ready {
		return "", ErrPending
	}
	return path, nil
}

// Get 按 ID 取上传记录。
func (s *Service) Get(id string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	if !ok {
		return nil, false
	}
	cp := *e
	return &cp, true
}

// IsPseudoPath 判断是否为上传伪路径。
func IsPseudoPath(p string) bool {
	return strings.HasPrefix(p, Scheme)
}

// Cleanup 清理过期上传文件。
func (s *Service) Cleanup(ctx context.Context) (removed int) {
	deadline := time.Now().Add(-TTL)

	s.mu.Lock()
	var expired []string
	for id, e := range s.entries {
		if e.CreatedAt.Before(deadline) {
			expired = append(expired, id)
		}
	}
	s.mu.Unlock()

	for _, id := range expired {
		s.mu.RLock()
		e := s.entries[id]
		s.mu.RUnlock()
		if e != nil && e.Path != "" {
			_ = os.Remove(e.Path)
			// 清理空目录
			_ = os.Remove(filepath.Dir(e.Path))
		}
		s.mu.Lock()
		delete(s.entries, id)
		s.mu.Unlock()
		removed++
	}

	if removed > 0 {
		s.log.Info("已清理过期上传文件", slog.Int("count", removed))
	}
	return removed
}

// --- 内部工具 ---

// newID 生成 32 位十六进制 ID。
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sanitizeFileName 清理文件名，去掉路径分隔符与危险字符。
