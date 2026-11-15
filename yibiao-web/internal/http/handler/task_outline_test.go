package handler

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOutlineMarkDoneClearsFindRunning(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	taskID := "a177e8ee-77ca-425a-b754-c12987839c5f"
	h.tasks[taskID] = &taskRecord{
		TaskID:    taskID,
		Type:      "outline-generation",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if got := h.findRunning("outline-generation"); got != taskID {
		t.Fatalf("findRunning=%q", got)
	}
	h.MarkDone(taskID, "success")
	if got := h.findRunning("outline-generation"); got != "" {
		t.Fatalf("still running after MarkDone: %q", got)
	}
}

func TestOutlineStartPayloadShape(t *testing.T) {
	runningTask := map[string]any{
		"task_id":  "task-1",
		"type":     "outline-generation",
		"status":   "running",
		"progress": 5,
		"logs":     []string{"目录生成任务已启动"},
	}
	payload := map[string]any{
		"task": runningTask,
		"technicalPlanPatch": map[string]any{
			"outlineGenerationTask": runningTask,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	patch := decoded["technicalPlanPatch"].(map[string]any)
	task := patch["outlineGenerationTask"].(map[string]any)
	if task["status"] != "running" || task["progress"].(float64) != 5 {
		t.Fatalf("%#v", task)
	}
}

func TestContentMarkDoneClearsFindRunning(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	h.tasks["c1"] = &taskRecord{TaskID: "c1", Type: "content-generation", Status: "running"}
	h.MarkDone("c1", "error")
	if h.findRunning("content-generation") != "" {
		t.Fatal("content lock not cleared")
	}
}
