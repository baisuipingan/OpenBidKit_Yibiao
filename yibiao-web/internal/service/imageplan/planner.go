// Package imageplan 实现配图编排。
//
// 解决的问题：
//
//	没有编排时，Agent 想配几张就配几张 —— 有的章节配 5 张、
//	有的关键章节一张没有，全文配图分布失衡。
//	桌面版的做法是「主 Agent 先分配全局名额，再交给各节执行」。
//
// **Web 版的编排策略**：
//
//	不让 LLM 做分配（不稳定、不可复现），改用**确定性算法**：
//	  1. 按章节层级和标题关键词算「配图适配分」
//	  2. 按分数排序，把名额分给最需要的章节
//	  3. 每节拿到明确的配图指令（布局 + 生成方式 + 数量）
//
// 为什么用算法而不是 LLM：
//   - 可复现：同样的目录永远得到同样的分配
//   - 可解释：能说清「为什么这节分了 2 张」
//   - 零成本：不消耗 token
//   - LLM 的优势在内容理解，配图分配是规划问题，算法更合适
package imageplan

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// 布局类型（对齐桌面版）。
const (
	// LayoutSingle 单张图片，图片本身表达意图
	LayoutSingle = "single"
	// LayoutImageText 左图右文，右侧文字解释左侧图片
	LayoutImageText = "imageText"
	// LayoutThreeImages 三列图片，围绕共同主题
	LayoutThreeImages = "threeImages"
	// LayoutFourImages 四宫格
	LayoutFourImages = "fourImages"
)

// 生成方式（对齐 contentImageKinds.cjs）。
const (
	KindAI      = "aiImage"
	KindHTML    = "htmlImage"
	KindMermaid = "mermaid"
)

// layoutImages 每种布局的图片张数。
var layoutImages = map[string]int{
	LayoutSingle:      1,
	LayoutImageText:   1,
	LayoutThreeImages: 3,
	LayoutFourImages:  4,
}

// Options 是编排参数。
type Options struct {
	// Sections 全部待编排的正文小节
	Sections []Section
	// TotalImages 期望的配图总数上限。
	// 0 表示按章节数自动估算（约每 3 节 1 张）。
	TotalImages int
	// EnabledKinds 可用的生成方式。空表示三种都可用。
	EnabledKinds []string
	// AIPercent AI 图片的目标占比（0-100）。
	// 只在 KindAI 启用时生效。
	AIPercent int
	// MaxPerSection 单节最多几张（默认 4）
	MaxPerSection int
}

// Section 是一个待编排的小节。
type Section struct {
	NodeID      string
	Title       string
	Level       int
	Description string
	TargetWords int
}

// Plan 是编排结果。
type Plan struct {
	// Allocations 每节的配图指令
	Allocations []Allocation `json:"allocations"`
	// TotalImages 实际分配的图片总数
	TotalImages int `json:"totalImages"`
	// TotalGroups 实际分配的组数
	TotalGroups int `json:"totalGroups"`
	// LayoutCounts 各布局的组数
	LayoutCounts map[string]int `json:"layoutCounts"`
	// KindCounts 各生成方式的图片数
	KindCounts map[string]int `json:"kindCounts"`
	// Note 说明
	Note string `json:"note,omitempty"`
}

// Allocation 是一节的配图指令。
type Allocation struct {
	NodeID string `json:"nodeId"`
	Title  string `json:"title"`
	// Score 适配评分（0-10），用于解释分配依据
	Score float64 `json:"score"`
	// Groups 配图组
	Groups []Group `json:"groups"`
	// Reason 分配理由（给用户看）
	Reason string `json:"reason"`
}

// Group 是一组配图。
type Group struct {
	Layout string `json:"layout"`
	// Count 该组的图片张数（由 layout 决定，显式写出便于 Agent 理解）
	Count int `json:"count"`
	// Purpose 表达目的（给 Agent 的写作指引）
	Purpose string `json:"purpose"`
	// Kind 生成方式
	Kind string `json:"kind"`
}

