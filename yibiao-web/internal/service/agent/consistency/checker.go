// Package consistency 实现标书正文的一致性检查。
//
// 为什么这是 P1 最高优先级：
//
//	标书前后矛盾会直接导致废标。常见矛盾：
//	- 工期：第三章写「90 天」，第七章写「三个月完成」
//	- 人员：项目经理在不同章节有不同姓名
//	- 金额：报价前后不一致
//	- 指标：可用性在一处写 99.9%、另一处 99.99%
//
// **与桌面版的差异**：
//
//	桌面版是「逐节并发核对 → 事实台账 → 跨节比对 → 分配修复」的多轮流程，
//	配 4 个工具（search-sections / recheck / repair / complete）。
//
//	Web 版先做**单轮全量检查**：
//	  把全部正文按节喂给模型，一次抽出事实 + 找出矛盾。
//	理由：标书正文通常 5-20 万字，但一致性关键事实（人员/金额/工期/指标）
//	数量有限，一次全量比多轮台账更直接。规模真超限时再分片。
package consistency

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/textutil"
)

// 问题类型（对齐桌面版）。
const (
	// TypeFactConflict 与全局事实冲突
	TypeFactConflict = "与全局事实冲突"
	// TypeInternalContradiction 小节内部或跨小节矛盾
	TypeInternalContradiction = "正文前后矛盾"
)

// 严重程度。
const (
	SeverityHigh   = "high"   // 会导致废标或质疑
	SeverityMedium = "medium" // 影响专业度
	SeverityLow    = "low"    // 表述不统一
)

// Fact 是一条可核对事实。
type Fact struct {
	// Category 类别：人员 | 金额 | 工期 | 指标 | 数量 | 日期 | 编号 | 名称 | 其他
	Category string `json:"category"`
	// Subject 主体（如「项目经理」「系统可用性」）
	Subject string `json:"subject"`
	// Value 取值
	Value string `json:"value"`
	// SectionID 所在小节
	SectionID string `json:"sectionId"`
	// SectionTitle 小节标题（便于人阅读）
	SectionTitle string `json:"sectionTitle"`
	// Quote 原文片段（证据）
	Quote string `json:"quote,omitempty"`
}

// Issue 是一致性问题。
type Issue struct {
	// Type 问题类型
	Type string `json:"type"`
	// Severity 严重程度
	Severity string `json:"severity"`
	// Subject 涉及的主体
	Subject string `json:"subject"`
	// Description 问题描述
	Description string `json:"description"`
	// Evidence 证据（各处的原文）
	Evidence []Evidence `json:"evidence"`
	// Suggestion 建议统一成什么
	Suggestion string `json:"suggestion,omitempty"`
}

// Evidence 是一处证据。
type Evidence struct {
	SectionID    string `json:"sectionId"`
	SectionTitle string `json:"sectionTitle"`
	Quote        string `json:"quote"`
}

// Report 是检查报告。
type Report struct {
	// TaskID 检查任务 ID
	TaskID string `json:"taskId"`
	// Issues 发现的问题
	Issues []Issue `json:"issues"`
	// Facts 抽取的事实（供人工核对，也是下一轮修复的依据）
	Facts []Fact `json:"facts"`
	// CheckedSections 检查了多少节
	CheckedSections int `json:"checkedSections"`
	// TotalWords 总字数
	TotalWords int `json:"totalWords"`
	// HighCount / MediumCount / LowCount 分级统计
	HighCount   int `json:"highCount"`
	MediumCount int `json:"mediumCount"`
	LowCount    int `json:"lowCount"`
	// DurationMs 耗时
	DurationMs int64 `json:"durationMs"`
	// Note 补充说明（如正文过长被截断）
	Note string `json:"note,omitempty"`
}

// Section 是待检查的正文小节。
type Section struct {
	NodeID  string
	Title   string
	Level   int
	Content string
}

