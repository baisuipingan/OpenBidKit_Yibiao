package service

import (
	"encoding/json"
	"testing"
)

func TestNormalizeGlobalFactsPayloadNamedArray(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"globalFacts": []any{
			map[string]any{"id": "a", "title": "A", "content": "- x"},
		},
	})
	facts, ok := NormalizeGlobalFactsPayload(body)
	if !ok || len(facts) != 1 {
		t.Fatalf("ok=%v len=%d", ok, len(facts))
	}
}

func TestNormalizeGlobalFactsPayloadValueWrapper(t *testing.T) {
	// 旧桥接把数组包成 {value:[...]}
	body, _ := json.Marshal(map[string]any{
		"value": []any{
			map[string]any{"id": "b", "title": "B", "content": "- y"},
		},
	})
	facts, ok := NormalizeGlobalFactsPayload(body)
	if !ok || len(facts) != 1 {
		t.Fatalf("ok=%v len=%d", ok, len(facts))
	}
	m := facts[0].(map[string]any)
	if m["id"] != "b" {
		t.Fatalf("%#v", m)
	}
}

func TestNormalizeGlobalFactsPayloadFactsKey(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"facts": []any{
			map[string]any{"id": "c", "title": "C", "content": "- z"},
		},
	})
	facts, ok := NormalizeGlobalFactsPayload(body)
	if !ok || len(facts) != 1 {
		t.Fatalf("ok=%v len=%d", ok, len(facts))
	}
}

func TestNormalizeGlobalFactsPayloadRawArray(t *testing.T) {
	body, _ := json.Marshal([]any{
		map[string]any{"id": "d", "title": "D", "content": "- w"},
	})
	facts, ok := NormalizeGlobalFactsPayload(body)
	if !ok || len(facts) != 1 {
		t.Fatalf("ok=%v len=%d", ok, len(facts))
	}
}

func TestNormalizeGlobalFactsPayloadRejectsObjectWithoutArray(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"foo": "bar"})
	if _, ok := NormalizeGlobalFactsPayload(body); ok {
		t.Fatal("expected reject")
	}
}
