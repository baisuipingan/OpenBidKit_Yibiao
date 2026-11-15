package handler

import (
	"fmt"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/settings"
)

// Config 实现前端 `config.*` 命名空间的接口。
//
// 对应桌面版的 user_config.json 管理。
//
// 关键安全约束：
//
//	桌面版返回完整配置含明文 API Key；
//	Web 版只返回 has_*_api_key 标志和掩码，绝不返回明文。
//
// 结构约束：
//
//	前端类型 `ClientConfig extends AiConfig`（client/src/shared/types/config.ts），
//	意味着文本模型字段扁平在顶层，而 image_model / components 是嵌套对象。
//	任何字段缺失都会让前端读 undefined 报错并白屏。
type Config struct {
	svc *settings.Service
}

// NewConfig 创建 handler。
func NewConfig(svc *settings.Service) *Config {
	return &Config{svc: svc}
}

// Register 注册路由。
func (c *Config) Register(g *echo.Group) {
	g.POST("/config/load", c.Load)
	// 统一保存入口：前端 config.save 传整个配置对象，
	// 只暴露 save-text / save-image 会让设置页保存时 404。
	g.POST("/config/save", c.Save)
	g.POST("/config/save-text", c.SaveText)
	g.POST("/config/save-image", c.SaveImage)
	g.POST("/config/status", c.Status)
	g.POST("/config/list-models", c.ListModels)
	g.POST("/config/get-model-info", c.GetModelInfo)
}

// Load 读取配置（不含明文 Key）。
func (c *Config) Load(e echo.Context) error {
	tenantID, _ := identity(e)

	view, err := c.svc.GetView(e.Request().Context(), tenantID)
	if err != nil {
		return httpx.Internal(err)
	}

	body := map[string]any{
		// ===== 文本模型（扁平，因为 AiConfig extends TextModelConfig）=====
		"text_model_provider": normalizeTextProvider(view.Text.Provider),
		// 关键：前端用 `!config.api_key` 判断"未配置"，
		// 返回空串会被误判成未配置并强制跳转设置页。
		// 这里用占位符表示"已配置"，真实 Key 永不返回。
		"api_key":              apiKeyPlaceholder(view.Text.HasAPIKey),
		"base_url":             view.Text.BaseURL,
		"model_name":           view.Text.Model,
		"multimodal_enabled":   false,
		"reasoning_effort":     "",
		"context_length_limit": view.Text.ContextLengthLimit,
		"output_token_limit":   32768,
		"concurrency_limit":    3,
		"temperature_enabled":  false,
		"temperature":          1.0,
		"request_mode":         normalizeRequestMode(view.Text.RequestMode),
		// Web 版新增：告诉前端「配了没有」，桌面版没这两个字段
		"has_text_api_key":    view.Text.HasAPIKey,
		"masked_text_api_key": view.Text.MaskedKey,

		// ===== 生图模型（嵌套）=====
		"image_model": map[string]any{
			"provider":          normalizeImageProvider(view.Image.Provider),
			"base_url":          view.Image.BaseURL,
			"api_key":           apiKeyPlaceholder(view.Image.HasAPIKey),
			"model_name":        view.Image.Model,
			"image_size":        "auto",
			"image_ratio":       "1:1",
			"image_watermark":   false,
			"request_mode":      "stream",
			"concurrency_limit": 1,
			"has_api_key":       view.Image.HasAPIKey,
			"masked_api_key":    view.Image.MaskedKey,
		},

		// ===== 文件解析（嵌套）=====
		"components": map[string]any{
			"file_parser": map[string]any{
				"provider":     normalizeFileParser(view.Parser.TenderParser),
				"mineru_token": "",
				"has_token":    view.Parser.HasMineruKey,
			},
			"mermaid_concurrency_limit": 2,
			"html_concurrency_limit":    2,
		},

		// ===== 模型档案表（前端按 provider 索引，必须有全部 key）=====
		// 前端 normalizeTextModelProfile(config.text_model_provider, textModelProfiles[...])
		// 会读 profiles['official'] 等，缺 key 就抛 undefined 错误。
		"text_model_profiles":  buildTextProfiles(view.Text),
		"image_model_profiles": buildImageProfiles(view.Image),

		// ===== 其他 =====
		"official_api_model_type":   "cost-effective",
		"export_format":             nil,
		"agent_auto_answer_enabled": true,
		"developer_mode":            false,
	}

	return httpx.OK(e, body)
}