// Options 是检查参数。
type Options struct {
	TenantID  string
	ProjectID string
	// Sections 全部待检查小节（按目录顺序）
	Sections []Section
	// GlobalFacts 全局事实（人员、金额等已经确定的取值）
	GlobalFacts string
	Provider    agent.Provider
	Model       agent.ModelConfig
	// MaxWordsPerBatch 单批发给模型的最大字数（默认 60000）
	MaxWordsPerBatch int
}

// Runner 编排一致性检查。
type Runner struct {
	agentSvc *agent.Service
	log      *slog.Logger
}

// NewRunner 创建编排器。
func NewRunner(agentSvc *agent.Service, log *slog.Logger) *Runner {
	return &Runner{agentSvc: agentSvc, log: log}
}

// Check 执行一致性检查。
//
// 流程：
//  1. 按字数分批（避免超上下文）
//  2. 每批调模型抽取事实 + 找矛盾
//  3. 跨批合并（同一主体在不同批的取值差异也是矛盾）
func (r *Runner) Check(ctx context.Context, opts Options) (*Report, error) {
	if len(opts.Sections) == 0 {
		return nil, fmt.Errorf("没有待检查的正文")
	}

	start := time.Now()

	maxWords := opts.MaxWordsPerBatch
	if maxWords <= 0 {
		maxWords = 60000
	}

	batches := splitBatches(opts.Sections, maxWords)
	r.log.Info("开始一致性检查",
		slog.Int("sections", len(opts.Sections)),
		slog.Int("batches", len(batches)),
		slog.Int("max_words_per_batch", maxWords),
	)

	var (
		allFacts   []Fact
		allIssues  []Issue
		totalWords int
		notes      []string
	)

	for i, batch := range batches {
		result, err := r.checkBatch(ctx, opts, batch, i+1, len(batches))
		if err != nil {
			r.log.Warn("批次检查失败，跳过",
				slog.Int("batch", i+1),
				slog.String("error", err.Error()),
			)
			notes = append(notes, fmt.Sprintf("第 %d 批检查失败：%s", i+1, err.Error()))
			continue
		}
		allFacts = append(allFacts, result.Facts...)
		allIssues = append(allIssues, result.Issues...)
		for _, s := range batch {
			totalWords += countWords(s.Content)
		}
	}

	// 跨批矛盾：同一主体不同批取值不同
	if len(batches) > 1 {
		crossIssues := detectCrossBatchConflicts(allFacts)
		allIssues = append(allIssues, crossIssues...)
	}

	// 去重
	allIssues = dedupeIssues(allIssues)

	// 统计
	report := &Report{
		Issues:          allIssues,
		Facts:           allFacts,
		CheckedSections: len(opts.Sections),
		TotalWords:      totalWords,
		DurationMs:      time.Since(start).Milliseconds(),
	}
	for _, iss := range allIssues {
		switch iss.Severity {
		case SeverityHigh:
			report.HighCount++
		case SeverityMedium:
			report.MediumCount++
		default:
			report.LowCount++
		}
	}
	if len(notes) > 0 {
		report.Note = strings.Join(notes, "；")
	}

	r.log.Info("一致性检查完成",
		slog.Int("issues", len(allIssues)),
		slog.Int("facts", len(allFacts)),
		slog.Int("high", report.HighCount),
		slog.Int64("duration_ms", report.DurationMs),
	)

	return report, nil
}

