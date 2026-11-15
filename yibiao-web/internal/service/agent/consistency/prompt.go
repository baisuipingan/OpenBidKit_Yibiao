package consistency

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildBatchPrompt 构造一致性检查提示词。
//
// 这段 Prompt 决定检查质量，原则来自桌面版
// contentGenerationConsistencyTools.cjs 的审计规则，核心是**克制**：
//
//   - 只报「影响阅读理解或项目实施」的明显问题
//   - 不报措辞差异、详略差异、文风差异
//   - 不追究内容是否有材料依据（那是别的事）
//   - 对象/条件/阶段不同产生的差异不算问题
//
// 为什么要克制：标书正文很长，如果吹毛求疵会报出上百条「问题」，
// 用户根本没法处理。宁可漏报，不要误报。
func buildBatchPrompt(globalFacts string, batch []Section, index, total int) string {
	var sb strings.Builder

	// ===== 角色 =====
	sb.WriteString("你是投标文件的一致性审计专家。任务是找出正文中的**前后矛盾**，不改写内容。\n\n")

	if total > 1 {
		sb.WriteString(fmt.Sprintf("当前是第 %d/%d 批小节。\n\n", index, total))
	}

	// ===== 全局事实 =====
	if strings.TrimSpace(globalFacts) != "" {
		sb.WriteString("## 全局事实（已确定的取值，正文必须与之一致）\n\n")
		sb.WriteString(globalFacts)
		sb.WriteString("\n\n")
	}

	// ===== 检查规则 =====
	sb.WriteString("## 检查规则\n\n")
	sb.WriteString("只关注两类问题：\n\n")
	sb.WriteString("### 1. 正文前后矛盾\n")
	sb.WriteString("同一事物在不同位置的说法互相冲突，例如：\n")
	sb.WriteString("- 工期：一处写「90 个日历天」，另一处写「三个月」\n")
	sb.WriteString("- 人员：项目经理一处是「张三」，另一处是「李四」\n")
	sb.WriteString("- 指标：系统可用性一处「99.9%」，另一处「99.99%」\n")
	sb.WriteString("- 数量：服务器一处「8 台」，另一处「10 台」\n")
	sb.WriteString("- 金额：合同额一处「860 万元」，另一处「850 万元」\n\n")
	sb.WriteString("### 2. 与全局事实冲突\n")
	sb.WriteString("正文的说法与上面「全局事实」中明确写出的内容不一致。\n\n")

	// ===== 不算问题的情况（关键）=====
	sb.WriteString("## 以下情况**不算**问题，不要报告\n\n")
	sb.WriteString("- 用词、称谓、表述不同（「甲方」vs「采购人」、「系统」vs「平台」）\n")
	sb.WriteString("- 详略不同（一处详述、一处概述）\n")
	sb.WriteString("- 全局事实未提及的补充内容（岗位、流程、交付物、频次、承诺）\n")
	sb.WriteString("  —— 只要不与全局事实或本节其他内容冲突，就不算问题\n")
	sb.WriteString("- 承诺语气强弱不同（「确保」vs「保证」）\n")
	sb.WriteString("- 文风、润色、篇幅差异\n")
	sb.WriteString("- 因**对象、适用条件或时间范围不同**产生的差异\n")
	sb.WriteString("  （如「试运行期 30 天」和「质保期 3 年」是两个不同的期限，不矛盾）\n")
	sb.WriteString("- 内容是否有材料依据、是否真实\n\n")
	sb.WriteString("**只报告会影响阅读理解或项目实施的明显问题。** ")
	sb.WriteString("宁可漏报，不要因为措辞差异而误报。没有问题时 issues 为空数组。\n\n")

	// ===== 待检查正文 =====
	sb.WriteString("## 待检查正文\n\n")
	for _, s := range batch {
		sb.WriteString(fmt.Sprintf("### 小节 [%s] %s\n\n", s.NodeID, s.Title))
		sb.WriteString(stripMarkup(s.Content))
		sb.WriteString("\n\n")
	}

	// ===== 输出格式 =====
	sb.WriteString("## 输出格式\n\n")
	sb.WriteString("严格输出 JSON（不要代码块包裹，不要任何说明文字）：\n\n")
	sb.WriteString(`{
  "facts": [
    {
      "category": "人员|金额|工期|指标|数量|日期|编号|名称|其他",
      "subject": "主体，如「项目经理」「系统可用性」「建设工期」",
      "value": "取值，如「张三」「99.9%」「90 个日历天」",
      "sectionId": "小节 ID，从上面 [xxx] 原样复制",
      "sectionTitle": "小节标题",
      "quote": "包含该事实的原文片段（30 字以内）"
    }
  ],
  "issues": [
    {
      "type": "正文前后矛盾|与全局事实冲突",
      "severity": "high|medium|low",
      "subject": "涉及的主体",
      "description": "问题描述，说明哪里和哪里不一致",
      "evidence": [
        {
          "sectionId": "小节 ID",
          "sectionTitle": "小节标题",
          "quote": "该处的原文片段"
        }
      ],
      "suggestion": "建议统一成什么取值"
    }
  ]
}
`)

	sb.WriteString("\n## 字段说明\n\n")
	sb.WriteString("- `facts`：**抽取所有可核对的事实**，即使没有矛盾也要抽取（供跨批合并比对）\n")
	sb.WriteString("  - 重点是人员姓名、金额、工期天数、技术指标、数量、日期、编号、专有名称\n")
	sb.WriteString("  - 每节通常 3-10 条，不要抽取形容词和主观描述\n")
	sb.WriteString("- `issues`：只报矛盾，不报单处事实\n")
	sb.WriteString("- `severity`：\n")
	sb.WriteString("  - `high`：金额/人员/工期/编号/名称不一致（会引发质疑或废标）\n")
	sb.WriteString("  - `medium`：指标/数量不一致\n")
	sb.WriteString("  - `low`：表述不统一，不影响理解\n")
	sb.WriteString("- `evidence`：至少两条（矛盾的两处），每条带原文片段作证据\n\n")

	sb.WriteString("只输出 JSON，现在开始。\n")

	return sb.String()
}

