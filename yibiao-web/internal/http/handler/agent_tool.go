package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/observ"
	"github.com/patrick/yibiao-web/internal/service/agenttool"
)

// AgentTool 接收 sidecar 的工具回调。
//
// 认证：用内部令牌（AGENT_INTERNAL_TOKEN），不是用户 JWT。
// 原因：调用方是 sidecar 进程，不是浏览器。
//
// 安全：租户/项目/工作区信息由 Go 侧根据 sessionId 反查，
// 不信任请求体里的任何 scope 字段。
type AgentTool struct {
	reg *agenttool.Registry
	// resolveInvocation 根据 sessionId/taskId 反查调用上下文。
	// 由上层注入，避免 handler 依赖 Agent 服务。
	resolveInvocation func(ctx echo.Context, sessionID, taskID string) (agenttool.Invocation, error)
	internalToken     string
	// answerer 转发应答给 sidecar（可为 nil）
	answerer QuestionAnswerer
	// questionLister 列出待答问题（可为 nil）
	questionLister QuestionLister
	// questionPusher 推送提问事件（测试用，可为 nil）
	questionPusher QuestionPusher
}

// questionSessions 维护 questionId → sessionId 的映射。
//
// 为什么需要：
//
//	前端 answerQuestion 只传 question_id（桌面版由 Main 维护会话上下文），
//	Web 版必须自己反查。
var questionSessions sync.Map

// rememberQuestionSession 记录问题所属会话。
func rememberQuestionSession(questionID, sessionID string) {
	if questionID != "" && sessionID != "" {
		questionSessions.Store(questionID, sessionID)
	}
}