// checkBatch 检查一批小节。
func (r *Runner) checkBatch(ctx context.Context, opts Options, batch []Section, index, total int) (*Report, error) {
	prompt := buildBatchPrompt(opts.GlobalFacts, batch, index, total)

	// 用 Agent 的通用任务接口跑一次纯文本分析
	// 注意：这里不需要工具，纯文本输入输出更稳定
	result, err := r.agentSvc.RunTextTask(ctx, agent.TextTaskSpec{
		TenantID:  opts.TenantID,
		ProjectID: opts.ProjectID,
		Provider:  opts.Provider,
		Model:     opts.Model,
		Prompt:    prompt,
		// 一致性检查是分析任务，不需要工作区
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		return nil, err
	}

	return parseCheckResult(result.Text, batch)
}

// splitBatches 按字数分批。
//
// 尽量保持同一批内小节数量均衡，且不切断小节。
func splitBatches(sections []Section, maxWords int) [][]Section {
	var out [][]Section
	var cur []Section
	curWords := 0

	for _, s := range sections {
		w := countWords(s.Content)
		// 单节就超限时独占一批
		if w > maxWords {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
				curWords = 0
			}
			out = append(out, []Section{s})
			continue
		}
		if curWords+w > maxWords && len(cur) > 0 {
			out = append(out, cur)
			cur = nil
			curWords = 0
		}
		cur = append(cur, s)
		curWords += w
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// detectCrossBatchConflicts 检测跨批矛盾。
//
// 同一 category+subject 在不同小节出现不同 value → 矛盾。
// 这是本地能算出来的，不需要再调模型。
func detectCrossBatchConflicts(facts []Fact) []Issue {
	// 按 category+subject 分组
	groups := make(map[string][]Fact)
	for _, f := range facts {
		if f.Subject == "" || f.Value == "" {
			continue
		}
		key := f.Category + "|" + normalizeSubject(f.Subject)
		groups[key] = append(groups[key], f)
	}

	var issues []Issue
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		// 按归一化后的值分组
		byValue := make(map[string][]Fact)
		for _, f := range group {
			v := normalizeValue(f.Value)
			byValue[v] = append(byValue[v], f)
		}
		if len(byValue) < 2 {
			continue // 值都一样，没矛盾
		}

		// 找出出现次数最多的值作为「建议统一值」
		var counts []valueCount
		for v, fs := range byValue {
			counts = append(counts, valueCount{value: v, facts: fs})
		}
		sort.Slice(counts, func(i, j int) bool {
			return len(counts[i].facts) > len(counts[j].facts)
		})

		// 构造证据（每个不同取值各取一条）
		var evidence []Evidence
		for _, c := range counts {
			f := c.facts[0]
			evidence = append(evidence, Evidence{
				SectionID:    f.SectionID,
				SectionTitle: f.SectionTitle,
				Quote:        f.Quote,
			})
		}

		subject := group[0].Subject
		issues = append(issues, Issue{
			Type:     TypeInternalContradiction,
			Severity: severityForCategory(group[0].Category, len(counts)),
			Subject:  subject,
			Description: fmt.Sprintf("「%s」在不同章节取值不一致（%s）",
				subject, joinValues(counts)),
			Evidence:   evidence,
			Suggestion: fmt.Sprintf("建议统一为「%s」（出现次数最多）", counts[0].value),
		})
	}

	return issues
}

// valueCount 是一个取值及其出现位置。
type valueCount struct {
	value string
	facts []Fact
}

// joinValues 把不同取值拼成可读文本。
func joinValues(counts []valueCount) string {
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s ×%d", c.value, len(c.facts)))
	}
	return strings.Join(parts, "、")
}

// severityForCategory 按类别判断严重程度。
//
// 人员、金额、工期、编号不一致 → high（容易废标或质疑）
// 指标、数量 → medium
// 其他 → low
func severityForCategory(category string, valueCount int) string {
	switch category {
	case "人员", "金额", "工期", "编号", "名称":
		return SeverityHigh
	case "指标", "数量", "日期":
		return SeverityMedium
	default:
		if valueCount >= 3 {
			return SeverityMedium
		}
		return SeverityLow
	}
}

