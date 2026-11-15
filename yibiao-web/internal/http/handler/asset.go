package handler

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service/artifact"
)

// Asset 处理资产（个人资产库）接口。
type Asset struct {
	svc *artifact.Service
}

// NewAsset 创建 handler。
func NewAsset(svc *artifact.Service) *Asset {
	return &Asset{svc: svc}
}

// Register 注册路由。
func (h *Asset) Register(g *echo.Group) {
	g.POST("/assets/list", h.List)
	g.POST("/assets/task", h.ListByTask)
	g.POST("/assets/delete", h.Delete)
	// 下载走 GET：浏览器直接打开时无法设置请求头
	g.GET("/assets/:id/download", h.Download)
	// HEAD 用于探测文件大小与类型（部分下载器会先发 HEAD）
	g.HEAD("/assets/:id/download", h.Download)
}

// ListRequest 是列出资产的请求。
type ListRequest struct {
	Kind   string `json:"kind"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// List 列出项目资产。
func (h *Asset) List(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req ListRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	assets, err := h.svc.ListByProject(c.Request().Context(), tenantID, projectID, req.Kind, req.Limit, req.Offset)
	if err != nil {
		return httpx.Internal(err)
	}

	out := make([]map[string]any, 0, len(assets))
	for _, a := range assets {
		out = append(out, assetJSON(a))
	}
	return httpx.OK(c, map[string]any{
		"assets": out,
		"count":  len(out),
	})
}

// TaskRequest 是按任务查产物的请求。
type TaskRequest struct {
	TaskID string `json:"taskId"`
}

// ListByTask 列出某 Agent 任务的产物。
func (h *Asset) ListByTask(c echo.Context) error {
	tenantID, _ := identity(c)

	var req TaskRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.TaskID == "" {
		return httpx.BadRequest("缺少 taskId")
	}

	assets, err := h.svc.ListByTask(c.Request().Context(), tenantID, req.TaskID)
	if err != nil {
		return httpx.Internal(err)
	}

	out := make([]map[string]any, 0, len(assets))
	for _, a := range assets {
		out = append(out, assetJSON(a))
	}
	return httpx.OK(c, map[string]any{
		"assets": out,
		"count":  len(out),
	})
}

// DeleteRequest 是删除资产的请求。
type DeleteRequest struct {
	AssetID string `json:"assetId"`
}

// Delete 软删除资产。
func (h *Asset) Delete(c echo.Context) error {
	tenantID, _ := identity(c)

	var req DeleteRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.AssetID == "" {
		return httpx.BadRequest("缺少 assetId")
	}

	if err := h.svc.Delete(c.Request().Context(), tenantID, req.AssetID); err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, map[string]bool{"success": true})
}

// Download 下载资产内容。
//
// 支持 ?version=N 指定版本；不传取最新。
// 支持 ?inline=1 让浏览器内联打开（图片预览用）。
func (h *Asset) Download(c echo.Context) error {
	tenantID, _ := identity(c)
	assetID := c.Param("id")
	if assetID == "" {
		return httpx.BadRequest("缺少资产 ID")
	}

	rc, asset, err := h.svc.GetByID(c.Request().Context(), tenantID, assetID)
	if err != nil {
		if err == artifact.ErrAssetNotFound {
			return httpx.NotFound("资产不存在")
		}
		return httpx.Internal(err)
	}
	defer rc.Close()

	// 文件名：从 name 取最后一段
	filename := asset.Name
	if i := strings.LastIndexByte(filename, '/'); i >= 0 {
		filename = filename[i+1:]
	}

	disposition := "attachment"
	if c.QueryParam("inline") == "1" {
		disposition = "inline"
	}

	res := c.Response()
	res.Header().Set("Content-Type", asset.MimeType)
	res.Header().Set("Content-Length", strconv.FormatInt(asset.SizeBytes, 10))
	// RFC 5987：中文文件名需要 filename* 参数
	res.Header().Set("Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
			disposition, asciiFallback(filename), urlEscape(filename)))
	// 内容 hash 可用于缓存校验
	if asset.ContentHash != "" {
		res.Header().Set("ETag", `"`+asset.ContentHash[:16]+`"`)
	}

	res.WriteHeader(http.StatusOK)

	// HEAD 请求只要头，不写 body
	if c.Request().Method == http.MethodHead {
		return nil
	}

	_, err = io.Copy(res, rc)
	return err
}

// assetJSON 把资产转成前端要的结构。
func assetJSON(a *artifact.Asset) map[string]any {
	return map[string]any{
		"id":          a.ID,
		"name":        a.Name,
		"version":     a.Version,
		"kind":        a.Kind,
		"mimeType":    a.MimeType,
		"sizeBytes":   a.SizeBytes,
		"contentHash": a.ContentHash,
		"source":      a.Source,
		"taskId":      a.TaskID,
		"createdAt":   a.CreatedAt,
		"downloadUrl": "/api/assets/" + a.ID + "/download",
	}
}

// asciiFallback 为非 ASCII 文件名生成 ASCII 兜底名。
func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "download"
	}
	return out
}

// urlEscape 按 RFC 5987 转义文件名。
func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		// 保留非保留字符
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
