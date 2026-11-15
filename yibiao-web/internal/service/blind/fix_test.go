package blind

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patrick/yibiao-web/internal/service/openxml"
)

// TestFix 验证自动修复能力。
//
// 用带格式问题的 docx 跑修复，检查：
//   - 修复报告是否返回 fixes 明细
//   - 修复后复查的错误数是否下降
//   - 校验是否发现新错误（有的话说明修复把文件改坏了）
func TestFix(t *testing.T) {
	binary := os.Getenv("OPENXML_BINARY")
	docxPath := os.Getenv("BLIND_CHECK_DOCX")
	workDir := os.Getenv("BLIND_WORK_DIR")
	if binary == "" || docxPath == "" {
		t.Skip("未设置 OPENXML_BINARY / BLIND_CHECK_DOCX，跳过")
	}
	if workDir == "" {
		workDir = os.TempDir()
	}

	ctx := context.Background()
	oc, err := openxml.NewClient(ctx, openxml.Config{
		Binary:       binary,
		PoolSize:     1,
		JobTimeout:   2 * time.Minute,
		StartTimeout: 30 * time.Second,
	}, slog.Default())
	if err != nil {
		t.Fatalf("创建 openxml client 失败: %v", err)
	}
	defer oc.Close()

	svc := New(nil, oc, workDir, slog.Default())

	outPath := filepath.Join(workDir, "fixed-output.docx")
	_ = os.Remove(outPath)

	// 先检查，记下原始错误数
	before, err := svc.Inspect(ctx, InspectOptions{
		DocxPath:           docxPath,
		PresetID:           "builtin-general",
		UseCredentialWords: false,
	})
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	t.Logf("修复前：high=%d medium=%d total=%d",
		before.Summary.High, before.Summary.Medium, before.Summary.Total)

	// 跑修复（选常用的可修规则）
	report, err := svc.Fix(ctx, FixOptions{
		SourcePath: docxPath,
		OutputPath: outPath,
		PresetID:   "builtin-general",
		FixRules: []string{
			"page.paper_size", "page.margins",
			"font.chinese", "font.size",
			"para.line_spacing", "para.first_line_indent",
		},
		UseCredentialWords: false,
	})
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}

	// 输出报告明细
	raw, _ := json.MarshalIndent(map[string]any{
		"sourceBytes": report.SourceBytes,
		"outputBytes": report.OutputBytes,
		"fixes":       report.Fixes,
		"notes":       report.Notes,
		"validation":  report.Validation,
		"after":       report.After.Summary,
		"durationMs":  report.DurationMs,
	}, "", " ")
	t.Logf("修复报告:\n%s", string(raw))

	// 断言：产出了文件
	st, statErr := os.Stat(outPath)
	if statErr != nil {
		t.Fatalf("修复产物不存在: %v", statErr)
	}
	t.Logf("修复产物大小: %d 字节", st.Size())

	// 断言：有实际修复动作
	if len(report.Fixes) == 0 {
		t.Error("修复报告里没有任何 fixes 明细")
	}

	// 断言：校验没发现新错误
	if len(report.Validation.NewErrors) > 0 {
		t.Errorf("修复引入了 %d 个新错误（副本不该交付）: %v",
			len(report.Validation.NewErrors), report.Validation.NewErrors)
	}

	// 断言：修复后错误数下降
	after := report.After.Summary
	if after.Total >= before.Summary.Total {
		t.Errorf("修复后错误数没下降：修复前 %d，修复后 %d",
			before.Summary.Total, after.Total)
	}
	t.Logf("修复效果：%d → %d（减少 %d 处）",
		before.Summary.Total, after.Total, before.Summary.Total-after.Total)
}
