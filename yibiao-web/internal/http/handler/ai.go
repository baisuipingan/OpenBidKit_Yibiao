package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/image"
	"github.com/patrick/yibiao-web/internal/service/settings"
)

// AI 处理模型连通性测试等直接调用模型的接口。
//
// 背景：桌面版这些调用走 Electron Main（`ai.chat` / `ai.testImageModel`），
// 前端设置页的「测试」按钮依赖它们。Web 版原来没实现，
// 点测试直接 404。
//
// 设计：这两个接口不经过 agent，直接转发到 OpenAI 兼容端点。
// 目的是「验证 Key 能不能用」，不需要 agent 的工具编排。
type AI struct {
	settings *settings.Service
	client   *http.Client
}

// NewAI 创建 handler。
func NewAI(settingsSvc *settings.Service) *AI {
	return &AI{
		settings: settingsSvc,
		client: &http.Client{
			// 模型首 token 有时要等几十秒，给足时间
			Timeout: 180 * time.Second,
		},
	}
}

// Register 注册路由。
func (a *AI) Register(g *echo.Group) {
	g.POST("/ai/chat", a.Chat)
	g.POST("/ai/request-json", a.RequestJSON)
	g.POST("/ai/test-image-model", a.TestImageModel)
	g.POST("/ai/generate-image", a.GenerateImage)
}

// chatMessage 是 OpenAI 格式的一条消息。
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// chatRequest 是前端传来的请求。
type chatRequest struct {
	Messages []chatMessage `json:"messages"`
	// Provider 可选：不传则用租户配置
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	TimeoutMS      int    `json:"timeout_ms"`
	TimeoutMessage string `json:"timeout_message"`
	// Temperature 可选
	Temperature *float64 `json:"temperature"`
	// MaxTokens 可选
	MaxTokens int `json:"max_tokens"`
}

// Chat 调用文本模型，返回纯文本回复。
//
// 前端设置页「测试」按钮走这里：发一条 "hi"，看模型能不能回。
func (a *AI) Chat(c echo.Context) error {
	tenantID, _ := identity(c)

	var req chatRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if len(req.Messages) == 0 {
		return httpx.BadRequest("messages 不能为空")
	}

	baseURL, apiKey, model, err := a.resolveProvider(c, tenantID, req.Provider)
	if err != nil {
		return err
	}

	// 超时：用请求指定的，默认 60 秒
	timeout := 60 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), timeout)
	defer cancel()

	reply, err := a.callChat(ctx, baseURL, apiKey, model, req)
	if err != nil {
		msg := err.Error()
		if ctx.Err() == context.DeadlineExceeded && req.TimeoutMessage != "" {
			msg = req.TimeoutMessage
		}
		return httpx.Fail(c, "AI_CALL_FAILED", msg)
	}

	return httpx.OK(c, reply)
}

// RequestJSON 调用模型并强制返回 JSON。
func (a *AI) RequestJSON(c echo.Context) error {
	tenantID, _ := identity(c)

	var req chatRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	baseURL, apiKey, model, err := a.resolveProvider(c, tenantID, req.Provider)
	if err != nil {
		return err
	}

	timeout := 120 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), timeout)
	defer cancel()

	reply, err := a.callChat(ctx, baseURL, apiKey, model, req)
	if err != nil {
		return httpx.Fail(c, "AI_CALL_FAILED", err.Error())
	}

	// 尝试解析成 JSON 返回；解析不了就返回原始串
	var parsed any
	cleaned := stripJSONFence(reply)
	if json.Unmarshal([]byte(cleaned), &parsed) == nil {
		return httpx.OK(c, parsed)
	}
	return httpx.OK(c, reply)
}

// testImageRequest 是生图测试请求。
//
// 前端设置页「测试」传的是整个 ClientConfig（含 image_model），
// 也兼容早期 provider / config 两种嵌套写法。
type testImageRequest struct {
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	Config *struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
		Model    string `json:"model_name"`
	} `json:"config"`
	ImageModel *struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
		Model    string `json:"model_name"`
	} `json:"image_model"`
}

// configuredAPIKeyPlaceholder 与 config handler 返回给前端的占位符一致。
// 前端测试时会把这个占位符原样传回，服务端应改用库里存的真实 Key。
const configuredAPIKeyPlaceholder = "__CONFIGURED__"

// pickImageTestCredentials 从请求体解析生图测试用的 Base URL / Key / 模型。
//
// 优先级：provider → config → image_model（ClientConfig）→ 调用方提供的回退值。
// 返回的 usedRequest 表示是否用了请求里显式携带的地址（相对租户库回退）。
func pickImageTestCredentials(req testImageRequest, fallbackBase, fallbackKey, fallbackModel string) (baseURL, apiKey, model string, usedRequest bool) {
	switch {
	case req.Provider != nil && strings.TrimSpace(req.Provider.BaseURL) != "":
		return strings.TrimSpace(req.Provider.BaseURL), strings.TrimSpace(req.Provider.APIKey), strings.TrimSpace(req.Provider.Model), true
	case req.Config != nil && strings.TrimSpace(req.Config.BaseURL) != "":
		return strings.TrimSpace(req.Config.BaseURL), strings.TrimSpace(req.Config.APIKey), strings.TrimSpace(req.Config.Model), true
	case req.ImageModel != nil && strings.TrimSpace(req.ImageModel.BaseURL) != "":
		return strings.TrimSpace(req.ImageModel.BaseURL), strings.TrimSpace(req.ImageModel.APIKey), strings.TrimSpace(req.ImageModel.Model), true
	default:
		return strings.TrimSpace(fallbackBase), strings.TrimSpace(fallbackKey), strings.TrimSpace(fallbackModel), false
	}
}