// SaveText 保存文本模型配置。
//
// 同时兼容两种字段命名：
//   - Web 版：text_api_key / text_base_url / text_model
//   - 桌面版：api_key / base_url / model_name
//
// 前端设置页用的是桌面版命名，所以兼容层是必需的。
func (c *Config) SaveText(e echo.Context) error {
	tenantID, _ := identity(e)

	raw := make(map[string]any)

	// 用通用 map 接收，避免两套结构体
	body := make(map[string]any)
	if err := e.Bind(&body); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	raw = body

	req := settings.SaveTextRequest{
		Provider:           pickString(raw, "text_provider", "text_model_provider", "provider"),
		BaseURL:            pickString(raw, "text_base_url", "base_url"),
		Model:              pickString(raw, "text_model", "model_name", "model"),
		RequestMode:        pickString(raw, "text_request_mode", "request_mode"),
		ContextLengthLimit: pickInt(raw, "context_length_limit"),
		APIKey:             pickString(raw, "text_api_key", "api_key"),
	}

	// 收到占位符说明用户没改 Key，不应该覆盖真实值
	if req.APIKey == placeholderAPIKey {
		req.APIKey = ""
	}

	if err := c.svc.SaveText(e.Request().Context(), tenantID, req); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(e, map[string]any{"success": true, "message": "配置已保存"})
}

// Save 统一保存配置（前端 config.save 的落点）。
//
// 为什么需要一个统一入口：设置页保存时传的是整个配置对象
// （文本模型 + 生图模型 + 服务商档案），而不是分开的两块。
// 若只暴露 /config/save-text 和 /config/save-image，
// 前端一次保存会 404。
//
// 实现上按字段分发：有文本相关字段就存文本，有图片字段就存图片。
func (c *Config) Save(e echo.Context) error {
	tenantID, _ := identity(e)

	body := make(map[string]any)
	if err := e.Bind(&body); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := e.Request().Context()

	// 文本模型：只要出现任一文本字段就保存
	hasText := false
	for _, k := range []string{
		"text_provider", "text_model_provider", "text_base_url",
		"text_model", "model_name", "api_key", "text_api_key", "request_mode",
	} {
		if _, ok := body[k]; ok {
			hasText = true
			break
		}
	}

	if hasText {
		apiKey := pickString(body, "text_api_key", "api_key")
		if apiKey == placeholderAPIKey {
			apiKey = ""
		}
		req := settings.SaveTextRequest{
			Provider:           pickString(body, "text_provider", "text_model_provider", "provider"),
			BaseURL:            pickString(body, "text_base_url", "base_url"),
			Model:              pickString(body, "text_model", "model_name", "model"),
			RequestMode:        pickString(body, "text_request_mode", "request_mode"),
			ContextLengthLimit: pickInt(body, "context_length_limit"),
			APIKey:             apiKey,
		}
		if err := c.svc.SaveText(ctx, tenantID, req); err != nil {
			return httpx.Internal(fmt.Errorf("保存文本模型配置失败: %w", err))
		}
	}

	// 生图模型：前端 ClientConfig 用嵌套 image_model 对象；
	// 旧接口用扁平 image_* 字段。两种都要认。
	nestedImage, hasNestedImage := body["image_model"].(map[string]any)
	hasImageFlat := false
	for _, k := range []string{
		"image_provider", "image_base_url", "image_api_key",
	} {
		if _, ok := body[k]; ok {
			hasImageFlat = true
			break
		}
	}
	// 注意：不能把顶层字符串 key "image_model" 当成扁平模型名——
	// ClientConfig 里它是对象；误走扁平会把生图配置写成空串并覆盖。
	if hasNestedImage || hasImageFlat {
		apiKey := firstNonEmpty(
			pickString(body, "image_api_key"),
			pickString(nestedImage, "api_key"),
		)
		if apiKey == placeholderAPIKey {
			apiKey = ""
		}
		req := settings.SaveImageRequest{
			Provider: firstNonEmpty(pickString(body, "image_provider"), pickString(nestedImage, "provider")),
			BaseURL:  firstNonEmpty(pickString(body, "image_base_url"), pickString(nestedImage, "base_url")),
			Model:    firstNonEmpty(pickString(nestedImage, "model_name"), pickString(nestedImage, "model")),
			APIKey:   apiKey,
		}
		if err := c.svc.SaveImage(ctx, tenantID, req); err != nil {
			return httpx.Internal(fmt.Errorf("保存生图模型配置失败: %w", err))
		}
	}

	// 文件解析：components.file_parser
	if components, ok := body["components"].(map[string]any); ok {
		if parser, ok := components["file_parser"].(map[string]any); ok {
			token := pickString(parser, "mineru_token", "token", "api_key")
			if token == placeholderAPIKey {
				token = ""
			}
			req := settings.SaveParserRequest{
				TenderParser:  pickString(parser, "provider", "tender_parser"),
				MineruBaseURL: pickString(parser, "mineru_base_url", "base_url"),
				MineruAPIKey:   token,
			}
			if req.TenderParser != "" || req.MineruBaseURL != "" || req.MineruAPIKey != "" {
				if err := c.svc.SaveParser(ctx, tenantID, req); err != nil {
					return httpx.Internal(fmt.Errorf("保存文件解析配置失败: %w", err))
				}
			}
		}
	}

	return httpx.OK(e, map[string]any{"success": true, "message": "配置已保存"})
}

