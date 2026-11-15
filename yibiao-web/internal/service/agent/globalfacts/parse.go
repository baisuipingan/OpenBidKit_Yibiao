package globalfacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Group 是前端 GlobalFactGroupState：{id,title,content}。
type Group struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ParseFile 读取工作区 global-facts.json 并归一化为 groups。
func ParseFile(workspaceDir string) ([]Group, error) {
	data, err := os.ReadFile(filepath.Join(workspaceDir, OutputFile))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", OutputFile, err)
	}
	return ParseJSON(data)
}

// ParseJSON 解析 Agent 产出的 JSON。
func ParseJSON(data []byte) ([]Group, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON: %w", OutputFile, err)
	}
	groups := normalizeGroups(raw)
	if len(groups) == 0 {
		return nil, fmt.Errorf("%s 缺少有效 groups", OutputFile)
	}
	for i, g := range groups {
		if g.ID == "" || g.Title == "" || strings.TrimSpace(g.Content) == "" {
			return nil, fmt.Errorf("全局事实第 %d 项缺少 id、title 或 content", i+1)
		}
	}
	return groups, nil
}

func normalizeGroups(raw any) []Group {
	var list []any
	switch v := raw.(type) {
	case []any:
		list = v
	case map[string]any:
		if nested, ok := v["result"]; ok {
			if m, ok := nested.(map[string]any); ok {
				v = m
			}
		}
		for _, key := range []string{"groups", "facts", "items"} {
			if arr, ok := v[key].([]any); ok {
				list = arr
				break
			}
		}
	}
	used := map[string]bool{}
	out := make([]Group, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title := singleLine(firstString(m, "title", "name", "category", "label"))
		content := valueToMarkdown(firstAny(m, "content", "markdown", "facts", "items", "details", "description"))
		if title == "" || content == "" {
			continue
		}
		id := ensureUniqueID(normalizeFactID(firstString(m, "id", "group_id", "key"), title, i), used)
		out = append(out, Group{ID: id, Title: title, Content: content})
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstAny(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func valueToMarkdown(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case []any:
		lines := make([]string, 0, len(x))
		for _, item := range x {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s == "" {
				continue
			}
			if !strings.HasPrefix(s, "- ") {
				s = "- " + s
			}
			lines = append(lines, s)
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		b, _ := json.MarshalIndent(x, "", "  ")
		return string(b)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

var nonID = regexp.MustCompile(`[^a-z0-9_-]+`)

func normalizeFactID(raw, title string, index int) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		s = strings.ToLower(title)
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			continue
		}
		b.WriteRune(r)
	}
	s = nonID.ReplaceAllString(strings.ReplaceAll(b.String(), " ", "_"), "_")
	s = strings.Trim(s, "_-")
	if s == "" {
		s = fmt.Sprintf("fact_%d", index+1)
	}
	return s
}

func ensureUniqueID(id string, used map[string]bool) string {
	base := id
	for i := 2; used[id]; i++ {
		id = fmt.Sprintf("%s_%d", base, i)
	}
	used[id] = true
	return id
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// GroupsToAny 转成 []any 便于写入 extra JSON。
func GroupsToAny(groups []Group) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{
			"id":      g.ID,
			"title":   g.Title,
			"content": g.Content,
		})
	}
	return out
}
