// Package export 实现技术方案导出 Word。
//
// 完整链路：
//  1. 从数据库读目录树与正文（Markdown）
//  2. 转成受限 HTML（openxml.ParseMarkdown + RenderSection）
//  3. 调 C# 助手 insert-body-html 生成 docx
//  4. 保存为资产，返回下载地址
//
// **为什么导出在服务端而不是浏览器**：
//
//	标书排版依赖 Open XML 精确控制（页眉页脚、页码域、章节页框、图片画框）。
//	浏览器侧没有等价能力。C# 助手 23k 行做的就是这个，原样复用。
package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/openxml"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

// Service 处理导出。
type Service struct {
	openxml   *openxml.Client
	artifacts *artifact.Service
	log       *slog.Logger
	// templatesDir 存放导出模板（.docx）
	templatesDir string
}

// NewService 创建服务。
func NewService(client *openxml.Client, artifacts *artifact.Service, templatesDir string, log *slog.Logger) *Service {
	return &Service{
		openxml:      client,
		artifacts:    artifacts,
		templatesDir: templatesDir,
		log:          log,
	}
}

// Section 是导出用的小节。
type Section struct {
	NodeID  string
	Title   string
	Level   int
	Content string // Markdown
}

// Request 是导出请求。
type Request struct {
	TenantID  string
	ProjectID string
	OwnerID   string
	// ProjectName 项目名称（用于文件名）
	ProjectName string
	// Sections 按顺序排列的正文小节
	Sections []Section
	// TemplatePath 导出模板路径（.docx）。
	// 为空时用内置的空模板。
	TemplatePath string
	// TargetID 模板里正文占位符的 tag（默认 "technical-plan"）
	TargetID string
}

// Result 是导出结果。
type Result struct {
	AssetID   string `json:"assetId"`
	FileName  string `json:"fileName"`
	SizeBytes int64  `json:"sizeBytes"`
	// DownloadURL 下载地址
	DownloadURL string `json:"downloadUrl"`
	// Warning 非致命提示
	Warning string `json:"warning,omitempty"`
}