// SaveImage 保存生图配置。
func (c *Config) SaveImage(e echo.Context) error {
	tenantID, _ := identity(e)

	body := make(map[string]any)
	if err := e.Bind(&body); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 也支持 image_model 嵌套
	nested := map[string]any{}
	if m, ok := body["image_model"].(map[string]any); ok {
		nested = m
	}

	req := settings.SaveImageRequest{
		Provider: firstNonEmpty(pickString(body, "image_provider"), pickString(nested, "provider")),
		BaseURL:  firstNonEmpty(pickString(body, "image_base_url"), pickString(nested, "base_url")),
		Model: firstNonEmpty(
			pickString(nested, "model_name"),
			pickString(nested, "model"),
			// 仅当 image_model 不是对象时，才把扁平字符串当模型名
			func() string {
				if _, isMap := body["image_model"].(map[string]any); isMap {
					return ""
				}
				return pickString(body, "image_model")
			}(),
		),
		APIKey: firstNonEmpty(pickString(body, "image_api_key"), pickString(nested, "api_key")),
	}

	if req.APIKey == placeholderAPIKey {
		req.APIKey = ""
	}

	if err := c.svc.SaveImage(e.Request().Context(), tenantID, req); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(e, map[string]any{"success": true, "message": "配置已保存"})
}

// Status 返回配置状态。
func (c *Config) Status(e echo.Context) error {
	tenantID, _ := identity(e)

	view, err := c.svc.GetView(e.Request().Context(), tenantID)
	if err != nil {
		return httpx.Internal(err)
	}

	textReady := view.Text.HasAPIKey && view.Text.BaseURL != "" && view.Text.Model != ""
	imageReady := view.Image.HasAPIKey && view.Image.BaseURL != ""

	return httpx.OK(e, map[string]any{
		"textReady":   textReady,
		"imageReady":  imageReady,
		"hasTextKey":  view.Text.HasAPIKey,
		"hasImageKey": view.Image.HasAPIKey,
	})
}

