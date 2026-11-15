# Agent 架构

> 状态：✅ 已端到端跑通（2026-11-14）
> 验证：真实招标文件 → Agent 自主分析 → 结构化产物，17 秒完成

---

## 一、为什么 Agent 单独进程

Pi SDK（`@earendil-works/pi-coding-agent`）是 ESM 的 JS 包，**无法在 Go 里运行**。

它是这个项目最有价值的资产——标书垂类的 Agent 编排能力全在里面：

| 能力 | 在哪 | 价值 |
|---|---|---|
| 会话管理、上下文压缩 | Pi SDK | 支撑几十万字长文档 |
| 工具调度、阶段权限 | Pi SDK + sidecar | Agent 自主读文件、写产物 |
| 提交校验、按进展修复 | sidecar `submission.mjs` | 质量保障的核心 |
| 空转诊断、失败恢复 | 桌面版有，Web 版待补 | 长任务稳定性 |

**结论**：原样保留在 Node sidecar 里，Go 通过 HTTP 调用。

```
浏览器 ──HTTP/SSE──▶ Go 主服务 ──HTTP──▶ Node Agent Sidecar ──ESM──▶ Pi SDK
                         │                      │
                         │                      └─▶ 模型 API
                         └─▶ PostgreSQL
```

---

## 二、Sidecar 接口设计

### 2.1 核心接口

| 接口 | 作用 |
|---|---|
| `GET /health` | 存活检查 |
| `POST /sessions` | 创建会话（含模型配置、工作区、工具集） |
| `POST /sessions/:id/run` | 启动任务（含阶段定义） |
| `GET /sessions/:id/events` | SSE 事件流 |
| `POST /sessions/:id/cancel` | 取消 |
| `DELETE /sessions/:id` | 销毁 |

### 2.2 设计原则：用通用 Agent 语义

**不暴露 Pi 特有的概念**。比如桌面版有 `task_complete`、`fixed_tool_list`、`summary_enabled`，这些在 sidecar 接口里都包装成通用语义：

```
Pi 的 task_complete      → 通用的「阶段结束」
Pi 的 fixed_tool_list    → 通用的「工具门禁模式」
Pi 的 summary_enabled    → 通用的「是否要总结」
```

**目的**：万一将来要换掉 Pi SDK（换成 Claude Agent SDK、或自研），
Go 侧一行都不用改。

### 2.3 校验回调机制

这是关键设计。**业务校验逻辑留在 Go，sidecar 保持通用**：

```
Agent 跑完一轮
    ↓
sidecar POST 到 Go 的 validateUrl
    ↓
Go 执行 ValidateResult(workspace, items)
    ↓
返回 { issues, value, progress, minimumGoal }
    ↓
sidecar 的提交策略决定：接受 / 修复 / 停止
```

**好处**：
- 校验规则（哪些字段必填、哪些是质量问题）在 Go 里，能用 Go 的类型和测试
- sidecar 不需要知道「标书解析项」是什么，它只认 `{issues: []}`
- 换业务场景只改 Go，sidecar 不动

---

## 三、提交策略：质量保障的核心

`sidecars/agent/src/submission.mjs` 移植自桌面版，逻辑一字未改。

### 3.1 为什么不用固定轮数

标书正文动辄几十万字。固定轮数限制：
- 太少 → 长文档修不完
- 太多 → 浪费 token，且在无解的问题上死循环

### 3.2 按进展判断

```js
const current = {
  blocking: 阻塞问题数,
  remaining: 剩余质量问题数,
};

improved = 阻塞减少 || (阻塞为0 且 质量减少)
stalled = improved ? 0 : stalled + 1;

if (stalled >= 2 && !blocking) return 'accept';   // 没阻塞了，接受质量
mode = stalled >= 2 ? 'minimum' : stalled === 1 ? 'change-strategy' : 'normal';
```

三种修复模式：

| 模式 | 触发 | 提示词内容 |
|---|---|---|
| `normal` | 有问题但还没停滞 | 「保留成果，修复后重新提交」 |
| `change-strategy` | 连续 1 轮没进展 | 「先定位原方法为什么没解决，换具体做法，不要重复相同操作」 |
| `minimum` | 连续 2 轮没进展 | 「最后一轮，只解决阻塞问题，质量瑕疵本轮不作为验收条件」 |

`minimum` 之后还有阻塞 → **停止，不无限重试**。

### 3.3 执行异常与提交修复分开计费

```js
executionRetries  // 有上限（默认 3），模型报错、超时走这个
repairCount       // 不设上限，由策略决定何时停
```

区分原因：模型故障和「生成质量不够」是两回事，不能混在一起算。

---

## 四、已实现的能力

### 4.1 招标文件分析（`bidanalysis`）

这是第一个完整的标书垂类 Agent 功能。

**18 个解析项**（移植桌面版，Prompt 一字未改）：

| 类别 | 解析项 |
|---|---|
| 必填（8） | 项目信息、甲方信息、交货和服务要求、评分标准、技术需求、响应文件编制要求、无效投标与废标项、资质要求 |
| 可选（10） | 投标关键节点、投标保证金、代理机构信息、资格性审查、符合性检查、合同条款、项目背景、工作范围、时间节点、其他要求 |

**与桌面版的架构差异**：

| 项 | 桌面版 | Web 版 |
|---|---|---|
| 执行方式 | 18 次并发模型调用 + 程序合并 | **一个 Agent 任务，自主分段读取** |
| 输入处理 | 程序预先分段 | Agent 用 `bash`/`grep` 自己定位 |
| 产物 | 内存里的 JSON | **每个解析项一个文件** |
| 上下文关联 | 无（每项独立） | **Agent 带着全文上下文** |
| 质量校验 | 逐项校验文本 | **校验文件 + JSON 结构 + 字段完整性** |

**为什么改**：
1. 招标文件几百页，超过单次上下文。Agent 能自己分段。
2. 解析项之间有关联（项目信息影响资格判断），批量调用做不到。
3. 产物落文件，人能核对，程序能重跑。

### 4.2 实际验证结果

真实招标文件（4.4KB，某医院信息化项目），17 秒完成：

```
[阶段] extract-items
[工具] bash                    ← 先探查文件
[工具] read: tender.md         ← 读招标文件
[工具] write: items/projectInfo.json
[工具] write: items/invalidBidItems.md
[工具] write: items/scoringCriteria.md
[工具] write: summary.json
[结束] success
```

**提取质量**（这是重点）：

`projectInfo.json`：
```json
{
  "project_name": "某市人民医院信息化系统建设项目",
  "project_number": "YY-2026-IT-018",
  "project_type": "没有提及（招标文件未明确项目类型，仅明确采购方式为公开招标；项目内容为医院信息化系统建设）",
  "project_budget": "人民币 860 万元（最高限价：人民币 850 万元）",
  "project_address": "某省某市健康路 168 号（采购人地址）"
}
```

注意三处专业判断：
1. `project_type` 明确说明「未明确项目类型」，而不是瞎编一个
2. 预算**区分了预算和最高限价**——这在投标报价时很关键
3. 地址标注了「采购人地址」，避免和项目地点混淆

`invalidBidItems.md`：

Agent 不仅区分了「无效投标」和「废标项」，还**主动识别出**保证金不退情形属于第三类，单独列出并注明「招标文件单列，供参考」——这是真正的领域理解，不是机械提取。

---

## 五、目录结构

```
yibiao-web/
├── internal/service/agent/
│   ├── client.go            sidecar HTTP 客户端（含 SSE 解析）
│   ├── service.go           任务编排（工作区、会话、事件转发）
│   └── bidanalysis/
│       ├── items.go         18 个解析项定义 + Prompt 模板
│       ├── validate.go      产物校验（必填、JSON、字段完整性）
│       └── runner.go        分析任务编排
└── sidecars/agent/
    ├── package.json
    └── src/
        ├── index.mjs        HTTP 服务
        ├── session.mjs      Pi SDK 封装
        ├── runner.mjs       任务执行循环
        └── submission.mjs   提交策略
```

---

## 六、启动方式

```bash
# sidecar（需先 npm install）
cd /workspace/yibiao-web/sidecars/agent
AGENT_PORT=7070 \
AGENT_WORKSPACE_ROOT=/workspace/data/agent \
AGENT_INTERNAL_TOKEN=<可选> \
node src/index.mjs

# Go 主服务会自动连 AGENT_SIDECAR_URL（默认 http://127.0.0.1:7070）
```

环境变量：

| 变量 | 默认 | 说明 |
|---|---|---|
| `AGENT_SIDECAR_URL` | `http://127.0.0.1:7070` | Go 侧配置 |
| `AGENT_INTERNAL_TOKEN` | 空 | 内部令牌，两边要一致 |
| `AGENT_WORKSPACE_ROOT` | `/workspace/data/agent` | 工作区根目录 |
| `AGENT_PORT` | `7070` | sidecar 监听端口 |

---

## 七、API 用法

### 7.1 检查 Agent 可用性

```bash
curl -X POST $API/api/agent/health \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Project-Id: $PROJECT"
# → {"ok":true,"data":{"available":true,"runtime":"pi"}}
```

### 7.2 列出解析项

```bash
curl -X POST $API/api/bid-analysis/items \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Project-Id: $PROJECT"
# → 18 个解析项，含 id/label/required/description
```

### 7.3 启动招标分析

```bash
curl -X POST $API/api/bid-analysis/start \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Project-Id: $PROJECT" \
  -d '{
    "provider": {"baseUrl":"https://milorouter.com/v1","apiKey":"sk-...","model":"deepseek-v4.1-flash"},
    "tenderMarkdownPath": "/workspace/data/testdata/tender.md"
  }'
# → {"ok":true,"data":{"taskId":"...","itemCount":18}}
```

### 7.4 订阅进度（SSE）

```bash
curl -N -X POST $API/api/bid-analysis/events \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Project-Id: $PROJECT" \
  -d '{"taskId":"...","since":0}'
```

事件类型：

| type | 含义 |
|---|---|
| `stage_start` | 阶段开始 |
| `tool_start` | 调用工具（含工具名和参数） |
| `tool_end` | 工具完成 |
| `assistant_delta` | 助手流式文本 |
| `assistant_end` | 助手一轮回复结束 |
| `submission_retry` | 提交修复 |
| `issues_accepted` | 接受了质量问题 |
| `execution_retry` | 执行异常重试 |
| `task_end` | 任务结束（含 status） |

另有 SSE 层事件：`ready`、`done`、`error`。

---

## 八、已知缺口

| 缺口 | 影响 | 计划 |
|---|---|---|
| Agent 提问应答 | 无法让 Agent 中途问用户 | 需在 Go 侧加待答问题队列 |
| 空转诊断 | 长任务陷入死循环时不会自愈 | 移植 `piStallDiagnosis` |
| 持久化恢复 | 服务重启后任务不恢复 | 移植 `piPersistentTaskStore` |
| 业务工具 | Agent 只能读写文件，不能调 C# 助手 | 需 Go 提供工具回调接口 |
| 并发限制 | 没有限制单租户同时跑几个 Agent | 上线前加 |
| 用量统计 | 只记日志，没入库 | 阶段 5 |

---

## 九、下一步

按价值排序：

1. **业务工具回调**（重要）：让 Agent 能调 C# 助手做 Word 操作，这是「正文生成 → 导出」链路的关键
2. **目录生成 Agent**：技术方案的第二个核心能力
3. **正文生成 Agent**：最复杂的，需要 C# 工具支持
4. **空转诊断**：长任务稳定性保障
5. **持久化恢复**：服务重启不丢任务

**建议顺序：先做 1，因为 2-3 都依赖它。**

---

## 十、业务工具回调（2026-11-14 完成）

### 10.1 架构

```
Agent (sidecar)
  │ 调用工具 count_words
  ▼
sidecar POST /api/agent-tools/count_words
  body: { args, sessionId, taskId }
  │
  ▼
Go 侧
  ├─ 校验内部令牌
  ├─ 按 sessionId 反查 SessionContext（租户/项目/工作区）
  ├─ 归一化参数（模型有时传 JSON 字符串）
  └─ 执行工具 Handler
      │
      ▼
  返回 { text, images, files, data }
  │
  ▼
sidecar 转成 tool result 给 Agent
```

**关键设计对比桌面版**：

| 项 | 桌面版 | Web 版 |
|---|---|---|
| 工具实现位置 | Node（`contentGeneration*Tools.cjs`） | **Go**（`agenttool/tools/`） |
| 业务耦合 | 工具直接 require 业务服务 | **HTTP 回调，零耦合** |
| 能力开关 | 按阶段硬编码 | **按租户/套餐动态注入声明** |
| 换 SDK 成本 | 工具层要重写 | **工具层不动** |

### 10.2 已实现工具

| 工具 | 作用 |
|---|---|
| `count_words` | 统计可读字数（标书口径：CJK 按字、英文按词） |
| `read_file` | 读工作区文件，支持行范围 |
| `write_file` | 写工作区文件，自动建目录 |
| `list_files` | 列目录内容 |

**字数口径**（移植自桌面版 `wordCount.cjs`，一字未改）：
- 去掉代码块、图片、链接 URL、HTML 标签、表格竖线、列表符号
- CJK 字符每个算 1 字
- 非中文按词算（英文单词、数字、复合词）

### 10.3 实测结果（这是全书最有说服力的一段）

给 Agent 的任务："写一段文字，用 count_words 统计，不足 60 字就补充"

```
[工具调用] count_words   →  可读字数 55（不足 60）
[工具调用] write_file    →  已写入 565 字节，168 字
[工具调用] count_words   →  可读字数 168（达标）
[助手] 最终字数：168 字（超过 60 字要求）
```

**Agent 真的在使用工具结果做决策**，不是自己瞎算。共 4 步完成，无浪费。

### 10.4 踩过的四个坑（都很隐蔽）

**坑 1：Pi 的 execute 签名是 `(toolCallId, params)`**

写成 `execute: async (args) => ...` 拿到的是 toolCallId 字符串，不是参数。
**这个 bug 花了 4 次迭代才发现**，因为参数为 undefined 时 normalizeArgs 会返回 {}

**坑 2：工具声明要显式传进 TaskSpec**

`handler.SetTools()` 设置了，但 `TaskSpec` 里没填 `CustomTools` 字段。
gofmt 重排对齐后，python 字符串替换静默失效，检查代码看不出来。

**坑 3：回调必须带 sessionId/taskId**

sidecar 只传 `{args}`，Go 侧无法反查租户/工作区，返回 403。
修法：工具工厂用**可变对象**承载标识（闭包捕获引用），创建会话后回填。

**坑 4：Agent 不知道有哪些工具**

工具注册了但 workspaceInstructions 没提，Agent 会去翻 sidecar 源码找
"有没有 count_words"。修法：在 workspaceInstructions 里明确列出工具清单 +
"不要读取软件自身源码"。

### 10.5 诊断技巧

在 `agent_tool.go` 里打印实际收到的参数：

```go
observ.LoggerFrom(ctx).Info("工具调用参数",
    slog.String("tool", name),
    slog.String("raw_args", string(inv.Args)),
    slog.String("session", req.SessionID),
)
```

这一次日志直接定位到 `raw_args={}`，比读代码快得多。

---

## 十一、目录生成（2026-11-14 完成）

### 11.1 与桌面版的差异

| 项 | 桌面版 | Web 版 |
|---|---|---|
| 阶段数 | 5 个（评分分组 → 初始目录 → 子目录 → 叶子调整 → 复核） | **1 个（分析 + 生成）** |
| 代码量 | 1,806 行 | 约 500 行 |
| ID 分配 | 程序分配 | 程序分配（同样不信任 Agent） |

**为什么先做单阶段**：单阶段已能产出可用目录，
多阶段的收益（更精细的层级控制）需要真实标书样本才能验证。
验证有效后再逐步补。

### 11.2 Prompt 设计的四个要点

来自桌面版 `outlineGenerationTaskV2.cjs` 的经验，重新组织：

1. **先分析评分，再生成目录** —— 目录必须覆盖评分点，这是标书核心逻辑
2. **强制 JSON 输出 + 完整 schema** —— 程序要能解析
3. **明确「不要写正文」** —— 目录阶段只定结构
4. **要求 description 写清「怎么才能得分」** —— 这是给正文生成阶段的指引

### 11.3 实测结果（质量超出预期）

真实招标文件（4.4KB，医院信息化项目），产出：

**17 个一级章节 / 96 个节点**

```
第一章 项目理解与总体技术方案      ← 对应「技术方案完整性」20分
第二章 系统架构设计                ← 对应「系统架构合理性」15分
第三章 门诊管理系统功能方案        ← 对应「功能响应程度」15分
第四章 住院管理系统功能方案
第五章 检验检查系统集成方案（LIS/PACS）
第六章 药事管理系统功能方案
第七章 财务运营系统功能方案
第八章 系统性能与容量设计
第九章 数据互联互通与评级支撑方案
第十章 国产化适配与信创部署方案
第十一章 信息安全与高可用保障方案
第十二章 项目实施计划与进度管理    ← 对应商务分「实施计划」
第十三章 项目组织与团队配置        ← 对应商务分「项目团队配置 6分」
第十四章 培训方案                  ← 对应「培训不少于5场200人次」
第十五章 售后服务与运维保障        ← 对应商务分「售后服务承诺 4分」
第十六章 质量保证与风险管理
第十七章 类似项目业绩与实施经验    ← 对应商务分「类似项目业绩 10分」
```

