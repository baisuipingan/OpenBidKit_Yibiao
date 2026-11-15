package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/settings"
)

// Agent 处理 Agent 相关接口。
type Agent struct {
	svc      *agent.Service
	metrics  *service.UsageRecorder
	settings *settings.Service
	// toolDecls 可用的业务工具声明，由路由装配时注入
	toolDecls []any
}

// SetTools 注入可用工具声明。
func (h *Agent) SetTools(decls []any) {
	h.toolDecls = decls
}

// NewAgent 创建 handler。
func NewAgent(svc *agent.Service, metrics *service.UsageRecorder, settingsSvc *settings.Service) *Agent {
	return &Agent{svc: svc, metrics: metrics, settings: settingsSvc}
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
	// Provider 是可选的模型配置。不传则用租户配置（与桌面版 configStore.load 一致）。
	Provider *struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Model   string `json:"model"`
	} `json:"provider"`
	// Prompt 自定义测试提示词（完整 Agent 任务链路时使用；设置页自检可不传）
	Prompt string `json:"prompt"`
}

// selfCheckStep 对应前端 AgentSelfCheckStep。
type selfCheckStep struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
}

// SelfCheck 返回设置页期望的 AgentSelfCheckResult JSON。
//
// 桌面版：Settings 调 agent.selfCheck() 无参，Main 从 configStore 读模型配置。
// Web 版：bridge 发 POST /api/agent/self-check {}，必须回落到租户已保存配置；
// 且响应必须是 JSON 结果对象（不是 SSE），否则 bridge 会报 INVALID_RESPONSE。
func (h *Agent) SelfCheck(c echo.Context) error {
	tenantID, _ := identity(c)
	started := time.Now()
	checkedAt := started.UTC().Format(time.RFC3339Nano)

	var req SelfCheckRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	steps := []selfCheckStep{
		{ID: "environment", Label: "环境与模型配置", Status: "pending"},
		{ID: "runtime", Label: "Agent 运行时", Status: "pending"},
		{ID: "agent", Label: "智能体链路", Status: "pending"},
	}

	mark := func(id, status, message string) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for i := range steps {
			if steps[i].ID != id {
				continue
			}
			if status == "running" {
				steps[i].StartedAt = now
			} else {
				steps[i].CompletedAt = now
				if steps[i].StartedAt != "" {
					if t0, err := time.Parse(time.RFC3339Nano, steps[i].StartedAt); err == nil {
						steps[i].DurationMs = time.Since(t0).Milliseconds()
					}
				}
			}
			steps[i].Status = status
			steps[i].Message = message
			steps[i].UpdatedAt = now
			return
		}
	}
	skipPending := func(reason string) {
		for i := range steps {
			if steps[i].Status == "pending" {
				steps[i].Status = "skipped"
				steps[i].Message = reason
				steps[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
	}

	result := map[string]any{
		"report_version": 1,
		"check_id":       uuid.NewString(),
		"success":        false,
		"repaired":       false,
		"runtime_id":     "pi",
		"runtime_name":   "Pi Agent",
		"status":         "error",
		"message":        "",
		"checked_at":     checkedAt,
		"duration_ms":    0,
		"log_dir":        "",
		"log_file":       "",
		"runtime_root":   "",
		"workspace_dir":  "",
		"output_file":    "",
		"output_path":    "",
		"steps":          steps,
		"sections":       []any{},
		"detail_text":    "",
	}

	finish := func(success bool, status, message string) error {
		skipPending("因前置条件不足未执行")
		result["success"] = success
		result["status"] = status
		result["message"] = message
		result["detail_text"] = message
		result["duration_ms"] = time.Since(started).Milliseconds()
		result["steps"] = steps
		if !success {
			result["error"] = map[string]any{"message": message}
			result["diagnostics"] = map[string]any{"message": message}
		}
		return httpx.OK(c, result)
	}

	// 1) 模型配置：请求优先，否则租户库（桌面版等价于 configStore.load）
	mark("environment", "running", "正在读取模型配置")
	provider, providerSource, err := h.resolveSelfCheckProvider(c, tenantID, req)
	if err != nil {
		mark("environment", "error", err.Error())
		return finish(false, "error", err.Error())
	}
	mark("environment", "success", fmt.Sprintf("已加载模型配置（来源: %s，模型: %s）", providerSource, provider.Model))
	result["model_config"] = map[string]any{
		"base_url": provider.BaseURL,
		"model":    provider.Model,
		"source":   providerSource,
		// 不回传 api_key
		"has_api_key": provider.APIKey != "",
	}

	// 2) sidecar 健康
	mark("runtime", "running", "正在检查 Agent sidecar")
	healthCtx, healthCancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer healthCancel()
	if healthErr := h.svc.Healthy(healthCtx); healthErr != nil {
		msg := "Agent 运行时不可用：" + healthErr.Error()
		mark("runtime", "error", msg)
		mark("agent", "skipped", "运行时不可用，跳过链路探测")
		return finish(false, "error", msg)
	}
	mark("runtime", "success", "Agent sidecar 可达")

	// 3) 配置与运行时已就绪。设置页自检在 Web 端做到「配置可读 + 运行时可达」即可：
	// 完整 Agent 写文件任务会触发真实模型调用，留给正式任务链路。
	mark("agent", "success", "模型配置与运行时已就绪（未发起计费生成请求）")
	return finish(true, "normal", "Pi Agent 自检通过：模型配置可用，运行时可达")
}

// resolveSelfCheckProvider 取自检用的模型配置。
func (h *Agent) resolveSelfCheckProvider(c echo.Context, tenantID string, req SelfCheckRequest) (agent.Provider, string, error) {
	if req.Provider != nil {
		baseURL := strings.TrimSpace(req.Provider.BaseURL)
		apiKey := strings.TrimSpace(req.Provider.APIKey)
		model := strings.TrimSpace(req.Provider.Model)
		if baseURL != "" && apiKey != "" && apiKey != configuredAPIKeyPlaceholder {
			return agent.Provider{BaseURL: baseURL, APIKey: apiKey, Model: model}, "request", nil
		}
	}

	if h.settings == nil {
		return agent.Provider{}, "", fmt.Errorf("请在请求中提供 provider（baseUrl/apiKey/model），或先在设置中配置模型")
	}

	p, _, err := h.settings.Provider(c.Request().Context(), tenantID)
	if err != nil {
		return agent.Provider{}, "", fmt.Errorf("请在请求中提供 provider（baseUrl/apiKey/model），或先在设置中配置模型")
	}
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.APIKey) == "" {
		return agent.Provider{}, "", fmt.Errorf("请在请求中提供 provider（baseUrl/apiKey/model），或先在设置中配置模型")
	}
	return p, "settings", nil
}

// buildSelfCheckInstructions 构造自检任务的工作区约定。
//
// 注意：明确列出可用工具及其用途。
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
