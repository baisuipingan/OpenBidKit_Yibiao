// Package server 装配 HTTP 路由。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/config"
	"github.com/patrick/yibiao-web/internal/events"
	"github.com/patrick/yibiao-web/internal/http/handler"
	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/http/middleware"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/agent/bidanalysis"
	"github.com/patrick/yibiao-web/internal/service/agent/consistency"
	"github.com/patrick/yibiao-web/internal/service/agent/content"
	"github.com/patrick/yibiao-web/internal/service/agent/outline"
	"github.com/patrick/yibiao-web/internal/service/agenttool"
	agenttoolbuiltin "github.com/patrick/yibiao-web/internal/service/agenttool/tools"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/blind"
	"github.com/patrick/yibiao-web/internal/service/duplicate"
	"github.com/patrick/yibiao-web/internal/service/export"
	"github.com/patrick/yibiao-web/internal/service/image"
	"github.com/patrick/yibiao-web/internal/service/knowledge"
	"github.com/patrick/yibiao-web/internal/service/openxml"
	"github.com/patrick/yibiao-web/internal/service/rejection"
	"github.com/patrick/yibiao-web/internal/service/render"
	"github.com/patrick/yibiao-web/internal/service/secret"
	"github.com/patrick/yibiao-web/internal/service/settings"
	"github.com/patrick/yibiao-web/internal/service/storage"
	"github.com/patrick/yibiao-web/internal/service/taskrun"
	"github.com/patrick/yibiao-web/internal/service/upload"
	"github.com/patrick/yibiao-web/internal/service/workspace"
	"github.com/patrick/yibiao-web/internal/store"
	"strconv"
)

// Deps 是装配依赖。
type Deps struct {
	Config  *config.Config
	Logger  *slog.Logger
	Pool    *pgxpool.Pool
	Bus     *events.Bus
	Version string
}

