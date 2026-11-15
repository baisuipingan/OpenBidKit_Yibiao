package duplicate

import (
	"strings"
)

// Index 是招标要求的句式索引。
//
// 为什么要建索引：招标文件 500+ 条要求，投标文件 3000+ 句，
// 两两比对是 150 万次 bigram 运算，慢到用户会放弃。
//
// 用 bigram 倒排索引：先按 bigram 找候选，再算精确相似度。
// 候选通常只有几十个，比两两比对快两个数量级。
type Index struct {
	// exactSet 完全一致的句子集合
	exactSet map[string]struct{}
	// strictSet 严格归一化的句子集合
	strictSet map[string]struct{}
	// skeletonSet 骨架键集合
	skeletonSet map[string]struct{}
	// fieldSet 出现过的字段名集合
	fieldSet map[string]struct{}

	// gramIndex bigram → 候选编号列表（倒排索引）
	gramIndex map[string][]int
	// entries 每个招标句子的信息
	entries []tenderEntry

	// looseText 所有招标句子的拼接（用于字段尾部的安全校验）
	looseText string
}

// tenderEntry 是一个招标句子。
type tenderEntry struct {
	// Original 原文
	Original string
	// Normalized 归一化后
	Normalized string
	// Grams bigram 集合（算相似度用）
	Grams map[string]struct{}
	// LooseText 去掉空白标点的文本（算包含度用）
	LooseText string
	// Index 在原文件里的序号（用于回指）
	Index int
}

// BuildIndex 从招标句子列表建索引。
func BuildIndex(tenderSentences []string) *Index {
	idx := &Index{
		exactSet:    make(map[string]struct{}, len(tenderSentences)),
		strictSet:   make(map[string]struct{}, len(tenderSentences)),
		skeletonSet: make(map[string]struct{}, len(tenderSentences)),
		fieldSet:    make(map[string]struct{}, len(tenderSentences)),
		gramIndex:   make(map[string][]int, len(tenderSentences)*4),
		entries:     make([]tenderEntry, 0, len(tenderSentences)),
	}

	var looseBuf strings.Builder

	for i, raw := range tenderSentences {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}

		normalized := Normalize(s)
		if normalized == "" {
			continue
		}

		loose := looseTextOf(s)
		grams := Bigrams(loose)

		entryIndex := len(idx.entries)
		idx.entries = append(idx.entries, tenderEntry{
			Original:   s,
			Normalized: normalized,
			Grams:      grams,
			LooseText:  loose,
			Index:      i,
		})

		idx.exactSet[s] = struct{}{}
		idx.strictSet[StrictKey(s)] = struct{}{}
		if sk := SkeletonKey(s); sk != "" {
			idx.skeletonSet[sk] = struct{}{}
		}
		if f, _, ok := FormatField(s); ok {
			idx.fieldSet[f] = struct{}{}
		}

		// 建倒排索引
		for g := range grams {
			idx.gramIndex[g] = append(idx.gramIndex[g], entryIndex)
		}

		looseBuf.WriteString(loose)
		looseBuf.WriteString("\n")
	}

	idx.looseText = looseBuf.String()
	return idx
}

// looseTextOf 去掉空白和标点的文本。
func looseTextOf(text string) string {
	s := strings.TrimSpace(text)
	s = reSpace.ReplaceAllString(s, "")
	s = rePunct.ReplaceAllString(s, "")
	return strings.ToLower(s)
}

// TenderCount 返回索引里的招标句子数。
func (idx *Index) TenderCount() int {
	return len(idx.strictSet)
}

// MatchResult 是一条匹配结果。
type MatchResult struct {
	// Matched 是否匹配到
	Matched bool
	// Reason 匹配方式
	Reason MatchReason
	// TenderText 匹配到的招标原文（near 匹配时返回最相似的那条）
	TenderText string
	// Dice Dice 相似度（仅 near 有值）
	Dice float64
	// Containment 包含度（仅 near 有值）
	Containment float64
}

