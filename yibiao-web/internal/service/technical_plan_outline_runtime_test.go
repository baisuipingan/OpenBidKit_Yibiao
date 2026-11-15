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

func TestParseWordControlSnapshot(t *testing.T) {
	if parseWordControlSnapshot(nil) != nil {
		t.Fatal("nil raw should be nil snapshot")
	}
	raw, _ := json.Marshal(map[string]any{
		"minimumWords": 2000,
		"maximumWords": 2000,
		"sectionWords": 500,
	})
	got := parseWordControlSnapshot(raw)
	if got["minimumWords"] != int32(2000) || got["maximumWords"] != int32(2000) || got["sectionWords"] != int32(500) {
		t.Fatalf("%#v", got)
	}
}

func TestToPlanStateIncludesWordControlSnapshot(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"minimumWords": 2000,
		"maximumWords": 2000,
		"sectionWords": 500,
	})
	st := toPlanState(store.TechnicalPlanMetum{
		OutlineWordControlSnapshot: raw,
	}, nil, nil, store.TechnicalPlanGenerationConfig{
		MinimumWords: 2000,
		MaximumWords: 2000,
		SectionWords: 500,
	})
	if st.OutlineWordControlSnapshot == nil {
		t.Fatal("expected snapshot from meta column")
	}
	if st.OutlineWordControlSnapshot["sectionWords"] != int32(500) {
		t.Fatalf("%#v", st.OutlineWordControlSnapshot)
	}
	if st.OutlineWordControlOptions["maximumWords"] != int32(2000) {
		t.Fatalf("options=%#v", st.OutlineWordControlOptions)
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
