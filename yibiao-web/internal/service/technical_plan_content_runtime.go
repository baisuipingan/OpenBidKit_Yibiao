package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/textutil"
	"github.com/patrick/yibiao-web/internal/store"
)

// PersistContentGenerationProgress 增量写入正文进度（小节状态 + 任务进度）。
//
// sections 为完整 map（nodeID → {id,title,status,content?}），覆盖写入。
func (s *TechnicalPlanService) PersistContentGenerationProgress(
	ctx context.Context,
	tenantID, projectID, taskID string,
	progress int,
	logLine string,
	sections map[string]any,
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

	logs := []string{}
	if prev, ok := extra["contentGenerationTask"].(map[string]any); ok {
		if arr, ok := prev["logs"].([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					logs = append(logs, s)
				}
			}
		}
		if arr, ok := prev["logs"].([]string); ok {
			logs = append(logs, arr...)
		}
	}
	if logLine != "" {
		logs = append(logs, logLine)
		if len(logs) > 40 {
			logs = logs[len(logs)-30:]
		}
	}
	if progress < 5 {
		progress = 5
	}
	if progress > 99 {
		progress = 99
	}
	task := map[string]any{
		"task_id":  taskID,
		"type":     "content-generation",
		"status":   "running",
		"progress": progress,
		"logs":     logs,
	}
	if prev, ok := extra["contentGenerationTask"].(map[string]any); ok {
		if started, ok := prev["started_at"]; ok {
			task["started_at"] = started
		}
	}
	if task["started_at"] == nil {
		task["started_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	extra["contentGenerationTask"] = task
	if sections != nil {
		extra["contentGenerationSections"] = sections
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// PersistContentGenerationSuccess 写入正文成功态与小节 success 映射。
func (s *TechnicalPlanService) PersistContentGenerationSuccess(
	ctx context.Context,
	tenantID, projectID, taskID string,
	sections map[string]any,
	written int,
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
	if sections == nil {
		built, n, buildErr := s.BuildContentSectionsFromNodes(ctx, tenantID, projectID)
		if buildErr == nil {
			sections = built
			if written <= 0 {
				written = n
			}
		}
	}
	msg := fmt.Sprintf("正文生成完成（%d 节）", written)
	extra["contentGenerationTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "content-generation",
		"status":       "success",
		"progress":     100,
		"message":      msg,
		"logs":         []string{msg},
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}
	if sections != nil {
		extra["contentGenerationSections"] = sections
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// PersistContentGenerationInterrupted 把僵尸 running 标成 interrupted/error，允许重新生成。
func (s *TechnicalPlanService) PersistContentGenerationInterrupted(
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
		message = "正文生成已中断，可重新生成"
	}
	extra["contentGenerationTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "content-generation",
		"status":       "interrupted",
		"progress":     0,
		"error":        message,
		"message":      message,
		"logs":         []string{message},
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}
	// 把仍标为 running 的空小节改成 pending，避免 UI 永久 0/N 且无法重试感知。
	if secs, ok := extra["contentGenerationSections"].(map[string]any); ok {
		for id, raw := range secs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			st, _ := m["status"].(string)
			content, _ := m["content"].(string)
			if st == "running" && strings.TrimSpace(content) == "" {
				m["status"] = "pending"
				secs[id] = m
			}
		}
		extra["contentGenerationSections"] = secs
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// PersistContentGenerationFailed 写入正文失败态。
func (s *TechnicalPlanService) PersistContentGenerationFailed(
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
		message = "正文生成失败"
	}
	extra["contentGenerationTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "content-generation",
		"status":       "error",
		"progress":     0,
		"error":        message,
		"message":      message,
		"logs":         []string{message},
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// BuildContentSectionsFromNodes 从目录节点正文构造 contentGenerationSections。
func (s *TechnicalPlanService) BuildContentSectionsFromNodes(
	ctx context.Context,
	tenantID, projectID string,
) (map[string]any, int, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, 0, err
	}
	nodes, err := s.q.ListOutlineNodes(ctx, store.ListOutlineNodesParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return nil, 0, err
	}
	hasChild := map[string]bool{}
	for _, n := range nodes {
		if n.ParentNodeID != nil {
			hasChild[n.ParentNodeID.String()] = true
		}
	}
	out := map[string]any{}
	written := 0
	for _, n := range nodes {
		id := n.NodeID.String()
		if hasChild[id] {
			continue
		}
		content := strings.TrimSpace(n.Content)
		status := "pending"
		if len([]rune(content)) > 50 {
			status = "success"
			written++
		}
		preview := content
		if len([]rune(preview)) > 4000 {
			preview = string([]rune(preview)[:4000])
		}
		entry := map[string]any{
			"id":      id,
			"title":   n.Title,
			"status":  status,
			"content": preview,
		}
		if words := textutil.CountReadableWords(content); words > 0 {
			entry["words"] = words
		}
		out[id] = entry
	}
	return out, written, nil
}

// ContentTaskSnapshot 是 meta 里正文任务的精简视图。
type ContentTaskSnapshot struct {
	TaskID string
	Status string
}

// LoadContentTaskSnapshot 读取 contentGenerationTask。
func (s *TechnicalPlanService) LoadContentTaskSnapshot(
	ctx context.Context,
	tenantID, projectID string,
) (*ContentTaskSnapshot, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	raw, err := s.q.GetTechnicalPlanExtra(ctx, store.GetTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid,
	})
	if err != nil || len(raw) == 0 {
		return nil, nil
	}
	extra := map[string]any{}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return nil, nil
	}
	task, _ := extra["contentGenerationTask"].(map[string]any)
	if task == nil {
		return nil, nil
	}
	id, _ := task["task_id"].(string)
	if id == "" {
		id, _ = task["taskId"].(string)
	}
	status, _ := task["status"].(string)
	if id == "" {
		return nil, nil
	}
	return &ContentTaskSnapshot{TaskID: id, Status: status}, nil
}

// ListProjectsWithRunningContentTask 列出 meta 中正文仍标为 running 的项目。
func (s *TechnicalPlanService) ListProjectsWithRunningContentTask(ctx context.Context) ([]struct {
	TenantID  string
	ProjectID string
	TaskID    string
}, error) {
	rows, err := s.pool.Query(ctx, `
SELECT tenant_id::text, project_id::text,
       COALESCE(extra->'contentGenerationTask'->>'task_id', extra->'contentGenerationTask'->>'taskId', '') AS task_id
  FROM technical_plan_meta
 WHERE extra->'contentGenerationTask'->>'status' IN ('running', 'pausing')
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]struct {
		TenantID  string
		ProjectID string
		TaskID    string
	}, 0)
	for rows.Next() {
		var tenantID, projectID, taskID string
		if err := rows.Scan(&tenantID, &projectID, &taskID); err != nil {
			continue
		}
		out = append(out, struct {
			TenantID  string
			ProjectID string
			TaskID    string
		}{TenantID: tenantID, ProjectID: projectID, TaskID: taskID})
	}
	return out, rows.Err()
}
