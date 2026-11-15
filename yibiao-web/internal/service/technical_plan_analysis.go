package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/google/uuid"

	"github.com/patrick/yibiao-web/internal/service/agent/outline"
	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/store"
)

// 本文件实现「分析产物 → meta 状态」的回写。
//
// 背景：
//
//	bidanalysis runner 把产物提交进 artifact 资产库
//	（items/*.md、items/*.json、summary.json），
//	但 technical_plan_meta 里的 bid_analysis_tasks 一直是空的，
//	前端读不到任何分析结果，页面上永远显示"未分析"。
//
// 职责：
//
//	任务完成后把资产库里的产物读出来，映射成前端认识的
//	bidAnalysisTasks 结构，写进 meta.extra。这样刷新页面也能看到。

// analysisItemLabel 是各分析项的显示名。
//
// 前端按 id 取值，label 只用于展示。
// 键名必须与 agent 实际产出的文件名一致（已核对）。
var analysisItemLabel = map[string]string{
	"projectInfo":                    "项目信息",
	"partAInfo":                      "甲方信息",
	"agentInfo":                      "代理机构信息",
	"keyInfo":                        "关键信息",
	"marginInfo":                     "保证金信息",
	"projectBackground":              "项目背景",
	"scopeOfWork":                    "工作范围",
	"technicalRequirements":          "技术需求",
	"qualificationRequirements":      "资质要求",
	"qualificationReview":            "资格性审查",
	"complianceCheck":                "符合性检查",
	"contractTerms":                  "合同条款",
	"scoringCriteria":                "评分标准",
	"invalidBidAndRejectionItems":    "无效投标与废标项",
	"responseFileRequirements":       "响应文件编制要求",
	"deliveryAndServiceRequirements": "交付与服务要求",
}

// 前端期望的 ID 与 agent 实际产出的文件名不是一一对应，
// 这里有别名关系的做显式映射（左=前端 ID，右=agent 产出名）。
//
// 不映射的话，前端 BidAnalysisPage 会认为这些项「待解析」，
// 明明数据已经在库里了，界面却显示没解析。
var analysisAlias = map[string]string{
	"projectOverview":  "projectBackground",           // 项目概述 ← 项目背景
	"techRequirements": "technicalRequirements",       // 技术评分要求 ← 技术需求
	"businessScoring":  "scoringCriteria",             // 商务评分要求 ← 评分标准
	"discardedBids":    "invalidBidAndRejectionItems", // 废标项 ← 无效投标与废标项
}

// 顶层冗余字段：把某几项单独提一份到 meta 顶层，
// 方便不遍历 bidAnalysisTasks 的地方直接读。
// key = meta 顶层字段名，value = agent 产出文件名
var topLevelFieldSource = map[string]string{
	"projectOverview":  "projectBackground",
	"techRequirements": "technicalRequirements",
	"projectInfo":      "projectInfo",
}

