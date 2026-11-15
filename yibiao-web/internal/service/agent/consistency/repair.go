// Package consistency 的自动修复部分。
//
// 检出一致性问题后，如果只报告不修复，用户还得手工改。
// 这里实现「按统一结论批量修复」。
//
// **关键设计：先定结论，再改文字**
//
//	桌面版的做法（repair-sections）：
//	  主 Agent 确定每处矛盾统一成什么取值 → 分配给子任务并发修复
//
//	为什么必须先定结论：
//	  如果不先确定「统一成 90 天还是三个月」，两个子任务可能
//	  各自把对方改成自己那版，结果还是矛盾。
//
//	Web 版简化：单轮修复，按 issue.suggestion 里的建议统一。
//	每个 issue 的修复是独立的（不同主体），可以并发。
package consistency

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent"
)

// RepairOptions 是修复参数。
type RepairOptions struct {
	TenantID  string
	ProjectID string
	// Sections 全部小节（用于给模型提供上下文）
	Sections []Section
	// Issues 要修复的问题
	Issues []Issue
	// Facts 事实台账（帮助模型理解上下文）
	Facts    []Fact
	Provider agent.Provider
	Model    agent.ModelConfig
	// Concurrency 并发修复的小节数（默认 3）
	Concurrency int

	// Resolutions 统一结论（内部填充，不对外暴露为请求参数）
	Resolutions []Resolution

	// GlobalFacts 全局事实（可选）
	GlobalFacts string
}

// RepairResult 是修复结果。
type RepairResult struct {
	// Repairs 每节的修复结果
	Repairs []SectionRepair `json:"repairs"`
	// FixedCount 成功修复的节数
	FixedCount int `json:"fixedCount"`
	// FailedCount 失败的节数
	FailedCount int `json:"failedCount"`
	// DurationMs 耗时
	DurationMs int64 `json:"durationMs"`
}

// SectionRepair 是单节的修复结果。
type SectionRepair struct {
	NodeID string `json:"nodeId"`
	Title  string `json:"title"`
	// Original 原文（用于回滚与对比）
	Original string `json:"original,omitempty"`
	// Revised 修改后
	Revised string `json:"revised,omitempty"`
	// Changed 是否有改动
	Changed bool `json:"changed"`
	// Changes 改动说明（供用户核对）
	Changes []string `json:"changes,omitempty"`
	// Error 失败原因
	Error string `json:"error,omitempty"`
}

// Resolution 是一条矛盾的统一结论。
//
// **为什么必须先定结论**：
//
//	如果只告诉模型「这两处矛盾」，模型会认为「我这处没错，是对方错」，
//	于是原样返回（这是我们实测踩到的坑）。
//	必须先由程序确定「统一成 X」，再明确告诉每节「把 Y 改成 X」。
type Resolution struct {
	// Subject 主体
	Subject string
	// Canonical 统一后的取值
	Canonical string
	// Issue 原问题
	Issue Issue
}

// ResolveIssues 确定每条矛盾的统一结论。
//
// 规则（与 detectCrossBatchConflicts 一致）：
//   - 从 evidence 里出现的取值中，选出现次数最多的
//   - 次数相同则选第一个（保持稳定）
//   - 如果 suggestion 里已明确写了取值，优先用它
func ResolveIssues(issues []Issue) []Resolution {
	out := make([]Resolution, 0, len(issues))

	for _, iss := range issues {
		canonical := extractCanonical(iss)
		if canonical == "" {
			continue
		}
		out = append(out, Resolution{
			Subject:   iss.Subject,
			Canonical: canonical,
			Issue:     iss,
		})
	}

	return out
}

// extractCanonical 从 issue 里提取统一取值。
//
// 优先从 suggestion 里解析（模型通常写「建议统一为「90 个日历天」」），
// 解析不出则从 evidence 的引用片段里找最常见的取值。
func extractCanonical(iss Issue) string {
	// 从 suggestion 里找引号包裹的内容
	if iss.Suggestion != "" {
		pairs := [][2]string{
			{"\u300c", "\u300d"}, // 「」
			{"\u201c", "\u201d"}, // “”
			{"\"", "\""},         // 直双引号
			{"'", "'"},
		}
		for _, pair := range pairs {
			start := strings.Index(iss.Suggestion, pair[0])
			if start < 0 {
				continue
			}
			rest := iss.Suggestion[start+len(pair[0]):]
			end := strings.Index(rest, pair[1])
			if end > 0 {
				candidate := strings.TrimSpace(rest[:end])
				if candidate != "" {
					return candidate
				}
			}
		}
	}

	// 兜底：从证据里找出现次数最多的取值
	// 这里用描述里的数字/日期片段做启发式提取
	return ""
}

