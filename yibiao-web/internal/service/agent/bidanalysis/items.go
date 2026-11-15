// Package bidanalysis 实现招标文件分析能力。
//
// 这是标书垂类 Agent 的第一个真实业务场景。
//
// 与桌面版的差异：
//
//	桌面版用「批量并发调模型 + 程序合并」的方式提取 18 个解析项，
//	不经过 Agent。Web 版改成真正的 Agent 任务：
//	  - Agent 用工具读取招标文件（可以分段、可以 grep 定位）
//	  - 逐项提取，每项写成独立文件
//	  - 最后自己汇总成 JSON
//
// 为什么改：
//  1. 招标文件动辄几百页，远超单次上下文。Agent 能自己分段读取。
//  2. 提取项之间有关联（如「项目信息」影响「资格要求」的判断），
//     Agent 能带着上下文工作，纯批量调用做不到。
//  3. 提取质量可校验：Agent 能看到自己写了什么，能自查补漏。
package bidanalysis

import (
	"fmt"
	"strings"
)

// Item 是一个招标解析项。
type Item struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Output      string `json:"output"` // json | markdown
	Required    bool   `json:"required"`
	Description string `json:"description"`
	// Schema 是 JSON 输出时的字段定义
	Schema string `json:"schema,omitempty"`
	// Goals 是提取目标描述
	Goals string `json:"goals,omitempty"`
}