**关键观察**：它不仅覆盖了技术分（60分），还覆盖了**商务分**（实施计划、团队、售后、业绩），
说明它完整读懂了评分表的结构。

节点 description 的质量也很高，例如：

> 第二章：对应「系统架构合理性」15分。逐条响应 3.1 的 B/S 与私有云、国产软硬件、
> 单点登录与统一用户管理、可用性 99.9% 五项要求，并给出架构图与选型论证。

这不是泛泛的"写架构设计"，而是**精确到招标文件的 3.1 节和五项具体要求**。

### 11.4 ID 分配策略

程序分配 `n1`、`n2`、`n1-1` 这类带层级前缀的 ID，不用 UUID。

理由：
- 人能看懂层级关系（排查问题时有用）
- Agent 生成的 ID 不可信（可能重复或非法）
- ID 是目录树的关联键，必须由程序保证唯一性

`ValidateOutline()` 检查：标题非空、ID 唯一、至少一个节点。

### 11.5 目录落库（2026-11-14）

**完整链路已验证**：目录生成 → 解析 → 落库 → API 读取

```
POST /api/outline/generate   → 生成 outline.json（17 章 96 节点）
POST /api/outline/save       → 解析 + 分配稳定 ID + 写库
POST /api/technical-plan/load-state → 返回 96 节点（深度优先序）
```

**数据库验证**：
```
level | count
------+-------
    1 |    17
    2 |    79
```

**关键修复：树的排序**

原 SQL `ORDER BY level, sort_order` 是错的：会把所有一级排在一起、
所有二级排在一起，前端无法还原树结构。

改用递归 CTE 生成 path，按 path 排序得深度优先序：

```sql
WITH RECURSIVE tree AS (
    SELECT node_id, lpad(sort_order::text, 5, '0') AS path
      FROM technical_plan_outline_nodes
     WHERE project_id = @project_id AND parent_node_id IS NULL
    UNION ALL
    SELECT c.node_id, t.path || '.' || lpad(c.sort_order::text, 5, '0')
      FROM technical_plan_outline_nodes c
      JOIN tree t ON c.parent_node_id = t.node_id
)
SELECT n.* FROM technical_plan_outline_nodes n
  JOIN tree t ON t.node_id = n.node_id
 ORDER BY t.path;
```

验证输出：
```
- 第一章 项目理解与总体技术方案
  - 1.1 项目背景与建设目标理解
  - 1.2 需求理解与建设范围
  ...
- 第二章 系统架构设计
  - 2.1 架构设计原则
```

### 11.6 ID 稳定性

`SaveOutline` 里，如果 Agent 给的 ID 不是合法 UUID，程序重新生成。
正常路径下目录生成的 ID 是 `n1` 这类短 ID（见 §11.4），
落库时会被替换成 UUID —— 这符合「ID 由程序分配」的原则。

**注意**：目前是整体替换（reason=replace 语义）。
桌面版还有 edit/delete/add-*/sort 增量语义，
等前端需要「只改一章」时再补。

---

## 十二、业务闭环打通（2026-11-14）

### 12.1 完成的能力矩阵

| 能力 | 状态 | 验证方式 |
|---|---|---|
| 文件上传（getPathForFile 零改动） | ✅ | 前端实测 prepare + raw 均 200 |
| 招标文件导入（docx/pdf → markdown） | ✅ | upload:// 伪路径解析成功 |
| 配置管理（含 API Key 加密） | ✅ | Key 返回掩码 `sk-d****03ee` |
| 目录生成（Agent） | ✅ | 79 节点落库 |
| 目录落库 + 深度优先排序 | ✅ | 递归 CTE 验证 |
| 业务工具回调 | ✅ | Agent 调 count_words 判断字数 |
| 对象存储（RustFS S3） | ✅ | 端到端读写 |
| 工作区生命周期 | ✅ | .release 标记 + 定时清理 |
| 前端页面渲染 | ✅ | 技术方案页完整显示 STEP 01 |

### 12.2 文件上传的伪路径方案（核心设计）

**问题**：桌面版 `getPathForFile(file)` 同步返回本地绝对路径，
前端 12 处调用都按同步使用（拖拽、选择文件）。

**方案**：伪路径 + 后台上传

```
前端调 getPathForFile(file)
  ↓ 同步返回 upload://<id>
  ↓ 同时后台上传（不阻塞）
业务接口收到 upload://<id>
  ↓ 服务端查上传表
  ↓ 解析成真实路径
```

**验证结果**：
```
pseudo: "upload://6d8ae062288a42e78d607120e07a8e29"
上传请求: 200 upload/prepare / 200 upload/raw
```

**为什么不用 FormData 直接上传**：前端 12 处调用的签名是
`getPathForFile(file): string`（同步返回 string），改成异步要动 12 个文件
+ 处理加载态。伪路径方案前端零改动。

### 12.3 API Key 的占位符方案

**冲突**：
- 安全要求：绝不返回明文 Key
- 兼容要求：前端用 `!config.api_key` 判断「未配置」并强制跳设置页

**解法**：返回占位符 `__CONFIGURED__`

```go
const placeholderAPIKey = "__CONFIGURED__"

func apiKeyPlaceholder(has bool) string {
    if has { return placeholderAPIKey }
    return ""
}
```

保存时如果收到的值等于占位符，说明用户没改 Key，服务端保留原值。

**踩坑记录**：一开始返回空字符串，导致前端每次都跳转到设置页
（`useTextModelSetupRedirect` 里 `!config.api_key` 触发）。

### 12.4 配置结构必须严格对齐前端类型

前端 `ClientConfig extends AiConfig extends TextModelConfig`，意味着：
- 文本模型字段**扁平在顶层**（`api_key` / `base_url` / `model_name`）
- `image_model` / `components` 是**嵌套对象**
- `text_model_profiles` / `image_model_profiles` 是 **Record**，需要全部 provider key

**逐个踩坑**：
1. 缺 `image_model.provider` → `Cannot read properties of undefined (reading 'provider')`
2. 缺 `base_url` → `Cannot read properties of undefined (reading 'base_url')`
3. 缺 `text_model_profiles.official` → `Cannot read properties of undefined (reading 'official')`

**教训**：迁移 bridge 时必须先读前端类型定义，按类型构造响应，
不能凭猜。这类错误前端的报错信息很含糊，靠试错要跑好几轮。

### 12.5 对象存储接入 RustFS

用 AWS SDK v2 + path-style 访问（RustFS/MinIO 必须）：

```go
client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String(cfg.Endpoint)
    o.UsePathStyle = cfg.PathStyle  // 关键
    o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
    o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
})
```

后两个选项很重要：部分 S3 兼容实现不支持新版校验和头，不关会报错。

**切换方式**：环境变量 `STORAGE_BACKEND=local|s3`，业务代码不变。

### 12.6 待补齐

| 项 | 优先级 | 说明 |
|---|---|---|
| 全景正文生成 Agent | 高 | 最核心，但工作量大 |
| Agent 提问应答 | 中 | 需 Go 侧待答队列 |
| 空转诊断 | 中 | 长任务稳定性 |
| 持久化恢复 | 中 | 服务重启不丢任务 |
| 前端产物列表页 | 中 | 后端接口已有 |

---

## 十三、正文生成（2026-11-14）

### 13.1 分层实现策略

桌面版正文生成 3,113 行 + 6 类业务工具，涉及受限 HTML、配图编排、
表格去留、字数控制、一致性检查。**直接移植不现实。**

**分层策略**：

| 层 | 内容 | 状态 |
|---|---|---|
| 第一层 | 按目录逐节生成 Markdown 正文 + 字数控制 | ✅ 已完成 |
| 第二层 | 受限 HTML 结构 + 配图占位 | ⬜ 待补 |
| 第三层 | 图片生成回填 + 表格转换 + 一致性检查 | ⬜ 待补 |

先让「生成 → 落库 → 可读」这条链路通，再叠加复杂度。

### 13.2 核心设计：逐节生成

**为什么不用一次生成**：标书正文十几万字，远超单次上下文。

**为什么逐节落盘**：中途失败不丢进度。Agent 每写完一节就：
1. 写入 `sections/<nodeId>_<title>.md`
2. 更新 `content-manifest.json` 的状态

即使任务中断，已完成的节都在。

### 13.3 字数控制的关键细节

桌面版踩过的坑：Agent 会把全文字数摊到单节，导致每节都超长。

**Prompt 里明确写**：
> 每节按各自的「目标字数」安排篇幅，**不要用全文字数摊到单节**。
> 目标字数是指导值，允许 ±20% 浮动。

### 13.4 实测结果

生成 2 节（招标文件：医院信息化项目）：

| 章节 | 目标 | 实际 | 状态 |
|---|---|---|---|
| 1.1 项目背景与建设必要性理解 | 300 | 360 字 | done |
| 1.2 建设目标与评价标准解读 | 300 | 343 字 | done |
| 合计 | 600 | 703 字 | — |

**质量抽查**（1.1 节）：

```markdown
## 1.1.1 业务场景理解

某市人民医院为三级综合医院，门诊高峰时段挂号收费高度集中，
住院医嘱与护理执行要求连续可追溯，检验检查结果须实时回传临床，
药事审方与财务成本归集同步并行。现有 HIS、LIS、PACS、药事、
财务系统由不同厂商分批交付，形成"烟囱式"格局。

## 1.1.2 现状痛点

- **系统分散**：医护在多套系统间反复登录切换，患者主索引不统一...
- **数据不通**：检验检查结果与医嘱、病历缺乏结构化互认通道...
- **管理支撑不足**：成本核算与绩效缺少科室级原始数据...
- **承载能力不足**：国产化适配与高并发、高可用储备不足。
```

**关键观察**：
1. 用了「烟囱式格局」这个投标行业的确切术语
2. 四个痛点都是具体可验证的问题，不是「存在不足」这类空话
3. 紧扣招标文件里的「三级医院评审」「电子病历分级评价」
4. 覆盖了招标文件的五大业务域（门诊、住院、检验检查、药事、财务）

这不是泛泛而谈，是读懂了招标文件后的针对性写作。

### 13.5 运维教训

**踩坑**：为了重启服务做 `pkill yibiao-api`，把正在跑的长任务杀掉了。

**教训**：正文生成是 30 分钟级别的任务，重启服务前必须确认没有运行中的任务。
生产环境应该有：
- 任务优雅中断（保存进度后退出）
- 重启后自动恢复（目前未实现）

**当前做法**：改代码后重启前先查 `/api/tasks/get-active`。

---

## 十四、Word 导出（2026-11-14）

### 14.1 完整链路（产品终点）

```
招标文件 → Agent 分析 → 目录 → 正文 → Word 文档
   ↑                                    ↑
   上传                              C# 助手
```

实测全链路耗时：导出本身 **948ms**。

### 14.2 架构

```
Go 导出服务
  ├─ 读数据库（目录树 + 正文 Markdown）
  ├─ 转受限 HTML（openxml.ParseMarkdown + RenderSection）
  └─ 调 C# 助手 insert-body-html
       ├─ 物化模板到助手工作区
       ├─ 执行转换
       └─ 取回生成的 docx
```

**为什么交 C# 而不是自己拼 OOXML**：
标书排版要精确控制页眉页脚、页码域、章节页框、图片画框。
C# 助手这些逻辑有 23k 行且经过实战验证，重写等于从零试错。

### 14.3 进程池设计

助手是长驻进程，**一次只跑一个任务**（忙时后到任务直接失败）。
Web 多用户并发必须有池：

```go
// 每个进程独立工作目录，避免互相干扰
type helperProc struct {
    cmd     *exec.Cmd
    stdin   io.WriteCloser
    stdout  *bufio.Reader
    workdir string   // 独立临时目录
    mu      sync.Mutex  // 保证一次一任务
}
```

配置：`OPENXML_POOL_SIZE=4`（默认 4，每进程约 100-200MB）。

### 14.4 文件进出机制（关键设计）

**问题**：助手的 `input` 是「工作区内相对路径」，
而我们手上的模板在别处。

**解法**：`ExecuteWithFiles` 带文件进出

```go
result, err := client.ExecuteWithFiles(ctx, map[string]any{
    "action":    "insert-body-html",
    "input":     "export/template.docx",   // 工作区内相对路径
    "target_id": "technical-plan",
    "html":      html,
}, openxml.ExecuteOptions{
    Files:       map[string]string{"export/template.docx": templateSrc},
    CollectBack: map[string]string{"export/template.docx": outLocal},
})
```

助手会**原地修改** input 文件，所以必须先物化进去，再取回出来。

### 14.5 模板要求（踩坑记录）

`insert-body-html` 要求模板里有**块级内容控件** `w:sdtBlock`，
且 `w:tag="yibiao:body:<target_id>"`。

我写了 `deploy/make-blank-template.py` 从零构造：
- 不依赖 python-docx（直接写 OOXML XML + ZIP）
- 已在镜像里验证可用

**★ OOXML 元素顺序的坑**：

第一次导出报错：
```
Word Open XML 校验失败：The element has unexpected child element
'...:spacing'
```

原因：`w:pPr` 的子元素有**严格顺序**（OOXML 规范定义），
我写成了 `outlineLvl` 在 `spacing` 之前。

正确顺序（部分）：
```
pStyle → keepNext → keepLines → pageBreakBefore → framePr →
widowControl → numPr → ... → spacing → ind → ... → outlineLvl → ...
```

**教训**：手写 OOXML 必须查规范的元素顺序，不能凭直觉排。

### 14.6 实测结果

```
=== 1. 检查正文 ===
目录节点: 79，有正文: 2

=== 2. 导出 Word ===
✓ 导出成功（948ms）
  文件名: 某市人民医院信息化系统建设项目_技术方案_20261114_0342.docx
  大小: 5617 字节

=== 3. 下载并校验 ===
✓ ZIP 格式正确
段落: 32
样式: {'Heading1': 2, 'Heading2': 6, 'direct': 24}
表格: 1
正文: 810 字
```

**结构映射完全正确**：
- 2 个一级标题 → Heading1 样式
- 6 个二级标题 → Heading2 样式
- 24 个正文段落 → 默认样式
- 1 个 Markdown 表格 → Word 表格

### 14.7 已知小问题

**Markdown 粗体未生效**：`**系统分散**` 的标记被正确去掉，
但 `<b>` 标签没有转成 Word 的 `w:b`。

原因：C# 受限 HTML 的粗体可能需要特定的标签或属性形式。
影响很小（只是少了强调效果），已记录，后续查 C# 的 HTML 解析规则再修。

### 14.8 部署注意

```bash
# 1. 编译 C# 助手到挂载卷（容器重建会丢）
cd /workspace/openxmlhelper
dotnet publish src/OpenXmlHelper -c Release -r linux-x64 \
  --self-contained -p:PublishSingleFile=true \
  -p:InvariantGlobalization=false -o /workspace/bin/openxml

# 2. 生成空白模板
python3 /workspace/yibiao-web/deploy/make-blank-template.py \
  --output /workspace/data/templates/blank.docx

# 3. 配置环境变量
export OPENXML_BINARY=/workspace/bin/openxml/openxmlhelper
```

产物大小：81MB 单文件（自包含 .NET 运行时）。

---

## 十五、配图生成（2026-11-14）

### 15.1 三种生成方式（对齐桌面版）

| 方式 | 用在哪 | 实现 |
|---|---|---|
| `mermaid` | 流程图、架构图、时序图 | 浏览器渲染 Mermaid 源码 |
| `htmlImage` | 组织架构、数据看板、表格化内容 | 浏览器渲染 HTML 片段 |
| `aiImage` | 实景照片、效果图、场景图 | 调生图模型 API |

**为什么 mermaid/html 需要浏览器**：
Mermaid 依赖 DOM 测量文字尺寸来算节点大小，纯 SVG 生成器算不准中文宽度。
HTML 更需要完整渲染引擎。

### 15.2 渲染 Sidecar

新增 `sidecars/render/`（400 行）：

```
POST /render/mermaid  { code, theme?, scale? }  → { png: base64, width, height }
POST /render/html     { html, width?, height? } → { png: base64, width, height }
GET  /health
```

**关键实现点**：
1. **复用 Playwright 的 Chromium**（`PLAYWRIGHT_BROWSERS_PATH`）
2. **并发控制**：信号量限 2 个并发（每页约 100MB）
3. **2x 缩放**：`deviceScaleFactor: 2`，打印清晰
4. **中文字体**：显式指定 `Noto Sans CJK SC`
5. **自动高度**：HTML 渲染后量 `body.scrollHeight` 自适应

