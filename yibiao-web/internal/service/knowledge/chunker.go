// Package knowledge 实现知识库：文档切块、过滤、AI 提炼、检索。
//
// 移植自桌面版 electron/services/knowledgeBaseService.cjs，
// 核心算法（切块、过滤、合并）保持完全一致 —— 这决定了知识库质量，
// 不能随意改动阈值。
package knowledge

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 切块参数。与桌面版常量保持一致。
const (
	// semanticMergeTargetChars 语义合并的目标字数。
	// 相邻小段合并到这个长度才切成一个块 —— 太短检索没上下文，
	// 太长 AI 提炼时容易丢细节。
	semanticMergeTargetChars = 520

	// oversizedBlockChars 超过这个长度的单个块要拆分。
	// 模型上下文有限，超长块没法一次处理。
	oversizedBlockChars = 2600
)

// BlockType 块类型。
type BlockType string

const (
	BlockHeading   BlockType = "heading"
	BlockParagraph BlockType = "paragraph"
	BlockList      BlockType = "list"
	BlockTable     BlockType = "table"
)

// RawBlock 是切块后的原始块（未过滤）。
type RawBlock struct {
	ID          string
	Type        BlockType
	HeadingPath []string
	Content     string
}

// Block 是过滤后的块。
type Block struct {
	RawBlock
	// Filtered 是否被过滤掉（噪音内容）
	Filtered bool
	// FilterReason 过滤原因（给用户看，可追溯）
	FilterReason string
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	reTable   = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	reList    = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)、]\s+)`)
	// rePageNumber 页码："- 1 -" / "第1页" / "1/10" / "page 1 of 10"
	rePageNumber   = regexp.MustCompile(`^(?:[-—_]*\d+[-—_]*|第\d+页(?:共\d+页)?|\d+/\d+|(?i)page\d+(?:of\d+)?)$`)
	reCatalogTitle = regexp.MustCompile(`^(?:#+)?(?:目录|目次|(?i)contents)$`)
	// reCatalogLine 目录行："1.1 背景 ...... 5"
	reCatalogLine = regexp.MustCompile(`(?:\.{2,}|…{2,}|·{2,}|\s{4,})\s*\d+\s*$`)
	// reLongSentence 有句末标点（判断是否是长句而非封面片段）
	reLongSentence = regexp.MustCompile(`[。！？；]`)
)

// SplitBlocks 把 markdown 切成原始块。
//
// 规则（对齐桌面版）：
//   - 标题行独立成块，同时维护 heading 路径栈
//   - 表格行聚成一个块（表格内容有结构，不能拆）
//   - 列表项聚成一个块
//   - 段落按空行分隔
//   - 超长块（> oversizedBlockChars）继续拆分
func SplitBlocks(markdown string) []RawBlock {
	blocks := make([]RawBlock, 0, 64)
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")

	var buffer []string
	currentType := BlockParagraph
	headings := make([]string, 0, 6)

	pushBuffer := func() {
		content := strings.TrimSpace(strings.Join(buffer, "\n"))
		buffer = nil
		if content == "" {
			return
		}

		// 标题路径要过滤空项
		path := make([]string, 0, len(headings))
		for _, h := range headings {
			if h != "" {
				path = append(path, h)
			}
		}

		chunks := []string{content}
		if utf8.RuneCountInString(content) > oversizedBlockChars {
			chunks = splitOversized(content, oversizedBlockChars*3/4)
		}

		for _, chunk := range chunks {
			blocks = append(blocks, RawBlock{
				ID:          blockID(len(blocks) + 1),
				Type:        currentType,
				HeadingPath: path,
				Content:     chunk,
			})
		}
	}

	for _, line := range lines {
		if m := reHeading.FindStringSubmatch(line); m != nil {
			pushBuffer()

			level := len(m[1])
			// 维护标题栈：新标题要顶掉同级及更深的
			for len(headings) < level {
				headings = append(headings, "")
			}
			headings = headings[:level]
			headings[level-1] = strings.TrimSpace(m[2])

			currentType = BlockHeading
			buffer = []string{line}
			pushBuffer()
			currentType = BlockParagraph
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			pushBuffer()
			currentType = BlockParagraph
			continue
		}

		nextType := BlockParagraph
		switch {
		case reTable.MatchString(line):
			nextType = BlockTable
		case reList.MatchString(line):
			nextType = BlockList
		}

		// 类型切换时先落盘（段落之间不切，保持连续正文）
		if len(buffer) > 0 && currentType != nextType &&
			(currentType != BlockParagraph || nextType != BlockParagraph) {
			pushBuffer()
		}

		currentType = nextType
		buffer = append(buffer, line)
	}

	pushBuffer()
	return blocks
}

