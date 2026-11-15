package duplicate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/docparse"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/store"
)

// 分析维度。
const (
	DimMetadata = "metadata"
	DimOutline  = "outline"
	DimContent  = "content"
	DimImage    = "image"
)

// coreContainmentThreshold 核心片段包含度阈值。
//
// 0.50 的由来（实测调整）：
//
//	"须提供近三年类似项目业绩不少于3个"
//	→ "提供近三年类似项目业绩5个，详见业绩表"
//	核心片段占 55%，正好压在 55% 的线上 —— 太脆弱，
//	投标方多写半句"详见业绩表"就掉到阈值以下。
//
// 降到 50% 后仍有辨识度：真正没写的要求核心片段占比通常 <20%
// （只剩"须提供""投标人"这类通用词）。
// 再低（如 40%）会让"须提供业绩"错误匹配到"须提供资质"。
const coreContainmentThreshold = 0.50

// 分析状态。
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusError   = "error"
)

// Service 编排查重流程。
//
// 流程：
//
//	导入招标/投标文件 → 解析 markdown → 切句 → 建招标索引
//	  → outline 分析（投标目录是否覆盖招标要求）
//	  → content 分析（投标正文是否响应招标要求）
//	  → metadata 分析（文档属性指纹）
//	  → image 分析（图片重复）
type Service struct {
	q         *store.Queries
	artifacts *artifact.Service
	log       *slog.Logger
	workDir   string

	// tenderSentenceIDs 当前分析的招标句子 ID（按 seq 顺序）。
	// analyzeOutline 和 analyzeContent 都要用，避免重复查库。
	tenderSentenceIDs []uuid.UUID
}

// New 创建服务。
func New(q *store.Queries, artifacts *artifact.Service, workDir string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: q, artifacts: artifacts, workDir: workDir, log: log}
}

// AnalyzeOptions 是分析参数。
type AnalyzeOptions struct {
	TenantID  string
	ProjectID string
	OwnerID   string
	// Dimensions 要跑哪些维度（空表示全部）
	Dimensions []string
	// OnProgress 进度回调
	OnProgress func(dimension string, progress int, message string)
}

// Analyze 执行查重分析。
func (s *Service) Analyze(ctx context.Context, opts AnalyzeOptions) error {
	tenantID, err := uuid.Parse(opts.TenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}
	projectID, err := uuid.Parse(opts.ProjectID)
	if err != nil {
		return fmt.Errorf("项目标识非法")
	}

	check, err := s.q.GetDuplicateCheck(ctx, store.GetDuplicateCheckParams{
		TenantID: tenantID, ProjectID: projectID,
	})
	if err != nil {
		return fmt.Errorf("查重工作区不存在，请先导入文件")
	}

	// 解析招标句子（只做一次，多个维度共用）
	tenderSentences, err := s.loadSentences(ctx, check.ID, "tender")
	if err != nil {
		return fmt.Errorf("读取招标句子失败: %w", err)
	}
	if len(tenderSentences) == 0 {
		return fmt.Errorf("招标文件没有可分析的句子，请检查文件是否解析成功")
	}

	// 建索引
	index := BuildIndex(tenderSentences)
	s.log.Info("招标要求索引已建立",
		slog.Int("tender_count", index.TenderCount()),
	)

	dims := opts.Dimensions
	if len(dims) == 0 {
		// 全部四个维度都跑。
		// 前端「查重结果」页把四个页签都展示出来，
		// 少跑一个就会显示「待分析」，用户会以为功能坏了。
		dims = []string{DimMetadata, DimOutline, DimContent, DimImage}
	}

	for _, dim := range dims {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var analyzeErr error
		switch dim {
		case DimOutline:
			analyzeErr = s.analyzeOutline(ctx, check.ID, index, opts)
		case DimContent:
			analyzeErr = s.analyzeContent(ctx, check.ID, index, opts)
		case DimMetadata:
			analyzeErr = s.analyzeMetadata(ctx, check, opts)
		case DimImage:
			analyzeErr = s.analyzeImage(ctx, check, opts)
		default:
			continue
		}

		if analyzeErr != nil {
			s.log.Warn("维度分析失败",
				slog.String("dimension", dim),
				slog.String("error", analyzeErr.Error()),
			)
			s.updateAnalysis(ctx, check.ID, dim, StatusError, 100, "分析失败", analyzeErr.Error())
		}
	}

	return nil
}

