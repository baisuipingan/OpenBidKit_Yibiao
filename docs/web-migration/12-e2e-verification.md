# 端到端全流程验证报告（2026-11-15）

用真实招标文件跑一遍：**上传 → 解析 → 配置 → 生成 → 导出 docx**。

## 结论先说

**能跑通，但过程中挖出 7 个真实缺陷，且 AI 生成段被无效 API Key 阻塞。**

| 环节 | 状态 |
|---|---|
| 上传招标文件 | ✅ 修好后可用 |
| 解析（docx → markdown） | ✅ 4182 字，内容完整 |
| 生成设置 | ✅ 页面正常 |
| AI 分析 / 目录 / 正文 | ⚠️ 接口打通，**Key 无效无法真跑** |
| 导出 Word | ✅ 生成真实 docx，内容正确 |

---

## 一、挖出的 7 个缺陷

### 1. 「点上传」按钮完全没反应（致命）

**现象**：点「上传」按钮，页面毫无变化，等 64 秒也没反应。

**根因**：桌面版的「点按钮上传」走主进程弹系统文件框（`importTenderDocument()` 无参 → 后端弹框）。浏览器没这个能力，bridge 层把它实现成返回空数组，于是静默失败。

**修复**：在 bridge 里实现 `pickFilesFromBrowser`——动态创建 `<input type="file">` 并触发点击，用 `window.focus` 兜底取消场景。

```ts
function pickFilesFromBrowser(accept: string): Promise<File[]> {
  // 用户取消时 change 不触发，用 focus 事件收尾
}
```

### 2. 伪路径 ID 与后端不一致（致命）

**现象**：上传成功了（文件真在磁盘上），但导入时后端报 `上传文件不存在或已过期`。

**根因**：`getPathForFile` 是**同步** API，必须立刻返回伪路径。前端就用自己生成的随机 ID 造了 `upload://<clientId>`。但后端 `Prepare` 会自己生成一个**不同的** ID 存进 entries——两边对不上。

```ts
// 修复前
const pseudo = `upload://${localId}`;       // 客户端 ID
const prep = await request('/upload/prepare')  // 后端另有 ID

// 修复后：把 clientId 传给后端，两边用同一个
const prep = await request('/upload/prepare', { uploadId: localId })
```

### 3. 竞态：上传还没完成就调导入（致命）

**现象**：修了 #2 之后仍偶发失败。

**根因**：`import-tender-document` 在 `upload/raw` 完成**之前**就发出了。日志时间戳只差 4 毫秒。

**修复**：调接口前先 `await waitForUpload(pseudoPath)`。

### 4. `NS_LOCAL` 把同步方法包成 Promise（致命）

**现象**：请求体变成 `{"filePaths":[{}]}`——路径变成了空对象。

**根因**：我把 `file.getPathForFile` 加进了 `NS_LOCAL`，而那个分支统一包了 `Promise.resolve(impl(...))`：

```ts
if (nsLocalKey in NS_LOCAL) {
  return (...args) => Promise.resolve(impl(...args));  // 返回 Promise！
}
```

`getPathForFile` 的契约是**同步返回字符串**，被包成 Promise 后 `paths.map(...)` 得到 `[Promise]`，JSON 序列化就变成 `{}`。

**修复**：从 `NS_LOCAL` 里删掉，只保留 `buildNamespace` 里的同步实现。

### 5. step 值不在白名单，整页白屏（致命）

**现象**：招标文件明明导入成功（数据库有数据），页面却是空白，只有一个「步」字。

**根因**：数据库里 `step` 的值是 `documents`，但前端只认 `document-analysis`。`UpdateStep` **没有任何校验**，任意字符串都能写进去。

```go
// 修复：加白名单 + 历史值归一化
var validSteps = map[string]bool{
    "document-analysis": true, "bid-analysis": true,
    "outline-generation": true, "global-facts": true,
    "content-generation": true, "export": true,
}
if step == "documents" { step = "document-analysis" }  // 归一化旧数据
```

### 6. 页面初始化只读本地缓存（致命）

**现象**：刷新页面后，明明已导入的招标文件「消失」了。

**根因**：`useTechnicalPlanWorkflow` 初始化时**只读本地缓存**（`technicalPlanStorage.load()`），从不调后端 `loadState`。桌面版靠本地 JSON 就够，Web 版必须读服务端。

**修复**：优先读后端，失败时回退本地缓存（离线容错）。

### 7. 22 个前端方法，后端只有 8 个（功能缺口）

**清单**：

```
缺：saveOutline / saveGlobalFacts / saveBidAnalysisConfig / selectBidSection
    checkBidSections / inspectBidTemplate / openBidTemplate / resetContentGeneration
    saveContentGenerationOptions / saveBusinessFillReview / getBusinessFillReview
    readTenderSourceMarkdown / removeOriginalPlanDocument / loadGenerationConfig
    previewContentWord / previewBusinessChapterWord / available / clear
    saveOutlineSelection
