// Package duplicate 实现标书查重。
//
// 查重要解决的问题：判断投标文件是否响应了招标文件的每一条要求。
// 不是"两份标书互相比对"，而是"招标要求 → 投标响应"的单向核对。
//
// 为什么需要四级归一化（移植自桌面版 duplicateCheckService.cjs）：
//
//	招标原文："投标人须提供近三年（2021-2023）类似项目业绩，不少于 3 个"
//	投标响应："我方提供 2021-2023 年类似项目业绩 5 个，详见附件"
//
//	字面不同，但语义上是响应的。如果只用完全匹配，会把已响应的判成漏项，
//	误报率高到用户不会用。所以要做归一化，逐级放宽：
//
//	  exact    完全一致
//	  strict   去空白/标点/大小写后一致
//	  field    结构化字段匹配（"项目名称：XXX"）
//	  skeleton 骨架匹配（数字/日期/编号替换成占位符后一致）
//	  near     bigram 包含度 + Dice 相似度（阈值按句长区分）
package duplicate

import (
	"regexp"
	"strings"
)

// MatchReason 匹配方式（从严到宽）。
type MatchReason string

const (
	ReasonExact    MatchReason = "exact"
	ReasonStrict   MatchReason = "strict"
	ReasonField    MatchReason = "field"
	ReasonSkeleton MatchReason = "skeleton"
	ReasonNear     MatchReason = "near"
)

// 相似度阈值（按句长区分）。
//
// 为什么短句要更严：短句的 bigram 少，容易偶然重合。
// 比如"项目名称"只有 3 个 bigram，随便两句都能命中。
const (
	// 长句（去标点后 ≥ 30 字）
	longMinContainment = 0.90
	longMinDice        = 0.82
	longMinLengthRatio = 0.50

	// 短句
	shortMinContainment = 0.95
	shortMinDice        = 0.88
	shortMinLengthRatio = 0.55

	// longThreshold 判定长短句的字数分界
	longThreshold = 30

	// minGramsForNear bigram 太少时不做近似匹配（噪音太大）
	minGramsForNear = 4
)

var (
	// 需要去掉的标点（中英文都覆盖）
	rePunct = regexp.MustCompile(`[.,，。;；:：、!！?？"'“”‘’《》〈〉()（）\[\]【】{}]`)
	// 空白（含全角空格）
	reSpace = regexp.MustCompile(`[\s　]+`)

	// 骨架化用的占位符替换规则
	reDateCn    = regexp.MustCompile(`\d{4}年\d{1,2}月\d{1,2}日`)
	reDateSlash = regexp.MustCompile(`\d{4}[-/.]\d{1,2}[-/.]\d{1,2}`)
	reCode      = regexp.MustCompile(`[A-Z]{2,}[-A-Z0-9]{4,}`)
	reMoneyWan  = regexp.MustCompile(`\d+(?:\.\d+)?\s*万元`)
	reMoneyYuan = regexp.MustCompile(`\d+(?:\.\d+)?\s*元`)
	rePercent   = regexp.MustCompile(`\d+(?:\.\d+)?\s*%`)
	reScore     = regexp.MustCompile(`\d+(?:\.\d+)?\s*分`)
	rePage      = regexp.MustCompile(`(?i)P\s*\d+(?:\s*[-~至]\s*P?\s*\d+)?`)
	reNumber    = regexp.MustCompile(`\d+(?:\.\d+)?`)

	// 结构化字段模式："项目名称：XXX" / "招标编号:XXX"
	reFormatField = regexp.MustCompile(`^([\p{Han}A-Za-z][\p{Han}A-Za-z0-9（）()]{1,14})[：:]\s*(\S.*)$`)

	// 短句在什么情况下仍做近似匹配
	reNearWhitelist = regexp.MustCompile(`(评分|评标|页码|投标日期|日期|技术要求|招标要求)`)
)

// Normalize 基础归一化：去空白、去标点、转小写。
//
// 这是所有匹配的起点。
func Normalize(text string) string {
	s := strings.TrimSpace(text)
	s = reSpace.ReplaceAllString(s, "")
	s = rePunct.ReplaceAllString(s, "")
	return strings.ToLower(s)
}

// StrictKey 严格键 = 基础归一化。
//
// 用途：两份文本只是空格/标点/大小写不同，应该判定为一致。
func StrictKey(text string) string {
	return Normalize(text)
}