// stripMarkup 去掉正文里的标记，只留可读文本。
//
// 目的：让模型注意力集中在文字内容上，不被 HTML 标签和图片提示词干扰。
// 同时保留图注文字（那也可能是矛盾来源，如「图 1-1」编号重复）。
func stripMarkup(content string) string {
	s := content

	// 去掉 figure 里的 template（配图提示词不是正文内容）
	s = removeBetween(s, `<template data-yb-role="prompt">`, `</template>`)

	// 去掉所有 HTML 标签，但保留 figcaption / caption 里的文字
	// 做法：先把 figcaption 和 caption 的内容提取出来，去掉标签后再拼回去
	var captions []string
	captions = append(captions, extractAll(s, "<figcaption>", "</figcaption>")...)
	captions = append(captions, extractAll(s, "<caption>", "</caption>")...)

	// 去掉标签
	s = stripTags(s)

	// 去掉 Markdown 标记符号（保留文字）
	s = removeMarkdownMarkers(s)

	// 图注追加到末尾（模型能读到编号）
	if len(captions) > 0 {
		s += "\n\n图注：" + strings.Join(captions, "；")
	}

	// 压缩连续空行
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(s)
}

// removeBetween 删除 start 和 end 之间的内容（含标记本身）。
func removeBetween(s, start, end string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, start)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			break
		}
		s = rest[j+len(end):]
	}
	return b.String()
}

// extractAll 提取全部 start...end 之间的内容。
func extractAll(s, start, end string) []string {
	var out []string
	for {
		i := strings.Index(s, start)
		if i < 0 {
			break
		}
		rest := s[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			break
		}
		text := strings.TrimSpace(rest[:j])
		if text != "" {
			out = append(out, text)
		}
		s = rest[j+len(end):]
	}
	return out
}

// stripTags 去掉 HTML 标签。
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
			// 标签替换成空格，避免「前文后文」粘连
			b.WriteRune(' ')
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// removeMarkdownMarkers 去掉 Markdown 标记但保留文字。
func removeMarkdownMarkers(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		// 标题标记
		t = strings.TrimLeft(t, "#")
		// 引用标记
		if strings.HasPrefix(t, ">") {
			t = strings.TrimPrefix(t, ">")
		}
		// 列表标记
		for _, prefix := range []string{"- ", "* ", "+ "} {
			if strings.HasPrefix(t, prefix) {
				t = strings.TrimPrefix(t, prefix)
				break
			}
		}
		// 粗体/斜体/代码标记
		t = strings.ReplaceAll(t, "**", "")
		t = strings.ReplaceAll(t, "__", "")
		t = strings.ReplaceAll(t, "`", "")

		lines[i] = strings.TrimSpace(t)
	}
	return strings.Join(lines, "\n")
}

