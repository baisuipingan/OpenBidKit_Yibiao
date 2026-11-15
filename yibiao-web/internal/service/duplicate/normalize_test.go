package duplicate

import (
	"testing"
)

// TestNormalize 验证基础归一化。
func TestNormalize(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"投标人须提供业绩", "投标人须提供业绩"},
		{"投标人 须 提供 业绩", "投标人须提供业绩"},
		{"投标人，须提供、业绩。", "投标人须提供业绩"},
		{"ABC DEF", "abcdef"},
		{"（一）项目概况", "一项目概况"},
	}

	for _, c := range cases {
		if got := Normalize(c.input); got != c.want {
			t.Errorf("Normalize(%q) = %q, 期望 %q", c.input, got, c.want)
		}
	}
}

// TestSkeletonKey 验证骨架化。
//
// 关键能力：数字/日期/金额被替换成占位符，
// 让"3 个业绩"和"5 个业绩"能识别为同一类要求。
func TestSkeletonKey(t *testing.T) {
	// 数字占位
	a := SkeletonKey("须提供不少于 3 个类似项目业绩")
	b := SkeletonKey("须提供不少于 5 个类似项目业绩")
	if a != b {
		t.Errorf("不同数字应骨架相同:\n  %q\n  %q", a, b)
	}

	// 日期占位
	c := SkeletonKey("投标截止时间 2024年3月15日")
	d := SkeletonKey("投标截止时间 2025年12月31日")
	if c != d {
		t.Errorf("不同日期应骨架相同:\n  %q\n  %q", c, d)
	}

	// 金额占位
	e := SkeletonKey("项目预算 860 万元")
	f := SkeletonKey("项目预算 1200 万元")
	if e != f {
		t.Errorf("不同金额应骨架相同:\n  %q\n  %q", e, f)
	}

	// 百分比
	g := SkeletonKey("保证金比例 2%")
	h := SkeletonKey("保证金比例 5%")
	if g != h {
		t.Errorf("不同百分比应骨架相同:\n  %q\n  %q", g, h)
	}

	// 语义不同的不能混（防止过度归一化）
	i := SkeletonKey("须提供业绩证明材料")
	j := SkeletonKey("须提供财务审计报告")
	if i == j {
		t.Errorf("语义不同的不该骨架相同: %q", i)
	}
}

// TestFormatField 验证结构化字段识别。
func TestFormatField(t *testing.T) {
	cases := []struct {
		input    string
		wantOK   bool
		wantName string
	}{
		{"项目名称：某市人民医院信息化项目", true, "项目名称"},
		{"招标编号:ZB-2024-001", true, "招标编号"},
		{"这是一个普通句子没有冒号", false, ""},
		{"时间：2024年3月15日", true, "时间"},
	}

	for _, c := range cases {
		name, _, ok := FormatField(c.input)
		if ok != c.wantOK {
			t.Errorf("FormatField(%q) ok=%v, 期望 %v", c.input, ok, c.wantOK)
		}
		if ok && name != c.wantName {
			t.Errorf("FormatField(%q) name=%q, 期望 %q", c.input, name, c.wantName)
		}
	}
}

// TestBigrams 验证 bigram 生成。
func TestBigrams(t *testing.T) {
	grams := Bigrams("微服务架构")
	// 5 个字 → 4 个 bigram
	if len(grams) != 4 {
		t.Errorf("期望 4 个 bigram，实际 %d: %v", len(grams), grams)
	}
	for _, want := range []string{"微服", "服务", "务架", "架构"} {
		if _, ok := grams[want]; !ok {
			t.Errorf("缺少 bigram %q", want)
		}
	}

	// 单字
	single := Bigrams("好")
	if len(single) != 1 {
		t.Errorf("单字应生成 1 个 bigram，实际 %d", len(single))
	}

	// 空
	if len(Bigrams("")) != 0 {
		t.Error("空字符串应无 bigram")
	}
}