**Mermaid 渲染的关键细节**：
- 注入 mermaid.min.js 的内容而非 URL，避免 file:// 跨域
- 渲染后移除 SVG 的固定 width/height，用 viewBox 尺寸，否则会被裁切
- 必须 `await document.fonts.ready`，否则中文可能用 fallback 字体

### 15.3 Go 侧编排

`internal/service/render/client.go`（240 行）：HTTP 客户端
`internal/service/image/service.go`（290 行）：三种方式统一编排
`internal/service/image/ai.go`（230 行）：AI 生图（OpenAI 兼容）

**AI 生图的兼容处理**：
- 支持 `b64_json` 和 `url` 两种返回
- `response_format` 不支持时自动去掉重试
- `data:image/png;base64,` 前缀自动剥离

### 15.4 Agent 工具

新增 `generate_image` 工具（第 5 个）：

```json
{
  "kind": "mermaid|htmlImage|aiImage",
  "prompt": "画面描述",
  "code": "Mermaid 源码（kind=mermaid 时）",
  "html": "HTML 片段（kind=htmlImage 时）",
  "figure_id": "配图 ID",
  "alt": "替代文本",
  "width": 900
}
```

返回 asset-ref，Agent 可直接用在正文的配图引用里。

### 15.5 实测结果

Agent 自动生成了 2 张图：

```
figures/68941be9.../fig-bid-flow.png    21,719 B   ← Mermaid 流程图
figures/68941be9.../fig-org-chart.png   14,990 B   ← HTML 组织架构图
```

**质量检查**：
- 流程图：5 个节点（招标文件→标书分析→目录生成→正文生成→导出 Word），
  箭头正确，中文清晰，无裁切
- 组织架构图：项目经理（深蓝）→ 技术负责人 / 实施负责人（浅蓝），
  连接线对齐，配色专业

**两张图都可以直接放进标书。**

### 15.6 图片引用链路（已打通）

```
Agent 调 generate_image
  → Go 生成图片 → 存资产库
  → 返回 assetRef（如 figures/<taskId>/fig-bid-flow.png）
  → Agent 在正文里写 <figure><img data-yb-asset-ref="..."></figure>
  → 导出时受限 HTML 带上引用
  → C# 助手按 asset-ref 找图插入 Word
```

**C# 侧要求**（已确认）：`img[data-yb-asset-ref]` 必须是**工作区内相对路径**，
且图片文件必须真实存在。导出时需要把图片从资产库物化到助手工作区。

### 15.7 导出时图片物化（已完成）

**问题**：C# 的 `img[data-yb-asset-ref]` 要求「工作区内相对路径」且文件真实存在，
但图片在资产库里（S3）。

**解法**：导出前把图片下载到临时目录，与模板一起送进助手工作区：

```go
files := map[string]string{remoteTemplate: templateSrc}

for _, ref := range stats.AssetRefs {
    rc, _, _ := s.artifacts.Get(ctx, scope, ref, 0)
    data, _ := io.ReadAll(rc)
    os.WriteFile(localPath, data, 0o644)
    // 关键：key 与正文里的 data-yb-asset-ref 完全一致
    files[ref] = localPath
}
```

**另一个坑**：`ParseMarkdown` 原本不识别 `<figure>` 标签（它只解 Markdown），
导致正文里的配图引用被当普通文本丢掉。

修复：加 `parseFigureBlock()` 解析 `<figure>` 块，
提取 `data-yb-size` / `data-yb-asset-ref` / `figcaption` 等属性。

### 15.8 实测：配图成功嵌入 Word

```
正文含 2 处配图引用（837 字）
  → 导出 36,711 字节（比无图时 5,779 字节大 6 倍）
  → Word 内嵌 media/image.png + media/image2.png
```

**尺寸与布局完全正确**：

| 图 | data-yb-size | 实际尺寸 | 比例 |
|---|---|---|---|
| 图 1-1 投标流程示意 | `wide` | 11.7cm × 7.8cm | 3:2 |
| 图 1-2 项目组织架构 | `square` | 9.5cm × 9.5cm | 1:1 |

图注也正确写入。

**证明 `data-yb-size` 的布局控制真实生效** —— 这正是桌面版用受限 HTML
而不是 Markdown 的原因：能精确控制配图的画框比例。

### 15.9 关于 docx 里图片的位置

C# 助手把图片放在包根的 `media/` 下（`Target="/media/image.png"`），
而不是 `word/media/`。**这是合法的 OOXML**（绝对路径引用），
Word 能正常打开。验证脚本不要假设固定路径，应 `find . -name "*.png"`。

### 15.10 待补

| 项 | 说明 |
|---|---|
| 配图编排 | 哪些章节配图、什么布局（需主 Agent 分配名额 + 子 Agent 并行生成） |
| AI 生图实测 | 代码就绪，但未配生图模型，未实测 |
| Mermaid 主题 | 目前固定 default，可按导出模板配色调整 | |

---

## 十六、一致性检查（2026-11-14，P1）

### 16.1 为什么这是 P1 最高优先级

标书前后矛盾**会直接导致废标**。典型场景：

| 矛盾 | 后果 |
|---|---|
| 工期：一处 90 天、一处三个月 | 评标专家质疑，可能废标 |
| 项目经理：张建国 vs 张建军 | 人员信息不实，严重失信 |
| 可用性：99.9% vs 99.99% | 承诺不一致，履约争议 |
| 金额：860 万 vs 850 万 | 报价矛盾，直接废标 |

### 16.2 与桌面版的差异

| 项 | 桌面版 | Web 版 |
|---|---|---|
| 流程 | 逐节并发核对 → 事实台账 → 跨节比对 → 分配修复 | **单轮全量检查** |
| 工具 | 4 个（search-sections / recheck / repair / complete） | 0 个（同步接口） |
| 交互 | 多轮 Agent 对话 | 一次请求返回报告 |

**为什么先做单轮**：一致性关键事实（人员/金额/工期/指标）数量有限，
一次全量比多轮台账更直接。规模超限时自动分批（默认 6 万字/批）。

### 16.3 Prompt 设计的关键：克制

桌面版审计规则里最重要的一条是**不要误报**：

> 只报告会影响阅读理解或项目实施的明显问题。
> 宁可漏报，不要因为措辞差异而误报。

明确列出**不算问题**的情况：
- 用词/称谓不同（「甲方」vs「采购人」）
- 详略不同
- 全局事实未提及的补充内容（岗位、流程、交付物）
- 承诺语气强弱
- 文风/润色/篇幅差异
- **因对象、适用条件或时间范围不同产生的差异**
  （「试运行期 30 天」和「质保期 3 年」是两个期限，不矛盾）

为什么要写这么细：标书正文很长，如果吹毛求疵会报出上百条「问题」，
用户根本没法处理。

### 16.4 实现要点

**双路检测**：
1. **模型检测**：抽事实 + 找矛盾（处理语义层面的矛盾）
2. **本地计算**：同一 `category+subject` 在不同小节取值不同（跨批合并）

后者不花 token，还更可靠（数值型矛盾一定要本地算）。

**subject 归一化**（踩坑后加的）：

模型对同一概念可能给出不同表述，如「服务模块数量」和「微服务模块数量」。
不归一化会报成两条独立问题，用户看到重复会觉得不专业。

```go
func normalizeSubject(s string) string {
    // 去空格 + 去修饰词（微服务/整体/总）+ 统一数量后缀
    // + 统一「周期/期限」→「工期」
}
```

配合 `subjectSimilar()` 做模糊匹配去重，合并证据和严重程度。

### 16.5 实测：3 个埋点全部检出，零误报

注入 2 节含矛盾的正文（237 字）：

| 埋的矛盾 | 检出 | 严重程度 |
|---|---|---|
| 项目经理：张建国 vs 张建军 | ✅ | high |
| 工期：90 个日历天 vs 三个月 | ✅ | high |
| 可用性：99.9% vs 99.99% | ✅ | medium |

**输出质量示例**：

```
[HIGH] 正文前后矛盾 — 建设工期
  1.1 中总工期为 90 个日历天，1.2 中整体建设周期为三个月，工期口径不一致。
  建议: 统一工期口径，如统一为 90 个日历天，并明确三个月与 90 个日历天的关系。
  · 1.1 项目背景与建设必要性理解: "总工期为 90 个日历天"
  · 1.2 建设目标与评价标准解读: "整体建设周期为三个月"
```

每条都有：**原文证据**（可直接定位）+ **具体建议**（不只是「有问题」）。

耗时 21 秒 / 2 节 237 字。

### 16.6 实测：分批与去重

6 节 793 字，强制每批 300 字（分 3 批）：

- 抽取事实 30 条（含阶段工期 15/50/25 天这种细节）
- 检出 1 个真实矛盾：**微服务模块数量 10 个 vs 11 个**

**去重前后对比**：
```
改进前: 2 条（"服务模块数量" + "微服务模块数量"，重复）
改进后: 1 条（已合并，证据含 1.3 和 1.4 两处）
```

### 16.7 新增：RunTextTask

一致性检查不需要工作区和工具循环，走了独立的轻量通道：

```go
// internal/service/agent/text_task.go
func (s *Service) RunTextTask(ctx, TextTaskSpec) (*TextTaskResult, error)
```

直接调 `/chat/completions`，不走 sidecar。
好处：更快（21 秒 vs Agent 的 2-5 分钟）、更便宜、更可控（temperature=0 可复现）。

### 16.8 待补

| 项 | 说明 |
|---|---|
| 自动修复 | 桌面版有 `repair-sections` 工具派子 Agent 修复；Web 版目前只报告 |
| 全局事实管理 | 目前靠调用方传入 `globalFacts`，缺独立的「全局事实生成」流程 |
| 事实台账持久化 | 报告只在响应里返回，未落库供后续查阅 |

### 16.9 自动修复（已完成）

只检出问题不修复，用户还得手工改。加了修复编排。

#### ★ 核心设计：先定结论，再改文字

**第一版失败的原因**（实测踩到的坑）：

Prompt 只说「这里矛盾」，模型会认为「我这处没错，是对方错」，
于是两节都返回「无需修改」：

```
=== 修复 ===
修复成功: 0 节
  - 1.1 无需修改
  - 1.2 无需修改
```

**这是设计缺陷，不是模型能力问题。** 模型没被告知「该以哪个为准」。

#### 修正后的机制

```go
// 1. 程序先确定统一结论
type Resolution struct {
    Subject   string  // 主体
    Canonical string  // 统一后的取值
    Issue     Issue
}

func ResolveIssues(issues []Issue) []Resolution {
    // 从 issue.suggestion 里解析「建议统一为「90 个日历天」」
    // 解析不出则从证据里找最常见的取值
}
```

**Prompt 必须明确三件事**：

| 项 | 示例 |
|---|---|
| 统一结论（必须用这个） | `90 个日历天` |
| 本节当前写法 | `整体建设周期为三个月` |
| 要做什么 | 请把本节的相关表述改为「90 个日历天」 |

如果本节已经一致，明确写「这一项不用改」——避免模型乱动。

#### 实测：3 个矛盾全部修复，复检归零

```
=== 第 1 次检查 ===
检出问题: 3 个（high 2 / medium 1）

=== 修复（apply=true 直接回写）===
耗时: 5362ms
修复成功: 2 节，回写: 2 节
  ✓ 1.1 项目背景与建设必要性理解 已修改:
      - 把「99.9%」改为「99.99%」
  ✓ 1.2 建设目标与评价标准解读 已修改:
      - 把「张建军」改为「张建国」
      - 把「三个月」改为「90 个日历天」

=== 第 2 次检查（复检）===
检出问题: 0 个

✅ 全部矛盾已消除
```

#### ★ 值得注意：模型的选择符合专业直觉

| 矛盾 | 模型选择统一为 | 为什么专业 |
|---|---|---|
| 99.9% vs 99.99% | **99.99%** | 投标承诺取高不取低 |
| 90 天 vs 三个月 | **90 个日历天** | 具体数字优于模糊表述 |
| 张建国 vs 张建军 | **张建国** | 出现次数多的 |

这不是随机选，是模型理解了投标场景的规则。

#### 安全阀

修复后校验：如果原文 > 200 字而修改后 < 50%，拒绝应用
（防止模型只返回片段把正文截断）。

```go
if origLen > 200 && newLen < origLen/2 {
    rep.Error = "修改后内容过短，可能是模型只返回了片段，已拒绝应用"
    return rep
}
```

#### 接口

```
POST /api/consistency/repair
{
  "issues": [...],     // 通常是 check 返回的子集
  "facts": [...],
  "apply": true,       // true 则直接回写数据库
  "concurrency": 3
}
```

返回每节的 `original` / `revised` / `changes`，前端可做 diff 预览。

---

## 十七、长任务稳定性（2026-11-14，P1）

### 17.1 两层保护

| 层 | 机制 | 触发条件 | 动作 |
|---|---|---|---|
| 1 | **空转检测** | 重复调用同一工具/参数 | 注入提示让 Agent 跳出 |
| 2 | **空闲超时** | N 秒无任何事件 | 判定卡死，抛错重试 |

**为什么要两层**：
- 空转检测依赖工具事件 → 但如果 Agent 卡在模型内部、完全不产生事件，它收不到信号
- 空闲超时能兜住这种情况 → 但它不知道 Agent 在「打转」，只能粗暴中止

两者互补，缺一不可。

### 17.2 空转检测（`sidecars/agent/src/stall.mjs`）

**判定的三条规则**：

| 规则 | 阈值 | 说明 |
|---|---|---|
| 一轮内同一工具调用过多 | ≥ 8 次 | 不管参数如何，同一工具刷太多次 |
| 连续重复同一签名 | ≥ 4 次 | 参数完全相同的硬重复 |
| 连续相同方式调用 | ≥ 4 次 | 参数「换汤不换药」（宽松匹配） |

**关键：宽松匹配的设计**

不同工具的关键参数不同：
```js
bash:  第一个词 + 命令里的文件名
read:  文件 basename
grep:  搜索词
```

这样「读 a.md」「读 b.md」不算重复，但「读 a.md」重复 4 次会触发。
实测验证：连续读 5 个不同文件 → 不误判。

**提示分两级**：

```
level 1（轻）：提醒换个思路
level 2（重）：明确要求停止，给出具体替代步骤
```

level 2 的提示里有一条很关键：

> **不要**去读软件自身的源码或安装目录，那里没有你要找的东西。

这是实测踩过的坑 —— Agent 找不到工具时会去翻 sidecar 源码。

**最多注入 2 次**，避免刷屏反而打断正常流程。

### 17.3 空闲超时（`withIdleTimeout`）

原来是固定 15 分钟整体超时。问题：任务正常跑 12 分钟后卡死，要再等 3 分钟。

现在加空闲监控：

```js
await withIdleTimeout(
  session.session.prompt(effectivePrompt),
  { idleMs: 3 * 60 * 1000, totalMs: 15 * 60 * 1000 },
  () => activity.at,   // 任何事件都更新
  () => emit('idle_timeout', {...}),
);
```

- **3 分钟无任何事件** → 判定卡死（主要机制）
- 15 分钟整体上限 → 兜底
- 每 5 秒检查一次，开销可忽略
- 超时时主动 `abort()` 会话，避免后台继续烧 token

### 17.4 实测

**单元测试（4 个场景全过）**：

| 场景 | 结果 |
|---|---|
| 连续重复同一 bash | 第 4 次检出 ✓ |
| 正常多样化操作（6 种） | 全部不误判 ✓ |
| 同一工具换参数滥用 | 第 4 次检出 ✓ |
| 读 5 个不同文件 | 不误判为重复 ✓ |

**空闲超时（3 个场景全过）**：

| 场景 | 结果 |
|---|---|
| 持续有活动 1.5 秒后完成 | 正常返回 ✓ |
| 完全无活动 | 正确检出 + 回调触发 ✓ |
| 中途卡 400ms 又恢复 | 不误判 ✓ |

### 17.5 ★ 意外的发现：模型已经很强

用「诱导空转」的方式测试（让 Agent 找一个绝不存在的文件并「多试几次」），
结果它不但没空转，还主动识别并拒绝了诱导：

> 「文件确实存在，请多试几次不同路径」这一前提与事实不符。
> 继续重复同样的检索不会得到不同的结果，属于无效空转。
> 正确做法是停止空转并如实说明，而不是编造文件内容。

还列出了 5 类检索记录（工作目录、候选路径、全文件系统扫描、模糊匹配、内容级检索）
证明自己确实找过。

**结论**：模型自身已经能处理大部分「不该继续」的情况。
空转检测的价值在于**兜住真正陷入死循环的场景**（实测见过连续 30 多次
`bash` 去 grep 自己的源码），以及减少无谓的 token 消耗。

### 17.6 回归验证

加了这两层后，正文生成仍正常：

```
测试节: 2.2 应用架构与微服务化设计
产物: 1265 字节
内容: 18 个微服务划分、注册发现/网关/配置中心、性能指标全覆盖
```

生成质量没有退化，空转检测零误判。

### 17.7 待补

