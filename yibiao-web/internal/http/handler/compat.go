package handler

import (
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
)

// Compat 提供桌面版专属能力的 Web 兼容实现。
//
// 背景：
//
//	前端是从桌面版移植的，会调用一批桌面版特有的接口：
//	  - 官方账号 / 许可证 / 在线服务状态
//	  - 自动确认（Electron 对话框）
//	  - 公告同步（依赖 Electron 主进程缓存）
//	  - ui.setCurrentView（Electron 多窗口导航）
//
//	Web 版没有这些概念，但**不能返回 404** —— 前端拿不到值会报错、
//	甚至白屏。所以提供「明确的空状态」而不是「接口不存在」。
//
// 设计原则：
//   - 返回结构必须与前端类型完全一致（否则 undefined 报错）
//   - 语义上表达「此功能在 Web 版不适用」，而不是假装成功
//   - 不伪造业务数据
type Compat struct{}

// NewCompat 创建 handler。
func NewCompat() *Compat {
	return &Compat{}
}

// Register 注册路由。
func (h *Compat) Register(g *echo.Group) {
	// 官方账号与许可证：Web 版由平台统一授权，不走客户端许可
	g.POST("/official-account/get-state", h.OfficialAccountState)
	g.POST("/license/get-status", h.LicenseStatus)

	// 在线服务：Web 版所有服务都在服务端，无「必需在线服务」检查
	g.POST("/required-online-services/get-status", h.RequiredOnlineServices)

	// 自动确认：桌面版用 Electron 对话框，Web 版用前端自己的弹窗
	g.POST("/auto-confirmation/get-state", h.AutoConfirmationState)

	// 公告：Web 版未接入公告系统
	g.POST("/notice/sync-current", h.NoticeSync)
	g.POST("/notice/prepare-media", h.PrepareNoticeMedia)
	g.POST("/notice/release-media", h.ReleaseNoticeMedia)

	// 视图导航：桌面版多窗口，Web 版由前端路由自己管
	g.POST("/ui/set-current-view", h.SetCurrentView)

	// Agent 状态：Web 版按任务粒度管理，没有全局「Agent 服务状态」
	g.POST("/agent/get-status", h.AgentStatus)
}

// OfficialAccountState 返回官方账号状态。
//
// Web 版：不使用客户端授权账号，返回「未登录」的空状态。
func (h *Compat) OfficialAccountState(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"loggedIn":     false,
		"email":        "",
		"nickname":     "",
		"balanceCents": 0,
		"balanceYuan":  "0.00",
		"vipLevel":     "",
		"vipExpiresAt": "",
		// Web 版说明：平台侧统一提供模型服务，不走客户端授权
		"managedByPlatform": true,
	})
}

// LicenseStatus 返回许可证状态。
//
// Web 版：由平台授权，不需要客户端许可证。
func (h *Compat) LicenseStatus(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		// 视为已授权（平台侧已管控）
		"status":            "active",
		"activated":         true,
		"expiresAt":         "",
		"deviceBound":       false,
		"managedByPlatform": true,
	})
}

// RequiredOnlineServices 返回必需在线服务状态。
//
// Web 版：所有服务都在服务端，客户端无需检查。
func (h *Compat) RequiredOnlineServices(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		// 空列表表示「没有必需检查的在线服务」
		"services": []any{},
		"allReady": true,
		"missing":  []any{},
	})
}

// AutoConfirmationState 返回自动确认状态。
//
// 桌面版：某些操作（如覆盖已有文件）由 Main 弹原生对话框确认。
// Web 版：这类确认由前端 AppDialog 处理，不需要 Main 参与。
func (h *Compat) AutoConfirmationState(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"enabled": false,
		"rules":   []any{},
		// Web 版说明：确认弹窗由前端负责
		"handledByFrontend": true,
	})
}

// NoticeSync 同步公告。
//
// Web 版：未接入公告系统，作为空操作成功。
func (h *Compat) NoticeSync(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"synced":  false,
		"notice":  nil,
		"message": "Web 版未接入公告系统",
	})
}

// SetCurrentView 设置当前视图。
//
// 桌面版：Main 记录当前视图用于多窗口恢复。
// Web 版：前端路由自己管，这里接受但不做任何事。
func (h *Compat) SetCurrentView(c echo.Context) error {
	return httpx.OK(c, map[string]any{"success": true})
}

// AgentStatus 返回 Agent 服务状态。
//
// Web 版按任务粒度管理，没有全局状态。返回「就绪」让前端正常渲染。
func (h *Compat) AgentStatus(c echo.Context) error {
	return httpx.OK(c, map[string]any{
		"running": false,
		"ready":   true,
		"tasks":   []any{},
	})
}

// NoticeMediaCacheRequest 与前端 NoticeMediaCacheRequest 对齐。
type NoticeMediaCacheRequest struct {
	URL      string `json:"url"`
	NoticeID string `json:"noticeId"`
}

// NoticeCachedMedia 与前端 NoticeCachedMedia 对齐。
type NoticeCachedMedia struct {
	LeaseID  string `json:"leaseId"`
	FilePath string `json:"filePath"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
}

// PrepareNoticeMedia 缓存公告媒体。
//
// Web 版：浏览器可以直接加载 URL，不需要主进程下载到本地再给 file:// 路径。
// 所以直接把原始 URL 返回，前端按 URL 加载即可。
func (h *Compat) PrepareNoticeMedia(c echo.Context) error {
	var req NoticeMediaCacheRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// path 为空 + url 有值 → 前端会当远程资源加载
	return httpx.OK(c, NoticeCachedMedia{
		LeaseID:  "direct-" + req.NoticeID,
		FilePath: "",
		URL:      req.URL,
		MimeType: "",
	})
}

// ReleaseNoticeMedia 释放媒体缓存。
//
// Web 版没有本地缓存，直接成功。
func (h *Compat) ReleaseNoticeMedia(c echo.Context) error {
	return httpx.OK(c, map[string]any{"released": true})
}
