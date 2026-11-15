// Package openxml 的受限 HTML 生成部分。
//
// C# 助手的 insert-body-html 只接受「标书工作台受限 HTML」，不是任意 HTML。
// 规范见 client/electron/resources/content-generation/受限HTML生成规范.md。
//
// 本文件负责把 Markdown 正文转成受限 HTML。
//
// 为什么需要这一步：
//
//	Markdown 无法精确控制图片布局（画框比例、图文混排），
//	而标书对配图版式有要求（方图、宽图、竖图、全景）。
//	受限 HTML 用 data-yb-size 等属性表达这些约束，C# 转成 Open XML 时精确还原。
package openxml

import (
	"fmt"
	"strings"
)

// FigureSize 是图片画框类型。
type FigureSize string

const (
	SizeSquare   FigureSize = "square"   // 方图 65% 宽
	SizeWide     FigureSize = "wide"     // 宽图 80% 宽，3:2
	SizeTall     FigureSize = "tall"     // 竖图 50% 宽，3:4
	SizePanorama FigureSize = "panorama" // 全景 90% 宽，16:9
)

// Figure 是一张配图。
type Figure struct {
	// ID 唯一标识（如 fig_001）
	ID string
	// Size 画框类型
	Size FigureSize
	// Prompt 配图提示（不写入 Word，只给生成图片用）
	Prompt string
	// AssetRef 图片在工作区内的相对路径
	AssetRef string
	// Alt 替代文本（无障碍，也便于排查）
	Alt string
	// Caption 图注
	Caption string
	// Generation 生成方式：aiImage | mermaid | htmlImage
	Generation string
}

// Blocks 是一节正文的结构化内容。
//
// 用结构体而不是直接拼字符串，是为了：
//   - 各段类型有明确的字段，不会拼错
//   - 后续加表格、列表等类型时改动可控
type Blocks struct {
	// Paragraphs 普通段落（已转义 HTML）
	Paragraphs []string
	// Headings 标题，key 是级别（1-6），value 是标题文本
	Headings []Heading
	// Figures 配图
	Figures []Figure
	// Tables 表格（受限 HTML 只支持简单表格）
	Tables []Table
}

// Heading 是一个标题。
type Heading struct {
	Level int
	Text  string
}

// Table 是一个表格。
type Table struct {
	Caption string
	Headers []string
	Rows    [][]string
}

// RenderSection 把一节正文渲染成受限 HTML。
//
// 输出符合 C# 助手的 insert-body-html 要求：
//   - 每块用 <!-- yibiao:block --> 包裹
//   - figure 必须带 data-yb-size
//   - img 必须带 data-yb-asset-ref
func RenderSection(sectionID string, blocks Blocks) string {
	var sb strings.Builder

	// 标题：ID 用递增序号，保证唯一
	for i, h := range blocks.Headings {
		lvl := h.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 6 {
			lvl = 6
		}
		sb.WriteString("<!-- yibiao:block -->\n")
		sb.WriteString(fmt.Sprintf("<h%d id=\"h_%s_%03d\">%s</h%d>\n",
			lvl, sanitizeID(sectionID), i+1, escapeHTML(h.Text), lvl))
	}

	// 段落
	for i, p := range blocks.Paragraphs {
		sb.WriteString("<!-- yibiao:block -->\n")
		sb.WriteString(fmt.Sprintf("<p id=\"p_%s_%03d\">%s</p>\n",
			sanitizeID(sectionID), i+1, p))
	}

	// 表格
	for i, t := range blocks.Tables {
		sb.WriteString("<!-- yibiao:block -->\n")
		sb.WriteString(renderTable(sectionID, i, t))
	}

	// 配图
	for _, f := range blocks.Figures {
		sb.WriteString("<!-- yibiao:block -->\n")
		sb.WriteString(renderFigure(f))
	}

	return sb.String()
}

