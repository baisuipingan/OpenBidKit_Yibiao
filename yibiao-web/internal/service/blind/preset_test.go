package blind

import (
	"testing"
)

// TestBuiltinPresets 验证内置预设。
func TestBuiltinPresets(t *testing.T) {
	presets := BuiltinPresets()

	if len(presets) != 4 {
		t.Errorf("应有 4 个内置预设，实际 %d", len(presets))
	}

	// 每个预设都要有 id / name / profile
	for _, p := range presets {
		if p.ID == "" || p.Name == "" {
			t.Errorf("预设缺少必要字段: %+v", p)
		}
		if len(p.Profile) == 0 {
			t.Errorf("预设 %s 的 profile 为空，会导致所有规则不检查", p.ID)
		}

		// body 段必须有字体和字号（这是暗标检查的核心）
		body, ok := p.Profile["body"].(map[string]any)
		if !ok {
			t.Errorf("预设 %s 缺少 body 段", p.ID)
			continue
		}
		if body["font_cn"] == nil {
			t.Errorf("预设 %s 的 body 缺少 font_cn", p.ID)
		}
		if body["size_pt"] == nil {
			t.Errorf("预设 %s 的 body 缺少 size_pt", p.ID)
		}
	}
}

// TestPresetDifferences 验证各预设的差异（防止复制粘贴错误）。
func TestPresetDifferences(t *testing.T) {
	general, _ := FindPreset("builtin-general")
	neimenggu, _ := FindPreset("builtin-neimenggu")
	hebei, _ := FindPreset("builtin-hebei")
	sichuan, _ := FindPreset("builtin-sichuan")

	getBodyPt := func(p Preset) any {
		body, _ := p.Profile["body"].(map[string]any)
		return body["line_pt"]
	}

	// 行距：通用 25 / 内蒙 28 / 河北 30 / 四川 28
	if getBodyPt(general) != 25 {
		t.Errorf("通用预设行距应为 25，实际 %v", getBodyPt(general))
	}
	if getBodyPt(neimenggu) != 28 {
		t.Errorf("内蒙预设行距应为 28，实际 %v", getBodyPt(neimenggu))
	}
	if getBodyPt(hebei) != 30 {
		t.Errorf("河北预设行距应为 30，实际 %v", getBodyPt(hebei))
	}
	if getBodyPt(sichuan) != 28 {
		t.Errorf("四川预设行距应为 28，实际 %v", getBodyPt(sichuan))
	}

	// 页边距：内蒙 2.0，河北上 2.5 下左右 2.0
	nmgPage, _ := neimenggu.Profile["page"].(map[string]any)
	if nmgPage["margin_top_cm"] != 2 {
		t.Errorf("内蒙页边距应为 2.0，实际 %v", nmgPage["margin_top_cm"])
	}

	hbPage, _ := hebei.Profile["page"].(map[string]any)
	if hbPage["margin_top_cm"] != 2.5 || hbPage["margin_left_cm"] != 2 {
		t.Errorf("河北页边距应为上2.5/左2.0，实际 top=%v left=%v",
			hbPage["margin_top_cm"], hbPage["margin_left_cm"])
	}
}

// TestResolveProfile 验证 profile 解析和合并。
func TestResolveProfile(t *testing.T) {
	// 未知预设 → 用通用兜底
	profile, name, err := ResolveProfile("nonexistent", nil)
	if err != nil {
		t.Fatalf("未知预设不该报错: %v", err)
	}
	if name != "通用" {
		t.Errorf("未知预设应回退到通用，实际 %s", name)
	}
	if len(profile) == 0 {
		t.Error("回退后 profile 不该为空")
	}

	// 自定义覆盖单个字段
	custom := map[string]any{
		"body": map[string]any{
			"line_pt": 32,
		},
	}
	merged, _, err := ResolveProfile("builtin-general", custom)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}

	body, _ := merged["body"].(map[string]any)
	if body["line_pt"] != 32 {
		t.Errorf("自定义行距应覆盖为 32，实际 %v", body["line_pt"])
	}
	// 其他字段应保留。
	// 注意：deepCopyMap 走了一次 JSON 往返，数字都变成 float64，
	// 所以要用 toFloat 比较而不是直接 != 14（int）。
	if body["font_cn"] != "宋体" {
		t.Errorf("未覆盖的 font_cn 应保留宋体，实际 %v", body["font_cn"])
	}
	if toFloat(body["size_pt"]) != 14 {
		t.Errorf("未覆盖的 size_pt 应保留 14，实际 %v (%T)", body["size_pt"], body["size_pt"])
	}

	// **关键**：合并不能污染原始预设
	again, _, _ := ResolveProfile("builtin-general", nil)
	againBody, _ := again["body"].(map[string]any)
	if toFloat(againBody["line_pt"]) != 25 {
		t.Errorf("合并污染了原始预设！行距应为 25，实际 %v", againBody["line_pt"])
	}
}

