package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/patrick/yibiao-web/internal/store"
)

// PersistOutlineGenerationStart 写入 running 态，供 get-active / load-state / SSE 使用。
func (s *TechnicalPlanService) PersistOutlineGenerationStart(
	ctx context.Context,
	tenantID, projectID, taskID string,
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
	extra["outlineGenerationTask"] = map[string]any{
		"task_id":    taskID,
		"type":       "outline-generation",
		"status":     "running",
		"progress":   5,
		"logs":       []string{"目录生成任务已启动"},
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"message":    "目录生成进行中",
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// MarkOutlineFailed 在空产物或失败时写入 error，避免界面显示 100% 且 0 章。
func (s *TechnicalPlanService) MarkOutlineFailed(
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
		message = "目录生成失败"
	}
	extra["outlineGenerationTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "outline-generation",
		"status":       "error",
		"progress":     0,
		"error":        message,
		"message":      message,
		"logs":         []string{message},
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(extra)
	if err := s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	}); err != nil {
		return fmt.Errorf("写入目录失败状态失败: %w", err)
	}
	return nil
}

// PersistOutlineGenerationSuccess 在节点落库后写入 success 任务态。
func (s *TechnicalPlanService) PersistOutlineGenerationSuccess(
	ctx context.Context,
	tenantID, projectID, taskID string,
	nodeCount int,
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
	msg := fmt.Sprintf("目录生成完成（%d 个节点）", nodeCount)
	extra["outlineGenerationTask"] = map[string]any{
		"task_id":      taskID,
		"type":         "outline-generation",
		"status":       "success",
		"progress":     100,
		"message":      msg,
		"logs":         []string{msg},
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// LoadOutlineRuntime 读取目录任务态，供最终 SSE 使用。
func (s *TechnicalPlanService) LoadOutlineRuntime(ctx context.Context, tenantID, projectID string) (map[string]any, error) {
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

// PersistOutlineWordControlSnapshot 把当前生成配置的字数选项写入 meta 快照列。
//
// 桌面版在目录生成完成时固化这份配置；前端用它判断「下一步」是否可点，
// 以及字数设置是否相对目录已过期。
func (s *TechnicalPlanService) PersistOutlineWordControlSnapshot(
	ctx context.Context,
	tenantID, projectID string,
) (map[string]any, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	if _, err := s.q.EnsureGenerationConfig(ctx, store.EnsureGenerationConfigParams{
		ProjectID: pid, TenantID: tid,
	}); err != nil {
		return nil, fmt.Errorf("初始化生成配置失败: %w", err)
	}
	cfg, err := s.q.GetGenerationConfig(ctx, store.GetGenerationConfigParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return nil, fmt.Errorf("读取生成配置失败: %w", err)
	}
	snapshot := map[string]any{
		"minimumWords": cfg.MinimumWords,
		"maximumWords": cfg.MaximumWords,
		"sectionWords": cfg.SectionWords,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `
UPDATE technical_plan_meta
   SET outline_word_control_snapshot = $3::jsonb, updated_at = now()
 WHERE project_id = $1 AND tenant_id = $2`,
		pid, tid, raw,
	)
	if err != nil {
		return nil, fmt.Errorf("保存字数控制快照失败: %w", err)
	}
	return snapshot, nil
}

// PersistContentGenerationStart 写入正文生成 running 态。
func (s *TechnicalPlanService) PersistContentGenerationStart(
	ctx context.Context,
	tenantID, projectID, taskID string,
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
	extra["contentGenerationTask"] = map[string]any{
		"task_id":    taskID,
		"type":       "content-generation",
		"status":     "running",
		"progress":   5,
		"logs":       []string{"正文生成任务已启动"},
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}
