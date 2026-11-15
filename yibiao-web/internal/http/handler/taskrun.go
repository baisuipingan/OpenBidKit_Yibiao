package handler

import (
	"context"
	"sort"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/service"
	"github.com/patrick/yibiao-web/internal/service/agent/content"
	"github.com/patrick/yibiao-web/internal/service/taskrun"
)

// TaskRun 处理任务运行记录查询。
type TaskRun struct {
	svc *taskrun.Service
	// planSvc 用于统计目录里的 AI 小节数（算「还剩多少要生成」）
	planSvc *service.TechnicalPlanService
}

// SetPlanService 注入技术方案服务。
func (h *TaskRun) SetPlanService(svc *service.TechnicalPlanService) {
	h.planSvc = svc
}

// NewTaskRun 创建 handler。
func NewTaskRun(svc *taskrun.Service) *TaskRun {
	return &TaskRun{svc: svc}
}

// Register 注册路由。
func (h *TaskRun) Register(g *echo.Group) {
	g.POST("/task-runs/running", h.ListRunning)
	g.POST("/task-runs/list", h.ListByProject)
	g.POST("/task-runs/get", h.Get)
	// 可恢复的正文生成任务列表
	g.POST("/task-runs/resumable", h.ListResumable)
	// 前端通过 tasks 命名空间调用
	g.POST("/tasks/list-resumable", h.ListResumable)
}

// ListRunning 列出运行中的任务。
//
// 用途：前端页面挂载时检查是否有任务在跑；
// 运维排查「为什么磁盘一直不释放」。
func (h *TaskRun) ListRunning(c echo.Context) error {
	items, err := h.svc.ListRunning(c.Request().Context())
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// listRequest 是列出任务历史的请求。
type taskRunListRequest struct {
	Limit int32 `json:"limit"`
}

// ListByProject 列出项目的任务历史。
func (h *TaskRun) ListByProject(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	var req taskRunListRequest
	_ = c.Bind(&req)
	if req.Limit <= 0 {
		req.Limit = 20
	}

	items, err := h.svc.ListByProject(c.Request().Context(), tenantID, projectID, req.Limit)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// getRequest 是查单个任务的请求。
type taskRunGetRequest struct {
	TaskID string `json:"taskId"`
}

// Get 查单个任务。
func (h *TaskRun) Get(c echo.Context) error {
	var req taskRunGetRequest
	if err := c.Bind(&req); err != nil {
		return httpx.BadRequest("请求体格式错误")
	}
	if req.TaskID == "" {
		return httpx.BadRequest("缺少 taskId")
	}

	v, err := h.svc.Get(c.Request().Context(), req.TaskID)
	if err != nil {
		return httpx.NotFound("任务不存在")
	}
	return httpx.OK(c, v)
}

// resumableTask 是一个可恢复的任务。
type resumableTask struct {
	TaskID    string `json:"taskId"`
	ProjectID string `json:"projectId"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	// 已完成的小节数
	CompletedCount int `json:"completedCount"`
	// 剩余小节数
	PendingCount int `json:"pendingCount"`
	// 已完成字数
	TotalWords int `json:"totalWords"`
	// 上次活动时间
	UpdatedAt string `json:"updatedAt"`
	// 说明（给用户看为什么能恢复）
	Hint string `json:"hint"`
}

// ListResumable 列出可以恢复的正文生成任务。
//
// 判定条件：
//   - kind = content-generation
//   - status 不是成功
//   - 工作区还在，且 manifest 里至少有一节完成（且文件真的存在）
//
// 用途：服务重启后，前端展示「上次有 N 个任务被打断，可以接着做」。
func (h *TaskRun) ListResumable(c echo.Context) error {
	tenantID, _ := identity(c)
	projectID, err := projectID(c)
	if err != nil {
		return err
	}

	runs, err := h.svc.ListByProject(c.Request().Context(), tenantID, projectID, 50)
	if err != nil {
		return httpx.Internal(err)
	}

	// 只需要扫描能力，不需要完整 Runner
	scanner := content.NewRunner(nil, nil, nil)
	out := make([]resumableTask, 0)

	for _, run := range runs {
		if run.Kind != "content-generation" {
			continue
		}
		if run.Status == "success" || run.Status == "completed" {
			continue
		}
		if run.WorkspaceDir == "" {
			continue
		}

		scan, scanErr := scanner.ScanResumeState(run.WorkspaceDir)
		if scanErr != nil || len(scan.Completed) == 0 {
			continue
		}

		// 关键：剩余数量要按**当前目录**算，不能只看 manifest。
		//
		// 为什么：旧任务的 manifest 只包含它当时的待办小节；
		// 如果目录后来扩了（比如从 1 节变 64 节），
		// manifest 里的 pending 会是 0，但实际还有大量章节没做。
		//
		// 这一点如果不处理，用户会看到「剩余 0 节」而以为不用恢复。
		currentPending := scan.Pending

		// 用当前目录的 AI 叶子数校正：剩余 = 目录里未完成的小节数
		if leaves := h.countAILeaves(c.Request().Context(), tenantID, projectID); leaves > 0 {
			remain := leaves - len(scan.Completed)
			if remain < 0 {
				remain = 0
			}
			currentPending = remain
		}

		hint := "可以继续"
		switch run.Status {
		case "interrupted":
			hint = "任务被中断，可以接着做"
		case "running":
			hint = "任务可能已卡住，可重新拉起"
		case "error":
			hint = "任务失败，可从中断处继续"
		}

		out = append(out, resumableTask{
			TaskID:         run.TaskID,
			ProjectID:      projectID,
			Kind:           run.Kind,
			Status:         run.Status,
			CompletedCount: len(scan.Completed),
			PendingCount:   currentPending,
			TotalWords:     scan.TotalWords,
			UpdatedAt:      run.HeartbeatAt.Format(time.RFC3339),
			Hint:           hint,
		})
	}

	// 按最近活动时间倒序（最新的在前，去重时保留它）
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})

	// 去重：同一个项目只保留最新的一条。
	//
	// 为什么：任务失败后用户通常重试多次，会产生多个中断记录，
	// 但它们指向的是同一批未完成的小节。全列出来用户不知道该点哪个。
	// 保留最近的就够用了（恢复时也是复用那个工作区）。
	if len(out) > 1 {
		out = out[:1]
	}

	return httpx.OK(c, map[string]any{
		"items": out,
		"count": len(out),
	})
}

// countAILeaves 统计目录里 content_mode=ai-generate 的叶子节点数。
//
// 就是「正文生成要处理的小节」数量。
func (h *TaskRun) countAILeaves(ctx context.Context, tenantID, projectID string) int {
	if h.planSvc == nil {
		return 0
	}

	state, err := h.planSvc.LoadState(ctx, tenantID, projectID)
	if err != nil || state.OutlineData == nil {
		return 0
	}

	count := 0
	var walk func(items []service.OutlineItem)
	walk = func(items []service.OutlineItem) {
		for _, item := range items {
			if len(item.Children) > 0 {
				walk(item.Children)
				continue
			}
			// 叶子节点且标记为 AI 生成
			if item.ContentMode == "ai-generate" || item.ContentMode == "" {
				count++
			}
		}
	}
	walk(state.OutlineData.Outline)

	return count
}