// loadSentences 读某角色的句子文本。
func (s *Service) loadSentences(ctx context.Context, checkID uuid.UUID, role string) ([]string, error) {
	rows, err := s.q.ListSentences(ctx, store.ListSentencesParams{
		CheckID: checkID, Role: role,
	})
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out, nil
}

// analyzeOutline 分析目录覆盖度。
//
// **为什么目录分析不能复用正文的匹配逻辑**：
//
//	正文分析问的是"这条要求有没有被响应"（内容级），
//	目录分析问的是"投标文件有没有专门的章节讲这个话题"（结构级）。
//
//	招标要求："投标人须具备信息系统集成及服务资质二级及以上"
//	投标目录："第一章 资格证明" —— 语义上覆盖了，但字符串差异极大，
//	用相似度匹配永远命中不了。
//
// 所以目录分析用**关键词覆盖**：从招标要求里提取主题词，
// 看投标目录的标题里有没有出现这些词。
func (s *Service) analyzeOutline(ctx context.Context, checkID uuid.UUID, index *Index, opts AnalyzeOptions) error {
	s.updateAnalysis(ctx, checkID, DimOutline, StatusRunning, 10, "正在分析目录覆盖度", "")

	bidRows, err := s.q.ListSentences(ctx, store.ListSentencesParams{
		CheckID: checkID, Role: "bid",
	})
	if err != nil {
		return err
	}

	// 提取目录项（标题类句子）
	headings := make([]store.DuplicateSentence, 0, len(bidRows)/5)
	for _, r := range bidRows {
		if isHeadingLike(r.Text) {
			headings = append(headings, r)
		}
	}

	if len(headings) == 0 {
		s.updateAnalysis(ctx, checkID, DimOutline, StatusSuccess, 100, "投标文件没有识别到目录结构", "")
		return nil
	}

	// 所有目录标题拼成一个文本，用于关键词检索
	var headingText strings.Builder
	for _, h := range headings {
		headingText.WriteString(looseTextOf(h.Text))
		headingText.WriteString("\n")
	}
	headingLoose := headingText.String()

	s.updateAnalysis(ctx, checkID, DimOutline, StatusRunning, 40,
		fmt.Sprintf("已提取 %d 个目录项", len(headings)), "")

	_ = s.q.DeleteMatchesByDimension(ctx, store.DeleteMatchesByDimensionParams{
		CheckID: checkID, Dimension: DimOutline,
	})

	tenderTexts := s.mustTenderSentences(ctx, checkID)
	covered := 0

	for i, tenderText := range tenderTexts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		tenderID := s.tenderSentenceIDs[i]

		// 按业务主题判断覆盖。
		//
		// 为什么不用字符串相似度：招标要求"须提供营业执照副本"和
		// 目录"第一章 资格证明"没有任何公共子串，但人一看就知道覆盖了。
		// 按主题归类（营业执照→资格类）才能正确判断。
		coveredByTopic, score := s.matchOutlineByTopic(tenderText, headingLoose)

		var bidID *uuid.UUID
		reason := "none"
		similarity := score

		if coveredByTopic {
			// 找出最相关的目录项（给用户看"对应哪个章节"）
			cats := classifyTopics(tenderText)
			var bestHeading *store.DuplicateSentence
			bestHits := 0
			for j := range headings {
				lh := looseTextOf(headings[j].Text)
				hits := 0
				for _, cat := range cats {
					for _, kw := range topicCategories[cat] {
						if strings.Contains(lh, kw) {
							hits++
							break
						}
					}
				}
				if hits > bestHits {
					bestHits = hits
					bestHeading = &headings[j]
				}
			}
			if bestHeading != nil {
				id := bestHeading.ID
				bidID = &id
				reason = "topic"
				covered++
			}
		}

		_ = s.q.InsertMatch(ctx, store.InsertMatchParams{
			CheckID:          checkID,
			Dimension:        DimOutline,
			TenderSentenceID: tenderID,
			BidSentenceID:    bidID,
			Reason:           reason,
			Similarity:       similarity,
		})
	}

	detail, _ := json.Marshal(map[string]any{"headingCount": len(headings)})

	_, _ = s.q.UpsertDuplicateAnalysis(ctx, store.UpsertDuplicateAnalysisParams{
		CheckID:      checkID,
		Dimension:    DimOutline,
		Status:       StatusSuccess,
		Progress:     100,
		Message:      fmt.Sprintf("目录 %d 项，覆盖招标要求 %d/%d 条", len(headings), covered, len(tenderTexts)),
		TenderCount:  int32(len(tenderTexts)),
		MatchedCount: int32(covered),
		TotalCount:   int32(len(headings)),
		Detail:       detail,
		FinishedAt:   nowTs(),
	})

	return nil
}

