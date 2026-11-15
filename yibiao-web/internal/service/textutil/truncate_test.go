package textutil

import "testing"

// TestTruncate 验证按字符（rune）截断，不切断中文。
func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"短于限制", "投标", 5, "投标"},
		{"等于限制", "投标文件", 4, "投标文件"},
		{"超长截断", "投标文件内容", 4, "投标文件…"},
		{"中文不切断", "中文测试内容很长很长", 6, "中文测试内容…"},
		{"英文", "hello world", 5, "hello…"},
		{"n 为 0", "任意", 0, ""},
		{"n 为负", "任意", -1, ""},
		{"空串", "", 5, ""},
	}

	for _, c := range cases {
		got := Truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("%s: Truncate(%q, %d) = %q，期望 %q", c.name, c.in, c.n, got, c.want)
		}
	}
}

// TestTruncateNoBrokenRune 确认截断结果永远是合法 UTF-8。
func TestTruncateNoBrokenRune(t *testing.T) {
	long := "这是一段很长的中文内容用来测试截断是否会产生乱码"
	for n := 0; n <= len([]rune(long))+2; n++ {
		got := Truncate(long, n)
		for _, r := range got {
			if r == '\uFFFD' {
				t.Errorf("n=%d 时出现替换字符（乱码）: %q", n, got)
				break
			}
		}
	}
}

// TestCollapseWhitespace 验证空白压缩。
func TestCollapseWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a  b", "a b"},
		{"  a  ", "a"},
		{"a\n\nb", "a b"},
		{"a\t\tb", "a b"},
		{"中文　全角空格", "中文 全角空格"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := CollapseWhitespace(c.in); got != c.want {
			t.Errorf("CollapseWhitespace(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestStripCodeFence 验证去掉 markdown 代码围栏。
//
// 这是模型输出解析的必经步骤：要求返回 JSON 时，
// 模型经常仍包一层 ```json，不剥掉就会 json.Unmarshal 失败。
func TestStripCodeFence(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "带 json 标识",
			in:   "```json\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "无语言标识",
			in:   "```\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			name: "没有围栏",
			in:   `{"a":1}`,
			want: `{"a":1}`,
		},
		{
			name: "前后有空白",
			in:   "  \n```json\n{\"a\":1}\n```\n  ",
			want: `{"a":1}`,
		},
		{
			name: "多行 JSON",
			in:   "```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```",
			want: "{\n  \"a\": 1,\n  \"b\": 2\n}",
		},
		{
			name: "只有开头没有结尾",
			in:   "```json\n{\"a\":1}",
			want: `{"a":1}`,
		},
		{
			name: "空串",
			in:   "",
			want: "",
		},
		{
			name: "只有围栏",
			in:   "```",
			want: "```",
		},
	}

	for _, c := range cases {
		if got := StripCodeFence(c.in); got != c.want {
			t.Errorf("%s: StripCodeFence(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}
