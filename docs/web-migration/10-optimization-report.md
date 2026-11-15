# 全面优化报告（2026-11-15）

本次对项目做了从架构到 UI 的系统性排查与修复。**所有改动均已通过编译、vet、单元测试和全站冒烟（19 通过 / 0 失败）验证。**

---

## 一、发现并修复的真实缺陷

### 1. 生成设置页显示「范围·undefined」（功能缺陷，高）

**现象**：进入「内容生成 → 投标文件 → 生成设置」，页签摘要显示 `范围·undefined`。

**根因**：`TechnicalPlanService.LoadState` 只读了 meta / nodes / sections 三张表，**完全没读 `technical_plan_generation_config`**：

```go
// 修复前
return toPlanState(meta, nodes, sections), nil

// 修复后
cfg, err := s.q.GetGenerationConfig(ctx, ...)
return toPlanState(meta, nodes, sections, cfg), nil
```

`PlanState` 里 `OutlineMode`、`GlobalFactsMode`、`ExportTemplateID`、字数控制等字段只声明未赋值，前端读到空值。

**影响面比表面大**：这不只是显示问题 —— 用户之前保存的生成配置（标书类型、字数范围、导出模板）全部读不回来，等于**设置从未生效**。

**修复**：`LoadState` 读取配置表，`toPlanState` 补齐 8 个字段的映射。

---

### 2. 模版设置页接口 404（功能缺失，高）

**现象**：进入「模版设置」，`templates/list` 返回 404。

**根因**：`templates` 整组接口（list/get/create/update/delete/duplicate/renderPreview）在前后端都没有实现。

**修复**：
- 新建 `export_templates` 表（含 `is_system` 标记，系统预设不可改不可删）
- 实现 7 个 handler，含名称冲突自动加序号、系统模板保护
- 补 bridge 映射

---

### 3. 设置页保存 404（功能缺陷，高）

**现象**：设置页保存配置时报 404。

**根因**：前端调 `config.save`（传整个配置对象），后端只有 `config/save-text` 和 `config/save-image`。

**修复**：新增 `/config/save` 统一入口，按字段分发到文本/生图两块。保留占位符 Key 不覆盖真实值的逻辑。

**验证**：保存后 `has_text_api_key` 仍为 true，占位符没有冲掉真实 Key。

---

### 4. 12 个外键列缺索引（性能，中）

**根因**：PostgreSQL 不为外键自动建索引。

**影响**：
- 按外键过滤的查询走全表扫
- 删除父记录时要扫全表检查引用（级联删除极慢）

**修复**：补齐 12 个索引，重点是 `duplicate_matches.tender_sentence_id`（查重报告关联）和 `knowledge_documents.project_id`（知识库过滤）。

---

### 5. 知识库上传 N+1 查询（性能，中）

**根因**：`UploadDocuments` 拿到 `Upload` 返回的文档列表后，又按 ID 逐个查库。

```go
// 修复前：N 个文件 = N 次多余查询
for _, d := range result.Documents {
    row, _ := h.q.GetKnowledgeDocument(...)   // 纯冗余
    docs = append(docs, toDocView(row))
}
```

**修复**：直接从 `DocumentView` 映射字段，删掉所有查询。

---

### 6. 中文截断产生乱码（正确性，中）

**现象**：日志、错误消息里偶发乱码字符。

**根因**：`truncate` 在 8 个文件里各有一份，其中 **7 份按字节截断**：

```go
// 错误：中文被切成半个字
if len(s) <= n { return s }
return s[:n] + "…"

// 正确
runes := []rune(s)
return string(runes[:n]) + "…"
```

**修复**：统一到 `textutil.Truncate`（按 rune 截断），8 处调用全部替换。

---

### 7. 路径穿越防护不一致（安全，中）

**根因**：`sanitizeFileName` 有两份实现，逻辑不同：

| 位置 | 有 `..` 拦截 | 有长度限制 | 反斜杠处理 |
|---|---|---|---|
| `handler/rejection.go` | ✅ | ❌ | ❌ |
| `service/upload/service.go` | ✅ | ✅ | ✅ |

**隐患**：只要哪天有人改了一份忘了另一份，就是安全漏洞。

