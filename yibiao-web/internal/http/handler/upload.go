package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/upload"
)

// Upload 处理文件上传。
//
// 前端流程（桌面版兼容）：
//  1. `window.yibiao.file.getPathForFile(file)` → 返回 `upload://<id>`
//  2. 同时 POST /api/upload/raw?... 上传文件内容
//  3. 业务接口收到 `upload://<id>` → 服务端解析成真实路径
//
// 为什么这么设计：让前端 12 处 getPathForFile 调用零改动。
type Upload struct {
	svc *upload.Service
	// maxBytes 单文件上限
	maxBytes int64
}

// NewUpload 创建 handler。
func NewUpload(svc *upload.Service, maxBytes int64) *Upload {
	return &Upload{svc: svc, maxBytes: maxBytes}
}

// Register 注册路由。
func (h *Upload) Register(g *echo.Group) {
	// 预登记：前端拿到 File 对象时先申请 ID
	g.POST("/upload/prepare", h.Prepare)
	// 实际上传
	g.POST("/upload/raw", h.Raw)
	// 查询状态
	g.POST("/upload/status", h.Status)
}

// PrepareRequest 是预登记请求。
type PrepareRequest struct {
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	// UploadID 是客户端指定的 ID（可选）。
	//
	// 为什么要客户端指定：前端 getPathForFile 是**同步** API，
	// 必须立刻返回伪路径。如果等后端生成 ID 就来不及了。
	// 所以让客户端先生成 ID 传过来，双方用同一个 ——
	// 否则后端 Resolve 按自己的 ID 查，永远查不到客户端那个。
	UploadID string `json:"uploadId"`
}

// Prepare 预登记上传，返回伪路径。
//
// 前端在 getPathForFile 被调用时立即请求，拿到 ID 后
// 可以并行上传，不必等待。
func (h *Upload) Prepare(c echo.Context) error {
	var req PrepareRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.FileName == "" {
		return httpx.BadRequest("缺少 fileName")
	}
	if req.Size > h.maxBytes {
		return httpx.Fail(c, "FILE_TOO_LARGE",
			fmt.Sprintf("文件超过上限 %d MB", h.maxBytes>>20))
	}

	id, pseudoPath := h.svc.NewPendingWithID(req.UploadID, req.FileName)

	return httpx.OK(c, map[string]any{
		"id":         id,
		"pseudoPath": pseudoPath,
	})
}

// Raw 接收文件内容（multipart）。
//
// 客户端应带 X-Upload-Id 头（从 /upload/prepare 拿到）。
// 没带时自动生成新 ID，便于简单场景直接上传。
func (h *Upload) Raw(c echo.Context) error {
	ctx := c.Request().Context()

	id := c.Request().Header.Get("X-Upload-Id")

	fh, err := c.FormFile("file")
	if err != nil {
		return httpx.BadRequest("缺少 file 字段")
	}

	if fh.Size > h.maxBytes {
		return httpx.Fail(c, "FILE_TOO_LARGE",
			fmt.Sprintf("文件超过上限 %d MB", h.maxBytes>>20))
	}

	src, err := fh.Open()
	if err != nil {
		return httpx.Internal(fmt.Errorf("打开上传文件失败: %w", err))
	}
	defer src.Close()

	mime := fh.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}

	// 没传 ID 时用文件名生成
	if id == "" {
		id, _ = h.svc.NewPending(fh.Filename)
	}

	entry, err := h.svc.Save(ctx, id, fh.Filename, mime, src, h.maxBytes)
	if err != nil {
		return httpx.Fail(c, "UPLOAD_FAILED", err.Error())
	}

	return httpx.OK(c, map[string]any{
		"id":         entry.ID,
		"pseudoPath": upload.Scheme + entry.ID,
		"fileName":   entry.FileName,
		"size":       entry.Size,
		"mimeType":   entry.MimeType,
	})
}

// UploadStatusRequest 是查询上传状态的请求。
type UploadStatusRequest struct {
	ID string `json:"id"`
}

// Status 查询上传状态。
func (h *Upload) Status(c echo.Context) error {
	var req UploadStatusRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	entry, ok := h.svc.Get(req.ID)
	if !ok {
		return httpx.NotFound("上传记录不存在")
	}

	return httpx.OK(c, map[string]any{
		"id":       entry.ID,
		"fileName": entry.FileName,
		"size":     entry.Size,
		"ready":    entry.Ready,
		"error":    entry.Err,
	})
}

// serveLocalFile 把本地文件作为下载返回（供内部复用）。
func serveLocalFile(c echo.Context, path, fileName, mime string) error {
	c.Response().Header().Set("Content-Type", mime)
	c.Response().Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, asciiFallback(fileName)))
	c.Response().WriteHeader(http.StatusOK)
	return c.File(path)
}

// 确保 strings 被使用（部分工具函数用到）
var _ = strings.TrimSpace