// SkeletonKey 骨架键 = 把可变部分替换成占位符后的归一化文本。
//
// 为什么需要：招标文件里同一类要求会带不同的具体数值，
//
//	"投标人须提供不少于 3 个类似项目业绩"
//	"须提供类似项目业绩 5 个"
//
// 骨架化后都变成"投标人须提供不少于{num}个类似项目业绩"这一类，
// 能识别出这是同一条要求。
func SkeletonKey(text string) string {
	s := strings.TrimSpace(text)

	// 先去掉原文里的花括号（避免和占位符冲突）
	s = strings.NewReplacer("{", "", "}", "").Replace(s)

	// 按顺序替换：先长后短，避免"万元"被"元"抢先匹配
	s = reDateCn.ReplaceAllString(s, "{date}")
	s = reDateSlash.ReplaceAllString(s, "{date}")
	s = reCode.ReplaceAllString(s, "{code}")
	s = reMoneyWan.ReplaceAllString(s, "{money}")
	s = reMoneyYuan.ReplaceAllString(s, "{money}")
	s = rePercent.ReplaceAllString(s, "{percent}")
	s = reScore.ReplaceAllString(s, "{score}")
	s = rePage.ReplaceAllString(s, "{page}")
	s = reNumber.ReplaceAllString(s, "{num}")

	// 最后再做基础归一化。
	// 注意：不能去掉花括号（占位符要靠它们区分）。
	s = reSpace.ReplaceAllString(s, "")
	s = strings.NewReplacer(
		".", "", ",", "", "，", "", "。", "", ";", "", "；", "",
		":", "", "：", "", "、", "", "!", "", "！", "",
		"?", "", "？", "", "\"", "", "'", "",
		"“", "", "”", "", "‘", "", "’", "",
		"《", "", "》", "", "〈", "", "〉", "",
		"(", "", ")", "", "[", "", "]", "",
		"【", "", "】", "", "/", "",
	).Replace(s)

	// 全角转半角小写
	return strings.ToLower(s)
}