// normalizeSubject 归一化主体名。
//
// 为什么需要：模型对同一概念可能抽出不同表述，
// 如「服务模块数量」和「微服务模块数量」——指的是同一件事。
// 不归一化会报成两条独立问题，用户看到重复会觉得不专业。
//
// 策略：去掉空格 + 去掉常见修饰前缀 + 统一数量类后缀。
func normalizeSubject(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	s = strings.ToLower(s)

	// 去掉「微」「整体」「总」这类修饰词（它们不改变所指）
	for _, prefix := range []string{"微服务", "微服务架构", "整体的", "整体", "总的", "全部"} {
		s = strings.Replace(s, prefix, "", 1)
	}

	// 统一数量类表述
	replacer := strings.NewReplacer(
		"数量", "", "数", "", "个数", "", "数目", "",
		"数量指标", "", "的总", "",
	)
	s = replacer.Replace(s)

	// 统一「工期」「周期」「期限」
	s = strings.Replace(s, "周期", "工期", 1)
	s = strings.Replace(s, "期限", "工期", 1)

	return s
}

// subjectSimilar 判断两个 subject 是否指同一事物。
//
// 用于去重时的模糊匹配：任一方包含另一方主体部分即视为相关。
func subjectSimilar(a, b string) bool {
	if a == b {
		return true
	}
	// 长度接近且互相包含时，视为同一主体
	if len(a) >= 3 && len(b) >= 3 {
		if strings.Contains(a, b) || strings.Contains(b, a) {
			return true
		}
	}
	return false
}

// normalizeValue 归一化取值。
//
// 数字去掉千分位、单位统一；文本去空格。
func normalizeValue(v string) string {
	v = strings.TrimSpace(v)
	v = strings.ReplaceAll(v, ",", "")
	v = strings.ReplaceAll(v, "，", "")
	v = strings.ReplaceAll(v, " ", "")
	// 「90天」和「90 天」视为同一个
	return v
}

// dedupeIssues 去重。
//
// 同一 subject 的同类问题只保留证据最全的一条。
// 用模糊匹配，因为模型对同一概念可能给出不同表述。
func dedupeIssues(issues []Issue) []Issue {
	out := make([]Issue, 0, len(issues))

	for _, iss := range issues {
		norm := normalizeSubject(iss.Subject)

		// 在已有结果里找相似主体
		merged := false
		for i := range out {
			if out[i].Type != iss.Type {
				continue
			}
			if subjectSimilar(normalizeSubject(out[i].Subject), norm) {
				// 合并证据
				out[i].Evidence = mergeEvidence(out[i].Evidence, iss.Evidence)
				// 保留更完整的描述和建议
				if len(iss.Description) > len(out[i].Description) {
					out[i].Description = iss.Description
				}
				if iss.Suggestion != "" && out[i].Suggestion == "" {
					out[i].Suggestion = iss.Suggestion
				}
				// 严重程度取更高
				if severityRank(iss.Severity) < severityRank(out[i].Severity) {
					out[i].Severity = iss.Severity
				}
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, iss)
		}
	}

	// 按严重程度排序
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if oi != oj {
			return oi < oj
		}
		return len(out[i].Evidence) > len(out[j].Evidence)
	})

	return out
}

// severityRank 严重程度排序权重（越小越严重）。
func severityRank(s string) int {
	switch s {
	case SeverityHigh:
		return 0
	case SeverityMedium:
		return 1
	default:
		return 2
	}
}