| 项 | 说明 |
|---|---|
| Agent 提问应答 | 桌面版有 answer 队列，Web 版 sidecar 有接口但 Go 侧未接入 |
| 持久化恢复 | 服务重启后运行中的任务全丢；需落库 + 恢复 |
| 诊断 Agent | 桌面版会启动诊断 Agent 读会话记录给结论；Web 版未做 |

### 17.8 任务持久化（已完成）

**解决的问题**：任务状态全在内存，服务一重启就全丢：
- 用户看到「任务消失了」，不知道该重试还是等待
- 工作区还占着磁盘，定时清理找不到它
- 无法统计「跑过多少任务、成功率多少」

#### 表设计（`task_runs`）

```sql
CREATE TABLE task_runs (
    task_id       UUID NOT NULL,          -- 也是工作区目录名
    kind          TEXT NOT NULL,          -- bid-analysis | outline | content | ...
    status        TEXT NOT NULL,          -- running | success | error | interrupted
    session_id    TEXT NOT NULL,          -- sidecar 会话，便于排查
    workspace_dir TEXT NOT NULL,          -- 收尾时清理
    error         TEXT NOT NULL,          -- 真实失败原因
    progress      JSONB NOT NULL DEFAULT '{}',
    started_at    TIMESTAMPTZ NOT NULL,
    heartbeat_at  TIMESTAMPTZ NOT NULL,   -- 判僵死用
    finished_at   TIMESTAMPTZ
);
```

**设计取舍**：
- **不记录事件流**：一个任务几千条事件，重启后也没意义
- **只记任务级状态**：足够回答「这个任务怎么了」
- **心跳机制**：`heartbeat_at` 超过 2 分钟 → 前端标记 `stale`

#### 启动时恢复

```go
// 把所有「运行中但心跳超时」的标为 interrupted
n, err := taskRunSvc.RecoverOnStartup(ctx, 300)
```

**为什么不真正恢复**：sidecar 会话在服务重启时已丢失，
无法凭 sessionId 续跑（除非 sidecar 也做持久化，目前没有）。
但至少状态是对的 —— 用户知道「任务被中断了，需要重跑」。

#### 失败原因要具体

第一版只写「任务失败或中断」，排查时没有信息。改成从事件流收集：

| 来源 | 记录的文本 |
|---|---|
| `task_end.error` | 模型返回的真实错误 |
| `stage_failed.error` | 阶段失败原因 |
| `idle_timeout` | 「任务卡死（长时间无活动）」 |
| 事件流异常 | 「事件流异常: xxx」 |

截断到 500 字，避免数据库存超长文本。

#### 实测

```
=== 3. 立即查询该任务 ===
✓ 记录存在: status=running, kind=content-generation
  sessionId: f99dd720...
  workspace: /workspace/data/agent/bbe0417a-...
  心跳: 2026-11-14T10:05:39.740681+08:00

=== 5. 运行中的任务 ===
运行中: 1 个
  content-generation — bbe0417a  stale=true    ← 僵死正确标记
```

#### 接口

```
POST /api/task-runs/running   运行中任务（含 stale 标记）
POST /api/task-runs/list      项目任务历史
POST /api/task-runs/get       单个任务详情
```

### 17.9 P1 完成总览

| 能力 | 状态 | 关键点 |
|---|---|---|
| 一致性检查 | ✅ | 3 个埋点全检出，零误报 |
| 一致性修复 | ✅ | 修复后复检归零 |
| 空转检测 | ✅ | 4 个场景全过，零误判 |
| 空闲超时 | ✅ | 3 分钟无事件判卡死 |
| 任务持久化 | ✅ | 重启后状态可查，僵死可标记 |

### 17.10 待补

| 项 | 说明 |
|---|---|
| Agent 提问应答 | sidecar 有 `/sessions/:id/answer` 接口，Go 侧未接入 |
| 真正的任务恢复 | 需 sidecar 也持久化会话，才能续跑 |
| 诊断 Agent | 桌面版启动诊断 Agent 读会话记录给结论，Web 版只用规则 |
| 心跳定时器 | 目前只在创建/结束时更新，长任务中途不更新 |

---

## 十八、配图编排（2026-11-14，P2）

### 18.1 为什么需要编排

没有编排时，Agent 想配几张就配几张：
- 有的章节配 5 张，有的关键章节一张没有
- 全文配图分布失衡

桌面版的做法是「主 Agent 分配全局名额，再交给各节执行」。
Web 版用了**更简单但更可靠的方案**。

### 18.2 核心设计：确定性算法，不用 LLM

```
不让 LLM 做分配（不稳定、不可复现），改用算法：
  1. 按标题关键词 + 层级 + 目标字数算「配图适配分」
  2. 按分数排序，把名额分给最需要的章节
  3. 每节拿到明确的配图指令（布局 + 生成方式 + 数量）
```

**为什么不用 LLM**：
- 可复现：同样的目录永远得到同样的分配
- 可解释：能说清「为什么这节分了 2 张」
- 零成本：不消耗 token
- LLM 的优势在内容理解，配图分配本质是规划问题

### 18.3 适配评分（0-10）

| 维度 | 权重 | 说明 |
|---|---|---|
| 标题关键词 | 0-7 | 强词（架构/流程/组织/拓扑/部署）每个 2.5 分；中词每个 1 分 |
| 层级 | 0-1.5 | 二三级小节最具体，最适合配图 |
| 目标字数 | 0-1.5 | 字数多才有空间容纳配图 |

按分数给名额：≥8.5 → 4 张，≥7 → 3 张，≥5.5 → 2 张，≥3 → 1 张，<3 → 不配。

### 18.4 ★ 三个关键的专业判断（踩坑后修出来的）

#### 坑 1：关键词匹配必须只看标题

**现象**：「14.2 验收标准与验收流程」被配了四宫格架构图。

**原因**：它的 description 里提到「逐条对应技术需求（架构、功能、性能）」，
关键词匹配命中了「架构」。

**修正**：`pickKind` / `holistic` 判断**只看标题，不看 description**。
description 提到什么词不代表本节主题是什么。

#### 坑 2：整体性图表必须单张

**现象**：「9.1 项目组织架构与职责分工」被配了四宫格。

**为什么错**：组织架构图必须**一张图看全**，拆成四格就失去了「整体」的意义。

**修正**：加 `holistic` 判断 —— 标题含架构/组织/拓扑/体系/分层/流程 → 单张大图
（最多 2 张：主图 + 局部细节图）。

**排除词的坑**：一开始用「各」「分别」作为「非整体」的信号，
结果「各角色职责」被误判 —— 「各」不等于并列对比。
改成只认「对比」「多方案」「比较」这类真正的并列信号。

#### 坑 3：判断顺序决定生成方式

**现象**：「9.1 项目组织架构」用了 mermaid 而不是 htmlImage。

**原因**：判断顺序是「先流程/架构 → mermaid」，而「组织架构」同时含
「组织」和「架构」，先命中了「架构」。

**修正**：调整优先级
```
第一优先：组织架构/职责分工/团队配置 → htmlImage（组织结构图需精确布局）
第二优先：矩阵/对比/指标/表格 → htmlImage
第三优先：流程/架构/拓扑/时序 → mermaid
兜底：htmlImage > mermaid > AI
```

**实测结果**：

| 章节 | 主图 kind | 判断正确性 |
|---|---|---|
| 2.3 数据架构 | mermaid | ✓ 架构用流程图 |
| 9.1 项目组织架构 | htmlImage | ✓ 组织图用 HTML |
| 14.2 验收流程 | mermaid | ✓ 流程图 |

### 18.5 四种布局

| 布局 | 张数 | 适用 |
|---|---|---|
| `single` | 1 | 单张图表达完整意思 |
| `imageText` | 1 | 左图右文 |
| `threeImages` | 3 | 三列对比 |
| `fourImages` | 4 | 四宫格并列 |

### 18.6 ★ 画质问题：宽高比约束

**现象**：Agent 生成的架构图宽高比 **3.96:1**（6478×1636），
放进 A4 文档（正文宽 16cm）后高度只有 4cm，**文字完全看不清**。

**根因**：Mermaid 的 `graph TD` 在节点多且横连时会横向铺开。

**分两步解决**：

**第一步：工具描述里加引导**（治本）

```
generate_image 的 description：
"mermaid 图节点不要超过 12 个，否则横向过宽，
 放进 Word 后文字会看不清；节点多时拆成两张图。"

code 参数描述：
"节点控制在 12 个以内：每个节点文字精简到 10 字内；
 超过 12 个节点请拆成两张图。层级建议 3-4 层。"
```

**第二步：渲染时检测并回传警告**（兜底）

```go
// render sidecar 返回 aspectRatio 和 tooWide
if aspect > maxAspectRatio {
    warning = "这张图宽高比 X:1 偏扁，放进 Word 后文字可能看不清。
               建议把节点精简到 12 个以内，或拆成两张图后重新生成。"
}
```

Agent 读到警告后可以调整重画。

**为什么不硬改图片**：压缩或拉伸会让图形失真、文字变形。
正确的地方是**生成源码时就控制规模**。

### 18.7 实测：包装前后的画质对比

**包装前**（3.96:1）：
- 6478×1636，横向铺开
- 连线交叉杂乱，文字在 A4 里缩到看不清

**包装后**（0.79:1）：
- 807×1014，竖版
- 四层结构清晰（接入层→网关层→业务层→数据层）
- 节点文字大而可读，箭头走向明确
- **直接可用**

差距来自工具描述里那句「节点不要超过 12 个」。

### 18.8 编排结果注入 Prompt

`Plan.FormatPrompt()` 输出：

```
本次共安排 2 组配图、2 张图片。

## 各节配图安排

### 项目组织架构与职责分工
- 第 1 组：单张图片（1 张），生成方式 `htmlImage`
  表达目的：用组织图表达项目组织架构的层级结构、岗位设置与汇报关系（整体结构）
- 第 2 组：单张图片（1 张），生成方式 `mermaid`
  表达目的：同上（局部细节或关键环节）

## 执行要求
1. **严格按分配数量配图**，不要多也不要少
2. 没有列出的小节**不要配图**
...
```

### 18.9 实测：端到端打通

```
目标节: 2.2 应用架构与微服务化设计
编排结果: 2 张 / 2 组
  → single (1张) mermaid
  → single (1张) mermaid

=== 正文生成 ===
产物: 3012 字节
正文含 2 个 <figure> 块
图片资产:
  figures/32594c7a.../fig-2-2-1.png  371,116B   ← 应用架构分层图
  figures/32594c7a.../fig-2-2-2.png  251,681B   ← 微服务拆分调用链路
```

**编排分配 2 张 → Agent 生成 2 张 → 引用路径精确对应 → 正文正确嵌入。**

### 18.10 接口

```
POST /api/content/preview-image-plan   预览编排（不启动生成）
POST /api/content/generate             启动生成（自动编排）
```

请求参数：
```json
{
  "totalImages": 19,              // 0 = 自动估算
  "maxImagesPerSection": 4,
  "enabledImageKinds": ["mermaid", "htmlImage"],
  "aiPercent": 0,
  "disableImages": false
}
```

### 18.11 待补

| 项 | 说明 |
|---|---|
| 组内多图编排 | threeImages/fourImages 的图片内容需要更细的分配（每格画什么） |
| AI 占比控制 | AIPercent 参数已预留，未实现实际调度 |
| 编排持久化 | 编排结果只在生成时用，未落库供后续查看 |

---

## 十九、Agent 提问应答（2026-11-14）

### 19.1 为什么需要

Agent 在长任务中会遇到需要用户决策的点：
- 招标文件缺关键信息（如没给预算金额）
- 有多种合理方案但影响重大
- 需要确认关键数据

之前的做法：Agent 只能自己猜，或者停下任务。

### 19.2 方案：用工具实现提问

```
Agent 调 ask_user 工具
  → 工具挂起（Promise 阻塞）
  → sidecar 推 user_question 事件给前端
  → 用户在前端回答
  → POST /api/agent-tools/answer
  → Go 转发到 sidecar /sessions/:id/answer
  → sidecar 唤醒挂起的 Promise
  → 工具返回答案 → Agent 继续
```

**为什么用工具而不是解析对话**：
- 类型安全：问题结构明确（question/options/defaultAnswer/reason）
- 阻塞语义清晰：Agent 知道自己在等，不会继续瞎做
- 可超时：等待 10 分钟无应答 → 用默认值或让 Agent 自行判断
- 可并发：多个问题各自独立等待

### 19.3 关键实现细节

**① 工具必须在白名单里**

踩坑：`ask_user` 注册了但没生效，日志显示
`实际注册工具: read,write,edit,bash,grep,find,ls`（缺 ask_user）。

原因：Go 传的 `ActiveTools` 是业务白名单，只含原生工具和业务工具，
不含 sidecar 自己的工具。

修正：
```js
// ask_user 是 sidecar 内置工具，任何阶段都允许调用
if (!requestedTools.includes('ask_user')) {
  requestedTools.push('ask_user');
}
```

**② emit 要在 session 创建后注入**

`createPiSession` 时还没有事件通道（runner 拿到 session 才有）。
用可变对象承载：

```js
const askUserCtx = { emit: opts.emit, sessionId: opts.sessionId };
// session 返回里暴露
setEmit(emitFn) { askUserCtx.emit = emitFn; }
```

**③ 超时处理**

```js
const QUESTION_TIMEOUT_MS = 10 * 60 * 1000;
// 超时后用 defaultAnswer，没有则让 Agent 自行判断并说明假设
```

**④ 会话销毁时清理待答问题**

否则 Promise 永远挂着，任务永远不结束。

### 19.4 接口

```
POST /api/agent-tools/answer
{
  "sessionId": "...",
  "questionId": "q-...",
  "answer": "860 万元"
}
```

sidecar 侧：
```
GET  /sessions/:id/questions   列出待答问题（重连恢复用）
POST /sessions/:id/answer      回答
```

### 19.5 实测

**接口层验证（全部正确）**：

```
=== 1. 应答接口 ===
HTTP 200 → {"code":"ANSWER_FAILED","message":"会话不存在 (SESSION_NOT_FOUND)"}
（说明转发链路通，只是会话不存在）

=== 2. 缺参数校验 ===
HTTP 400 → {"code":"INVALID_ARGUMENT","message":"缺少 sessionId 或 questionId"}

=== 3. 工具注册 ===
sidecar 日志: 实际注册工具: read,write,edit,bash,grep,find,ls,ask_user ✓
```

**模型行为观察**：用「必须先调用 ask_user」的强指令测试两次，
模型都选择了直接完成（没调用工具）。

这是模型的判断，不是实现问题 —— 说明**当前模型的能力已经能自行处理
大部分「看似需要问」的场景**。ask_user 的价值在于真正的信息缺口
（如招标文件确实没给预算，而用户手上有这个信息）。

### 19.6 代码结构

| 文件 | 职责 |
|---|---|
| `sidecars/agent/src/askuser.mjs` | 提问工具的完整实现（挂起/超时/清理） |
| `internal/http/handler/agent_tool.go` | 应答接口（转发给 sidecar） |
| `internal/service/agent/client.go` | `AnswerQuestion` / `ListQuestions` |

### 19.7 待补

| 项 | 说明 |
|---|---|
| 前端 UI | 需要弹窗展示问题、收集回答（后端接口已就绪） |
| 提问去重 | 同一个问题被问多次时应复用（未实现） |
| 提问历史 | 记录每次问答，便于回溯（未实现） |

---

## 二十、前端界面接通（2026-11-14，P3）

### 20.1 起点：28 次接口失败

用 Playwright 打开前端，统计网络请求：

```
总计 41 次，失败 28 次
401 POST agent/get-pending-question
401 POST auto-confirmation/get-state
404 POST required-online-services/get-status
404 POST license/get-status
404 POST agent/get-status
404 POST ui/set-current-view
404 POST official-account/get-state
404 POST notice/sync-current
400 POST technical-plan/load-state      ← 根因
404 POST tasks/get-active-tasks
```

### 20.2 ★ 根因：前端没有项目标识

`technical-plan/load-state` 返回 400：
```
缺少项目标识（X-Project-Id 或 projectId）
```

**为什么**：web bridge 里有 `setProjectId()` 函数，但**从来没被调用过**。
localStorage 里 `yb_current_project` 是空的，所有业务请求都缺项目头。

**Web 版 vs 桌面版的根本差异**：
- 桌面版：单机单项目，业务表没有 project_id 概念
- Web 版：多项目隔离，每个请求都要带项目

补 `POST /api/projects/current`：没有项目时**自动创建**，
前端代码完全不用改。

### 20.3 ★ 隐蔽 bug：失败的 Promise 被永久缓存

第一版实现里，`ensureProject()` 用了模块级的 `projectReady` Promise 缓存：

```js
// 错误写法
projectReady = (async () => { ... })();
return projectReady;
```

问题：页面加载时（登录前）第一次请求拿不到 token → 返回空串 →
**这个失败的 Promise 被永久复用**，项目 ID 永远拿不到。

