package service

import (
	"encoding/json"
	"testing"
)

func TestInspectBidTemplateStandaloneTechnicalReturnsEmptyArrays(t *testing.T) {
	st := &PlanState{
		OutlineMode: "standalone-technical",
		OutlineData: &OutlineData{
			Outline: []OutlineItem{{ID: "1", Title: "第一章", Number: "1"}},
		},
		BidTemplateExists:     true,
		BidTemplateChapterIDs: []string{"x"},
	}
	got := InspectBidTemplate(st)
	if got.Added == nil || got.Changed == nil {
		t.Fatalf("arrays must be non-nil: %+v", got)
	}
	if len(got.Added) != 0 || len(got.Changed) != 0 {
		t.Fatalf("expected empty inspection, got %+v", got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["exists"]; ok {
		t.Fatal("must not emit exists/chapterIds shape")
	}
	if _, ok := decoded["chapterIds"]; ok {
		t.Fatal("must not emit exists/chapterIds shape")
	}
	added, ok := decoded["added"].([]any)
	if !ok {
		t.Fatalf("added missing: %v", decoded)
	}
	changed, ok := decoded["changed"].([]any)
	if !ok {
		t.Fatalf("changed missing: %v", decoded)
	}
	_ = added
	_ = changed
}

func TestInspectBidTemplateEmptyOutlineReturnsEmpty(t *testing.T) {
	got := InspectBidTemplate(&PlanState{OutlineMode: "response-file"})
	if len(got.Added) != 0 || len(got.Changed) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestInspectBidTemplateNilStateSafe(t *testing.T) {
	got := InspectBidTemplate(nil)
	if got.Added == nil || got.Changed == nil {
		t.Fatalf("%+v", got)
	}
}
