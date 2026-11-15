// Package agenttool 实现 Agent 业务工具的 Go 侧。
//
// 架构：
//
//	Agent (sidecar) 调用工具
//	  → sidecar POST 到 /api/agent-tools/<name>
//	  → Go 执行工具逻辑
//	  → 返回 { ok, data: { text, images, files } }
//	  → sidecar 转成 tool result 给 Agent
//
// 为什么这样设计（对比桌面版）：
//
//	桌面版的业务工具直接 require 业务服务，Agent 层和业务层强耦合。
//	Web 版通过 HTTP 解耦：
//	  - 业务逻辑全在 Go（能用 Go 的类型和测试）
//	  - sidecar 只做转发，换掉 Pi SDK 时工具层不用重写
//	  - 工具能力可按租户/套餐动态开关
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Declaration 是工具声明，传给 sidecar。
type Declaration struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters 是 JSON Schema（OpenAI 兼容）
	Parameters json.RawMessage `json:"parameters"`
	// CallbackURL 是 Go 侧的接收地址，由服务端填充
	CallbackURL string `json:"callbackUrl"`
}

// Result 是工具执行结果。
//
// sidecar 会把它转成 Agent 可读的 tool result。
type Result struct {
	// Text 是主要输出，Agent 直接读到
	Text string `json:"text,omitempty"`
	// Images 是生成的图片，Agent 可以引用路径
	Images []ImageRef `json:"images,omitempty"`
	// Files 是涉及的文件
	Files []FileRef `json:"files,omitempty"`
	// Data 是结构化数据，便于 Agent 后续处理
	Data map[string]any `json:"data,omitempty"`
}

// ImageRef 是一张图片的引用。
type ImageRef struct {
	Path string `json:"path"`
	Alt  string `json:"alt,omitempty"`
	// Width/Height 便于 Agent 判断布局
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// FileRef 是一个文件的引用。
type FileRef struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// Invocation 是一次工具调用的上下文。
//
// 关键字段都是服务端从会话信息填充的，Agent 无法伪造：
//   - TenantID / ProjectID 决定数据可见范围
//   - WorkspaceDir 决定文件读写的根目录
type Invocation struct {
	TenantID  string
	ProjectID string
	UserID    string
	TaskID    string
	SessionID string
	// WorkspaceDir 是 Agent 的工作目录，工具产出的文件应写在这里
	WorkspaceDir string
	// Args 是 Agent 传入的参数（已解析）
	Args json.RawMessage
}

// Tool 是一个业务工具的实现。
type Tool struct {
	Name        string
	Description string
	// Parameters 是 JSON Schema
	Parameters json.RawMessage
	// Handler 执行工具逻辑
	Handler func(ctx context.Context, inv Invocation) (*Result, error)
}

// Registry 管理已注册的工具。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
	log   *slog.Logger
	// basePath 是回调地址前缀（如 /api/agent-tools）
	basePath string
	// publicBase 是服务对外地址，用于拼完整 callbackUrl
	publicBase string
}

// NewRegistry 创建注册表。
func NewRegistry(basePath, publicBase string, log *slog.Logger) *Registry {
	return &Registry{
		tools:      make(map[string]*Tool),
		log:        log,
		basePath:   basePath,
		publicBase: publicBase,
	}
}

// Register 注册一个工具。
//
// 同名工具会被覆盖（便于热更新）。
func (r *Registry) Register(t *Tool) error {
	if t.Name == "" {
		return fmt.Errorf("工具名不能为空")
	}
	if t.Handler == nil {
		return fmt.Errorf("工具 %s 缺少 Handler", t.Name)
	}
	// 工具名只能含字母数字下划线，防 URL 注入
	for _, c := range t.Name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("工具名 %q 含非法字符（只允许字母数字下划线）", t.Name)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	return nil
}

// Get 按名字取工具。
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Declarations 返回全部工具声明（传给 sidecar）。
func (r *Registry) Declarations() []Declaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Declaration, 0, len(r.tools))
	for _, t := range r.tools {
		params := t.Parameters
		if len(params) == 0 {
			// 无参数工具给一个空对象 schema
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, Declaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
			CallbackURL: r.publicBase + r.basePath + "/" + t.Name,
		})
	}
	return out
}

// Names 返回全部工具名。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	return out
}

// Invoke 执行一个工具。
func (r *Registry) Invoke(ctx context.Context, name string, inv Invocation) (*Result, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("未知工具: %s", name)
	}

	// 补默认值
	if inv.WorkspaceDir == "" {
		return nil, fmt.Errorf("工具 %s 缺少工作目录", name)
	}

	// 统一解包 args。
	// Pi 有时把参数序列化成字符串再传（`"{\"path\":...}"`），
	// 这里做一次归一化，避免每个工具都处理两种形态。
	inv.Args = normalizeArgs(inv.Args)

	start := nowMillis()
	res, err := t.Handler(ctx, inv)
	elapsed := nowMillis() - start

	if err != nil {
		r.log.Warn("工具执行失败",
			slog.String("tool", name),
			slog.String("task_id", inv.TaskID),
			slog.Int64("elapsed_ms", elapsed),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	r.log.Info("工具执行完成",
		slog.String("tool", name),
		slog.String("task_id", inv.TaskID),
		slog.Int64("elapsed_ms", elapsed),
	)
	return res, nil
}

// normalizeArgs 把可能是「JSON 字符串」的参数解成「JSON 对象」。
//
// 现象：Agent 调用工具时 args 偶尔是 `"{\"path\":\"a.md\"}"`
// （带引号的字符串），而不是 `{"path":"a.md"}`。
// 这是模型的输出习惯差异，Go 侧统一容错。
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}

	// 先看是不是字符串字面量
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		trimmed := strings.TrimSpace(str)
		if trimmed == "" {
			return json.RawMessage(`{}`)
		}
		// 字符串内容本身是对象则解包
		if trimmed[0] == '{' || trimmed[0] == '[' {
			return json.RawMessage(trimmed)
		}
		return raw
	}

	return raw
}

// nowMillis 返回当前毫秒时间戳。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// GenerateImageArgs 是 generate_image 工具的参数。
//
// 放在 agenttool 包而不是 tools 包，是因为 image.Service
// 需要实现工具接口，反向依赖 tools 会形成循环。
type GenerateImageArgs struct {
	// Kind 生成方式：mermaid | htmlImage | aiImage
	Kind string `json:"kind"`
	// Prompt 画面描述（AI 生图必填；其他方式作为用途说明）
	Prompt string `json:"prompt"`
	// Code Mermaid 源码（kind=mermaid 时必填）
	Code string `json:"code"`
	// HTML 片段（kind=htmlImage 时必填）
	HTML string `json:"html"`
	// FigureID 配图 ID（不填自动生成）
	FigureID string `json:"figure_id"`
	// Alt 替代文本
	Alt string `json:"alt"`
	// Width HTML 画布宽度
	Width int `json:"width"`
}

// ImageGenerator 是图片生成能力的接口。
//
// 由 image.Service 实现；定义在 agenttool 是为了避免循环依赖。
//
// 返回 (assetRef, warning, error)：
//
//	warning 非空时表示图生成了但有质量问题（如宽高比过大），
//	Agent 会读到这条提示，可以调整后重画。
type ImageGenerator interface {
	GenerateForTool(ctx context.Context, inv Invocation, args GenerateImageArgs) (string, string, error)
}
