package handler

import (
	"testing"
)

func TestPickStringFromNestedImageModel(t *testing.T) {
	nested := map[string]any{
		"provider":   "custom",
		"base_url":   "https://milorouter.com",
		"api_key":    "fake-key",
		"model_name": "gpt-image-2",
	}
	if got := pickString(nested, "base_url"); got != "https://milorouter.com" {
		t.Fatalf("base_url = %q", got)
	}
	if got := firstNonEmpty(pickString(nil, "image_provider"), pickString(nested, "provider")); got != "custom" {
		t.Fatalf("provider = %q", got)
	}
	if got := firstNonEmpty(pickString(nested, "model_name"), pickString(nested, "model")); got != "gpt-image-2" {
		t.Fatalf("model = %q", got)
	}
}

func TestSaveDetectsNestedImageModelNotFlatString(t *testing.T) {
	body := map[string]any{
		"api_key":   "__CONFIGURED__",
		"base_url":  "https://text.example",
		"model_name": "text-model",
		"image_model": map[string]any{
			"provider":   "custom",
			"base_url":   "https://image.example",
			"api_key":    "__CONFIGURED__",
			"model_name": "gpt-image-2",
		},
	}

	nestedImage, hasNestedImage := body["image_model"].(map[string]any)
	if !hasNestedImage {
		t.Fatal("expected nested image_model map")
	}
	// 旧 bug：pickString(body, "image_model") 对 map 返回 ""，再 SaveImage 空串覆盖。
	if pickString(body, "image_model") != "" {
		t.Fatal("pickString must ignore non-string image_model")
	}
	model := firstNonEmpty(pickString(nestedImage, "model_name"), pickString(nestedImage, "model"))
	if model != "gpt-image-2" {
		t.Fatalf("model = %q", model)
	}
	apiKey := firstNonEmpty(pickString(body, "image_api_key"), pickString(nestedImage, "api_key"))
	if apiKey != placeholderAPIKey {
		t.Fatalf("apiKey = %q", apiKey)
	}
}
