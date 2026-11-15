package knowledge

import (
	"regexp"
	"strings"
)

// Filter 过滤噪音块。
//
// 为什么要过滤：一份 200 页的招标/方案文档里，
// 页码、目录、封面、签字页、页眉页脚这些内容可能占 30%以上。
// 它们提炼不出知识点，还会污染检索结果。
//
// 规则移植自桌面版 filterBlocks，保证判定一致。
func Filter(blocks []RawBlock) []Block {
	// 先算重复文本出现次数（页眉页脚会重复出现）
	repeated := make(map[string]int, len(blocks))
	for _, b := range blocks {
		key := normalizeRepeated(b.Content)
		if key != "" && len([]rune(key)) <= 80 {
			repeated[key]++
		}
	}

	out := make([]Block, 0, len(blocks))
	for i, b := range blocks {
		reason := ""
		text := strings.TrimSpace(b.Content)

		switch {
		case text == "":
			reason = "空内容"

		// 重复检测优先于封面判断：
		// 页眉里常含"投标文件"这类封面词，会被 isCover 抢走；
		// 但"同一段文本出现 3 次以上"是页眉页脚的强证据，先判。
		case isRepeatedHeaderFooter(text, repeated):
			reason = "重复的页眉页脚"

		case isPageNumber(text):
			reason = "页码"

		case isCatalog(text):
			reason = "目录"

		case isCover(text, i):
			reason = "封面信息"

		case isSignature(text):
			reason = "签字盖章页"

		case contentChars(text) < 12:
			// 太短的碎片提炼不出知识点。
			// 12 字是经验值：再短基本是"（1）"、"如下："这类残片。
			reason = "内容过短"
		}

		out = append(out, Block{
			RawBlock:     b,
			Filtered:     reason != "",
			FilterReason: reason,
		})
	}

	return out
}

var (
	// reCatalogEntry 目录行带页码引导符
	reCatalogEntry = regexp.MustCompile(`(?:\.{2,}|…{2,}|·{2,}|\s{4,})\s*\d+\s*$`)
	// reSignature 签字相关词
	reSignature = regexp.MustCompile(`(盖章|签章|签名|法定代表人|授权代表|委托代理人|被授权人|投标人代表签字|代表签字)`)
	// reRealSignature 真正的签名栏（不该过滤，是有效内容）
	reRealSignature = regexp.MustCompile(`(签字确认|用户签字|双方责任人.{0,12}签字)`)
	// reDateOnly 纯日期行
	reDateOnly = regexp.MustCompile(`^[0-9]{4}\s*年\s*[0-9]{1,2}\s*月\s*[0-9]{1,2}\s*日$`)
)

// coverMarkers 封面常见词。
var coverMarkers = []string{
	"投标文件", "投标书", "正本", "副本", "项目名称",
	"招标编号", "投标人", "编制日期", "日期：", "日期:",
}

// isPageNumber 判断是否页码。
func isPageNumber(text string) bool {
	compact := compactText(text)
	if compact == "" {
		return false
	}
	return rePageNumber.MatchString(compact)
}

// isCatalog 判断是否目录。
//
// 两个特征：
//  1. 标题就是"目录"/"目次"/"Contents"
//  2. 超过 60% 的行都是"标题 ...... 页码"格式
func isCatalog(text string) bool {
	compact := compactText(text)
	if compact == "" {
		return false
	}
	if reCatalogTitle.MatchString(compact) {
		return true
	}

	lines := strings.Split(text, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) < 2 {
		return false
	}

	catalogLines := 0
	for _, line := range nonEmpty {
		if reCatalogEntry.MatchString(line) {
			catalogLines++
		}
	}

	// 60% 以上是目录行
	return catalogLines*100/len(nonEmpty) >= 60
}

// isCover 判断是否封面信息。
//
// 只在前 12 个块里判断 —— 封面在文档开头。
// 条件：含封面关键词，且不是长句（封面都是短标签）。
func isCover(text string, index int) bool {
	if index > 12 {
		return false
	}

	compact := compactText(text)
	if compact == "" || len([]rune(compact)) > 220 {
		return false
	}

	hasMarker := false
	for _, m := range coverMarkers {
		if strings.Contains(compact, m) {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		return false
	}

	// 有句末标点且超过 80 字 → 是正文，不是封面
	hasLongSentence := reLongSentence.MatchString(text) && len([]rune(text)) > 80
	return !hasLongSentence
}

// isSignature 判断是否签字盖章页。
//
// 真正的签名栏（"投标人代表签字确认"）要保留，
// 纯盖章信息（"（盖章）"、"法定代表人：___"）才过滤。
func isSignature(text string) bool {
	compact := compactText(text)
	if compact == "" || len([]rune(compact)) > 260 {
		return false
	}

	// 有"签字确认"这类实质内容 → 不过滤
	if reRealSignature.MatchString(compact) {
		return false
	}

	if !reSignature.MatchString(compact) {
		return false
	}

	// 有句末标点且后面还有 20 字以上 → 是正文提到"盖章"，不是盖章页
	return !regexp.MustCompile(`[。！？；].{20,}`).MatchString(text)
}

// isRepeatedHeaderFooter 判断是否重复的页眉页脚。
//
// 同一段文本在文档里出现 3 次以上，且长度不超过 80 字，
// 基本就是页眉页脚或水印。
func isRepeatedHeaderFooter(text string, repeated map[string]int) bool {
	key := normalizeRepeated(text)
	if key == "" {
		return false
	}
	if len([]rune(key)) > 80 {
		return false
	}
	return repeated[key] >= 3
}

// normalizeRepeated 归一化重复文本（去空白和标点差异）。
func normalizeRepeated(text string) string {
	var sb strings.Builder
	for _, r := range text {
		switch r {
		case ' ', '\t', '\n', '\r', '　', '.', '·', '…', '-', '—', '_', '*', '#':
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// compactText 去掉所有空白（用于正则匹配）。
func compactText(text string) string {
	var sb strings.Builder
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　' {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// RenderForPrompt 把块渲染成给 AI 的文本。
//
// 格式对齐桌面版：
//
//	[R000001]
//	type: paragraph
//	heading_path: 第一章 > 1.1 背景
//	text:
//	（正文内容）
//
// 为什么把 type 和 heading_path 显式标出来：
//
//	模型能据此判断"这段是列表还是论述"、"属于哪一章"，
//	提炼出的知识点标题质量明显更好。
func RenderForPrompt(blocks []Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Filtered {
			continue
		}

		sb.WriteString("[")
		sb.WriteString(b.ID)
		sb.WriteString("]\n")

		sb.WriteString("type: ")
		sb.WriteString(string(b.Type))
		sb.WriteString("\n")

		sb.WriteString("heading_path: ")
		if len(b.HeadingPath) > 0 {
			sb.WriteString(strings.Join(b.HeadingPath, " > "))
		} else {
			sb.WriteString("无")
		}
		sb.WriteString("\n")

		sb.WriteString("text:\n")
		sb.WriteString(b.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}
