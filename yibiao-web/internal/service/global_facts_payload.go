package service

import "encoding/json"

// NormalizeGlobalFactsPayload 把各种 Web 桥接体归一成 []any。
//
// 返回 ok=false 表示无法解析成事实数组。
func NormalizeGlobalFactsPayload(body []byte) ([]any, bool) {
	trimmed := body
	if len(trimmed) == 0 {
		return nil, false
	}
	var asArray []any
	if err := json.Unmarshal(trimmed, &asArray); err == nil {
		return asArray, true
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil || payload == nil {
		return nil, false
	}
	for _, key := range []string{"globalFacts", "facts", "value"} {
		if raw, ok := payload[key]; ok {
			switch v := raw.(type) {
			case []any:
				return v, true
			case nil:
				return []any{}, true
			default:
				// 有时被二次编码成 JSON 字符串
				if s, ok := v.(string); ok && s != "" {
					var nested []any
					if json.Unmarshal([]byte(s), &nested) == nil {
						return nested, true
					}
				}
			}
		}
	}
	return nil, false
}