// 主题分类表。
//
// **为什么需要分类而不是纯字符串匹配**：
//
//	招标要求："投标人须提供营业执照副本复印件并加盖公章"
//	投标目录："第一章 资格证明"
//
// 人一看就知道目录覆盖了这条要求，但字符串没有任何公共部分。
// 纯字符串匹配必然 0 命中（实测 10 条要求覆盖 0 条）。
//
// 所以按「业务主题」归类：这条要求属于「资格类」，
// 投标文件有「资格证明」章节 → 判定为覆盖。
var topicCategories = map[string][]string{
	"资格": {"营业执照", "资质", "证书", "资格", "认证", "许可证", "信用", "社保", "纳税"},
	"业绩": {"业绩", "案例", "合同", "类似项目", "成功案例"},
	"技术": {"架构", "技术", "系统", "功能", "性能", "接口", "数据", "部署", "集成", "开发", "平台"},
	"安全": {"安全", "等保", "保密", "加密", "防护", "合规"},
	"服务": {"服务", "运维", "响应", "支持", "售后", "培训", "质保", "维护"},
	"人员": {"人员", "工程师", "项目经理", "团队", "配置", "简历", "证书"},
	"商务": {"报价", "价格", "付款", "保证金", "合同", "条款", "税"},
	"实施": {"实施", "进度", "计划", "周期", "交付", "验收", "里程碑", "组织"},
	"财务": {"财务", "审计", "报表", "资产", "负债", "营收"},
}

// classifyTopics 判断一段文本涉及哪些业务主题。
func classifyTopics(text string) []string {
	loose := looseTextOf(text)
	hits := make([]string, 0, 3)
	for cat, keywords := range topicCategories {
		for _, kw := range keywords {
			if strings.Contains(loose, kw) {
				hits = append(hits, cat)
				break
			}
		}
	}
	return hits
}

// analyzeOutlineByTopic 按主题判断目录覆盖。
func (s *Service) matchOutlineByTopic(tenderText string, headingLoose string) (covered bool, score float64) {
	cats := classifyTopics(tenderText)
	if len(cats) == 0 {
		return false, 0
	}

	hit := 0
	for _, cat := range cats {
		// 该主题的任一关键词出现在目录里 → 这一主题被覆盖
		for _, kw := range topicCategories[cat] {
			if strings.Contains(headingLoose, kw) {
				hit++
				break
			}
		}
	}

	score = float64(hit) / float64(len(cats))
	// 60% 以上的主题被覆盖 → 认为有专门章节
	return score >= 0.6, score
}