**额外发现**：Linux 的 `filepath.Base` **不把 `\` 当分隔符**，Windows 客户端传来的 `..\..\cmd.exe` 不会被切开。

**修复**：统一到 `textutil.SafeFileName`，含五道防线（统一分隔符 → 取 basename → 剔除保留字符 → 拦截 `..` → 超长截断保留扩展名），并补了 2 个安全单元测试。

---

### 8. 生产环境泄露内部错误详情（安全，中）

**根因**：`httpx.Internal(err)` 把 `err.Error()` 原样放进 response 的 `detail`。生产环境会暴露表名、SQL、文件路径。

**修复**：加环境开关，`dev` 暴露（方便排查）、`prod` 隐藏。

---

### 9. 前端 bridge 初始化失败会白屏（健壮性，中）

**根因**：前端有 **147 处**直接调 `window.yibiao.xxx`（桌面版遗留写法，无 `?.` 保护）。`index.html` 里的 `install()` 一旦抛错，`window.yibiao` 就是 undefined，连锁崩溃。

**修复**：`install()` 包 try/catch，失败时挂一个返回 rejected Promise 的最小 shim，页面能正常渲染并提示错误。

---

### 10. 死代码与占位变量（可维护性，低）

```go
_ = tenantStr
_ = userID
_ = check
```

这些说明「参数取了但没用」。已清理 3 处。

---

### 11. Web 端不适用功能产生 404 噪音（体验，中）

**根因**：17 个 IPC 命名空间未映射，页面调用时全报 404。

**修复**：按性质分三类处理：

| 类型 | 处理 | 例子 |
|---|---|---|
| Web 端**不适用** | 返回合理默认值 | 授权检测、插件管理、系统字体 |
| **未迁移**功能 | 明确错误消息 | 可行性研究报告、资信库写操作 |
| **本地能力** | 菜单加提示 | Word批量转换、插件管理 |

新增 `webUnavailableNotice` 提示文案，与"还没开发"区分开 —— 这类是浏览器环境本身做不到，不该承诺开发进度。

---

## 二、架构与代码组织改进

### 2.1 建立公共工具包 `textutil`

原来散落 8 处的 `truncate`、3 处 `cleanJSON`、2 处 `sanitizeFileName`、2 处 `itoa` 全部收敛：

| 函数 | 原来 | 现在 |
|---|---|---|
| `Truncate` | 8 份（7 份有 bug） | 1 份 |
| `SafeFileName` | 2 份（逻辑不一致） | 1 份 |
| `StripCodeFence` | 3 份 | 1 份 |
| `itoa` | 2 份手写 | 直接用 `strconv` |

**收益**：重复函数从 8/3/3/2/2 降到 3/2/2/2/2，且消除了「同一逻辑有多份实现、改一份漏一份」的风险。

### 2.2 统一错误处理约定

- 关键路径（用户操作触发的写入）**不再吞错**
- 后台 goroutine 的状态更新可吞（无法向用户报错）
- 生产环境隐藏内部详情

---

## 三、测试补强

新增 4 个测试文件、**18 个测试用例**：

| 测试 | 覆盖点 |
|---|---|
| `textutil/truncate_test.go` | 按 rune 截断（含"永不产生乱码"穷举）、空白压缩、代码围栏剥离 |
| `textutil/filename_test.go` | 路径穿越（Unix/Windows/混合分隔符）、保留字符、超长截断 |
| `service/blind/preset_test.go` | 预设完整性（防白屏）、三层合并、敏感词去重、签名变更 |
| `service/openxml/blindcheck_test.go` | 调 C# 引擎跑真实 docx，验证 64 条规则 |

**特别的防回归测试**：

```go
// TestTruncateNoBrokenRune：穷举所有 n，确认结果永远是合法 UTF-8
for n := 0; n <= len([]rune(long))+2; n++ {
    got := Truncate(long, n)
    for _, r := range got {
        if r == '\uFFFD' { t.Errorf("n=%d 时出现乱码", n) }
    }
}
```

```go
// TestResolveProfileComplete：数据结构不完整会让前端白屏，
// 遍历每个预设断言 9 个段都在、headings 有 1~4 级
```

### 冒烟测试固化

把临时验证脚本整理成 `scripts/dev/smoke-all.mjs`：

- 覆盖 6 个一级菜单 + 13 个二级页面
- 检查白屏 / JS 错误 / 接口 4xx / 页面含 `undefined`
- **有退出码**，可接入 CI
- 区分「real failure」和「notice 占位项」（后者点击只弹提示，是设计行为）

---

## 四、一个重要的过程教训

优化过程中有一段时间「改了代码但现象没变」。追查发现：**本地改完没同步到服务器**，Vite dev server 读的是旧文件。

排查方法（值得记住）：

```bash
# 直接问服务器上的文件到底有没有那段代码
ssh srv253 "docker exec ... bash -c 'grep -c WEB_NOOP_HANDLERS /workspace/client/src/shared/bridge/web.ts'"
# 本地 2 处，服务器 0 处 → 同步问题，不是逻辑问题
```

**教训**：现象与预期不符时，先确认「改动真的生效了」，再怀疑逻辑。否则会在错误的方向上反复调试。

---

## 五、最终状态

```
Go 代码        32,766 行
gofmt          合规
go vet         无告警
单元测试       7 个包全过（18 个用例）
全站冒烟       19 通过 / 0 失败
```

### 页面验证明细

| 一级菜单 | 二级页面 | 状态 |
|---|---|---|
| 内容生成 | 投标文件 / 可行性研究报告 | ✅ |
| 模版设置 | （单页） | ✅ |
| 知识库 | 文档知识库 / 资信库 / 图片知识库 | ✅ |
| 标书检查 | 查重 / 废标项 / 暗标 / AI评标 | ✅ |
| 其他功能 | Word转换 / 插件 / 产物库 / 精选资源 | ✅ |
| 设置 | （单页） | ✅ |

其中「图片知识库」「AI评标」「Word批量转换」「插件管理」是**已标注的未开放项**，点击弹提示而非报错。

---

## 六、未做的与原因

| 项 | 原因 |
|---|---|
| 前端 2763 行的 SettingsPage 拆分 | 来自桌面版，改动风险大于收益；本次只补了缺失的接口 |
| 后台 goroutine 的错误处理 | 无法向用户报错，改为记日志即可 |
| 可研报告 / 资信库完整实现 | 桌面版功能，需独立迁移周期；本次只做优雅降级 |
| 模板样张预览 | 需把 ExportFormatConfig 转 HTML 再渲染 docx，工作量独立 |