// ListModels 列出可用模型。
//
// 设置页会把整个 ClientConfig 传来；文本获取用顶层 base_url/api_key，
// 生图获取会覆盖顶层 base_url/api_key，但 reload 后 api_key 常是占位符。
func (c *Config) ListModels(e echo.Context) error {
	tenantID, _ := identity(e)

	body := make(map[string]any)
	_ = e.Bind(&body)

	baseURL := strings.TrimSpace(pickString(body, "base_url"))
	apiKey := strings.TrimSpace(pickString(body, "api_key"))
	// 前端 reload 后 api_key 常是占位符 __CONFIGURED__，不能当真 Key 去上游。
	if apiKey == placeholderAPIKey {
		apiKey = ""
	}

	_, looksLikeImage := body["image_model"].(map[string]any)
	// 生图「获取」会把 image 的 base_url/api_key 写到顶层，同时仍带 image_model 对象。
	if looksLikeImage {
		if nested, ok := body["image_model"].(map[string]any); ok {
			if baseURL == "" {
				baseURL = strings.TrimSpace(pickString(nested, "base_url"))
			}
			if apiKey == "" {
				nestedKey := strings.TrimSpace(pickString(nested, "api_key"))
				if nestedKey != "" && nestedKey != placeholderAPIKey {
					apiKey = nestedKey
				}
			}
		}
	}

	if baseURL == "" || apiKey == "" {
		ctx := e.Request().Context()
		if looksLikeImage {
			if apiKey == "" {
				if stored := c.svc.ImageAPIKey(ctx, tenantID); stored != "" {
					apiKey = stored
				}
			}
			if baseURL == "" {
				if view, err := c.svc.GetView(ctx, tenantID); err == nil {
					baseURL = view.Image.BaseURL
				}
			}
		}
		if baseURL == "" || apiKey == "" {
			p, _, err := c.svc.Provider(ctx, tenantID)
			if err != nil && apiKey == "" && baseURL == "" {
				return httpx.Fail(e, "MISSING_PROVIDER", err.Error())
			}
			if err == nil {
				if baseURL == "" {
					baseURL = p.BaseURL
				}
				if apiKey == "" {
					apiKey = p.APIKey
				}
			}
		}
	}

	if baseURL == "" {
		return httpx.Fail(e, "MISSING_PROVIDER", "未配置模型服务地址")
	}
	if apiKey == "" {
		return httpx.Fail(e, "MISSING_API_KEY", "未配置 API Key，请在设置中填写")
	}

	models, err := settings.FetchModels(e.Request().Context(), baseURL, apiKey)
	if err != nil {
		return httpx.Fail(e, "LIST_MODELS_FAILED", err.Error())
	}

	return httpx.OK(e, map[string]any{
		"success": true,
		"models":  models,
	})
}

// getModelInfoRequest 对应前端 window.yibiao.config.getModelInfo(modelName)。
// Web bridge 未声明位置参数名时，会把单参数包成 { "value": modelName }。
type getModelInfoRequest struct {
	Value     string `json:"value"`
	ModelName string `json:"modelName"`
	ModelID   string `json:"model_name"`
}

// GetModelInfo 按模型名从官方缓存填充高级参数。
//
// 与桌面版相同：不访问用户配置的 Base URL / API Key。
func (c *Config) GetModelInfo(e echo.Context) error {
	var req getModelInfoRequest
	_ = e.Bind(&req)

	modelName := strings.TrimSpace(req.ModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(req.ModelID)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(req.Value)
	}

	result, err := settings.FetchModelInfo(e.Request().Context(), modelName)
	if err != nil {
		return httpx.Fail(e, "GET_MODEL_INFO_FAILED", err.Error())
	}
	return httpx.OK(e, result)
}

// apiKeyPlaceholder 返回 API Key 的占位表示。
//
// 安全要求：绝不返回明文 Key。
// 兼容要求：前端用 truthy 判断"是否已配置"，空串会被误判。
//
// 用固定占位符，前端看到就认为是"已配置"；
// 保存配置时如果收到的值等于占位符，说明用户没改 Key，服务端保留原值。
const placeholderAPIKey = "__CONFIGURED__"

