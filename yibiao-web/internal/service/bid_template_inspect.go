package service

// BidTemplateInspection 对齐桌面版 BidTemplateInspection。
// 前端 TechnicalPlanHome.switchStep 会读 added.length / changed.length。
type BidTemplateInspection struct {
	Added   []BidTemplateInspectionItem `json:"added"`
	Changed []BidTemplateInspectionItem `json:"changed"`
}

// BidTemplateInspectionItem 是不一致条目。
type BidTemplateInspectionItem struct {
	ID       string `json:"id"`
	Number   string `json:"number,omitempty"`
	Title    string `json:"title,omitempty"`
	AutoFill bool   `json:"auto_fill"`
	RulePage bool   `json:"rule_page"`
}

// InspectBidTemplate 按桌面规则核对投标模版。
//
// 桌面跳过条件（直接返回空数组）：
//   - 没有目录
//   - 技术文件独立成册（standalone-technical）
//   - 没有招标 Word 原件（Web 版目前没有本地 docx 清单，等价跳过比对）
//   - 正文已开始
//
// Web 版完整抽章比对依赖桌面的 collectTemplateItems / Word 章节清单，
// 尚未完整移植；跳过之外也返回空数组（视为无 mismatch），
// 绝不返回 {exists, chapterIds} 这种会让前端抛错的形状。
func InspectBidTemplate(state *PlanState) BidTemplateInspection {
	out := BidTemplateInspection{
		Added:   []BidTemplateInspectionItem{},
		Changed: []BidTemplateInspectionItem{},
	}
	if state == nil {
		return out
	}
	if state.OutlineData == nil || len(state.OutlineData.Outline) == 0 {
		return out
	}
	outlineMode := state.OutlineMode
	if outlineMode == "" {
		// 兼容：目录任务 stats 里也可能带 outline_mode；Web 主要读配置列
		if task := state.OutlineGenerationTask; task != nil {
			if s, ok := task["outline_mode"].(string); ok && s != "" {
				outlineMode = s
			}
		}
	}
	if outlineMode == "standalone-technical" {
		return out
	}
	if state.ContentGenerationTask != nil {
		return out
	}
	if runtime := state.ContentGenerationRuntime; runtime != nil {
		if started, ok := runtime["generation_started"].(bool); ok && started {
			return out
		}
	}
	// Web 尚无招标 Word 原件路径清单；桌面在 listTenderSourceDocx 为空时也跳过。
	return out
}
