package blind

import (
	"encoding/json"
	"fmt"
)

// Preset 是暗标格式预设。
//
// 预设定义了「什么样的格式才算合规」：
// 字体、字号、行距、页边距、是否允许加粗/彩色等。
//
// 为什么需要预设而不是一套固定标准：
// 各地招标文件对暗标格式的要求不同（内蒙 28 磅、河北 30 磅、
// 四川一级标题不加粗），用一套标准会把合规文件判成违规。
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Profile 是给 C# helper 的格式规范（JSON）
	Profile map[string]any `json:"profile"`
}

// defaultProfile 是完整的默认格式规范。
//
// **为什么后端也要有一份完整默认值**：
// 前端的预设编辑界面按 profile 的完整结构渲染
// （presetFields.ts 里直接读 profile.headings['1'].size_pt），
// 如果后端返回稀疏结构（只有 body 段），前端渲染时会崩溃白屏：
//
//	TypeError: Cannot read properties of undefined (reading '1')
//	  at baseTabSummary (presetFields.ts:442)
//
// 所以预设必须是「完整结构 + 覆盖项」，与桌面版
// blindCheckPresets.cjs 的 DEFAULT_PROFILE 保持一致。
var defaultProfile = map[string]any{
	"page": map[string]any{
		"paper":             "A4",
		"margin_top_cm":     2.5,
		"margin_bottom_cm":  2.5,
		"margin_left_cm":    2.5,
		"margin_right_cm":   2.5,
		"check_orientation": true,
		"forbid_columns":    true,
		"check_sections":    false,
	},
	"body": map[string]any{
		"font_cn":                 "宋体",
		"font_en":                 nil,
		"size_pt":                 14,
		"line_rule":               "exact",
		"line_pt":                 25,
		"line_multiple":           nil,
		"space_before_pt":         0,
		"space_after_pt":          0,
		"first_line_indent_chars": 2,
		"alignment":               nil,
		"allow_bold":              false,
		"allow_italic":            false,
		"allow_underline":         false,
		"allow_color":             false,
		"allow_highlight":         false,
		"allow_char_spacing":      true,
		"allow_font_scaling":      true,
		"check_latin_size":        true,
		"tolerance_pt":            0.5,
		"tolerance_line_pt":       0.5,
		"tolerance_indent_chars":  0.1,
		"tolerance_margin_cm":     0.02,
		"check_snap_to_grid":      false,
		"check_vert_align":        false,
	},
	"headings": map[string]any{
		"1": map[string]any{"font_cn": nil, "size_pt": 16, "bold": true},
		"2": map[string]any{"font_cn": nil, "size_pt": 14, "bold": false},
		"3": map[string]any{"font_cn": nil, "size_pt": 14, "bold": false},
		"4": map[string]any{"font_cn": nil, "size_pt": 14, "bold": false},
	},
	"table": map[string]any{
		"check_font":              true,
		"font_cn":                 "宋体",
		"check_size":              true,
		"size_pt":                 10.5,
		"check_first_line_indent": true,
		"first_line_indent_chars": 0,
		"allow_bold":              nil,
		"check_tblStyle":          true,
		"check_shading":           true,
		"check_nested":            true,
		"alignment":               nil,
		"check_merge":             false,
	},
	"structure": map[string]any{
		"check_empty_paragraphs": true,
		"check_revisions":        true,
		"check_comments":         true,
		"check_hidden_text":      true,
		"check_header_footer":    true,
		"forbid_header_content":  true,
		"forbid_footer_content":  true,
		"page_number":            "forbid",
		"heading_numbering":      nil,
		"check_toc_present":      "hint",
		"check_textbox":          true,
		"check_hyperlink":        true,
		"check_field_codes":      true,
		"check_bookmarks":        false,
		"skip_cover":             true,
	},
	"text": map[string]any{
		"check_english_punct": true,
		"check_spaces":        true,
		"space_rules": map[string]any{
			"strip_leading_trailing": true,
			"remove_between_cjk":     true,
			"forbid_fullwidth_space": false,
		},
		"check_tabs":               true,
		"check_manual_linebreak":   true,
		"check_irregular":          true,
		"check_halfwidth_brackets": false,
		"check_fullwidth_digits":   false,
		"check_fullwidth_letters":  false,
	},
	"privacy": map[string]any{
		"enable_builtin":       true,
		"keywords":             []any{},
		"regex":                []any{},
		"whitelist":            []any{},
		"ignore_orgs":          []any{},
		"check_company_suffix": true,
		"check_phone":          true,
		"check_id_card":        true,
		"check_email":          true,
		"check_url":            true,
		"check_credit_code":    true,
		"check_bank_account":   true,
		"check_placeholder":    true,
		"check_toc":            true,
	},
	"props": map[string]any{
		"check_author":           true,
		"check_last_modified_by": true,
		"check_company":          true,
		"check_template":         true,
		"check_custom_props":     true,
	},
	"consistency": map[string]any{
		"check_heading_format":     true,
		"check_heading_numbering":  true,
		"check_heading_level_jump": true,
		"check_heading_content":    true,
		"heading_max_chars":        60,
		"check_body_format":        true,
		"check_mixed_fonts":        true,
	},
	"image": map[string]any{
		"check_color":    false,
		"check_vector":   true,
		"check_metadata": true,
	},
	"rules": map[string]any{},
}