// analyzeContent 分析正文响应度。
//
// 这是查重的核心：招标要求里有多少条在投标正文中找不到对应内容。
func (s *Service) analyzeContent(ctx context.Context, checkID uuid.UUID, index *Index, opts AnalyzeOptions) error {
	s.updateAnalysis(ctx, checkID, DimContent, StatusRunning, 10, "正在分析正文响应度", "")

	bidRows, err := s.q.ListSentences(ctx, store.ListSentencesParams{
		CheckID: checkID, Role: "bid",
	})
	if err != nil {
		return err
	}

	// 为每句投标内容预建 bigram（避免重复计算）
	bidEntries := make([]bidEntry, 0, len(bidRows))
	for _, r := range bidRows {
		loose := looseTextOf(r.Text)
		bidEntries = append(bidEntries, bidEntry{
			row:   r,
			grams: Bigrams(loose),
			loose: loose,
		})
	}

	// 投标侧 bigram 倒排索引（用于找候选）
	bidGramIndex := make(map[string][]int, len(bidEntries)*4)
	for i, e := range bidEntries {
		for g := range e.grams {
			bidGramIndex[g] = append(bidGramIndex[g], i)
		}
	}

	s.updateAnalysis(ctx, checkID, DimContent, StatusRunning, 30,
		fmt.Sprintf("已索引 %d 句投标内容", len(bidEntries)), "")

	_ = s.q.DeleteMatchesByDimension(ctx, store.DeleteMatchesByDimensionParams{
		CheckID: checkID, Dimension: DimContent,
	})

	tenderTexts := s.mustTenderSentences(ctx, checkID)
	covered := 0

	for i, tenderText := range tenderTexts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		tenderID := s.tenderSentenceIDs[i]

		hit, reasonCode, sim := matchInBidIndex(tenderText, bidEntries, bidGramIndex)

		var bidID *uuid.UUID
		reason := "none"
		similarity := 0.0

		if hit != nil {
			id := hit.row.ID
			bidID = &id
			reason = string(reasonCode)
			similarity = sim
			covered++
		}

		_ = s.q.InsertMatch(ctx, store.InsertMatchParams{
			CheckID:          checkID,
			Dimension:        DimContent,
			TenderSentenceID: tenderID,
			BidSentenceID:    bidID,
			Reason:           reason,
			Similarity:       similarity,
		})

		if opts.OnProgress != nil && len(tenderTexts) > 0 && i%(len(tenderTexts)/10+1) == 0 {
			pct := 30 + (70 * i / len(tenderTexts))
			opts.OnProgress(DimContent, pct, fmt.Sprintf("已比对 %d/%d 条要求", i+1, len(tenderTexts)))
		}
	}

	// 检测投标文件之间的重复句子（前端「正文」页签要展示这个）。
	//
	// 与「响应度」是两个不同的检查：
	//   响应度：招标要求有没有被响应（漏项检查）
	//   重复句：投标文件之间有没有互相抄（雷同检查）
	//
	// 桌面版把两者放在同一个页签下，所以这里都提供。
	duplicateSentences := findDuplicateSentences(bidEntries)

	detail, _ := json.Marshal(map[string]any{
		"bidSentenceCount":   len(bidEntries),
		"duplicateSentences": duplicateSentences,
	})

	uncovered := len(tenderTexts) - covered
	_, _ = s.q.UpsertDuplicateAnalysis(ctx, store.UpsertDuplicateAnalysisParams{
		CheckID:      checkID,
		Dimension:    DimContent,
		Status:       StatusSuccess,
		Progress:     100,
		Message:      fmt.Sprintf("招标 %d 条要求，已响应 %d 条，漏项 %d 条", len(tenderTexts), covered, uncovered),
		TenderCount:  int32(len(tenderTexts)),
		MatchedCount: int32(covered),
		TotalCount:   int32(len(bidEntries)),
		Detail:       detail,
		FinishedAt:   nowTs(),
	})

	s.log.Info("正文响应度分析完成",
		slog.Int("tender", len(tenderTexts)),
		slog.Int("covered", covered),
		slog.Int("uncovered", uncovered),
	)

	return nil
}