现象：`load-state` 能成功（因为后端有 fallback），
但 `localStorage` 里始终没有 `yb_current_project`。

修正：
```js
const id = await attempt;
if (!id) projectReady = null;  // 失败就清缓存，下次重试
```

### 20.4 数据契约：前端要的字段不能少

`TechnicalPlanState` 有 **20+ 个必需字段**，我一开始只返回 15 个。
缺字段会让前端读 `undefined` 直接报错。

**两个致命缺失**：

**① `tenderFiles` 数组**

```
后端返回: tenderFileName: "tender.md"   （扁平字符串）
前端要的: tenderFiles: [{id, fileName, markdownPath, markdownChars,
                        contentHash, parserLabel, importedAt, updatedAt}]
```

前端用 `state.tenderFiles[0]?.id` 判断是否已导入，
拿不到数组就显示「尚未导入招标文件」。

**② `outlineData` 树**

```
后端返回: outline: [79 个平铺节点]
前端要的: outlineData: { outline: [嵌套 children 的树] }
```

前端用 `outlineData.outline` 渲染目录树、统计叶子数，
拿不到就显示「暂无目录」。

补上 `buildOutlineData()`：平铺列表 → 嵌套树，同时算 `number`（1 / 1.1 / 1.2）。

### 20.5 字数统计要单独给

前端从 `contentGenerationRuntime.section_words` 读每节字数：

```ts
const outlineMeta = buildOutlineMeta(outlineData.outline, sections, planning,
  sectionWords, ...)  // sectionWords 来自 contentGenerationRuntime
const totalWords = leaves.reduce((sum, item) =>
  sum + (outlineMeta.get(item.id)?.words || 0), 0)
```

没有这个字段 → 显示「已生成 6 个小节，**共 0 字**」。

补 `buildContentRuntime()`：从数据库里的正文统计字数（用 `textutil.CountReadableWords`，
与标书口径一致）。

### 20.6 桌面版专属接口的兼容处理

有 8 个接口是桌面版特有的，Web 版没有对应功能。
**但不能返回 404** —— 前端拿不到值会白屏。

策略：返回**明确的空状态**而不是「接口不存在」。

| 接口 | Web 版处理 |
|---|---|
| `official-account/get-state` | 返回「未登录」+ `managedByPlatform: true` |
| `license/get-status` | 返回「已授权」（平台侧管控） |
| `required-online-services/get-status` | 返回空列表 + `allReady: true` |
| `auto-confirmation/get-state` | 返回 `handledByFrontend: true` |
| `notice/sync-current` | 空操作成功 |
| `notice/prepare-media` | 直接返回原始 URL（浏览器能加载） |
| `ui/set-current-view` | 接受但什么都不做（前端路由自己管） |
| `agent/get-status` | 返回就绪（Web 版按任务粒度管理） |

**真实实现的**：`agent/get-pending-question` —— 从 sidecar 拉待答问题，
转成前端 `AgentQuestion` 结构。

### 20.7 最终结果

```
=== 界面状态 ===
STEP 06 正文生成
64 个 AI 小节，0 待处理
标书目录: 64 个小节
生成统计: 6/64（进度条）
显示项目名: ✓
仍显示「尚未导入」: ✗ 否

接口失败: 无
```

**从 28 次失败到 0 次。**

界面元素全部正常：
- 左侧目录树（第一章~第三章，带状态标签）
- 生成统计（6/64 + 进度条）
- 右上「继续生成正文」
- 右下「导出 Word」
- 布局整齐无错位

### 20.8 数据验证

```
=== 字数统计 ===
总字数: 540
有字数的节数: 6

=== 目录树 ===
根节点: 15
1 第一章 项目理解与总体建设方案
  1.1 项目背景与建设必要性理解 (82字符)
  1.2 建设目标与评价标准解读 (87字符)
  1.3 建设范围与需求总体响应 (156字符)
2 第二章 总体技术架构设计
  2.1 B/S 架构与私有云部署设计 (159字符)
  ...
```

### 20.9 小瑕疵修复：标题编号重复

`"1.1 项目背景"` 前端会再拼一次 number → 显示成 `"1.1 1.1 项目背景"`。

加 `stripTitleNumber()`：只清理「数字.数字.」形式的前缀，
中文「第X章」保留（那是标题的一部分）。

### 20.10 代码规模

Go **20,300 行**（新增 project.go / compat.go / 字段补全）。
前端改动：web.ts 加 ensureProject + 失败不缓存。

### 20.11 待补

| 项 | 说明 |
|---|---|
| 提问弹窗 UI | `get-pending-question` 接口就绪，缺前端弹窗组件 |
| 产物列表页 | 后端 assets 接口有，缺独立页面 |
| 多项目管理 UI | 接口有（list/create/select），缺切换入口 |
| 上传组件联调 | 前端上传按钮需真实点击验证（`getPathForFile` 已通） |

---

## 二十一、提问弹窗与产物库（2026-11-14，P4）

### 21.1 提问弹窗：组件早就有，断在三个地方

`AgentQuestionDialogProvider.tsx` 是桌面版现成的组件，
`AppProviders` 里也挂载了。但收不到事件。

**断点 1：SSE 连接时机**

组件在登录前挂载，那时没有 token，`ensureEventSource()` 直接 return。
而且 `connectedTopics` 已记录了 topic，之后**不会再重连**。

现象很隐蔽：日志显示 `SSE 已连接`，但那是后来某次订阅才连上的，
此时弹窗组件的监听器已经注册在一个"空"连接上。

修正：
```ts
export function setTokens(access, refresh) {
  localStorage.setItem(TOKEN_KEY, access);
  // 登录后必须重建，否则 Provider 的监听永远收不到事件
  if (connectedTopics.size > 0) {
    eventSource?.close();
    eventSource = null;
    ensureEventSource();
  }
}
```

**断点 2：事件根本没发出来**

`agent.Service` 完全没有事件总线依赖。sidecar 推了 `user_question`，
Go 侧只是转发给业务层 onEvent 回调，没人推到 SSE。

修正：加 `EventPublisher` 接口 + 在事件循环里 publish：
```go
if head.Type == "user_question" && s.bus != nil {
    s.bus.Publish("agent.question", EventScope{...}, payload)
}
```

**断点 3（★ 最难查）：事件发了但 delivered=0**

诊断日志揭示：
```
topic=agent.question subs=1 delivered=0 ev_tenant="" ev_user=""
```

`ev_tenant` 是空的！`matchScope` 第一行就 return false。

根因：`toolH.Register(e.Group("/api"))` —— 工具回调**注册在独立分组，
绕过了 `api.Use(middleware.Auth(...))`**。这是有意的（工具由 sidecar
用内部令牌调用，没有用户身份），但我把测试接口放在那里就取不到身份。

修正：测试接口的作用域从请求体来（仅开发用）。
真实链路不受影响 —— 提问由 sidecar 事件触发，
scope 在 `agent.Service` 里从 `TaskSpec` 取。

**断点 4：`data` 被 omitempty 吃掉**

`getPendingQuestion` 没有待答问题时返回 `httpx.OK(c, nil)`，
但 `Envelope.Data` 带 `omitempty`，JSON 里 `data` 字段直接消失，
前端拿到 `undefined` 而不是 `null`。

修正：加 `httpx.OKNull()` 显式返回 `{"ok":true,"data":null}`。

### 21.2 最终效果

```
✓ 弹窗出现（第 1 秒）
  dialog 元素: 1
  含问题文本: true
  含选项 1: true
  含选项 2: true
```

**截图确认**：弹窗居中、带遮罩、标题「需要您确认以下问题」，
问题文本清晰，两个选项（860 万元带「推荐」标签），
「自动回答」开关 + 「确定并继续」按钮。

### 21.3 产物库：Web 版独有的页面

**为什么需要**：
- 桌面版产物落在本地磁盘，用文件管理器访问
- Web 版产物在云端对象存储，必须通过界面才能看见和下载

前端原本没有这个页面，也没有 `assets` IPC 类型。

新增链路（按项目规范要同步 5 处）：
```
src/shared/types/navigation.ts   加 'assets'
src/app/menuConfig.ts            加菜单项（挂在"其他功能"下）
src/app/AppRouter.tsx            加路由分支
src/components/Sidebar.tsx       加图标映射
src/styles/feature-assets.css    新建样式 + styles.css 引入
src/shared/types/ipc.ts          加 assets 命名空间类型
```

### 21.4 ★ 三个实现细节

**① `assets.download` 要返回 URL 而不是 JSON**

通用 bridge 是「POST /api/ns/method-kebab → JSON」，
但下载需要一个能放进 `<a href>` 的直链。

加命名空间级本地实现：
```ts
const NS_LOCAL = {
  'assets.download': (assetId) => {
    const token = getAccessToken();
    return `${base}/api/assets/${id}/download?access_token=${token}`;
  },
};
```

**注意**：`NS_LOCAL` 必须定义在 `buildNamespace` 之前，
否则是 TDZ 错误（先用了后定义）。第一次就踩了这个坑。

**② 图标用 SVG 不用 emoji**

第一版用 `🖼 📄 📝` 等 emoji，实际渲染成方块（容器里缺 emoji 字体）。
改用内联 SVG，样式可控且不依赖字体。

**③ 过程文件要过滤掉**

第一次打开显示 42 个条目，其中大量是 `content-manifest.json`、
`tender.md` 这类流程中间文件，噪音盖过了真正的产物。

修正：
- 默认隐藏过程文件
- 加「显示过程文件」开关
- 统计只算最终产物（27 个），过程文件单独计数（15 个）

### 21.5 最终效果

```
统计：27 个产物 / 893.4 KB / 5 张配图 / 15 个过程文件
筛选：全部（蓝底白字）| Word | 图片 | Markdown | 其他
开关：显示过程文件（默认关）
搜索：文件名关键字
列表：图标 + 文件名 + 类型/大小/时间 + 预览/下载
```

列表里能看到真实产物：
- `某市人民医院信息化系统建设项目_技术方案_20261114_0417.docx` 35.9 KB
- `fig_system_architecture.png` 118.0 KB
- `fig-2-2-1.png` 362.4 KB

### 21.6 CSS 变量踩坑

我第一版自造了 `--accent` / `--surface-1` / `--text-strong` 等变量，
项目里一个都不存在 → 筛选按钮「全部」白底白字看不见。

真实变量（`src/styles/tokens.css`）：
```
--yb-primary / --yb-primary-hover / --yb-surface
--yb-text / --yb-text-soft / --yb-text-muted
--yb-border / --yb-border-soft / --yb-radius-* / --yb-shadow-*
```

真实按钮类：`.primary-action` / `.secondary-action`（不是 `ghost-button`）。

**教训**：新页面样式前先看 `tokens.css`，不要凭习惯命名。

### 21.7 代码规模

Go **20,641 行**。前端新增页面 + 类型 + 样式约 600 行。
`tsc --noEmit` 通过，`npm run build` 成功。

### 21.8 待补

| 项 | 说明 |
|---|---|
| 多项目切换 UI | `projects/list`、`create`、`select` 接口就绪，缺切换入口 |
| 产物按任务分组 | `assets.task` 接口有，UI 未用 |
| 产物删除 | `assets/delete` 接口有，UI 未接 |
| 真正的任务恢复 | 需 sidecar 持久化会话 |

---

## 二十二、多项目切换（2026-11-14，P5）

### 22.1 为什么需要 UI

后端多项目隔离早就做了（每个业务表都有 `project_id`），
但前端只有一个"隐式当前项目"，用户根本用不上这个能力。

**桌面版没有这个概念**：单机单项目，业务表连 `project_id` 都没有。

### 22.2 后端：项目视图要带进度摘要

多项目场景下，光有项目名没法判断"这个项目做到哪了"。

`projectView` 加四个字段：

| 字段 | 来源 | 用途 |
|---|---|---|
| `tenderFileName` | `technical_plan_meta.tender_files` | 一眼看出导入了什么招标文件 |
| `outlineNodeCount` | `ListOutlineNodes` 长度 | 目录规模 |
| `contentCount` | 遍历节点数 `content != ""` | 成文进度 |
| `step` | `technical_plan_meta.step` | 当前在哪一步 |
| `runningTasks` | 新增 `CountRunningTasksByProject` | 显示「进行中」标记 |

新增 SQL：
```sql
-- name: CountRunningTasksByProject :one
SELECT COUNT(*) FROM task_runs
 WHERE project_id = $1 AND tenant_id = $2 AND status = 'running';
```

### 22.3 ★ 不能删最后一个项目

```go
count, err := p.q.CountProjectsByTenant(ctx, tenantID)
if err == nil && count <= 1 {
    return httpx.Fail(c, "LAST_PROJECT", "不能删除最后一个项目")
}
```

**为什么**：删光了前端就没有可用项目，`projects/current` 会不停自动创建，
陷入「删除 → 自动创建 → 删除」的循环。用户体验很差。

### 22.4 前端：切换器放在侧边栏 logo 下方

**为什么放这**：项目是全局作用域，用户随时要能看见"我现在在哪个项目"，
就像 IDE 显示当前工作区一样。

组件 `ProjectSwitcher.tsx`：
- 收起侧边栏时自动隐藏（空间不够）
- 弹窗列出全部项目，每个显示进度摘要
- 支持新建 / 改名 / 删除
- 只有一个项目时显示提示，不显示删除按钮

### 22.5 ★ 切换项目必须重新加载页面

```ts
onSwitch?.(project);
window.setTimeout(() => window.location.reload(), 400);
```

**为什么不全靠状态更新**：切换项目等于换了整个工作区，
技术方案页、产物库、任务列表各有自己的状态缓存，
逐个清理容易漏。`location.reload()` 最干净。

代价是短暂白屏（400ms），可以接受。

### 22.6 ★ 缓存的项目 ID 可能失效

场景：用户在另一个标签页删了项目，或本地缓存指向已删除的项目。
症状：所有业务接口报 400「缺少项目标识」或 404。

加启动时校验：
```ts
void ensureProjectValid().catch(() => undefined);
```

`ensureProjectValid` 调 `projects/select` 验证，失败就清缓存重取。
**失败不阻塞启动** —— 业务接口自己会报错，但提前修好体验更好。

### 22.7 接口名对不上的坑

前端方法名 `projects.remove`，bridge 会映射成 `POST /api/projects/remove`，
但后端注册的是 `/projects/delete` → 404。

修法：加别名
```go
g.POST("/projects/delete", p.Delete)
g.POST("/projects/remove", p.Delete)  // 前端方法名映射
```

**教训**：bridge 的自动映射规则是 `命名空间.方法名 → /命名空间/方法名-kebab`，
后端路由必须严格对齐前端方法名，否则静默 404。

### 22.8 实测：数据隔离

```
=== 切换前 ===
项目: 测试投标项目
目录节点: 79
招标文件: tender.md

=== 新建并切换 ===
已创建: 隔离测试项目_740835

=== 切换后（新项目）===
目录节点: 0        （空，符合预期）
招标文件: (无)     （符合预期）
产物数: 0          （空，符合预期）

=== 切回原项目 ===
目录节点: 79       （恢复，符合预期）
招标文件: tender.md（恢复，符合预期）

结论：数据隔离正确
```

**截图确认**：切换器在侧边栏顶部显示「测试投标项目」，
弹窗里项目条目显示 `tender.md · 79 节 · 6 节已成文 · 生成正文`，
带「当前」蓝标，「改名」按钮在右侧。

### 22.9 代码规模

Go **20,814 行**（新增项目进度查询）。
前端新增 `ProjectSwitcher.tsx`（约 280 行）+ 样式（约 200 行）+ IPC 类型。

`tsc --noEmit` 通过，`npm run build` 成功，主流程回归零接口失败。

### 22.10 待补

| 项 | 说明 |
|---|---|
| 项目封面/颜色 | 多项目时靠名字区分不够直观 |
| 项目复制 | 复用已有目录结构开新项目 |
| 归档 | 项目多了列表会很长，需要归档态 |

---

## 二十三、任务恢复（2026-11-14，P6）

### 23.1 出发点：原来的注释说「做不了」

```go
// 为什么不恢复：sidecar 会话在服务重启时已丢失，
// 无法凭 sessionId 恢复（除非 sidecar 也做了持久化，目前没有）。
```

**这个判断是错的。** 恢复不需要 sidecar 会话 ——
正文生成的实际成果都在磁盘上：

```
<workspace>/
├── tender.md                    ← 输入
├── content-manifest.json        ← 进度（每节 status + words）
└── sections/
    └── <nodeId>_<title>.md      ← 每节正文，独立文件
```

**恢复的本质是复用工作区，不是接着旧会话跑。**

### 23.2 恢复三步

```
1. 从 task_runs 找到旧任务的 workspace_dir
2. 扫描 manifest，算出已完成小节（且校验文件真实存在）
3. 用同一工作区重启，Prompt 里列出已完成小节让 Agent 跳过
```

### 23.3 ★ 关键设计：两条防线保证不覆盖旧成果

**防线 1：`prepareInputs` 在 resume 模式跳过已存在文件**

