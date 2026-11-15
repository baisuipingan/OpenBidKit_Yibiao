package service

import (
	"context"
	"fmt"

	"github.com/patrick/yibiao-web/internal/store"
)

// SaveGenerationConfigPatch 合并桌面版 saveGenerationConfig 的局部字段。
//
// 前端每次只传一个字段（outlineMode / outlineWordControlOptions / …），
// 必须回传完整生成配置，否则 TechnicalPlanHome 会把 undefined 写进 state 白屏。
func (s *TechnicalPlanService) SaveGenerationConfigPatch(
	ctx context.Context,
	tenantID, projectID string,
	patch map[string]any,
) (map[string]any, error) {
	tid, pid, err := s.resolve(tenantID, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureProject(ctx, tid, pid); err != nil {
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

	if v, ok := asString(patch["outlineMode"]); ok && v != "" {
		cfg.OutlineMode = v
	}
	if v, ok := asString(patch["outlineExpansionMode"]); ok && v != "" {
		cfg.OutlineExpansionMode = v
	}
	if v, ok := asString(patch["globalFactsMode"]); ok && v != "" {
		cfg.GlobalFactsMode = v
	}
	if v, ok := asString(patch["exportTemplateId"]); ok {
		cfg.ExportTemplateID = v
	}
	if v, ok := asString(patch["exportTemplateScope"]); ok && v != "" {
		cfg.ExportTemplateScope = v
	}
	if v, ok := asString(patch["bidAnalysisMode"]); ok && v != "" {
		cfg.BidAnalysisMode = v
	}
	if v, ok := asString(patch["bidSectionMode"]); ok && v != "" {
		cfg.BidSectionMode = v
	}
	if ow, ok := patch["outlineWordControlOptions"].(map[string]any); ok {
		if n, ok := asInt32(ow["minimumWords"]); ok {
			cfg.MinimumWords = n
		}
		if n, ok := asInt32(ow["maximumWords"]); ok {
			cfg.MaximumWords = n
		}
		if n, ok := asInt32(ow["sectionWords"]); ok {
			cfg.SectionWords = n
		}
	}
	if opts, ok := patch["contentGenerationOptions"].(map[string]any); ok {
		if n, ok := asInt32(opts["imageQuantity"]); ok {
			cfg.ImageQuantity = n
		}
		if b, ok := opts["useAiImages"].(bool); ok {
			cfg.UseAiImages = b
		}
		if b, ok := opts["useMermaidImages"].(bool); ok {
			cfg.UseMermaidImages = b
		}
		if b, ok := opts["useHtmlImages"].(bool); ok {
			cfg.UseHtmlImages = b
		}
		if b, ok := opts["layoutCheck"].(bool); ok {
			cfg.LayoutCheck = b
		}
		if b, ok := opts["useSingleImageLayout"].(bool); ok {
			cfg.UseSingleImageLayout = b
		}
		if b, ok := opts["useImageTextLayout"].(bool); ok {
			cfg.UseImageTextLayout = b
		}
		if b, ok := opts["useThreeImagesLayout"].(bool); ok {
			cfg.UseThreeImagesLayout = b
		}
		if b, ok := opts["useFourImagesLayout"].(bool); ok {
			cfg.UseFourImagesLayout = b
		}
		if v, ok := asString(opts["tableRequirement"]); ok {
			cfg.TableRequirement = v
		}
	}

	_, err = s.pool.Exec(ctx, `
UPDATE technical_plan_generation_config SET
  bid_analysis_mode = $3,
  bid_section_mode = $4,
  outline_mode = $5,
  outline_expansion_mode = $6,
  minimum_words = $7,
  maximum_words = $8,
  section_words = $9,
  global_facts_mode = $10,
  export_template_id = $11,
  export_template_scope = $12,
  use_ai_images = $13,
  use_mermaid_images = $14,
  use_html_images = $15,
  table_requirement = $16,
  image_quantity = $17,
  layout_check = $18,
  use_single_image_layout = $19,
  use_image_text_layout = $20,
  use_three_images_layout = $21,
  use_four_images_layout = $22,
  updated_at = now()
 WHERE project_id = $1 AND tenant_id = $2`,
		pid, tid,
		cfg.BidAnalysisMode, cfg.BidSectionMode,
		cfg.OutlineMode, cfg.OutlineExpansionMode,
		cfg.MinimumWords, cfg.MaximumWords, cfg.SectionWords,
		cfg.GlobalFactsMode, cfg.ExportTemplateID, cfg.ExportTemplateScope,
		cfg.UseAiImages, cfg.UseMermaidImages, cfg.UseHtmlImages,
		cfg.TableRequirement, cfg.ImageQuantity, cfg.LayoutCheck,
		cfg.UseSingleImageLayout, cfg.UseImageTextLayout,
		cfg.UseThreeImagesLayout, cfg.UseFourImagesLayout,
	)
	if err != nil {
		return nil, fmt.Errorf("保存生成配置失败: %w", err)
	}

	// 参考知识文档 ID 走 meta 独立列
	if ids, ok := patch["referenceKnowledgeDocumentIds"].([]any); ok {
		out := make([]string, 0, len(ids))
		for _, item := range ids {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		if err := s.q.UpdateTechnicalPlanReferenceKnowledge(ctx, store.UpdateTechnicalPlanReferenceKnowledgeParams{
			TenantID:                      tid,
			ProjectID:                     pid,
			ReferenceKnowledgeDocumentIds: out,
		}); err != nil {
			return nil, fmt.Errorf("保存参考知识失败: %w", err)
		}
	}

	return GenerationConfigView(cfg), nil
}

// GenerationConfigView 转成前端 TechnicalPlanGenerationConfig 形状。
func GenerationConfigView(cfg store.TechnicalPlanGenerationConfig) map[string]any {
	return map[string]any{
		"bidAnalysisMode":      cfg.BidAnalysisMode,
		"bidSectionMode":       cfg.BidSectionMode,
		"outlineMode":          cfg.OutlineMode,
		"outlineExpansionMode": cfg.OutlineExpansionMode,
		"outlineWordControlOptions": map[string]any{
			"minimumWords": cfg.MinimumWords,
			"maximumWords": cfg.MaximumWords,
			"sectionWords": cfg.SectionWords,
		},
		"globalFactsMode":     cfg.GlobalFactsMode,
		"exportTemplateId":    cfg.ExportTemplateID,
		"exportTemplateScope": cfg.ExportTemplateScope,
		"contentGenerationOptions": map[string]any{
			"useAiImages":           cfg.UseAiImages,
			"useMermaidImages":      cfg.UseMermaidImages,
			"useHtmlImages":         cfg.UseHtmlImages,
			"tableRequirement":      cfg.TableRequirement,
			"imageQuantity":         cfg.ImageQuantity,
			"layoutCheck":           cfg.LayoutCheck,
			"useSingleImageLayout":  cfg.UseSingleImageLayout,
			"useImageTextLayout":    cfg.UseImageTextLayout,
			"useThreeImagesLayout":  cfg.UseThreeImagesLayout,
			"useFourImagesLayout":   cfg.UseFourImagesLayout,
			"htmlImageOptimization": cfg.HtmlImageOptimization,
			"wordCountRepair":       cfg.WordCountRepair,
		},
	}
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asInt32(v any) (int32, bool) {
	switch n := v.(type) {
	case float64:
		return int32(n), true
	case int:
		return int32(n), true
	case int32:
		return n, true
	case int64:
		return int32(n), true
	default:
		return 0, false
	}
}
