// Package blind 实现暗标合规检查。
//
// 解决的问题：投标文件是否泄露投标人身份、格式是否符合暗标要求。
//
// **架构要点**：格式检查引擎（64 条规则、9,922 行 C#）已存在于
// openxmlhelper 里，Go 侧只负责：
//
//  1. 组装 profile（预设 + 自定义）
//  2. 组装敏感词（用户手填 + 资信库）
//  3. 调 helper 的 blind-check-inspect
//  4. 把 groups 结果落库
//
// 不重写规则引擎 —— 那 9,922 行里有大量 OOXML 细节
// （样式继承链、编号格式、图片格式识别），重写等于重犯所有坑。
package blind

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/patrick/yibiao-web/internal/service/openxml"
	"github.com/patrick/yibiao-web/internal/store"
)

// 检查状态。
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusError   = "error"
)

// 结果文件名（helper 输出的）。
const resultFileName = "blind-check-result.json"

// Service 编排暗标检查。
type Service struct {
	q       *store.Queries
	openxml *openxml.Client
	workDir string
	log     *slog.Logger
}

// New 创建服务。
func New(q *store.Queries, oc *openxml.Client, workDir string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: q, openxml: oc, workDir: workDir, log: log}
}

// InspectOptions 是检查参数。
type InspectOptions struct {
	TenantID  string
	ProjectID string
	// DocxPath 待检 docx 的本地路径
	DocxPath string
	// PresetID 格式预设
	PresetID string
	// CustomProfile 自定义格式规范（覆盖预设）
	CustomProfile map[string]any
	// ManualWords 用户手填敏感词
	ManualWords string
	// CredentialWords 资信库里的公司名/人名
	CredentialWords []string
	// UseCredentialWords 是否用资信库
	UseCredentialWords bool
}

// Report 是 helper 返回的检查报告（对齐 C# 输出结构）。
type Report struct {
	Version int `json:"version"`
	Summary struct {
		High   int `json:"high"`
		Medium int `json:"medium"`
		Low    int `json:"low"`
		Total  int `json:"total"`
	} `json:"summary"`
	Stats struct {
		Paragraphs      int            `json:"paragraphs"`
		BodyParagraphs  int            `json:"body_paragraphs"`
		TableParagraphs int            `json:"table_paragraphs"`
		Tables          int            `json:"tables"`
		Sections        int            `json:"sections"`
		Headings        int            `json:"headings"`
		Images          int            `json:"images"`
		RulesChecked    int            `json:"rules_checked"`
		DurationMs      int            `json:"duration_ms"`
		Extra           map[string]any `json:"-"`
	} `json:"stats"`
	Groups []Group `json:"groups"`
	// RuleErrors 部分规则执行失败（文档结构异常时）
	RuleErrors []string `json:"rule_errors"`
}

// Group 是按规则聚合的发现。
type Group struct {
	RuleID    string   `json:"rule_id"`
	RuleTitle string   `json:"rule_title"`
	Category  string   `json:"category"`
	Severity  string   `json:"severity"`
	Message   string   `json:"message"`
	Count     int      `json:"count"`
	Candidate bool     `json:"candidate"`
	Samples   []Sample `json:"samples"`
	Items     []Item   `json:"items"`
}

// Sample 是样本（给用户看）。
type Sample struct {
	Where  string `json:"where"`
	Text   string `json:"text"`
	Detail string `json:"detail"`
	Origin string `json:"origin"`
	Hint   string `json:"hint"`
}

// Item 是定位条目（带 locator，供修复用）。
type Item struct {
	Where   string `json:"where"`
	Text    string `json:"text"`
	Detail  string `json:"detail"`
	Locator string `json:"locator"`
}