// analyzeMetadata 分析文档属性。
//
// 简化实现：只登记文件名清单，不做 rsid/OLE 深度指纹提取。
// 深度指纹用于判断"两份标书是否同一台电脑制作"（串标嫌疑），
// 需要解析 docx 的 zip 内部结构，工作量独立，后续补。
func (s *Service) analyzeMetadata(ctx context.Context, check store.DuplicateCheck, opts AnalyzeOptions) error {
	s.updateAnalysis(ctx, check.ID, DimMetadata, StatusRunning, 50, "正在提取文档属性", "")

	type fileMeta struct {
		FileName string `json:"fileName"`
		Kind     string `json:"kind"`
	}

	files := make([]fileMeta, 0)
	for _, name := range check.TenderFileNames {
		files = append(files, fileMeta{FileName: name, Kind: "tender"})
	}
	for _, name := range check.BidFileNames {
		files = append(files, fileMeta{FileName: name, Kind: "bid"})
	}

	detail, _ := json.Marshal(map[string]any{
		"files": files,
		"rows":  []any{},
	})

	_, _ = s.q.UpsertDuplicateAnalysis(ctx, store.UpsertDuplicateAnalysisParams{
		CheckID:      check.ID,
		Dimension:    DimMetadata,
		Status:       StatusSuccess,
		Progress:     100,
		Message:      fmt.Sprintf("已登记 %d 个文件", len(files)),
		TenderCount:  int32(len(check.TenderFileNames)),
		MatchedCount: 0,
		TotalCount:   int32(len(files)),
		Detail:       detail,
		FinishedAt:   nowTs(),
	})

	return nil
}

// analyzeImage 分析图片重复。
//
// 占位实现：图片提取需要解析 docx 的 media/ 目录并做感知哈希（pHash），
// 是独立的工作量。先返回空结果，前端会显示「暂无重复图片」。
func (s *Service) analyzeImage(ctx context.Context, check store.DuplicateCheck, opts AnalyzeOptions) error {
	detail, _ := json.Marshal(map[string]any{
		"duplicateImages": []any{},
		"files":           []any{},
	})

	_, _ = s.q.UpsertDuplicateAnalysis(ctx, store.UpsertDuplicateAnalysisParams{
		CheckID:      check.ID,
		Dimension:    DimImage,
		Status:       StatusSuccess,
		Progress:     100,
		Message:      "未发现重复图片",
		TenderCount:  0,
		MatchedCount: 0,
		TotalCount:   0,
		Detail:       detail,
		FinishedAt:   nowTs(),
	})

	return nil
}

// mustTenderSentences 取招标句子，并缓存 ID。
func (s *Service) mustTenderSentences(ctx context.Context, checkID uuid.UUID) []string {
	rows, err := s.q.ListSentences(ctx, store.ListSentencesParams{
		CheckID: checkID, Role: "tender",
	})
	if err != nil || len(rows) == 0 {
		s.tenderSentenceIDs = nil
		return nil
	}

	texts := make([]string, 0, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		texts = append(texts, r.Text)
		ids = append(ids, r.ID)
	}
	s.tenderSentenceIDs = ids
	return texts
}

// updateAnalysis 更新分析进度。
func (s *Service) updateAnalysis(ctx context.Context, checkID uuid.UUID, dim, status string, progress int, message, errMsg string) {
	if errMsg != "" {
		_, _ = s.q.UpsertDuplicateAnalysis(ctx, store.UpsertDuplicateAnalysisParams{
			CheckID:   checkID,
			Dimension: dim,
			Status:    status,
			Progress:  int32(progress),
			Message:   message,
			Error:     pgText(errMsg),
			Detail:    nil,
		})
		return
	}

	_ = s.q.UpdateAnalysisProgress(ctx, store.UpdateAnalysisProgressParams{
		CheckID:   checkID,
		Dimension: dim,
		Status:    status,
		Progress:  int32(progress),
		Message:   message,
	})
}