// renderTable 渲染表格。
func renderTable(sectionID string, index int, t Table) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("<table id=\"t_%s_%03d\">", sanitizeID(sectionID), index+1))
	if t.Caption != "" {
		sb.WriteString(fmt.Sprintf("<caption>%s</caption>", escapeHTML(t.Caption)))
	}

	if len(t.Headers) > 0 {
		sb.WriteString("<thead><tr>")
		for _, h := range t.Headers {
			sb.WriteString(fmt.Sprintf("<th>%s</th>", renderInline(h)))
		}
		sb.WriteString("</tr></thead>")
	}

	sb.WriteString("<tbody>")
	for _, row := range t.Rows {
		sb.WriteString("<tr>")
		for _, cell := range row {
			sb.WriteString(fmt.Sprintf("<td>%s</td>", renderInline(cell)))
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</tbody></table>\n")

	return sb.String()
}

// renderFigure 渲染配图。
func renderFigure(f Figure) string {
	size := f.Size
	if size == "" {
		size = SizeWide
	}
	generation := f.Generation
	if generation == "" {
		generation = "aiImage"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"<figure id=\"%s\" data-yb-generation=\"%s\" data-yb-size=\"%s\">",
		escapeAttr(f.ID), escapeAttr(generation), escapeAttr(string(size)),
	))

	// 配图提示：只给生成图片用，不写入 Word
	if f.Prompt != "" {
		sb.WriteString(fmt.Sprintf("<template data-yb-role=\"prompt\">%s</template>",
			escapeHTML(f.Prompt)))
	}

	// 图片引用：还没生成的图留空，生成后回填
	assetRef := f.AssetRef
	if assetRef == "" {
		// 没有图片时不写 img 标签，避免 C# 报错
		// （C# 要求 data-yb-asset-ref 必须存在且文件真实存在）
		sb.WriteString(fmt.Sprintf("<img alt=\"%s\" data-yb-asset-ref=\"\" />",
			escapeAttr(f.Alt)))
	} else {
		sb.WriteString(fmt.Sprintf("<img alt=\"%s\" data-yb-asset-ref=\"%s\" />",
			escapeAttr(f.Alt), escapeAttr(assetRef)))
	}

	if f.Caption != "" {
		sb.WriteString(fmt.Sprintf("<figcaption>%s</figcaption>", escapeHTML(f.Caption)))
	}

	sb.WriteString("</figure>\n")
	return sb.String()
}

// --- Markdown → Blocks 的转换 ---

