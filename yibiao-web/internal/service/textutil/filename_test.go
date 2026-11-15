package textutil

import (
	"strings"
	"testing"
)

// TestSafeFileName 验证文件名清理的安全性与可用性。
func TestSafeFileName(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"普通中文", "投标文件.docx", "投标文件.docx"},
		{"带空格", "my bid.docx", "my bid.docx"},

		// 路径穿越
		{"父目录", "..", "unnamed"},
		{"当前目录", ".", "unnamed"},
		{"纯点", "...", "unnamed"},
		{"Unix 绝对路径", "/etc/passwd", "passwd"},
		{"Unix 穿越", "../../etc/passwd", "passwd"},
		{"Windows 穿越", `..\..\windows\system32\cmd.exe`, "cmd.exe"},
		{"混合分隔符", "a/b\\c.docx", "c.docx"},
		{"空串", "", "unnamed"},
		{"只有空白", "   ", "unnamed"},

		// 保留字符
		{"冒号", "a:b.docx", "a_b.docx"},
		{"竖线", "a|b.docx", "a_b.docx"},
		{"尖括号", "a<b>.docx", "a_b_.docx"},
		{"问号", "a?.docx", "a_.docx"},
		{"星号", "a*.docx", "a_.docx"},
	}

	for _, c := range cases {
		if got := SafeFileName(c.in); got != c.want {
			t.Errorf("%s: SafeFileName(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// TestSafeFileNameNoSeparator 结果永不含路径分隔符。
func TestSafeFileNameNoSeparator(t *testing.T) {
	bad := []string{
		"../../../etc/passwd", `..\..\boot.ini`, "/root/.ssh/id_rsa",
		"a/b/c.docx", `a\b\c.docx`, "..%2f..%2fetc",
	}
	for _, in := range bad {
		out := SafeFileName(in)
		if strings.ContainsAny(out, "/\\") {
			t.Errorf("SafeFileName(%q) = %q，仍含分隔符", in, out)
		}
	}
}

// TestSafeFileNameLength 超长名保留扩展名。
func TestSafeFileNameLength(t *testing.T) {
	long := strings.Repeat("很长的文件名", 50) + ".docx"
	out := SafeFileName(long)

	if n := len([]rune(out)); n > maxFileNameRunes {
		t.Errorf("超长名未截断：%d 字符 > %d", n, maxFileNameRunes)
	}
	if !strings.HasSuffix(out, ".docx") {
		t.Errorf("截断后丢了扩展名: %q", out[len(out)-20:])
	}
}
