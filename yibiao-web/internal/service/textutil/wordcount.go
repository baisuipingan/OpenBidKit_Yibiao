// Package textutil 提供文本处理工具。
//
// 字数统计口径移植自桌面版 client/electron/utils/wordCount.cjs，
// 一字未改——标书场景对字数有硬性要求（招标文件常规定「不少于 X 万字」），
// 口径不一致会导致导出的标书被判废标。
package textutil

import (
	"regexp"
	"strings"
)

// 正则预编译。Go 的 regexp 不支持 backreference，但这里的模式都不需要。
var (
	// 围栏代码块 ```...```
	reFencedCode = regexp.MustCompile("(?s)```.*?```")

	// Markdown 图片 ![alt](url)
	reMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

	// Markdown 引用式链接定义 [id]: url
	reLinkDef = regexp.MustCompile(`(?m)^\s*\[[^\]]+\]:\s*\S+.*$`)

	// Markdown 行内链接 [text](url)
	reInlineLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

	// Markdown 引用式链接 [text][id]
	reRefLink = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)

	// URL
	reURL = regexp.MustCompile(`(?i)\b(?:https?|file|yibiao-asset)://\S+`)

	// HTML 注释
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)

	// <br>
	reBR = regexp.MustCompile(`(?i)<br\s*/?>`)

	// 块级 HTML 标签
	reBlockTag = regexp.MustCompile(`(?i)</?(?:p|div|section|article|tr|td|th|li|ul|ol|table|thead|tbody|blockquote|h[1-6])\b[^>]*>`)

	// 任意 HTML 标签
	reAnyTag = regexp.MustCompile(`<[^>]+>`)

	// 表格分隔行 |---|---|
	reTableSep = regexp.MustCompile(`^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)

	// 行首 Markdown 标题
	reHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)

	// 行首引用
	reQuote = regexp.MustCompile(`^\s{0,3}>\s?`)

	// 行首无序列表
	reBullet = regexp.MustCompile(`^\s*[-+*]\s+(\[[ xX]\]\s*)?`)

	// 行首有序列表
	reOrdered = regexp.MustCompile(`^\s*\d+[.)．、]\s+`)

	// 行首表格竖线
	reLeadingPipe = regexp.MustCompile(`^\s*\|`)

	// 行尾表格竖线
	reTrailingPipe = regexp.MustCompile(`\|\s*$`)

	// 行内竖线
	rePipe = regexp.MustCompile(`\|`)

	// &nbsp;
	reNbsp = regexp.MustCompile(`(?i)&nbsp;`)

	// 连续空白
	reSpaces = regexp.MustCompile(`\s+`)

	// CJK 字符（含扩展 A 区）
	reCJK = regexp.MustCompile(`[\x{3400}-\x{9fff}]`)

	// 非中文的词（英文单词、数字、含连字符/下划线/点的复合词）
	reWord = regexp.MustCompile(`[A-Za-z0-9]+(?:[-_./][A-Za-z0-9]+)*`)
)

// NormalizeReadableText 把 Markdown 归一化成「可读正文」。
//
// 去掉：代码块、图片、链接 URL、HTML 标签、表格竖线、列表符号。
// 保留：正文文字、链接文字。
func NormalizeReadableText(content string) string {
	s := content

	s = reFencedCode.ReplaceAllString(s, "\n")
	s = reMarkdownImage.ReplaceAllString(s, " ")
	s = reLinkDef.ReplaceAllString(s, " ")
	s = reInlineLink.ReplaceAllString(s, "$1")
	s = reRefLink.ReplaceAllString(s, "$1")
	s = reURL.ReplaceAllString(s, " ")
	s = reHTMLComment.ReplaceAllString(s, " ")
	s = reBR.ReplaceAllString(s, "\n")
	s = reBlockTag.ReplaceAllString(s, " ")
	s = reAnyTag.ReplaceAllString(s, " ")

	// 逐行清理
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		// 表格分隔行整行丢弃
		if reTableSep.MatchString(trimmed) {
			out = append(out, "")
			continue
		}

		l := line
		l = reHeading.ReplaceAllString(l, " ")
		l = reQuote.ReplaceAllString(l, " ")
		l = reBullet.ReplaceAllString(l, " ")
		l = reOrdered.ReplaceAllString(l, " ")
		l = reLeadingPipe.ReplaceAllString(l, " ")
		l = reTrailingPipe.ReplaceAllString(l, " ")
		l = rePipe.ReplaceAllString(l, " ")
		l = reNbsp.ReplaceAllString(l, " ")
		l = reSpaces.ReplaceAllString(l, " ")
		out = append(out, strings.TrimSpace(l))
	}

	result := strings.Join(out, "\n")
	result = reSpaces.ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}

// CountReadableWords 统计可读字数。
//
// 口径（与桌面版一致）：
//   - CJK 字符：每个字符算 1 字
//   - 非中文：每个词算 1 字（英文单词、数字、复合词）
//
// 这样的口径符合中文标书的「字数」习惯。
func CountReadableWords(content string) int {
	text := NormalizeReadableText(content)
	if text == "" {
		return 0
	}

	cjkCount := len(reCJK.FindAllString(text, -1))

	// 去掉 CJK 后统计词数，避免中文被当成一个词
	withoutCJK := reCJK.ReplaceAllString(text, " ")
	wordCount := len(reWord.FindAllString(withoutCJK, -1))

	return cjkCount + wordCount
}

// CountChars 统计字符数（含标点、不含空白）。
//
// 用于「严格字符数」场景，与 CountReadableWords 区分。
func CountChars(content string) int {
	text := NormalizeReadableText(content)
	n := 0
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		n++
	}
	return n
}

// CountCJK 只统计中文字符数。
func CountCJK(content string) int {
	text := NormalizeReadableText(content)
	return len(reCJK.FindAllString(text, -1))
}
