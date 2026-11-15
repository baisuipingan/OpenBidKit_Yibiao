package image

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"net/http"
	"strings"
	"time"
)

// AIGenerator 通过 OpenAI 兼容的 /images/generations 接口生图。
//
// 大多数中转站（金龙、火山方舟、Agnes）都兼容这个接口。
// 也支持返回 base64 或 URL 两种形式。
type AIGenerator struct {
	baseURL string
	apiKey  string
	model   string
	// requestMode: stream | normal（生图一般用 normal）
	requestMode string
	client      *http.Client
}

// NewAIGenerator 创建 AI 生图器。
func NewAIGenerator(baseURL, apiKey, model string) *AIGenerator {
	if model == "" {
		model = "dall-e-3"
	}
	return &AIGenerator{
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		model:       model,
		requestMode: "normal",
		client:      &http.Client{Timeout: 180 * time.Second},
	}
}

// Generate 生成一张图。
func (g *AIGenerator) Generate(ctx context.Context, prompt string, opts AIOptions) ([]byte, error) {
	if g.baseURL == "" || g.apiKey == "" {
		return nil, fmt.Errorf("未配置生图模型")
	}

	size := opts.Size
	if size == "" {
		size = "1024x1024"
	}

	body := map[string]any{
		"model":  g.model,
		"prompt": prompt,
		"n":      1,
		"size":   size,
	}

	// 部分模型支持 response_format，不支持时去掉重试
	body["response_format"] = "b64_json"

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	endpoint := g.baseURL
	if !strings.HasSuffix(endpoint, "/images/generations") {
		endpoint += "/images/generations"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求生图接口失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// 如果是 response_format 不支持，去掉重试一次
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" &&
			strings.Contains(strings.ToLower(apiErr.Error.Message), "response_format") {
			return g.generateWithoutFormat(ctx, prompt, size)
		}
		return nil, fmt.Errorf("生图接口返回 HTTP %d: %s", resp.StatusCode, textutil.Truncate(string(raw), 300))
	}

	return g.parseImageResponse(ctx, raw)
}

// generateWithoutFormat 去掉 response_format 重试。
func (g *AIGenerator) generateWithoutFormat(ctx context.Context, prompt, size string) ([]byte, error) {
	body := map[string]any{
		"model":  g.model,
		"prompt": prompt,
		"n":      1,
		"size":   size,
	}
	data, _ := json.Marshal(body)

	endpoint := g.baseURL
	if !strings.HasSuffix(endpoint, "/images/generations") {
		endpoint += "/images/generations"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求生图接口失败: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("生图接口返回 HTTP %d: %s", resp.StatusCode, textutil.Truncate(string(raw), 300))
	}

	return g.parseImageResponse(ctx, raw)
}

// parseImageResponse 解析生图响应。
//
// 两种格式：
//   - { "data": [{ "b64_json": "..." }] }
//   - { "data": [{ "url": "https://..." }] }
func (g *AIGenerator) parseImageResponse(ctx context.Context, raw []byte) ([]byte, error) {
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析生图响应失败: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("生图接口没有返回图片")
	}

	item := out.Data[0]

	// base64 形式
	if item.B64JSON != "" {
		// 可能带 data:image/png;base64, 前缀
		b64 := item.B64JSON
		if i := strings.Index(b64, "base64,"); i >= 0 {
			b64 = b64[i+7:]
		}
		png, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("解码 base64 图片失败: %w", err)
		}
		return png, nil
	}

	// URL 形式：需要再下载一次
	if item.URL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := g.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("下载生成的图片失败: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("下载图片返回 HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return nil, fmt.Errorf("读取图片失败: %w", err)
		}
		return data, nil
	}

	return nil, fmt.Errorf("生图响应里既没有 b64_json 也没有 url")
}

// 编译期断言
var _ Generator = (*AIGenerator)(nil)