// mergeEvidence 合并证据，按 sectionID+quote 去重。
func mergeEvidence(base, extra []Evidence) []Evidence {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]Evidence, 0, len(base)+len(extra))

	for _, e := range append(base, extra...) {
		key := e.SectionID + "|" + e.Quote
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// countWords 统计字数（复用标书口径的简化版）。
func countWords(s string) int {
	n := 0
	for _, r := range s {
		// 只数非空白字符，与标书字数口径足够接近
		if r != ' ' && r != '\n' && r != '\t' && r != '\r' {
			n++
		}
	}
	return n
}

// parseCheckResult 解析模型返回的 JSON。
func parseCheckResult(text string, batch []Section) (*Report, error) {
	cleaned := cleanJSON(text)

	var raw struct {
		Facts []struct {
			Category     string `json:"category"`
			Subject      string `json:"subject"`
			Value        string `json:"value"`
			SectionID    string `json:"sectionId"`
			SectionTitle string `json:"sectionTitle"`
			Quote        string `json:"quote"`
		} `json:"facts"`
		Issues []struct {
			Type        string `json:"type"`
			Severity    string `json:"severity"`
			Subject     string `json:"subject"`
			Description string `json:"description"`
			Evidence    []struct {
				SectionID    string `json:"sectionId"`
				SectionTitle string `json:"sectionTitle"`
				Quote        string `json:"quote"`
			} `json:"evidence"`
			Suggestion string `json:"suggestion"`
		} `json:"issues"`
	}

	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return nil, fmt.Errorf("解析检查结果失败: %w（原文前 300 字：%s）", err, textutil.Truncate(text, 300))
	}

	// 补全 sectionTitle（模型可能漏）
	titleByID := make(map[string]string, len(batch))
	for _, s := range batch {
		titleByID[s.NodeID] = s.Title
	}

	out := &Report{}

	for _, f := range raw.Facts {
		fact := Fact{
			Category:     normalizeCategory(f.Category),
			Subject:      strings.TrimSpace(f.Subject),
			Value:        strings.TrimSpace(f.Value),
			SectionID:    f.SectionID,
			SectionTitle: f.SectionTitle,
			Quote:        strings.TrimSpace(f.Quote),
		}
		if fact.SectionTitle == "" {
			fact.SectionTitle = titleByID[fact.SectionID]
		}
		if fact.Subject == "" || fact.Value == "" {
			continue
		}
		out.Facts = append(out.Facts, fact)
	}

	for _, i := range raw.Issues {
		iss := Issue{
			Type:        normalizeIssueType(i.Type),
			Severity:    normalizeSeverity(i.Severity),
			Subject:     strings.TrimSpace(i.Subject),
			Description: strings.TrimSpace(i.Description),
			Suggestion:  strings.TrimSpace(i.Suggestion),
		}
		for _, e := range i.Evidence {
			ev := Evidence{
				SectionID:    e.SectionID,
				SectionTitle: e.SectionTitle,
				Quote:        strings.TrimSpace(e.Quote),
			}
			if ev.SectionTitle == "" {
				ev.SectionTitle = titleByID[ev.SectionID]
			}
			iss.Evidence = append(iss.Evidence, ev)
		}
		if iss.Description == "" {
			continue
		}
		out.Issues = append(out.Issues, iss)
	}

	return out, nil
}

// normalizeCategory 规范化类别。
func normalizeCategory(c string) string {
	c = strings.TrimSpace(c)
	switch c {
	case "人员", "金额", "工期", "指标", "数量", "日期", "编号", "名称":
		return c
	}
	// 常见别名
	switch {
	case strings.Contains(c, "人"), strings.Contains(c, "岗位"):
		return "人员"
	case strings.Contains(c, "价"), strings.Contains(c, "费"), strings.Contains(c, "金额"):
		return "金额"
	case strings.Contains(c, "期"), strings.Contains(c, "时间"):
		return "工期"
	case strings.Contains(c, "指标"), strings.Contains(c, "SLA"), strings.Contains(c, "可用性"):
		return "指标"
	default:
		return "其他"
	}
}

// normalizeIssueType 规范化问题类型。
func normalizeIssueType(t string) string {
	if strings.Contains(t, "全局") {
		return TypeFactConflict
	}
	return TypeInternalContradiction
}

// normalizeSeverity 规范化严重程度。
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high", "高", "严重":
		return SeverityHigh
	case "medium", "中", "一般":
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// cleanJSON 清理可能被代码块包裹的 JSON。
func cleanJSON(s string) string {
	return textutil.StripCodeFence(s)
}
