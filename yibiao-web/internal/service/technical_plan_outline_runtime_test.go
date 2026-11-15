package service

import (
	"encoding/json"
	"testing"

	"github.com/patrick/yibiao-web/internal/store"
)

func TestBuildOutlineDataEmptyIsNil(t *testing.T) {
	if buildOutlineData(nil) != nil {
		t.Fatal("empty nodes should yield nil outlineData")
	}
	if buildOutlineData([]store.TechnicalPlanOutlineNode{}) != nil {
		t.Fatal("empty slice should yield nil outlineData")
	}
}

func TestOutlineRuntimePayloadJSON(t *testing.T) {
	extra := map[string]any{
		"outlineGenerationTask": map[string]any{
			"task_id":  "t1",
			"type":     "outline-generation",
			"status":   "error",
			"progress": 0,
			"error":    "目录生成失败：未得到有效目录",
			"logs":     []string{"目录生成失败：未得到有效目录"},
		},
	}
	raw, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	task := decoded["outlineGenerationTask"].(map[string]any)
	if task["status"] != "error" {
		t.Fatalf("%#v", task)
	}
}