// lookupQuestionSession 反查会话。
func lookupQuestionSession(questionID string) string {
	if v, ok := questionSessions.Load(questionID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// QuestionLister 列出会话的待答问题。
type QuestionLister interface {
	ListQuestions(ctx context.Context, sessionID string) ([]map[string]any, error)
}

// QuestionAnswerer 是问答转发接口。
type QuestionAnswerer interface {
	AnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error
}

// NewAgentTool 创建 handler。
func NewAgentTool(
	reg *agenttool.Registry,
	internalToken string,
	resolve func(ctx echo.Context, sessionID, taskID string) (agenttool.Invocation, error),
	answerer QuestionAnswerer,
	lister QuestionLister,
	pusher QuestionPusher,
) *AgentTool {
	return &AgentTool{
		reg:               reg,
		internalToken:     internalToken,
		resolveInvocation: resolve,
		answerer:          answerer,
		questionLister:    lister,
		questionPusher:    pusher,
	}
}

// Register 注册路由。挂在不需要用户 JWT 的组下（用内部令牌）。
func (h *AgentTool) Register(g *echo.Group) {
	g.POST("/agent-tools/:name", h.Invoke)
	g.POST("/agent-tools/list", h.List)
	// Agent 提问应答：转发给 sidecar 唤醒挂起的工具
	g.POST("/agent-tools/answer", h.Answer)
	// 待答问题查询（前端轮询）
	g.POST("/agent/get-pending-question", h.GetPendingQuestion)
	// 前端命名：agent.answerQuestion → /api/agent/answer-question
	g.POST("/agent/answer-question", h.AnswerQuestion)
	// 开发调试：直接推一条提问事件验证前端弹窗
	g.POST("/agent/test-push-question", h.TestPushQuestion)
}

// toolCallbackRequest 是 sidecar 发来的回调请求。
type toolCallbackRequest struct {
	Args      json.RawMessage `json:"args"`
	SessionID string          `json:"sessionId"`
	TaskID    string          `json:"taskId"`
}

// Invoke 执行工具。
func (h *AgentTool) Invoke(c echo.Context) error {
	// 内部令牌校验
	if h.internalToken != "" {
		auth := c.Request().Header.Get("Authorization")
		if auth != "Bearer "+h.internalToken {
			return httpx.Unauthorized("内部令牌无效")
		}
	}

	name := c.Param("name")
	if name == "" {
		return httpx.BadRequest("缺少工具名")
	}

	var req toolCallbackRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 反查调用上下文（租户、项目、工作区）
	inv, err := h.resolveInvocation(c, req.SessionID, req.TaskID)
	if err != nil {
		return httpx.Forbidden("无法确定工具调用上下文: " + err.Error())
	}
	inv.Args = req.Args
	if len(inv.Args) == 0 {
		inv.Args = json.RawMessage(`{}`)
	}

	// 诊断：参数传不过来时最容易排查
	observ.LoggerFrom(c.Request().Context()).Info("工具调用参数",
		slog.String("tool", name),
		slog.String("raw_args", string(inv.Args)),
		slog.String("session", req.SessionID),
	)
	inv.SessionID = req.SessionID
	inv.TaskID = req.TaskID

	res, err := h.reg.Invoke(c.Request().Context(), name, inv)
	if err != nil {
		// 工具失败也返回结构化结果，让 Agent 能读到错误并自行修正
		return httpx.OK(c, map[string]any{
			"text": "工具执行失败：" + err.Error(),
			"data": map[string]any{"error": err.Error()},
		})
	}

	return httpx.OK(c, res)
}

// answerRequest 是回答 Agent 提问的请求。
type answerRequest struct {
	SessionID  string `json:"sessionId"`
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

// Answer 回答 Agent 的提问。
//
// 流程：
//
//	Agent 调 ask_user 工具 → 挂起
//	→ sidecar 推 user_question 事件给前端
//	→ 用户回答 → 前端调这个接口
//	→ 本接口转发给 sidecar /sessions/:id/answer
//	→ sidecar 唤醒挂起的 Promise → 工具返回答案 → Agent 继续
func (h *AgentTool) Answer(c echo.Context) error {
	if h.internalToken != "" {
		auth := c.Request().Header.Get("Authorization")
		if auth != "Bearer "+h.internalToken {
			return httpx.Unauthorized("内部令牌无效")
		}
	}

	var req answerRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.SessionID == "" || req.QuestionID == "" {
		return httpx.BadRequest("缺少 sessionId 或 questionId")
	}

	if h.answerer == nil {
		return httpx.Fail(c, "NOT_ENABLED", "提问应答未启用")
	}

	if err := h.answerer.AnswerQuestion(c.Request().Context(), req.SessionID, req.QuestionID, req.Answer); err != nil {
		return httpx.Fail(c, "ANSWER_FAILED", err.Error())
	}

	return httpx.OK(c, map[string]any{"accepted": true})
}

// List 返回全部可用工具声明（调试用）。
func (h *AgentTool) List(c echo.Context) error {
	if h.internalToken != "" {
		auth := c.Request().Header.Get("Authorization")
		if auth != "Bearer "+h.internalToken {
			return httpx.Unauthorized("内部令牌无效")
		}
	}

	decls := h.reg.Declarations()
	names := h.reg.Names()
	return httpx.OK(c, map[string]any{
		"tools": decls,
		"names": names,
		"count": len(decls),
		// 便于排查：确认回调地址前缀正确
		"callbackBase": strings.TrimSuffix(firstCallbackURL(decls), "/"),
	})
}

// firstCallbackURL 取第一个工具的回调地址做样例。
func firstCallbackURL(decls []agenttool.Declaration) string {
	if len(decls) == 0 {
		return ""
	}
	return decls[0].CallbackURL
}

// agentAnswerRequest 是前端 answerQuestion 的请求。
//
// 前端结构（ipc.ts）：
//
//	AgentQuestionAnswerPayload { question_id, option_id?, custom_answer? }
type agentAnswerRequest struct {
	QuestionID   string `json:"question_id"`
	OptionID     string `json:"option_id"`
	CustomAnswer string `json:"custom_answer"`
	SessionID    string `json:"session_id"`
}

// AnswerQuestion 回答提问（前端命名）。
//
// 与前端的 agent.answerQuestion 对应。
// 前端传的是 option_id / custom_answer，需要转成实际答案文本。
func (h *AgentTool) AnswerQuestion(c echo.Context) error {
	var req agentAnswerRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.QuestionID == "" {
		return httpx.BadRequest("缺少 question_id")
	}

	if h.answerer == nil {
		return httpx.Fail(c, "NOT_ENABLED", "提问应答未启用")
	}

	// 前端不传 session_id（桌面版由 Main 维护），这里反查
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = lookupQuestionSession(req.QuestionID)
	}
	if sessionID == "" {
		return httpx.Fail(c, "SESSION_NOT_FOUND",
			"找不到该问题所属的会话，可能已超时")
	}

	// 答案优先用自定义输入，其次选项 ID
	answer := strings.TrimSpace(req.CustomAnswer)
	if answer == "" && req.OptionID != "" {
		// option_id 形如 "opt-1"，需映射回原始选项文本。
		// 这里从待答问题里查回原始 options。
		if h.questionLister != nil {
			if qs, err := h.questionLister.ListQuestions(c.Request().Context(), sessionID); err == nil {
				for _, q := range qs {
					if strOf(q["questionId"]) != req.QuestionID {
						continue
					}
					if opts, ok := q["options"].([]any); ok {
						// opt-1 → 索引 0
						var idx int
						if _, err := fmt.Sscanf(req.OptionID, "opt-%d", &idx); err == nil && idx >= 1 && idx <= len(opts) {
							if s, ok := opts[idx-1].(string); ok {
								answer = s
							}
						}
					}
					break
				}
			}
		}
		// 拿不到原始文本时直接用 ID，至少不丢信息
		if answer == "" {
			answer = req.OptionID
		}
	}

	if answer == "" {
		return httpx.BadRequest("缺少 answer（option_id 或 custom_answer）")
	}

	if err := h.answerer.AnswerQuestion(c.Request().Context(), sessionID, req.QuestionID, answer); err != nil {
		return httpx.Fail(c, "ANSWER_FAILED", err.Error())
	}

	// 答完清掉映射
	questionSessions.Delete(req.QuestionID)

	return httpx.OK(c, map[string]any{"accepted": true, "answer": answer})
}

// TestPushQuestion 直接推送一条提问事件（仅开发环境，用于验证前端弹窗）。
//
// 为什么需要：模型不一定会调用 ask_user，没有它就无法验证链路。
// 生产环境应通过配置禁用。
type testQuestionRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	// 作用域由调用方显式传（工具回调分组没有用户认证）
	TenantID  string `json:"tenantId"`
	UserID    string `json:"userId"`
	ProjectID string `json:"projectId"`
}

func (h *AgentTool) TestPushQuestion(c echo.Context) error {
	var req testQuestionRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.Question == "" {
		req.Question = "测试提问：请确认项目预算金额"
	}
	if len(req.Options) == 0 {
		req.Options = []string{"860 万元", "850 万元"}
	}

	if h.questionPusher == nil {
		return httpx.Fail(c, "NOT_ENABLED", "事件推送未启用")
	}

	// 工具回调分组不走用户认证（用内部令牌），所以这里推不出当前用户。
	//
	// 解决办法：作用域从请求体来。前端调用时带上自己的 tenantId/userId
	// （localStorage 里有）。只用于开发验证，不影响生产链路 ——
	// 真实的提问由 sidecar 事件触发，scope 在 agent.Service 里从 TaskSpec 取。
	tenantID := req.TenantID
	userID := req.UserID
	projectID := req.ProjectID

	if tenantID == "" {
		return httpx.BadRequest("缺少 tenantId（此接口用于开发验证，需显式传作用域）")
	}

	opts := make([]map[string]any, 0, len(req.Options))
	for i, o := range req.Options {
		opts = append(opts, map[string]any{
			"id":          fmt.Sprintf("opt-%d", i+1),
			"label":       o,
			"description": "",
			"recommended": i == 0,
			"custom":      false,
		})
	}

	qid := fmt.Sprintf("q-test-%d", time.Now().UnixNano())
	sid := "test-session"
	rememberQuestionSession(qid, sid)

	// 诊断：打印实际作用域
	c.Logger().Infof("测试推送 scope: tenant=%s user=%s project=%s", tenantID, userID, projectID)

	h.questionPusher.PushQuestion(EventScopeIn{
		TenantID:  tenantID,
		UserID:    userID,
		ProjectID: projectID,
	}, map[string]any{
		"question_id": qid,
		"task_id":     "test-task",
		"session_id":  sid,
		"task_title":  "测试任务",
		"question":    req.Question,
		"options":     opts,
		"asked_at":    time.Now().Format(time.RFC3339),
		"is_primary":  true,
	})

	return httpx.OK(c, map[string]any{"pushed": true, "questionId": qid})
}

// EventScopeIn 是事件作用域（handler 层）。
type EventScopeIn struct {
	TenantID  string
	UserID    string
	ProjectID string
}

// QuestionPusher 推送提问事件。
type QuestionPusher interface {
	PushQuestion(scope EventScopeIn, payload map[string]any)
}

// pendingQuestionRequest 是查询待答问题的请求。
type pendingQuestionRequest struct {
	SessionID string `json:"sessionId"`
}

// GetPendingQuestion 返回当前待答的 Agent 问题。
//
// 前端在任务运行期间轮询这个接口，有提问就弹窗让用户回答。
//
// 返回结构必须与前端 AgentQuestion 类型一致：
//
//	{ question_id, task_id, session_id, task_title, question, options[{id,label,description,recommended,custom}], asked_at }
//
// 没有待答问题时返回 null（不是空对象）。
func (h *AgentTool) GetPendingQuestion(c echo.Context) error {
	var req pendingQuestionRequest
	_ = c.Bind(&req)

	// 没有待答问题时返回显式 null（前端用 === null 判断）
	if h.questionLister == nil || req.SessionID == "" {
		return httpx.OKNull(c)
	}

	questions, err := h.questionLister.ListQuestions(c.Request().Context(), req.SessionID)
	if err != nil || len(questions) == 0 {
		return httpx.OKNull(c)
	}

	// 只返回第一个（前端一次处理一个）
	raw := questions[0]

	// sidecar 的结构转成前端要的结构
	opts := make([]map[string]any, 0, 4)
	if rawOpts, ok := raw["options"].([]any); ok {
		for i, o := range rawOpts {
			label := ""
			if s, ok := o.(string); ok {
				label = s
			}
			opts = append(opts, map[string]any{
				"id":          fmt.Sprintf("opt-%d", i+1),
				"label":       label,
				"description": "",
				// 第一个选项作为推荐（通常是用户最可能选的）
				"recommended": i == 0,
				"custom":      false,
			})
		}
	}

	// 没有选项时给一个「自由输入」选项，前端会渲染输入框
	if len(opts) == 0 {
		opts = append(opts, map[string]any{
			"id":          "custom",
			"label":       "自行输入",
			"description": "",
			"recommended": false,
			"custom":      true,
		})
	}

	// 记住这个问题属于哪个会话，供 answerQuestion 反查
	qid := strOf(raw["questionId"])
	rememberQuestionSession(qid, strOf(raw["sessionId"]))

	return httpx.OK(c, map[string]any{
		"question_id": qid,
		"task_id":     strOf(raw["sessionId"]),
		"session_id":  strOf(raw["sessionId"]),
		"task_title":  "技术方案生成",
		"question":    strOf(raw["question"]),
		"options":     opts,
		"asked_at":    strOf(raw["askedAt"]),
		"is_primary":  true,
	})
}

// strOf 从 map 里取字符串。
func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
