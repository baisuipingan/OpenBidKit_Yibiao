package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/patrick/yibiao-web/internal/service/agent/content"
	"github.com/patrick/yibiao-web/internal/store"
)

// Matcher 为目录章节匹配知识库素材。
//
// 两种来源：
//  1. **手动关联**：用户在目录里给节点挂了 knowledge_item_ids（最准，优先用）
//  2. **自动匹配**：按章节标题+描述检索知识库（兜底，覆盖没手动挂的章节）
//
// 为什么需要自动匹配：一个 60 节的标书，让用户逐节挂知识点不现实。
// 自动匹配能命中 60-70%，剩下的用户再补 —— 体验比全手动好得多。
type Matcher struct {
	q   *store.Queries
	log *slog.Logger
}

// NewMatcher 创建匹配器。
func NewMatcher(q *store.Queries, log *slog.Logger) *Matcher {
	if log == nil {
		log = slog.Default()
	}
	return &Matcher{q: q, log: log}
}

// MatchOptions 是匹配参数。
type MatchOptions struct {
	TenantID string
	// MaxPerSection 每节最多给几条参考（太多会淹没 Prompt）
	MaxPerSection int
	// MinScore 最低匹配分（低于这个不采用，避免硬塞无关内容）
	MinScore float64
	// ManualOnly 只用用户手动关联的，不做自动匹配
	ManualOnly bool
	// DocumentIDs 限定检索范围（用户在设置页勾选的参考文档）。
	//
	// 为空表示不限（搜全部）。
	// 为什么需要这个：用户可能只想用某几份高质量材料，
	// 不想让旧文档里的过时信息混进来。
	DocumentIDs []string
}

// FillSections 给章节列表填充知识参考。
//
// 就地修改 sections 的 KnowledgeRefs 字段。
func (m *Matcher) FillSections(ctx context.Context, tenantID, projectID string, sections []content.Section, opts MatchOptions) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}

	// 没显式指定范围时，读项目配置里的参考文档（设置页勾选的）
	if len(opts.DocumentIDs) == 0 && projectID != "" {
		if pid, perr := uuid.Parse(projectID); perr == nil {
			if docs, qerr := m.q.GetReferenceKnowledgeDocs(ctx, store.GetReferenceKnowledgeDocsParams{
				ProjectID: pid, TenantID: tid,
			}); qerr == nil {
				opts.DocumentIDs = docs
			}
		}
	}

	if opts.MaxPerSection <= 0 {
		opts.MaxPerSection = 3
	}
	if opts.MinScore <= 0 {
		opts.MinScore = 0.1
	}

	// 1. 先尝试手动关联
	manual, err := m.loadManualRefs(ctx, tid)
	if err != nil {
		m.log.Warn("读取手动关联失败，只用自动匹配", slog.String("error", err.Error()))
		manual = nil
	}

	// 2. 逐节填充
	filled := 0
	for i := range sections {
		sec := &sections[i]

		// 优先用手动关联
		if refs, ok := manual[sec.NodeID]; ok && len(refs) > 0 {
			sec.KnowledgeRefs = refs
			filled++
			continue
		}

		if opts.ManualOnly {
			continue
		}

		// 自动匹配
		refs, matchErr := m.autoMatch(ctx, tid, sec, opts)
		if matchErr != nil {
			continue
		}
		if len(refs) > 0 {
			sec.KnowledgeRefs = refs
			filled++
		}
	}

	m.log.Info("知识库素材匹配完成",
		slog.Int("sections", len(sections)),
		slog.Int("with_refs", filled),
	)

	return nil
}