// New 创建 Echo 实例并注册全部路由。
func New(d Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// 统一错误处理：所有 error 汇聚成 { ok, error } 信封
	e.HTTPErrorHandler = httpx.ErrorHandler

	// 中间件顺序有意义：RequestID → Recover → Logger → CORS
	e.Use(middleware.RequestID())
	e.Use(middleware.Recover(d.Logger))
	e.Use(middleware.Logger(d.Logger))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins:     d.Config.HTTP.AllowedOrigins,
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders:     []string{"Content-Type", "Authorization", "X-Project-Id", "X-Request-Id"},
		ExposeHeaders:    []string{"X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           3600,
	}))
	// 请求体大小限制
	e.Use(echomw.BodyLimit(byteSizeString(d.Config.HTTP.MaxUploadBytes)))

	// --- 装配服务 ---
	authMgr := auth.NewManager(d.Config.Auth)
	authSvc := service.NewAuthService(d.Pool, authMgr)
	// 注意：tpSvc 依赖 artifactSvc，装配顺序不能乱
	// （artifactSvc 在上面已创建）
	usageRec := service.NewUsageRecorder(d.Logger)

	// 存储后端：按配置选择 LocalFS 或 S3
	storageBackend, err := buildStorageBackend(context.Background(), d.Config.Store, d.Logger)
	if err != nil {
		// 存储不可用是致命错误，服务不该启动
		d.Logger.Error("初始化存储后端失败", slog.String("error", err.Error()))
		panic(err)
	}

	// 资产服务（个人资产库）
	artifactSvc := artifact.NewService(d.Pool, storageBackend, d.Logger)

	// 技术方案服务（依赖资产服务）
	tpSvc := service.NewTechnicalPlanService(d.Pool, artifactSvc, d.Logger)

	// 临时工作区管理
	wsMgr, err := workspace.NewManager(d.Config.Agent.WorkspaceRoot, d.Logger)
	if err != nil {
		d.Logger.Error("初始化工作区管理器失败", slog.String("error", err.Error()))
		panic(err)
	}

	// 密钥加密器：生产必须配 SECRETS_KEY，开发可降级明文
	var cipher settings.Cipher
	if c, err := secret.NewCipher(d.Config.Auth.SecretsKey); err == nil {
		cipher = c
	} else {
		d.Logger.Warn("未配置 SECRETS_KEY，API Key 将以 base64 形式存储（仅适合开发）",
			slog.String("error", err.Error()))
		cipher = secret.PlainCipher{}
	}

	settingsSvc := settings.NewService(d.Pool, cipher, d.Config.AI.DefaultBaseURL, d.Logger)

	// 上传服务：处理前端文件上传（getPathForFile 伪路径）
	uploadSvc, err := upload.NewService(filepath.Join(d.Config.Store.Root, "_uploads"), d.Logger)
	if err != nil {
		d.Logger.Error("初始化上传服务失败", slog.String("error", err.Error()))
		panic(err)
	}

	// C# OpenXmlHelper（可选：未配置路径时不启用导出）
	// oc 提到外层：暗标检查也要用它
	var exportSvc *export.Service
	var openxmlClient *openxml.Client
	if d.Config.OpenXML.Binary != "" {
		oc, err := openxml.NewClient(context.Background(), openxml.Config{
			Binary:     d.Config.OpenXML.Binary,
			PoolSize:   d.Config.OpenXML.PoolSize,
			JobTimeout: d.Config.OpenXML.JobTimeout,
		}, d.Logger)
		if err != nil {
			// 助手起不来不阻塞服务启动，只让导出不可用
			d.Logger.Error("OpenXmlHelper 初始化失败，导出功能不可用", slog.String("error", err.Error()))
		} else {
			openxmlClient = oc
			templatesDir := filepath.Join(d.Config.Store.Root, "templates")
			_ = os.MkdirAll(templatesDir, 0o755)
			exportSvc = export.NewService(oc, artifactSvc, templatesDir, d.Logger)
		}
	} else {
		d.Logger.Warn("未配置 OPENXML_BINARY，Word 导出功能不可用")
	}

	// 渲染服务（Mermaid / HTML 转图）
	var renderClient *render.Client
	var imageSvc *image.Service
	if d.Config.Render.BaseURL != "" {
		renderClient = render.NewClient(d.Config.Render.BaseURL, d.Config.Agent.InternalToken)
		// AI 生图：从租户配置读模型（可能未配，运行时判断）
		imageSvc = image.NewService(renderClient, nil, artifactSvc, d.Logger)
		d.Logger.Info("渲染服务已配置", slog.String("url", d.Config.Render.BaseURL))
	}

	// Agent 业务工具注册表
	toolReg := agenttool.NewRegistry(
		"/api/agent-tools",
		"http://127.0.0.1:8080", // sidecar 与服务同容器，走回环
		d.Logger,
	)
	if err := agenttoolbuiltin.RegisterAll(toolReg); err != nil {
		d.Logger.Error("注册内置工具失败", slog.String("error", err.Error()))
		panic(err)
	}
	// 图片生成工具（需要图片服务）
	if imageSvc != nil {
		if err := agenttoolbuiltin.RegisterImageTools(toolReg, imageSvc); err != nil {
			d.Logger.Warn("注册图片工具失败", slog.String("error", err.Error()))
		}
	}

	d.Logger.Info("Agent 业务工具已注册", slog.Int("count", len(toolReg.Names())), slog.Any("tools", toolReg.Names()))

	// Agent sidecar 客户端
	agentClient := agent.NewClient(d.Config.Agent.BaseURL, d.Config.Agent.InternalToken)
	agentSvc := agent.NewService(agentClient, wsMgr, d.Logger)

	// 任务运行记录：让重启后能看出「哪些任务被中断了」
	taskRunSvc := taskrun.NewService(d.Pool, d.Logger)
	agentSvc.SetTaskRunRecorder(&taskRunRecorderAdapter{svc: taskRunSvc})
	// 事件总线：把 sidecar 的提问推给前端
	agentSvc.SetEventPublisher(&eventPublisherAdapter{bus: d.Bus})

	// 启动时清理僵死任务（心跳超过 5 分钟）
	if n, err := taskRunSvc.RecoverOnStartup(context.Background(), 300); err != nil {
		d.Logger.Warn("清理僵死任务失败", slog.String("error", err.Error()))
	} else if n > 0 {
		d.Logger.Info("已清理僵死任务", slog.Int64("count", n))
	}
	// taskH 在下方装配；完成回调通过闭包变量调用 MarkDone，
	// 不依赖 /tasks/events SSE 是否有订阅者。
	var taskH *handler.Task

	bidRunner := bidanalysis.NewRunner(agentSvc, artifactSvc, d.Logger)
	// 分析产物入库后回写到 technical_plan_meta，
	// 否则前端 loadState 拿不到分析结果。
	bidRunner.SetOnAnalysisDone(func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool) {
		n, err := tpSvc.SyncBidAnalysisFromArtifacts(ctx, tenantID, projectID, taskID, artifactSvc, d.Logger)
		if err != nil {
			d.Logger.Warn("分析结果回写失败", slog.String("task_id", taskID), slog.String("error", err.Error()))
		}
		status := "success"
		if !succeeded || n == 0 {
			status = "error"
			msg := "招标文件解析失败：模型未返回有效结果（请检查文本模型 Base URL / API Key / 额度）"
			if markErr := tpSvc.MarkBidAnalysisFailed(ctx, tenantID, projectID, taskID, msg); markErr != nil {
				d.Logger.Warn("写入招标分析失败状态失败", slog.String("error", markErr.Error()))
			}
		}
		if taskH != nil {
			taskH.MarkDone(taskID, status)
		}
		// 推送最终任务事件，让仍连着 SSE 的页面立刻刷新；
		// 未连上的页面靠 load-state 里的 bidAnalysisTask / bidAnalysisTasks。
		if runtime, loadErr := tpSvc.LoadBidAnalysisRuntime(ctx, tenantID, projectID); loadErr == nil {
			taskPayload, _ := runtime["bidAnalysisTask"].(map[string]any)
			if taskPayload == nil {
				taskPayload = map[string]any{"task_id": taskID, "type": "bid-analysis", "status": status}
			} else {
				taskPayload["status"] = status
				taskPayload["task_id"] = taskID
				taskPayload["type"] = "bid-analysis"
			}
			d.Bus.Publish("task", events.Scope{TenantID: tenantID, ProjectID: projectID}, map[string]any{
				"task": taskPayload,
				"technicalPlanPatch": map[string]any{
					"bidAnalysisTask":     taskPayload,
					"bidAnalysisTasks":    runtime["bidAnalysisTasks"],
					"bidAnalysisProgress": runtime["bidAnalysisProgress"],
					"projectOverview":     runtime["projectOverview"],
					"techRequirements":    runtime["techRequirements"],
				},
			})
		}
	})
	outlineRunner := outline.NewRunner(agentSvc, artifactSvc, d.Logger)
	// 目录产物入库后写进节点表，否则前端目录页拿不到数据。
	outlineRunner.SetOnOutlineDone(func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool) {
		n, err := tpSvc.SyncOutlineFromArtifacts(ctx, tenantID, projectID, taskID, artifactSvc, d.Logger)
		if err != nil {
			d.Logger.Warn("目录回写失败", slog.String("task_id", taskID), slog.String("error", err.Error()))
		}
		status := "success"
		if !succeeded || err != nil || n == 0 {
			status = "error"
			msg := "目录生成失败：未得到有效目录"
			if err != nil {
				msg = "目录生成失败：" + err.Error()
			}
			if markErr := tpSvc.MarkOutlineFailed(ctx, tenantID, projectID, taskID, msg); markErr != nil {
				d.Logger.Warn("写入目录失败状态失败", slog.String("error", markErr.Error()))
			}
		}
		if taskH != nil {
			taskH.MarkDone(taskID, status)
		}
		// 推送最终任务事件 + outlineData，让页面立刻离开「等待生成任务启动」
		state, loadErr := tpSvc.LoadState(ctx, tenantID, projectID)
		if loadErr != nil {
			d.Logger.Warn("目录完成后读状态失败", slog.String("error", loadErr.Error()))
			return
		}
		taskPayload := state.OutlineGenerationTask
		if taskPayload == nil {
			taskPayload = map[string]any{"task_id": taskID, "type": "outline-generation", "status": status, "progress": 100}
		}
		d.Bus.Publish("task", events.Scope{TenantID: tenantID, ProjectID: projectID}, map[string]any{
			"task": taskPayload,
			"technicalPlanPatch": map[string]any{
				"outlineGenerationTask": taskPayload,
				"outlineData":           state.OutlineData,
			},
		})
	})
	contentRunner := content.NewRunner(agentSvc, artifactSvc, d.Logger)
	// 正文产物入库后写进节点表，否则前端看不到生成内容。
	contentRunner.SetOnContentDone(func(ctx context.Context, tenantID, projectID, taskID string, succeeded bool) {
		n, err := tpSvc.SyncContentFromArtifacts(ctx, tenantID, projectID, taskID, artifactSvc, d.Logger)
		if err != nil {
			d.Logger.Warn("正文回写失败", slog.String("task_id", taskID), slog.String("error", err.Error()))
		}
		status := "success"
		if !succeeded || err != nil || n == 0 {
			status = "error"
		}
		if taskH != nil {
			taskH.MarkDone(taskID, status)
		}
		state, loadErr := tpSvc.LoadState(ctx, tenantID, projectID)
		if loadErr != nil {
			return
		}
		taskPayload := state.ContentGenerationTask
		if taskPayload == nil {
			taskPayload = map[string]any{"task_id": taskID, "type": "content-generation", "status": status}
		} else {
			taskPayload["status"] = status
			taskPayload["task_id"] = taskID
			taskPayload["type"] = "content-generation"
		}
		d.Bus.Publish("task", events.Scope{TenantID: tenantID, ProjectID: projectID}, map[string]any{
			"task": taskPayload,
			"technicalPlanPatch": map[string]any{
				"contentGenerationTask":     taskPayload,
				"contentGenerationSections": state.ContentGenerationSections,
				"contentGenerationRuntime":  state.ContentGenerationRuntime,
			},
		})
	})
	consistencyRunner := consistency.NewRunner(agentSvc, d.Logger)

	// --- 装配 handler ---
	authH := handler.NewAuth(authSvc, d.Config.Auth.DefaultTenantID, d.Pool)

	// 演示旁路：解析默认管理员身份。失败则启动失败，避免半开半关。
	var demoIdentity *auth.DemoIdentity
	if d.Config.Auth.DemoAuthBypass {
		id, err := auth.ResolveDemoIdentity(context.Background(), d.Pool)
		if err != nil {
			d.Logger.Error("DEMO_AUTH_BYPASS 已开启但无法解析默认用户", slog.String("error", err.Error()))
			panic(err)
		}
		demoIdentity = id
		d.Logger.Warn("DEMO_AUTH_BYPASS 已开启：未登录请求将使用默认管理员身份",
			slog.String("email", id.Email),
			slog.String("tenant_id", id.TenantID),
			slog.String("user_id", id.UserID),
		)
	}
	tpH := handler.NewTechnicalPlan(tpSvc, uploadSvc)
	tpH.SetQueries(store.New(d.Pool))
	uploadH := handler.NewUpload(uploadSvc, d.Config.HTTP.MaxUploadBytes)
	eventH := handler.NewEvent(d.Bus)
	healthH := handler.NewHealth(d.Pool, d.Bus, storageBackend, d.Version)
	rootH := handler.NewRoot(d.Version)
	publicH := handler.NewPublic(d.Pool, demoIdentity != nil)
	agentH := handler.NewAgent(agentSvc, usageRec, settingsSvc)
	bidH := handler.NewBidAnalysis(bidRunner, usageRec)
	assetH := handler.NewAsset(artifactSvc)

	// 知识库
	knowledgeSvc := knowledge.New(store.New(d.Pool), artifactSvc,
		filepath.Join(d.Config.Store.Root, "_knowledge"), d.Logger)
	knowledgeExtract := knowledge.NewExtractor(store.New(d.Pool), agentSvc, d.Logger)
	knowledgeH := handler.NewKnowledge(store.New(d.Pool), knowledgeSvc, knowledgeExtract, settingsSvc, uploadSvc)

	// 标书查重
	duplicateSvc := duplicate.New(store.New(d.Pool), artifactSvc,
		filepath.Join(d.Config.Store.Root, "_duplicate"), d.Logger)
	duplicateH := handler.NewDuplicate(store.New(d.Pool), duplicateSvc, uploadSvc)

	// 废标项检查
	rejectionSvc := rejection.New(store.New(d.Pool), agentSvc, d.Logger)
	rejectionH := handler.NewRejection(store.New(d.Pool), rejectionSvc, uploadSvc, settingsSvc)

	// 暗标检查
	blindWorkDir := filepath.Join(d.Config.Store.Root, "_blind")
	_ = os.MkdirAll(blindWorkDir, 0o755)
	blindSvc := blind.New(store.New(d.Pool), openxmlClient, blindWorkDir, d.Logger)
	blindH := handler.NewBlind(store.New(d.Pool), blindSvc, uploadSvc, blindWorkDir)

	// 导出模板
	templateH := handler.NewTemplate(store.New(d.Pool))

	// 可行性研究报告
	feasibilityH := handler.NewFeasibility(store.New(d.Pool), uploadSvc)

	// 资信库
	credentialH := handler.NewCredential(store.New(d.Pool))

	// AI 连通性测试（设置页「测试」按钮）
	aiH := handler.NewAI(settingsSvc)
	outlineH := handler.NewOutline(outlineRunner, tpSvc)
	taskH = handler.NewTask(tpSvc, outlineRunner, bidRunner, contentRunner, settingsSvc, artifactSvc, d.Bus)
	contentH := handler.NewContent(contentRunner, tpSvc, settingsSvc)

	consistencyH := handler.NewConsistency(consistencyRunner, tpSvc, settingsSvc)
	projectH := handler.NewProject(store.New(d.Pool))
	compatH := handler.NewCompat()
	taskRunH := handler.NewTaskRun(taskRunSvc)
	taskRunH.SetPlanService(tpSvc)

	var exportH *handler.Export
	if exportSvc != nil {
		exportH = handler.NewExport(exportSvc, tpSvc)
	}
	configH := handler.NewConfig(settingsSvc)

	// 工具回调：按 sessionId 反查上下文
	toolH := handler.NewAgentTool(toolReg, d.Config.Agent.InternalToken,
		func(c echo.Context, sessionID, taskID string) (agenttool.Invocation, error) {
			var sc *agent.SessionContext
			var ok bool
			if sessionID != "" {
				sc, ok = agentSvc.LookupSession(sessionID)
			}
			if !ok && taskID != "" {
				sc, ok = agentSvc.LookupTask(taskID)
			}
			if !ok {
				return agenttool.Invocation{}, fmt.Errorf("会话不存在或已结束")
			}
			return agenttool.Invocation{
				TenantID:     sc.TenantID,
				ProjectID:    sc.ProjectID,
				UserID:       sc.UserID,
				TaskID:       sc.TaskID,
				SessionID:    sc.SessionID,
				WorkspaceDir: sc.WorkspaceDir,
			}, nil
		}, &questionAnswerer{client: agentClient}, &questionLister{client: agentClient}, &eventPublisherAdapter{bus: d.Bus})

	// --- 公开路由 ---
	healthH.Register(e)
	authH.Register(e.Group("/api/auth"))
	publicH.Register(e.Group("/api"))

	// --- 需要登录的路由 ---
	api := e.Group("/api")
	api.Use(middleware.Auth(authMgr, demoIdentity))
	api.Use(middleware.ProjectScope())

	// 当前用户
	api.GET("/auth/me", authH.Me)
	api.POST("/auth/logout", authH.Logout)

	// ROOT 层
	rootH.Register(api)

	// SSE
	eventH.Register(api)

	// 技术方案
	tpH.Register(api)

	// Agent
	agentH.SetTools(declsToAny(toolReg.Declarations()))
	agentH.Register(api)

	// 招标文件分析（第一个标书垂类 Agent 功能）
	bidH.SetTools(declsToAny(toolReg.Declarations()))
	bidH.Register(api)

	// 目录生成（第二个标书垂类 Agent 功能）
	outlineH.SetTools(declsToAny(toolReg.Declarations()))
	outlineH.Register(api)

	// 前端 tasks.* 命名空间的桥接
	taskH.SetTools(declsToAny(toolReg.Declarations()))
	taskH.Register(api)

	// 配置（对应桌面版 user_config.json）
	configH.Register(api)

	// 文件上传（不需要项目作用域，但要登录）
	uploadH.Register(api)

	// 正文生成（第三个标书垂类 Agent 功能）
	contentH.SetTools(declsToAny(toolReg.Declarations()))
	contentH.SetTaskRunLister(taskRunSvc)

	// 知识库素材匹配：让正文生成能引用素材库里的真实内容
	matcher := knowledge.NewMatcher(store.New(d.Pool), d.Logger)
	contentH.SetKnowledge(matcher, d.Config.Knowledge.Enabled)
	contentH.Register(api)

	// 一致性检查
	consistencyH.Register(api)

	// 任务运行记录
	taskRunH.Register(api)

	// 项目管理（Web 版多项目必需）
	projectH.Register(api)

	// 桌面版专属接口的兼容实现（不能让前端拿 404）
	compatH.Register(api)

	// Word 导出
	if exportH != nil {
		exportH.Register(api)
	}

	// 资产（个人资产库）
	assetH.Register(api)
	knowledgeH.Register(api)
	duplicateH.Register(api)
	rejectionH.Register(api)
	blindH.Register(api)
	templateH.Register(api)
	feasibilityH.Register(api)
	credentialH.Register(api)
	aiH.Register(api)

	// 工具回调：独立分组，用内部令牌认证
	toolH.Register(e.Group("/api"))

	return e
}