// ParseMarkdown 把 Markdown 正文解析成 Blocks。
//
// 支持的语法（覆盖标书正文的常见用法）：
//   - 标题 # ## ###
//   - 段落
//   - 无序列表 - 或 *
//   - 有序列表 1. 2.
//   - 表格 | a | b |
//   - 代码块（原样保留为段落）
//
// 不支持：图片语法（配图由 Figure 结构单独管理）
func ParseMarkdown(md string) Blocks {
	var blocks Blocks
	lines := strings.Split(md, "\n")

	var (
		para    strings.Builder
		table   *Table
		inCode  bool
		codeBuf strings.Builder
	)

	flushPara := func() {
		text := strings.TrimSpace(para.String())
		para.Reset()
		if text == "" {
			return
		}
		blocks.Paragraphs = append(blocks.Paragraphs, renderInline(text))
	}

	flushTable := func() {
		if table != nil && (len(table.Headers) > 0 || len(table.Rows) > 0) {
			blocks.Tables = append(blocks.Tables, *table)
		}
		table = nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// 代码块
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				// 结束代码块：内容作为段落
				flushPara()
				content := strings.TrimSpace(codeBuf.String())
				if content != "" {
					// 代码块用等宽字体段落表示
					for _, cl := range strings.Split(content, "\n") {
						if strings.TrimSpace(cl) != "" {
							blocks.Paragraphs = append(blocks.Paragraphs,
								escapeHTML(strings.TrimSpace(cl)))
						}
					}
				}
				codeBuf.Reset()
				inCode = false
			} else {
				flushPara()
				flushTable()
				inCode = true
			}
			continue
		}
		if inCode {
			codeBuf.WriteString(line)
			codeBuf.WriteString("\n")
			continue
		}

		// 空行：结束当前段落/表格
		if trimmed == "" {
			flushPara()
			flushTable()
			continue
		}

		// 已有的 <figure> 块（Agent 或手工写入的配图引用）
		//
		// 正文里可能已经带受限 HTML 的 figure 标签，
		// 这些要原样保留并解析成 Figure 结构，否则导出时会丢图。
		if strings.HasPrefix(trimmed, "<figure") {
			flushPara()
			flushTable()

			// 收集到 </figure>
			var figLines []string
			figLines = append(figLines, line)
			for !strings.Contains(line, "</figure>") && i+1 < len(lines) {
				i++
				line = lines[i]
				figLines = append(figLines, line)
			}

			if fig := parseFigureBlock(strings.Join(figLines, "\n")); fig != nil {
				blocks.Figures = append(blocks.Figures, *fig)
			}
			continue
		}

		// 标题
		if lvl := headingLevel(trimmed); lvl > 0 {
			flushPara()
			flushTable()
			text := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			blocks.Headings = append(blocks.Headings, Heading{Level: lvl, Text: text})
			continue
		}

		// 表格行
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			flushPara()
			cells := parseTableRow(trimmed)

			// 分隔行 |---|---| 跳过
			if isTableSeparator(cells) {
				continue
			}

			if table == nil {
				table = &Table{}
			}
			if len(table.Headers) == 0 {
				table.Headers = cells
			} else {
				table.Rows = append(table.Rows, cells)
			}
			continue
		}
		flushTable()

		// 列表项：转成带项目符号的段落
		if isListItem(trimmed) {
			flushPara()
			text := stripListMarker(trimmed)
			blocks.Paragraphs = append(blocks.Paragraphs, "• "+renderInline(text))
			continue
		}

		// 普通段落：累积（Markdown 的软换行合并）
		if para.Len() > 0 {
			para.WriteString(" ")
		}
		para.WriteString(trimmed)
	}

	flushPara()
	flushTable()

	return blocks
}

// parseFigureBlock 解析一个 <figure> 块。
//
// 支持的形式：
//
//	<figure id="fig_1" data-yb-size="wide" data-yb-generation="mermaid">
//	  <template data-yb-role="prompt">提示词</template>
//	  <img alt="说明" data-yb-asset-ref="figures/x.png" />
//	  <figcaption>图注</figcaption>
//	</figure>
func parseFigureBlock(block string) *Figure {
	fig := &Figure{
		Size:       SizeWide,
		Generation: "aiImage",
	}

	// size
	if m := extractAttr(block, "data-yb-size"); m != "" {
		switch FigureSize(m) {
		case SizeSquare, SizeWide, SizeTall, SizePanorama:
			fig.Size = FigureSize(m)
		}
	}

	// generation
	if m := extractAttr(block, "data-yb-generation"); m != "" {
		fig.Generation = m
	}

	// id
	if m := extractAttr(block, "id"); m != "" {
		fig.ID = m
	}

	// asset-ref（关键：没有它图片插不进去）
	fig.AssetRef = extractAttr(block, "data-yb-asset-ref")

	// alt
	fig.Alt = extractAttr(block, "alt")

	// 提示词
	if i := strings.Index(block, `data-yb-role="prompt"`); i >= 0 {
		if j := strings.Index(block[i:], ">"); j >= 0 {
			start := i + j + 1
			if k := strings.Index(block[start:], "</template>"); k >= 0 {
				fig.Prompt = strings.TrimSpace(block[start : start+k])
			}
		}
	}

	// 图注
	if i := strings.Index(block, "<figcaption>"); i >= 0 {
		start := i + len("<figcaption>")
		if k := strings.Index(block[start:], "</figcaption>"); k >= 0 {
			fig.Caption = strings.TrimSpace(block[start : start+k])
		}
	}

	// 没有 asset-ref 的 figure 也保留（导出时 C# 会写「待填写」）
	return fig
}

