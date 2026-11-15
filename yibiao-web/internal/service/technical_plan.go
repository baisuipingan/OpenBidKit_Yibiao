package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/agent/outline"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/store"
)

// TechnicalPlanService 处理技术方案的读取与状态维护。
//
// 当前只实现「读取状态」，因为这是前端第一个页面需要的。
// 后续逐步补：导入招标文件、保存目录、生成正文等。
type TechnicalPlanService struct {
	pool      *pgxpool.Pool
	q         *store.Queries
	artifacts *artifact.Service
	log       *slog.Logger
}

// NewTechnicalPlanService 创建服务。
func NewTechnicalPlanService(pool *pgxpool.Pool, artifacts *artifact.Service, log *slog.Logger) *TechnicalPlanService {
	return &TechnicalPlanService{
		pool:      pool,
		q:         store.New(pool),
		artifacts: artifacts,
		log:       log,
	}
}

// OutlineData 是目录树。
//
// 结构对齐前端 OutlineData：{ outline: OutlineItem[] }
type OutlineData struct {
	Outline []OutlineItem `json:"outline"`
}

// OutlineItem 是目录节点。
//
// 前端用它渲染树、统计叶子、判断内容模式。
type OutlineItem struct {
	ID          string        `json:"id"`
	Number      string        `json:"number"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	Content     string        `json:"content,omitempty"`
	ContentMode string        `json:"content_mode"`
	Children    []OutlineItem `json:"children,omitempty"`
	// Level 层级（1 起）
	Level      int   `json:"level"`
	WordTarget int   `json:"word_target,omitempty"`
	SortOrder  int32 `json:"sortOrder"`
}

// TenderFile 同时覆盖前端的：
//   - TechnicalPlanTenderSourceFile（tenderFiles[]）
//   - TechnicalPlanTenderFile（tenderFile 单数）
//   - TechnicalPlanOriginalPlanFile（originalPlanFile）
//
// 单数对象比列表项多 selectedSection* / originalMarkdown*；
// 多出来的 id 字段对前端无害。缺 tenderFile 会让「下一步」一直禁用。
type TenderFile struct {
	// ID 是稳定标识。Web 版没有本地路径，用文件名哈希做 ID。
	ID       string `json:"id,omitempty"`
	FileName string `json:"fileName"`
	// MarkdownPath 前端在用；Web 版常为空路径，用资产名占位（如 tender.md）
	MarkdownPath          string `json:"markdownPath"`
	MarkdownChars         int32  `json:"markdownChars"`
	ContentHash           string `json:"contentHash"`
	OriginalMarkdownPath  string `json:"originalMarkdownPath,omitempty"`
	OriginalMarkdownChars int32  `json:"originalMarkdownChars,omitempty"`
	OriginalContentHash   string `json:"originalContentHash,omitempty"`
	ParserLabel           string `json:"parserLabel,omitempty"`
	SourceDocxPath        string `json:"sourceDocxPath,omitempty"`
	ImportedAt            string `json:"importedAt,omitempty"`
	SelectedSectionID     string `json:"selectedSectionId,omitempty"`
	SelectedSectionTitle  string `json:"selectedSectionTitle,omitempty"`
	UpdatedAt             string `json:"updatedAt"`
}

// PlanState 是前端 `technicalPlan.loadState()` 期望的返回结构。
//
// 字段名必须与桌面版 `src/shared/types/ipc.ts` 里的定义一致，
// 否则前端读不到值。这里只放已有数据对应的部分，
// 后续字段随功能一起补。
type PlanState struct {
	Step string `json:"step"`
	// TenderFiles 是招标文件列表。
	//
	// **前端必须这个字段**（TechnicalPlanTenderSourceFile[]）：
	// DocumentAnalysisPage 用 state.tenderFiles[0].id 判断是否已导入。
	// 只给 tenderFileName 会让前端以为「尚未导入」。
	TenderFiles               []TenderFile `json:"tenderFiles"`
	TenderFileName            string       `json:"tenderFileName"`
	TenderMarkdownChars       int32        `json:"tenderMarkdownChars"`
	TenderParserLabel         string       `json:"tenderParserLabel"`
	TenderImportedAt          *string      `json:"tenderImportedAt,omitempty"`
	OriginalPlanFileName      string       `json:"originalPlanFileName"`
	OriginalPlanMarkdownChars int32        `json:"originalPlanMarkdownChars"`
	OriginalPlanParserLabel   string       `json:"originalPlanParserLabel"`
	OriginalPlanImportedAt    *string      `json:"originalPlanImportedAt,omitempty"`
	BidSections               any          `json:"bidSections"`
	SelectedSectionID         string       `json:"selectedSectionId"`
	SelectedSectionTitle      string       `json:"selectedSectionTitle"`
	// ===== 以下字段前端 TechnicalPlanState 必需 =====
	// 缺任何一个都会让前端读 undefined 报错。

	// OutlineData 是目录树（前端用它渲染目录编辑页和正文页）
	OutlineData *OutlineData `json:"outlineData"`

	TenderFile                 *TenderFile    `json:"tenderFile"`
	OriginalPlanFile           *TenderFile    `json:"originalPlanFile"`
	ProjectOverview            string         `json:"projectOverview"`
	TechRequirements           string         `json:"techRequirements"`
	BidAnalysisMode            string         `json:"bidAnalysisMode"`
	BidAnalysisSelectedIDs     []string       `json:"bidAnalysisSelectedTaskIds"`
	BidAnalysisTasks           map[string]any `json:"bidAnalysisTasks"`
	BidAnalysisProgress        int            `json:"bidAnalysisProgress"`
	BidAnalysisTask            map[string]any `json:"bidAnalysisTask"`
	BidSectionMode             string         `json:"bidSectionMode"`
	BidSectionExtractionStatus string         `json:"bidSectionExtractionStatus"`
	OutlineMode                string         `json:"outlineMode"`
	OutlineExpansionMode       string         `json:"outlineExpansionMode"`
	OutlineWordControlOptions  map[string]any `json:"outlineWordControlOptions"`
	OutlineWordControlSnapshot map[string]any `json:"outlineWordControlSnapshot"`
	OutlineGenerationTask      map[string]any `json:"outlineGenerationTask"`
	ReferenceKnowledgeIDs      []string       `json:"referenceKnowledgeDocumentIds"`
	GlobalFactsMode            string         `json:"globalFactsMode"`
	GlobalFacts                []any          `json:"globalFacts"`
	ExportTemplateID           string         `json:"exportTemplateId"`
	ExportTemplateScope        string         `json:"exportTemplateScope"`
	ContentGenerationOptions   map[string]any `json:"contentGenerationOptions"`
	ContentGenerationTask      map[string]any `json:"contentGenerationTask"`
	ContentGenerationSections  map[string]any `json:"contentGenerationSections"`
	// ContentGenerationRuntime 是正文生成运行时状态。
	// 前端从这里读 section_words 显示每节字数。
	ContentGenerationRuntime map[string]any `json:"contentGenerationRuntime"`
	ContentGenerationPlans   map[string]any `json:"contentGenerationPlans"`
	BidTemplateExists        bool           `json:"bidTemplateExists"`
	BidTemplateChapterIDs    []string       `json:"bidTemplateChapterIds"`

	OutlineProjectName     string          `json:"outlineProjectName"`
	OutlineProjectOverview string          `json:"outlineProjectOverview"`
	Outline                []OutlineNode   `json:"outline"`
	ContentSections        []ContentStatus `json:"contentSections"`
}

// OutlineNode 是目录树节点。
//
// 注意：nodeId 是稳定 UUID，number 只是展示用的章节编号，
// 不能从 ID 推算编号或层级（见 AGENTS.md）。
type OutlineNode struct {
	NodeID       string  `json:"nodeId"`
	ParentNodeID *string `json:"parentNodeId"`
	SortOrder    int32   `json:"sortOrder"`
	Level        int32   `json:"level"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	ContentMode  *string `json:"contentMode,omitempty"`
	RuleBasis    *string `json:"ruleBasis,omitempty"`
	Content      string  `json:"content"`
}