// AnalysisItemResult 是一个分析项的结果。
type AnalysisItemResult struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// SyncBidAnalysisFromArtifacts 把任务产物回写进 meta。
//
// 调用时机：分析任务完成（成功或失败）后。
// 失败任务也回写 —— 部分产物仍有价值，而且前端能看到哪个环节出错。
func (s *TechnicalPlanService) SyncBidAnalysisFromArtifacts(
	ctx context.Context,
	tenantID, projectID, taskID string,
	artifacts *artifact.Service,
	log *slog.Logger,
) (int, error) {
	if artifacts == nil {
		return 0, fmt.Errorf("缺少资产服务")
	}

	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return 0, err
	}

	assets, err := artifacts.ListByTask(ctx, tenantID, taskID)
	if err != nil {
		return 0, fmt.Errorf("读取任务产物失败: %w", err)
	}

	// 只处理本次任务的产物
	items := make(map[string]*AnalysisItemResult, len(assets))
	var summaryContent string

	for _, a := range assets {
		name := a.Name
		if !strings.HasPrefix(name, "items/") && name != "summary.json" {
			continue
		}

		content, readErr := readAssetContent(ctx, artifacts, tenantID, a.ID)
		if readErr != nil {
			log.Warn("读取产物失败", slog.String("name", name), slog.String("error", readErr.Error()))
			continue
		}

		if name == "summary.json" {
			summaryContent = content
			continue
		}

		// items/xxx.md 或 items/xxx.json → id = xxx
		base := strings.TrimPrefix(name, "items/")
		id := strings.TrimSuffix(base, path.Ext(base))
		if id == "" {
			continue
		}

		label := analysisItemLabel[id]
		if label == "" {
			label = id
		}

		items[id] = &AnalysisItemResult{
			ID:      id,
			Label:   label,
			Status:  "success",
			Content: content,
		}
	}

	if len(items) == 0 {
		return 0, nil
	}

	// 写进 meta.extra
	meta, err := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return 0, fmt.Errorf("读取技术方案状态失败: %w", err)
	}

	extra := map[string]any{}
	if len(meta.Extra) > 0 {
		_ = json.Unmarshal(meta.Extra, &extra)
	}
	if extra == nil {
		extra = map[string]any{}
	}

	// 保留已有条目，只用新结果覆盖同 id 的（支持增量重跑）
	existing, _ := extra["bidAnalysisTasks"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for id, item := range items {
		existing[id] = item
	}

	// 按前端期望的 ID 补一份别名，避免界面显示「待解析」
	for frontID, agentID := range analysisAlias {
		if src, ok := items[agentID]; ok {
			existing[frontID] = &AnalysisItemResult{
				ID:      frontID,
				Label:   analysisItemLabel[agentID],
				Status:  src.Status,
				Content: src.Content,
			}
		}
	}
	extra["bidAnalysisTasks"] = existing
	extra["bidAnalysisProgress"] = 100

	// 顶层冗余字段：value 是 agent 产出的文件名，直接在 items 里找
	for dst, src := range topLevelFieldSource {
		if m, ok := items[src]; ok && m.Content != "" {
			extra[dst] = m.Content
		}
	}

	if summaryContent != "" {
		extra["bidAnalysisSummary"] = summaryContent
	}

	raw, _ := json.Marshal(extra)
	if err := s.q.UpdateTechnicalPlanExtra(ctx, store.UpdateTechnicalPlanExtraParams{
		TenantID: tid, ProjectID: pid, Extra: raw,
	}); err != nil {
		return 0, fmt.Errorf("保存分析结果失败: %w", err)
	}

	log.Info("分析结果已回写",
		slog.String("task_id", taskID),
		slog.Int("items", len(items)),
	)

	return len(items), nil
}

// readAssetContent 读取资产内容为字符串。
func readAssetContent(ctx context.Context, artifacts *artifact.Service, tenantID, assetID string) (string, error) {
	rc, _, err := artifacts.GetByID(ctx, tenantID, assetID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SyncOutlineFromArtifacts 把目录产物（outline.json）写进节点表。
//
// 和 bid-analysis 同样的坑：runner 只把产物提交进资产库，
// technical_plan_outline_nodes 一直是空的，前端目录页拿不到东西。
func (s *TechnicalPlanService) SyncOutlineFromArtifacts(
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
		return 0, fmt.Errorf("读取目录产物失败: %w", err)
	}

	var outlineJSON string
	for _, a := range assets {
		if a.Name == "outline.json" {
			content, readErr := readAssetContent(ctx, artifacts, tenantID, a.ID)
			if readErr != nil {
				return 0, fmt.Errorf("读取目录内容失败: %w", readErr)
			}
			outlineJSON = content
			break
		}
	}

	if outlineJSON == "" {
		return 0, fmt.Errorf("未找到 outline.json")
	}

	// 结构：{"outline": [{title, description, content_mode, children}]}
	var payload struct {
		Outline []map[string]any `json:"outline"`
	}
	if err := json.Unmarshal([]byte(outlineJSON), &payload); err != nil {
		return 0, fmt.Errorf("目录格式异常: %w", err)
	}
	if len(payload.Outline) == 0 {
		return 0, fmt.Errorf("目录为空")
	}

	// 转成 outline.Node 树
	items := make([]any, 0, len(payload.Outline))
	for _, m := range payload.Outline {
		items = append(items, m)
	}
	nodes := buildOutlineNodesFromMap(items)
	if len(nodes) == 0 {
		return 0, fmt.Errorf("目录节点解析失败")
	}

	// 项目名从 agent 分析结果里取（如果有）
	projectName := ""
	if meta, mErr := s.q.EnsureTechnicalPlanMeta(ctx, store.EnsureTechnicalPlanMetaParams{}); mErr == nil && len(meta.Extra) > 0 {
		var extra map[string]any
		if json.Unmarshal(meta.Extra, &extra) == nil {
			if v, ok := extra["outlineProjectName"].(string); ok {
				projectName = v
			}
		}
	}

	if err := s.SaveOutline(ctx, tenantID, projectID, nodes, projectName, ""); err != nil {
		return 0, err
	}

	total := countOutlineNodes(nodes)
	log.Info("目录已写进节点表",
		slog.String("task_id", taskID),
		slog.Int("nodes", total),
	)

	return total, nil
}

// buildOutlineNodesFromMap 把 map 形式的目录树转成 outline.Node。
func buildOutlineNodesFromMap(items []any) []*outline.Node {
	out := make([]*outline.Node, 0, len(items))

	for idx, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := m["title"].(string)
		if title == "" {
			continue
		}
		desc, _ := m["description"].(string)
		mode, _ := m["content_mode"].(string)
		if mode == "" {
			mode, _ = m["contentMode"].(string)
		}

		node := &outline.Node{
			ID:          fmt.Sprintf("n%d", idx+1),
			Title:       title,
			Description: desc,
			ContentMode: mode,
		}

		if children, ok := m["children"].([]any); ok && len(children) > 0 {
			node.Children = buildOutlineNodesFromMap(children)
			for i, ch := range node.Children {
				ch.ID = fmt.Sprintf("%s-%d", node.ID, i+1)
			}
		}
		out = append(out, node)
	}

	return out
}

