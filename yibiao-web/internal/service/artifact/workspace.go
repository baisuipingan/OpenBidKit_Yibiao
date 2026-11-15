package artifact

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/patrick/yibiao-web/internal/service/storage"
)

// CommitRules 决定工作区里哪些文件提交为资产。
//
// 设计依据（调研自 Manus 的「回收后只恢复重要文件」）：
// 不是工作区里所有文件都值得进资产库。中间产物、缓存、
// 临时文件应该丢弃，只保留真正的交付物。
type CommitRules struct {
	// TaskID 关联到产出这些资产的 Agent 任务
	TaskID string

	// Kind 资产类别
	Kind string

	// Include 白名单（glob）。为空表示全部文件。
	// 例：[]string{"*.md", "items/*.json"}
	Include []string

	// Exclude 黑名单（glob），优先级高于 Include。
	// 不填时用 DefaultExcludes。
	Exclude []string

	// MaxFileBytes 单文件上限，0 表示用服务默认值
	MaxFileBytes int64

	// MaxTotalBytes 总量上限，超出后停止提交（防止爆存储）
	MaxTotalBytes int64

	// Source 来源标记，默认 "agent"
	Source string

	// Prefix 资产名加前缀，避免不同任务同名产物冲突。
	// 例：Prefix = "bid-analysis-20261114"，则 "items/a.json" 变成
	// "bid-analysis-20261114/items/a.json"
	Prefix string
}

// DefaultExcludes 是默认忽略的文件模式。
//
// 这些文件不该进资产库：临时文件、缓存、日志、系统文件。
var DefaultExcludes = []string{
	"**/node_modules/**",
	"**/.git/**",
	"**/__pycache__/**",
	"**/*.tmp",
	"**/*.log",
	"**/.DS_Store",
	"**/Thumbs.db",
	// Agent 常见的临时目录
	".cache/**",
	"tmp/**",
	"temp/**",
	// 会话文件（由 sidecar 自己管）
	"sessions/**",
}

// CommitResult 是提交结果。
type CommitResult struct {
	Assets  []*Asset
	Skipped []SkippedFile
	// TotalBytes 实际提交的总字节
	TotalBytes int64
	// Truncated 是否因为超限提前停止
	Truncated bool
}

// SkippedFile 记录被跳过的文件及原因。
type SkippedFile struct {
	Path   string
	Reason string
}

// CommitWorkspace 把工作区里的产物提交为资产。
//
// 这是「沙箱 → 资产库」的关键一步，实现调研里的
// "explicit export" 原则：Agent 写文件 ≠ 用户能拿到文件，
// 中间必须有显式的登记步骤。
//
// 流程：
//  1. 遍历工作区，按 Include/Exclude 筛选
//  2. 逐文件检查大小
//  3. 上传到存储后端并登记 assets 表
//  4. 返回资产列表
func (s *Service) CommitWorkspace(
	ctx context.Context,
	scope storage.Scope,
	ownerID string,
	workspaceDir string,
	rules CommitRules,
) (*CommitResult, error) {
	if scope.TenantID == "" {
		return nil, fmt.Errorf("缺少租户标识")
	}
	if workspaceDir == "" {
		return nil, fmt.Errorf("缺少工作区目录")
	}
	if rules.Kind == "" {
		return nil, fmt.Errorf("缺少资产类别")
	}
	if rules.Source == "" {
		rules.Source = "agent"
	}

	root, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("解析工作区路径失败: %w", err)
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("工作区不存在: %w", err)
	}

	maxFile := rules.MaxFileBytes
	if maxFile <= 0 {
		maxFile = s.maxFileBytes
	}

	excludes := rules.Exclude
	if len(excludes) == 0 {
		excludes = DefaultExcludes
	}

	result := &CommitResult{
		Assets:  make([]*Asset, 0, 16),
		Skipped: make([]SkippedFile, 0, 8),
	}

	// 遍历工作区
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 单个文件读不了不中断整个提交
			result.Skipped = append(result.Skipped, SkippedFile{Path: path, Reason: walkErr.Error()})
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 相对路径（用 / 分隔，跨平台一致）
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			// 目录命中排除规则就整棵跳过
			if matchesAny(rel, excludes) {
				return fs.SkipDir
			}
			return nil
		}

		// 文件筛选
		if matchesAny(rel, excludes) {
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: "命中排除规则"})
			return nil
		}
		if len(rules.Include) > 0 && !matchesAny(rel, rules.Include) {
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: "不在包含列表"})
			return nil
		}

		// 大小检查
		info, err := d.Info()
		if err != nil {
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: err.Error()})
			return nil
		}
		if info.Size() > maxFile {
			result.Skipped = append(result.Skipped, SkippedFile{
				Path:   rel,
				Reason: fmt.Sprintf("超过单文件上限（%d MB）", maxFile>>20),
			})
			return nil
		}
		if rules.MaxTotalBytes > 0 && result.TotalBytes+info.Size() > rules.MaxTotalBytes {
			result.Truncated = true
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: "超过总量上限"})
			return fs.SkipAll
		}

		// 打开并提交
		f, err := os.Open(path)
		if err != nil {
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: err.Error()})
			return nil
		}
		defer f.Close()

		assetName := rel
		if rules.Prefix != "" {
			assetName = rules.Prefix + "/" + rel
		}

		asset, err := s.Put(ctx, PutRequest{
			Scope:   scope,
			OwnerID: ownerID,
			Name:    assetName,
			Kind:    rules.Kind,
			Content: f,
			Size:    info.Size(),
			Source:  rules.Source,
			TaskID:  rules.TaskID,
			Metadata: map[string]any{
				"workspacePath": rel,
			},
		})
		if err != nil {
			s.log.Warn("提交文件失败",
				slog.String("path", rel),
				slog.String("error", err.Error()),
			)
			result.Skipped = append(result.Skipped, SkippedFile{Path: rel, Reason: err.Error()})
			return nil
		}

		result.Assets = append(result.Assets, asset)
		result.TotalBytes += info.Size()
		return nil
	})

	if walkErr != nil && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return result, fmt.Errorf("遍历工作区失败: %w", walkErr)
	}

	s.log.Info("工作区产物已提交",
		slog.String("tenant", scope.TenantID),
		slog.String("project", scope.ProjectID),
		slog.String("kind", rules.Kind),
		slog.Int("committed", len(result.Assets)),
		slog.Int("skipped", len(result.Skipped)),
		slog.Int64("total_bytes", result.TotalBytes),
	)

	return result, nil
}

