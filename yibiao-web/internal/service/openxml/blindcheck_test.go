package openxml

import (
	"context"
	"encoding/json"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBlindCheckInspect 验证 C# helper 的暗标检查能力。
//
// helper 已经内置 blind-check-inspect action（9,922 行 C# 实现），
// 这个测试确认 Go 侧能正常调用并拿到规则命中结果。
func TestBlindCheckInspect(t *testing.T) {
	binary := os.Getenv("OPENXML_BINARY")
	docxPath := os.Getenv("BLIND_CHECK_DOCX")
	if binary == "" || docxPath == "" {
		t.Skip("未设置 OPENXML_BINARY / BLIND_CHECK_DOCX，跳过")
	}

	ctx := context.Background()
	client, err := NewClient(ctx, Config{
		Binary:       binary,
		PoolSize:     1,
		JobTimeout:   2 * time.Minute,
		StartTimeout: 30 * time.Second,
	}, slog.Default())
	if err != nil {
		t.Fatalf("创建 client 失败: %v", err)
	}
	defer client.Close()

	// 用 ExecuteWithFiles 把 docx 物化进 helper 的临时工作区。
	//
	// 注意：client 的 workdir 是 os.MkdirTemp 建的临时目录，
	// 不是我们能预知的位置 —— 必须通过 Files 传进去。
	remoteName := "blind-test.docx"
	outLocal := filepath.Join(os.TempDir(), "blind-check-result.json")
	_ = os.Remove(outLocal)

	result, err := client.ExecuteWithFiles(ctx, map[string]any{
		"action": "blind-check-inspect",
		"source": remoteName,
		// 用「通用」规范：宋体四号、固定行距 25 磅、首行缩进 2 字符、页边距 2.5cm
		"profile": map[string]any{
			"page": map[string]any{
				"paper":            "A4",
				"margin_top_cm":    2.5,
				"margin_bottom_cm": 2.5,
				"margin_left_cm":   2.5,
				"margin_right_cm":  2.5,
			},
			"body": map[string]any{
				"font_cn":                 "宋体",
				"size_pt":                 14,
				"line_rule":               "exact",
				"line_pt":                 25,
				"first_line_indent_chars": 2,
			},
			"table": map[string]any{
				"font_cn": "宋体",
				"size_pt": 10.5,
			},
		},
		"sensitive_words": []map[string]any{
			{"text": "北京智慧医疗科技有限公司"},
			{"text": "张三"},
		},
	}, ExecuteOptions{
		Files: map[string]string{
			remoteName: docxPath,
		},
		// 结果写在 job 子目录里，用 CollectJobFiles 取
		CollectJobFiles: map[string]string{
			"blind-check-result.json": outLocal,
		},
	})
	if err != nil {
		t.Fatalf("执行检查失败: %v", err)
	}

	t.Logf("OK=%v Error=%s", result.OK, result.Error)

	// 读结果文件
	raw, err := os.ReadFile(outLocal)
	if err != nil {
		t.Fatalf("读结果文件失败: %v", err)
	}

	// 结果结构：按规则分组（不是平铺的 findings）
	var report struct {
		Summary struct {
			High   int `json:"high"`
			Medium int `json:"medium"`
			Low    int `json:"low"`
			Total  int `json:"total"`
		} `json:"summary"`
		Stats struct {
			Paragraphs   int `json:"paragraphs"`
			RulesChecked int `json:"rules_checked"`
			DurationMs   int `json:"duration_ms"`
		} `json:"stats"`
		Groups []struct {
			RuleID    string `json:"rule_id"`
			RuleTitle string `json:"rule_title"`
			Category  string `json:"category"`
			Severity  string `json:"severity"`
			Message   string `json:"message"`
			Count     int    `json:"count"`
			Candidate bool   `json:"candidate"`
			Samples   []struct {
				Where  string `json:"where"`
				Text   string `json:"text"`
				Detail string `json:"detail"`
			} `json:"samples"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("解析结果失败: %v\n原文: %s", err, textutil.Truncate(string(raw), 500))
	}

	t.Logf("检查了 %d 段正文，%d 条规则，耗时 %dms",
		report.Stats.Paragraphs, report.Stats.RulesChecked, report.Stats.DurationMs)
	t.Logf("汇总：high=%d medium=%d low=%d total=%d",
		report.Summary.High, report.Summary.Medium, report.Summary.Low, report.Summary.Total)

	for i, g := range report.Groups {
		if i >= 12 {
			break
		}
		t.Logf("  [%s] %s (%s) ×%d | %s",
			g.Severity, g.RuleTitle, g.RuleID, g.Count, g.Message)
	}

	if report.Summary.Total == 0 {
		t.Error("应该检出格式问题，实际 0 条")
	}

	// 隐私词必须检出
	foundPrivacy := false
	for _, g := range report.Groups {
		if g.Category == "privacy" {
			foundPrivacy = true
			break
		}
	}
	if !foundPrivacy {
		t.Error("应检出敏感词（公司名、人名），实际没有")
	}
}
