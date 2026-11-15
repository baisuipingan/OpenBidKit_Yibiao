package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/patrick/yibiao-web/internal/service"
)

// applyContentPlanDefaults 在请求未带字数/事实时，从已保存的技术方案状态补齐。
func applyContentPlanDefaults(req *generateRequest, state *service.PlanState) string {
	if req == nil || state == nil {
		return ""
	}

	if req.TotalWords <= 0 {
		req.TotalWords = firstPositiveInt(
			mapInt(state.OutlineWordControlSnapshot, "minimumWords"),
			mapInt(state.OutlineWordControlSnapshot, "maximumWords"),
			mapInt(state.OutlineWordControlOptions, "minimumWords"),
			mapInt(state.OutlineWordControlOptions, "maximumWords"),
		)
	}
	// 有全文总量时由 collectSections 按叶子均分，不要用 sectionWords 覆盖成「每节 500×叶子数」。
	if req.TotalWords <= 0 && req.TargetWordsPerSection <= 0 {
		req.TargetWordsPerSection = firstPositiveInt(
			mapInt(state.OutlineWordControlSnapshot, "sectionWords"),
			mapInt(state.OutlineWordControlOptions, "sectionWords"),
		)
	}

	return formatGlobalFactsMarkdown(state.GlobalFacts)
}

func formatGlobalFactsMarkdown(facts []any) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range facts {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := m["title"].(string)
		content, _ := m["content"].(string)
		title = strings.TrimSpace(title)
		content = strings.TrimSpace(content)
		if title == "" && content == "" {
			continue
		}
		if title != "" {
			b.WriteString("## ")
			b.WriteString(title)
			b.WriteString("\n\n")
		}
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func mapInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	default:
		return 0
	}
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
