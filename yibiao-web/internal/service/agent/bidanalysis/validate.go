package bidanalysis

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Issue 是校验发现的问题。
// severity: blocking（阻塞流程）| quality（质量瑕疵）
type Issue struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// ItemID 关联到哪个解析项，便于前端定位
	ItemID string `json:"itemId,omitempty"`
}

// ValidationReport 是校验报告，传给 Agent 的提交策略。
type ValidationReport struct {
	Value  any     `json:"value"`
	Issues []Issue `json:"issues"`
	// Progress 是剩余待完成数，用于策略判断「有没有进展」
	Progress int `json:"progress"`
	// MinimumGoal 是最后一轮修复的最低目标
	MinimumGoal string `json:"minimumGoal,omitempty"`
}

// ValidateResult 校验 Agent 的分析产物。
//
// 核心判断：
//   - 必填项缺失 = blocking（流程无法继续）
//   - 可选项缺失 = 不算问题（本来就可以没有）
//   - 内容为空或过短 = quality（质量不好但可接受）
//   - JSON 格式错误 = blocking（程序读不了）
//
// 与桌面版的差异：桌面版逐项校验 AI 返回的文本；
// Web 版校验的是 Agent 写入的文件，更接近最终产物。
func ValidateResult(workspace string, items []Item) (*ValidationReport, error) {
	var issues []Issue
	results := make(map[string]any, len(items))
	missingRequired := 0

	for _, item := range items {
		content, found, err := readItemResult(workspace, item)
		if err != nil {
			issues = append(issues, Issue{
				Severity: "blocking",
				Message:  fmt.Sprintf("「%s」读取失败：%v", item.Label, err),
				ItemID:   item.ID,
			})
			if item.Required {
				missingRequired++
			}
			continue
		}

		// 文件不存在
		if !found {
			if item.Required {
				missingRequired++
				issues = append(issues, Issue{
					Severity: "blocking",
					Message:  fmt.Sprintf("必填项「%s」未生成，请提取后写入 items/%s.%s", item.Label, item.ID, extFor(item.Output)),
					ItemID:   item.ID,
				})
			}
			continue
		}

		// 内容为空
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			if item.Required {
				missingRequired++
				issues = append(issues, Issue{
					Severity: "blocking",
					Message:  fmt.Sprintf("必填项「%s」内容为空，请重新提取", item.Label),
					ItemID:   item.ID,
				})
			}
			continue
		}

		// JSON 项：必须是合法 JSON
		if item.Output == "json" {
			var parsed map[string]any
			// 容忍 Agent 用代码块包裹
			cleaned := stripCodeFence(trimmed)
			if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
				issues = append(issues, Issue{
					Severity: "blocking",
					Message:  fmt.Sprintf("「%s」输出不是合法 JSON，请修正格式（只输出 JSON，不要加说明文字）", item.Label),
					ItemID:   item.ID,
				})
				continue
			}
			// 字段是否齐全
			expected := extractSchemaKeys(item.Schema)
			var missingKeys []string
			for _, k := range expected {
				if _, ok := parsed[k]; !ok {
					missingKeys = append(missingKeys, k)
				}
			}
			if len(missingKeys) > 0 {
				issues = append(issues, Issue{
					Severity: "blocking",
					Message:  fmt.Sprintf("「%s」缺少字段 %s，请按给定 JSON 结构补齐", item.Label, strings.Join(missingKeys, "、")),
					ItemID:   item.ID,
				})
			}
			results[item.ID] = parsed
			continue
		}

		// Markdown 项：检查是否太短（可能敷衍）
		if len([]rune(trimmed)) < 10 {
			issues = append(issues, Issue{
				Severity: "quality",
				Message:  fmt.Sprintf("「%s」内容过短（%d 字），请补充更多细节", item.Label, len([]rune(trimmed))),
				ItemID:   item.ID,
			})
		}
		results[item.ID] = trimmed
	}

	return &ValidationReport{
		Value:    results,
		Issues:   issues,
		Progress: missingRequired,
		MinimumGoal: fmt.Sprintf(
			"补齐全部 %d 个必填解析项（%s），使后续目录生成能读到完整的招标信息。",
			countRequired(items), requiredLabels(items),
		),
	}, nil
}

// readItemResult 读某项的产物文件。
func readItemResult(workspace string, item Item) (content string, found bool, err error) {
	// 两种扩展名都试，Agent 可能用任意一种
	candidates := []string{
		filepath.Join(workspace, "items", item.ID+"."+extFor(item.Output)),
		filepath.Join(workspace, "items", item.ID+".md"),
		filepath.Join(workspace, "items", item.ID+".json"),
		filepath.Join(workspace, item.ID+"."+extFor(item.Output)),
	}

	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			return string(b), true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

// ValidateSummary 检查 Agent 是否写了汇总文件。
func ValidateSummary(workspace string, items []Item) (*ValidationReport, error) {
	report, err := ValidateResult(workspace, items)
	if err != nil {
		return nil, err
	}

	summaryPath := filepath.Join(workspace, "summary.json")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
		report.Issues = append(report.Issues, Issue{
			Severity: "quality",
			Message:  "缺少 summary.json，请在完成后写出已完成的解析项清单",
		})
	}
	return report, nil
}

func extFor(output string) string {
	if output == "json" {
		return "json"
	}
	return "md"
}

// stripCodeFence 去掉 Markdown 代码块包裹。
// Agent 经常把 JSON 包在 ```json ... ``` 里。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 去掉首行 ```json 或 ```
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	lines = lines[1:]
	// 去掉结尾 ```
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			lines = lines[:i]
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractSchemaKeys 从 JSON 结构字符串里取 key。
// 例如 {"a":"描述","b":"描述"} → ["a","b"]
func extractSchemaKeys(schema string) []string {
	if schema == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(schema), &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func countRequired(items []Item) int {
	n := 0
	for _, i := range items {
		if i.Required {
			n++
		}
	}
	return n
}

func requiredLabels(items []Item) string {
	var labels []string
	for _, i := range items {
		if i.Required {
			labels = append(labels, i.Label)
		}
	}
	return strings.Join(labels, "、")
}
