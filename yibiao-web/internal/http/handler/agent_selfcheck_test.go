package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/http/middleware"
)

func TestResolveSelfCheckProviderPrefersRequest(t *testing.T) {
	h := &Agent{}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/self-check", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	p, source, err := h.resolveSelfCheckProvider(c, "tenant", SelfCheckRequest{
		Provider: &struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		}{
			BaseURL: "https://example.com/v1",
			APIKey:  "fake-key",
			Model:   "demo-model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if source != "request" {
		t.Fatalf("source = %q", source)
	}
	if p.BaseURL != "https://example.com/v1" || p.APIKey != "fake-key" || p.Model != "demo-model" {
		t.Fatalf("unexpected provider: %+v", p)
	}
}

func TestResolveSelfCheckProviderRejectsPlaceholderWithoutSettings(t *testing.T) {
	h := &Agent{} // settings == nil
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_, _, err := h.resolveSelfCheckProvider(c, "tenant", SelfCheckRequest{
		Provider: &struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		}{
			BaseURL: "https://example.com/v1",
			APIKey:  configuredAPIKeyPlaceholder,
			Model:   "demo-model",
		},
	})
	if err == nil {
		t.Fatal("expected error when placeholder key and no settings")
	}
	if !strings.Contains(err.Error(), "设置中配置模型") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelfCheckEmptyBodyWithoutSettingsReturnsJSONFailure(t *testing.T) {
	h := &Agent{} // no settings → cannot fall back
	e := echo.New()
	e.HTTPErrorHandler = httpx.ErrorHandler
	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/self-check", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/agent/self-check")
	c.Set(middleware.CtxTenantID, "00000000-0000-0000-0000-000000000001")
	c.Set(middleware.CtxUserID, "00000000-0000-0000-0000-000000000002")

	if err := h.SelfCheck(c); err != nil {
		t.Fatalf("SelfCheck returned error: %v", err)
	}

	raw, _ := io.ReadAll(rec.Body)
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Steps   []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"steps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("response not JSON envelope: %s", raw)
	}
	if !envelope.OK {
		t.Fatalf("expected ok envelope with failed self-check data, got: %s", raw)
	}
	if envelope.Data.Success {
		t.Fatal("expected success=false")
	}
	if !strings.Contains(envelope.Data.Message, "设置中配置模型") {
		t.Fatalf("message = %q", envelope.Data.Message)
	}
	ct := rec.Header().Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected JSON, got SSE content-type %q", ct)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "ok", "later"); got != "ok" {
		t.Fatalf("got %q", got)
	}
}
