package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TextTaskSpec 是一次纯文本分析任务。
//
// 与 TaskSpec 的区别：
//   - 不创建 sidecar 会话，不需要工作区
//   - 不走 Agent 工具循环，单次请求单次响应
//   - 适合分类、抽取、比对这类确定性任务
//
// 为什么需要它：
//
//	一致性检查要读全文再输出 JSON，用 Agent 反而增加不确定性和延迟。
//	直接调 chat/completions 更可控、更便宜、更快。
type TextTaskSpec struct {
	TenantID  string
	ProjectID string
	Provider  Provider
	Model     ModelConfig
	// Prompt 用户消息
	Prompt string
	// System 系统消息（可选）
	System string
	// Timeout 超时
	Timeout time.Duration
	// Temperature 温度（抽取类任务建议 0）
	Temperature float64
	// MaxTokens 输出上限
	MaxTokens int
}

// TextTaskResult 是分析结果。
type TextTaskResult struct {
	// Text 模型返回的文本
	Text string
	// PromptTokens / CompletionTokens 用量
	PromptTokens     int
	CompletionTokens int
	// DurationMs 耗时
	DurationMs int64
}

// RunTextTask 执行一次纯文本分析。
//
// 直接调 OpenAI 兼容的 /chat/completions，不走 sidecar。
func (s *Service) RunTextTask(ctx context.Context, spec TextTaskSpec) (*TextTaskResult, error) {
	if spec.Provider.BaseURL == "" || spec.Provider.APIKey == "" {
		return nil, fmt.Errorf("未配置模型服务，请先在设置中填写 API Key")
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return nil, fmt.Errorf("缺少提示词")
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	// 构造消息
	messages := make([]map[string]string, 0, 2)
	if spec.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": spec.System})
	}
	messages = append(messages, map[string]string{"role": "user", "content": spec.Prompt})

	// 抽取类任务默认温度 0，保证可复现
	temperature := spec.Temperature

	body := map[string]any{
		"model":       spec.Provider.Model,
		"messages":    messages,
		"temperature": temperature,
		"stream":      false,
	}
	if spec.MaxTokens > 0 {
		body["max_tokens"] = spec.MaxTokens
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	endpoint := strings.TrimRight(spec.Provider.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+spec.Provider.APIKey)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}

	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("模型返回错误: %s", out.Error.Message)
	}

	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("模型没有返回内容")
	}

	content := out.Choices[0].Message.Content

	return &TextTaskResult{
		Text:             content,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		DurationMs:       time.Since(start).Milliseconds(),
	}, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
