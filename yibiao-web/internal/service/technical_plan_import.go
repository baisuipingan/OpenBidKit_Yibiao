package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/docparse"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/store"
)

// ImportResult 是文档导入结果。
//
// 字段名与前端期望的一致（success / markdown / message）。
type ImportResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	// Markdown 是解析后的正文
	Markdown string `json:"markdown"`
	// FileName 原始文件名
	FileName string `json:"fileName"`
	// ParserLabel 用了哪个解析器
	ParserLabel string `json:"parserLabel"`
	// AssetID 保存成的资产 ID
	AssetID string `json:"assetId"`
	// Chars 字数
	Chars int `json:"chars"`
}

// ImportTender 导入招标文件。
//
// 流程：
//  1. 解析文档（docx/pdf → markdown）
//  2. 保存原始文件与 Markdown 为资产
//  3. 更新 technical_plan_meta 的资产引用
func (s *TechnicalPlanService) ImportTender(
	ctx context.Context,
	tenantID, projectID, ownerID string,
	paths []string,
) (*ImportResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("未提供文件")
	}

	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureProject(ctx, tid, pid); err != nil {
		return nil, err
	}
	if _, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	}); err != nil {
		return nil, fmt.Errorf("初始化技术方案失败: %w", err)
	}

	scope := storage.Scope{TenantID: tenantID, ProjectID: projectID}

	// 逐个文件解析（当前只支持单个，多文件时拼接）
	var markdownParts []string
	var lastFileName, lastParserLabel string
	var totalChars int

	for _, path := range paths {
		if _, statErr := os.Stat(path); statErr != nil {
			return nil, fmt.Errorf("文件不存在: %s", filepath.Base(path))
		}

		result, err := docparse.ToMarkdown(ctx, path)
		if err != nil {
			// 解析失败要给出可操作的提示
			return nil, fmt.Errorf("解析 %s 失败: %w", filepath.Base(path), err)
		}

		markdownParts = append(markdownParts, result.Markdown)
		lastFileName = filepath.Base(path)
		lastParserLabel = result.ParserLabel
		totalChars += len([]rune(result.Markdown))

		// 保存原始文件为资产（便于后续重新解析或排查）
		if f, err := os.Open(path); err == nil {
			_, putErr := s.artifacts.Put(ctx, artifact.PutRequest{
				Scope:   scope,
				OwnerID: ownerID,
				Name:    "tender-original/" + filepath.Base(path),
				Kind:    artifact.KindTender,
				Content: f,
				Source:  "upload",
			})
			f.Close()
			if putErr != nil {
				s.log.Warn("保存招标原文失败", slog.String("error", putErr.Error()))
			}
		}
	}

	markdown := strings.Join(markdownParts, "\n\n---\n\n")

	// 保存 Markdown 为资产
	assetID, err := s.saveMarkdownAsset(ctx, scope, ownerID, "tender.md", markdown)
	if err != nil {
		return nil, err
	}

	// 更新元数据
	if err := s.q.UpdateTenderDocument(ctx, store.UpdateTenderDocumentParams{
		ProjectID:           pid,
		TenantID:            tid,
		TenderMarkdownPath:  "", // 改为资产引用后不再存路径
		TenderMarkdownHash:  hashString(markdown),
		TenderMarkdownChars: int32(totalChars),
		TenderParserLabel:   lastParserLabel,
	}); err != nil {
		return nil, fmt.Errorf("更新招标文件信息失败: %w", err)
	}

	// 更新 tender_files JSON（前端用它显示文件名列表）
	tenderFiles := fmt.Sprintf(`[{"fileName":%q,"chars":%d,"parserLabel":%q}]`,
		lastFileName, totalChars, lastParserLabel)
	if err := s.q.UpdateTenderFiles(ctx, store.UpdateTenderFilesParams{
		ProjectID:   pid,
		TenantID:    tid,
		TenderFiles: []byte(tenderFiles),
	}); err != nil {
		s.log.Warn("更新文件列表失败", slog.String("error", err.Error()))
	}

	s.log.Info("招标文件已导入",
		slog.String("project_id", projectID),
		slog.String("file", lastFileName),
		slog.Int("chars", totalChars),
	)

	return &ImportResult{
		Success:     true,
		Message:     "招标文件已导入",
		Markdown:    markdown,
		FileName:    lastFileName,
		ParserLabel: lastParserLabel,
		AssetID:     assetID,
		Chars:       totalChars,
	}, nil
}

