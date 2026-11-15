package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/knowledge"
	"github.com/patrick/yibiao-web/internal/service/settings"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/store"
)

// Knowledge 处理知识库接口。
//
// **字段命名注意**：前端类型是 snake_case（沿用桌面版 SQLite 命名），
// 而 Go 结构体习惯 camelCase。这里统一在前端视图结构上打 snake_case 标签，
// 避免前端改代码。
type Knowledge struct {
	q        *store.Queries
	svc      *knowledge.Service
	extract  *knowledge.Extractor
	planSvc  *service.TechnicalPlanService
	settings *settings.Service
	uploads  *upload.Service
}

// NewKnowledge 创建 handler。
func NewKnowledge(
	q *store.Queries,
	svc *knowledge.Service,
	extract *knowledge.Extractor,
	settingsSvc *settings.Service,
	uploads *upload.Service,
) *Knowledge {
	return &Knowledge{
		q:        q,
		svc:      svc,
		extract:  extract,
		settings: settingsSvc,
		uploads:  uploads,
	}
}

// Register 注册路由。
func (h *Knowledge) Register(g *echo.Group) {
	g.POST("/knowledge-base/list", h.List)
	g.POST("/knowledge-base/search", h.Search)
	g.POST("/knowledge-base/create-folder", h.CreateFolder)
	g.POST("/knowledge-base/rename-folder", h.RenameFolder)
	g.POST("/knowledge-base/reorder-folder", h.ReorderFolder)
	g.POST("/knowledge-base/delete-folder", h.DeleteFolder)
	g.POST("/knowledge-base/delete-document", h.DeleteDocument)
	g.POST("/knowledge-base/move-document", h.MoveDocument)
	g.POST("/knowledge-base/upload-documents", h.UploadDocuments)
	g.POST("/knowledge-base/retry-document", h.RetryDocument)
	g.POST("/knowledge-base/start-matching", h.StartMatching)
	g.POST("/knowledge-base/read-markdown", h.ReadMarkdown)
	g.POST("/knowledge-base/read-items", h.ReadItems)
	g.POST("/knowledge-base/read-analysis", h.ReadAnalysis)
	// 同步提炼（调试用：直接返回结果，不等后台）
	g.POST("/knowledge-base/analyze-sync", h.AnalyzeSync)
}

