package content

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PromptOptions 是构造 Prompt 的参数。
type PromptOptions struct {
	TenderFile  string
	Sections    []Section
	GlobalFacts string
	MinWords    int
	// ImagePlan 是配图编排结果（格式化后的文本）。
	// 为空表示不安排配图。
	ImagePlan string

	// CompletedSections 已完成的小节 nodeId 列表（恢复任务时用）。
	//
	// 恢复不靠 sidecar 会话（重启已失效），靠的是磁盘上的产物。
	// 告诉 Agent 哪些已完成，它就会跳过，只补未完成的。
	CompletedSections []string
}

// BuildPrompt 构造正文生成提示词。
//
// 设计要点（结合桌面版 contentGenerationAgent.cjs 的经验）：
//
//  1. **逐节生成**：每节独立写文件，写完更新清单。
//     好处：中途失败不丢进度；单节上下文可控。
//
//  2. **字数控制**：明确每节目标字数，不用全文目标代替本节目标。
//     桌面版踩过坑：Agent 会把全文字数摊到单节，导致每节都超长。
//
//  3. **全局事实约束**：人员姓名、金额、型号等必须全文一致。
//     这是标书的硬要求，写错会被判废标。
//
//  4. **禁止编造**：招标文件没写的内容不要瞎编，
//     但允许基于行业经验合理补充（标书本来就要求"方案详实"）。
//
//  5. **Markdown 输出**：第一版用 Markdown，后续再升级到受限 HTML
//     （桌面版用 HTML 是为了精确控制配图布局，Markdown 做不到）。
func BuildPrompt(opts PromptOptions) string {
	tenderFile := opts.TenderFile
	if tenderFile == "" {
		tenderFile = "tender.md"
	}

	var sb strings.Builder

	// ===== 角色与总体要求 =====
	sb.WriteString("你是资深的投标文件编写专家。请按目录逐节编写技术方案正文。\n\n")

	sb.WriteString("## 工作区\n\n")
	sb.WriteString(fmt.Sprintf("- `%s`：招标文件（务必先读，正文必须对齐招标要求）\n", tenderFile))
	sb.WriteString(fmt.Sprintf("- `%s/`：各节正文输出目录\n", SectionsDir))
	sb.WriteString(fmt.Sprintf("- `%s`：进度清单（每写完一节就更新状态）\n\n", ManifestFile))

	// ===== 全局事实 =====
	if strings.TrimSpace(opts.GlobalFacts) != "" {
		sb.WriteString("## 全局事实（全文必须严格一致）\n\n")
		sb.WriteString(opts.GlobalFacts)
		sb.WriteString("\n\n")
		sb.WriteString("**这些事实在所有章节中必须完全一致**，包括人员姓名、金额数字、")
		sb.WriteString("产品型号、工期天数、资质名称。前后矛盾会导致标书被判无效。\n\n")
	}

	// ===== 字数要求 =====
	sb.WriteString("## 字数要求\n\n")
	if opts.MinWords > 0 {
		sb.WriteString(fmt.Sprintf("全文不少于 %d 字。\n", opts.MinWords))
	}
	sb.WriteString("**每节按各自的「目标字数」安排篇幅，不要用全文字数摊到单节。**\n")
	sb.WriteString("目标字数是指导值，允许 ±20% 浮动；不为凑字数重复表达，也不因省事而大幅缩水。\n\n")

	// ===== 待生成的章节 =====
	sb.WriteString("## 待生成的章节\n\n")
	for i, s := range opts.Sections {
		sb.WriteString(fmt.Sprintf("### %d. %s\n\n", i+1, s.Path))
		if s.Description != "" {
			sb.WriteString(fmt.Sprintf("- 写作要点：%s\n", s.Description))
		}
		if s.TargetWords > 0 {
			sb.WriteString(fmt.Sprintf("- 目标字数：约 %d 字\n", s.TargetWords))
		}
		sb.WriteString(fmt.Sprintf("- 输出文件：`%s/%s`\n", SectionsDir, sectionFileName(s)))
		sb.WriteString(fmt.Sprintf("- 清单 nodeId：`%s`\n", s.NodeID))

		// 知识库参考素材：有就列出来，没有不提
		if len(s.KnowledgeRefs) > 0 {
			sb.WriteString("\n**参考素材**（用户知识库中的真实材料，优先采用其中的具体信息）：\n\n")
			for j, ref := range s.KnowledgeRefs {
				sb.WriteString(fmt.Sprintf("%d. 【%s】", j+1, ref.Title))
				if ref.Source != "" {
					sb.WriteString(fmt.Sprintf("（来源：%s）", ref.Source))
				}
				sb.WriteString("\n")
				if ref.Resume != "" {
					sb.WriteString(fmt.Sprintf("   摘要：%s\n", ref.Resume))
				}
				if ref.Content != "" {
					sb.WriteString("   内容：\n")
					// 缩进正文，避免和章节结构混淆
					for _, line := range strings.Split(ref.Content, "\n") {
						if strings.TrimSpace(line) != "" {
							sb.WriteString("   > " + line + "\n")
						}
					}
				}
				sb.WriteString("\n")
			}
			sb.WriteString("**使用原则**：\n")
			sb.WriteString("- 素材里的具体数据（型号、参数、案例、资质）直接采用，这比编造的可信\n")
			sb.WriteString("- 但不要整段抄写 —— 要结合本章节主题重新组织语言\n")
			sb.WriteString("- 素材与本章无关时忽略，不要硬塞\n\n")
		} else {
			sb.WriteString("\n")
		}
	}

	// ===== 恢复说明 =====
	if len(opts.CompletedSections) > 0 {
		sb.WriteString("## 恢复任务\n\n")
		sb.WriteString(fmt.Sprintf(
			"**这是恢复的任务**：上次执行到一半被中断，已有 %d 个小节完成。\n\n",
			len(opts.CompletedSections),
		))
		sb.WriteString("已完成的小节（**不要重新生成，直接跳过**）：\n\n")
		for _, id := range opts.CompletedSections {
			sb.WriteString("- `" + id + "`\n")
		}
		sb.WriteString("\n已完成的正文文件都在 `" + SectionsDir + "/` 下，需要参考时可以直接读。\n")
		sb.WriteString("你只需要生成清单里 `status` 仍为 `pending` 的小节。\n\n")
	}

	// ===== 配图安排 =====
	if strings.TrimSpace(opts.ImagePlan) != "" {
		sb.WriteString("## 配图安排\n\n")
		sb.WriteString(opts.ImagePlan)
		sb.WriteString("\n")
	} else {
		sb.WriteString("## 配图\n\n")
		sb.WriteString("本次不安排配图。正文只用文字表达，不要插入配图块。\n\n")
	}

	// ===== 写作规范 =====
	sb.WriteString("## 写作规范\n\n")
	sb.WriteString("1. **专业严谨**：使用投标行业的规范表述，避免口语化和营销腔\n")
	sb.WriteString("2. **紧扣评分点**：每节要覆盖目录 description 里提到的得分要点\n")
	sb.WriteString("3. **结构清晰**：用二级/三级标题分层，用列表承载并列内容\n")
	sb.WriteString("4. **表格适度**：需要对比或罗列参数时用 Markdown 表格\n")
	sb.WriteString("5. **不编造硬事实**：企业名称、资质编号、业绩项目不要编造；\n")
	sb.WriteString("   技术方案、实施方法、保障措施可以基于行业经验合理展开\n")
	sb.WriteString("6. **Markdown 格式**：正文用 Markdown，不要用 HTML\n\n")

	// ===== 工作流程 =====
	sb.WriteString("## 工作流程\n\n")
	sb.WriteString("1. 先读招标文件的相关章节（用 `read_file` 配合行范围，或 `bash` + `grep` 定位）\n")
	sb.WriteString("2. 按顺序逐节生成，每节写完**立即**写入对应文件\n")
	sb.WriteString("3. 每写完一节，用 `count_words` 核对字数，偏差过大就调整\n")
	if strings.TrimSpace(opts.ImagePlan) != "" {
		sb.WriteString("4. 按「配图安排」调用 `generate_image` 生成图片，把返回的 assetRef 填进正文\n")
	}
	num := "4"
	if strings.TrimSpace(opts.ImagePlan) != "" {
		num = "5"
	}
	sb.WriteString(num + ". 更新清单文件 `" + ManifestFile + "`，把该节 status 改成 `done` 并记录字数\n")
	sb.WriteString("6. 全部完成后确认清单里所有节都是 `done`\n\n")

	// ===== 清单格式 =====
	sb.WriteString("## 清单文件格式\n\n")
	sb.WriteString("`" + ManifestFile + "` 已预置好，你只需更新 `status` 和 `words` 字段：\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`{
  "taskId": "xxx",
  "generated": [
    {
      "nodeId": "n1-1",
      "title": "1.1 项目背景理解",
      "file": "n1-1_项目背景理解.md",
      "words": 1250,
      "status": "done"
    }
  ],
  "updatedAt": "2026-11-14T10:00:00Z"
}
` + "```\n\n")

	sb.WriteString("status 取值：`pending`（未开始）、`running`（正在写）、`done`（完成）、`error`（失败）\n\n")

	// ===== 收尾 =====
	sb.WriteString("## 注意\n\n")
	sb.WriteString("- 不要输出总结、说明或客套话，写完就结束\n")
	sb.WriteString("- 不要读取软件自身的源码或安装目录\n")
	sb.WriteString("- 如果某节内容确实无法从招标文件获得，按行业惯例写通用方案，并在清单里标注\n")

	return sb.String()
}