// builtinPresets 是内置预设（对齐桌面版 blindCheckPresets.cjs）。
//
// **重要**：这里的 profile 只写「与默认值的差异」，
// 实际返回给前端时会与 defaultProfile 深度合并成完整结构。
// 详见 ResolveProfile。
var builtinPresets = []Preset{
	{
		ID:          "builtin-general",
		Name:        "通用",
		Description: "A4，正文宋体四号、固定行距 25 磅、首行缩进 2 字符，表格宋体五号，页边距四边 2.5 cm。",
		Profile: map[string]any{
			"page": map[string]any{
				"paper":            "A4",
				"margin_top_cm":    2.5,
				"margin_bottom_cm": 2.5,
				"margin_left_cm":   2.5,
				"margin_right_cm":  2.5,
			},
			"body": map[string]any{
				"font_cn":                 "宋体",
				"size_pt":                 14,
				"line_rule":               "exact",
				"line_pt":                 25,
				"first_line_indent_chars": 2,
			},
			"table": map[string]any{
				"font_cn": "宋体",
				"size_pt": 10.5,
			},
		},
	},
	{
		ID:          "builtin-neimenggu",
		Name:        "内蒙古（参考）",
		Description: "正文宋体四号、固定行距 28 磅，表格宋体五号，一级标题宋体三号加粗，页边距四边 2.0 cm。",
		Profile: map[string]any{
			"page": map[string]any{
				"paper":            "A4",
				"margin_top_cm":    2,
				"margin_bottom_cm": 2,
				"margin_left_cm":   2,
				"margin_right_cm":  2,
			},
			"body": map[string]any{
				"font_cn":                 "宋体",
				"size_pt":                 14,
				"line_rule":               "exact",
				"line_pt":                 28,
				"first_line_indent_chars": 2,
			},
			"table": map[string]any{
				"font_cn": "宋体",
				"size_pt": 10.5,
			},
			"headings": map[string]any{
				"1": map[string]any{"size_pt": 16, "bold": true},
			},
			"text": map[string]any{
				"check_english_punct": false,
				"check_spaces":        false,
			},
		},
	},
	{
		ID:          "builtin-hebei",
		Name:        "河北（参考）",
		Description: "正文宋体四号、固定行距 30 磅，表格宋体四号，标题均为四号，页边距上 2.5 下 2.0 左 2.0 右 2.0 cm。",
		Profile: map[string]any{
			"page": map[string]any{
				"paper":            "A4",
				"margin_top_cm":    2.5,
				"margin_bottom_cm": 2,
				"margin_left_cm":   2,
				"margin_right_cm":  2,
			},
			"body": map[string]any{
				"font_cn":                 "宋体",
				"size_pt":                 14,
				"line_rule":               "exact",
				"line_pt":                 30,
				"first_line_indent_chars": 2,
			},
			"table": map[string]any{
				"font_cn": "宋体",
				"size_pt": 14,
			},
			"headings": map[string]any{
				"1": map[string]any{"size_pt": 14, "bold": true},
			},
		},
	},
	{
		ID:          "builtin-sichuan",
		Name:        "四川（参考）",
		Description: "正文宋体四号、固定行距 28 磅，表格宋体四号，一级标题三号不加粗，页边距四边 2.5 cm。",
		Profile: map[string]any{
			"page": map[string]any{
				"paper":            "A4",
				"margin_top_cm":    2.5,
				"margin_bottom_cm": 2.5,
				"margin_left_cm":   2.5,
				"margin_right_cm":  2.5,
			},
			"body": map[string]any{
				"font_cn":                 "宋体",
				"size_pt":                 14,
				"line_rule":               "exact",
				"line_pt":                 28,
				"first_line_indent_chars": 2,
			},
			"table": map[string]any{
				"font_cn": "宋体",
				"size_pt": 14,
			},
			"headings": map[string]any{
				"1": map[string]any{"size_pt": 16, "bold": false},
			},
		},
	},
}