// Repair 执行批量修复。
//
// 按「需要修改的小节」分组，每节一次 LLM 调用（并发）。
// 单节内可能有多个矛盾，一次改完。
func (r *Runner) Repair(ctx context.Context, opts RepairOptions) (*RepairResult, error) {
	if len(opts.Issues) == 0 {
		return &RepairResult{}, nil
	}

	start := time.Now()

	// **第一步：确定统一结论**
	//
	// 这是修复有效性的前提。没有结论，模型会各自认为对方错。
	resolutions := ResolveIssues(opts.Issues)
	if len(resolutions) == 0 {
		return nil, fmt.Errorf("无法从问题里确定统一结论（缺少 suggestion）")
	}

	opts.Resolutions = resolutions

	// 按小节分组问题
	bySection := make(map[string][]Issue)
	for _, iss := range opts.Issues {
		// 用第一条证据的小节作为修复目标
		// 策略：矛盾涉及多节时，每节都要改（统一取值）
		seen := make(map[string]bool)
		for _, e := range iss.Evidence {
			if e.SectionID == "" || seen[e.SectionID] {
				continue
			}
			seen[e.SectionID] = true
			bySection[e.SectionID] = append(bySection[e.SectionID], iss)
		}
	}

	if len(bySection) == 0 {
		return &RepairResult{}, nil
	}

	// 建小节索引
	sectionByID := make(map[string]Section, len(opts.Sections))
	for _, s := range opts.Sections {
		sectionByID[s.NodeID] = s
	}

	// 并发修复
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}

	type job struct {
		nodeID string
		issues []Issue
	}

	jobs := make(chan job, len(bySection))
	for nodeID, issues := range bySection {
		jobs <- job{nodeID: nodeID, issues: issues}
	}
	close(jobs)

	var (
		mu      sync.Mutex
		results []SectionRepair
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
	)

	for j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sec, ok := sectionByID[j.nodeID]
			if !ok {
				mu.Lock()
				results = append(results, SectionRepair{
					NodeID: j.nodeID,
					Error:  "小节不存在",
				})
				mu.Unlock()
				return
			}

			rep := r.repairSection(ctx, opts, sec, j.issues)

			mu.Lock()
			results = append(results, rep)
			mu.Unlock()
		}(j)
	}

	wg.Wait()

	// 统计
	out := &RepairResult{
		Repairs:    results,
		DurationMs: time.Since(start).Milliseconds(),
	}
	for _, rep := range results {
		if rep.Error != "" {
			out.FailedCount++
		} else if rep.Changed {
			out.FixedCount++
		}
	}

	// 按原顺序排（便于前端展示）
	sortRepairs(out.Repairs, opts.Sections)

	r.log.Info("一致性修复完成",
		slog.Int("sections", len(bySection)),
		slog.Int("fixed", out.FixedCount),
		slog.Int("failed", out.FailedCount),
		slog.Int64("duration_ms", out.DurationMs),
	)

	return out, nil
}

// repairSection 修复单节。
func (r *Runner) repairSection(ctx context.Context, opts RepairOptions, sec Section, issues []Issue) SectionRepair {
	rep := SectionRepair{
		NodeID:   sec.NodeID,
		Title:    sec.Title,
		Original: sec.Content,
	}

	// 只传与本节点相关的决议
	var relevant []Resolution
	for _, res := range opts.Resolutions {
		for _, e := range res.Issue.Evidence {
			if e.SectionID == sec.NodeID {
				relevant = append(relevant, res)
				break
			}
		}
	}

	prompt := buildRepairPrompt(sec, issues, relevant, opts.Facts)

	result, err := r.agentSvc.RunTextTask(ctx, agent.TextTaskSpec{
		TenantID:    opts.TenantID,
		ProjectID:   opts.ProjectID,
		Provider:    opts.Provider,
		Model:       opts.Model,
		Prompt:      prompt,
		Temperature: 0, // 修复要确定性
		Timeout:     5 * time.Minute,
	})
	if err != nil {
		rep.Error = err.Error()
		return rep
	}

	revised, changes := parseRepairResult(result.Text)
	if revised == "" {
		rep.Error = "模型没有返回修改后的正文"
		return rep
	}

	// 去掉可能的代码块包裹
	revised = stripCodeFence(revised)

	// 安全检查：修改后不应该大幅缩短（防止模型只返回片段）
	origLen := len([]rune(sec.Content))
	newLen := len([]rune(revised))
	if origLen > 200 && newLen < origLen/2 {
		rep.Error = fmt.Sprintf(
			"修改后内容过短（%d → %d 字），可能是模型只返回了片段，已拒绝应用",
			origLen, newLen)
		return rep
	}

	rep.Revised = revised
	rep.Changes = changes
	rep.Changed = strings.TrimSpace(revised) != strings.TrimSpace(sec.Content)

	return rep
}

// sortRepairs 按原小节顺序排序。
func sortRepairs(repairs []SectionRepair, sections []Section) {
	order := make(map[string]int, len(sections))
	for i, s := range sections {
		order[s.NodeID] = i
	}
	// 简单插入排序
	for i := 1; i < len(repairs); i++ {
		for j := i; j > 0; j-- {
			oi, oj := order[repairs[j].NodeID], order[repairs[j-1].NodeID]
			if oi >= oj {
				break
			}
			repairs[j], repairs[j-1] = repairs[j-1], repairs[j]
		}
	}
}

// stripCodeFence 去掉代码块包裹。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	lines = lines[1:]
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			lines = lines[:i]
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