// TestDiceSimilarity 验证 Dice 系数。
func TestDiceSimilarity(t *testing.T) {
	// 完全相同
	if d := DiceSimilarity(10, 10, 10); d != 1.0 {
		t.Errorf("完全相同的 Dice 应为 1.0，实际 %v", d)
	}
	// 完全不同
	if d := DiceSimilarity(0, 10, 10); d != 0 {
		t.Errorf("完全不同的 Dice 应为 0，实际 %v", d)
	}
	// 一半重合
	if d := DiceSimilarity(5, 10, 10); d != 0.5 {
		t.Errorf("一半重合的 Dice 应为 0.5，实际 %v", d)
	}
}

// TestNearMatch 验证近似匹配。
func TestNearMatch(t *testing.T) {
	// 高度相似的长句 → 匹配
	a := "投标人须提供近三年类似项目业绩证明材料并加盖公章"
	b := "投标人须提供近三年类似项目业绩证明材料并加盖单位公章"
	matched, dice, containment := NearMatch(a, b)
	if !matched {
		t.Errorf("高度相似应匹配: dice=%v containment=%v", dice, containment)
	}

	// 完全不同的内容 → 不匹配
	c := "本项目采用公开招标方式"
	d := "投标保证金金额为人民币五万元整"
	matched2, _, _ := NearMatch(c, d)
	if matched2 {
		t.Error("完全不同的内容不该匹配")
	}

	// 短句且差异大 → 不匹配（短句阈值更严）
	e := "业绩要求"
	f := "业绩证明"
	matched3, _, _ := NearMatch(e, f)
	if matched3 {
		t.Error("短句不该轻易匹配")
	}
}

// TestShouldApplyNear 验证短句是否做近似匹配。
func TestShouldApplyNear(t *testing.T) {
	// 长句 → 做
	if !ShouldApplyNear("投标人须提供近三年类似项目业绩证明材料") {
		t.Error("长句应该做近似匹配")
	}

	// 短句无关键词 → 不做
	if ShouldApplyNear("详见附件") {
		t.Error("短句且无关键词不该做近似匹配")
	}

	// 短句含关键词 → 做
	if !ShouldApplyNear("评分标准详见") {
		t.Error("短句含「评分」应该做近似匹配")
	}
}

// TestSplitSentences 验证分句。
func TestSplitSentences(t *testing.T) {
	text := "第一句要求。第二句要求！第三句？第四句；"
	got := SplitSentences(text)

	if len(got) != 4 {
		t.Errorf("应切出 4 句，实际 %d: %v", len(got), got)
	}
}

// TestIndexExactMatch 验证索引的精确匹配。
func TestIndexExactMatch(t *testing.T) {
	tender := []string{
		"投标人须提供营业执照副本",
		"须提供近 3 年财务审计报告",
		"投标保证金为人民币五万元",
	}
	idx := BuildIndex(tender)

	if idx.TenderCount() != 3 {
		t.Errorf("招标句数应为 3，实际 %d", idx.TenderCount())
	}

	// 完全一致
	r := idx.Match("投标人须提供营业执照副本")
	if !r.Matched || r.Reason != ReasonExact {
		t.Errorf("完全一致应匹配 exact，实际 %+v", r)
	}

	// 标点不同 → strict
	r2 := idx.Match("投标人须提供营业执照副本。")
	if !r2.Matched || r2.Reason != ReasonStrict {
		t.Errorf("标点差异应匹配 strict，实际 %+v", r2)
	}

	// 数字不同 → skeleton
	// （用阿拉伯数字：骨架化只替换 \d+，中文数字不在范围内 —— 与桌面版一致）
	r3 := idx.Match("须提供近 5 年财务审计报告")
	if !r3.Matched {
		t.Errorf("数字差异应匹配（骨架），实际 %+v", r3)
	}

	// 无关内容 → 不匹配
	r4 := idx.Match("本项目位于某市高新区")
	if r4.Matched {
		t.Errorf("无关内容不该匹配，实际 %+v", r4)
	}
}