func apiKeyPlaceholder(has bool) string {
	if has {
		return placeholderAPIKey
	}
	return ""
}

// buildTextProfiles 构造文本模型的档案表。
//
// 前端枚举：official | jinlong | volcengine | deepseek | agnes | custom
// 每个 key 都要有，否则前端读 profiles[xxx] 会拿到 undefined。
//
// 策略：当前配置的 provider 用真实值，其他 provider 给空默认值。
func buildTextProfiles(cur settings.TextView) map[string]any {
	out := make(map[string]any, 6)

	makeProfile := func(provider string) map[string]any {
		isCurrent := normalizeTextProvider(cur.Provider) == provider
		p := map[string]any{
			"api_key":              "",
			"base_url":             "",
			"model_name":           "",
			"multimodal_enabled":   false,
			"reasoning_effort":     "",
			"context_length_limit": 128000,
			"output_token_limit":   32768,
			"concurrency_limit":    3,
			"temperature_enabled":  false,
			"temperature":          1.0,
			"request_mode":         "stream",
		}
		if isCurrent {
			p["base_url"] = cur.BaseURL
			p["model_name"] = cur.Model
			p["context_length_limit"] = cur.ContextLengthLimit
			p["request_mode"] = normalizeRequestMode(cur.RequestMode)
			p["api_key"] = apiKeyPlaceholder(cur.HasAPIKey)
		}
		return p
	}

	for _, provider := range []string{"official", "jinlong", "volcengine", "deepseek", "agnes", "custom"} {
		out[provider] = makeProfile(provider)
	}
	return out
}

// buildImageProfiles 构造生图模型的档案表。
//
// 前端枚举：jinlong | volcengine | google-ai-studio | agnes | custom | comfyui
func buildImageProfiles(cur settings.ImageView) map[string]any {
	out := make(map[string]any, 6)

	makeProfile := func(provider string) map[string]any {
		isCurrent := normalizeImageProvider(cur.Provider) == provider
		p := map[string]any{
			"provider":          provider,
			"base_url":          "",
			"api_key":           "",
			"model_name":        "",
			"image_size":        "auto",
			"image_ratio":       "1:1",
			"image_watermark":   false,
			"request_mode":      "stream",
			"concurrency_limit": 1,
		}
		if isCurrent {
			p["base_url"] = cur.BaseURL
			p["model_name"] = cur.Model
			p["api_key"] = apiKeyPlaceholder(cur.HasAPIKey)
		}
		return p
	}

	for _, provider := range []string{"jinlong", "volcengine", "google-ai-studio", "agnes", "custom", "comfyui"} {
		out[provider] = makeProfile(provider)
	}
	return out
}

// --- 取值辅助 ---

// pickString 按优先级从多个 key 里取第一个非空字符串。
func pickString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// pickInt 取整数，兼容 float64（JSON 数字默认解析成 float64）。
func pickInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// --- 规范化：数据库值 → 前端枚举 ---

// normalizeTextProvider 映射文本模型服务商。
func normalizeTextProvider(v string) string {
	switch v {
	case "official", "jinlong", "volcengine", "deepseek", "agnes", "custom":
		return v
	default:
		return "custom"
	}
}

// normalizeImageProvider 映射生图服务商。
func normalizeImageProvider(v string) string {
	switch v {
	case "jinlong", "volcengine", "google-ai-studio", "agnes", "custom", "comfyui":
		return v
	default:
		return "custom"
	}
}

// normalizeFileParser 映射文件解析器。
func normalizeFileParser(v string) string {
	switch v {
	case "local", "mineru-accurate-api", "mineru-agent-api":
		return v
	case "mineru":
		return "mineru-accurate-api"
	default:
		return "local"
	}
}

// normalizeRequestMode 映射请求模式。
func normalizeRequestMode(v string) string {
	if v == "normal" {
		return "normal"
	}
	return "stream"
}
