// Package image 编排配图生成。
//
// 三种生成方式（对齐桌面版 contentImageKinds.cjs）：
//   - mermaid：Agent 给源码，浏览器渲染（流程图、架构图）
//   - htmlImage：Agent 给 HTML，浏览器渲染（组织架构、数据看板）
//   - aiImage：调生图模型 API（现场照片、效果图这类）
//
// 产出统一为 PNG，存进资产库，返回 asset-ref 供受限 HTML 引用。
package image

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agenttool"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/render"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/service/textutil"
)

// 生成方式。
const (
	GenerationMermaid = "mermaid"
	GenerationHTML    = "htmlImage"
	GenerationAI      = "aiImage"
)

// Generator 是 AI 生图的接口。
//
// 定义成接口是为了让 image 包不依赖具体的模型实现，
// 也便于测试时注入假实现。
type Generator interface {
	// Generate 生成一张图，返回 PNG 字节。
	Generate(ctx context.Context, prompt string, opts AIOptions) ([]byte, error)
}

// AIOptions 是 AI 生图参数。
type AIOptions struct {
	// Size 图片尺寸，如 "1024x1024"
	Size string
	// Style 风格描述
	Style string
}

// Service 编排图片生成。
type Service struct {
	renderer  *render.Client
	generator Generator // 可为 nil（未配置生图模型）
	artifacts *artifact.Service
	log       *slog.Logger
}

// NewService 创建服务。
func NewService(renderer *render.Client, generator Generator, artifacts *artifact.Service, log *slog.Logger) *Service {
	return &Service{
		renderer:  renderer,
		generator: generator,
		artifacts: artifacts,
		log:       log,
	}
}

// Request 是生成一张图的请求。
type Request struct {
	Scope   storage.Scope
	OwnerID string
	// TaskID 关联的 Agent 任务
	TaskID string
	// FigureID 配图 ID（用于文件名与溯源）
	FigureID string
	// Generation 生成方式
	Generation string
	// Prompt 提示词（AI 生图用）或说明
	Prompt string
	// Code Mermaid 源码（mermaid 用）
	Code string
	// HTML 片段（htmlImage 用）
	HTML string
	// Width/Height 画布尺寸（htmlImage 用）
	Width  int
	Height int
	// Alt 替代文本
	Alt string
}

// Result 是生成结果。
type Result struct {
	// AssetRef 供受限 HTML 的 data-yb-asset-ref 引用
	AssetRef string `json:"assetRef"`
	// AssetID 资产 ID
	AssetID string `json:"assetId"`
	// Width / Height 实际尺寸
	Width  int `json:"width"`
	Height int `json:"height"`
	// Generation 使用的生成方式
	Generation string `json:"generation"`
	// AspectRatio 宽高比（宽/高）
	AspectRatio float64 `json:"aspectRatio,omitempty"`
	// TooWide 宽高比超限（图会太扁，Word 里文字看不清）
	TooWide bool `json:"tooWide,omitempty"`
}