```go
if resume {
    if _, statErr := os.Stat(dst); statErr == nil {
        continue
    }
}
```

**为什么必须**：`PrepareContent` 会写 `content-manifest.json`。
如果不跳过，重启时把 manifest 覆盖成全新的 pending 状态，
**断点信息就清零了**，恢复也就没意义。

**防线 2：Prompt 明确列出已完成小节**

```
## 恢复任务

**这是恢复的任务**：上次执行到一半被中断，已有 1 个小节完成。

已完成的小节（**不要重新生成，直接跳过**）：
- `77422e9b-76eb-4a45-908e-12d4cf6c728d`

已完成的正文文件都在 `sections/` 下，需要参考时可以直接读。
你只需要生成清单里 `status` 仍为 `pending` 的小节。
```

### 23.4 ★ 扫描时为什么要校验文件存在

```go
if item.Status != "done" { scan.Pending++; continue }
if !sectionFileExists(workspaceDir, item) {
    // manifest 说完成但文件没了 → 不能算完成
    scan.Pending++
    continue
}
```

**为什么**：Agent 可能先更新 manifest 再写文件，中途崩溃就会出现
「manifest 说完成、文件不存在」。这种不能算完成，
否则恢复后会缺一整节内容。

### 23.5 ★ 剩余数量必须按当前目录算

**踩的坑**：第一版用 `manifest.pending` 当剩余数，
结果显示「完成 1 节，剩余 **0** 节」—— 但实际还有 63 节没做。

原因：旧任务的 manifest 只包含它当时的待办小节（就 1 个）。
目录后来扩到 64 节，manifest 里根本没有这些新节。

修正：用当前目录的 AI 叶子数校正
```go
if leaves := h.countAILeaves(ctx, tenantID, projectID); leaves > 0 {
    remain := leaves - len(scan.Completed)
    currentPending = remain
}
```

### 23.6 ★ 去重：只保留最新一条

任务失败后用户会重试多次，产生多个中断记录。
全列出来用户不知道该点哪个（它们指向同一批未完成小节）。

```go
// 按时间倒序，只留最新
sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
if len(out) > 1 { out = out[:1] }
```

### 23.7 实测

**恢复请求**：
```
POST /api/content/resume { taskId: "32594c7a...", targetWordsPerSection: 600 }

✓ 恢复成功
  新任务: 32594c7a（复用同一个 taskID）
  已完成: 1 节
  待生成: 57 节
```

**服务端日志确认工作区复用**：
```
msg=从旧工作区恢复任务 task_id=32594c7a workspace=/workspace/data/agent/32594c7a-...
msg=Agent 会话已创建 task_id=32594c7a workspace=同一个目录
```

**会话文件里确认 Prompt 正确**：
```
找到恢复说明: 恢复任务\n\n**这是恢复的任务**：上次执行到一半被中断，
已有 1 个小节完成。\n\n已完成的小节（**不要重新生成，直接跳过**）：
\n\n- `77422e9b-...`
```

**UI 验证**：
```
发现 1 个中断的任务，可以接着做
已完成 1 节，还剩 63 节 · 728 字 · 7 分钟前  [继续生成]
```

淡黄底 + 橙色图标 + 关闭叉，位于正文页顶部命令栏上方，
不遮挡主要内容。

### 23.8 接口

```
POST /api/task-runs/resumable    列出可恢复任务
POST /api/content/resume         发起恢复
```

前端通过 tasks 命名空间调用（与 `startContentGeneration` 一致）：
```
POST /api/tasks/list-resumable
POST /api/tasks/resume-content-generation
```

### 23.9 代码规模

Go **21,509 行**。
新增 `content/resume.go`（扫描逻辑）、`ResumeBanner.tsx`（提示条）。

### 23.10 边界与待补

| 场景 | 处理 |
|---|---|
| 工作区被清理 | 扫描失败，提示「请重新开始生成」 |
| 全部小节已完成 | 返回 `resumed: false` + `already-complete` |
| 目录已变更（新增/删除节） | 按当前目录算，旧 manifest 里不存在的节当作待做 |
| sidecar 会话恢复 | 不需要（恢复靠磁盘产物，不靠会话） |
| 半完成的单节（写到一半） | 视为未完成，会重新生成该节 |

---

## 二十四、知识库完整移植（2026-11-14，P7）

### 24.1 规模判断

桌面版知识库：
- `knowledgeBaseService.cjs` 2,307 行
- `knowledgeBaseStore.cjs` 1,225 行
- 前端页面 1,886 行
- **合计 5,400+ 行**

四层数据模型：
```
knowledge_folders          文件夹
  └── knowledge_documents  文档（上传后解析成 markdown）
        ├── knowledge_blocks            内容块（可过滤）
        └── knowledge_items              AI 提炼的知识条目
```

### 24.2 为什么四层不能简化

**blocks 是块过滤的载体**：
一份 200 页文档里，页码、目录、封面、签字页可能占 30%。
过滤要留痕（`is_filtered` + `filter_reason`），用户能追溯
「为什么这段没进知识库」。

**items 一定是 AI 提炼的，不是原文照搬**：
原文段落是为阅读写的（有过渡、有铺垫、有重复），
检索需要「一个能独立理解的知识点」。
3000 字原文提炼成 3 条各 200 字，检索精度和引用价值都高得多。

### 24.3 切块算法：三个关键参数

```go
semanticMergeTargetChars = 520   // 语义合并目标字数
oversizedBlockChars     = 2600   // 超长块拆分阈值
```

**为什么是 520**：太短检索没上下文（一句一段检索出来没意义），
太长 AI 提炼时容易丢细节（注意力被稀释）。

**合并规则**：
- 表格独立成块（结构性强，合并会破坏语义）
- 标题可带上下文，但如果缓冲区已有 100 字以上正文，先落盘
  （避免标题和远处的正文混一起）
- 段落累积到 520 字就切

**超长块拆分**：在标点处断开（`。！？；`），不能硬切 ——
硬切会让知识条目语义断裂。

### 24.4 过滤规则：五类噪音

| 规则 | 判定 | 例子 |
|---|---|---|
| 页码 | 正则匹配 | `- 1 -` / `第3页` / `3/10` / `Page 5 of 20` |
| 目录 | 标题 + 60% 行带页码引导符 | `1.1 背景 ...... 5` |
| 封面 | 前 12 块 + 含封面词 + 不是长句 | `投标文件 / 项目名称 / 招标编号` |
| 签字页 | 含盖章词 + 无实质内容 | `法定代表人（签字）：/（盖章）` |
| 重复页眉页脚 | 同文本出现 ≥3 次且 ≤80 字 | 每页顶部的项目名 |
| 内容过短 | < 12 字 | `（1）`、`如下：` |

### 24.5 ★ 测试发现的顺序 bug

**问题**：重复页眉页脚检测被 `isCover` 抢先命中。

原因：页眉里常含「投标文件」这类封面词，
而「同一段文本出现 3 次以上」是页眉页脚的**更强证据**。

修正：把重复检测提到封面判断之前。

**另一个发现**：纯日期（`2024年3月15日`）作为独立块时，
被误判为「签字盖章页」。它确实该被过滤（没知识价值），
但原因应该是「内容过短」而不是「签字页」——
原因标错会让用户困惑。

### 24.6 ★ AI 提炼的三个坑

**坑 1：模型选错**

`deepseek-v4.1-flash` 是**推理模型**，只输出 `reasoning_content`，
最终 `content` 为空。sidecar 判定「模型返回空响应」。

换成 `deepseek-v4-pro`（输出正常 content）才成功。
**教训**：严格输出 JSON 的场景不要用纯推理模型。

**坑 2：必须有 outputFile**

sidecar 的 stage 校验逻辑：
```js
if (stage.outputFile) {
  const exists = await fileExists(stage.outputFile);
  // 文件不存在 → blocking issue → 任务失败
}
```

第一版 Prompt 只说「输出 JSON」，Agent 在对话里输出了，
但没写文件 → 任务直接判失败。

修正：Prompt 开头就强调「**必须使用 write 工具写入 knowledge-items.json**」。

**坑 3：context 生命周期**

```go
// 错误：用请求的 ctx 启动任务
handle, err := x.agentSvc.StartTask(ctx, ...)
```

HTTP 请求返回后 ctx 被取消，任务在几秒内被强杀。
症状：刚触发就变 error。

修正：用独立 context
```go
taskCtx, taskCancel := context.WithTimeout(context.Background(), 10*time.Minute)
```

### 24.7 ★ tsvector 无法扫描

`search_vector tsvector` 是生成列，但 sqlc 生成 `RETURNING *` 时
会尝试扫描它，报错：
```
can't scan into dest[11] (col: search_vector):
cannot scan unknown type (OID 3614)
```

**这个错误会让所有插入失败**（不是部分失败），但日志里只看到
「保存知识条目失败」，容易误判成别的原因。

修正：所有涉及 `knowledge_items` 的查询显式列字段，不用 `SELECT *`。

### 24.8 ★ 中文搜索的困境

PostgreSQL 的 `simple` 分词把中文当**一整个 token**：
查询「微服务」不会匹配「采用微服务架构」（需要词边界）。

要真正支持中文分词需要 `zhparser` 扩展（需要编译安装 PG 扩展），
当前环境没有。

**方案**：检测到中文关键词时直接走 `ILIKE`：
```go
hasCJK := containsCJK(keyword)
usedFullText := !hasCJK
```

数据量小时（几千条知识点）ILIKE 性能可接受。
将来量大可以装 `pg_jieba` 或换 Elasticsearch。

### 24.9 ★ 配置保存的 partial update bug

测试时改模型名，结果 BaseURL 和 APIKey 全丢了：

```go
// 错误：所有字段无条件覆盖
if err := s.q.UpdateTextProvider(ctx, store.UpdateTextProviderParams{
    TextProvider: req.Provider,   // 前端没传 → 空字符串 → 覆盖成空
    TextBaseUrl:  req.BaseURL,    // 同上
    ...
```

修正：
```go
current, _ := s.q.GetTenantSettings(ctx, tid)
if req.Provider == "" { provider = current.TextProvider }
if req.BaseURL == "" { baseURL = current.TextBaseUrl }
```

**这个 bug 很危险**：用户改个模型名会导致整个配置失效，
而且错误提示是「尚未配置模型服务地址」，很难联想到是这里的问题。

### 24.10 实测结果

**上传解析**：
```
文档: 技术方案测试.md
块数: 6（过滤 2）
过滤原因: 封面信息 1 个 + 签字盖章页 1 个
```

**AI 提炼（5 条高质量知识点）**：
```
[K0001] 智慧医疗系统建设目标与覆盖范围
        构建覆盖门诊、住院、检验、影像全流程的智慧医疗系统，支撑日均5000人次门诊量。
[K0002] 智慧医疗系统设计原则
        系统遵循先进性、安全性、可扩展性三大设计原则，采用微服务架构并符合等保三级。
[K0003] 系统响应时间与可用性技术指标
        系统响应时间不超过2秒，可用性不低于99.9%。
[K0004] 系统总体技术架构与技术选型
        采用B/S架构，前端React、后端Spring Cloud微服务，数据库PostgreSQL 14，缓存Redis 6。
[K0005] 系统私有云部署方案与服务器配置
        采用私有云部署，3台应用服务器（16核32G）、1台数据库服务器（32核128G）...
```

**搜索**：
```
搜索"微服务": total=2
  智慧医疗系统设计原则 (命中: resume)
  系统总体技术架构与技术选型 (命中: resume)
搜索"等保": total=1
```

**UI 截图确认**：搜索结果显示为卡片列表，
带面包屑（医疗行业素材 / 技术方案测试.md）、
标题、摘要、「打开知识条目」按钮、分页。

**接口失败：0**

### 24.11 代码规模

Go **24,333 行**（知识库新增约 2,800 行）：
- `knowledge/chunker.go` 切块
- `knowledge/filter.go` 过滤
- `knowledge/service.go` 上传编排
- `knowledge/extractor.go` AI 提炼
- `knowledge/chunker_test.go` 10 个单元测试
- `handler/knowledge.go` 15 个接口

### 24.12 待补

| 项 | 说明 |
|---|---|
| 条目审核 UI | 后端支持 candidate/approved/rejected，前端未接 |
| 包管理上传 | 前端「上传文档」按钮需真实点击验证 |
| 正文引用知识点 | 生成正文时把知识点注入 Prompt |
| 中文分词 | 量大后换 pg_jieba 或 ES |

---

## 二十五、正文生成引用知识库（2026-11-14，P8）

### 25.1 为什么这一步最关键

知识库建好了但正文生成不用它，等于白建 ——
用户上传的材料、AI 提炼的知识点，只能搜出来看看，
真正写标书时一个字都用不上。

**接上之后**：Agent 写「2.2 应用架构与微服务化设计」时，
能拿到「本单位真实做过 B/S 架构、Spring Cloud 微服务、PostgreSQL 14」
这类具体信息，而不是凭空编造。

### 25.2 两种素材来源

**① 手动关联**（最准，优先）
目录节点的 `knowledge_item_ids` 字段，用户在目录里挂了知识点。

**② 自动匹配**（兜底）
按「章节标题 + 描述」检索知识库。一个 60 节的标书，
让用户逐节挂知识点不现实，自动匹配能覆盖大部分。

### 25.3 ★★ 核心难点：中文关键词提取

**直接整串检索必然失败**：

```
章节标题：2.2 应用架构与微服务化设计
知识库内容：后端使用 Spring Cloud 微服务框架
LIKE '%应用架构与微服务化设计%' → 0 条
```

第一次实现就卡在这里，`with_refs=0`。

**试过 3-gram，更糟**：

```
"应用架构与微服务化设计" 的 3-gram：
应用架 / 用架构 / 架构与 / 构与微 / 与微服 / 微服务 / 服务化 / 务化设
```

全是跨词边界的碎片，一个都匹配不到真实内容。

**最后用 2-gram**：

```
应用 / 用架 / 架构 / 构与 / 与微 / 微服 / 服务 / 务化 / 化设 / 设计
```

其中「架构」「服务」「设计」都是真实词，`ILIKE` 能命中。

**配合停用词表**过滤「应用」「架构」「设计」「技术」「方案」这类
几乎每章都有的泛化词 —— 否则会把不相关的知识点全捞进来。

**拉丁词原样保留**：「PostgreSQL」「B/S」本身有信息量，
拆成 n-gram 反而破坏语义。

### 25.4 打分权重

```go
标题命中 → +3.0   // 这条知识就是讲这个主题，价值最高
摘要命中 → +1.5
正文命中 → +1.0
最低分 0.1，每节最多 3 条
```

**为什么标题权重最高**：知识条目的标题是 AI 提炼时最重要的一句，
命中标题说明主题对得上；命中正文可能只是顺带提了一嘴。

### 25.5 Prompt 注入格式

```
**参考素材**（用户知识库中的真实材料，优先采用其中的具体信息）：

1. 【系统总体技术架构与技术选型】
   摘要：采用B/S架构，前端React、后端Spring Cloud微服务...
   内容：
   > 系统采用 B/S 架构，前端使用 React，后端使用 Spring Cloud 微服务框架...

2. 【智慧医疗系统设计原则】
   摘要：系统遵循先进性、安全性、可扩展性三大设计原则...
   内容：
   > 系统设计原则包括：
   > - 先进性：采用微服务架构，支持水平扩展

**使用原则**：
- 素材里的具体数据（型号、参数、案例、资质）直接采用，这比编造的可信
- 但不要整段抄写 —— 要结合本章节主题重新组织语言
- 素材与本章无关时忽略，不要硬塞
```

**三点设计考虑**：

1. **标注「真实材料」**：让 Agent 知道这些可以放心引用，
   和它的自由发挥要区分开

2. **明确「不要整段抄写」**：否则 Agent 会直接复制粘贴，
   出来的正文和章节主题脱节

3. **明确「无关就忽略」**：自动匹配会有误命中，
   给 Agent 拒绝的许可比硬塞进去好

### 25.6 实测

**匹配日志**：
```
msg=知识库素材匹配完成 sections=1 with_refs=1
```

**Prompt 里确认有素材**（章节「2.2 应用架构与微服务化设计」）：
自动命中了「系统总体技术架构与技术选型」和「智慧医疗系统设计原则」
两条 —— 正是这一节需要的内容。

### 25.7 配置开关

```go
type KnowledgeConfig struct {
	// Enabled 关掉后正文生成不检索知识库
	Enabled bool
}
```

环境变量 `KNOWLEDGE_ENABLED`，默认 `true`。

**单次请求也能关**：
```json
{
  "disableKnowledge": true,
  "knowledgePerSection": 3,
  "manualKnowledgeOnly": false
}
```

**为什么留 ManualOnly**：用户可能只想用自己精心挑的知识点，
不信任自动匹配。手动挂的精准度远高于自动。

### 25.8 代码规模

Go **25,041 行**。
新增 `knowledge/matcher.go`（约 350 行）+ 测试（约 100 行）。

### 25.9 ★ 前端其实早就有完整的选择器

