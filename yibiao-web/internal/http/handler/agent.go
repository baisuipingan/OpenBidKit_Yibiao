package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
)

// Agent 处理 Agent 相关接口。
type Agent struct {
	svc     *agent.Service
	metrics *service.UsageRecorder
	// toolDecls 可用的业务工具声明，由路由装配时注入
	toolDecls []any
}

// SetTools 注入可用工具声明。
func (h *Agent) SetTools(decls []any) {
	h.toolDecls = decls
}

// NewAgent 创建 handler。
func NewAgent(svc *agent.Service, metrics *service.UsageRecorder) *Agent {
	return &Agent{svc: svc, metrics: metrics}
}

// Register 注册路由。已登录。
func (h *Agent) Register(g *echo.Group) {
	// 健康检查：确认 sidecar 可达
	g.POST("/agent/health", h.Health)

	// 自检：跑一个最小 Agent 任务，验证整条链路
	g.POST("/agent/self-check", h.SelfCheck)

	// 取消任务
	g.POST("/agent/cancel", h.Cancel)
}

// Health 检查 sidecar 可用性。
func (h *Agent) Health(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()

	if err := h.svc.Healthy(ctx); err != nil {
		return httpx.Fail(c, "AGENT_UNAVAILABLE", err.Error())
	}
	return httpx.OK(c, map[string]any{
		"available": true,
		"runtime":   "pi",
	})
}

// SelfCheckRequest 是自检请求。
type SelfCheckRequest struct {
	// Provider 是可选的模型配置。不传则用租户配置。
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// Prompt 自定义测试提示词，默认让 Agent 写一个文件
	Prompt string `json:"prompt"`
}

// SelfCheck 跑一个最小 Agent 任务，全程事件回放给前端。
//
// 用途：确认 Agent 链路可用（模型能调通、工具能执行、事件能推送）。
// 这是验证「AI 能力是否真的接上了」最直接的办法。
func (h *Agent) SelfCheck(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req SelfCheckRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 模型配置：优先用请求里的，否则用租户配置
	var provider agent.Provider
	if req.Provider != nil && req.Provider.BaseURL != "" && req.Provider.APIKey != "" {
		provider = agent.Provider{
			BaseURL: req.Provider.BaseURL,
			APIKey:  req.Provider.APIKey,
			Model:   req.Provider.Model,
		}
	} else {
		return httpx.Fail(c, "MISSING_PROVIDER", "请在请求中提供 provider（baseUrl/apiKey/model），或先在设置中配置模型")
	}

	prompt := req.Prompt
	if prompt == "" {
		prompt = "请在工作目录下创建 note.md，内容为「Agent 链路正常」。只写这一句，写完就结束。"
	}

	// SSE 流式返回事件，前端实时看到 Agent 在工作
	res := c.Response()
	res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	res.Header().Set("Cache-Control", "no-cache, no-transform")
	res.Header().Set("Connection", "keep-alive")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(200)

	writeSSE := func(event string, data any) {
		b, err := json.Marshal(data)
		if err != nil {
			b = []byte("{}")
		}
		fmt.Fprintf(res, "event: %s\ndata: %s\n\n", event, b)
		res.Flush()
	}

	// 事件回调：直接转发给客户端
	onEvent := func(ev agent.TaskEvent) {
		var payload any = json.RawMessage(ev.Data)
		writeSSE("agent", payload)
	}

	handle, err := h.svc.StartTask(c.Request().Context(), agent.TaskSpec{
		TenantID:              tenantID,
		ProjectID:             projectID,
		Type:                  "self-check",
		Provider:              provider,
		Model:                 agent.ModelConfig{ContextLengthLimit: 128000},
		WorkspaceInstructions: buildSelfCheckInstructions(h.toolDecls),
		ActiveTools:           []string{"read", "write", "bash", "ls"},
		// 传入业务工具：自检任务要能验证工具回调链路
		CustomTools: h.toolDecls,
		Stages: []agent.Stage{
			{
				ID:         "self-check",
				Prompt:     prompt,
				Tools:      append([]string{"read", "write", "bash", "ls"}, customToolNames(h.toolDecls)...),
				MaxRetries: 1,
				// 不强制产物文件：自检的 prompt 是调用方自定义的，产物不确定
			},
		},
		Timeout: 3 * time.Minute,
	}, onEvent)

	if err != nil {
		writeSSE("error", map[string]string{"message": err.Error()})
		return nil
	}

	writeSSE("started", map[string]string{
		"taskId":    handle.TaskID,
		"sessionId": handle.SessionID,
	})

	// 等任务结束或客户端断开
	<-c.Request().Context().Done()
	h.svc.CancelTask(handle.TaskID)
	return nil
}

// buildSelfCheckInstructions 构造自检任务的工作区约定。
//
// 关键：明确列出可用工具及其用途。
// 否则 Agent 会去找「有没有 count_words」而浪费大量轮次。
func buildSelfCheckInstructions(decls []any) string {
	var sb strings.Builder
	sb.WriteString("你是标书工作台的自检任务。\n\n")
	sb.WriteString("## 可用工具\n\n")
	sb.WriteString("除了 read/write/bash/ls/grep/find 等原生工具外，你还有以下**业务工具**，")
	sb.WriteString("它们已注册在当前会话中，直接调用即可：\n\n")

	if len(decls) == 0 {
		sb.WriteString("（本会话未注册业务工具）\n")
	} else {
		for _, d := range decls {
			b, err := json.Marshal(d)
			if err != nil {
				continue
			}
			var m struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if json.Unmarshal(b, &m) == nil && m.Name != "" {
				sb.WriteString(fmt.Sprintf("- `%s`：%s\n", m.Name, m.Description))
			}
		}
	}

	sb.WriteString("\n## 工作要求\n\n")
	sb.WriteString("1. 需要这些能力时**直接调用工具**，不要先用 bash 去搜索工具是否存在\n")
	sb.WriteString("2. 不要读取软件自身的源码或安装目录，只操作当前工作区\n")
	sb.WriteString("3. 完成后不要输出总结，直接结束\n")
	return sb.String()
}

// customToolNames 从工具声明里提取工具名。
func customToolNames(decls []any) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		if m, ok := d.(map[string]any); ok {
			if n, ok := m["name"].(string); ok && n != "" {
				out = append(out, n)
			}
			continue
		}
		// agenttool.Declaration 走反射拿不到，用 JSON 转一次
		b, err := json.Marshal(d)
		if err != nil {
			continue
		}
		var m struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &m) == nil && m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out
}

// CancelRequest 是取消请求。
type CancelRequest struct {
	TaskID string `json:"taskId"`
}

// Cancel 取消运行中的任务。
func (h *Agent) Cancel(c echo.Context) error {
	var req CancelRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.TaskID == "" {
		return httpx.BadRequest("缺少 taskId")
	}

	ok := h.svc.CancelTask(req.TaskID)
	return httpx.OK(c, map[string]any{"canceled": ok})
}
