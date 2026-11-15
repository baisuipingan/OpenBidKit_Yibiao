// Package artifact 管理版本化产物（个人资产库）。
//
// 设计依据：docs/web-migration/08-storage-architecture.md
//
// 核心能力：
//   - Put：保存新版本，自动分配版本号，去重
//   - Get：按 name 取最新版，或指定版本
//   - List：列出项目/用户级资产
//   - CommitWorkspace：把 Agent 工作区的产物批量入库（关键）
//
// 与业务的边界：
//
//	业务表只存 asset_id，不存路径。这样换存储后端不影响业务数据。
package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/store"
)

// 资产类别常量。
const (
	KindTender      = "tender"       // 招标文件原文/转换后 Markdown
	KindOriginal    = "original"     // 原方案
	KindBidItems    = "bid-items"    // 招标解析项产物
	KindOutline     = "outline"      // 目录
	KindGlobalFacts = "global-facts" // 全局事实变量
	KindContent     = "content"      // 正文
	KindImage       = "image"        // 配图
	KindExport      = "export"       // 导出的 Word
	KindKnowledge   = "knowledge"    // 知识库文档
	KindCredential  = "credential"   // 资信库素材
	KindTemplate    = "template"     // 导出模板
	KindAttachment  = "attachment"   // 其他附件
)

// 错误。
var (
	ErrAssetNotFound = errors.New("资产不存在")
	ErrTooLarge      = errors.New("文件超过大小限制")
)

// Service 是资产服务。
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	backend storage.Backend
	log     *slog.Logger
	// maxFileBytes 单文件上限，防止 Agent 产出巨大文件撑爆存储
	maxFileBytes int64
}

// NewService 创建服务。
func NewService(pool *pgxpool.Pool, backend storage.Backend, log *slog.Logger) *Service {
	return &Service{
		pool:         pool,
		q:            store.New(pool),
		backend:      backend,
		log:          log,
		maxFileBytes: 200 << 20, // 200MB
	}
}

// PutRequest 是保存资产的参数。
type PutRequest struct {
	Scope    storage.Scope
	OwnerID  string // 操作者 user_id
	Name     string // 逻辑名，如 "tender.md"
	Kind     string
	MimeType string
	// Content 是文件内容
	Content io.Reader
	// Size 已知时填，-1 表示未知
	Size int64
	// Source 来源：upload | agent | export | import
	Source string
	// TaskID 如果是 Agent 产出的
	TaskID string
	// Metadata 附加信息
	Metadata map[string]any
}

// Asset 是资产记录。
type Asset struct {
	ID          string
	Scope       storage.Scope
	Name        string
	Version     int
	Kind        string
	StorageKey  string
	MimeType    string
	SizeBytes   int64
	ContentHash string
	Source      string
	TaskID      string
	CreatedAt   time.Time
}

