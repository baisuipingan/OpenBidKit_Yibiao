// Package rejection 实现废标项检查。
//
// 三类检查（对齐桌面版）：
//
//	rejection  废标项（按招标硬性条款逐条核验）
//	typo       错别字
//	logic      逻辑谬误（数据冲突、前后矛盾）
//
// 与标书查重的区别：
//
//	查重：招标要求有没有被响应 → 算覆盖率 → 可以纯算法
//	废标：特定条款有没有踩雷 → 要理解语义 → 必须用 AI
//
// 为什么废标检查不能用纯算法：条款是自然语言描述
// （"投标人须具备二级及以上资质"），投标文件是另一种表述
// （"我公司持有信息系统集成二级证书，编号 XXX"）——
// 判断是否满足需要语义理解，字符串匹配做不到。
package rejection

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

// 检查类型。
const (
	KindRejection = "rejection"
	KindTypo      = "typo"
	KindLogic     = "logic"
)

// 运行状态。
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusError   = "error"
)

// 输出文件名。
const (
	extractionOutputFile = "invalid-items.md"
	rejectionOutputFile  = "rejection-findings.json"
	typoOutputFile       = "typo-findings.json"
	logicOutputFile      = "logic-findings.json"
)

// CheckInput 是检查输入。
type CheckInput struct {
	InvalidItems     string
	CustomCheckItems string
	BidDocuments     []BidDocument
}

// BidDocument 是一份投标文件。
type BidDocument struct {
	FileName string
	Content  string
}

// Service 编排废标项检查。
type Service struct {
	q        *store.Queries
	agentSvc *agent.Service
	log      *slog.Logger
}

// New 创建服务。
func New(q *store.Queries, agentSvc *agent.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{q: q, agentSvc: agentSvc, log: log}
}

// RunOptions 是检查参数。
type RunOptions struct {
	TenantID  string
	ProjectID string
	OwnerID   string
	Provider  agent.Provider
	Model     agent.ModelConfig

	// EnableRejection 废标项检查
	EnableRejection bool
	// EnableTypo 错别字检查
	EnableTypo bool
	// EnableLogic 逻辑谬误检查
	EnableLogic bool

	// OnProgress 进度回调
	OnProgress func(kind string, progress int, message string)
}

