package textutil

import (
	"strings"
	"unicode/utf8"
)

// Truncate 截断字符串到最多 n 个字符（按 rune 计，不切断中文）。
//
// 如果发生截断，末尾补 "…"。
//
// 为什么统一放在这里：这个函数在 8 个文件里各有一份实现，
// 其中几处按字节截断（len(s) 而不是 rune 数），
// 会把中文切成半个字，日志和错误消息里出现乱码。
//
//	错误：s[:n]           → "投标文件投…"（第三字被切一半）
//	正确：[]rune(s)[:n]   → 按字符边界切
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}

	runes := []rune(s)
	return string(runes[:n]) + "…"
}

// TruncateKeepTail 保留尾部 n 个字符（用于日志看末尾的错误细节）。
func TruncateKeepTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return "…" + string(runes[len(runes)-n:])
}

// CollapseWhitespace 把连续空白压成一个空格并去首尾。
//
// 用于「给用户看的一行摘要」：模型返回的多行文本要压成单行，
// 否则在表格/列表里会撑破布局。
func CollapseWhitespace(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))

	prevSpace := true
	for _, r := range s {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u3000'
		if isSpace {
			if !prevSpace {
				sb.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		sb.WriteRune(r)
		prevSpace = false
	}

	return strings.TrimSpace(sb.String())
}

// StripCodeFence 去掉模型返回内容外层的 markdown 代码围栏。
//
//	```json
//	{"a":1}
//	```
//	  ↓
//	{"a":1}
//
// 为什么需要：要求模型"输出 JSON"时，它经常仍包一层 ```json。
// 直接 json.Unmarshal 会失败，必须在解析前剥掉。
//
// 为什么统一一份：这个逻辑在 3 个 agent 子包里各有一份。
// 只要有一处判断写漏（比如没处理 ``` 后面带语言标识），
// 就会出现"某个阶段总是解析失败"这种难查的问题。
func StripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}

	// 去掉第一行（``` 或 ```json）
	lines = lines[1:]

	// 从后往前找收尾的 ```，把它及之后的内容丢掉
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			lines = lines[:i]
			break
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}
