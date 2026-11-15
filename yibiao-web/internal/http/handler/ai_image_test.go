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
)

func TestPickImageTestCredentialsPrefersClientConfigImageModel(t *testing.T) {
	req := testImageRequest{
		ImageModel: &struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
			APIKey   string `json:"api_key"`
			Model    string `json:"model_name"`
		}{
			Provider: "custom",
			BaseURL:  "https://milorouter.com",
			APIKey:   "fake-key",
			Model:    "gpt-image-2",
		},
	}

	baseURL, apiKey, model, used := pickImageTestCredentials(req, "", "stored-key", "stored-model")
	if !used {
		t.Fatal("expected request image_model to be used")
	}
	if baseURL != "https://milorouter.com" {
		t.Fatalf("baseURL = %q", baseURL)
	}
	if apiKey != "fake-key" {
		t.Fatalf("apiKey = %q", apiKey)
	}
	if model != "gpt-image-2" {
		t.Fatalf("model = %q", model)
	}
}

func TestPickImageTestCredentialsFallsBackWhenImageModelMissingURL(t *testing.T) {
	req := testImageRequest{
		ImageModel: &struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
			APIKey   string `json:"api_key"`
			Model    string `json:"model_name"`
		}{
			Provider: "custom",
			BaseURL:  "  ",
			APIKey:   "fake-key",
			Model:    "gpt-image-2",
		},
	}

	baseURL, apiKey, model, used := pickImageTestCredentials(req, "https://fallback.example", "stored-key", "stored-model")
	if used {
		t.Fatal("expected fallback when image_model.base_url empty")
	}
	if baseURL != "https://fallback.example" || apiKey != "stored-key" || model != "stored-model" {
		t.Fatalf("unexpected fallback: %q %q %q", baseURL, apiKey, model)
	}
}

func TestTestImageModelAcceptsClientConfigImageModelWithoutMissingAddress(t *testing.T) {
	// 本地假 /models，避免打到真实生图服务商。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-image-2"}]}`)
	}))
	defer upstream.Close()

	e := echo.New()
	e.HTTPErrorHandler = httpx.ErrorHandler
	ai := &AI{
		settings: nil, // 本用例不走租户回退
		client:   upstream.Client(),
	}

	body := map[string]any{
		"image_model": map[string]any{
			"provider":   "custom",
			"base_url":   upstream.URL,
			"api_key":    "fake-test-key",
			"model_name": "gpt-image-2",
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/test-image-model", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/ai/test-image-model")

	if err := ai.TestImageModel(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Data  map[string]any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("expected ok response, got error=%+v body=%s", envelope.Error, rec.Body.String())
	}
	if envelope.Error != nil && envelope.Error.Message == "未配置生图服务地址" {
		t.Fatal("still reporting missing image service address")
	}
	if envelope.Data["success"] != true {
		t.Fatalf("data=%v", envelope.Data)
	}
}

func TestTestImageModelStillReportsMissingWhenNoURLAnywhere(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httpx.ErrorHandler
	ai := &AI{client: http.DefaultClient}

	raw := []byte(`{"image_model":{"provider":"custom","base_url":"","api_key":"fake","model_name":"gpt-image-2"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/test-image-model", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := ai.TestImageModel(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	if envelope.OK {
		t.Fatal("expected failure when base_url empty")
	}
	if envelope.Error == nil || envelope.Error.Message != "未配置生图服务地址" {
		t.Fatalf("unexpected error: %+v body=%s", envelope.Error, rec.Body.String())
	}
}