// blockID 生成块 ID（R000001 格式，保留桌面版约定）。
func blockID(n int) string {
	return "R" + padZero(n, 6)
}

func padZero(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// splitOversized 把超长文本按句子/段落边界切成若干段。
//
// 不能硬切 —— 要在标点处断开，否则知识条目会语义断裂。
func splitOversized(text string, limit int) []string {
	if limit <= 0 {
		limit = 1000
	}

	var out []string
	remaining := text

	for utf8.RuneCountInString(remaining) > limit {
		runes := []rune(remaining)

		// 从 limit 位置向前找断点
		cut := limit
		for i := limit; i > limit/2; i-- {
			if isBreakRune(runes[i]) {
				cut = i + 1
				break
			}
		}

		out = append(out, strings.TrimSpace(string(runes[:cut])))
		remaining = strings.TrimSpace(string(runes[cut:]))
	}

	if strings.TrimSpace(remaining) != "" {
		out = append(out, strings.TrimSpace(remaining))
	}

	return out
}

// isBreakRune 判断是否可以作为断点。
func isBreakRune(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '\n', '.', '!', '?', ';':
		return true
	}
	return false
}

// MergeSemantic 把相邻小段合并成语义完整的块。
//
// 为什么需要：切块后会有大量 20-50 字的碎片（一句话一段），
// 单独提炼成知识条目没有价值。合并到 500 字左右，
// 一条知识点才有完整的论述。
//
// 规则：
//   - 表格独立（结构性强，合并会破坏语义）
//   - 标题可以带上下文，但如果缓冲区已有 100 字以上正文，先落盘
//   - 段落累积到 semanticMergeTargetChars 就切
func MergeSemantic(raw []RawBlock) []RawBlock {
	merged := make([]RawBlock, 0, len(raw))
	var buffer []RawBlock

	bufferText := func() string {
		parts := make([]string, 0, len(buffer))
		for _, b := range buffer {
			parts = append(parts, b.Content)
		}
		return strings.Join(parts, "\n\n")
	}

	bufferOnlyHeadings := func() bool {
		if len(buffer) == 0 {
			return false
		}
		for _, b := range buffer {
			if b.Type != BlockHeading {
				return false
			}
		}
		return true
	}

	flush := func() {
		if len(buffer) == 0 {
			return
		}

		typ := BlockParagraph
		for _, b := range buffer {
			if b.Type == BlockList {
				typ = BlockList
				break
			}
		}

		merged = append(merged, RawBlock{
			// 继承首块的标题路径
			ID:          blockID(len(merged) + 1),
			Type:        typ,
			HeadingPath: buffer[0].HeadingPath,
			Content:     strings.TrimSpace(bufferText()),
		})
		buffer = nil
	}

	pushStandalone := func(b RawBlock) {
		merged = append(merged, RawBlock{
			ID:          blockID(len(merged) + 1),
			Type:        b.Type,
			HeadingPath: b.HeadingPath,
			Content:     b.Content,
		})
	}

	for _, b := range raw {
		// 表格独立成块
		if b.Type == BlockTable {
			flush()
			pushStandalone(b)
			continue
		}

		if b.Type == BlockHeading {
			// 缓冲区里有实质内容就先落盘，避免标题和远处的正文混一起
			if len(buffer) > 0 && !bufferOnlyHeadings() &&
				contentChars(bufferText()) >= 100 {
				flush()
			}
			buffer = append(buffer, b)
			continue
		}

		chars := contentChars(b.Content)

		// 单块已经够大，独立成块
		if len(buffer) == 0 && chars >= semanticMergeTargetChars {
			pushStandalone(b)
			continue
		}

		buffer = append(buffer, b)
		if contentChars(bufferText()) >= semanticMergeTargetChars {
			flush()
		}
	}

	flush()
	return merged
}

// contentChars 统计有效字数（去掉所有空白）。
func contentChars(text string) int {
	n := 0
	for _, r := range text {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			n++
		}
	}
	return n
}
