package rejection

import (
	"fmt"
	"strings"
)

// buildCheckPrompt 构造废标项检查的 Prompt。
//
// **Prompt 质量直接决定检查效果**。这里的关键设计（来自桌面版实战经验）：
//
//  1. **排除线下事项**
//     签字、盖章、密封、纸质正副本、现场递交这些从电子文件判断不了。
//     不排除的话，AI 会对每一条都报"无法确认" —— 全是噪音，
//     用户看几条就再也不信这个功能了。
//
//  2. **结构性线索优先**
//     文本解析会丢掉图片、扫描件的内容。如果因为"看不到扫描件正文"
//     就判定"资质证书缺失"，是灾难性的误报。
//     只要投标文件里出现了章节标题/目录项/附件标题这类结构性线索，
//     就必须认为材料已提交。
//
//  3. **必须给证据**
//     每条风险都要引用投标文件原文。没有证据的结论 = 用户无法核实 = 无用。
func buildCheckPrompt(input CheckInput) string {
	var sb strings.Builder

	sb.WriteString("你是投标文件合规性检查专家。请根据招标文件的废标条款，核验投标文件是否踩雷。\n\n")

	// ===== 检查口径 =====
	sb.WriteString("## 一、检查口径（必须严格遵守）\n\n")
	sb.WriteString("以下是招标文件中「无效投标」和「废标项」的解析结果。\n")
	sb.WriteString("**你的检查必须严格限定在这些条款范围内**，不要自行扩大到其他方面。\n\n")
	sb.WriteString("```\n")
	sb.WriteString(input.InvalidItems)
	sb.WriteString("\n```\n\n")

	// ===== 硬性限制（最重要） =====
	sb.WriteString("## 二、必须排除的检查项\n\n")
	sb.WriteString("**以下事项从电子投标文件无法判断，必须完全忽略，绝不能报告为风险：**\n\n")
	sb.WriteString("- 签字、签章、手写签名\n")
	sb.WriteString("- 盖章、公章、骑缝章\n")
	sb.WriteString("- 密封、封装、封套\n")
	sb.WriteString("- 纸质正本/副本份数\n")
	sb.WriteString("- 现场递交、开标现场授权代表到场\n")
	sb.WriteString("- 投标保证金的实际到账情况\n")
	sb.WriteString("- 需要外部系统核验的资质真伪\n\n")
	sb.WriteString("**为什么**：这些事项无法从文本判断。报告它们只会制造噪音，")
	sb.WriteString("让用户以为检查工具不可靠。\n\n")

	// ===== 材料缺失判定规则 =====
	sb.WriteString("## 三、材料缺失的判定规则（关键）\n\n")
	sb.WriteString("投标文件由文本解析得到，**图片、扫描件、截图的正文内容可能不可见**。\n\n")
	sb.WriteString("因此判定材料缺失时，必须按以下顺序判断：\n\n")
	sb.WriteString("1. 先看有没有**结构性线索**：\n")
	sb.WriteString("   - 章节标题（如「第三章 资格证明文件」）\n")
	sb.WriteString("   - 目录项（如「附件一：营业执照复印件」）\n")
	sb.WriteString("   - 材料清单项、表格条目\n")
	sb.WriteString("   - 页码线索、图片占位标记\n")
	sb.WriteString("2. **只要有上述任一线索，就判定材料已提交**，不得因为看不到扫描件正文而报缺失\n")
	sb.WriteString("3. 只有完全没有任何线索时，才可报「可能缺失」，且严重程度不超过 medium\n\n")

	// ===== 自定义检查项 =====
	if strings.TrimSpace(input.CustomCheckItems) != "" {
		sb.WriteString("## 四、用户补充的检查关注点\n\n")
		sb.WriteString("以下是用户额外关注的检查点。同样只在能从文本判断时才使用：\n\n")
		sb.WriteString("```\n")
		sb.WriteString(input.CustomCheckItems)
		sb.WriteString("\n```\n\n")
	}

	// ===== 投标文件 =====
	sb.WriteString("## 五、投标文件内容\n\n")
	for i, doc := range input.BidDocuments {
		sb.WriteString(fmt.Sprintf("### 文件 %d：%s\n\n", i+1, doc.FileName))
		sb.WriteString("```\n")
		sb.WriteString(doc.Content)
		sb.WriteString("\n```\n\n")
	}

	// ===== 输出要求 =====
	sb.WriteString("## 六、输出格式\n\n")
	sb.WriteString("请使用 write 工具把结果写入 `rejection-findings.json`，格式为 JSON 数组。\n\n")
	sb.WriteString("**如果检查后没有发现任何风险，写入空数组 `[]`。**\n\n")
	sb.WriteString("每条风险包含以下字段：\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`[
  {
    "bidFileName": "投标文件.docx",
    "type": "invalidBid",
    "severity": "high",
    "title": "未提供信息系统集成资质证书",
    "summary": "招标要求二级及以上资质，投标文件未发现相关证书",
    "requirement": "投标人须具备信息系统集成及服务资质二级及以上",
    "bidEvidence": "（原文引用：投标文件第三章标题为「资格证明文件」，但内容仅有营业执照和业绩表，未出现资质证书相关字样）",
    "riskReason": "资质是资格性条款，缺失将导致投标无效",
    "suggestion": "补充信息系统集成及服务资质证书扫描件，放在第三章资格证明文件下"
  }
]
`)
	sb.WriteString("```\n\n")

	sb.WriteString("字段说明：\n")
	sb.WriteString("- `type`：`invalidBid`（无效投标）或 `rejectionItem`（废标项）\n")
	sb.WriteString("- `severity`：`high`（必然废标）/ `medium`（可能废标）/ `low`（建议改进）\n")
	sb.WriteString("- `bidEvidence`：**必须引用投标文件原文**。引用不到具体文字时，")
	sb.WriteString("要说明你检查了哪个章节、看到了什么线索\n")
	sb.WriteString("- 全部使用简体中文\n")

	return sb.String()
}

