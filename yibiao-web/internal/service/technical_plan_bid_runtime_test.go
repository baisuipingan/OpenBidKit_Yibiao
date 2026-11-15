package service

import (
	"encoding/json"
	"testing"
)

func TestSelectBidAnalysisPayloadShapeForPersist(t *testing.T) {
	// 确认启动态序列化后前端能读到 status=pending / bidAnalysisTask.running
	items := []map[string]any{
		{"id": "projectOverview", "label": "项目概述"},
		{"id": "projectInfo", "label": "项目信息"},
	}
	tasks := map[string]any{}
	for _, item := range items {
		id := item["id"].(string)
		tasks[id] = map[string]any{
			"id": id, "label": item["label"], "status": "pending", "content": "",
		}
	}
	extra := map[string]any{
		"bidAnalysisTasks":    tasks,
		"bidAnalysisProgress": 0,
		"bidAnalysisTask": map[string]any{
			"task_id": "task-1",
			"type":    "bid-analysis",
			"status":  "running",
			"progress": 0,
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
	task, ok := decoded["bidAnalysisTask"].(map[string]any)
	if !ok || task["status"] != "running" {
		t.Fatalf("bidAnalysisTask = %#v", decoded["bidAnalysisTask"])
	}
	bt, ok := decoded["bidAnalysisTasks"].(map[string]any)
	if !ok || len(bt) != 2 {
		t.Fatalf("bidAnalysisTasks = %#v", decoded["bidAnalysisTasks"])
	}
	item := bt["projectOverview"].(map[string]any)
	if item["status"] != "pending" {
		t.Fatalf("item status = %#v", item["status"])
	}
}

func TestMarkFailedPayloadTurnsPendingIntoError(t *testing.T) {
	tasks := map[string]any{
		"projectOverview": map[string]any{"id": "projectOverview", "status": "pending", "content": ""},
		"projectInfo":     map[string]any{"id": "projectInfo", "status": "success", "content": "ok"},
	}
	for id, raw := range tasks {
		m := raw.(map[string]any)
		if m["status"] == "success" {
			continue
		}
		m["status"] = "error"
		m["error"] = "模型未返回有效结果"
		tasks[id] = m
	}
	if tasks["projectOverview"].(map[string]any)["status"] != "error" {
		t.Fatal("pending should become error")
	}
	if tasks["projectInfo"].(map[string]any)["status"] != "success" {
		t.Fatal("success should stay")
	}
}