// TestImageModel 测试生图服务连通性。
//
// 做法：只验证 /models 列表能不能拉到（不发真实生图请求，
// 因为生图动辄几十秒且要计费）。
func (a *AI) TestImageModel(c echo.Context) error {
	tenantID, _ := identity(c)

	var req testImageRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 租户库回退：请求里没带地址时再用已保存配置
	var fallbackBase, fallbackKey, fallbackModel string
	if a.settings != nil {
		view, viewErr := a.settings.GetView(c.Request().Context(), tenantID)
		if viewErr == nil {
			fallbackBase, fallbackModel = view.Image.BaseURL, view.Image.Model
			fallbackKey = a.settings.ImageAPIKey(c.Request().Context(), tenantID)
		}
	}

	baseURL, apiKey, model, _ := pickImageTestCredentials(req, fallbackBase, fallbackKey, fallbackModel)

	// 前端回传的是占位符或空串时，改用服务端存的真实 Key
	if a.settings != nil && (apiKey == "" || apiKey == configuredAPIKeyPlaceholder) {
		if stored := a.settings.ImageAPIKey(c.Request().Context(), tenantID); stored != "" {
			apiKey = stored
		} else if apiKey == configuredAPIKeyPlaceholder {
			apiKey = ""
		}
	}

	if baseURL == "" {
		return httpx.Fail(c, "MISSING_CONFIG", "未配置生图服务地址")
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), 30*time.Second)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return httpx.Internal(err)
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return httpx.Fail(c, "IMAGE_TEST_FAILED", fmt.Sprintf("连接失败: %v", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return httpx.Fail(c, "IMAGE_TEST_FAILED", "API Key 无效或没有权限")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return httpx.Fail(c, "IMAGE_TEST_FAILED", fmt.Sprintf("服务返回 %d: %s", resp.StatusCode, string(body)))
	}

	// 能拉到列表就算通
	return httpx.OK(c, map[string]any{
		"success": true,
		"message": fmt.Sprintf("生图服务可用（模型: %s）", firstNonEmpty(model, "未指定")),
	})
}

// resolveProvider 取模型配置：请求里带就用，否则从租户配置读。
func (a *AI) resolveProvider(c echo.Context, tenantID string, explicit *struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}) (baseURL, apiKey, model string, err error) {
	if explicit != nil && explicit.BaseURL != "" {
		return explicit.BaseURL, explicit.APIKey, explicit.Model, nil
	}

	p, _, pErr := a.settings.Provider(c.Request().Context(), tenantID)
	if pErr != nil {
		return "", "", "", httpx.Fail(c, "MISSING_PROVIDER", pErr.Error())
	}
	if p.APIKey == "" {
		return "", "", "", httpx.Fail(c, "MISSING_API_KEY", "未配置 API Key，请在设置中填写")
	}
	return p.BaseURL, p.APIKey, p.Model, nil
}

// callChat 发一次 chat/completions 请求，返回回复文本。
func (a *AI) callChat(ctx context.Context, baseURL, apiKey, model string, req chatRequest) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("未配置 Base URL")
	}
	if apiKey == "" {
		return "", fmt.Errorf("未配置 API Key")
	}
	if model == "" {
		return "", fmt.Errorf("未配置模型名称")
	}

	payload := map[string]any{
		"model":    model,
		"messages": req.Messages,
		"stream":   false,
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}

	body, _ := json.Marshal(payload)
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("连接失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("API Key 无效或已过期（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// 尽量把服务商的错误信息透出来，方便用户排查
		return "", fmt.Errorf("服务返回 %d: %s", resp.StatusCode, truncateForError(string(raw), 300))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("响应格式异常: %s", truncateForError(string(raw), 200))
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("模型未返回内容")
	}

	return out.Choices[0].Message.Content, nil
}

// stripJSONFence 去掉 ``` 围栏。
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	lines = lines[1:]
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			lines = lines[:i]
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// truncateForError 截断错误详情，避免响应过大。
func truncateForError(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// GenerateImage 真实生成一张图，用于端到端验证生图链路。
//
// 区别于 test-image-model：那个只拉模型列表，不花钱。
// 这个是真生图，会消耗额度，所以只在显式调用时执行。
func (a *AI) GenerateImage(c echo.Context) error {
	tenantID, _ := identity(c)

	var req struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
		Model  string `json:"model"`
	}
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.Prompt == "" {
		req.Prompt = "a modern construction site with crane, blue sky, photorealistic"
	}

	view, err := a.settings.GetView(c.Request().Context(), tenantID)
	if err != nil {
		return httpx.Internal(err)
	}

	baseURL := view.Image.BaseURL
	if baseURL == "" {
		return httpx.Fail(c, "MISSING_CONFIG", "未配置生图服务地址")
	}
	model := req.Model
	if model == "" {
		model = view.Image.Model
	}
	apiKey := a.settings.ImageAPIKey(c.Request().Context(), tenantID)
	if apiKey == "" {
		return httpx.Fail(c, "MISSING_API_KEY", "未配置生图 API Key")
	}

	gen := image.NewAIGenerator(baseURL, apiKey, model)
	size := req.Size
	if size == "" {
		size = "1024x1024"
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), 180*time.Second)
	defer cancel()

	data, genErr := gen.Generate(ctx, req.Prompt, image.AIOptions{Size: size})
	if genErr != nil {
		return httpx.Fail(c, "IMAGE_GEN_FAILED", genErr.Error())
	}

	return httpx.OK(c, map[string]any{
		"success": true,
		"size":    len(data),
		"model":   model,
		"message": fmt.Sprintf("生图成功，%d 字节", len(data)),
	})
}