// MaterializeToWorkspace 把资产「物化」到工作区（下载到本地）。
//
// 用途：Agent 任务开始前，把需要的输入文件准备好。
// 这是「存储 → 沙箱」的方向，与 CommitWorkspace 相反。
func (s *Service) MaterializeToWorkspace(
	ctx context.Context,
	scope storage.Scope,
	assetName string,
	destPath string,
) error {
	rc, asset, err := s.Get(ctx, scope, assetName, 0)
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer f.Close()

	if _, err := copyWithLimit(f, rc, s.maxFileBytes); err != nil {
		return fmt.Errorf("写入失败: %w", err)
	}

	s.log.Debug("资产已物化到工作区",
		slog.String("name", asset.Name),
		slog.String("dest", destPath),
		slog.Int64("size", asset.SizeBytes),
	)
	return nil
}

// copyWithLimit 复制并限制总量。
func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	// LimitReader 多给 1 字节用于检测超限
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("内容超过上限 %d MB", limit>>20)
	}
	return n, nil
}

// matchesAny 判断路径是否匹配任一 glob 模式。
//
// 支持 ** 匹配任意层级目录。
func matchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

// matchGlob 实现带 ** 的简单 glob 匹配。
//
// 不引入第三方库：规则简单（只有 * 和 **），自己实现更可控。
func matchGlob(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}

	// 完全相等
	if pattern == path {
		return true
	}

	// 处理 ** 前缀：**/foo 匹配任意深度的 foo
	if strings.HasPrefix(pattern, "**/") {
		rest := pattern[3:]
		// 匹配任意层级的结尾
		if matchGlob(rest, path) {
			return true
		}
		// 递归剥离一层目录再试
		if i := strings.IndexByte(path, '/'); i >= 0 {
			return matchGlob(pattern, path[i+1:])
		}
		return false
	}

	// 处理 ** 后缀：foo/** 匹配 foo 下所有
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}

	// 单层 * 匹配：按段比较
	patternSegs := strings.Split(pattern, "/")
	pathSegs := strings.Split(path, "/")

	if len(patternSegs) != len(pathSegs) {
		return false
	}
	for i := range patternSegs {
		if !matchSegment(patternSegs[i], pathSegs[i]) {
			return false
		}
	}
	return true
}

// matchSegment 单段 glob 匹配（* 匹配任意字符序列）。
func matchSegment(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}

	// 简单的 * 匹配：拆成前后缀
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		return strings.HasPrefix(s, parts[0]) && strings.HasSuffix(s, parts[1])
	}

	// 多个 *：逐段定位
	idx := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		pos := strings.Index(s[idx:], part)
		if pos < 0 {
			return false
		}
		if i == 0 && pos != 0 {
			return false // 第一段必须从头开始
		}
		idx += pos + len(part)
	}
	// 最后一段必须在结尾
	return strings.HasSuffix(s, parts[len(parts)-1])
}