// Generate 生成一张配图。
func (s *Service) Generate(ctx context.Context, req Request) (*Result, error) {
	if req.FigureID == "" {
		return nil, fmt.Errorf("缺少配图 ID")
	}

	var (
		png         []byte
		width       int
		height      int
		aspectRatio float64
		tooWide     bool
		err         error
	)

	switch req.Generation {
	case GenerationMermaid:
		if strings.TrimSpace(req.Code) == "" {
			return nil, fmt.Errorf("配图 %s 缺少 Mermaid 源码", req.FigureID)
		}
		if s.renderer == nil {
			return nil, fmt.Errorf("渲染服务未就绪，无法生成 Mermaid 图片")
		}
		var img *render.Image
		img, err = s.renderer.Mermaid(ctx, req.Code, render.MermaidOptions{
			Theme: "default",
			Scale: 2, // 2x 适合打印
			// 限制宽高比：文档配图超过 2:1 会导致文字太小
			MaxAspectRatio: 2.0,
		})
		if err == nil {
			png, width, height = img.Data, img.Width, img.Height
			aspectRatio = img.AspectRatio
			tooWide = img.TooWide
		}

	case GenerationHTML:
		if strings.TrimSpace(req.HTML) == "" {
			return nil, fmt.Errorf("配图 %s 缺少 HTML 内容", req.FigureID)
		}
		if s.renderer == nil {
			return nil, fmt.Errorf("渲染服务未就绪，无法生成 HTML 图片")
		}
		w := req.Width
		if w <= 0 {
			w = 900
		}
		var img *render.Image
		img, err = s.renderer.HTML(ctx, req.HTML, render.HTMLOptions{
			Width:           w,
			Height:          req.Height,
			Scale:           2,
			BackgroundColor: "#ffffff",
		})
		if err == nil {
			png, width, height = img.Data, img.Width, img.Height
		}

	case GenerationAI:
		if s.generator == nil {
			return nil, fmt.Errorf("未配置生图模型，无法生成 AI 图片")
		}
		if strings.TrimSpace(req.Prompt) == "" {
			return nil, fmt.Errorf("配图 %s 缺少提示词", req.FigureID)
		}
		png, err = s.generator.Generate(ctx, req.Prompt, AIOptions{
			Size: "1024x1024",
		})
		// AI 图尺寸由模型决定，这里读 PNG 头拿实际尺寸
		width, height = pngSize(png)
		if height > 0 {
			aspectRatio = float64(width) / float64(height)
			tooWide = aspectRatio > 2.0
		}

	default:
		return nil, fmt.Errorf("未知的生成方式: %s", req.Generation)
	}

	if err != nil {
		return nil, fmt.Errorf("生成配图 %s 失败: %w", req.FigureID, err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("生成配图 %s 得到空图片", req.FigureID)
	}

	// 存进资产库
	// 文件名：figures/<taskId>/<figureId>.png
	name := fmt.Sprintf("figures/%s/%s.png", req.TaskID, req.FigureID)

	asset, err := s.artifacts.Put(ctx, artifact.PutRequest{
		Scope:    req.Scope,
		OwnerID:  req.OwnerID,
		Name:     name,
		Kind:     artifact.KindImage,
		MimeType: "image/png",
		Content:  strings.NewReader(string(png)),
		Size:     int64(len(png)),
		Source:   "agent",
		TaskID:   req.TaskID,
		Metadata: map[string]any{
			"figureId":   req.FigureID,
			"generation": req.Generation,
			"width":      width,
			"height":     height,
			"alt":        req.Alt,
			"prompt":     textutil.Truncate(req.Prompt, 500),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("保存配图失败: %w", err)
	}

	s.log.Info("配图已生成",
		slog.String("figure_id", req.FigureID),
		slog.String("generation", req.Generation),
		slog.Int("width", width),
		slog.Int("height", height),
		slog.Int("bytes", len(png)),
	)

	return &Result{
		// 受限 HTML 的 img[data-yb-asset-ref] 用这个相对路径
		AssetRef:    name,
		AssetID:     asset.ID,
		Width:       width,
		Height:      height,
		Generation:  req.Generation,
		AspectRatio: aspectRatio,
		TooWide:     tooWide,
	}, nil
}

// pngSize 从 PNG 头读尺寸。
//
// PNG 结构：8 字节签名 + IHDR 块（宽度在偏移 16，高度在 20，大端 4 字节）
func pngSize(data []byte) (int, int) {
	if len(data) < 24 {
		return 0, 0
	}
	// 校验 PNG 签名
	if data[0] != 0x89 || data[1] != 'P' || data[2] != 'N' || data[3] != 'G' {
		return 0, 0
	}
	w := int(data[16])<<24 | int(data[17])<<16 | int(data[18])<<8 | int(data[19])
	h := int(data[20])<<24 | int(data[21])<<16 | int(data[22])<<8 | int(data[23])
	return w, h
}

// GenerateForTool 是 agenttool 的适配器。
//
// 把工具调用转成生成请求，返回 asset-ref。
func (s *Service) GenerateForTool(
	ctx context.Context,
	inv agenttool.Invocation,
	args agenttool.GenerateImageArgs,
) (string, string, error) {
	figureID := args.FigureID
	if figureID == "" {
		figureID = fmt.Sprintf("fig-%d", time.Now().UnixNano())
	}

	warning := ""
	res, err := s.Generate(ctx, Request{
		Scope: storage.Scope{
			TenantID:  inv.TenantID,
			ProjectID: inv.ProjectID,
		},
		OwnerID:    inv.UserID,
		TaskID:     inv.TaskID,
		FigureID:   figureID,
		Generation: args.Kind,
		Prompt:     args.Prompt,
		Code:       args.Code,
		HTML:       args.HTML,
		Width:      args.Width,
		Alt:        args.Alt,
	})
	if err != nil {
		return "", "", err
	}

	// 宽高比检查：过宽的图在 Word 里文字会太小，提示 Agent 重画
	if res.TooWide {
		warning = fmt.Sprintf(
			"这张图宽高比 %.1f:1 偏扁，放进 Word 后文字可能看不清。"+
				"建议把节点精简到 12 个以内，或拆成两张图后重新生成。",
			res.AspectRatio,
		)
	}

	return res.AssetRef, warning, nil
}

// 编译期断言
var _ agenttool.ImageGenerator = (*Service)(nil)