// ImportOriginalPlan 导入原方案。
func (s *TechnicalPlanService) ImportOriginalPlan(
	ctx context.Context,
	tenantID, projectID, ownerID string,
	paths []string,
) (*ImportResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("未提供文件")
	}

	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureProject(ctx, tid, pid); err != nil {
		return nil, err
	}

	scope := storage.Scope{TenantID: tenantID, ProjectID: projectID}

	path := paths[0]
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, fmt.Errorf("文件不存在")
	}

	result, err := docparse.ToMarkdown(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}

	assetID, err := s.saveMarkdownAsset(ctx, scope, ownerID, "original-plan.md", result.Markdown)
	if err != nil {
		return nil, err
	}

	fileName := filepath.Base(path)
	chars := len([]rune(result.Markdown))

	if err := s.q.UpdateOriginalPlanDocument(ctx, store.UpdateOriginalPlanDocumentParams{
		ProjectID:                 pid,
		TenantID:                  tid,
		OriginalPlanFileName:      fileName,
		OriginalPlanMarkdownPath:  "",
		OriginalPlanMarkdownHash:  hashString(result.Markdown),
		OriginalPlanMarkdownChars: int32(chars),
		OriginalPlanParserLabel:   result.ParserLabel,
	}); err != nil {
		return nil, fmt.Errorf("更新原方案信息失败: %w", err)
	}

	return &ImportResult{
		Success:     true,
		Message:     "原方案已导入",
		Markdown:    result.Markdown,
		FileName:    fileName,
		ParserLabel: result.ParserLabel,
		AssetID:     assetID,
		Chars:       chars,
	}, nil
}

// ReadTenderMarkdown 读取招标正文。
func (s *TechnicalPlanService) ReadTenderMarkdown(ctx context.Context, tenantID, projectID string) (string, error) {
	scope := storage.Scope{TenantID: tenantID, ProjectID: projectID}

	rc, asset, err := s.artifacts.Get(ctx, scope, "tender.md", 0)
	if err != nil {
		if errors.Is(err, artifact.ErrAssetNotFound) {
			return "", fmt.Errorf("尚未导入招标文件")
		}
		return "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("读取正文失败: %w", err)
	}
	_ = asset
	return string(data), nil
}

// RemoveTender 移除招标文件。
func (s *TechnicalPlanService) RemoveTender(ctx context.Context, tenantID, projectID string) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}

	if err := s.q.ClearTenderDocument(ctx, store.ClearTenderDocumentParams{
		ProjectID: pid,
		TenantID:  tid,
	}); err != nil {
		return fmt.Errorf("清除招标文件失败: %w", err)
	}

	// 资产不删除（保留历史），只清引用
	return nil
}

// saveMarkdownAsset 把 Markdown 存为资产，返回资产 ID。
func (s *TechnicalPlanService) saveMarkdownAsset(
	ctx context.Context,
	scope storage.Scope,
	ownerID, name, content string,
) (string, error) {
	asset, err := s.artifacts.Put(ctx, artifact.PutRequest{
		Scope:   scope,
		OwnerID: ownerID,
		Name:    name,
		Kind:    artifact.KindTender,
		Content: strings.NewReader(content),
		Size:    int64(len(content)),
		Source:  "upload",
	})
	if err != nil {
		return "", fmt.Errorf("保存正文资产失败: %w", err)
	}
	return asset.ID, nil
}

// hashString 计算内容哈希（用 SHA-256 前 16 字节的十六进制）。
func hashString(s string) string {
	return storage.HashBytes([]byte(s))
}

// 需要引入的包
var (
	_ = uuid.Nil
	_ = pgxpool.Pool{}
)

// SaveSectionContent 保存单节正文。
//
// 用于正文生成完成后回写。
func (s *TechnicalPlanService) SaveSectionContent(
	ctx context.Context,
	tenantID, projectID, nodeID, content string,
) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}

	// 节点 ID 可能是 UUID（数据库主键）或字符串（前端/AI 的 external_id）。
	// 两种都要支持：前端保存正文时传的是它自己生成的 "n1" 这类 ID。
	var nid uuid.UUID
	if parsed, parseErr := uuid.Parse(nodeID); parseErr == nil {
		nid = parsed
		if err := s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
			NodeID:    nid,
			ProjectID: pid,
			Content:   content,
		}); err != nil {
			return fmt.Errorf("保存正文失败: %w", err)
		}
	} else {
		// 按 external_id 定位
		found, findErr := s.q.FindOutlineNodeByExternalID(ctx, store.FindOutlineNodeByExternalIDParams{
			TenantID: tid, ProjectID: pid, ExternalID: nodeID,
		})
		if findErr != nil {
			return fmt.Errorf("未找到章节: %s", nodeID)
		}
		nid = found
		if err := s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
			NodeID:    nid,
			ProjectID: pid,
			Content:   content,
		}); err != nil {
			return fmt.Errorf("保存正文失败: %w", err)
		}
	}

	// 更新小节状态为 success
	if err := s.q.UpsertContentSection(ctx, store.UpsertContentSectionParams{
		NodeID:    nid,
		ProjectID: pid,
		TenantID:  tid,
		Status:    "success",
		Error:     "",
	}); err != nil {
		s.log.Warn("更新小节状态失败", slog.String("error", err.Error()))
	}

	return nil
}