// ContentStatus 是正文小节状态。
type ContentStatus struct {
	NodeID string `json:"nodeId"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// LoadState 读取技术方案完整状态。
//
// 首次访问时自动初始化 meta 与 generation_config 行，
// 对应桌面版的「进入页面即创建单例记录」。
func (s *TechnicalPlanService) LoadState(ctx context.Context, tenantID, projectID string) (*PlanState, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}

	// 归属校验：项目必须属于当前租户
	if err := s.ensureProject(ctx, tid, pid); err != nil {
		return nil, err
	}

	meta, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid,
		TenantID:  tid,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化技术方案失败: %w", err)
	}

	if _, err := s.q.EnsureGenerationConfig(ctx, store.EnsureGenerationConfigParams{
		ProjectID: pid,
		TenantID:  tid,
	}); err != nil {
		return nil, fmt.Errorf("初始化生成配置失败: %w", err)
	}

	nodes, err := s.q.ListOutlineNodes(ctx, store.ListOutlineNodesParams{
		ProjectID: pid,
		TenantID:  tid,
	})
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}

	sections, err := s.q.ListContentSections(ctx, store.ListContentSectionsParams{
		ProjectID: pid,
		TenantID:  tid,
	})
	if err != nil {
		return nil, fmt.Errorf("读取正文状态失败: %w", err)
	}

	// 生成配置：不读的话前端拿到的 outlineMode / globalFactsMode 等全是空，
	// 生成设置页会显示「范围·undefined」这种坏文本。
	cfg, err := s.q.GetGenerationConfig(ctx, store.GetGenerationConfigParams{
		ProjectID: pid,
		TenantID:  tid,
	})
	if err != nil {
		return nil, fmt.Errorf("读取生成配置失败: %w", err)
	}

	return toPlanState(meta, nodes, sections, cfg), nil
}

// UpdateStep 更新当前步骤。
// validSteps 是合法的步骤标识。
//
// 为什么要白名单：之前没有校验，任意字符串都能写进 step 列，
// 结果库里出现了 "documents" 这种前端不认识的值，
// 页面渲染时匹配不到任何分支 → 整页空白。
// 必须以客户端的 TechnicalPlanStep 类型为准：
//   client/src/features/technical-plan/types.ts
// 两边不一致会导致 updateStep 报「无效的步骤」，页面卡在旧步骤。
var validSteps = map[string]bool{
	"document-analysis":   true, // 选择标书
	"generation-settings": true, // 生成设置
	"bid-analysis":        true, // 招标文件解析
	"outline-generation":  true, // 目录生成
	"global-facts":        true, // 全局事实设定
	"content-edit":        true, // 生成正文
	"expand":              true, // 扩写改写
}

func (s *TechnicalPlanService) UpdateStep(ctx context.Context, tenantID, projectID, step string) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}
	// 兼容旧值：早期版本用过这几组命名，统一映射到当前值。
	// 映射表要和 validSteps 一起维护，否则改步骤名时旧数据会卡住。
	legacySteps := map[string]string{
		"documents":          "document-analysis",
		"content-generation": "content-edit",
		"export":             "content-edit",
	}
	if mapped, ok := legacySteps[step]; ok {
		step = mapped
	}
	if step == "" {
		return httpx.BadRequest("步骤不能为空")
	}
	if !validSteps[step] {
		return httpx.BadRequest("无效的步骤: " + step)
	}
	if err := s.ensureProject(ctx, tid, pid); err != nil {
		return err
	}
	if _, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	}); err != nil {
		return err
	}
	return s.q.UpdateTechnicalPlanStep(ctx, store.UpdateTechnicalPlanStepParams{
		ProjectID: pid, TenantID: tid, Step: step,
	})
}

// ensureProject 校验项目存在且属于当前租户。
//
// 每个业务入口都必须调用它。这一步是越权防护的关键：
// 前端传的 project_id 可能属于别的租户，必须查库确认。
func (s *TechnicalPlanService) ensureProject(ctx context.Context, tenantID, projectID uuid.UUID) error {
	_, err := s.q.GetProject(ctx, store.GetProjectParams{
		ID:       projectID,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 故意不区分「不存在」与「无权限」，避免泄露项目存在性
			return httpx.NotFound("项目不存在或无权访问")
		}
		return fmt.Errorf("校验项目归属失败: %w", err)
	}
	return nil
}

func (s *TechnicalPlanService) resolve(tenantID, projectID string) (uuid.UUID, uuid.UUID, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return uuid.Nil, uuid.Nil, httpx.Unauthorized("登录态异常，请重新登录")
	}
	pid, err := uuid.Parse(projectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, httpx.BadRequest("项目标识格式非法")
	}
	return tid, pid, nil
}

func toPlanState(
	meta store.TechnicalPlanMetum,
	nodes []store.TechnicalPlanOutlineNode,
	sections []store.TechnicalPlanContentSection,
	cfg store.TechnicalPlanGenerationConfig,
) *PlanState {
	// 归一化历史脏数据：早期写入的 "documents" 不是合法步骤名，
	// 前端按 "document-analysis" 匹配渲染分支，值不对就整页空白。
	step := meta.Step
	if step == "" || step == "documents" {
		step = "document-analysis"
	}

	tenderFiles := parseTenderFiles(meta.TenderFiles, meta)

	st := &PlanState{
		Step:        step,
		TenderFiles: tenderFiles,
		// 单数 tenderFile / originalPlanFile：前端「下一步」和后续步骤门禁读它们。
		// 不能只靠 tender_markdown_path——Web 导入把路径写成空串，改用资产引用。
		TenderFile:       buildSingularTenderFile(meta, tenderFiles),
		OriginalPlanFile: buildOriginalPlanFile(meta),
		// 目录树：前端渲染目录/正文页必需
		OutlineData: buildOutlineData(nodes),
		// 以下字段给默认值，避免前端读 undefined
		BidAnalysisTasks:          map[string]any{},
		BidAnalysisSelectedIDs:    []string{},
		BidAnalysisTask:           nil,
		OutlineGenerationTask:     nil,
		ContentGenerationTask:     nil,
		ReferenceKnowledgeIDs:     append([]string{}, meta.ReferenceKnowledgeDocumentIds...),
		GlobalFacts:               []any{},
		ContentGenerationSections: map[string]any{},
		ContentGenerationRuntime:  buildContentRuntime(nodes),
		ContentGenerationPlans:    map[string]any{},
		BidTemplateChapterIDs:     []string{},
		TenderFileName:            firstTenderFileName(meta.TenderFiles),
		TenderMarkdownChars:       meta.TenderMarkdownChars,
		TenderParserLabel:         meta.TenderParserLabel,
		OriginalPlanFileName:      meta.OriginalPlanFileName,
		OriginalPlanMarkdownChars: meta.OriginalPlanMarkdownChars,
		OriginalPlanParserLabel:   meta.OriginalPlanParserLabel,
		SelectedSectionID:         meta.SelectedSectionID,
		SelectedSectionTitle:      meta.SelectedSectionTitle,
		OutlineProjectName:        meta.OutlineProjectName,
		OutlineProjectOverview:    meta.OutlineProjectOverview,
		BidSections:               rawJSON(meta.BidSections),
		Outline:                   make([]OutlineNode, 0, len(nodes)),
		ContentSections:           make([]ContentStatus, 0, len(sections)),

		// ===== 生成配置（technical_plan_generation_config 表）=====
		BidAnalysisMode:      cfg.BidAnalysisMode,
		BidSectionMode:       cfg.BidSectionMode,
		OutlineMode:          cfg.OutlineMode,
		OutlineExpansionMode: cfg.OutlineExpansionMode,
		GlobalFactsMode:      cfg.GlobalFactsMode,
		ExportTemplateID:     cfg.ExportTemplateID,
		ExportTemplateScope:  cfg.ExportTemplateScope,
		// 字数控制：前端生成设置页的「字数」页签读这三个值
		OutlineWordControlOptions: map[string]any{
			"minimumWords": cfg.MinimumWords,
			"maximumWords": cfg.MaximumWords,
			"sectionWords": cfg.SectionWords,
		},
		// 目录生效时的字数快照：下一步门禁读它；缺则「请重新生成目录」
		OutlineWordControlSnapshot: parseWordControlSnapshot(meta.OutlineWordControlSnapshot),
		ContentGenerationOptions: map[string]any{
			"useAiImages":           cfg.UseAiImages,
			"useMermaidImages":      cfg.UseMermaidImages,
			"useHtmlImages":         cfg.UseHtmlImages,
			"tableRequirement":      cfg.TableRequirement,
			"imageQuantity":         cfg.ImageQuantity,
			"layoutCheck":           cfg.LayoutCheck,
			"useSingleImageLayout":  cfg.UseSingleImageLayout,
			"useImageTextLayout":    cfg.UseImageTextLayout,
			"useThreeImagesLayout":  cfg.UseThreeImagesLayout,
			"useFourImagesLayout":   cfg.UseFourImagesLayout,
			"htmlImageOptimization": cfg.HtmlImageOptimization,
			"wordCountRepair":       cfg.WordCountRepair,
		},
	}

	if meta.TenderImportedAt.Valid {
		s := meta.TenderImportedAt.Time.UTC().Format(time.RFC3339)
		st.TenderImportedAt = &s
	}
	if meta.OriginalPlanImportedAt.Valid {
		s := meta.OriginalPlanImportedAt.Time.UTC().Format(time.RFC3339)
		st.OriginalPlanImportedAt = &s
	}

	// 从 extra 读回 AI 生成的中间状态。
	//
	// 这些字段（全局事实、分析配置、正文选项）结构由前端定义，
	// 存 JSONB 不建列；读的时候必须还原，否则前端拿到的永远是空值。
	if len(meta.Extra) > 0 {
		var extra map[string]any
		if json.Unmarshal(meta.Extra, &extra) == nil {
			if v, ok := extra["globalFacts"].([]any); ok {
				st.GlobalFacts = v
			}
			if v, ok := extra["bidAnalysisTasks"].(map[string]any); ok {
				st.BidAnalysisTasks = v
			}
			if v, ok := extra["bidAnalysisTask"].(map[string]any); ok {
				st.BidAnalysisTask = v
			}
			if v, ok := extra["outlineGenerationTask"].(map[string]any); ok {
				st.OutlineGenerationTask = v
			}
			if v, ok := extra["contentGenerationTask"].(map[string]any); ok {
				st.ContentGenerationTask = v
			}
			if v, ok := extra["bidAnalysisProgress"].(float64); ok {
				st.BidAnalysisProgress = int(v)
			}
			if v, ok := extra["bidAnalysisSelectedTaskIds"].([]any); ok {
				ids := make([]string, 0, len(v))
				for _, item := range v {
					if s, ok := item.(string); ok {
						ids = append(ids, s)
					}
				}
				st.BidAnalysisSelectedIDs = ids
			}
			if v, ok := extra["contentGenerationSections"].(map[string]any); ok {
				st.ContentGenerationSections = v
			}
			if v, ok := extra["contentGenerationPlans"].(map[string]any); ok {
				st.ContentGenerationPlans = v
			}
			if v, ok := extra["contentGenerationOptions"].(map[string]any); ok {
				// extra 覆盖列默认值（兼容旧只写 extra 的路径）
				merged := st.ContentGenerationOptions
				if merged == nil {
					merged = map[string]any{}
				}
				for k, val := range v {
					merged[k] = val
				}
				st.ContentGenerationOptions = merged
			}
			// 分析结果提到顶层，前端 BidAnalysisPage 直接读这两个字段
			if v, ok := extra["projectOverview"].(string); ok && v != "" {
				st.ProjectOverview = v
			}
			if v, ok := extra["techRequirements"].(string); ok && v != "" {
				st.TechRequirements = v
			}
			if v, ok := extra["outlineProjectName"].(string); ok && v != "" {
				st.OutlineProjectName = v
			}
			if v, ok := extra["outlineProjectOverview"].(string); ok && v != "" {
				st.OutlineProjectOverview = v
			}
		}
	}

	for _, n := range nodes {
		var parent *string
		if n.ParentNodeID != nil {
			p := n.ParentNodeID.String()
			parent = &p
		}
		st.Outline = append(st.Outline, OutlineNode{
			NodeID:       n.NodeID.String(),
			ParentNodeID: parent,
			SortOrder:    n.SortOrder,
			Level:        n.Level,
			Title:        n.Title,
			Description:  n.Description,
			ContentMode:  textPtr(n.ContentMode),
			RuleBasis:    textPtr(n.RuleBasis),
			Content:      n.Content,
		})
	}

	for _, sec := range sections {
		st.ContentSections = append(st.ContentSections, ContentStatus{
			NodeID: sec.NodeID.String(),
			Status: sec.Status,
			Error:  sec.Error,
		})
	}
	return st
}

// textPtr 把 pgtype.Text 转成 *string。
// 无效值返回 nil，前端拿到 undefined 而不是空字符串。
func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}

// firstTenderFileName 从 tender_files JSON 里取第一个文件名。
// JSON 结构： [{"fileName":"x.pdf", ...}]
// stripTitleNumber 去掉标题前的章节编号。
//
//	"1.1 项目背景" → "项目背景"
//	"第一章 项目总述" → "第一章 项目总述"（保留，中文编号是标题的一部分）
//	"2.3.1 数据架构" → "数据架构"
func stripTitleNumber(title string) string {
	s := strings.TrimSpace(title)

	// 只处理「数字.数字.」形式的编号，中文「第X章」保留
	i := 0
	hasDot := false
	for i < len(s) {
		c := s[i]
		if c >= '0' && c <= '9' {
			i++
			continue
		}
		if c == '.' {
			hasDot = true
			i++
			continue
		}
		break
	}

	if !hasDot || i == 0 || i >= len(s) {
		return s
	}

	rest := strings.TrimSpace(s[i:])
	if rest == "" {
		return s
	}
	return rest
}

// buildContentRuntime 构造正文生成运行时状态。
//
// 前端用它显示每节字数（contentGenerationRuntime.section_words）。
// Web 版没有进程内的运行时，直接从数据库里的正文统计。
func buildContentRuntime(nodes []store.TechnicalPlanOutlineNode) map[string]any {
	sectionWords := make(map[string]int, len(nodes))
	totalWords := 0

	for _, n := range nodes {
		if n.Content == "" {
			continue
		}
		// 与标书口径一致：去掉 HTML 标签、空白和 Markdown 标记
		w := textutil.CountReadableWords(n.Content)
		if w > 0 {
			sectionWords[n.NodeID.String()] = w
			totalWords += w
		}
	}

	return map[string]any{
		"section_words": sectionWords,
		"total_words":   totalWords,
		// Web 版没有实时任务，phase 固定为 idle
		"phase":  "idle",
		"paused": false,
	}
}

// buildOutlineData 把平铺的节点列表转成前端要的树。
//
// 数据库存的是平铺列表（parent_node_id + sort_order），
// 前端要的是嵌套 children 结构。
func buildOutlineData(nodes []store.TechnicalPlanOutlineNode) *OutlineData {
	// 空目录必须返回 nil：前端用 outlineData 真值判断「已生成」，
	// 空对象会让进度条显示 100% 且 0 章。
	if len(nodes) == 0 {
		return nil
	}

	// 建索引
	byID := make(map[string]*OutlineItem, len(nodes))
	childrenOf := make(map[string][]store.TechnicalPlanOutlineNode)
	var roots []store.TechnicalPlanOutlineNode

	for _, n := range nodes {
		id := n.NodeID.String()

		// content_mode 是 pgtype.Text，要取值并给默认值
		contentMode := n.ContentMode.String
		if contentMode == "" {
			contentMode = "ai-generate"
		}

		item := &OutlineItem{
			ID: id,
			// 标题去掉已有编号（数据库里的标题常带「1.1 xxx」，
			// 前端会再拼一次 number，不清理会显示成「1.1 1.1 xxx」）
			Title:       stripTitleNumber(n.Title),
			Description: n.Description,
			Content:     n.Content,
			ContentMode: contentMode,
			Level:       int(n.Level),
			SortOrder:   n.SortOrder,
		}
		byID[id] = item

		// ParentNodeID 是 *uuid.UUID
		if n.ParentNodeID != nil {
			childrenOf[n.ParentNodeID.String()] = append(childrenOf[n.ParentNodeID.String()], n)
		} else {
			roots = append(roots, n)
		}
	}

	// 按 sort_order 排序
	sortNodes := func(list []store.TechnicalPlanOutlineNode) {
		for i := 1; i < len(list); i++ {
			for j := i; j > 0 && list[j].SortOrder < list[j-1].SortOrder; j-- {
				list[j], list[j-1] = list[j-1], list[j]
			}
		}
	}
	sortNodes(roots)

	// 递归挂 children，同时算 number（按目录顺序）
	var counter int
	var walk func(list []store.TechnicalPlanOutlineNode, level int, prefix string) []OutlineItem
	walk = func(list []store.TechnicalPlanOutlineNode, level int, prefix string) []OutlineItem {
		out := make([]OutlineItem, 0, len(list))
		for i, n := range list {
			id := n.NodeID.String()
			item, ok := byID[id]
			if !ok {
				continue
			}

			// number 按层级编号：一级 1、2；二级 1.1、1.2
			num := fmt.Sprintf("%s%d", prefix, i+1)
			if prefix == "" {
				num = fmt.Sprintf("%d", i+1)
			}
			item.Number = num

			if kids, ok := childrenOf[id]; ok && len(kids) > 0 {
				sortNodes(kids)
				item.Children = walk(kids, level+1, num+".")
			}
			out = append(out, *item)
			counter++
		}
		return out
	}

	tree := walk(roots, 1, "")

	return &OutlineData{Outline: tree}
}

// buildSingularTenderFile 构造前端 TechnicalPlanTenderFile。
//
// Web 导入把 TenderMarkdownPath 写成 ""（资产引用），所以不能像桌面版那样
// 只看 path 是否非空；只要 tenderFiles 有条目，或 markdown chars/hash 已写入，
// 就返回非 null，否则「下一步」会一直 disabled。
func buildSingularTenderFile(meta store.TechnicalPlanMetum, files []TenderFile) *TenderFile {
	if len(files) == 0 && meta.TenderMarkdownChars <= 0 && meta.TenderMarkdownHash == "" {
		return nil
	}

	fileName := firstTenderFileName(meta.TenderFiles)
	parserLabel := meta.TenderParserLabel
	chars := meta.TenderMarkdownChars
	id := ""
	markdownPath := meta.TenderMarkdownPath

	if len(files) > 0 {
		id = files[0].ID
		if fileName == "" {
			fileName = files[0].FileName
		}
		if chars <= 0 {
			chars = files[0].MarkdownChars
		}
		if parserLabel == "" {
			parserLabel = files[0].ParserLabel
		}
		if markdownPath == "" {
			markdownPath = files[0].MarkdownPath
		}
	}
	if fileName == "" {
		fileName = "技术方案招标文件"
	}
	if markdownPath == "" {
		markdownPath = "tender.md"
	}

	importedAt := ""
	if meta.TenderImportedAt.Valid {
		importedAt = meta.TenderImportedAt.Time.UTC().Format(time.RFC3339)
	}

	origPath := meta.TenderOriginalMarkdownPath
	if origPath == "" {
		origPath = markdownPath
	}
	origChars := meta.TenderOriginalMarkdownChars
	if origChars <= 0 {
		origChars = chars
	}
	origHash := meta.TenderOriginalMarkdownHash
	if origHash == "" {
		origHash = meta.TenderMarkdownHash
	}

	return &TenderFile{
		ID:                    id,
		FileName:              fileName,
		MarkdownPath:          markdownPath,
		MarkdownChars:         chars,
		ContentHash:           meta.TenderMarkdownHash,
		OriginalMarkdownPath:  origPath,
		OriginalMarkdownChars: origChars,
		OriginalContentHash:   origHash,
		ParserLabel:           parserLabel,
		ImportedAt:            importedAt,
		SelectedSectionID:     meta.SelectedSectionID,
		SelectedSectionTitle:  meta.SelectedSectionTitle,
		UpdatedAt:             meta.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// buildOriginalPlanFile 构造前端 originalPlanFile。
// 与 tenderFile 同类：Web 导入路径可为空，靠文件名/字数/hash 判断已导入。
func buildOriginalPlanFile(meta store.TechnicalPlanMetum) *TenderFile {
	if meta.OriginalPlanFileName == "" && meta.OriginalPlanMarkdownChars <= 0 && meta.OriginalPlanMarkdownHash == "" {
		return nil
	}

	markdownPath := meta.OriginalPlanMarkdownPath
	if markdownPath == "" {
		markdownPath = "original-plan.md"
	}

	importedAt := ""
	if meta.OriginalPlanImportedAt.Valid {
		importedAt = meta.OriginalPlanImportedAt.Time.UTC().Format(time.RFC3339)
	}

	fileName := meta.OriginalPlanFileName
	if fileName == "" {
		fileName = "原方案"
	}

	return &TenderFile{
		FileName:      fileName,
		MarkdownPath:  markdownPath,
		MarkdownChars: meta.OriginalPlanMarkdownChars,
		ContentHash:   meta.OriginalPlanMarkdownHash,
		ParserLabel:   meta.OriginalPlanParserLabel,
		ImportedAt:    importedAt,
		UpdatedAt:     meta.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// parseTenderFiles 把数据库里的 JSON 转成前端要的数组。
//
// 数据库里 tender_files 存的是 [{fileName, chars, parserLabel}]，
// 前端要的是完整结构（含 id / markdownPath / contentHash / updatedAt）。
func parseTenderFiles(raw []byte, meta store.TechnicalPlanMetum) []TenderFile {
	items := rawJSON(raw)
	arr, ok := items.([]any)
	if !ok || len(arr) == 0 {
		return []TenderFile{}
	}

	out := make([]TenderFile, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		fileName, _ := m["fileName"].(string)
		if fileName == "" {
			continue
		}

		chars := int32(0)
		if v, ok := m["chars"].(float64); ok {
			chars = int32(v)
		}
		parserLabel, _ := m["parserLabel"].(string)

		importedAt := ""
		if meta.TenderImportedAt.Valid {
			importedAt = meta.TenderImportedAt.Time.Format(time.RFC3339)
		}

		out = append(out, TenderFile{
			ID:            fmt.Sprintf("tender-%d-%s", i+1, shortHash(fileName)),
			FileName:      fileName,
			MarkdownPath:  "tender.md",
			MarkdownChars: chars,
			ContentHash:   meta.TenderMarkdownHash,
			ParserLabel:   parserLabel,
			ImportedAt:    importedAt,
			UpdatedAt:     importedAt,
		})
	}

	return out
}

// shortHash 生成 8 位短哈希。
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// parseWordControlSnapshot 把 meta 列 JSONB 还原成前端 outlineWordControlSnapshot。
func parseWordControlSnapshot(raw []byte) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil
	}
	// 统一成 int 友好的数值（JSONB 数字是 float64）
	normalized := map[string]any{}
	for _, key := range []string{"minimumWords", "maximumWords", "sectionWords"} {
		if n, ok := asInt32(out[key]); ok {
			normalized[key] = n
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func firstTenderFileName(raw []byte) string {
	items := rawJSON(raw)
	if arr, ok := items.([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			if v, ok := m["fileName"].(string); ok {
				return v
			}
		}
	}
	return ""
}

// SaveOutline 保存目录树。
//
// 语义与桌面版 saveOutline 的 reason=replace 一致：
// 清空全部旧节点再插入新节点。
//
// 注意：桌面版还有 edit/delete/add-*/sort 等增量语义，
// 用于「只改动一部分」的场景。这里先实现 replace（目录生成后整体覆盖），
// 增量语义等前端需要时再补。
func (s *TechnicalPlanService) SaveOutline(
	ctx context.Context,
	tenantID, projectID string,
	nodes []*outline.Node,
	projectName, projectOverview string,
) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}
	if err := s.ensureProject(ctx, tid, pid); err != nil {
		return err
	}
	if _, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	}); err != nil {
		return err
	}

	// 事务：清空 + 插入必须原子
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)

	if err := q.DeleteOutlineNodesByProjectTx(ctx, store.DeleteOutlineNodesByProjectTxParams{
		ProjectID: pid,
		TenantID:  tid,
	}); err != nil {
		return fmt.Errorf("清空旧目录失败: %w", err)
	}

	// 递归插入，维护父子关系与排序
	var insert func(list []*outline.Node, parentID *uuid.UUID, level int32) error
	insert = func(list []*outline.Node, parentID *uuid.UUID, level int32) error {
		for i, n := range list {
			nodeID, err := uuid.Parse(n.ID)
			if err != nil {
				// Agent 可能给出非 UUID 的 ID，程序重新生成
				nodeID = uuid.New()
			}

			params := store.InsertOutlineNodeParams{
				NodeID:       nodeID,
				ProjectID:    pid,
				TenantID:     tid,
				ParentNodeID: parentID,
				SortOrder:    int32(i),
				Level:        level,
				Title:        n.Title,
				Description:  n.Description,
				// ExternalID 存前端/AI 给的字符串 ID。
				// 保留它才能按 "n1" 这种 ID 定位节点保存正文。
				ExternalID: n.ID,
			}
			if n.ContentMode != "" {
				params.ContentMode = pgtype.Text{String: n.ContentMode, Valid: true}
			}
			if n.RuleBasis != "" {
				params.RuleBasis = pgtype.Text{String: n.RuleBasis, Valid: true}
			}

			if err := q.InsertOutlineNode(ctx, params); err != nil {
				return fmt.Errorf("插入节点 %q 失败: %w", n.Title, err)
			}

			if len(n.Children) > 0 {
				if err := insert(n.Children, &nodeID, level+1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := insert(nodes, nil, 1); err != nil {
		return err
	}

	// 更新项目名与概览
	if projectName != "" || projectOverview != "" {
		if err := q.UpdateOutlineProjectInfo(ctx, store.UpdateOutlineProjectInfoParams{
			ProjectID:              pid,
			TenantID:               tid,
			OutlineProjectName:     projectName,
			OutlineProjectOverview: projectOverview,
		}); err != nil {
			return fmt.Errorf("更新项目信息失败: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}