// extractAttr 从 HTML 片段里提取属性值。
func extractAttr(block, name string) string {
	// 尝试 name="value"
	for _, quote := range []string{`"`, `'`} {
		needle := name + "=" + quote
		if i := strings.Index(block, needle); i >= 0 {
			start := i + len(needle)
			if k := strings.Index(block[start:], quote); k >= 0 {
				return block[start : start+k]
			}
		}
	}
	return ""
}

// headingLevel 判断标题级别。
func headingLevel(line string) int {
	if !strings.HasPrefix(line, "#") {
		return 0
	}
	n := 0
	for _, c := range line {
		if c == '#' {
			n++
		} else {
			break
		}
	}
	if n > 6 {
		n = 6
	}
	// 必须有空格分隔，否则是 #hashtag 不是标题
	if len(line) > n && line[n] != ' ' {
		return 0
	}
	return n
}

// parseTableRow 解析表格行。
func parseTableRow(line string) []string {
	trimmed := strings.Trim(line, "|")
	parts := strings.Split(trimmed, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// isTableSeparator 判断是否表格分隔行。
func isTableSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		// 只含 - 和 : 就算分隔
		hasDash := false
		for _, r := range c {
			if r == '-' {
				hasDash = true
			} else if r != ':' && r != ' ' {
				return false
			}
		}
		if !hasDash {
			return false
		}
	}
	return true
}

// isListItem 判断是否列表项。
func isListItem(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	// 有序列表 1. 2.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i < len(line)-1 && line[i] == '.' && line[i+1] == ' ' {
		return true
	}
	return false
}

// stripListMarker 去掉列表标记。
func stripListMarker(line string) string {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	// 有序列表
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i < len(line)-1 && line[i] == '.' {
		return strings.TrimSpace(line[i+1:])
	}
	return line
}

// --- 工具函数 ---

// renderInline 处理 Markdown 行内格式。
//
// **重要**：C# 的受限 HTML 只支持这些标签（查 RestrictedHtmlDocumentRenderer.cs）：
//
//	p, h1-h6, table, tr, th, td, caption, ol, ul, li, i, figure, img,
//	figcaption, template
//
// **不支持 <b> / <strong>**。所以 Markdown 的 **粗体** 只能去掉标记，
// 保留文字（让内容正确优先于强调效果）。
//
// 斜体 <i> 是支持的，保留。
func renderInline(text string) string {
	// 先转义，再替换标记，避免注入
	escaped := escapeHTML(text)

	// **粗体** → 去掉标记（受限 HTML 不支持 b/strong）
	escaped = stripPairs(escaped, "**")
	escaped = stripPairs(escaped, "__")
	// *斜体* → <i>（<i> 是支持的）
	escaped = replacePairs(escaped, "*", "<i>", "</i>")
	// `代码` → 去掉反引号（code 不在支持列表）
	escaped = stripPairs(escaped, "`")

	return escaped
}

// stripPairs 去掉成对出现的标记，只保留文本。
func stripPairs(s, marker string) string {
	return strings.ReplaceAll(s, marker, "")
}

// replacePairs 把成对出现的标记替换成标签。
//
// 只处理成对出现的情况：奇数个标记时最后一个不替换，避免破标签。
func replacePairs(s, marker, openTag, closeTag string) string {
	var b strings.Builder
	rest := s
	openNext := true

	for {
		idx := strings.Index(rest, marker)
		if idx < 0 {
			b.WriteString(rest)
			break
		}

		b.WriteString(rest[:idx])
		if openNext {
			b.WriteString(openTag)
		} else {
			b.WriteString(closeTag)
		}
		openNext = !openNext
		rest = rest[idx+len(marker):]
	}

	return b.String()
}

// escapeHTML 转义 HTML 特殊字符。
//
// 为什么必须转义：Agent 生成的正文可能含 < > &，
// 不转义会被 C# 当成标签解析，导致结构错乱。
func escapeHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeAttr 转义属性值。
func escapeAttr(s string) string {
	return escapeHTML(s)
}

// sanitizeID 清理 ID，只保留字母数字下划线。
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "s"
	}
	return out
}