// Build 执行编排。
func Build(opts Options) (*Plan, error) {
	if len(opts.Sections) == 0 {
		return &Plan{}, nil
	}

	kinds := opts.EnabledKinds
	if len(kinds) == 0 {
		kinds = []string{KindMermaid, KindHTML, KindAI}
	}

	maxPerSection := opts.MaxPerSection
	if maxPerSection <= 0 {
		maxPerSection = 4
	}

	// 1. 算每节的适配分
	scored := make([]scoredSection, 0, len(opts.Sections))
	for _, s := range opts.Sections {
		scored = append(scored, scoredSection{
			section: s,
			score:   scoreSection(s),
		})
	}

	// 2. 按分数降序排（同分按原顺序，保持稳定）
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// 3. 确定总名额
	total := opts.TotalImages
	if total <= 0 {
		// 默认：每 3 节 1 张，至少 3 张，最多不超过节数的 1.5 倍
		total = len(opts.Sections) / 3
		if total < 3 {
			total = 3
		}
		maxTotal := int(math.Ceil(float64(len(opts.Sections)) * 1.5))
		if total > maxTotal {
			total = maxTotal
		}
	}

	// 4. 分配名额
	//    策略：优先给高分节，每节最多 maxPerSection
	allocations := make([]Allocation, 0, len(scored))
	remaining := total

	for _, ss := range scored {
		if remaining <= 0 {
			break
		}

		// 分数太低不给配图（低于 3 分的章节配图价值不大）
		if ss.score < 3 {
			continue
		}

		// 本节名额：高分多给，最多 maxPerSection
		want := quotaForScore(ss.score)
		if want > remaining {
			want = remaining
		}
		if want > maxPerSection {
			want = maxPerSection
		}
		if want <= 0 {
			continue
		}

		groups := buildGroups(ss.section, want, kinds, opts.AIPercent)
		imageCount := 0
		for _, g := range groups {
			imageCount += g.Count
		}
		if imageCount == 0 {
			continue
		}

		allocations = append(allocations, Allocation{
			NodeID: ss.section.NodeID,
			Title:  ss.section.Title,
			Score:  math.Round(ss.score*10) / 10,
			Groups: groups,
			Reason: explainScore(ss.section, ss.score),
		})
		remaining -= imageCount
	}

	// 5. 恢复原顺序（便于前端按目录顺序展示）
	order := make(map[string]int, len(opts.Sections))
	for i, s := range opts.Sections {
		order[s.NodeID] = i
	}
	sort.SliceStable(allocations, func(i, j int) bool {
		return order[allocations[i].NodeID] < order[allocations[j].NodeID]
	})

	// 6. 统计
	plan := &Plan{
		Allocations:  allocations,
		LayoutCounts: make(map[string]int),
		KindCounts:   make(map[string]int),
	}
	for _, a := range allocations {
		for _, g := range a.Groups {
			plan.TotalGroups++
			plan.LayoutCounts[g.Layout]++
			plan.KindCounts[g.Kind] += g.Count
			plan.TotalImages += g.Count
		}
	}

	if plan.TotalImages == 0 {
		plan.Note = "没有章节适合配图（适配分均低于 3 分），建议补充分节内容或调整配置"
	}

	return plan, nil
}

// scoredSection 是带评分的小节。
type scoredSection struct {
	section Section
	score   float64
}

// 关键词分档。
//
// 强适合：天然适合图形表达（结构、关系、流程）
// 中适合：可以配图但不是必须
var strongKeywords = []string{
	"架构", "流程", "组织", "拓扑", "部署", "结构",
	"体系", "模型", "矩阵", "路线", "时序", "分层",
}

var mediumKeywords = []string{
	"方案", "设计", "规划", "实施", "计划", "对比",
	"分析", "划分", "集成", "接口", "标准", "规范",
	"指标", "性能", "容量", "监测", "监控", "运维",
}