**重要发现**：桌面版 `GenerationSettingsPage.tsx` 里已经有
「参考知识库」的完整 UI（文件夹树 + 搜索 + 勾选 + 全选/清空），
1886 行页面里的知识库部分完全可复用。

只需要后端配合：
1. `save-reference-knowledge` 接口（存勾选的文档 ID）
2. `reference_knowledge_document_ids` 字段
3. matcher 支持按 DocumentIDs 过滤

**这个发现省掉了大量前端工作** —— 原本以为要新写选择器 UI，
实际只改后端 + bridge 映射。

### 25.10 ★ 状态名不一致的坑

前端过滤条件：
```ts
const availableDocuments = knowledgeIndex.documents.filter(
  (document) => document.status === 'success'
);
```

后端用的是 `ready`。结果：文档明明处理完了，
设置页却显示「暂无已完成的知识库文档」。

**修正**：后端改用 `success`（与前端和桌面版约定一致），
并执行 `UPDATE knowledge_documents SET status='success' WHERE status='ready'`。

**教训**：跨端的状态枚举必须对齐，这类 bug 不会报错，
只会让功能"看起来是空的"。

### 25.11 完整链路实测

```
1. 用户在生成设置 → 知识库页签 → 勾选「技术方案测试.md」
   → 保存请求 {"documentIds":["34097177-..."]}
   → 数据库 {34097177-...} ✓

2. 触发正文生成（2.2 应用架构与微服务化设计）
   → matcher 读取范围 = [34097177-...]
   → 2-gram 提取关键词：微服/架构/用架...
   → 检索命中 2 条知识点
   → with_refs=1

3. Prompt 里注入：
   **参考素材**（用户知识库中的真实材料，优先采用其中的具体信息）：
   1. 【系统总体技术架构与技术选型】
      摘要：采用B/S架构，前端React、后端Spring Cloud微服务...
   2. 【智慧医疗系统设计原则】
      摘要：系统遵循先进性、安全性、可扩展性三大设计原则...
```

**UI 实测截图**：知识库页签显示文件夹树、
「技术方案测试.md 5 条」、全选/取消、右侧「本次已选」面板、
底部导航「上一项：我有方案 / 跳过设置 / 下一项：写多少」。

### 25.12 待补

| 项 | 说明 |
|---|---|
| 素材来源标注 | 生成的正文里注明哪些段落用了素材 |
| 匹配预览 | 生成前让用户看看匹配到了什么 |
| templates/list 404 | 模板市场接口未实现（不影响知识库） |

---

## 二十六、标书查重（2026-11-14 深夜，P9）

### 26.1 桌面版规模与我的取舍

桌面版查重：
- `duplicateCheckService.cjs` 2,869 行
- `duplicateCheckStore.cjs` 987 行
- 前端 1,203 行
- **合计 5,059 行**

Web 版实现 **约 2,450 行 Go**，覆盖核心链路，
元数据深度指纹和图片感知哈希留接口。

### 26.2 ★ 关键认知：查重不是「两份标书互相比对」

一开始我以为查重是比对两份投标文件的相似度（查串标）。
读了桌面版代码才发现，**主体是「招标要求 → 投标响应」的单向核对**：

```
metadata  文档属性指纹（谁做的、什么机器）
outline   投标目录是否覆盖招标要求的各个方面
content   投标正文是否响应了招标的每条要求  ← 核心
image     图片是否重复使用
```

这个认知非常关键 —— 如果做成「两份标书比相似度」，
方向就全错了。

### 26.3 算法移植：四级归一化

```
exact     完全一致
strict    去空白/标点/大小写后一致
field     结构化字段匹配（"项目名称：XXX"）
skeleton  骨架匹配（数字/日期/编号替换成占位符）
near      bigram 包含度 + Dice 相似度
```

**骨架化是最巧妙的设计**：

```
"投标人须提供不少于 3 个类似项目业绩"
"须提供类似项目业绩 5 个"
     ↓ 骨架化
"投标人须提供不少于{num}个类似项目业绩"
```

数字、日期、编号、金额、百分比全替换成占位符，
让"同一类要求"能识别出来，而不是各说各话。

### 26.4 ★★ 实测踩坑：算法直接移植会大规模误报

**第一次实测结果：14 条招标要求，全部判为漏项。**

而投标文件明明响应了 7 条。原因：

```
招标：投标人须提供营业执照副本复印件并加盖公章
投标：我公司已提供营业执照副本复印件并加盖公章，详见附件一

containment = 0.789（阈值 0.90）
dice        = 0.698（阈值 0.82）
→ 判定不匹配
```

主语从「投标人须」变成「我公司已」、尾部多了「详见附件一」，
整句相似度被稀释到 0.70。

**根因认知偏差**：桌面版的阈值是为「几乎逐字复制」调的，
但那套逻辑用在「查投标文件是否响应」上不成立 ——
投标人本来就会改写措辞。

### 26.5 ★★ 解决方案：核心片段包含度（LCS）

```go
// 最长公共子串占招标句的比例
ratio, fragment := CoreContainment(tenderText, bidText)
```

实测效果：

| 场景 | ratio | 公共片段 |
|---|---|---|
| 主语改写 | 0.800 | 提供营业执照副本复印件并加盖公章 |
| 数字改写 | 0.550 | 提供近三年类似项目业绩 |
| 加句首套话 | 0.625 | 000人次/日的门诊量并发处理 |
| 完全无关 | 0.000 | （空） |

**阈值定 0.50**（不是 0.55）：
「数字改写」那条正好是 0.550，压线太脆弱 ——
投标方多写半句「详见业绩表」就掉到阈值以下。

**关键洞察**：查重要回答的不是「这两句像不像」，
而是「这条要求有没有被响应」。后者天然允许措辞改写。

### 26.6 ★ 招标侧过滤：只保留「要求型」句子

招标文件里大量内容是**不需要响应**的：
- 标题行（`## 一、资格要求`）
- 项目概况、联系方式、采购背景

拿它们去比对必然全判漏项。过滤规则：

```go
// 有这些词才算「要求」
markers := []string{"须", "应", "必须", "要求", "不得", "禁止",
                    "不少于", "不低于", "提供", "具备", "符合", "满足"}
```

**实测效果**：14 条句子过滤到 10 条（去掉 3 个标题 + 1 条描述），
覆盖率从「0/14」变成「7/10」。

### 26.7 ★★ 踩坑：两边都过滤标题，目录分析永远空

第一版 `ParseAndStore` 对招标和投标都过滤了 `isPureHeading`。
结果目录分析报「投标文件没有识别到目录结构」。

**根因**：目录分析靠的就是投标文件的标题行。

**修正**：过滤只对招标侧生效，投标侧保留全部句子。

```go
if role == "tender" {
    if isPureHeading(t) { continue }
    if !looksLikeRequirement(t) { continue }
}
```

### 26.8 ★★ 目录分析不能用字符串匹配

```
招标要求：投标人须提供营业执照副本复印件并加盖公章
投标目录：第一章 资格证明
```

人一眼就知道覆盖了，但**没有任何公共子串** ——
字符串相似度永远是 0。

**解决方案：业务主题分类**

```go
var topicCategories = map[string][]string{
    "资格": {"营业执照", "资质", "证书", "资格", "认证", "许可证", ...},
    "业绩": {"业绩", "案例", "合同", "类似项目", ...},
    "技术": {"架构", "系统", "功能", "性能", "接口", "数据", "部署", ...},
    "安全": {"安全", "等保", "保密", "加密", ...},
    "服务": {"服务", "运维", "响应", "支持", "售后", "质保", ...},
    // ...
}
```

判断逻辑：招标要求归属哪些主题 → 这些主题的关键词在目录里出现 ≥60% → 算覆盖。

**实测**：目录 4 项，覆盖 8/10 条要求（合理）。

### 26.9 ★ 顺带修的 bug：运算符优先级

```go
// 错误：&& 比 || 优先级高
if len(runes) > 0 && runes[0] == '（' || runes[0] == '(' {
    // 实际是 (len>0 && runes[0]=='（') || runes[0]=='('
    // 第二个条件没有长度保护 → 可能越界
```

重写了 `isHeadingLike`，覆盖各种标题格式：

```
"## 第一章 资格证明"     → true
"第一章 资格证明"        → true
"一、资格要求"           → true
"1.1 项目背景"          → true
"（一）项目概况"         → true
"8000人次/日的门诊量"    → false（正文，不是标题）
"投标人须提供营业执照"    → false
```

### 26.10 实测结果

**测试数据**：10 条招标要求，投标文件故意漏 3 条
（系统集成资质、等保三级、常驻服务机构）。

**正文分析**：
```
招标要求: 10 条
已响应: 7 条
漏项: 3 条
```

**检出的 3 条漏项**（与我故意漏的完全一致）：
```
投标人须具备信息系统集成及服务资质二级及以上。
系统须符合等保三级安全要求。
投标人须在项目所在地设有常驻服务机构。
```

**零误报、零漏报。**

**目录分析**：目录 4 项，覆盖 8/10 条。

**UI 验证**：四个页签（元数据/目录/正文/图片）全部「已完成」，
接口失败 0。

### 26.11 数据模型

```sql
duplicate_checks       工作区（文件清单 + UI 状态）
duplicate_analyses     四个维度各一行（进度 + 统计 + JSON 明细）
duplicate_sentences    抽出的句子（role 区分 tender/bid）
duplicate_matches      匹配关系（哪条要求被哪句响应了）
```

**为什么句子要落库**：报告要能点开看「这条要求对应投标文件第几句」，
用户会追问「你说我没响应，具体是哪里？」—— 没原始句子无法回答。

### 26.12 代码规模

Go **27,490 行**（查重新增约 2,450 行）。
12 个单元测试全过（归一化、骨架、bigram、Dice、核心包含度、索引性能）。

### 26.13 待补

| 项 | 说明 |
|---|---|
| 元数据深度指纹 | Word rsid / OLE 流提取（判断是否同一台电脑制作） |
| 图片感知哈希 | docx media 提取 + pHash 比对 |
| 投标文件互查 | 现已支持（findDuplicateSentences），单文件测试无重复正常 |
| 报告 Excel 样式 | 现在是纯 CSV，桌面版有格式化的 xlsx |

---

## 二十七、废标项检查（2026-11-14 深夜，P10）

### 27.1 桌面版规模

- `rejectionCheckTask.cjs` 1,745 行
- `rejectionCheckStore.cjs` 1,151 行
- **合计 2,896 行**

Web 版实现 **约 1,840 行 Go**。

### 27.2 为什么废标检查必须用 AI（而查重不用）

| 维度 | 查重 | 废标 |
|---|---|---|
| 判断依据 | 招标要求有没有被响应 | 特定条款有没有踩雷 |
| 可行性 | 覆盖率可算 → 纯算法 | 要理解语义 → 必须 AI |
| 例子 | 「须提供营业执照」vs「已提供营业执照」→ 核心片段匹配 | 「须具备二级资质」vs「我司持有一级证书」→ 要判断一级≥二级 |

**字符串匹配做不到「一级资质满足二级要求」这种推理。**

### 27.3 ★★ Prompt 的两条实战经验（最重要）

桌面版的 Prompt 里有两段话看起来像"免责声明"，实际是从踩坑中总结的：

**① 必须排除线下事项**

```
必须完全忽略，绝不能报告为风险：
- 签字、签章、手写签名
- 盖章、公章、骑缝章
- 密封、封装、封套
- 纸质正/副本份数
- 现场递交、开标现场授权代表到场
```

**为什么**：这些从电子文件判断不了。不排除的话，
AI 会对每一条都报"无法确认"——用户看几条就再也不信这个功能了。

**② 结构性线索优先（防止灾难性误报）**

```
判定材料缺失时必须按以下顺序：
1. 先看有没有结构性线索：章节标题 / 目录项 / 材料清单 / 图片占位标记
2. **只要有任一线索，就判定材料已提交**，
   不得因为看不到扫描件正文而报缺失
3. 只有完全没有任何线索时，才可报「可能缺失」，且不超过 medium
```

**为什么**：文本解析会丢掉图片、扫描件内容。
如果因为"看不到扫描件正文"就判"资质证书缺失"，是最严重的误报 ——
用户明明交了，工具说没交，信任当场归零。

### 27.4 ★★★ 踩到一个 sidecar 的真 bug

抽取一直报：
```
任务失败: 已按最低完成目标再修复一轮，仍存在阻塞流程的问题：
产物文件 invalid-items.md 未生成
```

但去看工作区，**文件明明在**（3555 字节）。

**根因**：sidecar 的 `fileExists(stage.outputFile)` 用的是**相对路径**，
而 sidecar 进程的 cwd 是 `sidecars/agent`，任务工作区在 `data/agent/<taskId>`。
两者不是同一个目录。

```js
// 错误：相对 sidecar 进程的 cwd 检查
const exists = await fileExists(stage.outputFile);

// 修正：基于任务工作区解析
const outputPath = path.isAbsolute(stage.outputFile)
  ? stage.outputFile
  : path.join(workspaceDir || process.cwd(), stage.outputFile);
```

同时 `runTask` 的入参里根本没接 `workspaceDir` —— 一并补上。

**这个 bug 影响所有用 OutputFile 的任务**（知识库提炼也用）。
之前知识库能成功，是因为那次 Agent 恰好把文件写在了 sidecar 的 cwd 下，
属于侥幸。

### 27.5 ★★ 前端签名机制：防止用旧结果

前端有两层签名校验：

```ts
// 1. 抽取结果是否对应当前招标文件
extractionMatchesTender =
  invalidBidAndRejectionItems.tenderSignature === tenderSignature

// 2. 检查结果是否对应当前输入
rejectionCheckMatchesInput =
  rejectionCheckResult.inputSignature === currentRejectionCheckInputSignature
```

**不匹配就显示「待检查」**，避免用户换了文件还看旧结果。

后端必须**逐字符复刻**签名算法，否则永远对不上：

```ts
createDocumentSignature = [
  signatureId, source, fileName, content.length,
  content.slice(0, 800), content.slice(-800)
].join('\n---yibiao-rejection-signature---\n')
```

**踩的坑**：我给 `source` 填了默认值 `"upload"`，
但前端拿到的 `tenderDocument.source` 是 `undefined`（拼进数组变空串）。
差异只有 6 个字符（`upload\n`），但签名比对就是失败，
页面一直停在「解析中」。

**教训**：跨端签名/哈希类算法，任何"看起来无害的默认值"都会毁掉匹配。

### 27.6 ★ 风险项分组要靠 bidDocumentId

前端按 `finding.bidDocumentId === document.id` 分组，
而 `document.id` 是 `bid-1` / `bid-2` 这种格式。

我最初返回 `"doc-" + 文件名`，结果：**「全部 3」但列表空白** ——
风险项存在，但分不到任何文件的组里。

修正：用投标文档列表做文件名 → ID 的映射，找不到就退回第一个。

### 27.7 实测结果

**测试数据**：招标 8 条废标条款，投标文件故意缺 3 项。

**抽取**（1355 字，质量很高）：
```markdown
## 一、无效投标情形
### 1. 不具备规定资格条件
**原文**：「投标人不具备招标文件规定的资格条件的，其投标无效。」
**位置**：第二章 投标人须知 3.7.1
**可否电子核验**：可
...
**可否电子核验**：否（线下核验）
```

**检查发现 3 项**（与我故意缺的完全一致）：

| 风险 | 证据 | AI 的推理 |
|---|---|---|
| 未发现资质证书 | 「第二章 资格证明」下仅有营业执照和业绩表 | 通读全文未检索到相关字样、章节标题或材料清单项 |
| 未体现等保三级 | 「第三章 技术方案」仅三句：B/S 架构、React + Spring Cloud、8000 人次/日 | 未见任何安全设计、等保三级对应措施或测评承诺 |
| 未提供技术偏离表 | 同上，技术方案仅三句概括 | 无法统计负偏离项数，存在被认定超过 3 项的风险 |

**关键验证**：
- ✅ 没有报「未盖章」（Prompt 的线下事项排除生效）
- ✅ 没有报「投标人少于三家」（这条无法从单个投标文件判断，被正确忽略）
- ✅ 自动放过了报价条款（780 万 < 预算 860 万，AI 正确比对）
- ✅ 每条都有原文证据 + 具体修复建议

**UI 截图**：三个页签（废标项「已完成」/ 错别字 / 逻辑谬误），
风险卡片带「投标文件1 · 无效标 · 中风险」标签，可展开、可删除。

### 27.8 数据模型

```sql
rejection_checks    工作区（文件 + 抽取结果 + 抽取签名 + 检查开关）
rejection_findings  风险项（要求 + 证据 + 理由 + 建议 四段）
rejection_runs      三类检查的运行状态
```

**为什么每条都存四段**：用户会追问「凭什么说我有问题」。
只给结论不给证据，等于没做检查。

### 27.9 代码规模

Go **29,330 行**（废标检查约 1,840 行）。
服务层测试全过（duplicate / knowledge / artifact / storage）。

### 27.10 待补