```

**修复**：补齐 18 个 handler，状态统一存 `technical_plan_meta.extra`（JSONB）。

**为什么用 JSONB 而不是建列**：这些是 AI 生成的中间产物（目录、事实、分析配置），结构随功能演进会变。每加一种就做一次迁移成本太高，而且它们不需要被 SQL 单独查询索引。

### 8. 目录没写进节点表（致命）

**现象**：`save-outline` 返回 200，但正文生成拿不到目录。

**根因**：只写了 `extra` JSONB，没写 `technical_plan_outline_nodes` 表。正文生成按节点读、导出按节点遍历，全拿不到。

**修复**：改走 `service.SaveOutline`，真正写进节点表。

### 9. 节点 ID 类型不匹配（500 错误）

**现象**：`save-section-content` 返回 500。

**根因**：节点表主键是 UUID，但前端/AI 用字符串 ID（`"n1"`）。后端 `uuid.Parse("n1")` 直接失败。

**修复**：加 `external_id` 列存前端 ID，按 `(project_id, external_id)` 定位。

---

## 二、验证结果

### 上传 + 解析

```
[STEP 01] 文件名: tender.docx ✓
          字数: 4182 ✓
          正文可见: 是 ✓（完整招标文件内容）
          接口失败: 无 ✓
```

### 导出 Word

```
状态: 200
{
  "assetId": "31c1753c-...",
  "fileName": "某某产业园区综合服务中心建设项目_技术方案_20261115_1231.docx",
  "sizeBytes": 3532
}
```

解压 docx 验证内容：

```
第一章 项目总体认识
本项目为某某产业园区综合服务中心建设项目，总建筑面积 18600 平方米。
我公司充分理解招标文件要求，将严格按照国家现行规范组织施工。
```

**内容正确，格式是标准 docx。**

### 产物库

页面能列出所有导出产物：

```
某某产业园区综合服务中心建设项目_技术方案_20261115_1231.docx
某某产业园区综合服务中心建设项目_技术方案_20261115_1230.docx
tender.docx
```

### 全站回归

```
18 通过 / 0 失败
```

---

## 三、受阻的部分：AI 生成

**接口已全部打通**：

```
/tasks/start-bid-analysis        → 200，返回 taskId
/tasks/start-content-generation  → 200
/tasks/start-outline-generation  → 200
```

但实际执行会失败：

```json
{"code":"LIST_MODELS_FAILED","message":"服务返回 HTTP 401: {\"code\":\"INVALID_API_KEY\"}"}
```

**这是环境凭证问题，不是代码问题。** 需要在「设置」页填入有效的 API Key（当前配的是 `milorouter.com` + `deepseek-flash`，Key 已失效）。

补齐后，`startBidAnalysis` → `startOutlineGeneration` → `startContentGeneration` 这条链路就能真跑。

### 为此做的额外工作

**物化招标文件**：agent 层用 `os.Stat` / `os.ReadFile` 直接读文件，而 Web 版的 markdown 存在 artifact 存储里。两者之间加了一层「物化」：

```go
func (t *Task) materializeTenderMarkdown(...) (string, func(), error) {
    // 从 artifact 取出 → 写临时文件 → 返回路径 + cleanup
}
```

**补 2 个任务入口**：`startBidAnalysis` / `startContentGeneration`，注入对应的 agent Runner。

---

## 四、当前状态

```
Go 代码      34,948 行（本轮 +1,029）
gofmt        合规
go vet       无告警
单元测试     7 个包全过
全站冒烟     18 通过 / 0 失败
端到端       上传→解析→导出 通；AI 生成待有效 Key
```

### 数据库新增

| 表/列 | 用途 |
|---|---|
| `technical_plan_meta.extra` | AI 中间状态（JSONB） |
| `technical_plan_outline_nodes.external_id` | 前端字符串节点 ID |

### 下一步（需要用户操作）

1. 在「设置 → 模型配置」填入有效 API Key
2. 重跑 `scripts/dev/e2e-full-bid.mjs` 验证完整 AI 链路
