// Package agent 封装 Agent sidecar 的 HTTP 客户端。
//
// 职责边界：
//
//	本包只做「与 sidecar 通信」和「把事件转成 Go 侧事件」，
//	不实现 Agent 逻辑（那在 sidecar 里），不碰业务 Store。
//
// sidecar 为什么单独进程：
//
//	Pi SDK 是 ESM 的 JS 包，无法在 Go 里运行。它是项目最有价值的资产
//	（标书垂类的 Agent 编排能力全在里面），必须原样保留。
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client 是 Agent sidecar 的客户端。
type Client struct {
	baseURL       string
	internalToken string
	http          *http.Client
}

// NewClient 创建客户端。
func NewClient(baseURL, internalToken string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		internalToken: internalToken,
		// 建连和普通请求超时；长任务的等待不占这个超时
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Provider 是模型提供方配置。
type Provider struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// ModelConfig 是模型参数。
type ModelConfig struct {
	ContextLengthLimit int `json:"contextLengthLimit,omitempty"`
}

// CreateSessionRequest 是创建会话的请求。
type CreateSessionRequest struct {
	WorkspaceDir          string      `json:"workspaceDir"`
	SessionsDir           string      `json:"sessionsDir,omitempty"`
	SessionFile           string      `json:"sessionFile,omitempty"`
	Provider              Provider    `json:"provider"`
	ModelConfig           ModelConfig `json:"modelConfig,omitempty"`
	WorkspaceInstructions string      `json:"workspaceInstructions,omitempty"`
	ActiveTools           []string    `json:"activeTools,omitempty"`
	CustomTools           []any       `json:"customTools,omitempty"`
	FixedToolList         bool        `json:"fixedToolList,omitempty"`
	SummaryEnabled        *bool       `json:"summaryEnabled,omitempty"`
}

// SessionSnapshot 是会话创建后的快照信息。
type SessionSnapshot struct {
	SDKVersion  string   `json:"sdkVersion"`
	ActiveTools []string `json:"activeTools"`
	Model       struct {
		Provider      string `json:"provider"`
		ID            string `json:"id"`
		API           string `json:"api"`
		BaseURL       string `json:"baseUrl"`
		ContextWindow int    `json:"contextWindow"`
		MaxTokens     int    `json:"maxTokens"`
	} `json:"model"`
}

// CreateSessionResult 是创建会话的返回。
type CreateSessionResult struct {
	SessionID string          `json:"sessionId"`
	Snapshot  SessionSnapshot `json:"snapshot"`
}

// Stage 是任务的一个阶段。
type Stage struct {
	ID         string   `json:"id"`
	Prompt     string   `json:"prompt"`
	Tools      []string `json:"tools,omitempty"`
	MaxRetries int      `json:"maxRetries,omitempty"`
	OutputFile string   `json:"outputFile,omitempty"`
	// ValidateURL 是校验回调地址（Go 侧实现）。sidecar 每轮结束会 POST 到这里。
	ValidateURL string `json:"validateUrl,omitempty"`
	// BuildRepairPromptURL 是修复提示词回调地址。
	BuildRepairPromptURL string `json:"buildRepairPromptUrl,omitempty"`
}

// RunRequest 是启动任务的请求。
type RunRequest struct {
	TaskID    string  `json:"taskId"`
	Stages    []Stage `json:"stages"`
	TimeoutMs int64   `json:"timeoutMs,omitempty"`
}

// RunResult 是任务启动的返回。
type RunResult struct {
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
	Started   bool   `json:"started"`
}

// Event 是 sidecar 推送的事件。
type Event struct {
	Type string          `json:"type"`
	At   string          `json:"at"`
	Data json.RawMessage `json:"-"`
	// 原始 JSON，方便直接转发
	Raw json.RawMessage `json:"-"`
}

// envelope 是 sidecar 的统一响应格式。
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Health 检查 sidecar 是否可用。
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 Agent sidecar 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Agent sidecar 状态异常: HTTP %d", resp.StatusCode)
	}
	return nil
}