// scoreSection 计算配图适配分（0-10）。
//
// 评分维度（参考桌面版的 image_suitability_score）：
//
//	标题关键词（最重要）：架构/流程/组织/拓扑/部署 这类天然适合配图
//	内容长度：太短的小节配图显得空
//	层级：二级/三级小节比一级更具体，更适合配图
func scoreSection(s Section) float64 {
	score := 0.0

	title := s.Title
	desc := s.Description

	// === 标题关键词（0-7 分）===
	hitStrong, hitMedium := 0, 0
	combined := title + " " + desc
	for _, kw := range strongKeywords {
		if strings.Contains(combined, kw) {
			hitStrong++
		}
	}
	for _, kw := range mediumKeywords {
		if strings.Contains(combined, kw) {
			hitMedium++
		}
	}

	// 强关键词每个 2.5 分，中关键词每个 1 分，上限 7
	kwScore := float64(hitStrong)*2.5 + float64(hitMedium)*1.0
	if kwScore > 7 {
		kwScore = 7
	}
	score += kwScore

	// === 层级（0-1.5 分）===
	// 二三级小节最具体，最适合配图
	switch s.Level {
	case 2, 3:
		score += 1.5
	case 1:
		score += 0.5
	default:
		score += 1.0
	}

	// === 目标字数（0-1.5 分）===
	// 字数多的小节有空间容纳配图
	switch {
	case s.TargetWords >= 800:
		score += 1.5
	case s.TargetWords >= 500:
		score += 1.0
	case s.TargetWords >= 300:
		score += 0.5
	}

	// 上限 10
	if score > 10 {
		score = 10
	}

	return score
}

// quotaForScore 按分数决定给几张图。
func quotaForScore(score float64) int {
	switch {
	case score >= 8.5:
		return 4
	case score >= 7:
		return 3
	case score >= 5.5:
		return 2
	case score >= 3:
		return 1
	default:
		return 0
	}
}

// buildGroups 构造配图组。
//
// **布局选择的专业判断**（这是编排质量的关键）：
//
//	整体性图表（架构图/组织图/流程图）→ 单张大图，一张就够表达整体
//	并列性内容（多方案对比/多阶段/多指标）→ 三列或四宫格
//
// 桌面版的经验：「组织架构图」用四宫格是错的 —— 整体结构必须一张图看全，
// 拆成四格就失去了「整体」的意义。
func buildGroups(s Section, imageCount int, kinds []string, aiPercent int) []Group {
	hasKind := func(k string) bool {
		for _, kk := range kinds {
			if kk == k {
				return true
			}
		}
		return false
	}

	// 判断是否「整体性图表」：这类必须单张大图。
	//
	// **关键：只看标题，不看 description**
	//   description 里提到「架构」不等于本节讲架构
	//   （如「验收流程」的说明里会提到「架构、功能、性能」）。
	//   实测踩过这个坑：「14.2 验收标准与验收流程」因 description 含
	//   「架构」被判成架构类，配了四宫格 —— 完全不对。
	titleOnly := s.Title

	holistic := false
	for _, kw := range []string{"架构", "组织", "拓扑", "体系", "分层", "总体", "整体结构"} {
		if strings.Contains(titleOnly, kw) {
			holistic = true
			break
		}
	}
	// 「流程」「时序」也偏整体（一条主线的流程用一张图更清晰）
	for _, kw := range []string{"流程", "时序"} {
		if strings.Contains(titleOnly, kw) {
			holistic = true
			break
		}
	}
	// 并列性内容不按整体处理。
	// 注意：只认真正的并列信号，不能用「各」「分别」（「各角色职责」是正常的整体描述）
	for _, kw := range []string{"对比", "多方案", "比较", "选型对比"} {
		if strings.Contains(titleOnly, kw) {
			holistic = false
			break
		}
	}

	// 整体性图表：强制单张（最多 2 张：主图 + 补充图）
	if holistic {
		if imageCount >= 2 {
			return []Group{
				{Layout: LayoutSingle, Count: 1, Purpose: purposeFor(s) + "（整体结构）", Kind: pickKind(s, hasKind)},
				{Layout: LayoutSingle, Count: 1, Purpose: purposeFor(s) + "（局部细节或关键环节）", Kind: pickSupplementKind(s, hasKind)},
			}
		}
		return []Group{{
			Layout: LayoutSingle, Count: 1, Purpose: purposeFor(s), Kind: pickKind(s, hasKind),
		}}
	}

	// 按数量选布局
	var layout string
	switch imageCount {
	case 1:
		layout = LayoutSingle
	case 2:
		layout = LayoutSingle // 两组单张
	case 3:
		layout = LayoutThreeImages
	default:
		layout = LayoutFourImages
	}

	kind := pickKind(s, hasKind)
	purpose := purposeFor(s)

	if imageCount == 2 {
		// 两组单张，分别表达不同方面
		return []Group{
			{Layout: LayoutSingle, Count: 1, Purpose: purpose + "（第一个方面）", Kind: kind},
			{Layout: LayoutSingle, Count: 1, Purpose: purpose + "（第二个方面）", Kind: kind},
		}
	}

	count := imageCount
	if layoutImages[layout] != imageCount {
		count = layoutImages[layout]
	}

	return []Group{{
		Layout:  layout,
		Count:   count,
		Purpose: purpose,
		Kind:    kind,
	}}
}