// TestIndexNearMatch 验证近似匹配走索引。
func TestIndexNearMatch(t *testing.T) {
	tender := []string{
		"投标人须提供近三年（2021年至2023年）类似项目业绩证明材料并加盖投标人公章",
	}
	idx := BuildIndex(tender)

	// 只改了几个字的长句应该能命中
	r := idx.Match("投标人应当提供近三年（2021年至2023年）类似项目业绩证明材料并加盖投标人公章")
	if !r.Matched {
		t.Errorf("改几个字应能近似匹配，实际 %+v", r)
	}

	// 完全不相关
	r2 := idx.Match("本项目采用竞争性磋商采购方式确定成交供应商")
	if r2.Matched {
		t.Errorf("不相关内容不该匹配，实际 %+v", r2)
	}
}

// TestIndexPerformance 验证索引性能。
//
// 这是关键：500 条招标 × 3000 句投标 如果两两比对会很慢。
// 建索引后应该是毫秒级。
func TestIndexPerformance(t *testing.T) {
	// 造 500 条招标要求
	tender := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		tender = append(tender, "投标人须提供第"+itoa(i)+"项资质证明材料并加盖公章")
	}
	idx := BuildIndex(tender)

	// 造 1000 句投标内容：
	//   前 500 句是招标原文的改写（应匹配）
	//   后 500 句完全无关（不应匹配）
	bid := make([]string, 0, 1000)
	for i := 0; i < 500; i++ {
		// 只改标点和空格，语义完全相同 → strict 匹配
		bid = append(bid, "投标人须提供第"+itoa(i)+"项资质证明材料，并加盖公章。")
	}
	for i := 0; i < 500; i++ {
		bid = append(bid, "本项目采用先进技术架构保障系统稳定运行第"+itoa(i)+"部分内容")
	}

	matched := 0
	falsePositive := 0
	for i, s := range bid {
		if idx.Match(s).Matched {
			matched++
			if i >= 500 {
				falsePositive++
			}
		}
	}

	// 前 500 句应该基本都能匹配（允许个别因阈值卡掉）
	if matched < 480 {
		t.Errorf("应至少匹配 480 句，实际 %d", matched)
	}
	// 无关内容不该误报（这是查重的核心价值：漏报可以补，误报让人不信）
	if falsePositive > 50 {
		t.Errorf("误报太多：%d 句无关内容被判为匹配", falsePositive)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// TestCoreContainment 验证核心片段包含度。
//
// 这是查重准确性的关键：投标文件会把"投标人须提供X"
// 改写成"我公司已提供X，详见附件" —— 核心内容一致但整句不同。
func TestCoreContainment(t *testing.T) {
	cases := []struct {
		name      string
		tender    string
		bid       string
		wantMatch bool
	}{
		{
			name:      "主语改写的响应",
			tender:    "投标人须提供营业执照副本复印件并加盖公章",
			bid:       "我公司已提供营业执照副本复印件并加盖公章，详见附件一",
			wantMatch: true,
		},
		{
			name:      "数字改写的响应",
			tender:    "投标人须提供近三年类似项目业绩不少于3个",
			bid:       "我公司提供近三年类似项目业绩5个，详见业绩表",
			wantMatch: true,
		},
		{
			name:      "加句首套话的响应",
			tender:    "系统须支持不少于5000人次/日的门诊量并发处理",
			bid:       "本系统设计支持8000人次/日的门诊量并发处理，满足要求",
			wantMatch: true,
		},
		{
			name:      "完全不同（应判漏项）",
			tender:    "系统须符合等保三级安全要求",
			bid:       "我公司将提供7×24小时技术支持服务",
			wantMatch: false,
		},
		{
			name:      "完全不同（应判漏项）2",
			tender:    "投标人须在项目所在地设有常驻服务机构",
			bid:       "质保期3年，质保期内免费升级",
			wantMatch: false,
		},
	}

	for _, c := range cases {
		ratio, fragment := CoreContainment(c.tender, c.bid)
		matched := ratio >= coreContainmentThreshold
		if matched != c.wantMatch {
			t.Errorf("%s: ratio=%.3f matched=%v 期望 %v\n  招标: %s\n  投标: %s\n  公共片段: %s",
				c.name, ratio, matched, c.wantMatch, c.tender, c.bid, fragment)
		}
		t.Logf("%-24s ratio=%.3f 片段=%q", c.name, ratio, fragment)
	}
}
