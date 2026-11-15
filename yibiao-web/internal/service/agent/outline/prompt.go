package outline

import (
	"encoding/json"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"os"
	"path/filepath"
	"strings"
)

// PromptOptions 是构造 Prompt 的参数。
type PromptOptions struct {
	// TenderFile 招标文件在工作区里的文件名
	TenderFile string
	// ProjectName 项目名称
	ProjectName string
	// MaxLevels 最大层级（0 不限制）
	MaxLevels int
}

// BuildPrompt 构造目录生成提示词。
//
// 这段 Prompt 是标书垂类的核心知识沉淀，原则来自桌面版
// outlineGenerationTaskV2.cjs，但重新组织成单阶段版本。
//
// 关键设计（为什么这样写）：
//  1. 先分析评分要求，再生成目录 —— 目录必须覆盖评分点，这是标书的核心逻辑
//  2. 强制 JSON 输出并给出完整 schema —— 程序要能解析
//  3. 明确「不要写正文」—— 目录阶段只定结构，正文是下一阶段的事
//  4. 强调「覆盖全部评分项」—— 漏一条评分项就可能丢分
func BuildPrompt(opts PromptOptions) string {
	tenderFile := opts.TenderFile
	if tenderFile == "" {
		tenderFile = "tender.md"
	}

	maxLevels := opts.MaxLevels
	if maxLevels <= 0 {
		maxLevels = 3
	}

	projectHint := ""
	if opts.ProjectName != "" {
		projectHint = fmt.Sprintf("项目名称：%s\n\n", opts.ProjectName)
	}

	return fmt.Sprintf(`你是资深的投标文件编写专家。请为这个招标项目编制技术标目录。

%s招标文件位于工作区：`+"`%s`"+`

## 工作步骤

### 第一步：分析评分要求

读取招标文件，找出**技术评分标准**：
- 技术分的总分与各评分项分值
- 每个评分项的具体要求（要写什么内容才能得分）
- 是否有强制性的章节要求（如「必须包含项目实施计划」）

把分析结果写入 `+"`%s`"+`（Markdown 格式），内容包括：
- 技术评分项清单（逐条列，带分值）
- 每个评分项对应的建议章节
- 招标文件明确要求的章节（如有）

### 第二步：生成目录

基于评分分析，编制技术标目录并写入 `+"`%s`"+`。

## 目录编制原则

1. **覆盖全部评分项**：每个评分项都必须有对应章节，漏一项就可能丢分
2. **层级不超过 %d 级**：一级章节是大的板块，二三级是具体内容
3. **章节标题要专业**：用投标行业的规范表述，不要口语化
4. **description 要写清楚**：说明这一章要写什么、怎么才能得分，这是给正文生成阶段的指引
5. **不要写正文**：目录阶段只定结构，正文由后续阶段生成
6. **章节数量合理**：一般在 8-20 个一级章节之间，太少覆盖不全，太多显得零散

## 输出格式

`+"`outline.json`"+` 必须是以下结构（严格 JSON，不要加注释或说明文字）：

`+"```json"+`
{
  "outline": [
    {
      "title": "第一章 项目理解与总体方案",
      "description": "阐述对项目背景、建设目标的理解，给出总体技术方案架构。对应评分项「技术方案完整性」20分，需要体现对招标需求的准确把握。",
      "content_mode": "technical",
      "children": [
        {
          "title": "1.1 项目背景理解",
          "description": "分析项目建设的背景与痛点，展示对甲方需求的理解深度。",
          "children": []
        }
      ]
    }
  ]
}
`+"```"+`

字段说明：
- `+"`title`"+`：章节标题（必填）
- `+"`description`"+`：本章要写什么（必填，要具体到能让别人照着写正文）
- `+"`content_mode`"+`：`+"`technical`"+`（技术方案）或 `+"`business`"+`（商务响应），一般填 technical
- `+"`children`"+`：子章节数组，没有子章节时给空数组

## 注意事项

- 只输出 JSON，不要输出 Markdown 代码块包裹（或用一个 `+"```json"+` 块包裹即可）
- 不要编造招标文件里没有的要求
- 如果招标文件没有明确的技术评分标准，就按常规技术标结构编制，并在分析文件里说明
- 完成后不要输出总结，直接结束
`, projectHint, tenderFile, AnalysisFile, OutlineFile, maxLevels)
}

// BuildWorkspaceInstructions 构造工作区约定。
//
// 明确列出工具，避免 Agent 反复试探或去翻软件源码。
func BuildWorkspaceInstructions(toolDecls []any) string {
	var sb strings.Builder

	sb.WriteString("你是投标文件编写专家，当前任务是编制技术标目录。\n\n")

	sb.WriteString("## 当前工作区\n\n")
	sb.WriteString("- `tender.md`：招标文件（已就位，直接读）\n")
	sb.WriteString("- 你的产出：`score-analysis.md`（评分分析）和 `outline.json`（目录）\n\n")

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
		sb.WriteString("\n需要这些能力时直接调用，不要先用 bash 去搜索工具是否存在。\n")
	}

	sb.WriteString("\n## 重要约束\n\n")
	sb.WriteString("1. **只操作当前工作区**，不要读取软件自身的源码或安装目录\n")
	sb.WriteString("2. 招标文件可能很长，先用 `bash` 看文件大小，再用 `grep` 定位关键词（如「评分」「技术分」）\n")
	sb.WriteString("3. 不要编造招标文件里没有的内容\n")
	sb.WriteString("4. 完成后不要输出总结，直接结束\n")

	return sb.String()
}

// readWorkspaceFile 读工作区文件。
func readWorkspaceFile(workspaceDir, name string) ([]byte, error) {
	full := filepath.Join(workspaceDir, filepath.FromSlash(name))
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
	}
	return data, nil
}

// cleanJSON 清理可能被代码块包裹的 JSON。
func cleanJSON(data []byte) []byte {
	return []byte(textutil.StripCodeFence(string(data)))
}
