package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/service/agent/globalfacts"
)

func TestGlobalFactsRouteRegistered(t *testing.T) {
	e := echo.New()
	g := e.Group("/api")
	h := &Task{tasks: make(map[string]*taskRecord)}
	h.Register(g)

	found := false
	for _, r := range e.Routes() {
		if r.Method == http.MethodPost && r.Path == "/api/tasks/start-global-facts-generation" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("POST /api/tasks/start-global-facts-generation not registered")
	}
}

func TestGlobalFactsMarkDoneClearsFindRunning(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	taskID := "gf-task-1"
	h.tasks[taskID] = &taskRecord{
		TaskID:    taskID,
		Type:      globalfacts.TaskType,
		Status:    "running",
		StartedAt: time.Now(),
	}
	if got := h.findRunning(globalfacts.TaskType); got != taskID {
		t.Fatalf("findRunning=%q", got)
	}
	h.MarkDone(taskID, "success")
	if got := h.findRunning(globalfacts.TaskType); got != "" {
		t.Fatalf("still running after MarkDone: %q", got)
	}
}

func TestGlobalFactsStartPayloadShape(t *testing.T) {
	runningTask := map[string]any{
		"task_id":  "gf-1",
		"type":     globalfacts.TaskType,
		"status":   "running",
		"progress": 5,
		"logs":     []string{"全局事实设定任务已启动"},
		"mode":     "omit",
	}
	payload := map[string]any{
		"task": runningTask,
		"technicalPlanPatch": map[string]any{
			"globalFactsTask": runningTask,
			"globalFacts":     []any{},
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
	task := patch["globalFactsTask"].(map[string]any)
	if task["status"] != "running" || task["type"] != globalfacts.TaskType {
		t.Fatalf("%#v", task)
	}
	if task["mode"] != "omit" {
		t.Fatalf("mode=%v", task["mode"])
	}
}

func TestGlobalFactsStartWithoutRunnerReturnsConfiguredError(t *testing.T) {
	h := &Task{tasks: make(map[string]*taskRecord)}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/start-global-facts-generation", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.StartGlobalFactsGeneration(c); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "NOT_CONFIGURED" {
		t.Fatalf("envelope=%s", rec.Body.String())
	}
}

func TestNormalizeModeOmitAliases(t *testing.T) {
	if globalfacts.NormalizeMode("omit") != "omit" {
		t.Fatal("omit")
	}
	if globalfacts.NormalizeMode("笼统表述") != "omit" {
		t.Fatal("笼统表述")
	}
}