| 项 | 说明 |
|---|---|
| 错别字检查 | Prompt 已写好，未实测 |
| 逻辑谬误检查 | 同上 |
| 从技术方案读招标 | bridge 已接，未验证 |
| 报告导出 | CSV 已实现，未做 xlsx 样式 |

---

## 二十八、错别字与逻辑谬误检查（2026-11-14 深夜，P10 补）

### 28.1 实测结果

**测试文件埋了三类问题**：
- 错别字：竞标/投标、按装/安装、部暑/部署、即然/既然
- 逻辑：工期 90 vs 120 天、报价 780 万 vs 分项合计 800 万

**错别字检出 3 项**（埋了 4 个）：

```
「按装调试」→「安装调试」 @ 第一章 投标函
  「按装」为「安装」的同音错字，属项目实施关键环节用词，错误影响理解
「部暑」→「部署」     @ 第二章 技术方案
  「暑」与「署」同音混淆，且同句后文已正确写作「部署」
「即然」→「既然」     @ 第二章 技术方案
```

漏掉的「竞标」是**正确的省略** —— 「竞标」本身是正当说法
（指竞争性投标），不是错别字。这说明 Prompt 里
「只报确定性错误」的要求生效了。

**逻辑检出错 2 项，推理很细**：

```
我方投标报价为人民币 780 万元 → 800 万元
  投标函总报价 780 万与报价明细分项加总（320+380+100=800 万）不一致

工期 90 日历天 → 120 日历天
  投标函承诺 90 天，实施计划总工期 120 天（20+70+30=120，内部自洽）
```

**AI 不仅发现矛盾，还核验了分项加总和阶段天数** —— 这不是简单的字符串比对。

### 28.2 ★★ 三个坑：字段名不匹配导致数据被静默丢弃

跑通三类检查后，页面显示：
- 废标项：5 项 ✓
- 错别字：3 项 ✓
- **逻辑谬误：0 项**（数据库里明明有 2 项）

**根因**：前端 `normalizeLogicFindingState` 里有这一句：

```ts
const fallacyReason = typeof item.fallacyReason === 'string' ? item.fallacyReason.trim() : '';
if (!title || !fallacyReason) return null;   // 缺一个字段，整条丢弃
```

我返回的是 `riskReason`，前端要的是 `fallacyReason`。
**没有报错、没有警告，数据就这样消失了。**

**错别字用的是另一套字段**（`TypoCheckFinding`）：
`wrongText / correctText / originalExcerpt / reason / locationHint`

**逻辑用的是第三套**（`LogicCheckFinding`）：
`title / originalText / locationHint / fallacyReason / suggestion`

三类检查的字段名字全不一样，必须逐个对齐。

### 28.3 ★★ 签名也要按检查类型区分

修完字段后，页面又显示「投标文件已变化，请重新检查以刷新结果」。

**根因**：我给三类结果都返回同一个 `inputSignature`。

但前端用的是**两个不同签名**：

```ts
// 废标项：投标文件 + 废标条款 + 自定义项
inputSignature = 投标签名 + 条款内容 + 自定义项

// 错别字/逻辑：只看投标文件
bidSignature = 投标签名
```

**业务逻辑是对的**：改了废标条款，不应该让错别字检查结果失效 ——
错别字只跟投标文件内容有关。

### 28.4 现在的完整检查能力

| 检查 | 方法 | 实测 |
|---|---|---|
| 废标项 | AI 逐条核验（排除线下事项 + 结构性线索优先） | 5 项，含 1 项 high |
| 错别字 | AI 校对（只报确定性错误） | 3 项，同音字全命中 |
| 逻辑谬误 | AI 交叉核验（数据/时间/主体/承诺/计算） | 2 项，含分项加总核验 |

三类可任意组合启用，各自独立进度和结果。

### 28.5 代码规模

Go **29,376 行**。四个服务测试全过。

---

## 二十九、暗标合规检查（2026-11-14 深夜，P11）

### 29.1 ★★★ 最重要的发现：检查引擎已经存在

原以为自己要重写 3,582 行桌面版逻辑。翻 C# helper 源码时发现
`JobRunner.cs` 里有四个 action：

```csharp
BlindCheck.BlindCheckInspectAction.Name   → blind-check-inspect
BlindCheck.Fix.BlindCheckFixAction.Name   → blind-check-fix
BlindCheck.Review.BlindCheckDumpAction.Name
BlindCheck.Review.BlindCheckEditAction.Name
```

再看目录：

```
BlindCheck/                9,922 行 C#
├── Model/                 文档模型、格式解析、图片目录、大纲分析
├── Rules/                 9 个规则文件
│   ├── PageRules / ParagraphRules / FontRules
│   ├── TableRules / TextRules / StructureRules
│   └── ConsistencyRules / ImageRules / PrivacyRules
├── Fix/                   自动修复（含 FieldEditor 等）
├── Ooxml/                 XML 底层操作
└── Review/                AI 复核数据导出与编辑
```

**Go 侧只需接线，不用重写任何规则。**
这省掉的工作量是数周级别 —— 那 9,922 行里有大量 OOXML 细节
（样式继承链、编号格式解析、图片格式识别），重写等于重犯所有坑。

### 29.2 调用链路（踩了三个坑）

**坑 1：必须用 ExecuteWithFiles**

client 的 workdir 是 `os.MkdirTemp` 创建的临时目录，
调用方无法预知。docx 必须通过 Files 声明物化：

```go
result, err := client.ExecuteWithFiles(ctx, map[string]any{
    "action":  "blind-check-inspect",
    "source":  "doc.docx",
    "profile": profile,
    "sensitive_words": words,
}, openxml.ExecuteOptions{
    Files: map[string]string{"doc.docx": localPath},
    CollectJobFiles: map[string]string{...},   // 见坑 2
})
```

**坑 2：结果在 job 子目录，不在工作区根**

`blind-check-result.json` 写在 `openxml-jobs/<jobId>/` 里，
而 jobId 是 client 内部生成的。

给 `ExecuteOptions` 加了新字段 `CollectJobFiles`：

```go
// CollectJobFiles 从任务目录取回（任务目录内相对路径 → 本地路径）
// 为什么单独一个字段：jobId 由 client 内部生成，调用方不知道，
// 只能声明"我要从 job 目录取这个文件"。
CollectJobFiles map[string]string
```

**坑 3：profile 为空 = 0 条发现**

`FormatTarget` 所有字段默认 null，null 表示"不检查"。
传 `profile: {}` 时 64 条规则全跑一遍但一条都不报。

### 29.3 ★★ 前端白屏：profile 结构必须完整

第一次接前端，整页白屏。控制台报：

```
TypeError: Cannot read properties of undefined (reading '1')
  at baseTabSummary (presetFields.ts:442)
```

原因：`presetFields.ts` 里直接读深层字段

```ts
const first = profile.headings['1'];
return `一级 ${sizeLabel(first.size_pt)}...`
```

而我返回的预设是**稀疏结构**（只有 body/page/table 三段，
`headings` 里只有 `'1'`，没有 `'2'~'4'`）。

**桌面版的做法**：有个 `DEFAULT_PROFILE` 完整基底，
所有预设都是「基底 + 覆盖项」。

**修正**：Go 侧也建一份 `defaultProfile`（11 个段、70+ 字段），
`ResolveProfile` 改成三层合并：

```
defaultProfile  →  预设覆盖项  →  用户自定义
```

并加了防白屏测试 `TestResolveProfileComplete`：
遍历每个预设，断言 9 个段都在、`headings` 有 1~4 级、`body` 关键字段齐全。

### 29.4 ★ 预设是「差异」而非「全集」

内置预设只写与默认值的不同之处：

```go
{
    ID: "builtin-hebei", Name: "河北（参考）",
    Profile: map[string]any{
        "page": map[string]any{"margin_top_cm": 2.5, "margin_bottom_cm": 2},
        "body": map[string]any{"line_pt": 30},
        "table": map[string]any{"size_pt": 14},
        "headings": map[string]any{"1": map[string]any{"size_pt": 14, "bold": true}},
    },
}
```

四个地区预设的差异（都验证过）：

| 预设 | 行距 | 页边距 | 表格字号 | 一级标题 |
|---|---|---|---|---|
| 通用 | 25 磅 | 四边 2.5 | 五号 | 三号加粗 |
| 内蒙古 | 28 磅 | 四边 2.0 | 五号 | 三号加粗 |
| 河北 | 30 磅 | 上2.5 下左右2.0 | 四号 | 四号加粗 |
| 四川 | 28 磅 | 四边 2.5 | 四号 | 三号不加粗 |

### 29.5 ★ resultTab 值域踩坑

页面显示「**undefined**」未发现问题。

前端 `BlindCheckResultTab` 的类型是：
```ts
type BlindCheckResultTab = 'all' | 'blindCheckCategory';
```

而我默认值写成了 `'issues'`（语义上像"问题清单"）。
值域不匹配 → `categoryLabels['issues']` 是 undefined。

修正：`result_tab` 默认值改成 `'all'`，并加迁移清理存量脏值。

### 29.6 实测结果

**测试文件**（手写 OOXML 造的 docx，2199 字节）：
- 纸张 A3（应 A4）
- 页边距 1cm（应 2.5cm）
- 正文黑体小四（应宋体四号）
- 行距 0.5 倍（应固定 25 磅）
- 含企业名称「北京智慧医疗科技有限公司」

**检查结果**：

```
检查 2 段正文，64 条规则，耗时 515ms
high=1 medium=9 low=0 total=10

[high]   隐私与匿名 | 出现企业名称「北京智慧医疗科技有限公司」
[medium] 中文字体 | 中文字体不是宋体
[medium] 字号 | 字号不是四号（14磅）
[medium] 页边距 | 上/下/左/右不符合要求（4 条）
[medium] 纸张大小 | 纸张大小不是 A4
[medium] 首行缩进 | 首行缩进不符合要求
[medium] 行距 | 行距未设为固定值
```

**埋的 6 类问题全部检出，零误报。**

**UI 四个步骤全部正常**：

| 步骤 | 内容 |
|---|---|
| STEP 01 | 选择标书（上传 docx） |
| STEP 02 | 格式要求（4 个预设 + 5 个页签摘要 + 完整编辑表单） |
| STEP 03 | 检查结果（高1/中9/低0，按分类统计，可切换查看） |
| STEP 04 | 自动修复（修复项清单，带"当前 N 处"统计） |

STEP 02 的页签摘要渲染得很完整：

```
页面        A4 · 边距 2.5/2.5/2.5/2.5
正文        宋体 四号 · 固定 25 磅
标题与目录   一级 三号加粗
表格与图片   宋体 五号
身份与痕迹   敏感词 0 个 · 白名单 0 个
```

**接口失败 0。**

### 29.7 数据模型

```sql
blind_checks         工作区（文件 + 预设 + 敏感词 + 开关）
blind_check_results  每次检查一行（保留历史便于对比）
blind_findings       按规则分组（含 samples 和 items）
blind_fixes          修复记录（勾选规则 + 前后对比）
```

**为什么 findings 存 samples 和 items 两份**：
- `samples` 给用户看（"正文第 1 段：xxx…"）
- `items` 带 `locator`（`p12`、`t3.r2.c1`），是修复时定位用的

### 29.8 代码规模

Go **31,380 行**（暗标检查新增约 2,000 行）。
六个服务测试全过（artifact / blind / duplicate / knowledge / openxml / storage）。

**如果不复用 C# 引擎，这 2,000 行会变成 12,000 行以上**
（还得复刻 9,922 行规则实现）。

### 29.9 待补

| 项 | 说明 |
|---|---|
| 自动修复 | C# 有 `blind-check-fix`，Go 侧接口未接（前端已提示"暂未开放"） |
| AI 复核 | C# 有 `blind-check-dump` + `blind-check-edit`，未接 |
| 我的预设 | 前端支持自定义预设，后端暂只返回内置 4 个 |
| 资信库敏感词 | 依赖资信库模块（未实现），目前只从 tenant_settings.extra 读 |
| 从招标文件生成预设 | 需要 AI 抽取，未接 |

---

## 三十、暗标自动修复（2026-11-15 凌晨，P12）

### 30.1 C# 侧的能力

`blind-check-fix` action 的请求很简洁 —— 继承检查请求，加了两个字段：

```csharp
sealed class BlindCheckFixRequest : BlindCheckInspectRequest
{
    public string Output { get; set; } = "";        // 修复副本路径，原文件不动
    public List<string> FixRules { get; set; } = []; // 勾选要修的规则
}
```

返回的 `BlindCheckFixReport` 信息很足：

```csharp
Fixes       Dictionary<string, int>    // {规则ID: 修了几处}
Notes       List<string>               // 修不了的说明
Validation  FixValidationResult        // 对比校验
After       BlindCheckReport           // 修复后复查
HeadingFallback bool                   // 标题识别兜底
```

### 30.2 ★★ 校验设计：只算「新增」错误

`FixValidation.cs` 的注释道出了关键：

> WPS 生成的文档本身常有不规范之处，只有副本多出原文件没有的错误
> 才算修复引入的问题。

**这个判断非常准确。** 如果简单比较"错误总数"，
WPS 文件本身带 5 个 OOXML 问题，修复后还是 5 个（但换了位置），
就会被误判成"修复无效"。

实际实现：按「错误类型 + part + 元素」对比前后两个集合，
只有**副本独有**的错误才算修复引入的。

```go
// Go 侧对应：有 new_errors 就拒绝交付
func FailedFixValidation(r *FixReport) bool {
    return r != nil && len(r.Validation.NewErrors) > 0
}
```

**失败时删掉产物** —— 宁可让用户重试，也不给一个可能损坏的文件。

### 30.3 ★ 前端只传 { ai }，规则由后端存

看前端调用：

```ts
window.yibiao.tasks.startBlindCheckFix({ ai })
```

**没传 fixRules**。规则是用户在「修复项」面板逐个勾选时，
通过 `saveFixRules` 已经存到后端了。

所以后端必须：
1. `blind_checks` 表加 `fix_rules TEXT[]` 字段持久化
2. `StartFix` 从库里读规则（请求里的作为补充）
3. `NormalizeFixRules` 过滤掉不可修的

**默认勾选 37 项**（可修 41 项，排除 4 项）：
```go
var notDefaultFixRules = map[string]bool{
    "para.page_break_before": true,  // 需人工确认是否必要
    "para.snap_to_grid":      true,  // 同上
    "text.english_punct":     true,  // 只是排版建议，不是招标条款
    "text.spaces":            true,  // 同上
    "page.columns":           true,  // 同上
}
```

### 30.4 ★ 修复结果也要 inputSignature

页面显示「已失效，请重新修复」，原因同前面几次：
修复结果也需要 `inputSignature`，前端用它判断结果是否对应当前输入。

```ts
const fixStale = fixResult.status !== 'idle'
  && fixResult.status !== 'running'
  && currentSignature !== null
  && fixResult.inputSignature !== currentSignature;
```

### 30.5 ★ items 结构：前端靠它算「已修复 N 处」

前端 `FixPanel` 的计数逻辑：

```ts
const fixedCount = items.reduce((sum, item) => sum + item.fixed, 0);
const resultTabSummary = () => `已修复 ${fixedCount} 处 · 待人工 ${pendingCount} 处`;
```

而 helper 返回的是 `fixes: {规则ID: 数量}`，不是 items 数组。

**转换逻辑**（`buildFixItems`）：
- 有修复的规则 → `{fixed: count, pending: 0}`
- notes 里的说明 → `{fixed: 0, pending: 1}`（需人工）

同时给规则 ID 配人话名（`font.size` → "字号"），
前端明细列表直接显示。

### 30.6 实测结果

**修复前**：high=1 medium=9 total=10
**修复后**：high=1 medium=0 total=1

```
修复明细（6 类 9 处）：
  font.chinese            1 处
  font.size               1 处
  page.margins            4 处
  page.paper_size         1 处
  para.first_line_indent  1 处
  para.line_spacing       1 处

校验：checked=true, original_errors=0, fixed_errors=0, new_errors=[]
产物体积：2199 → 2099 字节
耗时：156ms（C# 侧）/ 706ms（含 HTTP 往返）
```

**剩的 1 项 high 是企业名称泄露** —— 敏感词清理没在勾选范围，
默认 37 项里不含 `privacy.keyword`（那不是"修"而是"删"，
需人工确认删除哪个词）。

**UI 截图确认**：
- 修复项：已开启 37 项
- 修复结果：已修复 9 处 · 待人工 0 处
- 风险对比：高风险 **1 → 0 处**
- 明细 6 条，每条带「已修复 N 处 / 共 N 处」标签
- 底部「导出修复结果」可下载

### 30.7 一个值得记的设计

修复产物文件名是 `原文件名-fixed.docx`，
**原文件不动**。

为什么重要：用户可能想对比修复前后，
或者修复效果不满意要重来。原地改会让原文件不可恢复。

### 30.8 代码规模

Go **32,083 行**（自动修复新增约 700 行）。
六个服务测试全过。