// BuildWorkspaceInstructions 构造工作区约定。
func BuildWorkspaceInstructions(toolDecls []any, minWords int) string {
	var sb strings.Builder

	sb.WriteString("你是投标文件编写专家，当前任务是生成技术方案正文。\n\n")

	sb.WriteString("## 可用业务工具\n\n")
	if len(toolDecls) == 0 {
		sb.WriteString("（本会话未注册业务工具，用原生 read/write/bash 即可）\n")
	} else {
		for _, d := range toolDecls {
			b, err := json.Marshal(d)
			if err != nil {
				continue
			}
			var m struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if json.Unmarshal(b, &m) == nil && m.Name != "" {
				sb.WriteString(fmt.Sprintf("- `%s`：%s\n", m.Name, m.Description))
			}
		}
		sb.WriteString("\n需要这些能力时直接调用，不要先用 bash 搜索工具是否存在。\n")
	}

	sb.WriteString("\n## 关键约束\n\n")
	sb.WriteString("1. **只操作当前工作区**，不要读取软件自身源码或安装目录\n")
	sb.WriteString("2. **逐节写入文件**，不要把所有内容攒到最后一次写\n")
	sb.WriteString("3. **字数用 count_words 工具核对**，不要自己估算\n")
	if minWords > 0 {
		sb.WriteString(fmt.Sprintf("4. **全文字数下限是 %d 字**，写完最后统计一次\n", minWords))
	}
	sb.WriteString("5. 完成后不要输出总结，直接结束\n\n")

	sb.WriteString("## 质量标准\n\n")
	sb.WriteString("- 内容要具体：写「采用微服务架构，拆分为 8 个服务」而不是「采用先进架构」\n")
	sb.WriteString("- 覆盖评分点：招标文件提到的每项要求都要在正文里有对应\n")
	sb.WriteString("- 前后一致：全局事实（人员、金额、型号）全文统一\n")

	return sb.String()
}
