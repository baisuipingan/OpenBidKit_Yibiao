package knowledge

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

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/store"
)

// 提炼参数。
// ExtractOutputFile 提炼结果的输出文件名。
//
// sidecar 会校验这个文件是否存在，Agent 必须写它。
const ExtractOutputFile = "knowledge-items.json"

const (
	// maxBlocksPerBatch 单批处理的块数上限。
	//
	// 为什么分批：一份 200 页文档可能有 300+ 块，
	// 一次全塞进 Prompt 会超上下文，而且效果会下降
	// （模型注意力被稀释，提炼质量变差）。
	maxBlocksPerBatch = 18

	// maxCharsPerBatch 单批字数上限。
	maxCharsPerBatch = 12000
)

// Extractor 用 AI 从内容块提炼知识条目。
//
// 为什么需要提炼而不是直接索引原文：
//
//	原文段落是为阅读写的（有过渡、有重复、有铺垫），
//	检索需要的是「一个可独立理解的知识点」。
//	提炼后每条 100-300 字，标题清晰，检索精度和引用价值都高得多。
type Extractor struct {
	q        *store.Queries
	agentSvc *agent.Service
	log      *slog.Logger
}

// NewExtractor 创建提炼器。
func NewExtractor(q *store.Queries, agentSvc *agent.Service, log *slog.Logger) *Extractor {
	if log == nil {
		log = slog.Default()
	}
	return &Extractor{q: q, agentSvc: agentSvc, log: log}
}

// AnalyzeOptions 是提炼参数。
type AnalyzeOptions struct {
	TenantID   string
	ProjectID  string
	OwnerID    string
	DocumentID string
	Provider   agent.Provider
	Model      agent.ModelConfig
	OnEvent    func(agent.TaskEvent)
}

// AnalyzeResult 是提炼结果。
type AnalyzeResult struct {
	ItemCount  int `json:"itemCount"`
	BatchCount int `json:"batchCount"`
	// Errors 每批的错误（部分失败不影响其他批次）
	Errors []string `json:"errors,omitempty"`
}

// Analyze 提炼知识条目。
//
// 流程：
//  1. 取该文档所有未过滤的块
//  2. 按 maxBlocksPerBatch / maxCharsPerBatch 分批
//  3. 每批调一次模型，产出若干知识条目
//  4. 落库（review_status = candidate，等用户确认）
//
// 为什么标 candidate 而不是直接 approved：
//
//	AI 提炼会出错（编造、合并错内容）。让用户过一眼，
//	确认后才能被正文生成引用 —— 这比事后发现错误标书里有假信息要好得多。
func (x *Extractor) Analyze(ctx context.Context, opts AnalyzeOptions) (*AnalyzeResult, error) {
	tenantID, err := uuid.Parse(opts.TenantID)
	if err != nil {
		return nil, fmt.Errorf("租户标识非法")
	}
	docID, err := uuid.Parse(opts.DocumentID)
	if err != nil {
		return nil, fmt.Errorf("文档标识非法")
	}

	// 1. 取块
	rows, err := x.q.ListBlocksByDocument(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("读取内容块失败: %w", err)
	}

	usable := make([]Block, 0, len(rows))
	for _, r := range rows {
		if r.IsFiltered {
			continue
		}
		var path []string
		if len(r.HeadingPath) > 0 {
			_ = json.Unmarshal(r.HeadingPath, &path)
		}
		usable = append(usable, Block{
			RawBlock: RawBlock{
				ID:          r.BlockID,
				Type:        BlockType(r.Type),
				HeadingPath: path,
				Content:     r.Content,
			},
		})
	}

	if len(usable) == 0 {
		return nil, fmt.Errorf("没有可提炼的内容块")
	}

	// 2. 分批
	batches := splitBatches(usable)

	x.log.Info("开始提炼知识条目",
		slog.String("document_id", opts.DocumentID),
		slog.Int("blocks", len(usable)),
		slog.Int("batches", len(batches)),
	)

	// 3. 逐批提炼
	result := &AnalyzeResult{BatchCount: len(batches)}

	// 先清掉旧条目（重新提炼场景）
	_ = x.q.DeleteItemsByDocument(ctx, docID)

	itemSeq := 0
	for i, batch := range batches {
		items, err := x.extractBatch(ctx, opts, batch)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("第 %d 批: %v", i+1, err))
			x.log.Warn("批次提炼失败",
				slog.Int("batch", i+1),
				slog.String("error", err.Error()),
			)
			continue
		}

		// 4. 落库
		for _, item := range items {
			itemSeq++
			headingJSON, _ := json.Marshal(item.SourceBlockIDs)
			_, err := x.q.InsertKnowledgeItem(ctx, store.InsertKnowledgeItemParams{
				TenantID:       tenantID,
				DocumentID:     docID,
				ItemID:         item.ID,
				Title:          item.Title,
				Resume:         item.Resume,
				Content:        item.Content,
				SourceBlockIds: headingJSON,
				ReviewStatus:   "candidate",
				SortOrder:      int32(itemSeq),
			})
			if err != nil {
				x.log.Warn("保存知识条目失败", slog.String("error", err.Error()))
				continue
			}
			result.ItemCount++
		}

		// 更新进度
		progress := int32(40 + (60 * (i + 1) / len(batches)))
		_ = x.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
			DocumentID: docID,
			TenantID:   tenantID,
			Status:     StatusAnalyzing,
			Progress:   progress,
			Message:    fmt.Sprintf("已提炼 %d/%d 批", i+1, len(batches)),
			Error:      pgtype.Text{},
		})
	}

	// 5. 收尾
	finalStatus := StatusReady
	if result.ItemCount == 0 && len(result.Errors) > 0 {
		finalStatus = StatusError
	}

	errMsg := ""
	if len(result.Errors) > 0 {
		errMsg = strings.Join(result.Errors, "; ")
	}

	_ = x.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
		DocumentID: docID,
		TenantID:   tenantID,
		Status:     finalStatus,
		Progress:   100,
		Message:    fmt.Sprintf("已提炼 %d 条知识", result.ItemCount),
		Error:      pgtype.Text{String: errMsg, Valid: errMsg != ""},
	})

	_ = x.q.UpdateDocumentStats(ctx, store.UpdateDocumentStatsParams{
		DocumentID:          docID,
		TenantID:            tenantID,
		ItemCount:           int32(result.ItemCount),
		CandidateItemCount:  int32(result.ItemCount),
		BlockCount:          int32(len(rows)),
		FilteredBlockCount:  countFiltered(rows),
		DiscardedBlockCount: countFiltered(rows),
	})

	return result, nil
}

