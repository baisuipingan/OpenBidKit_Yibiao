package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// FetchModels 调用 OpenAI 兼容的 /models 接口列出可用模型。
//
// 用途：前端设置页的模型下拉框。
// 不同的中转站返回格式略有差异，这里做统一处理。
func FetchModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("缺少服务地址")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("缺少 API Key")
	}

	// 规范化 URL：去掉尾部斜杠，确保以 /v1 结尾时正确拼接
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/models") {
		endpoint += "/models"
	}

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务返回 HTTP %d: %s", resp.StatusCode, textutil.Truncate(string(body), 200))
	}

	// 标准 OpenAI 格式：{ "data": [ { "id": "gpt-4o" }, ... ] }
	var openai struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openai); err == nil && len(openai.Data) > 0 {
		out := make([]string, 0, len(openai.Data))
		for _, m := range openai.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		sort.Strings(out)
		return out, nil
	}

	// 其他格式：[ "model1", "model2" ]
	var arr []string
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		sort.Strings(arr)
		return arr, nil
	}

	return nil, fmt.Errorf("无法解析模型列表响应: %s", textutil.Truncate(string(body), 200))
}

// truncate 截断字符串用于错误信息。

// modelInfoEndpoint 与桌面版 aiService.MODEL_INFO_ENDPOINT 保持一致。
// 按模型名查询上下文长度、输出上限、多模态等高级参数，不访问用户配置的中转站。
const modelInfoEndpoint = "https://analytics.agnet.top/model-info"

// ModelInfo 是自动填充高级参数所需的模型信息。
type ModelInfo struct {
	ReasoningEfforts  []string `json:"reasoningEfforts"`
	Context           int      `json:"context"`
	Output            int      `json:"output"`
	InputModalities   []string `json:"inputModalities"`
	OutputModalities  []string `json:"outputModalities"`
	ImageInputStatus  string   `json:"imageInputStatus"`
	TemperatureStatus string   `json:"temperatureStatus"`
	ConcurrencyLimit  int      `json:"concurrencyLimit"`
	RequestMode       string   `json:"requestMode"`
	SourceCount       int      `json:"sourceCount"`
}

// ModelInfoResult 对应桌面版 config.getModelInfo 的返回结构。
type ModelInfoResult struct {
	Success   bool       `json:"success"`
	Message   string     `json:"message"`
	ModelName string     `json:"modelName"`
	Model     *ModelInfo `json:"model"`
	SyncedAt  string     `json:"syncedAt"`
}

type modelInfoResponse struct {
	Code      int        `json:"code"`
	Message   string     `json:"message"`
	ModelName string     `json:"modelName"`
	Model     *ModelInfo `json:"model"`
	SyncedAt  string     `json:"syncedAt"`
}

func normalizeCapabilityStatus(value string) string {
	switch value {
	case "supported", "unsupported", "mixed", "unknown":
		return value
	default:
		return "unknown"
	}
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func lowerStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// FetchModelInfo 查询官方模型信息缓存，供设置页「自动填充高级参数」使用。
func FetchModelInfo(ctx context.Context, modelName string) (*ModelInfoResult, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return &ModelInfoResult{
			Success: false,
			Message: "请先填写文本模型名称",
		}, nil
	}

	endpoint := modelInfoEndpoint + "?modelName=" + url.QueryEscape(modelName)

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取模型信息失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var data modelInfoResponse
	if err := json.Unmarshal(body, &data); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("获取模型信息失败：HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("无法解析模型信息响应: %s", textutil.Truncate(string(body), 200))
	}

	if resp.StatusCode != http.StatusOK || data.Code != 0 {
		msg := strings.TrimSpace(data.Message)
		if msg == "" {
			msg = fmt.Sprintf("获取模型信息失败：HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}

	if data.Model == nil {
		return &ModelInfoResult{
			Success:   false,
			Message:   fmt.Sprintf("模型信息缓存中未找到 %s，请手动录入", modelName),
			ModelName: modelName,
			SyncedAt:  data.SyncedAt,
		}, nil
	}

	model := *data.Model
	model.ReasoningEfforts = cleanStringList(model.ReasoningEfforts)
	model.InputModalities = lowerStringList(model.InputModalities)
	model.OutputModalities = lowerStringList(model.OutputModalities)
	model.ImageInputStatus = normalizeCapabilityStatus(model.ImageInputStatus)
	model.TemperatureStatus = normalizeCapabilityStatus(model.TemperatureStatus)
	if model.Context < 0 {
		model.Context = 0
	}
	if model.Output < 0 {
		model.Output = 0
	}
	if model.ConcurrencyLimit <= 0 {
		model.ConcurrencyLimit = 10
	}
	if model.RequestMode != "normal" {
		model.RequestMode = "stream"
	}
	if model.SourceCount < 0 {
		model.SourceCount = 0
	}

	return &ModelInfoResult{
		Success:   true,
		Message:   "模型信息已获取",
		ModelName: modelName,
		Model:     &model,
		SyncedAt:  data.SyncedAt,
	}, nil
}
