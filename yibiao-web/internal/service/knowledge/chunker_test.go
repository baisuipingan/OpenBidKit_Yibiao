package knowledge

import (
	"strings"
	"testing"
)

// TestSplitBlocks 验证切块。
func TestSplitBlocks(t *testing.T) {
	md := `# 第一章 项目背景

这是第一段正文，讲述项目的基本情况。

## 1.1 建设必要性

第二段内容。

- 列表项一
- 列表项二

| 列1 | 列2 |
|-----|-----|
| a   | b   |
`

	blocks := SplitBlocks(md)

	if len(blocks) < 4 {
		t.Fatalf("块数太少: %d", len(blocks))
	}

	// 检查块 ID 格式
	for i, b := range blocks {
		want := blockID(i + 1)
		if b.ID != want {
			t.Errorf("第 %d 块 ID = %s, 期望 %s", i, b.ID, want)
		}
	}

	// 检查类型
	var hasHeading, hasList, hasTable bool
	for _, b := range blocks {
		switch b.Type {
		case BlockHeading:
			hasHeading = true
		case BlockList:
			hasList = true
		case BlockTable:
			hasTable = true
		}
	}
	if !hasHeading {
		t.Error("没有识别出标题块")
	}
	if !hasList {
		t.Error("没有识别出列表块")
	}
	if !hasTable {
		t.Error("没有识别出表格块")
	}

	// 检查标题路径
	for _, b := range blocks {
		if b.Type == BlockParagraph && len(b.HeadingPath) == 0 {
			// 第一个标题前的段落没有路径是正常的，
			// 但这里所有段落都在标题之后
			t.Logf("块 %s 没有标题路径（内容: %.20s）", b.ID, b.Content)
		}
	}
}

// TestFilterPageNumber 验证页码过滤。
func TestFilterPageNumber(t *testing.T) {
	cases := []struct {
		text   string
		reason string // 空字符串表示不该被过滤
	}{
		{"- 1 -", "页码"},
		{"第 3 页", "页码"},
		{"3/10", "页码"},
		{"Page 5 of 20", "页码"},
		{"这是正文内容，不是页码。", ""},
		// 纯日期：作为独立块没有知识价值，会被「内容过短」过滤，
		// 但绝不能被误判为页码或签字页
		{"2024年3月15日", "内容过短"},
	}

	for _, c := range cases {
		blocks := []RawBlock{{ID: "R000001", Content: c.text}}
		out := Filter(blocks)
		if out[0].FilterReason != c.reason {
			t.Errorf("%q: 过滤原因=%q, 期望 %q", c.text, out[0].FilterReason, c.reason)
		}
		if c.reason == "" && out[0].Filtered {
			t.Errorf("%q: 不该被过滤，原因: %s", c.text, out[0].FilterReason)
		}
	}
}

// TestFilterCatalog 验证目录过滤。
func TestFilterCatalog(t *testing.T) {
	// 典型目录：标题 + 带页码的行
	catalog := `目录

第一章 项目概况 .......... 1
第二章 技术方案 .......... 15
第三章 实施方案 .......... 48`

	blocks := []RawBlock{{ID: "R000001", Content: catalog}}
	out := Filter(blocks)
	if !out[0].Filtered {
		t.Errorf("目录应该被过滤，原因: %s", out[0].FilterReason)
	}
	if out[0].FilterReason != "目录" {
		t.Errorf("过滤原因应为「目录」，实际: %s", out[0].FilterReason)
	}

	// 正常正文不该被误判
	normal := `第一章 项目概况

本项目位于某市，总投资约 860 万元。建设内容包括机房改造、
系统集成和三年运维服务。`

	blocks = []RawBlock{{ID: "R000001", Content: normal}}
	out = Filter(blocks)
	if out[0].Filtered {
		t.Errorf("正文被误判为目录，原因: %s", out[0].FilterReason)
	}
}

// TestFilterCover 验证封面过滤。
func TestFilterCover(t *testing.T) {
	cover := `投标文件

项目名称：某市人民医院信息化建设项目
招标编号：ZB-2024-001
投标人：某某科技有限公司
编制日期：2024年3月`

	blocks := []RawBlock{{ID: "R000001", Content: cover}}
	out := Filter(blocks)
	if !out[0].Filtered {
		t.Errorf("封面应该被过滤，原因: %s", out[0].FilterReason)
	}
}