// buildRepairPrompt 构造修复提示词。
//
// **关键设计（踩坑后重写）**：
//
//	第一版 Prompt 的问题是只说「这里矛盾」，模型会认为
//	「我这处没错，是对方错」，于是原样返回「无需修改」。
//
//	现在必须明确告诉模型三件事：
//	  1. 统一结论是什么（Canonical）
//	  2. 本节现在是哪个值（Current）
//	  3. 你要把 Current 改成 Canonical（如果不同）
func buildRepairPrompt(sec Section, issues []Issue, resolutions []Resolution, facts []Fact) string {
	var sb strings.Builder

	sb.WriteString("你是投标文件的校对员。请按给定的统一结论修改本节的取值。\n\n")

	// ===== 必须执行的修改 =====
	sb.WriteString("## 必须执行的修改\n\n")
	sb.WriteString("下面是本节的取值与全文统一结论的差异。**你必须修改本节**，使之与统一结论一致。\n\n")

	modCount := 0
	for _, res := range resolutions {
		// 找出本节里这个矛盾涉及的原文
		var quotes []string
		for _, e := range res.Issue.Evidence {
			if e.SectionID == sec.NodeID && e.Quote != "" {
				quotes = append(quotes, e.Quote)
			}
		}
		if len(quotes) == 0 {
			continue // 本节与该矛盾无关
		}

		modCount++
		sb.WriteString(fmt.Sprintf("### 修改 %d：%s\n\n", modCount, res.Subject))
		sb.WriteString(fmt.Sprintf("- **统一结论（必须用这个）**：%s\n", res.Canonical))
		sb.WriteString("- 本节当前写法：\n")
		for _, q := range quotes {
			sb.WriteString(fmt.Sprintf("  > %s\n", q))
		}
		// 判断本节是否已经一致
		same := false
		for _, q := range quotes {
			if strings.Contains(q, res.Canonical) {
				same = true
				break
			}
		}
		if same {
			sb.WriteString(fmt.Sprintf("- 本节已经是「%s」，**这一项不用改**\n", res.Canonical))
		} else {
			sb.WriteString(fmt.Sprintf("- 请把本节的相关表述改为「%s」\n", res.Canonical))
		}
		sb.WriteString("\n")
	}

	if modCount == 0 {
		sb.WriteString("（本节没有需要修改的内容）\n\n")
	}

	// ===== 相关事实 =====
	if len(facts) > 0 {
		var relevant []Fact
		for _, f := range facts {
			if f.SectionID == sec.NodeID {
				relevant = append(relevant, f)
			}
		}
		if len(relevant) > 0 {
			sb.WriteString("## 本节的事实（供参考，不要改动）\n\n")
			for _, f := range relevant {
				sb.WriteString(fmt.Sprintf("- [%s] %s = %s\n", f.Category, f.Subject, f.Value))
			}
			sb.WriteString("\n")
		}
	}

	// ===== 修改要求 =====
	sb.WriteString("## 修改要求\n\n")
	sb.WriteString("1. **严格按「统一结论」修改取值**，不要自行选别的\n")
	sb.WriteString("2. **只动与统一结论不一致的那处表述**，其他文字一字不改\n")
	sb.WriteString("3. **保留原有的 Markdown 结构**（标题、列表、表格、段落）\n")
	sb.WriteString("4. **保留配图标签**（`<figure>` 块）原样不动\n")
	sb.WriteString("5. 不要在正文里加解释、批注或修改说明\n\n")

	// ===== 待修改正文 =====
	sb.WriteString("## 待修改的正文\n\n")
	sb.WriteString("````markdown\n")
	sb.WriteString(sec.Content)
	sb.WriteString("\n````\n\n")

	// ===== 输出格式 =====
	sb.WriteString("## 输出格式\n\n")
	sb.WriteString("严格输出 JSON（不要代码块包裹）：\n\n")
	sb.WriteString(`{
  "revised": "修改后的完整正文",
  "changes": [
    "把「张建军」改为「张建国」"
  ]
}
`)
	sb.WriteString("\n`revised` 必须是**完整正文**（含全部段落），不是片段。\n")
	sb.WriteString("`changes` 每项一句话。如果本节确实无需修改，`changes` 为空数组且 `revised` 原样返回。\n\n")

	sb.WriteString("只输出 JSON，现在开始。\n")

	return sb.String()
}

// parseRepairResult 解析修复结果。
func parseRepairResult(text string) (revised string, changes []string) {
	cleaned := cleanJSON(text)

	var raw struct {
		Revised string   `json:"revised"`
		Changes []string `json:"changes"`
	}
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		// 解析失败时，如果返回的是纯 Markdown 正文，也接受
		// （模型有时直接返回正文）
		if looksLikeMarkdown(cleaned) {
			return cleaned, []string{"（模型未返回结构化改动说明）"}
		}
		return "", nil
	}

	return raw.Revised, raw.Changes
}

// looksLikeMarkdown 粗判是否为 Markdown 正文。
func looksLikeMarkdown(s string) bool {
	if len(s) < 50 {
		return false
	}
	// 含标题或段落特征
	return strings.Contains(s, "#") || strings.Contains(s, "\n\n")
}