// Inspect 执行暗标检查。
func (s *Service) Inspect(ctx context.Context, opts InspectOptions) (*Report, error) {
	if s.openxml == nil {
		return nil, fmt.Errorf("Word 处理服务未启用，无法执行暗标检查")
	}

	// 组装 profile
	profile, presetName, err := ResolveProfile(opts.PresetID, opts.CustomProfile)
	if err != nil {
		return nil, err
	}

	// 组装敏感词
	words := BuildSensitiveWords(opts.ManualWords, opts.CredentialWords, opts.UseCredentialWords)

	s.log.Info("开始暗标检查",
		slog.String("preset", presetName),
		slog.Int("sensitive_words", len(words)),
	)

	// 结果文件落到临时位置
	outLocal := filepath.Join(os.TempDir(), fmt.Sprintf("blind-result-%d.json", time.Now().UnixNano()))
	defer os.Remove(outLocal)

	remoteName := "blind-check-source.docx"

	result, err := s.openxml.ExecuteWithFiles(ctx, map[string]any{
		"action":          "blind-check-inspect",
		"source":          remoteName,
		"profile":         profile,
		"sensitive_words": words,
	}, openxml.ExecuteOptions{
		Files: map[string]string{
			remoteName: opts.DocxPath,
		},
		// 结果写在 job 子目录，用 CollectJobFiles 取
		CollectJobFiles: map[string]string{
			resultFileName: outLocal,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("暗标检查失败: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("暗标检查失败: %s", result.Error)
	}

	raw, err := os.ReadFile(outLocal)
	if err != nil {
		return nil, fmt.Errorf("读取检查结果失败: %w", err)
	}

	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("解析检查结果失败: %w", err)
	}

	s.log.Info("暗标检查完成",
		slog.Int("total", report.Summary.Total),
		slog.Int("high", report.Summary.High),
		slog.Int("medium", report.Summary.Medium),
		slog.Int("rules_checked", report.Stats.RulesChecked),
		slog.Int("duration_ms", report.Stats.DurationMs),
	)

	return &report, nil
}

// SaveReport 把检查结果落库。
func (s *Service) SaveReport(ctx context.Context, resultID uuid.UUID, report *Report) error {
	// 清旧发现（同一 result 重跑）
	_ = s.q.DeleteBlindFindings(ctx, resultID)

	// 按严重程度排序：high → medium → low
	order := map[string]int{"high": 0, "medium": 1, "low": 2}
	groups := make([]Group, len(report.Groups))
	copy(groups, report.Groups)
	for i := 0; i < len(groups); i++ {
		for j := i + 1; j < len(groups); j++ {
			oi, oj := order[groups[i].Severity], order[groups[j].Severity]
			if oj < oi {
				groups[i], groups[j] = groups[j], groups[i]
			}
		}
	}

	for i, g := range groups {
		samplesJSON, _ := json.Marshal(g.Samples)
		itemsJSON, _ := json.Marshal(g.Items)

		_, err := s.q.InsertBlindFinding(ctx, store.InsertBlindFindingParams{
			ResultID:  resultID,
			RuleID:    g.RuleID,
			RuleTitle: g.RuleTitle,
			Category:  g.Category,
			Severity:  g.Severity,
			Message:   g.Message,
			Count:     int32(g.Count),
			Candidate: g.Candidate,
			Samples:   samplesJSON,
			Items:     itemsJSON,
			SortOrder: int32(i),
		})
		if err != nil {
			return fmt.Errorf("保存发现失败: %w", err)
		}
	}

	// 更新汇总
	summaryJSON, _ := json.Marshal(report.Summary)
	statsJSON, _ := json.Marshal(report.Stats)
	ruleErrorsJSON, _ := json.Marshal(report.RuleErrors)

	return s.q.FinishBlindResult(ctx, store.FinishBlindResultParams{
		ID:         resultID,
		Summary:    summaryJSON,
		Stats:      statsJSON,
		RuleErrors: ruleErrorsJSON,
	})
}

// InputSignature 计算输入签名（前端用来判断结果是否过期）。
//
// 签名包含：文件名 + 文件大小 + 预设 ID + 敏感词。
// 任何一项变了，旧结果就不该继续显示。
func InputSignature(fileName string, fileSize int64, presetID, manualWords string, useCredential bool) string {
	credFlag := "0"
	if useCredential {
		credFlag = "1"
	}
	return fmt.Sprintf("%s|%d|%s|%s|%s", fileName, fileSize, presetID, manualWords, credFlag)
}

// ============ 自动修复 ============

// FixOptions 是修复参数。
type FixOptions struct {
	TenantID  string
	ProjectID string
	// SourcePath 原 docx
	SourcePath string
	// OutputPath 修复副本的本地保存路径
	OutputPath string
	// PresetID 格式预设（与检查时一致）
	PresetID string
	// CustomProfile 自定义规范
	CustomProfile map[string]any
	// ManualWords 用户手填敏感词
	ManualWords string
	// CredentialWords 资信库敏感词
	CredentialWords    []string
	UseCredentialWords bool
	// FixRules 勾选要修的规则 ID
	FixRules []string
}

// FixReport 是 helper 返回的修复报告（对齐 C# BlindCheckFixReport）。
type FixReport struct {
	Version     int            `json:"version"`
	Output      string         `json:"output"`
	SourceBytes int64          `json:"source_bytes"`
	OutputBytes int64          `json:"output_bytes"`
	Fixes       map[string]int `json:"fixes"`
	Notes       []string       `json:"notes"`
	Validation  struct {
		Checked        bool     `json:"checked"`
		SkippedReason  string   `json:"skipped_reason"`
		OriginalErrors int      `json:"original_errors"`
		FixedErrors    int      `json:"fixed_errors"`
		NewErrors      []string `json:"new_errors"`
	} `json:"validation"`
	DurationMs      int  `json:"duration_ms"`
	HeadingFallback bool `json:"heading_fallback"`
	// After 是修复后复查的报告
	After Report `json:"after"`
}

// Fix 执行自动修复。
//
// **关键机制（C# 侧实现）**：
//  1. 只做最小改动，不改动无关内容
//  2. 修复后用 Open XML SDK 对比校验原文件和副本，
//     只有副本**多出原文件没有的错误**才算修复引入的问题
//  3. 校验发现新错误时**不交付副本**（宁可失败也不给坏文件）
func (s *Service) Fix(ctx context.Context, opts FixOptions) (*FixReport, error) {
	if s.openxml == nil {
		return nil, fmt.Errorf("Word 处理服务未启用，无法执行修复")
	}

	profile, presetName, err := ResolveProfile(opts.PresetID, opts.CustomProfile)
	if err != nil {
		return nil, err
	}

	words := BuildSensitiveWords(opts.ManualWords, opts.CredentialWords, opts.UseCredentialWords)

	if len(opts.FixRules) == 0 {
		return nil, fmt.Errorf("请至少勾选一项要修复的内容")
	}

	s.log.Info("开始自动修复",
		slog.String("preset", presetName),
		slog.Int("rules", len(opts.FixRules)),
	)

	outLocal := filepath.Join(os.TempDir(), fmt.Sprintf("blind-fix-%d.json", time.Now().UnixNano()))
	defer os.Remove(outLocal)

	const (
		remoteSource = "blind-fix-source.docx"
		remoteOutput = "blind-fix-output.docx"
		reportFile   = "blind-check-fix-result.json"
	)

	result, err := s.openxml.ExecuteWithFiles(ctx, map[string]any{
		"action":          "blind-check-fix",
		"source":          remoteSource,
		"output":          remoteOutput,
		"profile":         profile,
		"sensitive_words": words,
		"fix_rules":       opts.FixRules,
	}, openxml.ExecuteOptions{
		Files: map[string]string{
			remoteSource: opts.SourcePath,
		},
		// 修复后的 docx 在工作区根目录（helper 按 output 路径写）
		CollectBack: map[string]string{
			remoteOutput: opts.OutputPath,
		},
		// 修复报告在 job 目录
		CollectJobFiles: map[string]string{
			reportFile: outLocal,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("修复失败: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("修复失败: %s", result.Error)
	}

	raw, err := os.ReadFile(outLocal)
	if err != nil {
		return nil, fmt.Errorf("读取修复报告失败: %w", err)
	}

	var report FixReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("解析修复报告失败: %w", err)
	}

	s.log.Info("自动修复完成",
		slog.Int("fixes", len(report.Fixes)),
		slog.Int("original_errors", report.Validation.OriginalErrors),
		slog.Int("fixed_errors", report.Validation.FixedErrors),
		slog.Int("new_errors", len(report.Validation.NewErrors)),
		slog.Int64("output_bytes", report.OutputBytes),
	)

	return &report, nil
}

// FailedFixValidation 判断校验是否发现修复引入的新错误。
//
// 有 new_errors 说明修复把文件改坏了 —— 这种副本不能交付给用户。
func FailedFixValidation(r *FixReport) bool {
	return r != nil && len(r.Validation.NewErrors) > 0
}