// eventPublisherAdapter 把 events.Bus 适配成 agent.EventPublisher。
type eventPublisherAdapter struct {
	bus *events.Bus
}

func (a *eventPublisherAdapter) PushQuestion(scope handler.EventScopeIn, payload map[string]any) {
	if a.bus == nil {
		return
	}
	a.bus.Publish("agent.question", events.Scope{
		TenantID:  scope.TenantID,
		UserID:    scope.UserID,
		ProjectID: scope.ProjectID,
	}, payload)
}

func (a *eventPublisherAdapter) Publish(topic string, scope agent.EventScope, payload any) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(topic, events.Scope{
		TenantID:  scope.TenantID,
		UserID:    scope.UserID,
		ProjectID: scope.ProjectID,
	}, payload)
}

// questionAnswerer 把 agent.Client 适配成 handler.QuestionAnswerer。
type questionAnswerer struct {
	client *agent.Client
}

func (q *questionAnswerer) AnswerQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	return q.client.AnswerQuestion(ctx, sessionID, questionID, answer)
}

// questionLister 把 agent.Client 适配成 handler.QuestionLister。
type questionLister struct {
	client *agent.Client
}

func (q *questionLister) ListQuestions(ctx context.Context, sessionID string) ([]map[string]any, error) {
	return q.client.ListQuestions(ctx, sessionID)
}

