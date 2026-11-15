package globalfacts

import (
	"fmt"
	"strings"
)

// NormalizeMode 对齐桌面版 omit / placeholder / fabricate。
func NormalizeMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "omit", "笼统表述":
		return "omit"
	case "placeholder", "待填写":
		return "placeholder"
	case "fabricate", "补全", "补足":
		return "fabricate"
	default:
		if mode == "" {
			return "fabricate"
		}
		return mode
	}
}

func missingValueRule(mode string) string {
	switch NormalizeMode(mode) {
	case "omit":
		return "用户资料已经给出明确事实时，使用资料中的事实值。用户资料没有给出具体值时，该项仍须保留，写成不涉及具体时间、地点、人员、业绩、证书、规格型号、工艺步骤、数量指标的正确笼统承诺，表明本方案按招标要求执行该项，但不展开具体做法。严禁省略该项，也严禁杜撰具体值。必须包含工期、运维期或交货时间中的至少一个相关变量；没有具体值时同样使用笼统承诺，不要编造日期或周期。"
	case "placeholder":
		return "用户资料已经给出明确事实时，使用资料中的事实值。用户资料没有给出具体值时，该项仍须保留，值必须逐字写成【待填写】，不要改写成“待定”“TBD”或其他说法，也不要编造具体值。严禁省略该项。必须包含工期、运维期或交货时间中的至少一个相关变量；没有具体值时使用【待填写】。"
	default:
		return "用户资料已经给出明确事实时，使用资料中的事实值。用户资料没有给出具体值，但该信息对全文一致性重要时，根据项目语境补足一套合理、稳定、不冲突的具体事实值。必须包含工期、运维期或交货时间中的至少一个相关变量。"
	}
}

func jsonExample(mode string) string {
	switch NormalizeMode(mode) {
	case "omit":
		return `{
  "groups": [
    {
      "id": "project_team",
      "title": "项目角色变量",
      "content": "- 项目经理：按招标文件对该岗位的要求配备。\\n- 技术负责人：按招标文件对该岗位的要求配备。"
    }
  ]
}`
	case "placeholder":
		return `{
  "groups": [
    {
      "id": "project_team",
      "title": "项目角色变量",
      "content": "- 项目经理：【待填写】。\\n- 技术负责人：【待填写】。"
    }
  ]
}`
	default:
		return `{
  "groups": [
    {
      "id": "project_team",
      "title": "项目角色变量",
      "content": "- 项目经理：张伟，负责总体协调。\\n- 技术负责人：李明，负责方案设计和联调验收。"
    }
  ]
}`
	}
}

// BuildPrompt 构造全局事实生成提示词（对齐桌面 globalFactsTaskV2）。
func BuildPrompt(mode, fileCatalog string) string {
	mode = NormalizeMode(mode)
	if strings.TrimSpace(fileCatalog) == "" {
		fileCatalog = "- tender.md：招标原文\n- 项目概述.md：项目背景\n- 招标解析结果.md：已解析要点\n- 技术方案目录.md：已确认目录"
	}
	return fmt.Sprintf(`请只在当前工作目录内工作。已有材料足以判断时自主执行，不要调用 ask-user。

任务：整理后续技术方案正文必须统一采用的全局事实变量，写入 %s。

材料用途：
%s

工作原则：
1. 先根据招标文件、项目概述、招标解析结果和技术方案目录，确定全部全局事实大项（id、title）。凡这些材料表明后续正文需要统一口径的事项，都必须建项。
2. 再为每个大项填写 content。优先使用招标文件和招标解析结果中的明确值。
3. 全局事实不是招标要求摘录、评分规则或待办清单，而是正文要统一采用的方案事实、响应设定、承诺口径或执行安排。
4. 每条 content 只写简体中文短 bullet，回答“后续正文遇到这个事项时统一写什么”。
5. 必须包含工期、运维期或交货时间中的至少一个相关变量；缺具体值时按本任务给定的写法填写，不要省略该项。

缺具体值时的写法：
%s

输出：
1. 最终结果写入 %s，必须是纯 JSON，不要 Markdown 代码块。
2. 根对象只有 groups；每项包含 id、title、content。
3. groups 至少 1 项。

格式示意：
%s`, OutputFile, fileCatalog, missingValueRule(mode), OutputFile, jsonExample(mode))
}

// BuildWorkspaceInstructions 工作区说明。
func BuildWorkspaceInstructions() string {
	return "你正在为技术方案整理全局事实变量。只使用工作区已有材料，最终必须写出合法的 global-facts.json。"
}

// DefaultFileCatalog 默认材料清单文案。
func DefaultFileCatalog() string {
	return strings.Join([]string{
		"- tender.md：招标原文，用于确定大项并提取明确事实。",
		"- 项目概述.md：项目背景和术语，用于确定大项。",
		"- 招标解析结果.md：招标文件解析阶段已抽出的项目信息、甲方信息、交货和服务要求。",
		"- 技术方案目录.md：已确认目录，用于判断正文会反复用到哪些统一口径。",
		"- 材料说明.md：本次实际提供的文件清单。",
	}, "\n")
}
