package handler

import (
	"encoding/json"
	"testing"

	"github.com/patrick/yibiao-web/internal/service"
)

func TestInspectBidTemplateResponseShapeForContinuePath(t *testing.T) {
	// 模拟前端 switchStep：inspection.added.length / changed.length
	payload := service.InspectBidTemplate(&service.PlanState{
		OutlineMode: "standalone-technical",
		OutlineData: &service.OutlineData{
			Outline: []service.OutlineItem{{ID: "a", Title: "章", Number: "1"}},
		},
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var inspection struct {
		Added   []any `json:"added"`
		Changed []any `json:"changed"`
	}
	if err := json.Unmarshal(raw, &inspection); err != nil {
		t.Fatal(err)
	}
	// 这正是「仍然继续」之后会执行的访问；缺字段会 panic/throw
	if len(inspection.Added) != 0 || len(inspection.Changed) != 0 {
		t.Fatalf("%+v", inspection)
	}
}
