package handler

import (
	"testing"

	"github.com/patrick/yibiao-web/internal/service/agent/bidanalysis"
)

func TestSelectBidAnalysisItemsMapsFrontendAliases(t *testing.T) {
	items := selectBidAnalysisItems([]string{
		"projectOverview",
		"techRequirements",
		"projectInfo",
		"partAInfo",
		"deliveryAndServiceRequirements",
		"responseFileRequirements",
		"scoringCriteria",
	}, nil)

	got := map[string]bool{}
	for _, item := range items {
		got[item.ID] = true
	}
	for _, id := range []string{
		"projectBackground", // alias of projectOverview
		"technicalRequirements",
		"projectInfo",
		"partAInfo",
		"deliveryAndServiceRequirements",
		"responseFileRequirements",
		"scoringCriteria",
	} {
		if !got[id] {
			t.Fatalf("missing item %s in %#v", id, got)
		}
	}
	if len(items) >= len(bidanalysis.DefaultItems) {
		t.Fatalf("expected filtered items, got %d (all defaults=%d)", len(items), len(bidanalysis.DefaultItems))
	}
}

func TestSelectBidAnalysisItemsEmptyFallsBackToDefaults(t *testing.T) {
	items := selectBidAnalysisItems(nil, nil)
	if len(items) != len(bidanalysis.DefaultItems) {
		t.Fatalf("got %d items", len(items))
	}
}

func TestBidAnalysisUILabel(t *testing.T) {
	if bidAnalysisUILabel("projectOverview") != "项目概述" {
		t.Fatal(bidAnalysisUILabel("projectOverview"))
	}
}