// CreateSession 创建 Agent 会话。
func (c *Client) CreateSession(ctx context.Context, req CreateSessionRequest) (*CreateSessionResult, error) {
	var out CreateSessionResult
	if err := c.doJSON(ctx, http.MethodPost, "/sessions", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Run 启动任务。立即返回，任务在 sidecar 内异步执行。
func (c *Client) Run(ctx context.Context, sessionID string, req RunRequest) (*RunResult, error) {
	var out RunResult
	if err := c.doJSON(ctx, http.MethodPost, "/sessions/"+sessionID+"/run", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Cancel 取消会话任务。
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodPost, "/sessions/"+sessionID+"/cancel", nil, nil)
}

// Delete 销毁会话。
func (c *Client) Delete(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/sessions/"+sessionID, nil, nil)
}

// StreamEvents 订阅会话事件流。
//
// 阻塞直到 ctx 取消或连接断开。每收到一个事件调用 onEvent。
// 调用方负责在自己的 goroutine 里跑，并在 ctx 取消时退出。
func (c *Client) StreamEvents(ctx context.Context, sessionID string, onEvent func(raw json.RawMessage)) error {
	url := c.baseURL + "/sessions/" + sessionID + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)

	// SSE 是长连接，不能用带超时的 client
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接事件流失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("事件流返回 %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var dataBuf bytes.Buffer

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("读取事件流失败: %w", err)
		}

		trimmed := strings.TrimRight(string(line), "\r\n")

		// 空行表示一个事件结束
		if trimmed == "" {
			if dataBuf.Len() > 0 {
				payload := bytes.TrimSpace(dataBuf.Bytes())
				if len(payload) > 0 {
					raw := make(json.RawMessage, len(payload))
					copy(raw, payload)
					onEvent(raw)
				}
				dataBuf.Reset()
			}
			continue
		}

		// 注释行（心跳）
		if strings.HasPrefix(trimmed, ":") {
			continue
		}

		// data: 行，可能有多个，拼接
		if strings.HasPrefix(trimmed, "data: ") {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimPrefix(trimmed, "data: "))
		}
		// event: 行忽略（事件类型在 data 里）
	}
}

// Close 清理客户端资源。当前无需处理，保留接口。
func (c *Client) Close() error { return nil }

// --- 内部 ---

func (c *Client) setAuth(req *http.Request) {
	if c.internalToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.internalToken)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Agent sidecar 失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析响应失败（HTTP %d）: %s", resp.StatusCode, textutil.Truncate(string(raw), 200))
	}

	if !env.OK {
		msg := "未知错误"
		code := "UNKNOWN"
		if env.Error != nil {
			msg = env.Error.Message
			code = env.Error.Code
		}
		return &SidecarError{Code: code, Message: msg, Status: resp.StatusCode}
	}

	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("解析数据失败: %w", err)
		}
	}
	return nil
}

// SidecarError 是 sidecar 返回的业务错误。
type SidecarError struct {
	Code    string
	Message string
	Status  int
}

func (e *SidecarError) Error() string {
	return fmt.Sprintf("agent sidecar: %s (%s)", e.Message, e.Code)
}

// Manager 管理活跃会话的生命周期。
type Manager struct {
	client *Client
	mu     sync.Mutex
	// 按 taskID 记录取消函数，任务完成时清理
	cancels map[string]context.CancelFunc
}

// NewManager 创建管理器。
func NewManager(client *Client) *Manager {
	return &Manager{client: client, cancels: make(map[string]context.CancelFunc)}
}

// Register 注册任务的取消函数。
func (m *Manager) Register(taskID string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancels[taskID] = cancel
}

// Unregister 清理任务的取消函数。
func (m *Manager) Unregister(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, taskID)
}

// Cancel 取消任务。
func (m *Manager) Cancel(taskID string) bool {
	m.mu.Lock()
	cancel, ok := m.cancels[taskID]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Client 暴露底层客户端。
func (m *Manager) Client() *Client { return m.client }

// AnswerQuestion 回答 Agent 的提问。
//
// 转发给 sidecar 的 POST /sessions/:id/answer，
// 唤醒 ask_user 工具里挂起的 Promise。
func (c *Client) AnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	if sessionID == "" {
		return fmt.Errorf("缺少会话标识")
	}

	body := map[string]any{
		"questionId": questionID,
		"answer":     answer,
	}

	var out struct {
		OK    bool `json:"ok"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	path := "/sessions/" + url.PathEscape(sessionID) + "/answer"
	if err := c.doJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return err
	}
	if !out.OK {
		msg := "回答失败"
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// ListQuestions 列出会话的待答问题。
func (c *Client) ListQuestions(ctx context.Context, sessionID string) ([]map[string]any, error) {
	path := "/sessions/" + url.PathEscape(sessionID) + "/questions"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			Questions []map[string]any `json:"questions"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	if !out.OK {
		msg := "查询失败"
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out.Data.Questions, nil
}