// TestFilterSignature 验证签字页过滤。
func TestFilterSignature(t *testing.T) {
	// 纯盖章页 → 过滤
	signature := `法定代表人（签字）：
授权代表（签字）：
（盖章）
日期：2024年3月15日`

	blocks := []RawBlock{{ID: "R000001", Content: signature}}
	out := Filter(blocks)
	if !out[0].Filtered {
		t.Errorf("签字页应该被过滤，原因: %s", out[0].FilterReason)
	}

	// 正文里提到盖章 → 不过滤
	normal := `合同签订时，双方应当加盖公章，并由法定代表人签字确认。这一流程
是为了保障法律效力，任何一方不得省略。签章完成后合同正式生效。`

	blocks = []RawBlock{{ID: "R000001", Content: normal}}
	out = Filter(blocks)
	if out[0].Filtered {
		t.Errorf("正文被误判为签字页，原因: %s", out[0].FilterReason)
	}
}

// TestFilterRepeated 验证重复页眉页脚过滤。
func TestFilterRepeated(t *testing.T) {
	header := "某市人民医院信息化系统建设项目投标文件"
	blocks := []RawBlock{
		{ID: "R000001", Content: header},
		{ID: "R000002", Content: "第一章 正文内容开始，这里讲述项目背景和建设目标。"},
		{ID: "R000003", Content: header},
		{ID: "R000004", Content: header},
	}

	out := Filter(blocks)
	// 出现 3 次，应该全被过滤
	count := 0
	for _, b := range out {
		if b.Filtered && b.FilterReason == "重复的页眉页脚" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("应过滤 3 个重复块，实际 %d", count)
	}
}

// TestMergeSemantic 验证语义合并。
func TestMergeSemantic(t *testing.T) {
	// 构造很多短段落，应该合并成少数几个块
	raw := make([]RawBlock, 0, 20)
	for i := 0; i < 20; i++ {
		raw = append(raw, RawBlock{
			ID:      blockID(i + 1),
			Type:    BlockParagraph,
			Content: strings.Repeat("这是一段测试文本。", 5), // 约 45 字
		})
	}

	merged := MergeSemantic(raw)

	if len(merged) >= len(raw) {
		t.Errorf("合并后块数应该减少：原始 %d，合并后 %d", len(raw), len(merged))
	}

	// 检查每个合并块的字数不超过目标太多
	for _, b := range merged {
		chars := contentChars(b.Content)
		if chars > semanticMergeTargetChars*2 {
			t.Errorf("合并块过大: %d 字（目标 %d）", chars, semanticMergeTargetChars)
		}
	}
}

// TestMergeTableStandalone 验证表格独立成块。
func TestMergeTableStandalone(t *testing.T) {
	raw := []RawBlock{
		{ID: "R000001", Type: BlockParagraph, Content: "一些文字"},
		{ID: "R000002", Type: BlockTable, Content: "| a | b |\n|---|---|\n| 1 | 2 |"},
		{ID: "R000003", Type: BlockParagraph, Content: "更多文字"},
	}

	merged := MergeSemantic(raw)

	// 表格应该保持独立，不被合进段落块
	var tableFound bool
	for _, b := range merged {
		if b.Type == BlockTable {
			tableFound = true
			if !strings.Contains(b.Content, "| a | b |") {
				t.Error("表格内容被污染")
			}
		}
	}
	if !tableFound {
		t.Error("表格块丢失了")
	}
}

// TestSplitOversized 验证超长文本拆分。
func TestSplitOversized(t *testing.T) {
	// 构造超长文本（多句）
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("这是一个较长的句子，用于测试拆分逻辑。")
	}
	text := sb.String()

	parts := splitOversized(text, 500)

	if len(parts) < 2 {
		t.Fatalf("应该拆成多段，实际 %d 段", len(parts))
	}

	// 每段不应超限太多
	for i, p := range parts {
		chars := len([]rune(p))
		if chars > 700 {
			t.Errorf("第 %d 段过长: %d 字", i, chars)
		}
	}

	// 拼接后内容应该基本完整（不丢字）
	total := 0
	for _, p := range parts {
		total += len([]rune(p))
	}
	orig := len([]rune(strings.ReplaceAll(text, " ", "")))
	if total < orig*9/10 {
		t.Errorf("拆分后丢了太多内容: 原 %d, 拆后 %d", orig, total)
	}
}

// TestRenderForPrompt 验证 Prompt 渲染。
func TestRenderForPrompt(t *testing.T) {
	blocks := []Block{
		{
			RawBlock: RawBlock{
				ID:          "R000001",
				Type:        BlockParagraph,
				HeadingPath: []string{"第一章", "1.1 背景"},
				Content:     "这是正文内容。",
			},
		},
		{
			RawBlock:     RawBlock{ID: "R000002", Content: "被过滤的内容"},
			Filtered:     true,
			FilterReason: "页码",
		},
	}

	out := RenderForPrompt(blocks)

	if !strings.Contains(out, "[R000001]") {
		t.Error("缺少块 ID")
	}
	if !strings.Contains(out, "heading_path: 第一章 > 1.1 背景") {
		t.Error("缺少标题路径")
	}
	if strings.Contains(out, "被过滤的内容") {
		t.Error("过滤的块不该出现在 Prompt 里")
	}
}