// Finding AI 输出的一条风险。
type Finding struct {
	BidFileName string `json:"bidFileName"`
	Type        string `json:"type"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Requirement string `json:"requirement"`
	BidEvidence string `json:"bidEvidence"`
	RiskReason  string `json:"riskReason"`
	Suggestion  string `json:"suggestion"`

	// 错别字/逻辑检查专用字段
	WrongText       string `json:"wrongText"`
	CorrectText     string `json:"correctText"`
	OriginalExcerpt string `json:"originalExcerpt"`
	Reason          string `json:"reason"`
	LocationHint    string `json:"locationHint"`
}

// Run 执行检查。
func (s *Service) Run(ctx context.Context, checkID uuid.UUID, opts RunOptions, input CheckInput) error {
	kinds := make([]string, 0, 3)
	if opts.EnableRejection {
		kinds = append(kinds, KindRejection)
	}
	if opts.EnableTypo {
		kinds = append(kinds, KindTypo)
	}
	if opts.EnableLogic {
		kinds = append(kinds, KindLogic)
	}

	if len(kinds) == 0 {
		return fmt.Errorf("请至少启用一种检查")
	}

	for _, kind := range kinds {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var err error
		switch kind {
		case KindRejection:
			err = s.runRejection(ctx, checkID, opts, input)
		case KindTypo:
			err = s.runGeneric(ctx, checkID, opts, input, KindTypo, typoOutputFile, buildTypoPrompt)
		case KindLogic:
			err = s.runGeneric(ctx, checkID, opts, input, KindLogic, logicOutputFile, buildLogicPrompt)
		}

		if err != nil {
			s.log.Warn("检查失败",
				slog.String("kind", kind),
				slog.String("error", err.Error()),
			)
		}
	}

	return nil
}

// runRejection 跑废标项检查。
func (s *Service) runRejection(ctx context.Context, checkID uuid.UUID, opts RunOptions, input CheckInput) error {
	if strings.TrimSpace(input.InvalidItems) == "" {
		return s.failRun(ctx, checkID, KindRejection, "请先解析招标文件的无效与废标项")
	}

	prompt := buildCheckPrompt(input)
	findings, err := s.callAI(ctx, opts, prompt, rejectionOutputFile)
	if err != nil {
		return s.failRun(ctx, checkID, KindRejection, err.Error())
	}

	return s.saveFindings(ctx, checkID, KindRejection, findings)
}

// runGeneric 跑通用检查（错别字 / 逻辑）。
func (s *Service) runGeneric(
	ctx context.Context,
	checkID uuid.UUID,
	opts RunOptions,
	input CheckInput,
	kind, outputFile string,
	buildPrompt func(CheckInput) string,
) error {
	prompt := buildPrompt(input)
	findings, err := s.callAI(ctx, opts, prompt, outputFile)
	if err != nil {
		return s.failRun(ctx, checkID, kind, err.Error())
	}

	return s.saveFindings(ctx, checkID, kind, findings)
}

// callAI 调用模型并解析结果。
func (s *Service) callAI(ctx context.Context, opts RunOptions, prompt, outputFile string) ([]Finding, error) {
	type outcome struct {
		findings []Finding
		err      error
	}
	done := make(chan outcome, 1)

	// 独立 context：AI 检查要几分钟，不能绑在 HTTP 请求上
	taskCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, err := s.agentSvc.StartTask(taskCtx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  "rejection-check",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: "把检查结果写入 " + outputFile + "，格式为 JSON 数组。",
		Stages: []agent.Stage{
			{
				ID:     "check",
				Prompt: prompt,
				Tools:  []string{"write"},
				// sidecar 会校验这个文件存在，不存在判任务失败
				OutputFile: outputFile,
				MaxRetries: 1,
			},
		},
		Timeout: 10 * time.Minute,
		OnComplete: func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
			if !succeeded {
				done <- outcome{err: fmt.Errorf("检查任务未成功完成")}
				return nil
			}

			raw, readErr := os.ReadFile(filepath.Join(workspaceDir, outputFile))
			if readErr != nil {
				done <- outcome{err: fmt.Errorf("读取结果文件失败: %w", readErr)}
				return nil
			}

			items, parseErr := parseFindings(string(raw))
			done <- outcome{findings: items, err: parseErr}
			return nil
		},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("启动检查任务失败: %w", err)
	}

	select {
	case out := <-done:
		return out.findings, out.err
	case <-taskCtx.Done():
		return nil, fmt.Errorf("检查超时（10 分钟）")
	}
}

// parseFindings 解析 AI 输出的 JSON。
func parseFindings(text string) ([]Finding, error) {
	cleaned := text

	// 去掉 markdown 代码块
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

	start := strings.Index(cleaned, "[")
	end := strings.LastIndex(cleaned, "]")
	if start < 0 || end <= start {
		// 没有数组 → 空结果（AI 可能判定"没问题"）
		return []Finding{}, nil
	}

	var findings []Finding
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &findings); err != nil {
		return nil, fmt.Errorf("解析结果失败: %w", err)
	}

	// 过滤掉没有实质内容的条目
	valid := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if strings.TrimSpace(f.Title) == "" && strings.TrimSpace(f.WrongText) == "" {
			continue
		}
		// 归一化枚举值
		if f.Severity != "high" && f.Severity != "medium" && f.Severity != "low" {
			f.Severity = "medium"
		}
		if f.Type != "invalidBid" {
			f.Type = "rejectionItem"
		}
		valid = append(valid, f)
	}

	return valid, nil
}

// saveFindings 保存检查结果。
func (s *Service) saveFindings(ctx context.Context, checkID uuid.UUID, kind string, findings []Finding) error {
	// 清旧结果
	_ = s.q.DeleteFindingsByKind(ctx, store.DeleteFindingsByKindParams{
		CheckID: checkID, Kind: kind,
	})

	for i, f := range findings {
		title := f.Title
		if title == "" {
			// 错别字/逻辑检查没有 title，用 wrongText 生成
			title = fmt.Sprintf("%s → %s", f.WrongText, f.CorrectText)
		}

		evidence := f.BidEvidence
		if evidence == "" {
			evidence = f.OriginalExcerpt
		}

		reason := f.RiskReason
		if reason == "" {
			reason = f.Reason
		}

		summary := f.Summary
		if summary == "" && f.LocationHint != "" {
			summary = f.LocationHint
		}

		_, err := s.q.InsertRejectionFinding(ctx, store.InsertRejectionFindingParams{
			CheckID:     checkID,
			Kind:        kind,
			BidFileName: f.BidFileName,
			FindingType: f.Type,
			Severity:    f.Severity,
			Title:       title,
			Summary:     summary,
			Requirement: f.Requirement,
			BidEvidence: evidence,
			RiskReason:  reason,
			Suggestion:  f.Suggestion,
			SortOrder:   int32(i),
		})
		if err != nil {
			s.log.Warn("保存发现失败", slog.String("error", err.Error()))
		}
	}

	msg := fmt.Sprintf("检查完成，发现 %d 项风险", len(findings))
	if len(findings) == 0 {
		msg = "检查完成，未发现风险"
	}

	_, _ = s.q.UpsertRejectionRun(ctx, store.UpsertRejectionRunParams{
		CheckID:      checkID,
		Kind:         kind,
		Status:       StatusSuccess,
		Progress:     100,
		Message:      msg,
		Error:        pgtype.Text{},
		FindingCount: int32(len(findings)),
		StartedAt:    nowTs(),
		FinishedAt:   nowTs(),
	})

	s.log.Info("检查完成",
		slog.String("kind", kind),
		slog.Int("findings", len(findings)),
	)

	return nil
}

// failRun 标记检查失败。
func (s *Service) failRun(ctx context.Context, checkID uuid.UUID, kind, errMsg string) error {
	_, _ = s.q.UpsertRejectionRun(ctx, store.UpsertRejectionRunParams{
		CheckID:      checkID,
		Kind:         kind,
		Status:       StatusError,
		Progress:     100,
		Message:      "检查失败",
		Error:        pgtype.Text{String: errMsg, Valid: true},
		FindingCount: 0,
		StartedAt:    nowTs(),
		FinishedAt:   nowTs(),
	})
	return fmt.Errorf("%s", errMsg)
}

// ExtractOptions 是抽取参数。
type ExtractOptions struct {
	TenantID  string
	ProjectID string
	OwnerID   string
	Provider  agent.Provider
	Model     agent.ModelConfig
	// TenderContent 招标文件正文
	TenderContent string
}

// ExtractItems 用 AI 从招标文件抽取「无效投标与废标项」条款。
//
// 为什么单独一个方法：抽取是废标检查的前置步骤，
// 用户可能只想先看看有哪些废标条款，不马上检查投标文件。
func (s *Service) ExtractItems(ctx context.Context, opts ExtractOptions) (string, error) {
	prompt := buildExtractionPrompt(opts.TenderContent)

	type outcome struct {
		content string
		err     error
	}
	done := make(chan outcome, 1)

	taskCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_, err := s.agentSvc.StartTask(taskCtx, agent.TaskSpec{
		TenantID:              opts.TenantID,
		ProjectID:             opts.ProjectID,
		OwnerID:               opts.OwnerID,
		Type:                  "rejection-extract",
		Provider:              opts.Provider,
		Model:                 opts.Model,
		WorkspaceInstructions: "把抽取结果写入 " + extractionOutputFile + "。",
		Stages: []agent.Stage{
			{
				ID:         "extract",
				Prompt:     prompt,
				Tools:      []string{"write"},
				OutputFile: extractionOutputFile,
				MaxRetries: 1,
			},
		},
		Timeout: 10 * time.Minute,
		OnComplete: func(cctx context.Context, workspaceDir, taskID string, succeeded bool) error {
			if !succeeded {
				done <- outcome{err: fmt.Errorf("抽取任务未成功完成")}
				return nil
			}
			raw, readErr := os.ReadFile(filepath.Join(workspaceDir, extractionOutputFile))
			if readErr != nil {
				done <- outcome{err: fmt.Errorf("读取抽取结果失败: %w", readErr)}
				return nil
			}
			done <- outcome{content: string(raw)}
			return nil
		},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("启动抽取任务失败: %w", err)
	}

	select {
	case out := <-done:
		return out.content, out.err
	case <-taskCtx.Done():
		return "", fmt.Errorf("抽取超时（10 分钟）")
	}
}

// SetExtractionState 更新抽取状态。
func (s *Service) SetExtractionState(ctx context.Context, tenantID, projectID uuid.UUID, status string, progress int, message, content, errMsg string) error {
	return s.q.UpdateExtractionState(ctx, store.UpdateExtractionStateParams{
		TenantID:           tenantID,
		ProjectID:          projectID,
		InvalidItems:       content,
		ExtractionStatus:   status,
		ExtractionProgress: int32(progress),
		ExtractionMessage:  message,
		ExtractionError:    pgtype.Text{String: errMsg, Valid: errMsg != ""},
	})
}

// SetExtractionSignature 记录抽取结果对应的招标文件签名。
func (s *Service) SetExtractionSignature(ctx context.Context, tenantID, projectID uuid.UUID, signature string) error {
	return s.q.SetExtractionSignature(ctx, store.SetExtractionSignatureParams{
		TenantID:            tenantID,
		ProjectID:           projectID,
		ExtractionSignature: signature,
	})
}

func nowTs() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now(), Valid: true}
}

// BuildInput 从数据库组装检查输入。
func (s *Service) BuildInput(check store.RejectionCheck, bidContents map[string]string) CheckInput {
	docs := make([]BidDocument, 0, len(check.BidFileNames))
	for _, name := range check.BidFileNames {
		content, ok := bidContents[name]
		if !ok {
			continue
		}
		docs = append(docs, BidDocument{FileName: name, Content: content})
	}

	return CheckInput{
		InvalidItems:     check.InvalidItems,
		CustomCheckItems: check.CustomCheckItems,
		BidDocuments:     docs,
	}
}
