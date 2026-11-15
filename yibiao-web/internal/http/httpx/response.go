// Package httpx 提供 HTTP 请求/响应封装。
//
// 设计依据（见 docs/web-migration/01-bridge-contract.md §1.2）：
// 业务失败也返回 HTTP 200，用 { ok: false, error } 表达。
// 原因：桌面版 bridge 通过 Promise reject 传递业务错误，前端大量 try/catch
// 依赖 error.code / error.message 结构。若用 4xx，fetch 不会 reject，
// 前端全部要改。
//
// 只有认证失败(401)、权限不足(403)、请求体非法(400)、服务崩溃(500)
// 才用非 200 状态码——这些情况下前端本来也没有业务逻辑可走。
package httpx

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/auth"
)

// 业务错误码。前端按 code 做分支。
const (
	CodeInvalidArgument = "INVALID_ARGUMENT"
	CodeNotFound        = "NOT_FOUND"
	CodeConflict        = "CONFLICT"
	CodeForbidden       = "FORBIDDEN"
	CodeUnauthorized    = "UNAUTHORIZED"
	CodeInternal        = "INTERNAL"
	CodeTaskRunning     = "TASK_RUNNING"
	CodeUploadPending   = "UPLOAD_PENDING"
)

// Error 是响应体里的错误结构。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

// Envelope 是统一响应信封。
type Envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// APIError 是可携带状态码与业务码的错误。
type APIError struct {
	Status  int
	Code    string
	Message string
	Detail  any
}

func (e *APIError) Error() string { return e.Message }

// NewAPIError 构造 API 错误。
func NewAPIError(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg}
}

// WithDetail 附加详情。
func (e *APIError) WithDetail(d any) *APIError {
	e.Detail = d
	return e
}

// 常用错误的快捷构造。

func BadRequest(msg string) *APIError {
	return NewAPIError(http.StatusBadRequest, CodeInvalidArgument, msg)
}

func Unauthorized(msg string) *APIError {
	return NewAPIError(http.StatusUnauthorized, CodeUnauthorized, msg)
}

func Forbidden(msg string) *APIError {
	return NewAPIError(http.StatusForbidden, CodeForbidden, msg)
}

func NotFound(msg string) *APIError {
	return NewAPIError(http.StatusNotFound, CodeNotFound, msg)
}

func Conflict(code, msg string) *APIError {
	return NewAPIError(http.StatusConflict, code, msg)
}

// exposeInternalDetail 控制是否把内部错误详情返回给客户端。
//
// 为什么需要：err.Error() 里可能带表名、SQL、文件路径。
// 开发环境要看到方便排查，生产环境必须隐藏 ——
// 否则攻击者能据此推断表结构和目录布局。
var exposeInternalDetail = false

// SetExposeInternalDetail 由启动流程按环境设置（dev=true, prod=false）。
func SetExposeInternalDetail(expose bool) {
	exposeInternalDetail = expose
}

func Internal(err error) *APIError {
	e := NewAPIError(http.StatusInternalServerError, CodeInternal, "服务内部错误")
	if exposeInternalDetail && err != nil {
		e.Detail = err.Error()
	}
	return e
}

// OK 返回成功响应。
func OK(c echo.Context, data any) error {
	return c.JSON(http.StatusOK, Envelope{OK: true, Data: data})
}

// OKNull 返回「成功但数据为空」的响应。
//
// 与 OK(c, nil) 的区别：
//
//	Envelope.Data 带 omitempty，传 nil 时 data 字段会被省略，
//	前端读到的是 undefined 而不是 null。
//	对于「没有结果」有明确语义的接口（如待答问题），
//	必须返回显式 null，否则前端 if 判断会走错分支。
func OKNull(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"ok":   true,
		"data": nil,
	})
}

// Fail 返回业务失败响应（HTTP 200）。
func Fail(c echo.Context, code, msg string) error {
	return c.JSON(http.StatusOK, Envelope{OK: false, Error: &Error{Code: code, Message: msg}})
}

// FailWithDetail 返回带详情的业务失败响应（HTTP 200）。
func FailWithDetail(c echo.Context, code, msg string, detail any) error {
	return c.JSON(http.StatusOK, Envelope{OK: false, Error: &Error{Code: code, Message: msg, Detail: detail}})
}

// ErrorHandler 是全局错误处理器。
//
// 所有 handler 返回的 error 都汇聚到这里，统一成信封格式。
// 这样 handler 里可以放心 `return err`，不用每处手写响应。
func ErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		// 认证包的错误映射
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			apiErr = Unauthorized(err.Error())
		case errors.Is(err, auth.ErrTokenInvalid), errors.Is(err, auth.ErrTokenExpired):
			apiErr = Unauthorized(err.Error())
		case errors.Is(err, auth.ErrAccountDisabled):
			apiErr = Forbidden(err.Error())
		case errors.Is(err, auth.ErrAccountPending):
			apiErr = Forbidden(err.Error())
		default:
			if he, ok := err.(*echo.HTTPError); ok {
				apiErr = NewAPIError(he.Code, CodeInternal, http.StatusText(he.Code))
			} else {
				apiErr = Internal(err)
			}
		}
	}

	body := Envelope{OK: false, Error: &Error{Code: apiErr.Code, Message: apiErr.Message, Detail: apiErr.Detail}}

	// 业务失败（4xx 之外的业务码）用 200，让前端走正常解析
	status := apiErr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}

	if err := c.JSON(status, body); err != nil {
		c.Logger().Error(err)
	}
}
