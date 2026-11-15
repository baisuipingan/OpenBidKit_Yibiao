package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/patrick/yibiao-web/internal/service/agent/globalfacts"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/store"
)

// PersistGlobalFactsGenerationStart 写入 running 态。
func (s *TechnicalPlanService) PersistGlobalFactsGenerationStart(
	ctx context.Context,
	tenantID, projectID, taskID, mode string,
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
	extra["globalFactsTask"] = map[string]any{
		"task_id":    taskID,
		"type":       globalfacts.TaskType,
		"status":     "running",
		"progress":   5,
		"logs":       []string{"全局事实设定任务已启动"},
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"message":    "全局事实生成进行中",
		"mode":       globalfacts.NormalizeMode(mode),
	}
	// 启动时清空旧事实，避免界面显示陈旧大项却任务仍在跑。
	extra["globalFacts"] = []any{}
	raw, _ := json.Marshal(extra)
	return s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	})
}

// MarkGlobalFactsFailed 写入 error 态。
func (s *TechnicalPlanService) MarkGlobalFactsFailed(
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
		message = "全局事实生成失败"
	}
	extra["globalFactsTask"] = map[string]any{
		"task_id":      taskID,
		"type":         globalfacts.TaskType,
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

// PersistGlobalFactsGenerationSuccess 写入 success + groups。
func (s *TechnicalPlanService) PersistGlobalFactsGenerationSuccess(
	ctx context.Context,
	tenantID, projectID, taskID string,
	groups []any,
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
	msg := fmt.Sprintf("全局事实变量生成完成（%d 个大项）", len(groups))
	extra["globalFacts"] = groups
	extra["globalFactsTask"] = map[string]any{
		"task_id":      taskID,
		"type":         globalfacts.TaskType,
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

// SyncGlobalFactsFromArtifacts 把 global-facts.json 回写进 meta.extra。
func (s *TechnicalPlanService) SyncGlobalFactsFromArtifacts(
	ctx context.Context,
	tenantID, projectID, taskID string,
	artifacts *artifact.Service,
	log *slog.Logger,
) (int, error) {
	if artifacts == nil {
		return 0, fmt.Errorf("缺少资产服务")
	}
	assets, err := artifacts.ListByTask(ctx, tenantID, taskID)
	if err != nil {
		return 0, fmt.Errorf("读取全局事实产物失败: %w", err)
	}
	var rawJSON string
	for _, a := range assets {
		if a.Name == globalfacts.OutputFile {
			content, readErr := readAssetContent(ctx, artifacts, tenantID, a.ID)
			if readErr != nil {
				return 0, fmt.Errorf("读取全局事实内容失败: %w", readErr)
			}
			rawJSON = content
			break
		}
	}
	if rawJSON == "" {
		return 0, fmt.Errorf("未找到 %s", globalfacts.OutputFile)
	}
	groups, err := globalfacts.ParseJSON([]byte(rawJSON))
	if err != nil {
		return 0, err
	}
	asAny := globalfacts.GroupsToAny(groups)
	if err := s.PersistGlobalFactsGenerationSuccess(ctx, tenantID, projectID, taskID, asAny); err != nil {
		return 0, err
	}
	if log != nil {
		log.Info("全局事实已写进 meta",
			slog.String("task_id", taskID),
			slog.Int("groups", len(groups)),
		)
	}
	return len(groups), nil
}

// BuildGlobalFactsPrepareContent 从当前 PlanState 拼工作区辅助文件。
func BuildGlobalFactsPrepareContent(st *PlanState) map[string][]byte {
	overview := "未提供项目概述。"
	if st != nil && strings.TrimSpace(st.ProjectOverview) != "" {
		overview = strings.TrimSpace(st.ProjectOverview)
	}
	bidFacts := formatBidAnalysisFactsForGlobalFacts(st)
	outlineMD := formatOutlineForGlobalFacts(st)
	catalog := globalfacts.DefaultFileCatalog()
	return map[string][]byte{
		"项目概述.md":    []byte(overview),
		"招标解析结果.md": []byte(bidFacts),
		"技术方案目录.md": []byte(outlineMD),
		"材料说明.md":    []byte("本次任务实际提供的材料如下。只使用这些文件，不要猜测未提供的材料。\n\n" + catalog),
	}
}

func formatBidAnalysisFactsForGlobalFacts(st *PlanState) string {
	if st == nil {
		return "未提供招标文件关键解析结果。"
	}
	keys := []struct{ id, label string }{
		{"projectInfo", "项目信息"},
		{"partAInfo", "甲方信息"},
		{"deliveryAndServiceRequirements", "交货和服务要求"},
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		content := ""
		if st.BidAnalysisTasks != nil {
			if item, ok := st.BidAnalysisTasks[k.id].(map[string]any); ok {
				if c, _ := item["content"].(string); strings.TrimSpace(c) != "" {
					content = strings.TrimSpace(c)
				}
			}
		}
		if content == "" {
			continue
		}
		parts = append(parts, "## "+k.label+"\n\n"+content)
	}
	if len(parts) == 0 {
		return "未提供招标文件关键解析结果。"
	}
	return strings.Join(parts, "\n\n")
}

func formatOutlineForGlobalFacts(st *PlanState) string {
	if st == nil || st.OutlineData == nil || len(st.OutlineData.Outline) == 0 {
		return "未提供技术方案目录。"
	}
	var lines []string
	var walk func(items []OutlineItem, level int)
	walk = func(items []OutlineItem, level int) {
		for _, item := range items {
			pad := strings.Repeat("  ", level)
			num := strings.TrimSpace(item.Number)
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = "未命名章节"
			}
			desc := strings.TrimSpace(item.Description)
			line := fmt.Sprintf("%s- %s %s", pad, num, title)
			if desc != "" {
				line += "：" + desc
			}
			lines = append(lines, line)
			if len(item.Children) > 0 {
				walk(item.Children, level+1)
			}
		}
	}
	walk(st.OutlineData.Outline, 0)
	if len(lines) == 0 {
		return "未提供技术方案目录。"
	}
	return strings.Join(lines, "\n")
}