// BuiltinPresets 返回内置预设列表（副本，防止调用方修改）。
func BuiltinPresets() []Preset {
	out := make([]Preset, len(builtinPresets))
	copy(out, builtinPresets)
	return out
}

// FindPreset 按 ID 找内置预设。
func FindPreset(id string) (Preset, bool) {
	for _, p := range builtinPresets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ResolveProfile 计算最终的 profile。
//
// custom 不为空时，与预设做**深度合并**（custom 覆盖预设的同名键）。
// 为什么不是替换：用户通常只改一两个值（比如只调行距），
// 其余仍用预设，替换会导致大部分规则失去约束。
func ResolveProfile(presetID string, custom map[string]any) (map[string]any, string, error) {
	preset, ok := FindPreset(presetID)
	if !ok {
		// 未知预设 → 用通用兜底（不要让用户因为改了个 ID 就跑不起来）
		preset, _ = FindPreset("builtin-general")
	}

	// 三层合并：默认值 → 预设覆盖 → 用户自定义
	//
	// **必须从 defaultProfile 起**：预设只写了差异项，
	// 直接返回会导致前端渲染崩溃（profile.headings['1'] 不存在）。
	base := deepCopyMap(defaultProfile)
	merged := deepMerge(base, preset.Profile)

	if len(custom) > 0 {
		merged = deepMerge(merged, custom)
	}

	return merged, preset.Name, nil
}

// deepCopyMap 深拷贝（防止合并时污染预设）。
func deepCopyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// deepMerge 深度合并两个 map（override 优先）。
func deepMerge(base, override map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for k, v := range override {
		ov, isMap := v.(map[string]any)
		if !isMap {
			base[k] = v
			continue
		}
		if bv, ok := base[k].(map[string]any); ok {
			base[k] = deepMerge(bv, ov)
			continue
		}
		base[k] = ov
	}
	return base
}

// SensitiveWord 是敏感词。
type SensitiveWord struct {
	Text  string `json:"text"`
	Label string `json:"label,omitempty"`
}

// BuildSensitiveWords 组装敏感词列表。
//
// 来源三处：
//  1. 用户手填（每行一个）
//  2. 资信库里的公司名/人名（如果启用）
//  3. 常见泄露词兜底（"我方公司"这类）
func BuildSensitiveWords(manualWords string, credentialWords []string, useCredential bool) []SensitiveWord {
	out := make([]SensitiveWord, 0, 32)
	seen := make(map[string]bool)

	add := func(text, label string) {
		text = trimSpace(text)
		if text == "" || len([]rune(text)) < 2 || seen[text] {
			return
		}
		seen[text] = true
		out = append(out, SensitiveWord{Text: text, Label: label})
	}

	// 1. 用户手填（按行 / 逗号 / 顿号切分）
	for _, w := range splitWords(manualWords) {
		add(w, "自定义")
	}

	// 2. 资信库
	if useCredential {
		for _, w := range credentialWords {
			add(w, "资信库")
		}
	}

	return out
}

// splitWords 按常见分隔符切分词语。
func splitWords(text string) []string {
	var out []string
	var buf []rune

	flush := func() {
		if len(buf) > 0 {
			out = append(out, string(buf))
			buf = buf[:0]
		}
	}

	for _, r := range text {
		switch r {
		case '\n', '\r', ',', '，', '、', ';', '；':
			flush()
		default:
			buf = append(buf, r)
		}
	}
	flush()
	return out
}

// trimSpace 去首尾空白（含全角空格）。
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpaceByte(s[start]) {
		start++
	}
	for end > start && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

var _ = fmt.Sprintf

// fixableRules 是支持自动修复的规则（对齐桌面版 FIX_RULE_IDS）。
//
// 为什么这份清单要写死：C# 侧只实现了这些规则的修复逻辑，
// 前端不该让用户勾选修不了的规则 —— 勾了也不会生效，是误导。
var fixableRules = []string{
	// 身份与痕迹
	"struct.doc_props", "struct.comment", "struct.revision", "text.hidden",
	"struct.hyperlink", "struct.field", "struct.header_footer",
	"image.meta", "struct.bookmark",
	// 页面
	"page.paper_size", "page.margins", "page.columns", "page.background",
	// 段落
	"para.line_spacing", "para.first_line_indent", "para.space_before_after",
	"para.alignment", "para.page_break_before", "para.snap_to_grid",
	// 字体
	"font.chinese", "font.western", "font.size", "font.bold", "font.italic",
	"font.underline", "font.color", "font.highlight", "font.effects",
	"font.char_spacing", "font.scaling",
	// 表格
	"table.font", "table.size", "table.indent", "table.shading",
	// 图片
	"image.color",
	// 文本
	"text.english_punct", "text.spaces", "text.irregular", "text.linebreak",
	// 结构
	"struct.empty_paragraph", "struct.heading_number", "struct.toc",
}

// notDefaultFixRules 默认不勾选的规则。
//
// 原因：
//   - 段前分页、对齐到网格：需人工确认是否真有必要
//   - 英文标点、多余空格、分栏：不是招标文件的格式条款，只是排版建议
var notDefaultFixRules = map[string]bool{
	"para.page_break_before": true,
	"para.snap_to_grid":      true,
	"text.english_punct":     true,
	"text.spaces":            true,
	"page.columns":           true,
}

// FixableRules 返回支持修复的规则 ID 列表。
func FixableRules() []string {
	out := make([]string, len(fixableRules))
	copy(out, fixableRules)
	return out
}

// DefaultFixRules 返回默认勾选的修复规则。
func DefaultFixRules() []string {
	out := make([]string, 0, len(fixableRules))
	for _, id := range fixableRules {
		if notDefaultFixRules[id] {
			continue
		}
		out = append(out, id)
	}
	return out
}

// NormalizeFixRules 过滤出可修复的规则，保持固定顺序。
//
// 前端传来的规则可能包含不可修的，这里过滤掉 ——
// 传给 C# 侧无效规则会白跑一遍。
func NormalizeFixRules(ids []string) []string {
	if len(ids) == 0 {
		return DefaultFixRules()
	}

	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}

	out := make([]string, 0, len(ids))
	for _, id := range fixableRules {
		if selected[id] {
			out = append(out, id)
		}
	}
	return out
}