// taskRunRecorderAdapter 把 taskrun.Service 适配成 agent.TaskRunRecorder。
//
// 为什么需要：agent 包定义了接口但用 agent.TaskRunCreate，
// taskrun 用自己的 CreateParams，类型不同。
type taskRunRecorderAdapter struct {
	svc *taskrun.Service
}

func (a *taskRunRecorderAdapter) Create(ctx context.Context, p agent.TaskRunCreate) error {
	return a.svc.Create(ctx, taskrun.CreateParams{
		TenantID:     p.TenantID,
		ProjectID:    p.ProjectID,
		TaskID:       p.TaskID,
		Kind:         p.Kind,
		SessionID:    p.SessionID,
		WorkspaceDir: p.WorkspaceDir,
		OwnerID:      p.OwnerID,
	})
}

func (a *taskRunRecorderAdapter) Finish(ctx context.Context, taskID string, success bool, errMsg string) {
	a.svc.Finish(ctx, taskID, success, errMsg)
}

func (a *taskRunRecorderAdapter) UpdateProgress(ctx context.Context, taskID string, progress map[string]any) {
	a.svc.UpdateProgress(ctx, taskID, progress)
}

// declsToAny 把工具声明转成 []any（sidecar 请求体要求）。
func declsToAny(decls []agenttool.Declaration) []any {
	out := make([]any, 0, len(decls))
	for _, d := range decls {
		out = append(out, d)
	}
	return out
}