// ExportWord 导出 Word 文档。
func (s *Service) ExportWord(ctx context.Context, req Request) (*Result, error) {
	if len(req.Sections) == 0 {
		return nil, fmt.Errorf("没有正文内容，请先生成技术方案")
	}

	// 1. 拼接受限 HTML
	html, stats := s.buildHTML(req.Sections)
	if html == "" {
		return nil, fmt.Errorf("正文内容为空")
	}

	s.log.Info("开始导出 Word",
		slog.String("project_id", req.ProjectID),
		slog.Int("sections", len(req.Sections)),
		slog.Int("paragraphs", stats.Paragraphs),
		slog.Int("tables", stats.Tables),
		slog.Int("figures", stats.Figures),
	)

	// 2. 准备模板源文件
	templateSrc, cleanup, err := s.prepareTemplate(ctx, req)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 3. 调 C# 助手（带文件进出）
	targetID := req.TargetID
	if targetID == "" {
		targetID = "technical-plan"
	}

	// 工作区内的相对路径：助手在它自己的 --workspace 下找这个文件
	const remoteTemplate = "export/template.docx"

	// 产物取回到本地临时文件
	outLocal := filepath.Join(s.templatesDir, fmt.Sprintf("out-%d.docx", time.Now().UnixNano()))
	defer os.Remove(outLocal)

	// 物化图片到助手工作区
	//
	// C# 的 img[data-yb-asset-ref] 要求「工作区内相对路径」且文件真实存在。
	// 所以必须把正文引用的每张图从资产库下载到临时目录，
	// 再一起送进助手工作区。
	files := map[string]string{remoteTemplate: templateSrc}

	if len(stats.AssetRefs) > 0 {
		imgDir, err := os.MkdirTemp("", "export-images-*")
		if err != nil {
			return nil, fmt.Errorf("创建图片临时目录失败: %w", err)
		}
		defer os.RemoveAll(imgDir)

		missing := 0
		for _, ref := range stats.AssetRefs {
			localPath := filepath.Join(imgDir, filepath.Base(ref))

			// 从资产库读
			rc, _, err := s.artifacts.Get(ctx, storage.Scope{
				TenantID:  req.TenantID,
				ProjectID: req.ProjectID,
			}, ref, 0)
			if err != nil {
				s.log.Warn("配图不存在，跳过",
					slog.String("ref", ref),
					slog.String("error", err.Error()),
				)
				missing++
				continue
			}

			data, readErr := io.ReadAll(rc)
			rc.Close()
			if readErr != nil {
				missing++
				continue
			}

			if err := os.WriteFile(localPath, data, 0o644); err != nil {
				missing++
				continue
			}

			// 关键：key 必须与正文里的 data-yb-asset-ref 完全一致
			files[ref] = localPath
		}

		if missing > 0 {
			s.log.Warn("部分配图缺失",
				slog.Int("missing", missing),
				slog.Int("total", len(stats.AssetRefs)),
			)
		}
	}

	result, err := s.openxml.ExecuteWithFiles(ctx, map[string]any{
		"action":                  "insert-body-html",
		"input":                   remoteTemplate,
		"target_id":               targetID,
		"image_max_width_percent": 90,
		"html":                    html,
	}, openxml.ExecuteOptions{
		Files:       files,
		CollectBack: map[string]string{remoteTemplate: outLocal},
	})
	if err != nil {
		return nil, fmt.Errorf("生成 Word 失败: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("生成 Word 失败: %s", result.Error)
	}

	// 4. 读取取回的 docx
	data, err := os.ReadFile(outLocal)
	if err != nil {
		return nil, fmt.Errorf("读取生成的文档失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("生成的文档为空")
	}

	// 5. 保存为资产
	fileName := buildFileName(req.ProjectName)
	asset, err := s.artifacts.Put(ctx, artifact.PutRequest{
		Scope: storage.Scope{
			TenantID:  req.TenantID,
			ProjectID: req.ProjectID,
		},
		OwnerID:  req.OwnerID,
		Name:     fileName,
		Kind:     artifact.KindExport,
		MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Content:  strings.NewReader(string(data)),
		Size:     int64(len(data)),
		Source:   "export",
		Metadata: map[string]any{
			"sectionCount": len(req.Sections),
			"paragraphs":   stats.Paragraphs,
			"tables":       stats.Tables,
			"figures":      stats.Figures,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("保存导出文件失败: %w", err)
	}

	s.log.Info("Word 导出完成",
		slog.String("asset_id", asset.ID),
		slog.Int64("bytes", int64(len(data))),
	)

	res := &Result{
		AssetID:     asset.ID,
		FileName:    fileName,
		SizeBytes:   int64(len(data)),
		DownloadURL: "/api/assets/" + asset.ID + "/download",
	}

	// 配图未生成时给提示
	if stats.Figures > 0 {
		res.Warning = fmt.Sprintf("文档含 %d 处配图占位，图片生成功能尚未接入，导出文件中这些位置会留空。", stats.Figures)
	}

	return res, nil
}

// HTMLStats 是 HTML 生成统计。
type HTMLStats struct {
	Paragraphs int
	Tables     int
	Figures    int
	Chars      int
	// AssetRefs 正文引用的图片相对路径（需物化到助手工作区）
	AssetRefs []string
}

// buildHTML 把正文小节拼成受限 HTML。
func (s *Service) buildHTML(sections []Section) (string, HTMLStats) {
	var sb strings.Builder
	var stats HTMLStats

	// 文档开头加一个空的块，确保有内容
	sb.WriteString("<!-- yibiao:block -->\n")

	for _, sec := range sections {
		if strings.TrimSpace(sec.Content) == "" {
			continue
		}

		// Markdown → 结构化块
		blocks := openxml.ParseMarkdown(sec.Content)

		// 如果正文没有一级标题，补上小节标题，保证结构完整
		hasTitle := false
		for _, h := range blocks.Headings {
			if h.Level <= sec.Level {
				hasTitle = true
				break
			}
		}
		if !hasTitle && sec.Title != "" {
			lvl := sec.Level
			if lvl < 1 || lvl > 6 {
				lvl = 2
			}
			blocks.Headings = append([]openxml.Heading{{Level: lvl, Text: sec.Title}}, blocks.Headings...)
		}

		html := openxml.RenderSection(sec.NodeID, blocks)
		sb.WriteString(html)

		stats.Paragraphs += len(blocks.Paragraphs)
		stats.Tables += len(blocks.Tables)
		stats.Figures += len(blocks.Figures)
		stats.Chars += len([]rune(sec.Content))

		// 收集图片引用
		for _, f := range blocks.Figures {
			if f.AssetRef != "" {
				stats.AssetRefs = append(stats.AssetRefs, f.AssetRef)
			}
		}
	}

	return sb.String(), stats
}

// prepareTemplate 准备导出模板源文件。
//
// 只返回源路径：实际复制到助手工作区由 ExecuteWithFiles 完成。
func (s *Service) prepareTemplate(ctx context.Context, req Request) (string, func(), error) {
	// 指定了模板路径
	if req.TemplatePath != "" {
		if _, err := os.Stat(req.TemplatePath); err != nil {
			return "", func() {}, fmt.Errorf("模板文件不存在: %w", err)
		}
		return req.TemplatePath, func() {}, nil
	}

	// 用内置空模板
	builtin := filepath.Join(s.templatesDir, "blank.docx")
	if _, err := os.Stat(builtin); err == nil {
		return builtin, func() {}, nil
	}

	return "", func() {}, fmt.Errorf(
		"未找到导出模板。请先在「模版设置」中创建或上传一个 .docx 模板，" +
			"模板里需要有一个 tag 为 yibiao:body:technical-plan 的块级内容控件作为正文插入位置")
}

// copyToTemp 复制文件到临时目录。
func (s *Service) copyToTemp(src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("读取模板失败: %w", err)
	}
	tmp := filepath.Join(s.templatesDir, fmt.Sprintf("export-%d.docx", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("写入临时模板失败: %w", err)
	}
	return tmp, nil
}

// extractOutputPath 从助手返回里取输出文件路径。
func extractOutputPath(res *openxml.Result) string {
	if res.Output == nil {
		return ""
	}
	// output 可能是字符串或对象
	switch v := res.Output.(type) {
	case string:
		return v
	case map[string]any:
		if p, ok := v["path"].(string); ok {
			return p
		}
		if p, ok := v["output"].(string); ok {
			return p
		}
	}
	return ""
}

// buildFileName 生成导出文件名。
//
// 格式：<项目名>_技术方案_<时间戳>.docx
func buildFileName(projectName string) string {
	name := strings.TrimSpace(projectName)
	if name == "" {
		name = "技术方案"
	}
	// 清理文件名里的非法字符
	safe := make([]rune, 0, len(name))
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			safe = append(safe, '_')
		default:
			safe = append(safe, r)
		}
	}
	base := strings.TrimSpace(string(safe))
	if len([]rune(base)) > 60 {
		base = string([]rune(base)[:60])
	}
	return fmt.Sprintf("%s_技术方案_%s.docx", base, time.Now().Format("20060102_1504"))
}

// 编译期断言
var _ = errors.New