// folderView 文件夹视图（snake_case 对齐前端）。
type folderView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// documentView 文档视图（snake_case 对齐前端）。
type documentView struct {
	ID                  string `json:"id"`
	FolderID            string `json:"folder_id"`
	FileName            string `json:"file_name"`
	Status              string `json:"status"`
	Progress            int    `json:"progress"`
	Message             string `json:"message"`
	ItemCount           int    `json:"item_count"`
	BlockCount          int    `json:"block_count"`
	FilteredBlockCount  int    `json:"filtered_block_count"`
	CandidateItemCount  int    `json:"candidate_item_count"`
	DiscardedBlockCount int    `json:"discarded_block_count"`
	SortOrder           int    `json:"sort_order"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
	Error               string `json:"error,omitempty"`
	MarkdownChars       int    `json:"markdown_chars"`
	ParserLabel         string `json:"parser_label,omitempty"`
}

func toFolderView(f store.KnowledgeFolder) folderView {
	return folderView{
		ID:        f.FolderID.String(),
		Name:      f.Name,
		SortOrder: int(f.SortOrder),
		CreatedAt: f.CreatedAt.Format(time.RFC3339),
		UpdatedAt: f.UpdatedAt.Format(time.RFC3339),
	}
}

func toDocView(d store.KnowledgeDocument) documentView {
	v := documentView{
		ID:                  d.DocumentID.String(),
		FolderID:            d.FolderID.String(),
		FileName:            d.FileName,
		Status:              d.Status,
		Progress:            int(d.Progress),
		Message:             d.Message,
		ItemCount:           int(d.ItemCount),
		BlockCount:          int(d.BlockCount),
		FilteredBlockCount:  int(d.FilteredBlockCount),
		CandidateItemCount:  int(d.CandidateItemCount),
		DiscardedBlockCount: int(d.DiscardedBlockCount),
		SortOrder:           int(d.SortOrder),
		CreatedAt:           d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:           d.UpdatedAt.Format(time.RFC3339),
		MarkdownChars:       int(d.MarkdownChars),
	}
	if d.Error.Valid {
		v.Error = d.Error.String
	}
	if d.ParserLabel.Valid {
		v.ParserLabel = d.ParserLabel.String
	}
	return v
}

// List 返回知识库索引（文件夹 + 文档）。
func (h *Knowledge) List(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	// 知识库是用户级的：project_id 传 nil 表示跨项目
	folders, err := h.q.ListKnowledgeFolders(c.Request().Context(), store.ListKnowledgeFoldersParams{
		TenantID: tenantID,
		// nil 表示用户级知识库（跨项目）
		ProjectID: nil,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("查询文件夹失败: %w", err))
	}

	docs, err := h.q.ListKnowledgeDocuments(c.Request().Context(), store.ListKnowledgeDocumentsParams{
		TenantID:  tenantID,
		ProjectID: nil,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("查询文档失败: %w", err))
	}

	folderViews := make([]folderView, 0, len(folders))
	for _, f := range folders {
		folderViews = append(folderViews, toFolderView(f))
	}

	docViews := make([]documentView, 0, len(docs))
	for _, d := range docs {
		docViews = append(docViews, toDocView(d))
	}

	return httpx.OK(c, map[string]any{
		"folders":   folderViews,
		"documents": docViews,
	})
}

// createFolderRequest 创建文件夹请求。
type createFolderRequest struct {
	Name string `json:"name"`
}

// CreateFolder 创建文件夹。
func (h *Knowledge) CreateFolder(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req createFolderRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "新建文件夹"
	}

	folder, err := h.q.CreateKnowledgeFolder(c.Request().Context(), store.CreateKnowledgeFolderParams{
		TenantID: tenantID,
		// 用户级知识库不绑项目
		ProjectID: nil,
		Name:      name,
		SortOrder: 0,
	})
	if err != nil {
		return httpx.Internal(fmt.Errorf("创建文件夹失败: %w", err))
	}

	return httpx.OK(c, toFolderView(folder))
}

// renameFolderRequest 重命名请求。
type renameFolderRequest struct {
	FolderID string `json:"folderId"`
	Name     string `json:"name"`
}

// RenameFolder 重命名文件夹。
func (h *Knowledge) RenameFolder(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req renameFolderRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	folderID, err := uuid.Parse(req.FolderID)
	if err != nil {
		return httpx.BadRequest("文件夹标识非法")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return httpx.BadRequest("名称不能为空")
	}

	if err := h.q.RenameKnowledgeFolder(c.Request().Context(), store.RenameKnowledgeFolderParams{
		FolderID: folderID, TenantID: tenantID, Name: name,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("重命名失败: %w", err))
	}

	folder, err := h.q.GetKnowledgeFolder(c.Request().Context(), store.GetKnowledgeFolderParams{
		FolderID: folderID, TenantID: tenantID,
	})
	if err != nil {
		return httpx.NotFound("文件夹不存在")
	}

	return httpx.OK(c, toFolderView(folder))
}

// reorderFolderRequest 排序请求。
type reorderFolderRequest struct {
	DraggedFolderID string `json:"draggedFolderId"`
	TargetFolderID  string `json:"targetFolderId"`
	Position        string `json:"position"` // before | after
}

// ReorderFolder 调整文件夹顺序。
func (h *Knowledge) ReorderFolder(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req reorderFolderRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	draggedID, err := uuid.Parse(req.DraggedFolderID)
	if err != nil {
		return httpx.BadRequest("标识非法")
	}
	targetID, err := uuid.Parse(req.TargetFolderID)
	if err != nil {
		return httpx.BadRequest("标识非法")
	}

	ctx := c.Request().Context()

	// 取当前列表，重排后统一写
	folders, err := h.q.ListKnowledgeFolders(ctx, store.ListKnowledgeFoldersParams{
		TenantID: tenantID, ProjectID: nil,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	// 从列表移除被拖拽项
	ordered := make([]store.KnowledgeFolder, 0, len(folders))
	var dragged *store.KnowledgeFolder
	for i := range folders {
		if folders[i].FolderID == draggedID {
			dragged = &folders[i]
			continue
		}
		ordered = append(ordered, folders[i])
	}
	if dragged == nil {
		return httpx.NotFound("被拖拽的文件夹不存在")
	}

	// 找目标位置
	insertAt := len(ordered)
	for i, f := range ordered {
		if f.FolderID == targetID {
			insertAt = i
			if req.Position == "after" {
				insertAt = i + 1
			}
			break
		}
	}

	// 插入
	ordered = append(ordered[:insertAt], append([]store.KnowledgeFolder{*dragged}, ordered[insertAt:]...)...)

	// 批量更新 sort_order
	for i, f := range ordered {
		_ = h.q.ReorderKnowledgeFolder(ctx, store.ReorderKnowledgeFolderParams{
			FolderID: f.FolderID, TenantID: tenantID, SortOrder: int32(i),
		})
	}

	return h.List(c)
}

// deleteFolderRequest 删除请求。
type deleteFolderRequest struct {
	FolderID string `json:"folderId"`
}

// DeleteFolder 删除文件夹。
//
// 级联删除其下所有文档（数据库外键 ON DELETE CASCADE）。
func (h *Knowledge) DeleteFolder(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req deleteFolderRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	folderID, err := uuid.Parse(req.FolderID)
	if err != nil {
		return httpx.BadRequest("文件夹标识非法")
	}

	if err := h.q.DeleteKnowledgeFolder(c.Request().Context(), store.DeleteKnowledgeFolderParams{
		FolderID: folderID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("删除失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true, "message": "已删除"})
}

// deleteDocumentRequest 删除文档请求。
type deleteDocumentRequest struct {
	DocumentID string `json:"documentId"`
}

// DeleteDocument 删除文档。
func (h *Knowledge) DeleteDocument(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req deleteDocumentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}

	if err := h.q.DeleteKnowledgeDocument(c.Request().Context(), store.DeleteKnowledgeDocumentParams{
		DocumentID: docID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("删除失败: %w", err))
	}

	return httpx.OK(c, map[string]any{"success": true, "message": "已删除"})
}

// moveDocumentRequest 移动文档请求。
type moveDocumentRequest struct {
	DocumentID     string `json:"documentId"`
	TargetFolderID string `json:"targetFolderId"`
}

// MoveDocument 移动文档到另一个文件夹。
func (h *Knowledge) MoveDocument(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req moveDocumentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}
	folderID, err := uuid.Parse(req.TargetFolderID)
	if err != nil {
		return httpx.BadRequest("文件夹标识非法")
	}

	// 排在目标文件夹末尾
	existing, _ := h.q.ListDocumentsByFolder(c.Request().Context(), store.ListDocumentsByFolderParams{
		FolderID: folderID, TenantID: tenantID,
	})

	if err := h.q.MoveDocument(c.Request().Context(), store.MoveDocumentParams{
		DocumentID: docID, TenantID: tenantID,
		FolderID: folderID, SortOrder: int32(len(existing)),
	}); err != nil {
		return httpx.Internal(fmt.Errorf("移动失败: %w", err))
	}

	return h.List(c)
}

// uploadDocumentsRequest 上传请求。
//
// 前端调用形式：uploadDocuments(folderId, filePaths)
// bridge 会把两个位置参数转成 { "folderId": ..., "filePaths": ... }
type uploadDocumentsRequest struct {
	FolderID  string   `json:"folderId"`
	FilePaths []string `json:"filePaths"`
}

// UploadDocuments 上传文档。
//
// 前端的 filePaths 是伪路径（upload://xxx），需要从上传队列解析。
func (h *Knowledge) UploadDocuments(c echo.Context) error {
	tenantStr, userID := identity(c)

	var req uploadDocumentsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	if req.FolderID == "" {
		return httpx.BadRequest("缺少 folderId")
	}

	// 解析伪路径到真实文件（upload.Service 负责路径规则）
	realPaths := make([]string, 0, len(req.FilePaths))
	for _, p := range req.FilePaths {
		real, err := h.uploads.Resolve(p)
		if err != nil {
			return httpx.BadRequest(fmt.Sprintf("无法解析上传文件 %s: %v", p, err))
		}
		realPaths = append(realPaths, real)
	}

	if len(realPaths) == 0 {
		return httpx.BadRequest("没有可上传的文件")
	}

	result, err := h.svc.Upload(c.Request().Context(), knowledge.UploadRequest{
		TenantID: tenantStr,
		FolderID: req.FolderID,
		OwnerID:  userID,
		Files:    realPaths,
	})
	if err != nil {
		return httpx.Fail(c, "UPLOAD_FAILED", err.Error())
	}

	// 转成前端要的结构。
	//
	// **不查库**：Upload 返回的 DocumentView 已经是最新状态，
	// 按 ID 再查一遍是纯冗余 —— 批量上传 20 个文件就是 20 次多余查询。
	docs := make([]documentView, 0, len(result.Documents))
	for _, d := range result.Documents {
		docs = append(docs, documentView{
			ID:                 d.DocumentID,
			FolderID:           d.FolderID,
			FileName:           d.FileName,
			Status:             d.Status,
			Progress:           d.Progress,
			Message:            d.Message,
			ItemCount:          d.ItemCount,
			BlockCount:         d.BlockCount,
			FilteredBlockCount: d.FilteredBlockCount,
			CandidateItemCount: d.CandidateItemCount,
			SortOrder:          d.SortOrder,
			CreatedAt:          d.CreatedAt,
			UpdatedAt:          d.UpdatedAt,
		})
	}

	msg := fmt.Sprintf("已上传 %d 个文档", len(docs))
	if len(result.Errors) > 0 {
		msg += fmt.Sprintf("，%d 个失败", len(result.Errors))
	}

	return httpx.OK(c, map[string]any{
		"success":   len(docs) > 0,
		"message":   msg,
		"documents": docs,
	})
}

// retryDocumentRequest 重试请求。
type retryDocumentRequest struct {
	DocumentID string `json:"documentId"`
}

// RetryDocument 重试失败的文档（重新提炼）。
func (h *Knowledge) RetryDocument(c echo.Context) error {
	tenantStr, userID := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req retryDocumentRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}

	provider, modelCfg, err := h.settings.Provider(c.Request().Context(), tenantStr)
	if err != nil {
		return httpx.Fail(c, "MISSING_PROVIDER", err.Error())
	}

	projID, _ := projectID(c)

	// 异步执行，立即返回。
	//
	// **关键**：goroutine 内所有操作都用 context.Background()，
	// 绝不引用 c.Request() —— 请求返回后 context 会被取消，
	// 任务会在几秒内被强杀，用户看到的就是"突然变成 error"。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		result, err := h.extract.Analyze(ctx, knowledge.AnalyzeOptions{
			TenantID:   tenantStr,
			ProjectID:  projID,
			OwnerID:    userID,
			DocumentID: req.DocumentID,
			Provider:   provider,
			Model:      modelCfg,
		})
		if err != nil {
			_ = h.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
				DocumentID: docID, TenantID: tenantID,
				Status: knowledge.StatusError, Progress: 0,
				Message: "提炼失败",
				Error:   pgtype.Text{String: err.Error(), Valid: true},
			})
			return
		}
		_ = result
	}()

	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "已开始重新提炼",
	})
}

// startMatchingRequest 匹配请求。
type startMatchingRequest struct {
	DocumentID string `json:"documentId"`
	BatchSize  int    `json:"batchSize"`
}

// StartMatching 为文档启动知识条目匹配（预留）。
//
// 桌面版用这个把知识点和当前目录章节关联。
// Web 版暂时只做提炼，匹配交给正文生成时按需检索。
func (h *Knowledge) StartMatching(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"success": true,
		"message": "匹配已合并到正文生成流程，无需单独触发",
		"matched": 0,
	})
}

// readMarkdownRequest 读 markdown 请求。
type readMarkdownRequest struct {
	DocumentID string `json:"documentId"`
}

// ReadMarkdown 返回文档的解析后 markdown。
func (h *Knowledge) ReadMarkdown(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req readMarkdownRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}

	doc, err := h.q.GetKnowledgeDocument(c.Request().Context(), store.GetKnowledgeDocumentParams{
		DocumentID: docID, TenantID: tenantID,
	})
	if err != nil {
		return httpx.NotFound("文档不存在")
	}

	// 从对象存储取 markdown
	if doc.MarkdownAssetID == nil {
		return httpx.OK(c, "")
	}

	rc, _, err := h.svc.GetAsset(c.Request().Context(), tenantStr, doc.MarkdownAssetID.String())
	if err != nil {
		return httpx.Fail(c, "READ_FAILED", err.Error())
	}
	defer rc.Close()

	buf := new(strings.Builder)
	if _, err := io.Copy(buf, rc); err != nil {
		return httpx.Fail(c, "READ_FAILED", err.Error())
	}

	return httpx.OK(c, buf.String())
}

// readItemsRequest 读条目请求。
type readItemsRequest struct {
	DocumentID string `json:"documentId"`
}

// ReadItems 返回文档提炼出的知识条目。
func (h *Knowledge) ReadItems(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req readItemsRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	var docID *uuid.UUID
	if req.DocumentID != "" {
		id, err := uuid.Parse(req.DocumentID)
		if err != nil {
			return httpx.BadRequest("文档标识非法")
		}
		docID = &id
	}

	rows, err := h.q.ListKnowledgeItems(c.Request().Context(), store.ListKnowledgeItemsParams{
		TenantID:     tenantID,
		DocumentID:   docID,
		ReviewStatus: pgtype.Text{},
		Lim:          500,
		Off:          0,
	})
	if err != nil {
		return httpx.Internal(err)
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var blockIDs []string
		if len(r.SourceBlockIds) > 0 {
			_ = json.Unmarshal(r.SourceBlockIds, &blockIDs)
		}
		items = append(items, map[string]any{
			"id":               r.ItemID,
			"internal_id":      r.ID.String(),
			"title":            r.Title,
			"resume":           r.Resume,
			"content":          r.Content,
			"source_block_ids": blockIDs,
			"review_status":    r.ReviewStatus,
			"document_id":      r.DocumentID.String(),
		})
	}

	return httpx.OK(c, items)
}

// readAnalysisRequest 读分析请求。
type readAnalysisRequest struct {
	DocumentID string `json:"documentId"`
}

// ReadAnalysis 返回文档的分析报告（块统计、过滤原因分布）。
func (h *Knowledge) ReadAnalysis(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req readAnalysisRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}

	blocks, err := h.q.ListBlocksByDocument(c.Request().Context(), docID)
	if err != nil {
		return httpx.Internal(err)
	}

	// 统计过滤原因分布
	reasonCounts := make(map[string]int)
	filtered := 0
	discarded := make([]map[string]any, 0)

	for _, b := range blocks {
		if !b.IsFiltered {
			continue
		}
		filtered++
		reason := "未知"
		if b.FilterReason.Valid {
			reason = b.FilterReason.String
		}
		reasonCounts[reason]++

		if len(discarded) < 50 {
			discarded = append(discarded, map[string]any{
				"block_ids": []string{b.BlockID},
				"reason":    reason,
				"source":    b.Content,
			})
		}
	}

	items, _ := h.q.ListKnowledgeItems(c.Request().Context(), store.ListKnowledgeItemsParams{
		TenantID: tenantID, DocumentID: &docID,
		ReviewStatus: pgtype.Text{}, Lim: 500, Off: 0,
	})

	return httpx.OK(c, map[string]any{
		"total_blocks":           len(blocks),
		"filtered_blocks_count":  filtered,
		"candidate_items_count":  len(items),
		"filter_reason_counts":   reasonCounts,
		"discarded_block_groups": discarded,
	})
}

// searchRequest 搜索请求。
type searchRequest struct {
	Keyword string `json:"keyword"`
	Page    int    `json:"page"`
}

// Search 全文搜索知识条目。
func (h *Knowledge) Search(c echo.Context) error {
	tenantStr, _ := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req searchRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	keyword := strings.TrimSpace(req.Keyword)
	if keyword == "" {
		return httpx.OK(c, map[string]any{
			"items": []any{}, "total": 0, "page": 1, "pageSize": 20,
		})
	}

	page := req.Page
	if page < 1 {
		page = 1
	}
	const pageSize = 20
	offset := int32((page - 1) * pageSize)

	ctx := c.Request().Context()

	// 知识库是**用户级**的（跨项目复用），搜索不能按当前项目过滤 ——
	// 否则用户切到新项目后，之前积累的知识点全都搜不到了。
	// 这里始终传 nil，表示"不限项目"。

	// 中文关键词直接走 LIKE。
	//
	// 为什么：PG 的 'simple' 分词把中文当一整个 token，
	// "微服务" 不会匹配 "采用微服务架构"（需要词边界）。
	// 要真正支持中文分词得装 zhparser 扩展（需要编译安装），
	// 当前环境没有，所以中文场景用 ILIKE 兜底。
	// 数据量不大时（几千条知识点）ILIKE 性能可接受。
	hasCJK := containsCJK(keyword)

	// 先用全文检索
	var rows []store.SearchKnowledgeItemsRow
	var total int64
	usedFullText := !hasCJK

	if usedFullText {
		rows, err = h.q.SearchKnowledgeItems(ctx, store.SearchKnowledgeItemsParams{
			Keyword:   keyword,
			TenantID:  tenantID,
			ProjectID: nil,
			Lim:       pageSize,
			Off:       offset,
		})
		if err != nil || len(rows) == 0 {
			usedFullText = false
		}
	}

	if usedFullText {
		cnt, cntErr := h.q.CountSearchResults(ctx, store.CountSearchResultsParams{
			Keyword:   keyword,
			TenantID:  tenantID,
			ProjectID: nil,
		})
		if cntErr == nil {
			total = cnt
		}
	}

	// 兜底：LIKE 搜索（中文分词不准时全文检索会漏）
	if !usedFullText {
		likeRows, likeErr := h.q.SearchKnowledgeByLike(ctx, store.SearchKnowledgeByLikeParams{
			Keyword:   pgtype.Text{String: keyword, Valid: true},
			TenantID:  tenantID,
			ProjectID: nil,
			Lim:       pageSize,
			Off:       offset,
		})
		if likeErr != nil {
			return httpx.Internal(likeErr)
		}

		items := make([]map[string]any, 0, len(likeRows))
		for _, r := range likeRows {
			items = append(items, map[string]any{
				"document_id": r.DocumentID.String(),
				"folder_id":   r.FolderID.String(),
				"folder_name": r.FolderName,
				"file_name":   r.FileName,
				"item_id":     r.ItemID,
				"title":       r.Title,
				"resume":      r.Resume,
				"snippet":     textutil.Truncate(r.Content, 200),
				"match_field": matchField(r.Title, r.Resume, r.Content, keyword),
			})
		}

		cnt, _ := h.q.CountLikeResults(ctx, store.CountLikeResultsParams{
			Keyword:   pgtype.Text{String: keyword, Valid: true},
			TenantID:  tenantID,
			ProjectID: nil,
		})

		return httpx.OK(c, map[string]any{
			"items": items, "total": cnt, "page": page, "pageSize": pageSize,
		})
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"document_id": r.DocumentID.String(),
			"folder_id":   r.FolderID.String(),
			"folder_name": r.FolderName,
			"file_name":   r.FileName,
			"item_id":     r.ItemID,
			"title":       r.Title,
			"resume":      r.Resume,
			"snippet":     r.Snippet,
			"match_field": matchField(r.Title, r.Resume, r.Content, keyword),
		})
	}

	return httpx.OK(c, map[string]any{
		"items": items, "total": total, "page": page, "pageSize": pageSize,
	})
}

// matchField 判断关键词命中了哪个字段。
func matchField(title, resume, content, keyword string) string {
	lower := strings.ToLower(keyword)
	if strings.Contains(strings.ToLower(title), lower) {
		return "title"
	}
	if strings.Contains(strings.ToLower(resume), lower) {
		return "resume"
	}
	return "content"
}

// truncate 截断文本。
// uuidOrNil 转可空 UUID 参数。
func uuidOrNil(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.NullUUID{}.UUID
	}
	return *id
}

// uuidOrNilStr 转可空 UUID 参数（字符串输入）。
func uuidOrNilStr(s string) uuid.UUID {
	if s == "" {
		return uuid.NullUUID{}.UUID
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.NullUUID{}.UUID
	}
	return id
}

// nullUUID 返回 nil UUID（表示 NULL）。
//
// sqlc 把可空的 uuid 参数生成为普通 uuid.UUID，
// 查询用 ($2::uuid IS NULL OR ...) 时传全零 UUID 表示 NULL。
func nullUUID() uuid.UUID {
	return uuid.UUID{}
}

// uuidPtrOrNil 把字符串转成可空 UUID 指针。
func uuidPtrOrNil(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

// analyzeSyncRequest 同步提炼请求。
type analyzeSyncRequest struct {
	DocumentID string `json:"documentId"`
}

// AnalyzeSync 同步提炼（调试用）。
//
// 与 retryDocument 的区别：这个直接等提炼完成再返回结果，
// 便于验证链路和排查问题。生产环境应该用异步版本。
func (h *Knowledge) AnalyzeSync(c echo.Context) error {
	tenantStr, userID := identity(c)
	tenantID, err := uuid.Parse(tenantStr)
	if err != nil {
		return httpx.Unauthorized("租户标识非法")
	}

	var req analyzeSyncRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	docID, err := uuid.Parse(req.DocumentID)
	if err != nil {
		return httpx.BadRequest("文档标识非法")
	}

	provider, modelCfg, err := h.settings.Provider(c.Request().Context(), tenantStr)
	if err != nil {
		return httpx.Fail(c, "MISSING_PROVIDER", err.Error())
	}

	projectID, _ := projectID(c)

	// 用独立 context，不受请求超时影响
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := h.extract.Analyze(ctx, knowledge.AnalyzeOptions{
		TenantID:   tenantStr,
		ProjectID:  projectID,
		OwnerID:    userID,
		DocumentID: req.DocumentID,
		Provider:   provider,
		Model:      modelCfg,
	})
	if err != nil {
		_ = h.q.UpdateDocumentStatus(ctx, store.UpdateDocumentStatusParams{
			DocumentID: docID, TenantID: tenantID,
			Status: knowledge.StatusError, Progress: 0,
			Message: "提炼失败",
			Error:   pgtype.Text{String: err.Error(), Valid: true},
		})
		return httpx.Fail(c, "ANALYZE_FAILED", err.Error())
	}

	return httpx.OK(c, result)
}

// containsCJK 判断字符串是否包含中日韩字符。
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
