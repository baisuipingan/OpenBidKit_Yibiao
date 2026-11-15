package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/patrick/yibiao-web/internal/store"
)

// PersistBidAnalysisStart 在任务启动时写入 running 状态，
// 这样即使 SSE 丢了，reload load-state 也能看到「解析中」而不是永远「待解析」。
func (s *TechnicalPlanService) PersistBidAnalysisStart(
	ctx context.Context,
	tenantID, projectID, taskID string,
	mode string,
	selectedIDs []string,
	items []map[string]any,
) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}
	meta, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return err
	}
	extra := map[string]any{}
	if len(meta.Extra) > 0 {
		_ = json.Unmarshal(meta.Extra, &extra)
	}
	if extra == nil {
		extra = map[string]any{}
	}

	tasks := map[string]any{}
	for _, item := range items {
		id, _ := item["id"].(string)
		if id == "" {
			continue
		}
		tasks[id] = map[string]any{
			"id":      id,
			"label":   item["label"],
			"status":  "pending",
			"content": "",
		}
	}
	extra["bidAnalysisTasks"] = tasks
	extra["bidAnalysisSelectedTaskIds"] = selectedIDs
	extra["bidAnalysisProgress"] = 0
	extra["bidAnalysisTask"] = map[string]any{
		"task_id":    taskID,
		"type":       "bid-analysis",
		"status":     "running",
		"progress":   0,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"message":    "招标文件解析进行中",
	}
	if mode != "" {
		extra["bidAnalysisMode"] = mode
	}

	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// MarkBidAnalysisFailed 在 Agent 失败且无产物时，把 pending/running 项标为 error，
// 避免界面一直显示「待解析」且下一步永远灰掉。
func (s *TechnicalPlanService) MarkBidAnalysisFailed(
	ctx context.Context,
	tenantID, projectID, taskID, message string,
) error {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return err
	}
	meta, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return err
	}
	extra := map[string]any{}
	if len(meta.Extra) > 0 {
		_ = json.Unmarshal(meta.Extra, &extra)
	}
	if extra == nil {
		extra = map[string]any{}
	}

	if message == "" {
		message = "招标文件解析失败"
	}

	tasks, _ := extra["bidAnalysisTasks"].(map[string]any)
	if tasks == nil {
		tasks = map[string]any{}
	}
	for id, raw := range tasks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		status, _ := m["status"].(string)
		if status == "success" {
			continue
		}
		m["status"] = "error"
		m["error"] = message
		tasks[id] = m
	}
	extra["bidAnalysisTasks"] = tasks
	extra["bidAnalysisTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "bid-analysis",
		"status":       "error",
		"progress":     0,
		"error":        message,
		"message":      message,
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}

	raw, _ := json.Marshal(extra)
	if err := s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	}); err != nil {
		return fmt.Errorf("写入失败状态失败: %w", err)
	}
	return nil
}

// LoadBidAnalysisRuntime 从 extra 取出任务态，供 SSE 最终事件和测试使用。
func (s *TechnicalPlanService) LoadBidAnalysisRuntime(ctx context.Context, tenantID, projectID string) (map[string]any, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	raw, err := s.q.GetTechnicalPlanExtra(ctx, store.GetTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid,
	})
	if err != nil {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}