// countOutlineNodes 递归数节点。
func countOutlineNodes(nodes []*outline.Node) int {
	n := 0
	for _, node := range nodes {
		n++
		n += countOutlineNodes(node.Children)
	}
	return n
}

// SyncContentFromArtifacts 把正文产物写进节点表。
//
// 产物结构：sections/*.md + manifest.json
// manifest 里记录了 nodeId 与文件名、状态的对应关系。
func (s *TechnicalPlanService) SyncContentFromArtifacts(
	ctx context.Context,
	tenantID, projectID, taskID string,
	artifacts *artifact.Service,
	log *slog.Logger,
) (int, error) {
	if artifacts == nil {
		return 0, fmt.Errorf("缺少资产服务")
	}

	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return 0, err
	}

	assets, err := artifacts.ListByTask(ctx, tenantID, taskID)
	if err != nil {
		return 0, fmt.Errorf("读取正文产物失败: %w", err)
	}

	// 按文件名索引内容
	byName := map[string]string{}
	var manifestRaw string
	for _, a := range assets {
		content, readErr := readAssetContent(ctx, artifacts, tenantID, a.ID)
		if readErr != nil {
			continue
		}
		if a.Name == "content-manifest.json" || a.Name == "manifest.json" {
			manifestRaw = content
			continue
		}
		byName[a.Name] = content
	}

	if manifestRaw == "" {
		return 0, fmt.Errorf("未找到 content-manifest.json")
	}

	// manifest: {"taskId":"", "generated":[{nodeId, title, file, words, status}]}
	//
	// 注意 nodeId 可能是空串（agent 不保证回填），
	// 所以定位节点要以 title 为主、nodeId 为辅。
	var manifest struct {
		Generated []struct {
			NodeID string `json:"nodeId"`
			Title  string `json:"title"`
			File   string `json:"file"`
			Status string `json:"status"`
		} `json:"generated"`
		Sections []struct {
			NodeID string `json:"nodeId"`
			Title  string `json:"title"`
			File   string `json:"file"`
			Status string `json:"status"`
		} `json:"sections"` // 兼容旧格式
	}
	if err := json.Unmarshal([]byte(manifestRaw), &manifest); err != nil {
		return 0, fmt.Errorf("manifest 格式异常: %w", err)
	}

	entries := manifest.Generated
	if len(entries) == 0 {
		entries = manifest.Sections
	}

	written := 0
	for _, sec := range entries {
		if sec.File == "" {
			continue
		}

		// manifest 里 file 可能已带 sections/ 前缀，
		// 先按原名查，再补前缀（不能无脑拼，否则变成 sections/sections/xxx.md）
		content := byName[sec.File]
		if content == "" {
			content = byName["sections/"+sec.File]
		}
		if content == "" {
			// 按文件名兜底匹配（防止前缀不一致）
			base := path.Base(sec.File)
			for name, c := range byName {
				if path.Base(name) == base {
					content = c
					break
				}
			}
		}
		if content == "" {
			continue
		}

		// 优先用 nodeId；为空时按标题匹配节点
		updated := false
		if sec.NodeID != "" {
			if updErr := s.UpdateSectionContent(ctx, tid, pid, sec.NodeID, content); updErr == nil {
				updated = true
			}
		}
		if !updated && sec.Title != "" {
			if updErr := s.UpdateSectionContentByTitle(ctx, tid, pid, sec.Title, content); updErr == nil {
				updated = true
			} else {
				log.Warn("按标题写入失败",
					slog.String("title", sec.Title),
					slog.String("error", updErr.Error()))
			}
		}
		if updated {
			written++
		}
	}

	log.Info("正文已写进节点表",
		slog.String("task_id", taskID),
		slog.Int("written", written),
	)

	return written, nil
}