// pickKind 选择生成方式。
func pickKind(s Section, hasKind func(string) bool) string {
	// **只看标题**：description 提到什么词不代表本节主题是什么
	// （实测踩坑：「验收流程」的说明里提到「架构」，被误判成架构类）
	title := s.Title

	// === 第一优先：组织结构图 ===
	// 「组织架构」既有「组织」又有「架构」，但组织结构图需要精确控制
	// 层级布局，mermaid 的 graph 画出来不像组织图，应该用 htmlImage。
	for _, kw := range []string{"组织架构", "组织图", "职责分工", "岗位设置", "团队配置", "人员配置"} {
		if strings.Contains(title, kw) {
			if hasKind(KindHTML) {
				return KindHTML
			}
			break
		}
	}

	// === 第二优先：并列/对比类 ===
	// 矩阵、对比、指标适合用表格布局（htmlImage）
	for _, kw := range []string{"矩阵", "对比", "指标", "表格", "清单"} {
		if strings.Contains(title, kw) {
			if hasKind(KindHTML) {
				return KindHTML
			}
			break
		}
	}

	// === 第三优先：流程/架构/拓扑/时序（mermaid 最合适）===
	for _, kw := range []string{"流程", "架构", "拓扑", "时序", "结构", "体系", "分层", "路线"} {
		if strings.Contains(title, kw) {
			if hasKind(KindMermaid) {
				return KindMermaid
			}
			break
		}
	}

	// 兜底：优先 htmlImage（比 AI 更可控）
	if hasKind(KindHTML) {
		return KindHTML
	}
	if hasKind(KindMermaid) {
		return KindMermaid
	}
	return KindAI
}

// pickSupplementKind 为补充图选生成方式（与主图不同，增加表现力）。
func pickSupplementKind(s Section, hasKind func(string) bool) string {
	primary := pickKind(s, hasKind)
	// 优先选一个与主图不同的方式
	for _, k := range []string{KindHTML, KindMermaid, KindAI} {
		if k != primary && hasKind(k) {
			return k
		}
	}
	return primary
}

// purposeFor 生成表达目的。
func purposeFor(s Section) string {
	// 去掉标题里的章节编号（如「2.3 」「第2章 」），只留内容
	clean := stripSectionNumber(s.Title)
	title := s.Title

	// 按标题特征给不同的目的描述
	if strings.Contains(title, "架构") || strings.Contains(title, "拓扑") {
		return fmt.Sprintf("用图形表达%s的整体结构、层次划分与组件关系", clean)
	}
	if strings.Contains(title, "流程") || strings.Contains(title, "时序") {
		return fmt.Sprintf("用流程图表达%s的关键步骤、角色分工与流转顺序", clean)
	}
	// 「组织架构」「组织图」「职责分工」才是真正的组织结构图
	// 「组织管理」「组织数据」这类不是
	if strings.Contains(title, "组织架构") || strings.Contains(title, "组织图") ||
		strings.Contains(title, "职责") || strings.Contains(title, "岗位") {
		return fmt.Sprintf("用组织图表达%s的层级结构、岗位设置与汇报关系", clean)
	}
	if strings.Contains(title, "部署") || strings.Contains(title, "实施") {
		return fmt.Sprintf("用图形表达%s的阶段划分、时间节点与交付物", clean)
	}
	if strings.Contains(title, "对比") || strings.Contains(title, "指标") {
		return fmt.Sprintf("用对比图表达%s的关键指标与差异", clean)
	}
	return fmt.Sprintf("用图形辅助说明%s的核心要点，让读者比纯文字更快理解", clean)
}

