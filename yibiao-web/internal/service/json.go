package service

import "encoding/json"

// rawJSON 把 JSONB 列解成通用结构。
//
// sqlc 把 JSONB 映射成 []byte，这里统一转成 Go 的 any，
// 前端拿到的是解析后的对象而不是字符串。
// 解析失败返回空对象而不是 nil，避免前端拿到 null 后崩溃。
func rawJSON(b []byte) any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return map[string]any{}
	}
	if v == nil {
		return map[string]any{}
	}
	return v
}