// buildTypoPrompt 构造错别字检查 Prompt。
func buildTypoPrompt(input CheckInput) string {
	var sb strings.Builder

	sb.WriteString("你是中文校对专家。请找出投标文件中的错别字。\n\n")

	sb.WriteString("## 要求\n\n")
	sb.WriteString("1. 只报**确定性错误**（错别字、同音字混淆、明显的词语误用）\n")
	sb.WriteString("2. 不要报风格问题、不要改写作手法\n")
	sb.WriteString("3. 每处错误必须给出原文片段（用于定位）\n")
	sb.WriteString("4. 专业术语、公司名、产品名要谨慎，不要当错别字改\n")
	sb.WriteString("5. 最多报 50 条（按严重程度排序，先报影响理解的）\n\n")

	sb.WriteString("## 投标文件\n\n")
	for i, doc := range input.BidDocuments {
		sb.WriteString(fmt.Sprintf("### 文件 %d：%s\n\n", i+1, doc.FileName))
		sb.WriteString("```\n")
		sb.WriteString(doc.Content)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## 输出\n\n")
	sb.WriteString("用 write 工具写入 `typo-findings.json`，JSON 数组格式：\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`[
  {
    "bidFileName": "投标文件.docx",
    "wrongText": "竞标",
    "correctText": "投标",
    "originalExcerpt": "……本次竞标将严格按照……",
    "reason": "「竞标」与「投标」混淆，招标文件中应统一使用「投标」",
    "locationHint": "第三章 服务承诺"
  }
]
`)
	sb.WriteString("```\n\n")
	sb.WriteString("没有发现错误时写入 `[]`。\n")

	return sb.String()
}

// buildLogicPrompt 构造逻辑谬误检查 Prompt。
func buildLogicPrompt(input CheckInput) string {
	var sb strings.Builder

	sb.WriteString("你是投标文件逻辑审核专家。请找出文件中的逻辑矛盾和前后不一致。\n\n")

	sb.WriteString("## 重点检查\n\n")
	sb.WriteString("1. **数据冲突**：同一指标在不同章节给出不同数值\n")
	sb.WriteString("2. **时间矛盾**：工期/服务期/质保期前后不一致\n")
	sb.WriteString("3. **主体矛盾**：项目名称、招标编号、投标人名称前后不一致\n")
	sb.WriteString("4. **承诺冲突**：前文承诺 X，后文说做不到\n")
	sb.WriteString("5. **计算错误**：报价汇总与分项不符、百分比加总不为 100%\n\n")

	sb.WriteString("## 不要报\n\n")
	sb.WriteString("- 表述重复（同一件事说两遍，正常）\n")
	sb.WriteString("- 详略不当（某章写得简略）\n")
	sb.WriteString("- 风格问题\n\n")

	sb.WriteString("## 投标文件\n\n")
	for i, doc := range input.BidDocuments {
		sb.WriteString(fmt.Sprintf("### 文件 %d：%s\n\n", i+1, doc.FileName))
		sb.WriteString("```\n")
		sb.WriteString(doc.Content)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## 输出\n\n")
	sb.WriteString("用 write 工具写入 `logic-findings.json`，JSON 数组格式：\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(`[
  {
    "bidFileName": "投标文件.docx",
    "wrongText": "工期 90 日历天",
    "correctText": "工期 120 日历天",
    "originalExcerpt": "投标函：工期 90 日历天。实施计划：项目工期 120 日历天。",
    "reason": "投标函与实施计划的工期不一致，评标时会被认定为实质性偏离",
    "locationHint": "投标函 / 实施计划"
  }
]
`)
	sb.WriteString("```\n\n")
	sb.WriteString("没有发现问题时写入 `[]`。\n")

	return sb.String()
}

// buildExtractionPrompt 构造「无效投标与废标项」抽取 Prompt。
//
// 目标：从招标文件里把可能导致废标的条款全找出来，供后续检查用。
// 关键要求：必须保留条款原文，不能概括 —— 后面比对时要按原文判断。
func buildExtractionPrompt(tenderContent string) string {
	return `你是招标文件分析专家。请从招标文件中提取「无效投标」和「废标项」条款。

## 概念区分

- **无效投标**：投标文件本身不符合要求，导致该投标被否决
  （如：未提供资格证明、报价超预算、技术参数负偏离超标）

- **废标项**：导致整个项目废标、采购失败、重新招标的情形
  （如：有效投标人不足三家、所有投标均不满足实质性要求）

## 提取要求

1. **逐条列全**：招标文件里凡带「无效」「废标」「否决」「不予接受」「按废标处理」
   「视为放弃」等表述的条款，全部提取
2. **保留原文**：每条必须引用招标文件的原句，不要概括改写 ——
   后续检查要按原句判断，概括会丢失关键限定条件
3. **标注位置**：说明该条款在招标文件的哪一章（如「第二章 投标人须知 3.7条」）
4. **区分可否电子核验**：
   - 能从投标文件正文判断的：「投标人须提供XX资质」「技术参数须满足XX」
   - 无法从电子文件判断的：「须加盖公章」「须现场递交纸质件」
   后者也要列出（供用户知道我漏了什么），但标注「线下核验」

## 输出格式

用 write 工具写入 ` + "`" + extractionOutputFile + "`" + `，Markdown 格式：

` + "```markdown" + `
## 一、无效投标情形

### 1. 资格条件不符
**原文**：「投标人不具备招标文件规定的资格条件的，其投标无效。」
**位置**：第二章 投标人须知 3.7.1
**可否电子核验**：可

### 2. 未按要求提供资质证明
**原文**：「未提供信息系统集成及服务资质证书的，投标无效。」
**位置**：第三章 资格要求
**可否电子核验**：可

### 3. 未加盖公章
**原文**：「投标文件未加盖投标人公章的，投标无效。」
**位置**：第二章 3.7.4
**可否电子核验**：否（线下核验）

## 二、废标情形

### 1. 有效投标人不足三家
**原文**：「提交投标文件的投标人少于三家的，本次招标废标。」
**位置**：第二章 8.1
**可否电子核验**：否
` + "```" + `

## 招标文件内容

` + "```" + `
` + tenderContent + `
` + "```" + `

请开始提取。全部使用简体中文。
`
}