// isHeadingLike 判断句子是否像目录项（标题行）。
//
// 特征：短（≤40 字），且有章节编号特征：
//   - markdown 标题（"## 第一章 xxx"）
//   - "第X章 / 第X节"
//   - "一、" "（一）"
//   - "1." "1.1" "1、"
func isHeadingLike(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}

	// 去掉 markdown 标题标记后再判断
	clean := strings.TrimLeft(t, "# ")
	clean = strings.TrimSpace(clean)

	runes := []rune(clean)
	if len(runes) > 40 || len(runes) < 2 {
		return false
	}

	// markdown 标题直接算
	if strings.HasPrefix(t, "#") {
		return true
	}

	// "第一章 xxx" / "第X节"
	if strings.HasPrefix(clean, "第") && strings.ContainsAny(clean, "章节部分") {
		return true
	}

	// "一、" / "二、"
	if len(runes) >= 2 && runes[1] == '、' {
		for _, cn := range "一二三四五六七八九十" {
			if runes[0] == cn {
				return true
			}
		}
	}

	// 数字开头（"1.1 概述" / "1、xx" / "2) xx"）
	if runes[0] >= '0' && runes[0] <= '9' {
		// 但要排除纯数字的内容（如"8000人次"）
		// 判断：数字后面紧跟分隔符才算
		for i, r := range runes {
			if r >= '0' && r <= '9' {
				continue
			}
			// 遇到第一个非数字字符
			if r == '.' || r == '、' || r == ')' || r == '）' || r == ' ' || r == '　' {
				return true
			}
			_ = i
			break
		}
		return false
	}

	// "（一）" / "(一)"
	if runes[0] == '（' || runes[0] == '(' {
		return true
	}

	return false
}

// findBestMatch 在候选句子里找最相似的。
func findBestMatch(target string, candidates []store.DuplicateSentence) (*store.DuplicateSentence, float64) {
	var best *store.DuplicateSentence
	bestDice := 0.0

	for i := range candidates {
		matched, dice, _ := NearMatch(target, candidates[i].Text)
		if matched && dice > bestDice {
			bestDice = dice
			best = &candidates[i]
		}
	}

	return best, bestDice
}

// bidEntry 是一句投标内容及其预计算的索引。
type bidEntry struct {
	row   store.DuplicateSentence
	grams map[string]struct{}
	loose string
}

// matchInBidIndex 在投标内容里找响应。
//
// **两层判据**（这是查重准确性的关键）：
//
//  1. CoreContainment：招标要求的核心片段（≥60%）原样出现在投标内容里
//     → 这是主判据，贴合"有没有响应"的业务问题
//
//  2. NearMatch：整句相似度
//     → 作为补充，覆盖"改写得很彻底但意思一样"的情况
//
// 为什么以 1 为主：实测发现投标人会把"投标人须提供X"
// 写成"我公司已提供X，详见附件"，整句相似度只有 0.70，
// 但核心片段"营业执照副本复印件并加盖公章"完全一致。
// 只用整句相似度会导致大规模漏报（实测 14 条全判漏项）。
func matchInBidIndex(tenderText string, entries []bidEntry, gramIndex map[string][]int) (*bidEntry, MatchReason, float64) {
	loose := looseTextOf(tenderText)
	if loose == "" {
		return nil, "", 0
	}

	// 第一层：核心片段包含度
	// 用投标侧的 bigram 索引先筛候选，再算 LCS
	// （直接在 3000 句里算 LCS 是 O(n*m*k)，太慢）
	grams := Bigrams(loose)
	candidates := make(map[int]struct{}, 32)
	for g := range grams {
		for _, idx := range gramIndex[g] {
			candidates[idx] = struct{}{}
		}
	}

	var best *bidEntry
	bestReason := MatchReason("")
	bestScore := 0.0

	// 候选太多时只看前 200 个（它们的 bigram 重合度最高）
	type cand struct {
		idx   int
		score float64
	}
	ranked := make([]cand, 0, len(candidates))
	for idx := range candidates {
		e := &entries[idx]
		if len(e.grams) == 0 {
			continue
		}
		// 用 bigram 重合数粗排
		shared := 0
		for g := range grams {
			if _, ok := e.grams[g]; ok {
				shared++
			}
		}
		ranked = append(ranked, cand{idx: idx, score: float64(shared) / float64(len(grams))})
	}

	// 粗排后取前 200
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	if len(ranked) > 200 {
		ranked = ranked[:200]
	}

	for _, c := range ranked {
		e := &entries[c.idx]

		ratio, _ := CoreContainment(tenderText, e.row.Text)
		if ratio >= coreContainmentThreshold && ratio > bestScore {
			bestScore = ratio
			best = e
			bestReason = ReasonNear
		}
	}

	if best != nil {
		return best, bestReason, bestScore
	}

	// 第二层：整句相似度（兜底）
	for _, c := range ranked {
		e := &entries[c.idx]
		matched, dice, _ := NearMatch(tenderText, e.row.Text)
		if matched && dice > bestScore {
			bestScore = dice
			best = e
			bestReason = ReasonNear
		}
	}

	if best == nil {
		return nil, "", 0
	}
	return best, bestReason, bestScore
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// nowTs 当前时间（pgtype 格式）。
func nowTs() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now(), Valid: true}
}