// ExtractedItem 是提炼出的一条知识。
type ExtractedItem struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Resume         string   `json:"resume"`
	Content        string   `json:"content"`
	SourceBlockIDs []string `json:"sourceBlockIds"`
}

// extractBatch 提炼一批块。
//
// 为什么用 OnComplete + channel 而不是轮询：
//
//	OnComplete 在任务结束时触发，直接拿到 workspaceDir。
//	我们让 Agent 把结果写到文件，回调里读出来即可 ——
//	比解析事件流可靠（事件流可能被截断，文件是完整的）。
func (x *Extractor) extractBatch(ctx context.Context, opts AnalyzeOptions, batch []Block) ([]ExtractedItem, error) {
	prompt := buildExtractPrompt(batch)

	type outcome struct {
		items []ExtractedItem
		err   error
	}
	done := make(chan outcome, 1)

	outputFile := ExtractOutputFile

	// 用独立 context 启动任务：
	// 提炼要几分钟，不能绑在 HTTP 请求的生命周期上
	taskCtx, taskCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer taskCancel()

	handle, err := x.agentSvc.StartTask(taskCtx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  "knowledge-extract",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: "把提炼结果写入 " + outputFile + "，格式为 JSON 数组。",
		Stages: []agent.Stage{
			{
				ID:     "extract",
				Prompt: prompt,
				// 只给写文件能力：提炼是纯文本任务
				Tools: []string{"write"},
				// 强制产出文件（sidecar 会校验）
				OutputFile: outputFile,
				MaxRetries: 1,
			},
		},
		Timeout: 10 * time.Minute,
		OnComplete: func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
			if !succeeded {
				done <- outcome{err: fmt.Errorf("提炼任务未成功")}
				return nil
			}

			raw, readErr := os.ReadFile(filepath.Join(workspaceDir, outputFile))
			if readErr != nil {
				done <- outcome{err: fmt.Errorf("读取结果文件失败: %w", readErr)}
				return nil
			}

			items, parseErr := parseItemsJSON(string(raw))
			done <- outcome{items: items, err: parseErr}
			return nil
		},
	}, opts.OnEvent)
	if err != nil {
		return nil, fmt.Errorf("启动提炼任务失败: %w", err)
	}

	// 等回调。
	//
	// **重要**：等 done channel（OnComplete 触发），不等 taskCtx.Done()。
	// taskCtx 是我们的超时控制，10 分钟足够；
	// 调用方的 ctx 已经不被使用（StartTask 传的是 taskCtx）。
	//
	// OnComplete 回调里读了结果文件，所以只需等它。
	select {
	case out := <-done:
		if out.err != nil {
			return nil, out.err
		}
		return out.items, nil
	case <-taskCtx.Done():
		x.agentSvc.CancelTask(handle.TaskID)
		// 超时了也尝试读一下文件 —— Agent 可能已经写完了
		if raw, readErr := os.ReadFile(filepath.Join(handle.WorkspaceDir, outputFile)); readErr == nil {
			if items, parseErr := parseItemsJSON(string(raw)); parseErr == nil && len(items) > 0 {
				x.log.Warn("提炼超时但产物已生成，使用已有结果", slog.Int("items", len(items)))
				return items, nil
			}
		}
		return nil, fmt.Errorf("提炼超时（10 分钟）")
	}
}