// buildStorageBackend 按配置创建存储后端。
//
// LocalFS 适合开发与单机部署，S3 适合生产多实例。
// 切换只改环境变量，业务代码不变。
func buildStorageBackend(ctx context.Context, cfg config.StoreConfig, log *slog.Logger) (storage.Backend, error) {
	switch cfg.Backend {
	case "", "local":
		log.Info("使用本地文件存储", slog.String("root", cfg.Root))
		return storage.NewLocalFS(cfg.Root)

	case "s3":
		log.Info("使用 S3 对象存储",
			slog.String("endpoint", cfg.S3Endpoint),
			slog.String("bucket", cfg.S3Bucket),
		)
		return storage.NewS3(ctx, storage.S3Config{
			Endpoint:  cfg.S3Endpoint,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Bucket:    cfg.S3Bucket,
			Region:    cfg.S3Region,
			UseSSL:    cfg.S3UseSSL,
			PathStyle: cfg.S3PathStyle,
		})

	default:
		return nil, fmt.Errorf("未知的存储后端: %q（可选 local / s3）", cfg.Backend)
	}
}

// byteSizeString 把字节数转成 echo BodyLimit 需要的格式。
func byteSizeString(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(int64(n), 10) + "B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatInt(int64(n/div), 10) + string("KMGT"[exp]) + "B"
}
