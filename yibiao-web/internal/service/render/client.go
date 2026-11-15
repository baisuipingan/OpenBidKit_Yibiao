// Package render 是渲染 sidecar 的 Go 客户端。
//
// 能力：
//   - Mermaid 源码 → PNG
//   - HTML 片段 → PNG
//
// 为什么需要它：
//
//	标书正文里的配图有两种来源，都需要浏览器渲染：
//	1. Mermaid：Agent 生成流程图源码，需真实渲染才好排版
//	2. HTML：组织架构图、数据看板这类，用 HTML 表达更精确
//
//	第三种是 AI 生图（调模型 API），不经过这里。
package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"net/http"
	"time"
)

// Client 是渲染 sidecar 的客户端。
type Client struct {
	baseURL       string
	internalToken string
	http          *http.Client
}

// NewClient 创建客户端。
func NewClient(baseURL, internalToken string) *Client {
	return &Client{
		baseURL:       baseURL,
		internalToken: internalToken,
		// 渲染可能较慢（首次启动浏览器），给足超时
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

// Image 是渲染结果。
type Image struct {
	// Data 是 PNG 字节
	Data []byte
	// Width / Height 像素尺寸
	Width  int
	Height int
	// AspectRatio 宽高比（宽/高）
	AspectRatio float64
	// TooWide 表示超过请求的 MaxAspectRatio
	TooWide bool
	// MimeType 固定 image/png
	MimeType string
}

// renderResponse 是 sidecar 的响应格式。
type renderResponse struct {
	OK    bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Data *struct {
		PNG         string  `json:"png"`
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		AspectRatio float64 `json:"aspectRatio"`
		TooWide     bool    `json:"tooWide"`
		MimeType    string  `json:"mimeType"`
	} `json:"data"`
}

// Mermaid 把 Mermaid 源码渲染成 PNG。
func (c *Client) Mermaid(ctx context.Context, code string, opts MermaidOptions) (*Image, error) {
	body := map[string]any{
		"code": code,
	}
	if opts.Theme != "" {
		body["theme"] = opts.Theme
	}
	if opts.Scale > 0 {
		body["scale"] = opts.Scale
	}
	if opts.MaxAspectRatio > 0 {
		body["maxAspectRatio"] = opts.MaxAspectRatio
	}

	return c.post(ctx, "/render/mermaid", body)
}

// MermaidOptions 是 Mermaid 渲染选项。
type MermaidOptions struct {
	// Theme: default | forest | dark | neutral
	Theme string
	// Scale 缩放倍数（2 表示 2x，适合打印）
	Scale int
	// MaxAspectRatio 最大宽高比（宽/高）。超过会自动收紧布局。
	//
	// 为什么需要：Mermaid 的 graph TD 如果节点很多且都连在一起，
	// 会横向铺开成极扁的长条（实测见过 3.96:1）。
	// 这种图放进 A4 文档后高度只有 4cm，文字完全看不清。
	// 经验值：文档配图控制在 2:1 以内。
	MaxAspectRatio float64
}

// HTMLOptions 是 HTML 渲染选项。
type HTMLOptions struct {
	// Width 画布宽度（像素）
	Width int
	// Height 固定高度，0 表示按内容自适应
	Height int
	// Scale 缩放倍数
	Scale int
	// BackgroundColor 背景色
	BackgroundColor string
}

// HTML 把 HTML 片段渲染成 PNG。
func (c *Client) HTML(ctx context.Context, html string, opts HTMLOptions) (*Image, error) {
	body := map[string]any{
		"html": html,
	}
	if opts.Width > 0 {
		body["width"] = opts.Width
	}
	if opts.Height > 0 {
		body["height"] = opts.Height
	}
	if opts.Scale > 0 {
		body["scale"] = opts.Scale
	}
	if opts.BackgroundColor != "" {
		body["backgroundColor"] = opts.BackgroundColor
	}

	return c.post(ctx, "/render/html", body)
}

// post 发送请求并解析结果。
func (c *Client) post(ctx context.Context, path string, body any) (*Image, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.internalToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.internalToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求渲染服务失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var out renderResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析响应失败（HTTP %d）: %s", resp.StatusCode, textutil.Truncate(string(raw), 200))
	}

	if !out.OK || out.Data == nil {
		msg := "渲染失败"
		if out.Error != nil {
			msg = out.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}

	png, err := base64.StdEncoding.DecodeString(out.Data.PNG)
	if err != nil {
		return nil, fmt.Errorf("解码图片失败: %w", err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("渲染结果是空图片")
	}
	// 校验 PNG 魔数
	if len(png) < 8 || png[0] != 0x89 || png[1] != 'P' || png[2] != 'N' || png[3] != 'G' {
		return nil, fmt.Errorf("渲染结果不是有效 PNG")
	}

	mt := out.Data.MimeType
	if mt == "" {
		mt = "image/png"
	}

	return &Image{
		Data:        png,
		Width:       out.Data.Width,
		Height:      out.Data.Height,
		AspectRatio: out.Data.AspectRatio,
		TooWide:     out.Data.TooWide,
		MimeType:    mt,
	}, nil
}

// Health 检查渲染服务可用性。
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接渲染服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("渲染服务状态异常: HTTP %d", resp.StatusCode)
	}
	return nil
}
