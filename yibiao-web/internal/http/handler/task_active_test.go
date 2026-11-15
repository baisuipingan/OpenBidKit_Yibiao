package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/middleware"
)

func TestMarkDoneClearsFindRunningAndGetActive(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	taskID := "ab028f41-1951-47cb-a500-bd3cff0716d2"
	h.tasks[taskID] = &taskRecord{
		TaskID:    taskID,
		Type:      "bid-analysis",
		Status:    "running",
		StartedAt: time.Date(2026, 11, 15, 14, 28, 2, 0, time.UTC),
	}

	if got := h.findRunning("bid-analysis"); got != taskID {
		t.Fatalf("before MarkDone findRunning=%q", got)
	}

	h.MarkDone(taskID, "error")

	if got := h.findRunning("bid-analysis"); got != "" {
		t.Fatalf("after failed MarkDone still running=%q", got)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/get-active", bytes.NewReader([]byte("{}")))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.CtxTenantID, "tenant-1")
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxProject, "project-1")

	if err := h.GetActive(c); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK   bool             `json:"ok"`
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if !envelope.OK {
		t.Fatalf("envelope=%s", rec.Body.String())
	}
	for _, item := range envelope.Data {
		if item["type"] == "bid-analysis" {
			t.Fatalf("get-active still returned finished bid-analysis: %#v", item)
		}
	}

	// 模拟下一次 start：findRunning 清空后不应再冲突
	if running := h.findRunning("bid-analysis"); running != "" {
		t.Fatalf("subsequent start would hit TASK_RUNNING for %s", running)
	}
}

func TestMarkDoneIdempotent(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	h.tasks["t1"] = &taskRecord{TaskID: "t1", Type: "outline-generation", Status: "running"}
	h.MarkDone("t1", "success")
	h.MarkDone("t1", "error") // 不应把已成功改成失败
	if h.tasks["t1"].Status != "success" || !h.tasks["t1"].Done {
		t.Fatalf("%#v", h.tasks["t1"])
	}
}

func TestTrackRegistersContentForGetActive(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	h.Track("c-track", "content-generation")
	if !h.HasRunning("c-track") {
		t.Fatal("expected running after Track")
	}
	if got := h.findRunning("content-generation"); got != "c-track" {
		t.Fatalf("findRunning=%q", got)
	}
	h.MarkDone("c-track", "error")
	if h.HasRunning("c-track") {
		t.Fatal("still running after MarkDone")
	}
	if got := h.findRunning("content-generation"); got != "" {
		t.Fatalf("findRunning after done=%q", got)
	}
}
