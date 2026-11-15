// Package workspace 管理 Agent 临时工作区的生命周期。
//
// 设计依据（调研自 Manus）：
//   - 工作区是临时的，任务结束就销毁
//   - 只有显式提交的产物才进资产库
//   - 失败的任务保留更久，便于排查
//
// 目录结构：<root>/<task_id>/
// 清理策略：
//   - 成功任务：保留 1 小时后删除（留时间给人下载/排查）
//   - 失败任务：保留 24 小时
//   - 超过 7 天的：无论什么状态都删
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 保留时长。
const (
	// SuccessTTL 任务成功后的保留时间
	SuccessTTL = time.Hour
	// FailureTTL 任务失败后的保留时间（更长，便于排查）
	FailureTTL = 24 * time.Hour
	// MaxTTL 任何工作区的最长保留时间
	MaxTTL = 7 * 24 * time.Hour
)

// Manager 管理工作区。
type Manager struct {
	root string
	log  *slog.Logger
}

// NewManager 创建管理器。
func NewManager(root string, log *slog.Logger) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析工作区根目录失败: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作区根目录失败: %w", err)
	}
	return &Manager{root: abs, log: log}, nil
}

// Root 返回根目录。
func (m *Manager) Root() string { return m.root }

// Create 创建一个任务工作区。
//
// taskID 必须是安全字符串（UUID），否则拒绝，防止路径穿越。
func (m *Manager) Create(taskID string) (string, error) {
	if !isSafeID(taskID) {
		return "", fmt.Errorf("非法的任务 ID: %q", taskID)
	}
	dir := filepath.Join(m.root, taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建工作区失败: %w", err)
	}
	return dir, nil
}

// Path 返回某任务的工作区路径（不创建）。
func (m *Manager) Path(taskID string) string {
	if !isSafeID(taskID) {
		return ""
	}
	return filepath.Join(m.root, taskID)
}

// Release 标记工作区可以回收。
//
// 不是立即删除，而是写入一个标记文件，由定时清理任务处理。
// 这样做的好处：
//   - 任务刚结束就删可能让人来不及排查
//   - 删除操作解耦，不会阻塞任务结束
func (m *Manager) Release(taskID string, succeeded bool) error {
	dir := m.Path(taskID)
	if dir == "" {
		return fmt.Errorf("非法任务 ID")
	}

	ttl := SuccessTTL
	if !succeeded {
		ttl = FailureTTL
	}

	deadline := time.Now().Add(ttl)
	marker := filepath.Join(dir, ".release")

	// 内容：过期时间 + 状态，清理任务据此判断
	content := fmt.Sprintf("%s\n%v\n", deadline.Format(time.RFC3339), succeeded)
	if err := os.WriteFile(marker, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入回收标记失败: %w", err)
	}

	m.log.Info("工作区已标记回收",
		slog.String("task_id", taskID),
		slog.Bool("succeeded", succeeded),
		slog.Time("delete_after", deadline),
	)
	return nil
}

// Cleanup 清理过期工作区。
//
// 判定规则：
//  1. 有 .release 标记且已过期 → 删
//  2. 无标记但目录创建时间超过 MaxTTL → 删（防止遗漏）
func (m *Manager) Cleanup(ctx context.Context) (removed int, err error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return 0, fmt.Errorf("读取工作区根目录失败: %w", err)
	}

	now := time.Now()

	for _, entry := range entries {
		if ctx.Err() != nil {
			return removed, ctx.Err()
		}
		if !entry.IsDir() {
			continue
		}

		dir := filepath.Join(m.root, entry.Name())
		shouldRemove, reason := m.shouldRemove(dir, now)
		if !shouldRemove {
			continue
		}

		if err := os.RemoveAll(dir); err != nil {
			m.log.Warn("删除工作区失败",
				slog.String("dir", dir),
				slog.String("error", err.Error()),
			)
			continue
		}

		m.log.Info("工作区已清理",
			slog.String("dir", entry.Name()),
			slog.String("reason", reason),
		)
		removed++
	}

	return removed, nil
}

// shouldRemove 判断某工作区是否该删。
func (m *Manager) shouldRemove(dir string, now time.Time) (bool, string) {
	marker := filepath.Join(dir, ".release")

	// 1. 有回收标记：按标记的过期时间
	if data, err := os.ReadFile(marker); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) > 0 {
			if deadline, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[0])); err == nil {
				if now.After(deadline) {
					return true, "已过回收时间"
				}
				return false, ""
			}
		}
		// 标记内容损坏：直接删，避免僵尸目录
		return true, "回收标记损坏"
	}

	// 2. 无标记：按目录修改时间判断（超 MaxTTL）
	info, err := os.Stat(dir)
	if err != nil {
		return false, ""
	}
	if now.Sub(info.ModTime()) > MaxTTL {
		return true, "超过最长保留期"
	}

	return false, ""
}

// Stats 返回工作区统计。
type Stats struct {
	Count      int
	TotalBytes int64
	Oldest     time.Time
}

// GetStats 统计工作区占用。
func (m *Manager) GetStats() (*Stats, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}

	stats := &Stats{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stats.Count++

		info, err := entry.Info()
		if err == nil {
			if stats.Oldest.IsZero() || info.ModTime().Before(stats.Oldest) {
				stats.Oldest = info.ModTime()
			}
		}

		dir := filepath.Join(m.root, entry.Name())
		size := dirSize(dir)
		stats.TotalBytes += size
	}
	return stats, nil
}

// dirSize 计算目录占用。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// isSafeID 判断 ID 是否安全（只允许 UUID 字符）。
func isSafeID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