// loadManualRefs 读用户手动关联的知识条目。
//
// 返回 map[nodeID][]KnowledgeRef。
func (m *Matcher) loadManualRefs(ctx context.Context, tenantID uuid.UUID) (map[string][]content.KnowledgeRef, error) {
	// 取所有挂了 knowledge_item_ids 的节点
	nodes, err := m.q.ListNodesWithKnowledge(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]content.KnowledgeRef, len(nodes))

	for _, n := range nodes {
		var itemIDs []string
		if err := jsonUnmarshal(n.KnowledgeItemIds, &itemIDs); err != nil || len(itemIDs) == 0 {
			continue
		}

		// 按 item_id 批量取知识条目
		items, err := m.q.ListItemsByIds(ctx, store.ListItemsByIdsParams{
			TenantID: tenantID,
			Column2:  itemIDs,
		})
		if err != nil {
			continue
		}

		refs := make([]content.KnowledgeRef, 0, len(items))
		for _, item := range items {
			refs = append(refs, content.KnowledgeRef{
				ID:      item.ItemID,
				Title:   item.Title,
				Resume:  item.Resume,
				Content: item.Content,
			})
		}
		if len(refs) > 0 {
			out[n.NodeID.String()] = refs
		}
	}

	return out, nil
}

// autoMatch 按章节标题自动检索知识库。
func (m *Matcher) autoMatch(
	ctx context.Context,
	tenantID uuid.UUID,
	sec *content.Section,
	opts MatchOptions,
) ([]content.KnowledgeRef, error) {
	// 用「标题 + 描述」拼检索词。
	// 只用标题太短（"项目背景"可能命中一堆不相关的），
	// 加上描述能显著提升精度。
	query := strings.TrimSpace(sec.Title)
	if sec.Description != "" {
		query += " " + truncateRunes(sec.Description, 60)
	}

	keywords := extractKeywords(query)
	if len(keywords) == 0 {
		return nil, nil
	}

	// 逐个关键词检索，累计打分
	scores := make(map[string]float64)
	items := make(map[string]store.KnowledgeItem)

	for _, kw := range keywords {
		rows, err := m.q.SearchKnowledgeByLike(ctx, store.SearchKnowledgeByLikeParams{
			Keyword:   pgText(kw),
			TenantID:  tenantID,
			ProjectID: nil,
			Lim:       int32(opts.MaxPerSection * 4),
			Off:       0,
		})
		if err != nil {
			continue
		}

		for _, row := range rows {
			// 限定文档范围（用户在设置页勾选的）
			if len(opts.DocumentIDs) > 0 {
				allowed := false
				for _, id := range opts.DocumentIDs {
					if row.DocumentID.String() == id {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
			}

			score := scoreMatch(kw, row.Title, row.Resume, row.Content)
			scores[row.ItemID] += score

			if _, exists := items[row.ItemID]; !exists {
				items[row.ItemID] = store.KnowledgeItem{
					ID:         row.ID,
					ItemID:     row.ItemID,
					Title:      row.Title,
					Resume:     row.Resume,
					Content:    row.Content,
					DocumentID: row.DocumentID,
					TenantID:   tenantID,
				}
			}
		}
	}

	// 排序取前 N 条
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, sc := range scores {
		if sc >= opts.MinScore {
			ranked = append(ranked, scored{id: id, score: sc})
		}
	}

	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	limit := opts.MaxPerSection
	if len(ranked) < limit {
		limit = len(ranked)
	}

	refs := make([]content.KnowledgeRef, 0, limit)
	for i := 0; i < limit; i++ {
		item := items[ranked[i].id]
		refs = append(refs, content.KnowledgeRef{
			ID:      item.ItemID,
			Title:   item.Title,
			Resume:  item.Resume,
			Content: item.Content,
		})
	}

	return refs, nil
}

// scoreMatch 计算匹配分。
//
// 权重设计：标题命中 > 摘要命中 > 正文命中。
// 标题命中说明这条知识就是讲这个主题的，价值最高。
func scoreMatch(keyword, title, resume, content string) float64 {
	kw := strings.ToLower(keyword)

	score := 0.0
	if strings.Contains(strings.ToLower(title), kw) {
		score += 3.0
	}
	if strings.Contains(strings.ToLower(resume), kw) {
		score += 1.5
	}
	if strings.Contains(strings.ToLower(content), kw) {
		score += 1.0
	}
	return score
}

// extractKeywords 从查询里提取关键词。
//
// **核心难点：中文没有空格**。
//
// 直接用整串检索必然失败：
//
//	标题「应用架构与微服务化设计」
//	知识库内容「后端使用 Spring Cloud 微服务框架」
//	LIKE '%应用架构与微服务化设计%' → 0 条
//
// 所以要拆成短的语义单元：
//   - 拉丁词/数字：原样保留（"B/S"、"PostgreSQL"、"2.2" 本身有信息量）
//   - 中文：按分隔符切开后，再切成 2-4 字的 n-gram
//     取 3-gram 优先（2 字太泛，"架构"、"设计" 到处都有；
//     4 字以上命中率低）
//
// 这样「微服务化设计」会拆出「微服务」「服务化」「务化设」等，
// 其中「微服务」能命中知识库。
func extractKeywords(query string) []string {
	// 泛化词黑名单
	stopwords := map[string]bool{
		"项目": true, "方案": true, "要求": true, "内容": true, "说明": true,
		"介绍": true, "概述": true, "分析": true, "设计": true, "实现": true,
		"建设": true, "系统": true, "管理": true, "服务": true, "技术": true,
		"架构": true, "应用": true, "平台": true, "体系": true, "机制": true,
		"的": true, "和": true, "与": true, "及": true, "等": true,
		"2.1": true, "2.2": true, "2.3": true, "1.1": true, "1.2": true,
	}

	out := make([]string, 0, 12)
	seen := make(map[string]bool)

	add := func(kw string) {
		kw = strings.TrimSpace(kw)
		if kw == "" || stopwords[kw] || seen[kw] {
			return
		}
		n := len([]rune(kw))
		if n < 2 {
			return
		}
		seen[kw] = true
		out = append(out, kw)
	}

	// 切分符
	sep := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '、', '，', ',', '。', '.', '；', ';',
			'：', ':', '（', '）', '(', ')', '【', '】', '[', ']', '/', '|', '>', '-':
			return true
		}
		return false
	}

	for _, piece := range strings.FieldsFunc(query, sep) {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}

		runes := []rune(piece)

		// 纯拉丁/数字：整串保留（"PostgreSQL"、"B"、"2"）
		if !containsCJKRune(runes) {
			if len(runes) <= 20 {
				add(piece)
			}
			continue
		}

		// 中文串：先整串试（短的有意义，如「微服务」）
		if len(runes) >= 2 && len(runes) <= 4 {
			add(piece)
			continue
		}

		// 长的切 2-gram。
		//
		// **为什么不用 3-gram**：
		//   "应用架构与微服务化设计" 的 3-gram 是
		//   「应用架」「用架构」「架构与」「构与微」「与微服」…
		//   全是跨词边界的碎片，一个都匹配不到真实内容。
		//
		//   2-gram 是「应用」「用架」「架构」「构与」「与微」
		//   「微服」「服务」「务化」「化设」「设计」——
		//   其中「架构」「服务」「设计」都是真实存在的词，
		//   ILIKE 能命中 "微服务"、"架构" 这类内容。
		for i := 0; i+2 <= len(runes); i++ {
			add(string(runes[i : i+2]))
		}
	}

	// 最多 8 个关键词
	if len(out) > 8 {
		out = out[:8]
	}

	return out
}

// containsCJKRune 判断是否含中日韩字符。
func containsCJKRune(runes []rune) bool {
	for _, r := range runes {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// truncateRunes 按字符截断。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// pgText 构造非空 pgtype.Text。
func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// jsonUnmarshal 解析 JSON，失败返回错误。
func jsonUnmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return fmt.Errorf("空数据")
	}
	return json.Unmarshal(data, v)
}
