package knowledge

import (
	"testing"
)

// TestExtractKeywords 验证关键词提取。
//
// 这是知识库匹配效果的关键：中文没有空格，
// 整串检索必然失败，必须拆成可命中的语义单元。
func TestExtractKeywords(t *testing.T) {
	cases := []struct {
		query    string
		mustHave []string // 必须出现的关键词
		mustNot  []string // 不该出现的关键词
	}{
		{
			query: "2.2 应用架构与微服务化设计",
			// 2-gram 切片后 "微服" 能命中"微服务"相关内容
			// （"服务" 是停用词，被过滤了 —— 它太泛，几乎每章都有）
			mustHave: []string{"微服"},
			// 停用词要过滤掉
			mustNot: []string{"应用", "架构", "设计", "2.2", "服务"},
		},
		{
			query:    "1.4 总体技术方案与设计原则",
			mustHave: []string{"总体", "原则"},
			mustNot:  []string{"技术", "方案", "设计"},
		},
		{
			query:    "数据架构与数据资源规划",
			mustHave: []string{"数据", "资源", "规划"},
		},
		{
			query:    "B/S 架构与私有云部署",
			mustHave: []string{"私有", "部署"},
		},
	}

	for _, c := range cases {
		got := extractKeywords(c.query)
		gotSet := make(map[string]bool, len(got))
		for _, k := range got {
			gotSet[k] = true
		}

		t.Logf("查询 %q → %v", c.query, got)

		for _, want := range c.mustHave {
			if !gotSet[want] {
				t.Errorf("%q: 期望包含关键词 %q，实际 %v", c.query, want, got)
			}
		}
		for _, notWant := range c.mustNot {
			if gotSet[notWant] {
				t.Errorf("%q: 不该包含关键词 %q", c.query, notWant)
			}
		}
	}
}

// TestScoreMatch 验证匹配打分。
func TestScoreMatch(t *testing.T) {
	// 标题命中权重最高
	titleScore := scoreMatch("微服务", "微服务架构设计", "", "")
	resumeScore := scoreMatch("微服务", "系统设计", "采用微服务架构", "")
	contentScore := scoreMatch("微服务", "系统设计", "概述", "后端使用微服务框架")

	if titleScore <= resumeScore {
		t.Errorf("标题命中(%v)应高于摘要命中(%v)", titleScore, resumeScore)
	}
	if resumeScore <= contentScore {
		t.Errorf("摘要命中(%v)应高于正文命中(%v)", resumeScore, contentScore)
	}

	// 完全没命中得 0
	if s := scoreMatch("量子计算", "微服务架构", "采用微服务", "微服务框架"); s != 0 {
		t.Errorf("不相关应该得 0，实际 %v", s)
	}
}

// TestExtractKeywordsTooShort 验证短查询。
func TestExtractKeywordsTooShort(t *testing.T) {
	// 太短的查询提取不出关键词 → 不做匹配（避免乱塞）
	for _, q := range []string{"", "的", "和"} {
		got := extractKeywords(q)
		if len(got) != 0 {
			t.Errorf("查询 %q 应无关键词，实际 %v", q, got)
		}
	}
}

// TestContainsCJKRune 验证中文检测。
func TestContainsCJKRune(t *testing.T) {
	if !containsCJKRune([]rune("微服务")) {
		t.Error("中文应被识别")
	}
	if containsCJKRune([]rune("PostgreSQL")) {
		t.Error("拉丁字母不该被识别为中文")
	}
	if containsCJKRune([]rune("2024")) {
		t.Error("数字不该被识别为中文")
	}
}
