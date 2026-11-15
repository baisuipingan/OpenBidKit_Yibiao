package handler

import (
	"strings"
	"testing"

	"github.com/patrick/yibiao-web/internal/service"
)

func TestApplyContentPlanDefaultsUsesWordSnapshotAndFacts(t *testing.T) {
	req := &generateRequest{}
	state := &service.PlanState{
		OutlineWordControlSnapshot: map[string]any{
			"minimumWords": 2000,
			"maximumWords": 2000,
			"sectionWords": 500,
		},
		GlobalFacts: []any{
			map[string]any{"id": "team", "title": "项目角色", "content": "- 项目经理：按要求配备。"},
		},
	}
	facts := applyContentPlanDefaults(req, state)
	if req.TotalWords != 2000 {
		t.Fatalf("TotalWords=%d", req.TotalWords)
	}
	if req.TargetWordsPerSection != 500 {
		t.Fatalf("TargetWordsPerSection=%d", req.TargetWordsPerSection)
	}
	if !strings.Contains(facts, "项目角色") || !strings.Contains(facts, "项目经理") {
		t.Fatalf("facts=%q", facts)
	}
}

func TestApplyContentPlanDefaultsKeepsExplicitRequestValues(t *testing.T) {
	req := &generateRequest{TotalWords: 100, TargetWordsPerSection: 40}
	state := &service.PlanState{
		OutlineWordControlSnapshot: map[string]any{
			"minimumWords": 2000,
			"sectionWords": 500,
		},
	}
	_ = applyContentPlanDefaults(req, state)
	if req.TotalWords != 100 || req.TargetWordsPerSection != 40 {
		t.Fatalf("%+v", req)
	}
}

func TestContentMarkDoneClearsRunning(t *testing.T) {
	h := &Content{tasks: map[string]*contentTaskState{
		"c1": {TaskID: "c1", Status: "running"},
	}}
	h.MarkDone("c1", "error")
	if !h.tasks["c1"].Done || h.tasks["c1"].Status != "error" {
		t.Fatalf("%+v", h.tasks["c1"])
	}
}
