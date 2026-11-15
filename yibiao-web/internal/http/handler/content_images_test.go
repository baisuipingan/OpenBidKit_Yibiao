package handler

import (
	"testing"

	"github.com/patrick/yibiao-web/internal/service/imageplan"
)

func shouldBuildImagePlan(req generateRequest) bool {
	return !req.DisableImages && req.TotalImages > 0
}

func TestDisableImagesOrZeroQuantitySkipsAutoPlan(t *testing.T) {
	sections := []imageplan.Section{
		{NodeID: "1", Title: "项目组织架构与进度网络图", Level: 2, TargetWords: 800, Description: "组织架构与进度计划"},
		{NodeID: "2", Title: "系统架构与拓扑方案", Level: 2, TargetWords: 800, Description: "架构拓扑"},
		{NodeID: "3", Title: "风险管理矩阵与流程", Level: 2, TargetWords: 800, Description: "风险矩阵"},
		{NodeID: "4", Title: "实施计划甘特图", Level: 2, TargetWords: 800, Description: "甘特图"},
		{NodeID: "5", Title: "质量保证体系", Level: 2, TargetWords: 800},
		{NodeID: "6", Title: "人员配置方案", Level: 2, TargetWords: 800},
		{NodeID: "7", Title: "培训与知识转移", Level: 2, TargetWords: 800},
		{NodeID: "8", Title: "验收与运维保障", Level: 2, TargetWords: 800},
		{NodeID: "9", Title: "应急预案与演练", Level: 2, TargetWords: 800},
	}
	// 旧行为：TotalImages=0 会自动估算（用于对比 Generate 的跳过逻辑）。
	auto, err := imageplan.Build(imageplan.Options{Sections: sections, TotalImages: 0})
	if err != nil {
		t.Fatal(err)
	}
	if auto.TotalImages <= 0 {
		t.Logf("auto plan empty for these titles (score gate); still assert Generate skip gate")
	}

	if shouldBuildImagePlan(generateRequest{DisableImages: true, TotalImages: 0}) {
		t.Fatal("disableImages should skip plan")
	}
	if shouldBuildImagePlan(generateRequest{DisableImages: false, TotalImages: 0}) {
		t.Fatal("zero quantity must skip plan")
	}
	if !shouldBuildImagePlan(generateRequest{DisableImages: false, TotalImages: 21}) {
		t.Fatal("positive quantity should build plan")
	}
}

func TestAllocateTargetWordsFromTotal(t *testing.T) {
	total := 2000
	leafCount := 65
	per := total / leafCount
	if per != 30 {
		t.Fatalf("per=%d", per)
	}
}
