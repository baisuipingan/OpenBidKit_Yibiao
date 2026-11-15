package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// 文档状态。
const (
	StatusPending   = "pending"   // 刚上传
	StatusParsing   = "parsing"   // 解析中
	StatusAnalyzing = "analyzing" // AI 提炼中
	// StatusReady 处理完成。
	//
	// **命名约定**：前端和桌面版都用 "success"。
	// 用 "ready" 会让设置页的知识库选择器过滤不到文档
	// （它只认 status === 'success'）。
	StatusReady = "success"
	StatusError = "error" // 失败
)

// Service 编排知识库的文档处理流程。
//
// 流程：
//
//	上传 → 存对象存储 → 解析 markdown → 存 markdown → 切块 → 存块
//	     → （AI 提炼，异步）→ 存知识条目 → ready
//
// 为什么要分两段（解析 / 提炼）：
//
//	解析是纯本地计算，秒级；提炼要调模型，分钟级。
//	分开后解析完就能看到块和过滤结果，用户不用干等。
type Service struct {
	q         *store.Queries
	artifacts *artifact.Service
	log       *slog.Logger

	// workspaceDir 临时文件目录（上传的原始文件先落这里再解析）
	workspaceDir string
}

// New 创建服务。
func New(q *store.Queries, artifacts *artifact.Service, workspaceDir string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		q:            q,
		artifacts:    artifacts,
		workspaceDir: workspaceDir,
		log:          log,
	}
}

// UploadRequest 是上传请求。
type UploadRequest struct {
	TenantID  string
	ProjectID string // 可空：用户级知识库
	FolderID  string
	OwnerID   string
	// Files 本地文件路径（由 HTTP 层从上传解析得到）
	Files []string
}

// UploadResult 是上传结果。
type UploadResult struct {
	Documents []*DocumentView `json:"documents"`
	Errors    []string        `json:"errors,omitempty"`
}

// Upload 上传并解析文档。
//
// 每个文件独立处理：一个失败不影响其他。
// 返回的文档处于 parsing 状态，前端订阅事件看进度。
func (s *Service) Upload(ctx context.Context, req UploadRequest) (*UploadResult, error) {
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		return nil, fmt.Errorf("租户标识非法")
	}

	folderID, err := uuid.Parse(req.FolderID)
	if err != nil {
		return nil, fmt.Errorf("文件夹标识非法")
	}

	var projectID *uuid.UUID
	if req.ProjectID != "" {
		pid, err := uuid.Parse(req.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("项目标识非法")
		}
		projectID = &pid
	}

	// 校验文件夹归属
	if _, err := s.q.GetKnowledgeFolder(ctx, store.GetKnowledgeFolderParams{
		FolderID: folderID, TenantID: tenantID,
	}); err != nil {
		return nil, fmt.Errorf("文件夹不存在")
	}

	out := &UploadResult{Documents: make([]*DocumentView, 0, len(req.Files))}

	for _, path := range req.Files {
		doc, err := s.uploadOne(ctx, tenantID, projectID, folderID, req.OwnerID, path)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		out.Documents = append(out.Documents, doc)
	}

	return out, nil
}