// TestBuildSensitiveWords 验证敏感词组装。
func TestBuildSensitiveWords(t *testing.T) {
	// 手填（多种分隔符）
	words := BuildSensitiveWords("某科技公司,某某集团、张三\n李四", nil, false)
	if len(words) != 4 {
		t.Errorf("应切出 4 个词，实际 %d: %+v", len(words), words)
	}

	// 去重
	words2 := BuildSensitiveWords("某公司\n某公司\n某集团", nil, false)
	if len(words2) != 2 {
		t.Errorf("应去重为 2 个，实际 %d", len(words2))
	}

	// 太短的词要过滤（1 个字噪音太大）
	words3 := BuildSensitiveWords("甲\n某公司", nil, false)
	if len(words3) != 1 {
		t.Errorf("单字应被过滤，实际 %d 个: %+v", len(words3), words3)
	}

	// 资信库开关
	cred := []string{"北京某科技有限公司", "王五"}
	withCred := BuildSensitiveWords("", cred, true)
	if len(withCred) != 2 {
		t.Errorf("启用资信库应有 2 个词，实际 %d", len(withCred))
	}
	withoutCred := BuildSensitiveWords("", cred, false)
	if len(withoutCred) != 0 {
		t.Errorf("禁用资信库应 0 个词，实际 %d", len(withoutCred))
	}

	// 手填 + 资信库合并去重
	both := BuildSensitiveWords("北京某科技有限公司", cred, true)
	if len(both) != 2 {
		t.Errorf("手填与资信库重复应去重为 2 个，实际 %d", len(both))
	}
}

// TestInputSignature 验证输入签名。
func TestInputSignature(t *testing.T) {
	a := InputSignature("投标文件.docx", 1024, "builtin-general", "某公司", true)
	b := InputSignature("投标文件.docx", 1024, "builtin-general", "某公司", true)
	if a != b {
		t.Error("相同输入应产生相同签名")
	}

	// 任何一项变化，签名都要变
	if a == InputSignature("投标文件2.docx", 1024, "builtin-general", "某公司", true) {
		t.Error("文件名变化应改变签名")
	}
	if a == InputSignature("投标文件.docx", 2048, "builtin-general", "某公司", true) {
		t.Error("文件大小变化应改变签名")
	}
	if a == InputSignature("投标文件.docx", 1024, "builtin-hebei", "某公司", true) {
		t.Error("预设变化应改变签名")
	}
	if a == InputSignature("投标文件.docx", 1024, "builtin-general", "某公司2", true) {
		t.Error("敏感词变化应改变签名")
	}
	if a == InputSignature("投标文件.docx", 1024, "builtin-general", "某公司", false) {
		t.Error("资信库开关变化应改变签名")
	}
}

// toFloat 把 JSON 往返后的数字统一成 float64 比较。
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return -1
}

// TestResolveProfileComplete 验证返回的 profile 结构完整。
//
// **这是防白屏的关键测试**：前端的预设编辑界面直接读
// profile.headings['1'].size_pt 等深层字段，
// 结构不完整会抛 TypeError 导致整页白屏。
func TestResolveProfileComplete(t *testing.T) {
	for _, p := range BuiltinPresets() {
		profile, _, err := ResolveProfile(p.ID, nil)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", p.ID, err)
		}

		// 顶层 9 个段必须都在
		for _, section := range []string{
			"page", "body", "headings", "table", "structure",
			"text", "privacy", "props", "consistency", "image",
		} {
			if _, ok := profile[section].(map[string]any); !ok {
				t.Errorf("%s 缺少 %s 段", p.ID, section)
			}
		}

		// headings 必须有 1~4 级
		headings, _ := profile["headings"].(map[string]any)
		for _, level := range []string{"1", "2", "3", "4"} {
			h, ok := headings[level].(map[string]any)
			if !ok {
				t.Errorf("%s 的 headings 缺少 %s 级", p.ID, level)
				continue
			}
			// 前端直接读 size_pt，必须有值
			if h["size_pt"] == nil {
				t.Errorf("%s 的 headings[%s] 缺少 size_pt", p.ID, level)
			}
		}

		// body 的关键字段
		body, _ := profile["body"].(map[string]any)
		for _, key := range []string{
			"font_cn", "size_pt", "line_rule", "line_pt",
			"first_line_indent_chars", "allow_bold", "tolerance_pt",
		} {
			if _, ok := body[key]; !ok {
				t.Errorf("%s 的 body 缺少 %s", p.ID, key)
			}
		}

		// page 的页边距
		page, _ := profile["page"].(map[string]any)
		for _, key := range []string{
			"paper", "margin_top_cm", "margin_bottom_cm",
			"margin_left_cm", "margin_right_cm",
		} {
			if _, ok := page[key]; !ok {
				t.Errorf("%s 的 page 缺少 %s", p.ID, key)
			}
		}
	}
}