// DefaultItems 是桌面版的 18 个解析项。
//
// 移植自 client/electron/services/bidAnalysisTask.cjs，
// Prompt 原则一字未改——这些是标书垂类经验的沉淀，不能动。
var DefaultItems = []Item{
	{
		ID: "projectInfo", Label: "项目信息", Output: "json", Required: true,
		Description: "项目名称、编号、类型、预算和地址。",
		Goals:       "提取项目名称、项目编号、项目类型、项目预算、项目地址。",
		Schema:      `{"project_name":"项目名称","project_number":"项目编号","project_type":"项目类型","project_budget":"项目预算","project_address":"项目地址"}`,
	},
	{
		ID: "partAInfo", Label: "甲方信息", Output: "json", Required: true,
		Description: "招标人公司、地址、联系人和电话。",
		Goals:       "提取公司名称、地址、联系人、联系电话。",
		Schema:      `{"company_name":"公司名称","address":"地址","contact_person":"联系人","contact_phone":"联系电话"}`,
	},
	{
		ID: "deliveryAndServiceRequirements", Label: "交货和服务要求", Output: "json", Required: true,
		Description: "实施周期、交付范围、地点、验收、质保、售后、响应、培训和文档要求。",
		Goals:       "提取实施周期/工期/交付期限、交付范围、交付/实施地点、验收要求、质保期、售后服务要求、响应时限、培训要求、资料/文档交付要求。",
		Schema:      `{"implementation_period":"实施周期/工期/交付期限","delivery_scope":"交付范围","delivery_location":"交付/实施地点","acceptance_requirements":"验收要求","warranty_period":"质保期","after_sales_service":"售后服务要求","response_time":"响应时限","training_requirements":"培训要求","documentation_requirements":"资料/文档交付要求"}`,
	},
	{
		ID: "keyInfo", Label: "投标关键节点", Output: "json", Required: false,
		Description: "公告、获取文件、递交、截止和开标信息。",
		Goals:       "提取招标公告发布日期、招标文件获取方式、售价、获取时间、提交地点、截止时间、开标时间、开标地点和其他注意事项。",
		Schema:      `{"bid_announcement_time":"招标公告发布日期","bid_file_get_way":"招标文件获取方式","bid_file_price":"招标文件售价","get_bid_file_time":"获取招标文件时间","bid_document_submission_location":"投标文件提交地点","bid_submission_deadline":"投标截止时间","bid_opening_time":"开标时间","bid_opening_address":"开标地点","other_notes":"其他注意事项"}`,
	},
	{
		ID: "marginInfo", Label: "投标保证金", Output: "json", Required: false,
		Description: "保证金金额、方式、截止和退还条件。",
		Goals:       "提取投标保证金、缴纳方式、截止日期、退还条件、不予退还情形和其他注意事项。",
		Schema:      `{"bidding_deposit":"投标保证金","payment_method":"缴纳方式","due_date":"截止日期","refund_conditions":"退还条件","non_refundable_conditions":"不予退还的情形","other_notes":"其他注意事项"}`,
	},
	{
		ID: "agentInfo", Label: "代理机构信息", Output: "json", Required: false,
		Description: "代理机构联系方式和账户信息。",
		Goals:       "提取代理机构名称、地址、联系人、电话、邮箱和银行账户信息。",
		Schema:      `{"company_name":"公司名称","address":"地址","contact_person":"联系人","contact_phone":"联系电话","email":"联系邮箱","bank_account_name":"银行账户名称","bank_account_number":"银行账户账号","bank_account_address":"银行账户开户行","bank_account_address_detail":"银行账户开户行地址"}`,
	},
	{
		ID: "qualificationReview", Label: "资格性审查", Output: "markdown", Required: false,
		Description: "投标人资格条件和资格审查要求。",
		Goals:       "提取招标文件中关于投标人资格性审查的信息。整理成方便阅读的 Markdown，不要使用表格；如果招标文件是表格，请转换为列表。仅输出整理结果。",
	},
	{
		ID: "complianceCheck", Label: "符合性检查", Output: "markdown", Required: false,
		Description: "文件完整性、有效性、规范和偏差处理要求。",
		Goals:       "总结招标文件中关于符合性检查的信息，包括文件完整性、文件有效性、文件规范、偏差处理等。整理成 Markdown，不要使用表格。仅输出整理结果。",
	},
	{
		ID: "scoringCriteria", Label: "评分标准", Output: "markdown", Required: true,
		Description: "技术分、商务分、价格分的评分规则和权重。",
		Goals:       "提取招标文件中的评分标准，按技术评分、商务评分、价格评分分类整理。保留原始分值、评分细则和加分扣分条件。使用 Markdown 列表，不要使用表格。",
	},
	{
		ID: "technicalRequirements", Label: "技术需求", Output: "markdown", Required: true,
		Description: "技术规格、功能要求、性能指标。",
		Goals:       "提取招标文件中的技术需求，包括技术规格、功能要求、性能指标、接口要求、兼容性要求。按招标文件的章节结构组织，保留关键指标的具体数值。使用 Markdown 列表，不要使用表格。",
	},
	{
		ID: "responseFileRequirements", Label: "响应文件编制要求", Output: "markdown", Required: true,
		Description: "投标文件的组成、格式、装订和递交要求。",
		Goals:       "提取招标文件中关于响应文件/投标文件编制与提交的要求，包括文件组成、格式要求、装订要求、份数、密封要求、递交方式和截止时间。使用 Markdown 列表。",
	},
	{
		ID: "invalidBidAndRejectionItems", Label: "无效投标与废标项", Output: "markdown", Required: true,
		Description: "可能导致投标无效或项目废标的条款。",
		Goals:       buildInvalidBidPrompt(),
	},
	{
		ID: "qualificationRequirements", Label: "资质要求", Output: "markdown", Required: true,
		Description: "企业资质、人员资质、业绩要求。",
		Goals:       "提取招标文件中对投标人的资质要求，包括企业资质证书、人员资格证书、类似业绩要求、财务要求、信用要求。明确区分「必须满足」和「加分项」。使用 Markdown 列表。",
	},
	{
		ID: "contractTerms", Label: "合同条款", Output: "markdown", Required: false,
		Description: "付款方式、违约责任、争议解决。",
		Goals:       "提取招标文件中的主要合同条款，包括付款方式和进度、履约保证金、违约责任、验收标准、知识产权、保密要求、争议解决方式。使用 Markdown 列表。",
	},
	{
		ID: "projectBackground", Label: "项目背景", Output: "markdown", Required: false,
		Description: "项目建设的背景、目标和意义。",
		Goals:       "总结项目的建设背景、建设目标、建设意义和预期效果。基于招标文件原文，不要自行发挥。使用 Markdown 段落。",
	},
	{
		ID: "scopeOfWork", Label: "工作范围", Output: "markdown", Required: false,
		Description: "需要完成的具体工作内容。",
		Goals:       "提取项目的具体工作范围和内容清单，包括需要交付的产品/服务、实施内容、工作界面划分。使用 Markdown 列表。",
	},
	{
		ID: "timeline", Label: "时间节点", Output: "markdown", Required: false,
		Description: "项目的关键时间节点和要求。",
		Goals:       "提取项目的全部时间节点，包括工期要求、里程碑、交付时间、验收时间、质保起始。使用 Markdown 列表，每条注明时间点。",
	},
	{
		ID: "otherRequirements", Label: "其他要求", Output: "markdown", Required: false,
		Description: "上述类别未覆盖的其他重要要求。",
		Goals:       "提取招标文件中不属于上述类别的其他重要要求，如演示要求、样品要求、现场踏勘、答疑安排、联合体投标规定等。使用 Markdown 列表。",
	},
}