// UpdateSectionContentByTitle 按章节标题定位节点并写入正文。
//
// 为什么需要：agent 产出的 manifest 里 nodeId 常常是空串，
// 只有 title 可靠。标题带编号（如「1.1 编制依据与编制原则」），
// 在节点表里能唯一定位。
func (s *TechnicalPlanService) UpdateSectionContentByTitle(ctx context.Context, tid, pid uuid.UUID, title, content string) error {
	nodes, err := s.q.ListOutlineNodes(ctx, store.ListOutlineNodesParams{
		ProjectID: pid, TenantID: tid,
	})
	if err != nil {
		return fmt.Errorf("读取目录失败: %w", err)
	}

	// 先精确匹配，再退化为「包含」匹配（标题可能有细微差异）
	for _, n := range nodes {
		if n.Title == title {
			return s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
				NodeID: n.NodeID, ProjectID: pid, Content: content,
			})
		}
	}
	// 去掉编号后的纯标题比较
	plain := stripLeadingNumber(title)
	for _, n := range nodes {
		if stripLeadingNumber(n.Title) == plain && plain != "" {
			return s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
				NodeID: n.NodeID, ProjectID: pid, Content: content,
			})
		}
	}

	return fmt.Errorf("未找到标题为 %q 的章节", title)
}

// stripLeadingNumber 去掉「1.1 」「第一章 」这类前缀。
//
// 用 rune 遍历而非字节：中文是多字节，按字节比较会溢出。
func stripLeadingNumber(s string) string {
	s = strings.TrimSpace(s)

	// 去掉「第一章 」「第 1 节 」这类中文前缀
	for _, p := range []string{"第", "章", "节"} {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
		}
	}

	runes := []rune(s)
	i := 0
	for i < len(runes) {
		r := runes[i]
		isNum := r >= '0' && r <= '9'
		isPunct := r == '.' || r == '、' || r == ' ' || r == '　'
		if !isNum && !isPunct {
			break
		}
		i++
	}
	return strings.TrimSpace(string(runes[i:]))
}

// UpdateSectionContent 更新某节点正文（内部用，支持字符串 ID）。
func (s *TechnicalPlanService) UpdateSectionContent(ctx context.Context, tid, pid uuid.UUID, nodeID, content string) error {
	if parsed, err := uuid.Parse(nodeID); err == nil {
		return s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
			NodeID: parsed, ProjectID: pid, Content: content,
		})
	}

	found, err := s.q.FindOutlineNodeByExternalID(ctx, store.FindOutlineNodeByExternalIDParams{
		TenantID: tid, ProjectID: pid, ExternalID: nodeID,
	})
	if err != nil {
		return fmt.Errorf("未找到章节 %s", nodeID)
	}
	return s.q.UpdateOutlineNodeContent(ctx, store.UpdateOutlineNodeContentParams{
		NodeID: found, ProjectID: pid, Content: content,
	})
}