// stripSectionNumber 去掉标题前的章节编号。
//
//	"2.3 数据架构与数据资源规划" → "数据架构与数据资源规划"
//	"第2章 系统架构设计" → "系统架构设计"
//	"1.1 项目背景" → "项目背景"
func stripSectionNumber(title string) string {
	// 去掉「X.Y.Z 」这种数字编号
	i := 0
	for i < len(title) {
		c := title[i]
		if (c >= '0' && c <= '9') || c == '.' || c == ' ' {
			i++
			continue
		}
		break
	}
	if i > 0 && i < len(title) {
		title = title[i:]
	}

	// 去掉「第X章/第X节」
	for _, prefix := range []string{"第", "章 ", "节 "} {
		if strings.HasPrefix(title, prefix) {
			title = strings.TrimPrefix(title, prefix)
		}
	}
	// 去掉剩余的「X章」「X节」
	if idx := strings.Index(title, "章 "); idx > 0 && idx < 4 {
		title = title[idx+len("章 "):]
	}
	if idx := strings.Index(title, "节 "); idx > 0 && idx < 4 {
		title = title[idx+len("节 "):]
	}

	return strings.TrimSpace(title)
}

// explainScore 生成分配理由。
func explainScore(s Section, score float64) string {
	var parts []string

	title := s.Title
	// 找命中的关键词。优先报更具体的组合词
	//
	// 为什么：报「标题含『架构』」不如报「标题含『组织架构』」准确，
	// 前者会让人以为所有带架构的都用同一种配图。
	reported := false
	for _, kw := range []string{"组织架构", "职责分工", "技术架构", "数据架构", "应用架构", "系统架构", "部署架构"} {
		if strings.Contains(title, kw) {
			parts = append(parts, "标题含「"+kw+"」")
			reported = true
			break
		}
	}
	if !reported {
		for _, kw := range strongKeywords {
			if strings.Contains(title, kw) {
				parts = append(parts, "标题含「"+kw+"」")
				reported = true
				break
			}
		}
	}
	if !reported {
		for _, kw := range mediumKeywords {
			if strings.Contains(title, kw) {
				parts = append(parts, "标题含「"+kw+"」")
				break
			}
		}
	}
	if s.Level == 2 || s.Level == 3 {
		parts = append(parts, fmt.Sprintf("%d 级小节", s.Level))
	}
	if s.TargetWords >= 500 {
		parts = append(parts, fmt.Sprintf("目标 %d 字", s.TargetWords))
	}

	if len(parts) == 0 {
		return fmt.Sprintf("适配分 %.1f", score)
	}
	return fmt.Sprintf("适配分 %.1f（%s）", score, strings.Join(parts, "、"))
}

// FormatPrompt 把编排结果格式化成给 Agent 的写作指引。
//
// 每节一段，明确告知布局、张数、生成方式和表达目的。
func (p *Plan) FormatPrompt() string {
	if p == nil || len(p.Allocations) == 0 {
		return "本次不安排配图。正文只用文字表达，不要插入配图块。"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("本次共安排 %d 组配图、%d 张图片。\n\n", p.TotalGroups, p.TotalImages))
	sb.WriteString("## 各节配图安排\n\n")

	for _, a := range p.Allocations {
		sb.WriteString(fmt.Sprintf("### %s\n\n", a.Title))
		for i, g := range a.Groups {
			sb.WriteString(fmt.Sprintf("- 第 %d 组：%s（%d 张），生成方式 `%s`\n",
				i+1, layoutLabel(g.Layout), g.Count, g.Kind))
			sb.WriteString(fmt.Sprintf("  表达目的：%s\n", g.Purpose))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 执行要求\n\n")
	sb.WriteString("1. **严格按分配数量配图**，不要多也不要少\n")
	sb.WriteString("2. 没有列出的小节**不要配图**\n")
	sb.WriteString("3. 调用 `generate_image` 时按指定的 `kind` 生成\n")
	sb.WriteString("4. 图片生成后用返回的 assetRef 填进正文的 `data-yb-asset-ref`\n")
	sb.WriteString("5. 每张图都要写 `alt` 和 `figcaption`\n")

	return sb.String()
}

// layoutLabel 返回布局的中文说明。
func layoutLabel(layout string) string {
	switch layout {
	case LayoutSingle:
		return "单张图片"
	case LayoutImageText:
		return "左图右文"
	case LayoutThreeImages:
		return "三列图片"
	case LayoutFourImages:
		return "四宫格"
	default:
		return layout
	}
}