// Put 保存一个新版本。
//
// 版本策略：
//   - 同名资产自动 version+1
//   - 内容 hash 与已有版本相同则直接复用（秒传，不重复占存储）
func (s *Service) Put(ctx context.Context, req PutRequest) (*Asset, error) {
	if req.Scope.TenantID == "" {
		return nil, fmt.Errorf("缺少租户标识")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("缺少资产名称")
	}
	if req.Kind == "" {
		return nil, fmt.Errorf("缺少资产类别")
	}
	if req.Source == "" {
		req.Source = "upload"
	}

	// 读入内存：需要算 hash + 可能需重试上传
	// 大文件场景应改成流式+分片，当前任务产物最大几十 MB，内存可接受
	data, err := io.ReadAll(io.LimitReader(req.Content, s.maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取内容失败: %w", err)
	}
	if int64(len(data)) > s.maxFileBytes {
		return nil, fmt.Errorf("%w: 超过 %d MB", ErrTooLarge, s.maxFileBytes>>20)
	}

	hash := storage.HashBytes(data)

	// 推断 mime
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = guessMime(req.Name)
	}

	// 查同内容是否已有（秒传）
	existing, err := s.q.FindAssetByHash(ctx, store.FindAssetByHashParams{
		TenantID:    mustUUID(req.Scope.TenantID),
		ContentHash: hash,
	})
	if err == nil && existing.StorageKey != "" {
		// 内容已存在：只登记新记录，复用 storage_key
		s.log.Info("资产内容已存在，复用存储",
			slog.String("hash", hash[:12]),
			slog.String("existing_key", existing.StorageKey),
		)
		return s.createRecord(ctx, req, hash, existing.StorageKey, mimeType, int64(len(data)))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("查询内容哈希失败: %w", err)
	}

	// 分配版本号
	maxVer, err := s.q.GetMaxAssetVersion(ctx, store.GetMaxAssetVersionParams{
		TenantID:  mustUUID(req.Scope.TenantID),
		ProjectID: nullableUUID(req.Scope.ProjectID),
		Name:      req.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("查询版本号失败: %w", err)
	}
	version := int(maxVer) + 1

	// 生成 assetID 与 storageKey
	assetID := uuid.NewString()
	key := storage.Key(req.Scope, req.Kind, assetID, version, req.Name)

	// 上传
	if _, err := s.backend.Put(ctx, key, bytes.NewReader(data), int64(len(data)), mimeType); err != nil {
		return nil, fmt.Errorf("上传失败: %w", err)
	}

	return s.createRecord(ctx, req, hash, key, mimeType, int64(len(data)))
}

// createRecord 写数据库记录。
func (s *Service) createRecord(
	ctx context.Context,
	req PutRequest,
	hash, storageKey, mimeType string,
	size int64,
) (*Asset, error) {
	// 版本号：复用存储时也要取最新版本+1
	maxVer, err := s.q.GetMaxAssetVersion(ctx, store.GetMaxAssetVersionParams{
		TenantID:  mustUUID(req.Scope.TenantID),
		ProjectID: nullableUUID(req.Scope.ProjectID),
		Name:      req.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("查询版本号失败: %w", err)
	}
	version := int(maxVer) + 1

	row, err := s.q.CreateAsset(ctx, store.CreateAssetParams{
		TenantID:    mustUUID(req.Scope.TenantID),
		ProjectID:   nullableUUID(req.Scope.ProjectID),
		OwnerID:     nullableUUID(req.OwnerID),
		Name:        req.Name,
		Version:     int32(version),
		Kind:        req.Kind,
		StorageKey:  storageKey,
		MimeType:    mimeType,
		SizeBytes:   size,
		ContentHash: hash,
		Source:      req.Source,
		TaskID:      nullableUUID(req.TaskID),
		Metadata:    mustJSON(req.Metadata),
	})
	if err != nil {
		return nil, fmt.Errorf("登记资产失败: %w", err)
	}

	return toAsset(row), nil
}

// Get 读取资产内容。
//
// version <= 0 表示取最新版。
func (s *Service) Get(ctx context.Context, scope storage.Scope, name string, version int) (io.ReadCloser, *Asset, error) {
	var row store.Asset
	var err error

	if version > 0 {
		row, err = s.q.GetAssetByNameVersion(ctx, store.GetAssetByNameVersionParams{
			TenantID:  mustUUID(scope.TenantID),
			ProjectID: nullableUUID(scope.ProjectID),
			Name:      name,
			Version:   int32(version),
		})
	} else {
		row, err = s.q.GetLatestAssetByName(ctx, store.GetLatestAssetByNameParams{
			TenantID:  mustUUID(scope.TenantID),
			ProjectID: nullableUUID(scope.ProjectID),
			Name:      name,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("%w: %s", ErrAssetNotFound, name)
		}
		return nil, nil, fmt.Errorf("查询资产失败: %w", err)
	}

	rc, err := s.backend.Get(ctx, row.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// 数据库有记录但存储没有 = 数据不一致，要报出来
			s.log.Error("资产记录存在但存储缺失",
				slog.String("asset_id", row.ID.String()),
				slog.String("storage_key", row.StorageKey),
			)
			return nil, nil, fmt.Errorf("资产文件丢失: %s", row.Name)
		}
		return nil, nil, fmt.Errorf("读取资产失败: %w", err)
	}

	return rc, toAsset(row), nil
}

// GetByID 按 ID 读取。
func (s *Service) GetByID(ctx context.Context, tenantID, assetID string) (io.ReadCloser, *Asset, error) {
	row, err := s.q.GetAsset(ctx, store.GetAssetParams{
		ID:       mustUUID(assetID),
		TenantID: mustUUID(tenantID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrAssetNotFound
		}
		return nil, nil, fmt.Errorf("查询资产失败: %w", err)
	}

	rc, err := s.backend.Get(ctx, row.StorageKey)
	if err != nil {
		return nil, nil, fmt.Errorf("读取资产失败: %w", err)
	}
	return rc, toAsset(row), nil
}

// ListByProject 列出项目的资产。
func (s *Service) ListByProject(ctx context.Context, tenantID, projectID, kind string, limit, offset int) ([]*Asset, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListAssetsByProject(ctx, store.ListAssetsByProjectParams{
		TenantID:  mustUUID(tenantID),
		ProjectID: nullableUUID(projectID),
		Kind:      kind,
		LimitVal:  int32(limit),
		OffsetVal: int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("列出资产失败: %w", err)
	}

	out := make([]*Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAsset(r))
	}
	return out, nil
}

// ListByTask 列出某 Agent 任务的产物。
func (s *Service) ListByTask(ctx context.Context, tenantID, taskID string) ([]*Asset, error) {
	rows, err := s.q.ListAssetsByTask(ctx, store.ListAssetsByTaskParams{
		TaskID:   nullableUUID(taskID),
		TenantID: mustUUID(tenantID),
	})
	if err != nil {
		return nil, fmt.Errorf("列出任务产物失败: %w", err)
	}

	out := make([]*Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAsset(r))
	}
	return out, nil
}

// Delete 软删除资产。
//
// 不删存储对象：留出恢复窗口，由清理任务统一处理。
func (s *Service) Delete(ctx context.Context, tenantID, assetID string) error {
	return s.q.SoftDeleteAsset(ctx, store.SoftDeleteAssetParams{
		ID:       mustUUID(assetID),
		TenantID: mustUUID(tenantID),
	})
}

// SetRef 建立业务对象到资产的引用。
func (s *Service) SetRef(ctx context.Context, tenantID, ownerType, ownerID, slot, assetID string) error {
	return s.q.UpsertAssetRef(ctx, store.UpsertAssetRefParams{
		OwnerType: ownerType,
		OwnerID:   mustUUID(ownerID),
		Slot:      slot,
		AssetID:   mustUUID(assetID),
		TenantID:  mustUUID(tenantID),
	})
}

// GetRef 读取业务对象引用的资产。
func (s *Service) GetRef(ctx context.Context, ownerType, ownerID, slot string) (*Asset, error) {
	ref, err := s.q.GetAssetRef(ctx, store.GetAssetRefParams{
		OwnerType: ownerType,
		OwnerID:   mustUUID(ownerID),
		Slot:      slot,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAssetNotFound
		}
		return nil, err
	}

	row, err := s.q.GetAsset(ctx, store.GetAssetParams{
		ID:       ref.AssetID,
		TenantID: ref.TenantID,
	})
	if err != nil {
		return nil, err
	}
	return toAsset(row), nil
}

// Backend 暴露存储后端（供工作区物化使用）。
func (s *Service) Backend() storage.Backend { return s.backend }

// MaxFileBytes 返回单文件上限。
func (s *Service) MaxFileBytes() int64 { return s.maxFileBytes }

// --- 工具函数 ---

func toAsset(r store.Asset) *Asset {
	a := &Asset{
		ID:          r.ID.String(),
		Name:        r.Name,
		Version:     int(r.Version),
		Kind:        r.Kind,
		StorageKey:  r.StorageKey,
		MimeType:    r.MimeType,
		SizeBytes:   r.SizeBytes,
		ContentHash: r.ContentHash,
		Source:      r.Source,
		CreatedAt:   r.CreatedAt,
	}
	a.Scope.TenantID = r.TenantID.String()
	if r.ProjectID != nil {
		a.Scope.ProjectID = r.ProjectID.String()
	}
	if r.TaskID != nil {
		a.TaskID = r.TaskID.String()
	}
	return a
}

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func nullableUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

func mustJSON(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	// 用简单的方式序列化，失败返回空对象
	b := marshalJSON(m)
	if b == nil {
		return []byte("{}")
	}
	return b
}

// guessMime 按扩展名猜 mime。
func guessMime(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	switch ext {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "application/octet-stream"
}

// marshalJSON 序列化为 JSON，失败返回 nil。
func marshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// LatestTaskIDs 返回最近产生某类资产的 task_id 列表（去重，按时间倒序）。
//
// 用途：手动重同步时找最近一次任务。
func (s *Service) LatestTaskIDs(ctx context.Context, tenantID, kind string, limit int) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.q.LatestAssetTaskIDs(ctx, store.LatestAssetTaskIDsParams{
		TenantID: mustUUID(tenantID),
		Kind:     kind,
		Limit:    int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r != "" {
			out = append(out, r)
		}
	}
	return out, nil
}
