package service

import (
	"testing"

	"github.com/patrick/yibiao-web/internal/store"
)

func TestGenerationConfigViewIncludesWordAndImageFields(t *testing.T) {
	view := GenerationConfigView(store.TechnicalPlanGenerationConfig{
		OutlineMode:          "standalone-technical",
		OutlineExpansionMode: "ai-complement",
		MinimumWords:         2000,
		MaximumWords:         2000,
		SectionWords:         500,
		GlobalFactsMode:      "fabricate",
		ImageQuantity:        0,
		UseAiImages:          true,
	})
	if view["outlineMode"] != "standalone-technical" {
		t.Fatalf("outlineMode = %v", view["outlineMode"])
	}
	ow := view["outlineWordControlOptions"].(map[string]any)
	if ow["maximumWords"] != int32(2000) {
		t.Fatalf("maximumWords = %v", ow["maximumWords"])
	}
	opts := view["contentGenerationOptions"].(map[string]any)
	if opts["imageQuantity"] != int32(0) {
		t.Fatalf("imageQuantity = %v", opts["imageQuantity"])
	}
	if view["globalFactsMode"] != "fabricate" {
		t.Fatalf("globalFactsMode = %v", view["globalFactsMode"])
	}
}
