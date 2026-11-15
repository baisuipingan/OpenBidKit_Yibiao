package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/middleware"
)

func TestExportRoutesRegistered(t *testing.T) {
	e := echo.New()
	api := e.Group("/api")
	h := NewExport(nil, nil)
	h.Register(api)

	foundWord, foundAlias := false, false
	for _, r := range e.Routes() {
		if r.Method != http.MethodPost {
			continue
		}
		if r.Path == "/api/export/word" {
			foundWord = true
		}
		if r.Path == "/api/export/export-word" {
			foundAlias = true
		}
	}
	if !foundWord || !foundAlias {
		t.Fatalf("routes missing word=%v alias=%v", foundWord, foundAlias)
	}
}

func TestExportWordWithoutHelperReturnsExportFailedNot404(t *testing.T) {
	e := echo.New()
	h := NewExport(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/export/export-word", strings.NewReader(`{"requestId":"r1","source":"technical-plan"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/export/export-word")
	c.Set(middleware.CtxTenantID, "tenant-1")
	c.Set(middleware.CtxUserID, "user-1")
	c.Set(middleware.CtxProject, "project-1")

	if err := h.ExportWord(c); err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if rec.Code == http.StatusNotFound {
		t.Fatalf("unexpected 404 body=%s", rec.Body.String())
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "EXPORT_FAILED" {
		t.Fatalf("envelope=%s", rec.Body.String())
	}
	if strings.Contains(strings.ToLower(envelope.Error.Message), "not found") {
		t.Fatalf("message still Not Found: %s", envelope.Error.Message)
	}
}

func TestWordExportResponseShape(t *testing.T) {
	resp := wordExportResponse{
		Success:     true,
		Path:        "/api/assets/abc/download",
		Message:     "Word 已导出",
		AssetID:     "abc",
		FileName:    "demo.docx",
		DownloadURL: "/api/assets/abc/download",
	}
	if !resp.Success || resp.Path == "" || resp.AssetID == "" {
		t.Fatalf("%+v", resp)
	}
}

func TestEnrichFlatFromSectionsFillsEmptyNodes(t *testing.T) {
	flat := []flatEntry{
		{NodeID: "a", Title: "有正文", Content: "已经足够长的章节正文内容"},
		{NodeID: "b", Title: "空", Content: ""},
	}
	enrichFlatFromSections(flat, map[string]any{
		"b": map[string]any{"content": "来自 contentGenerationSections 的正文，长度足够。"},
	})
	if flat[1].Content == "" {
		t.Fatal("expected content backfill")
	}
}