// Match 判断一个句子是否匹配到了招标要求。
//
// 匹配顺序（从严到宽）：
//
//	exact → strict → field → skeleton → near
//
// 为什么这个顺序：每一步的误判率都比上一步高。
// 先用严格匹配捞掉大部分，剩下模糊的才走近似。
func (idx *Index) Match(sentence string) MatchResult {
	raw := strings.TrimSpace(sentence)
	if raw == "" {
		return MatchResult{}
	}

	// 1. 完全一致
	if _, ok := idx.exactSet[raw]; ok {
		return MatchResult{Matched: true, Reason: ReasonExact, TenderText: raw}
	}

	normalized := Normalize(raw)
	if normalized == "" {
		return MatchResult{}
	}

	// 2. 严格归一化
	if _, ok := idx.strictSet[normalized]; ok {
		return MatchResult{Matched: true, Reason: ReasonStrict}
	}

	// 3. 结构化字段
	if field, tail, ok := FormatField(raw); ok {
		if _, hasField := idx.fieldSet[field]; hasField && idx.safeFieldTail(tail) {
			return MatchResult{Matched: true, Reason: ReasonField}
		}
	}

	// 4. 骨架
	if sk := SkeletonKey(raw); sk != "" {
		if _, ok := idx.skeletonSet[sk]; ok {
			return MatchResult{Matched: true, Reason: ReasonSkeleton}
		}
	}

	// 5. 近似
	return idx.matchNear(raw, normalized)
}

// safeFieldTail 校验字段值不是空壳。
//
// 为什么需要：招标文件里"项目名称："后面可能就跟着回车，
// 这种空字段不能算匹配 —— 否则任何"项目名称：xxx"都能蒙对。
func (idx *Index) safeFieldTail(tail string) bool {
	loose := looseTextOf(tail)
	if len([]rune(loose)) < 2 {
		return false
	}
	// 字段值里至少有部分内容出现在招标文本里
	return strings.Contains(idx.looseText, loose)
}

// matchNear 近似匹配。
//
// 用倒排索引找候选，再算精确相似度。
func (idx *Index) matchNear(raw, normalized string) MatchResult {
	if !ShouldApplyNear(raw) {
		return MatchResult{}
	}

	loose := looseTextOf(raw)
	grams := Bigrams(loose)
	if len(grams) < minGramsForNear {
		return MatchResult{}
	}

	// 用倒排索引找候选，统计每个候选命中了多少 gram
	candidateHits := make(map[int]int)
	for g := range grams {
		for _, entryIdx := range idx.gramIndex[g] {
			candidateHits[entryIdx]++
		}
	}

	if len(candidateHits) == 0 {
		return MatchResult{}
	}

	runes := []rune(loose)
	isLong := len(runes) >= longThreshold

	var best *MatchResult
	bestDice := 0.0

	for entryIdx, shared := range candidateHits {
		entry := idx.entries[entryIdx]
		if len(entry.Grams) == 0 {
			continue
		}

		shorter := len(grams)
		longer := len(entry.Grams)
		if len(entry.Grams) < shorter {
			shorter = len(entry.Grams)
		}
		if len(entry.Grams) > longer {
			longer = len(entry.Grams)
		}

		containment := float64(shared) / float64(max2(shorter, 1))
		dice := DiceSimilarity(shared, len(grams), len(entry.Grams))
		lengthRatio := float64(shorter) / float64(max2(longer, 1))

		var allowed bool
		if isLong {
			allowed = containment >= longMinContainment &&
				dice >= longMinDice &&
				lengthRatio >= longMinLengthRatio
		} else {
			allowed = containment >= shortMinContainment &&
				dice >= shortMinDice &&
				lengthRatio >= shortMinLengthRatio
		}

		if !allowed {
			continue
		}

		if dice > bestDice {
			bestDice = dice
			best = &MatchResult{
				Matched:     true,
				Reason:      ReasonNear,
				TenderText:  entry.Original,
				Dice:        dice,
				Containment: containment,
			}
		}
	}

	if best == nil {
		return MatchResult{}
	}
	return *best
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
