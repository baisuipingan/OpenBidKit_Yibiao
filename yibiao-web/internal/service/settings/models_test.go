package settings

import (
	"context"
	"testing"
)

func TestFetchModelInfoEmptyName(t *testing.T) {
	result, err := FetchModelInfo(context.Background(), "  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.Message != "请先填写文本模型名称" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestFetchModelInfoKnownModel(t *testing.T) {
	t.Parallel()
	result, err := FetchModelInfo(context.Background(), "deepseek-v4.1-flash")
	if err != nil {
		t.Skipf("catalog unavailable: %v", err)
	}
	if !result.Success || result.Model == nil {
		t.Fatalf("expected catalog hit, got %+v", result)
	}
	if result.Model.Context <= 0 || result.Model.Output <= 0 {
		t.Fatalf("unexpected limits: %+v", result.Model)
	}
}
