package content

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ResumeScan 是从旧工作区扫描出的恢复信息。
type ResumeScan struct {
	// TaskID 旧任务 ID
	TaskID string
	// WorkspaceDir 工作区路径
	WorkspaceDir string
	// Completed 已完成的小节 nodeId 集合
	Completed map[string]bool
	// CompletedFiles 已完成的文件名（用于校验文件真的存在）
	CompletedFiles map[string]string
	// TotalWords 已完成小节的总字数
	TotalWords int
	// ManifestUpdatedAt 上次更新时间
	ManifestUpdatedAt time.Time
	// Pending 还没做的小节数
	Pending int
}

// Scannable 表示工作区里能扫描到进度。
//
// 判定标准：
//   - manifest 存在且能解析
//   - 至少有一节 status=done **并且** 文件真的存在
//
// 为什么两个条件都要：Agent 可能更新了 manifest 但没写文件
// （中途崩溃），这种情况不能算完成，否则恢复后会缺内容。
func (r *Runner) ScanResumeState(workspaceDir string) (*ResumeScan, error) {
	manifestPath := filepath.Join(workspaceDir, ManifestFile)

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("没有进度文件")
		}
		return nil, fmt.Errorf("读取进度文件失败: %w", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("进度文件格式错误: %w", err)
	}

	scan := &ResumeScan{
		TaskID:         manifest.TaskID,
		WorkspaceDir:   workspaceDir,
		Completed:      make(map[string]bool),
		CompletedFiles: make(map[string]string),
		TotalWords:     manifest.TotalWords,
	}

	if manifest.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, manifest.UpdatedAt); err == nil {
			scan.ManifestUpdatedAt = t
		}
	}

	for _, item := range manifest.Generated {
		if item.Status != "done" {
			scan.Pending++
			continue
		}

		// 校验文件真的存在
		if !sectionFileExists(workspaceDir, item) {
			// manifest 说完成但文件没了 → 不能算完成
			scan.Pending++
			continue
		}

		scan.Completed[item.NodeID] = true
		scan.CompletedFiles[item.NodeID] = item.File
	}

	return scan, nil
}

// sectionFileExists 校验小节的正文文件是否存在且非空。
func sectionFileExists(workspaceDir string, item SectionState) bool {
	// 优先用 manifest 里记的 path
	candidates := []string{}
	if item.Path != "" {
		candidates = append(candidates, filepath.Join(workspaceDir, item.Path))
	}
	if item.File != "" {
		candidates = append(candidates, filepath.Join(workspaceDir, SectionsDir, item.File))
	}

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

// FindResumableWorkspaces 在根目录下找出可恢复的工作区。
//
// 用途：服务重启后展示"有哪些任务可以接着做"。
//
// 排序：按 manifest 更新时间倒序（最近的在前面），
// 因为用户最可能想恢复刚中断的那个。
func FindResumableWorkspaces(rootDir string) ([]*ResumeScan, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]*ResumeScan, 0, len(entries))
	r := &Runner{}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 跳过内部目录（会话持久化等）
		if len(name) > 0 && name[0] == '_' {
			continue
		}

		dir := filepath.Join(rootDir, name)
		scan, err := r.ScanResumeState(dir)
		if err != nil {
			continue
		}

		// 一个都没完成的不算可恢复（等于从头做，没必要）
		if len(scan.Completed) == 0 {
			continue
		}

		if scan.TaskID == "" {
			scan.TaskID = name
		}
		out = append(out, scan)
	}

	// 最近更新的在前
	sort.Slice(out, func(i, j int) bool {
		return out[i].ManifestUpdatedAt.After(out[j].ManifestUpdatedAt)
	})

	return out, nil
}