// buildExtractPrompt 构造提炼 Prompt。
func buildExtractPrompt(batch []Block) string {
	var sb strings.Builder

	sb.WriteString("你是知识库整理助手。请从下面的文档片段中提炼出结构化的知识点。\n\n")

	// **必须显式要求写文件** ——
	// sidecar 会校验 outputFile 是否存在，没写文件任务直接判失败。
	sb.WriteString("## ⚠️ 关键要求\n\n")
	sb.WriteString("**你必须使用 write 工具把结果写入 `" + ExtractOutputFile + "` 文件。**\n")
	sb.WriteString("只在回复里输出 JSON 不算完成 —— 系统会检查文件是否存在，")
	sb.WriteString("文件不存在任务直接失败。\n\n")

	sb.WriteString("## 要求\n\n")
	sb.WriteString("1. 每个知识点必须能独立理解（不依赖上下文就能看懂）\n")
	sb.WriteString("2. 标题要具体（例如「如何做X」而不是「方法」），不超过 30 字\n")
	sb.WriteString("3. 摘要一句话概括，不超过 60 字\n")
	sb.WriteString("4. 正文保留原文的具体信息：数字、标准号、型号、参数、流程步骤\n")
	sb.WriteString("5. **不要编造**原文没有的内容\n")
	sb.WriteString("6. 一条知识点聚焦一个主题，不要把多个主题混在一起\n")
	sb.WriteString("7. 记录来源块 ID（在每条内容前的 [R000001] 标记）\n\n")

	sb.WriteString("## 输出格式\n\n")
	sb.WriteString("调用 write 工具，文件路径填 `" + ExtractOutputFile + "`，")
	sb.WriteString("内容为严格的 JSON 数组（不要在文件里加 markdown 代码块标记）：\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`[
  {
    "id": "K0001",
    "title": "知识点标题",
    "resume": "一句话摘要",
    "content": "完整的知识点内容，可以引用进标书",
    "sourceBlockIds": ["R000001", "R000002"]
  }
]`)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## 文档片段\n\n")
	sb.WriteString(RenderForPrompt(batch))

	return sb.String()
}

// parseItemsJSON 解析 Agent 写出的 JSON 文件。
func parseItemsJSON(text string) ([]ExtractedItem, error) {
	return extractJSONArray(text)
}

// extractJSONArray 从文本里提取 JSON 数组（容忍 markdown 代码块包裹）。
func extractJSONArray(text string) ([]ExtractedItem, error) {
	// 去掉 markdown 代码块
	cleaned := text
	if idx := strings.Index(cleaned, "```json"); idx >= 0 {
		cleaned = cleaned[idx+7:]
		if end := strings.Index(cleaned, "```"); end >= 0 {
			cleaned = cleaned[:end]
		}
	} else if idx := strings.Index(cleaned, "```"); idx >= 0 {
		cleaned = cleaned[idx+3:]
		if end := strings.Index(cleaned, "```"); end >= 0 {
			cleaned = cleaned[:end]
		}
	}

	// 找 JSON 数组
	start := strings.Index(cleaned, "[")
	end := strings.LastIndex(cleaned, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("输出里没有 JSON 数组")
	}

	var items []ExtractedItem
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &items); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	// 校验必填字段
	valid := make([]ExtractedItem, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Content) == "" {
			continue
		}
		valid = append(valid, item)
	}

	if len(valid) == 0 {
		return nil, fmt.Errorf("没有有效的知识点")
	}

	return valid, nil
}

// splitBatches 按块数和字数分批。
func splitBatches(blocks []Block) [][]Block {
	var batches [][]Block
	var current []Block
	chars := 0

	for _, b := range blocks {
		bChars := contentChars(b.Content)

		// 超限就切批
		if len(current) >= maxBlocksPerBatch || (chars+bChars > maxCharsPerBatch && len(current) > 0) {
			batches = append(batches, current)
			current = nil
			chars = 0
		}

		current = append(current, b)
		chars += bChars
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches
}

// countFiltered 统计被过滤的块数。
func countFiltered(rows []store.KnowledgeBlock) int32 {
	var n int32
	for _, r := range rows {
		if r.IsFiltered {
			n++
		}
	}
	return n
}