// buildInvalidBidPrompt 是「无效投标与废标项」的提示词。
//
// 移植自桌面版，这段 Prompt 体现了标书领域的专业判断：
// 区分「无效投标」和「废标项」两个不同概念，且要求
// 既提取原文明确的、也补充经验判断的高风险项。
func buildInvalidBidPrompt() string {
	return `任务：提取并分析招标文件中的「无效投标」和「废标项」。

概念边界：
1. 「无效投标」指投标人、投标文件、签章密封、递交时间、报价、保证金、资格条件、实质性响应等原因导致投标被认定为无效、否决、不予受理或按无效响应处理的情形。
2. 「废标项」指可能导致项目废标、采购失败、重新招标、终止评审、有效投标人不足或实质性响应不足的条款或风险项。
3. 招标文件使用「否决投标」「投标无效」「不予受理」「无效响应」「重大偏差」「实质性偏离」「废标情形」等同义表达时，也要按上述边界归类。

输出要求：
1. 必须明确区分「无效投标」和「废标项」。
2. 「招标文件中明确提到的」只能提取招标文件中明确出现或同义表达的内容，尽量保留招标文件中的关键句；如果没有提及，写「招标文件未提及」。
3. 「此类标书还可能涉及的」需要根据你的经验，补充招标文件中未明确提及、但结合本招标文件类型和招投标经验判断非常重要的高风险遗漏项。
4. 不要罗列所有常见可能项，不要输出泛泛的通用清单；每个小节最多输出 3-5 条。
5. 不要使用表格，使用 Markdown 列表。
6. 仅输出下方格式，不要输出解释、过程或额外段落。

输出格式：
# 招标文件中明确提到的

## 无效投标
- ...

## 废标项
- ...

# 此类标书还可能涉及的

## 无效投标
- ...

## 废标项
- ...`
}

// SystemPrompt 是招标分析的通用系统提示。
//
// 移植自桌面版 bidAnalysisTask.cjs 的 stableSystemPrompt，一字未改。
const SystemPrompt = `你是专业的投标资料分析助手。请严格基于用户提供的上下文完成提取和总结。

通用要求：
1. 保持信息全面、准确，优先使用用户提供上下文中的内容；除非具体任务明确要求或允许根据经验补充，否则不要自行编造
2. 已提取到相关内容但局部信息没有提及时，明确写"没有提及"
3. 只输出最终结果，不输出过程、提示语或客套话
4. 始终使用简体中文`

// BuildItemPrompt 构造单项提取的提示词。
func BuildItemPrompt(item Item) string {
	if item.Output == "json" {
		return fmt.Sprintf(`任务：%s

目标：%s

约束：
1. 输出格式必须为 JSON。
2. 严格按照以下 JSON 格式输出，只修改 value，禁止修改 key 和结构。
3. 招标文件中没有的字段填充"没有提及"。

JSON 格式：
%s

仅输出 JSON，不要输出其他内容。`, item.Label, item.Goals, item.Schema)
	}
	return fmt.Sprintf("任务：%s\n\n%s", item.Label, item.Goals)
}

// BuildAgentPrompt 构造整个分析任务的 Agent 提示词。
//
// 注意设计：不是「一次提取 18 项」，而是让 Agent 用工具
// 逐个读取、逐个写入文件。这样：
//   - 不受单次上下文限制
//   - 每项独立成文件，便于程序和人工核对
//   - Agent 能看到进度，能自查遗漏
func BuildAgentPrompt(tenderPath string, items []Item) string {
	var sb strings.Builder

	sb.WriteString(SystemPrompt)
	sb.WriteString("\n\n---\n\n")
	sb.WriteString("## 本次任务\n\n")
	sb.WriteString(fmt.Sprintf("招标文件位于 `%s`。请依次提取以下 %d 个解析项，每项写入独立文件。\n\n", tenderPath, len(items)))

	sb.WriteString("### 工作流程\n\n")
	sb.WriteString("1. 先用 `read` 或 `bash` 查看招标文件的规模和结构（文件可能很长）\n")
	sb.WriteString("2. 按需分段读取，定位每个解析项对应的内容\n")
	sb.WriteString("3. 每个解析项写入 `items/<id>.md` 或 `items/<id>.json` 文件\n")
	sb.WriteString("4. 全部完成后写入 `summary.json`，列出已完成的项\n\n")

	sb.WriteString("### 解析项清单\n\n")
	for _, item := range items {
		marker := ""
		if item.Required {
			marker = " **[必填]**"
		}
		sb.WriteString(fmt.Sprintf("#### %s（`%s`）%s\n\n", item.Label, item.ID, marker))
		sb.WriteString(fmt.Sprintf("输出格式：%s\n\n", item.Output))
		sb.WriteString("提取要求：\n")
		sb.WriteString(item.Goals)
		sb.WriteString("\n\n")
		if item.Schema != "" {
			sb.WriteString("JSON 结构（只改 value，不改 key）：\n```json\n")
			sb.WriteString(item.Schema)
			sb.WriteString("\n```\n\n")
		}
	}

	sb.WriteString("### 注意事项\n\n")
	sb.WriteString("- 招标文件可能很长，不要试图一次读完，用 grep/rg 定位关键章节\n")
	sb.WriteString("- 每个文件只写该项的内容，不要把其他项的内容混进来\n")
	sb.WriteString("- 找不到的内容写「没有提及」，不要编造\n")
	sb.WriteString("- 全部完成后不要输出总结，直接结束\n")

	return sb.String()
}