// FormatField 从"字段名：值"结构里拆出字段名。
//
// 用途：招标文件里的"项目名称：某医院信息化项目"和投标文件里的
// "项目名称：某医院信息化项目二期"，字段名一致说明这一项有响应。
func FormatField(text string) (field, tail string, ok bool) {
	m := reFormatField.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// Bigrams 生成字符 bigram 集合。
//
// 用 bigram 而不是词：中文没有空格，词边界难判定，
// 字符级 bigram 简单且效果好。
//
//	"微服务架构" → {微服, 服务, 务架, 架构}
func Bigrams(text string) map[string]struct{} {
	runes := []rune(text)
	if len(runes) == 0 {
		return map[string]struct{}{}
	}
	if len(runes) == 1 {
		return map[string]struct{}{string(runes): {}}
	}

	grams := make(map[string]struct{}, len(runes)-1)
	for i := 0; i < len(runes)-1; i++ {
		grams[string(runes[i:i+2])] = struct{}{}
	}
	return grams
}

// DiceSimilarity 计算 Dice 系数。
//
//	Dice = 2 * |A ∩ B| / (|A| + |B|)
//
// 相比 Jaccard，Dice 对长度差异不那么敏感，
// 适合"一段短要求被写进长段落"这种场景。
func DiceSimilarity(shared, leftSize, rightSize int) float64 {
	if leftSize+rightSize == 0 {
		return 0
	}
	return float64(2*shared) / float64(leftSize+rightSize)
}

// NearMatch 判断两个文本是否近似匹配。
//
// 三个条件同时满足才算匹配（对齐桌面版阈值）：
//   - containment 包含度：短句的 bigram 有多少出现在长句里
//   - dice        Dice 相似度
//   - lengthRatio 长度比（差太多说明不是一个量级的内容）
func NearMatch(a, b string) (matched bool, dice float64, containment float64) {
	gramsA := Bigrams(a)
	gramsB := Bigrams(b)

	if len(gramsA) < minGramsForNear || len(gramsB) < minGramsForNear {
		return false, 0, 0
	}

	// 算交集
	shared := 0
	smaller, larger := gramsA, gramsB
	if len(gramsB) < len(gramsA) {
		smaller, larger = gramsB, gramsA
	}
	for g := range smaller {
		if _, ok := larger[g]; ok {
			shared++
		}
	}

	containment = float64(shared) / float64(len(smaller))
	dice = DiceSimilarity(shared, len(gramsA), len(gramsB))

	// 长度比
	shorter := len(smaller)
	longer := len(larger)
	lengthRatio := float64(shorter) / float64(longer)

	// 按句长选阈值
	runes := []rune(a)
	if len(runes) >= longThreshold {
		matched = containment >= longMinContainment &&
			dice >= longMinDice &&
			lengthRatio >= longMinLengthRatio
	} else {
		matched = containment >= shortMinContainment &&
			dice >= shortMinDice &&
			lengthRatio >= shortMinLengthRatio
	}

	return matched, dice, containment
}

// CoreContainment 计算「核心内容包含度」。
//
// **为什么需要这个额外的判据**：
//
//	招标：投标人须提供营业执照副本复印件并加盖公章
//	投标：我公司已提供营业执照副本复印件并加盖公章，详见附件一
//
// 这两句在语义上明确是响应的，但整句相似度只有 dice=0.70
// （主语"投标人须"→"我公司已"、尾部多了"详见附件一"），
// 按整句阈值 0.82 会判成漏项 —— 这就是大规模误报的根源。
//
// 查重要回答的不是"这两句像不像"，而是"这条要求有没有被响应"。
// 所以用**最长公共子串**占招标句的比例来衡量：
// 只要招标要求的主体部分（≥60%）原样出现在投标内容里，就算响应。
//
// 这比整句相似度更贴合业务：投标人写"我公司"还是"投标人"、
// 加不加"详见附件"，都不影响"这条要求被响应了"的结论。
func CoreContainment(tenderText, bidText string) (ratio float64, fragment string) {
	a := looseTextOf(tenderText)
	b := looseTextOf(bidText)

	if a == "" || b == "" {
		return 0, ""
	}

	ra := []rune(a)
	rb := []rune(b)

	// 最长公共子串（DP，O(n*m)）
	// 句子长度都在几百字以内，直接 DP 没问题
	bestLen := 0
	bestEnd := 0

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)

	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			if ra[i-1] == rb[j-1] {
				curr[j] = prev[j-1] + 1
				if curr[j] > bestLen {
					bestLen = curr[j]
					bestEnd = i
				}
			} else {
				curr[j] = 0
			}
		}
		prev, curr = curr, prev
		// 复用数组前清零
		for k := range curr {
			curr[k] = 0
		}
	}

	if bestLen == 0 {
		return 0, ""
	}

	ratio = float64(bestLen) / float64(len(ra))
	fragment = string(ra[bestEnd-bestLen : bestEnd])
	return ratio, fragment
}

// ShouldApplyNear 判断某个句子是否值得做近似匹配。
//
// 短句只在包含特定关键词时才做（否则噪音太多）：
// 去掉标点后不足 12 字，且不含"评分/评标/日期"这类词 → 不做近似匹配。
func ShouldApplyNear(original string) bool {
	loose := Normalize(original)
	if len([]rune(loose)) >= 12 {
		return true
	}
	return reNearWhitelist.MatchString(original)
}

// SplitSentences 把长文本切成句子。
//
// 切分规则：
//   - 按句末标点切（。！？；）
//   - 保留分隔符本身（有些句子靠分号连接分项）
//   - 过短的碎片合并到前一句（"如下："这种）
func SplitSentences(text string) []string {
	var out []string
	var buf strings.Builder

	flush := func() {
		s := strings.TrimSpace(buf.String())
		buf.Reset()
		if s == "" {
			return
		}

		// 极短的碎片合并到前一句。
		//
		// 阈值 4：只处理"如下："、"其中："这类纯引导语。
		// 用 8 会把正常的短句（"须加盖公章。"6 字）也合并掉，
		// 连续几个短句会被全并成一大句，招标要求就数不清了。
		if len([]rune(s)) < 4 && len(out) > 0 {
			out[len(out)-1] += s
			return
		}
		out = append(out, s)
	}

	for _, r := range text {
		buf.WriteRune(r)
		switch r {
		case '。', '！', '？', '；', '\n':
			flush()
		}
	}
	flush()

	return out
}
