package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"net/http"
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
