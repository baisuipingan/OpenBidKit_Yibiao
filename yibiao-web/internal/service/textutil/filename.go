package textutil

import (
	"path/filepath"
	"strings"
)

// maxFileNameRunes 文件名最大字符数。
//
// 200 是多数文件系统（ext4 255 字节 / NTFS 255 字符）的安全上限，
// 这里取 120 更保守 —— 还要给 "-fixed"、" (2)" 这类后缀留空间。
const maxFileNameRunes = 120

// SafeFileName 把任意用户输入变成可安全落盘的文件名。
//
// 处理的威胁：
//  1. 路径穿越：`../../etc/passwd` → `passwd`
//     - 统一反斜杠再 Base：Linux 的 filepath.Base 不把 \ 当分隔符，
//     而 Windows 客户端可能传 `..\..\cmd.exe`
//  2. 保留字符：`a<b>.docx` → `a_b_.docx`
//  3. 控制字符：\0 等会被截断文件名的字符
//  4. 空名 / 纯点：`.` `..` `...` → `unnamed`
//  5. 超长名：保留扩展名，截断主体
//
// 为什么必须统一一份实现：这个函数决定「用户输入能否写到工作目录之外」。
// 之前 handler 和 service 各有一份，逻辑不同（一份没有 .. 拦截），
// 只要哪天有人改了其中一份而忘了另一份，就是安全漏洞。
func SafeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed"
	}

	// 统一分隔符后再取最后一段
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}

	if name == "" || name == "." || name == ".." {
		return "unnamed"
	}

	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch r {
		case '<', '>', ':', '"', '|', '?', '*', '/', '\\', 0:
			b.WriteRune('_')
		default:
			if r < 32 {
				b.WriteRune('_')
			} else {
				b.WriteRune(r)
			}
		}
	}

	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." || strings.Trim(out, ".") == "" {
		return "unnamed"
	}

	// 超长：保留扩展名
	if len([]rune(out)) > maxFileNameRunes {
		ext := filepath.Ext(out)
		base := []rune(strings.TrimSuffix(out, ext))
		limit := maxFileNameRunes - len([]rune(ext))
		if limit < 1 {
			limit = 1
		}
		if len(base) > limit {
			base = base[:limit]
		}
		out = string(base) + ext
	}

	return out
}