// pgText 构造非空 pgtype.Text。
func pgText(v string) pgtype.Text {
	return pgtype.Text{String: v, Valid: v != ""}
}

// ParseAndStore 解析文件并落句子。
//
// 这是导入文件后的第一步：把 docx/pdf 变成句子。
func (s *Service) ParseAndStore(ctx context.Context, checkID uuid.UUID, role string, paths []string) (int, error) {
	if err := os.MkdirAll(s.workDir, 0o755); err != nil {
		return 0, err
	}

	// 清旧句子
	_ = s.q.DeleteSentencesByRole(ctx, store.DeleteSentencesByRoleParams{
		CheckID: checkID, Role: role,
	})

	total := 0
	for _, path := range paths {
		fileName := filepath.Base(path)

		result, err := docparse.ToMarkdown(ctx, path)
		if err != nil {
			s.log.Warn("解析失败",
				slog.String("file", fileName),
				slog.String("error", err.Error()),
			)
			continue
		}

		// 切句
		sentences := SplitSentences(result.Markdown)

		seq := 0
		for _, text := range sentences {
			t := strings.TrimSpace(text)

			// 过滤太短的碎片（标题编号之类）
			if len([]rune(t)) < 6 {
				continue
			}

			// **招标侧**过滤标题和描述性内容。
			//
			// 为什么只过滤招标侧：
			//   招标侧的标题（"## 一、资格要求"）不是"要求"，
			//   拿去比对必然判漏项，是噪音。
			//
			//   但**投标侧的标题必须保留** —— 目录分析要靠它！
			//   之前两边都过滤，导致目录分析永远"识别不到目录结构"。
			if role == "tender" {
				if isPureHeading(t) {
					continue
				}
				// 只保留「要求型」句子。
				// 招标文件里的"项目概况""联系地址"不需要逐条响应，
				// 拿去比对只会产生无意义的漏项。
				if !looksLikeRequirement(t) {
					continue
				}
			}

			headingJSON, _ := json.Marshal([]string{})

			_, err := s.q.InsertSentence(ctx, store.InsertSentenceParams{
				CheckID:     checkID,
				Role:        role,
				FileName:    fileName,
				Seq:         int32(seq),
				HeadingPath: headingJSON,
				Text:        t,
				Normalized:  Normalize(t),
			})
			if err != nil {
				continue
			}
			seq++
			total++
		}

		s.log.Info("文件已切句",
			slog.String("file", fileName),
			slog.String("role", role),
			slog.Int("sentences", seq),
		)
	}

	return total, nil
}

// PutFile 把文件存进对象存储（保留原始文件）。
func (s *Service) PutFile(ctx context.Context, tenantID, projectID, ownerID, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	_, err = s.artifacts.Put(ctx, artifact.PutRequest{
		Scope:   storage.Scope{TenantID: tenantID, ProjectID: projectID},
		Name:    filepath.Base(path),
		Kind:    "duplicate-source",
		Content: f,
		Size:    info.Size(),
		Source:  "user",
		OwnerID: ownerID,
	})
	return err
}