// uploadOne 处理单个文件。
func (s *Service) uploadOne(
	ctx context.Context,
	tenantID uuid.UUID,
	projectID *uuid.UUID,
	folderID uuid.UUID,
	ownerID string,
	path string,
) (*DocumentView, error) {
	fileName := filepath.Base(path)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))

	// 1. 原始文件入库
	sourceAsset, err := s.putFile(ctx, tenantID, projectID, ownerID, path, fileName, "knowledge-source")
	if err != nil {
		return nil, fmt.Errorf("保存原始文件失败: %w", err)
	}

	// 2. 建文档记录（parsing 状态）
	sourceAssetUUID, uuidErr := uuid.Parse(sourceAsset.ID)
	var sourceAssetPtr *uuid.UUID
	if uuidErr == nil {
		sourceAssetPtr = &sourceAssetUUID
	}

	doc, err := s.q.CreateKnowledgeDocument(ctx, store.CreateKnowledgeDocumentParams{
		TenantID:        tenantID,
		ProjectID:       projectID,
		FolderID:        folderID,
		FileName:        fileName,
		SourceAssetID:   sourceAssetPtr,
		MarkdownAssetID: nil,
		MarkdownChars:   0,
		SourceExtension: pgtype.Text{String: ext, Valid: true},
		Status:          StatusParsing,
		ParserLabel:     pgtype.Text{},
		SortOrder:       0,
	})
	if err != nil {
		return nil, fmt.Errorf("创建文档记录失败: %w", err)
	}

	// 3. 解析 markdown
	result, parseErr := docparse.ToMarkdown(ctx, path)
	if parseErr != nil {
		errMsg := parseErr.Error()
		_ = s.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
			DocumentID: doc.DocumentID, TenantID: tenantID,
			Status: StatusError, Progress: 0, Message: "解析失败",
			Error: pgtype.Text{String: errMsg, Valid: true},
		})
		return nil, fmt.Errorf("解析失败: %w", parseErr)
	}

	// 4. markdown 入库
	mdPath := filepath.Join(s.workspaceDir, "kb-"+doc.DocumentID.String()+".md")
	if err := os.MkdirAll(s.workspaceDir, 0o755); err == nil {
		if writeErr := os.WriteFile(mdPath, []byte(result.Markdown), 0o644); writeErr == nil {
			defer os.Remove(mdPath)

			if mdAsset, putErr := s.putFile(ctx, tenantID, projectID, ownerID, mdPath, fileName+".md", "knowledge-markdown"); putErr == nil {
				var mdAssetPtr *uuid.UUID
				if id, err := uuid.Parse(mdAsset.ID); err == nil {
					mdAssetPtr = &id
				}
				_ = s.q.UpdateDocumentMarkdown(ctx, store.UpdateDocumentMarkdownParams{
					DocumentID:      doc.DocumentID,
					TenantID:        tenantID,
					MarkdownAssetID: mdAssetPtr,
					MarkdownChars:   int32(len([]rune(result.Markdown))),
					Status:          StatusAnalyzing,
					Progress:        30,
					Message:         "已解析，等待提炼",
				})
			}
		}
	}

	// 5. 切块 + 过滤 + 合并
	raw := SplitBlocks(result.Markdown)
	merged := MergeSemantic(raw)
	blocks := Filter(merged)

	// 6. 存块
	_ = s.q.DeleteBlocksByDocument(ctx, doc.DocumentID)

	filteredCount := 0
	for i, b := range blocks {
		reason := pgtype.Text{}
		if b.Filtered {
			filteredCount++
			reason = pgtype.Text{String: b.FilterReason, Valid: true}
		}

		headingJSON, _ := jsonMarshal(b.HeadingPath)

		_ = s.q.InsertKnowledgeBlock(ctx, store.InsertKnowledgeBlockParams{
			TenantID:     tenantID,
			DocumentID:   doc.DocumentID,
			BlockID:      b.ID,
			Type:         string(b.Type),
			HeadingPath:  headingJSON,
			Content:      b.Content,
			ContentChars: int32(contentChars(b.Content)),
			IsFiltered:   b.Filtered,
			FilterReason: reason,
			SortOrder:    int32(i),
		})
	}

	total := int32(len(blocks))
	_ = s.q.UpdateDocumentStats(ctx, store.UpdateDocumentStatsParams{
		DocumentID:          doc.DocumentID,
		TenantID:            tenantID,
		BlockCount:          total,
		FilteredBlockCount:  int32(filteredCount),
		ItemCount:           0,
		CandidateItemCount:  0,
		DiscardedBlockCount: int32(filteredCount),
	})

	_ = s.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
		DocumentID: doc.DocumentID,
		TenantID:   tenantID,
		Status:     StatusAnalyzing,
		Progress:   40,
		Message:    fmt.Sprintf("已切分 %d 块（过滤 %d 块），等待 AI 提炼", total, filteredCount),
		Error:      pgtype.Text{},
	})

	s.log.Info("知识库文档解析完成",
		slog.String("document_id", doc.DocumentID.String()),
		slog.String("file", fileName),
		slog.Int("blocks", len(blocks)),
		slog.Int("filtered", filteredCount),
	)

	// 重新读取最新状态返回
	latest, err := s.q.GetKnowledgeDocument(ctx, store.GetKnowledgeDocumentParams{
		DocumentID: doc.DocumentID, TenantID: tenantID,
	})
	if err != nil {
		latest = doc
	}

	return toDocumentView(latest), nil
}

// putFile 把本地文件存进对象存储。
func (s *Service) putFile(
	ctx context.Context,
	tenantID uuid.UUID,
	projectID *uuid.UUID,
	ownerID, path, name, kind string,
) (*artifact.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	scope := storage.Scope{TenantID: tenantID.String()}
	if projectID != nil {
		scope.ProjectID = projectID.String()
	}

	return s.artifacts.Put(ctx, artifact.PutRequest{
		Scope:   scope,
		Name:    name,
		Kind:    kind,
		Content: f,
		Size:    info.Size(),
		Source:  "user",
		OwnerID: ownerID,
	})
}

// DocumentView 是文档视图。
type DocumentView struct {
	DocumentID         string `json:"documentId"`
	FolderID           string `json:"folderId"`
	FileName           string `json:"fileName"`
	Status             string `json:"status"`
	Progress           int    `json:"progress"`
	Message            string `json:"message"`
	Error              string `json:"error,omitempty"`
	MarkdownChars      int    `json:"markdownChars"`
	ItemCount          int    `json:"itemCount"`
	BlockCount         int    `json:"blockCount"`
	FilteredBlockCount int    `json:"filteredBlockCount"`
	CandidateItemCount int    `json:"candidateItemCount"`
	ParserLabel        string `json:"parserLabel,omitempty"`
	SortOrder          int    `json:"sortOrder"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

// toDocumentView 转视图。
func toDocumentView(d store.KnowledgeDocument) *DocumentView {
	v := &DocumentView{
		DocumentID:         d.DocumentID.String(),
		FolderID:           d.FolderID.String(),
		FileName:           d.FileName,
		Status:             d.Status,
		Progress:           int(d.Progress),
		Message:            d.Message,
		MarkdownChars:      int(d.MarkdownChars),
		ItemCount:          int(d.ItemCount),
		BlockCount:         int(d.BlockCount),
		FilteredBlockCount: int(d.FilteredBlockCount),
		CandidateItemCount: int(d.CandidateItemCount),
		SortOrder:          int(d.SortOrder),
		CreatedAt:          d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          d.UpdatedAt.Format(time.RFC3339),
	}
	if d.Error.Valid {
		v.Error = d.Error.String
	}
	if d.ParserLabel.Valid {
		v.ParserLabel = d.ParserLabel.String
	}
	return v
}

// jsonMarshal 序列化，失败时返回空数组（块没有标题路径是合法的）。
func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]"), err
	}
	return b, nil
}

// GetAsset 读取资产内容（用于读 markdown）。
func (s *Service) GetAsset(ctx context.Context, tenantID, assetID string) (io.ReadCloser, *artifact.Asset, error) {
	return s.artifacts.GetByID(ctx, tenantID, assetID)
}
