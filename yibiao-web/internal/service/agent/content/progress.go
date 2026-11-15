package content

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/patrick/yibiao-web/internal/service/textutil"
)

// SectionProgress 是工作区扫描出的单节进度。
type SectionProgress struct {
	NodeID  string
	Title   string
	Status  string // pending | running | success | error
	Content string
	Words   int
	Error   string
}

// ScanWorkspaceProgress 扫描工作区 manifest + sections，得到 UI 可用的小节进度。
//
// 优先读 manifest；manifest 缺失或未更新时，按预期文件名探测磁盘上的 .md。
func ScanWorkspaceProgress(workspaceDir string, expected []Section) ([]SectionProgress, int) {
	byID := map[string]SectionProgress{}
	for _, s := range expected {
		byID[s.NodeID] = SectionProgress{
			NodeID: s.NodeID,
			Title:  s.Title,
			Status: "pending",
		}
	}

	manifestPath := filepath.Join(workspaceDir, ManifestFile)
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var manifest Manifest
		if json.Unmarshal(cleanJSON(raw), &manifest) == nil {
			for _, item := range manifest.Generated {
				st := normalizeSectionStatus(item.Status)
				prog := SectionProgress{
					NodeID: item.NodeID,
					Title:  item.Title,
					Status: st,
					Error:  item.Error,
					Words:  item.Words,
				}
				if content, readErr := readSectionFile(workspaceDir, item); readErr == nil {
					prog.Content = content
					if prog.Words <= 0 {
						prog.Words = textutil.CountReadableWords(content)
					}
					if st == "pending" && strings.TrimSpace(content) != "" {
						prog.Status = "success"
					}
					if st == "done" || st == "success" {
						prog.Status = "success"
					}
				} else if st == "done" || st == "success" {
					// manifest 说完成但文件不在：仍记 pending，避免虚报
					prog.Status = "pending"
				}
				if item.NodeID != "" {
					byID[item.NodeID] = prog
				}
			}
		}
	}

	// 兜底：按预期文件名扫磁盘（Agent 可能写了文件但未及时更新 manifest）
	for _, s := range expected {
		cur := byID[s.NodeID]
		if cur.Status == "success" && strings.TrimSpace(cur.Content) != "" {
			continue
		}
		file := sectionFileName(s)
		candidates := []string{
			filepath.Join(workspaceDir, SectionsDir, file),
			filepath.Join(workspaceDir, SectionsDir, s.NodeID+".md"),
		}
		for _, p := range candidates {
			data, err := os.ReadFile(p)
			if err != nil || len(strings.TrimSpace(string(data))) == 0 {
				continue
			}
			content := string(data)
			cur.Content = content
			cur.Words = textutil.CountReadableWords(content)
			cur.Status = "success"
			byID[s.NodeID] = cur
			break
		}
	}

	out := make([]SectionProgress, 0, len(expected))
	done := 0
	for _, s := range expected {
		p := byID[s.NodeID]
		if p.NodeID == "" {
			p = SectionProgress{NodeID: s.NodeID, Title: s.Title, Status: "pending"}
		}
		if p.Status == "success" {
			done++
		}
		out = append(out, p)
	}
	return out, done
}

// ProgressMap 把扫描结果转成 contentGenerationSections 形态。
func ProgressMap(items []SectionProgress) map[string]any {
	out := make(map[string]any, len(items))
	for _, item := range items {
		preview := item.Content
		if len([]rune(preview)) > 4000 {
			preview = string([]rune(preview)[:4000])
		}
		entry := map[string]any{
			"id":      item.NodeID,
			"title":   item.Title,
			"status":  item.Status,
			"content": preview,
		}
		if item.Words > 0 {
			entry["words"] = item.Words
		}
		if item.Error != "" {
			entry["error"] = item.Error
		}
		out[item.NodeID] = entry
	}
	return out
}

func normalizeSectionStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "success", "completed":
		return "success"
	case "error", "failed":
		return "error"
	case "running", "in_progress":
		return "running"
	default:
		return "pending"
	}
}