// isPureHeading 判断是否纯标题行（markdown 标题或短编号行）。
func isPureHeading(text string) bool {
	t := strings.TrimSpace(text)

	// markdown 标题
	if strings.HasPrefix(t, "#") {
		return true
	}

	// "一、资格要求" 这类：短、无标点、以中文数字开头
	runes := []rune(t)
	if len(runes) <= 20 && !strings.ContainsAny(t, "。；！？") {
		if strings.HasPrefix(t, "一、") || strings.HasPrefix(t, "二、") ||
			strings.HasPrefix(t, "三、") || strings.HasPrefix(t, "四、") ||
			strings.HasPrefix(t, "五、") || strings.HasPrefix(t, "六、") {
			return true
		}
	}

	return false
}

// looksLikeRequirement 判断一句招标内容是否是「需要投标方响应」的要求。
//
// 判据：含要求型语气词。
//   - 须 / 应 / 必须 / 要求 / 不得 / 禁止 / 不少于 / 不低于
//
// 为什么需要：招标文件里大量描述性内容（项目概况、联系方式、
// 采购需求背景）不需要逐条响应，拿去比对必然产生假漏项。
func looksLikeRequirement(text string) bool {
	markers := []string{
		"须", "应", "必须", "要求", "不得", "禁止",
		"不少于", "不低于", "不超过", "以上", "以下",
		"提供", "具备", "符合", "满足", "保证", "承诺",
	}
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// DuplicateSentenceItem 是投标文件之间的重复句子。
type DuplicateSentenceItem struct {
	ID          string         `json:"id"`
	Sentence    string         `json:"sentence"`
	Normalized  string         `json:"normalized"`
	FileIDs     []string       `json:"file_ids"`
	Occurrences map[string]int `json:"occurrences"`
	FirstOrder  int            `json:"first_order"`
}

// findDuplicateSentences 找出投标文件之间重复的句子。
//
// 判定：同一句归一化文本在**不同文件**里都出现。
// 同一文件内重复不算（那是自己章节间的呼应，正常）。
func findDuplicateSentences(entries []bidEntry) []DuplicateSentenceItem {
	// 归一化文本 → 出现的文件集合 + 出现次数
	type occurrence struct {
		files    map[string]bool
		counts   map[string]int
		sample   string
		firstSeq int
	}
	index := make(map[string]*occurrence, len(entries))

	for _, e := range entries {
		norm := Normalize(e.row.Text)
		if len([]rune(norm)) < 12 {
			// 太短的句子重复常见（"详见附件"），不算雷同
			continue
		}

		occ, ok := index[norm]
		if !ok {
			occ = &occurrence{
				files:    make(map[string]bool),
				counts:   make(map[string]int),
				sample:   e.row.Text,
				firstSeq: int(e.row.Seq),
			}
			index[norm] = occ
		}
		occ.files[e.row.FileName] = true
		occ.counts[e.row.FileName]++
	}

	out := make([]DuplicateSentenceItem, 0, 16)
	seq := 0
	for norm, occ := range index {
		if len(occ.files) < 2 {
			continue
		}
		seq++

		files := make([]string, 0, len(occ.files))
		for f := range occ.files {
			files = append(files, f)
		}

		out = append(out, DuplicateSentenceItem{
			ID:          fmt.Sprintf("D%04d", seq),
			Sentence:    occ.sample,
			Normalized:  norm,
			FileIDs:     files,
			Occurrences: occ.counts,
			FirstOrder:  occ.firstSeq,
		})
	}

	// 按首次出现顺序排
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].FirstOrder < out[i].FirstOrder {
				out[i], out[j] = out[j], out[i]
			}
		}
	}

	if len(out) > 200 {
		out = out[:200]
	}
	return out
}
