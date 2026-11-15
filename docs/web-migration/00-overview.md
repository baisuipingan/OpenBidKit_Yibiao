# 易标 Web 版迁移总体方案

> 基线：`client/` 桌面版（2026-11-12，1066 commits）
> 目标：Go 后端 + 浏览器前端，先单公司定制，架构预留公网多租户、会员等级、积分
> 参考：`jdcome/OpenBidKit-Yibiao-Web`（重写版，已停更于 2026-08-20，仅借鉴思路不搬代码）

---

## 一、核心判断

### 1.1 这个项目的真实结构

桌面版不是「Electron 应用」，而是**一个纯 Node 后端 + 一个纯浏览器前端 + 一层很薄的 Electron 壳**。证据：

| 事实 | 数值 | 含义 |
|---|---|---|
| Renderer 里 `import 'electron'` / `'fs'` / `'node:*'` | **0 处** | 前端天然是 Web 前端，不需要改造 |
| Renderer 文件数 / 行数 | 169 / 43,860 | 可原样复用 |
| `window.yibiao` 调用 | 389 处，集中在 60 文件 | 收敛在一个入口，替换面可控 |
| bridge 接口 | 221 个 invoke + 21 个事件 | 一一映射 HTTP POST + SSE |
| Main 服务层 | 56,816 行（`electron/services/*.cjs`） | 真正要迁移的业务逻辑 |
| C# OpenXmlHelper | 23,172 行，82 文件，独立进程 | **不用改，Go 直接 spawn** |
| Preload + IPC 注册 | 1,668 + 1,021 行 | 全部丢弃，换成 Go 路由 |

**关键洞察：整本 Word 导出不经过 Node。** `technicalPlanExport.cjs` 对 `docx` npm 库的引用数为 **0**，主流程是拼接 HTML 后直接交给 C# `insert-body-html` 动作渲染。那 3,053 行 `exportService.cjs` 里用 `docx` 库的部分只服务次要路径（Markdown 导出、可行性报告）。这意味着**最难的 Word 排版不用 Go 重写**。

### 1.2 三块不能 Go 化的部分

| 部分 | 行数 | 原因 | 结论 |
|---|---|---|---|
| `@earendil-works/pi-coding-agent` 包装层 | 5,390（`services/pi/`） | 闭源私有 SDK，只有 JS 实现 | **保留为 Node sidecar** |
| `docx-editor.dev` 布局测量 | 依赖 DOM + Canvas | 服务端无 DOM | **headless Chromium sidecar** |
| `better-sqlite3` → PostgreSQL | 8,950（Store） | 数据库换了，写法必须重写 | **Go 重写，收益最大** |

### 1.3 一句话架构

```
浏览器（复用 43,860 行 React）
    │ HTTP + SSE
    ▼
Go 主服务（业务编排 / 认证 / 数据 / 任务 / 文件）
    ├─ HTTP ──▶ Node Agent Sidecar（Pi SDK，5,390 行原样保留）
    ├─ stdio ─▶ C# OpenXmlHelper（23,172 行原样保留）
    ├─ CDP ──▶ Chromium Sidecar（mermaid 转图 / Word 布局测量）
    └─ SQL ──▶ PostgreSQL
```

---

## 二、Go 侧技术选型

### 2.1 核心栈

| 层 | 选型 | 理由 |
|---|---|---|
| HTTP 框架 | **Echo v5** 或 chi | 项目已有 Echo 经验；路由分组清晰，中间件生态够用。chi 更轻但需自己搭 |
| 数据库 | **PostgreSQL 17** + `pgx/v5` | 原生 JSON、并发写、`FOR UPDATE SKIP LOCKED` 做任务队列 |
| SQL 层 | **sqlc** | 从 SQL 生成类型安全 Go 代码，56 张表手工写 ORM 模型成本太高 |
| 迁移 | **goose** | 纯 SQL 迁移，可读可审，比 gorm automigrate 可控 |
| 配置 | **koanf** + 环境变量 | 比 viper 轻，支持 TOML/YAML/env 多层覆盖 |
| 日志 | **slog**（标准库） | Go 1.21+ 原生，结构化，够用 |
| 认证 | **JWT**（`golang-jwt/jwt/v5`）+ bcrypt | 前期单租户够用；多租户时 JWT 加 `tenant_id` claim |
| 文件存储 | **本地磁盘接口 + S3 兼容实现** | 接口抽象，前期落盘，后期换 MinIO/R2 只改实现 |
| 任务队列 | **PG 表 + `SKIP LOCKED`** | 见 §5.2，不引入 Redis/RabbitMQ |
| 实时推送 | **SSE**（`text/event-stream`） | 桌面版 21 个事件全是单向推送，不需要 WebSocket |
| 进程管理 | `os/exec` + context 取消 | 子进程生命周期绑定请求/任务 context |
| 定时任务 | `robfig/cron/v3` | 会员到期、积分结算、清理任务 |

### 2.2 为什么不选这些

- **不用 GORM**：56 张表、大量 JSON 列、复杂 JOIN（技术方案目录树），GORM 的预加载和软删除会带来意外。sqlc 生成的代码直接可控。
- **不用 Redis**：任务量级是「几十个并发长任务」，不是高 QPS 短任务。PG 加 `SKIP LOCKED` 足够，少一个运维组件。后期真有性能瓶颈再加。
- **不用 WebSocket**：桌面版所有推送都是「服务端单向推进度」，SSE 更简单，还能自动重连。
- **不用 unioffice 生成 Word**：商业授权，且整本导出本来就走 C#。

### 2.3 Word 相关的 Go 库评估

**结论：Go 侧不生成 Word，全部交给 C#。** 但有几处需要 Go 做轻量处理：

| 需求 | 方案 |
|---|---|
| 读 docx 拿文本（解析招标文件前的预检） | `archive/zip` + `encoding/xml` 直接读 `word/document.xml` |
| 生成 Excel（查重/废标导出） | **`excelize/v2`**，成熟稳定 |
| 读 PDF 文本 | **`pdfcpu`** 或 `ledongthuc/pdf`；复杂排版走 C# 或 LibreOffice |
| 读 xlsx | `excelize/v2` |
| doc → docx 转换 | LibreOffice headless（与桌面版一致的降级路径） |

如果将来真需要 Go 生成 docx（比如可行性报告的简单表格），用 **`github.com/aksteps/godocx`**（MIT，对标 python-docx，2026 年活跃，样式/节/页眉页脚齐全）。**不要用 unioffice**，写入需商业授权。

---

## 三、进程边界设计

这是整个方案的核心。边界的划分原则是：**能在 Go 里干净重写的重写；依赖闭源 JS SDK 或浏览器 DOM 的保留为 sidecar。**

### 3.1 Go 主服务（新建）

承接原 `electron/services/` 里所有不依赖 Electron 和浏览器 DOM 的部分：

| 原模块 | 行数 | Go 侧做法 |
|---|---|---|
| `*Store.cjs`（11 个） | 8,950 | 全部重写为 sqlc query |
| `aiService.cjs` | 2,839 | 重写：HTTP 客户端 + SSE 解析 + 队列 + 重试 |
| `taskService.cjs` | 2,061 | 重写：PG 队列表 + 状态机 |
| `fileService.cjs` | 866 | 重写：上传 + LibreOffice 调用 + 格式解析 |
| `doc2markdown/convert.mjs` | 1,633 | 重写：调用 LibreOffice/soffice，前置探测 |
| `*Task.cjs`（20 个） | 14,170 | 重写业务编排，Prompt 原样搬 |
| `knowledgeBaseService/Store` | ~2,500 | 重写 |
| `blindCheckStore/Presets/Review` | ~1,500 | 重写规则数据部分，检查本身交 C# |
| `credentialLibraryService` | ~800 | 重写 |
| `pluginService.cjs` | ~700 | **建议砍掉**（见 §9） |
| `updateService.cjs` | 618 | **删除**，Web 无客户端更新 |
| `licenseService.cjs` | ~400 | 删除，换会员/积分体系 |
| `donationService.cjs` | ~200 | 删除或改配置项 |
| `officialAccountService.cjs` | ~300 | 视业务决定，可延后 |
| `noticeMediaCacheService.cjs` | 174 | 重写为 CDN/对象存储 |
| `localImageRenderService.cjs` | 46 处 BrowserWindow 调用 | **改走 Chromium sidecar** |

### 3.2 Node Agent Sidecar（保留，改写接口）

`services/pi/` + `services/agent/` 共 8,393 行，核心是 `@earendil-works/pi-coding-agent`。

**保留原因**：这个 SDK 是项目作者依赖的闭源组件，提供 Agent 会话、工具调用、上下文压缩、提交校验、空转诊断等能力。用 Go 重写等于从零造一个 Agent 框架，不现实。

**改造方式**：

```
现状：Go/Electron Main --(进程内函数调用)--> piRuntimeService.cjs
改造：Go --(HTTP POST /agent/run)--> Node Sidecar --(SDK)--> Pi
```

Sidecar 是一个独立 Node 进程，暴露：

| 接口 | 作用 |
|---|---|
| `POST /sessions` | 创建 Agent 会话（workspace、工具集、阶段门禁） |
| `POST /sessions/:id/continue` | 阶段续接（对应 `continueTask`） |
| `POST /sessions/:id/answer` | 回答 Agent 提问（对应 `agent:answer-question`） |
| `POST /sessions/:id/pause` | 暂停 |
| `GET /sessions/:id/events` | SSE 推 Agent 状态、工具调用、进度 |
| `POST /sessions/:id/tools/:name` | **反向回调**：Agent 调用业务工具时，由 sidecar 请求 Go |

**关键设计：业务工具反向注册。** 桌面版 Agent 业务工具（`contentGenerationImageTools` 等）直接调 Main 的服务。Web 版改为：sidecar 里的工具实现只做 HTTP 转发到 Go，Go 执行完返回。这样业务逻辑仍全在 Go。

**Pi SDK 的模型请求**：桌面版走 `agentOpenAiProxy.cjs`（1,514 行）本地 HTTP server 代理到 `aiService`。Web 版简化：sidecar 直接配 `baseURL` 指向 Go 的 `/v1/chat/completions` 端点，由 Go 统一做模型路由、限流、计费（积分扣减）。**这一层刚好是多租户计费的天然切入点。**

### 3.3 C# OpenXmlHelper（原样保留）

23,172 行，已是独立进程，stdin/stdout 传 JSON，工作目录传文件。

**Web 版唯一变化**：`--workspace` 参数从「每安装一个」变成「每个项目一个」：

```
桌面版：userData/workspace/
Web 版：/data/tenants/<tenant_id>/projects/<project_id>/
```

并发问题：桌面版**一次只跑一个任务**（助手忙时后到任务直接失败）。Web 多用户必须解决。两个选项：

1. **进程池**：每个 C# 进程独占一个工作目录，Go 维护 `chan *HelperProcess` 池。简单，内存可控（每进程约 100-200MB），但池大小受内存限制。
2. **每个任务起新进程**：启动约 1-2 秒（.NET AOT 可降到 200ms），无状态，最省心。

**建议：方案 1 做成池 + 方案 2 做兜底**。池设上限（如 8 个），超限排队；长任务（整本导出）走独立进程避免阻塞池。

**必须解决的 Windows 依赖问题**：`officePdfExport.mjs` 用 PowerShell 调 Word/WPS COM。Linux 容器下这条路断了。见 §6.3。

### 3.4 Chromium Sidecar（新增）

两个用途，都依赖真实浏览器环境：

| 用途 | 桌面版实现 | Web 版替代 |
|---|---|---|
| Mermaid 转图 | `localImageRenderService.cjs` 隐藏 BrowserWindow | **headless Chromium + CDP** |
| Word 布局测量（页码、格式自检） | `contentGenerationLayout.cjs` 隐藏 BrowserWindow + `wordLayoutPreload.cjs` | **headless Chromium + CDP，同 preload 脚本改注入** |
| 双层 PDF 逐页渲染 | `dualLayerPdf/workerWindow.cjs` 隐藏窗口 | **headless Chromium** |

**实现选择**：

- **`chromedp`**（Go 官方推荐，CDP 协议）：Go 直接控制浏览器，无需 Node。**首选。**
- **Playwright Go**：更上层，但依赖 Node/Python driver，多一层。
- **`go-rod`**：chromedp 的友好封装，API 更好用。

**建议 `go-rod` 或 `chromedp`**，起一个长期驻留的浏览器实例，用多 tab / 多 context 隔离并发任务。

**需要改的 preload 脚本**：`wordLayoutPreload.cjs` 用了 `contextBridge` + `fs.readFileSync`。改成在页面里 fetch 一个 `blob:` 或 `data:` URL，用 chromedp 的 `Runtime.evaluate` 注入。

### 3.5 边界汇总表

| 能力 | 归属 | 理由 |
|---|---|---|
| 认证/权限/会员/积分 | Go | 新写 |
| 所有业务数据读写 | Go + PG | 必须重写 |
| AI 模型调用 | Go | 统一路由 + 计费 |
| Agent 会话编排 | Node sidecar | 闭源 SDK |
| Agent 业务工具 | Go（sidecar 反向回调） | 业务逻辑归 Go |
| Word 结构操作 | C# helper | 原样保留 |
| HTML → Word 排版 | C# helper | 原样保留 |
| 整本导出编排 | Go | 原 `technicalPlanExport.cjs` |
| Mermaid / HTML 转图 | Chromium | 需要真实渲染 |
| Word 布局测量 | Chromium | 需要真实排版引擎 |
| 文档解析（docx/pdf/xlsx） | Go + LibreOffice | 纯文本提取 Go 够用 |
| 双层 PDF | Chromium + Go | 逐页渲染 + Go 写 PDF |
| 前端 UI | React 原样 | 只换 bridge 实现 |

---

## 四、前端迁移策略

### 4.1 核心思路：换掉 bridge，不碰组件

桌面版所有前端调用长这样：

```ts
await window.yibiao.technicalPlan.loadState()
window.yibiao.tasks.onTaskEvent(cb)  // 返回取消订阅函数
```

Web 版保持**完全相同的调用形态**，只替换 `window.yibiao` 的实现：

```
src/shared/bridge/
├── index.ts          // 导出 createBridge()，运行时选择实现
├── desktop.ts        // 原样：直接用 window.yibiao（Electron 注入）
├── web.ts            // 新写：HTTP + SSE 实现
└── types.ts          // 复用 src/shared/types/ipc.ts 的全部类型
```

**为什么能在不改造件的前提下做到**：现有 389 处调用全部是 `Promise` 返回 + `onXxx(cb) => unsubscribe` 模式，与 HTTP/SSE 天然同构。唯一的同步方法是 `appName`、`platform`、`getPathForFile`（见下）。

### 4.2 需要改的前端代码（极少）

| 改动点 | 影响文件 | 改法 |
|---|---|---|
| `window.yibiao` 直接引用 | 60 个文件 | 全局替换为 `import { bridge } from '@/shared/bridge'`，一次性 sed + 人工核对 |
| `file.getPathForFile(file)` | 12 处 | Web 版返回 `undefined`，改走上传接口 |
| `app.platform` / `appName` | 数处 | 保留常量 |
| 文件选择对话框 | 6 处 `selectXxxFiles()` | 改用 `<input type="file">` + 上传，或保持接口但返回已上传的 fileId |
| `dialog.showSaveDialog` 相关 | 导出流程 | 改成 `Content-Disposition` 下载 |
| `yibiao-asset://` 图片协议 | `main.cjs` 注册 | Web 版换成 `/api/assets/<id>` |
| 拖拽上传 | 10 处 `dataTransfer.files` | 保持，`getPathForFile` 后改为直接 `FormData` 上传 |

**关于 `getPathForFile` 的处理建议**：不要改 12 处调用点。让 `bridge.file.getPathForFile(file)` 在 Web 版返回一个 `upload://<临时id>` 伪路径，同时立刻发起后台上传。后端接口收到非本地路径时查上传表。这样前端零改动，代价是后端要处理这个伪协议。

### 4.3 路由

桌面版 `AppRouter` 是组件分发（非 URL 路由）。Web 版**建议改成真实 URL 路由**，理由：

- 浏览器刷新不丢当前页
- 可分享链接（给客户演示时有用）
- 前进后退可用

改动量：`src/app/AppRouter.tsx` + `navigation.ts`，约 300 行。

### 4.4 多窗口

桌面版有 3 个独立窗口（主窗口、token 统计、Agent 监控）+ 2 个隐藏窗口。Web 版：

- token 统计、Agent 监控 → **改成路由页面**，或删掉（开发者功能）
- 双层 PDF 处理窗口 → **完全移到服务端 Chromium**
- 格式自检隐藏窗口 → **完全移到服务端 Chromium**

---

## 五、数据层迁移

### 5.1 现状：这是最大的坑

| 事实 | 影响 |
|---|---|
| 56 张表 | 数量中等，可逐个处理 |
| **12 张表是 `CHECK (id = 1)` 单例行** | **模型没有「项目」概念**，一个工作区一份数据 |
| 全 schema 只有 1 处 `project_id`（`credential_library_projects`，语义是「投标业绩项目」不是「工作区分组」） | 同上 |
| 488 个 TEXT 列、125 个 INTEGER、8 个 REAL | 类型简单，迁移不复杂 |
| 43 个迁移版本、87 条迁移语句 | 说明 schema 演进频繁，需保证 Web 版有等价机制 |

**这是个结构性缺陷，不是技术细节。** 桌面版的隐含假设是「一份安装 = 一个工作区 = 一个投标项目」。Web 版多用户多项目，必须给每张业务表加上 `tenant_id` + `project_id`。

举例：`technical_plan_meta` 现在是 `CHECK (id = 1)`，只能存一行。Web 版必须改成 `PRIMARY KEY (project_id)`，并且加 `tenant_id` 做租户隔离。

### 5.2 PostgreSQL schema 设计

**分三层**：

```
控制面（新写）
├── tenants            租户
├── users              用户
├── memberships        用户-租户关系 + 角色
├── plans              会员等级定义
├── subscriptions      用户订阅
├── credit_accounts    积分账户
├── credit_ledgers     积分流水（append-only）
├── usage_records      用量记录（token/图片/导出）
└── audit_logs         审计

项目面（从 SQLite 迁移，加 project_id）
├── projects           投标项目（新）
├── technical_plan_*   原 12 张
├── duplicate_check_*  原 11 张
├── rejection_check_*  原 10 张
├── blind_check_*      原 7 张
└── feasibility_report_* 原 3 张

共享面（租户级，加 tenant_id 不加 project_id）
├── knowledge_*        原 11 张
├── credential_library_* 原 6 张
└── export_templates   原 1 张
```

**类型映射规则**：

| SQLite | PostgreSQL | 说明 |
|---|---|---|
| `INTEGER PRIMARY KEY` | `BIGINT GENERATED ALWAYS AS IDENTITY` 或 `UUID` | 业务 ID 建议 UUID（防枚举），自增留内部排序 |
| `TEXT` 存 JSON | `JSONB` | 大量 `*_json` 列，转 JSONB 可查询 |
| `TEXT` 存时间 | `TIMESTAMPTZ` | 需检查现有写入格式 |
| `INTEGER` 布尔 | `BOOLEAN` | 大量 `0/1` 语义列 |
| `REAL` | `DOUBLE PRECISION` | 仅 8 处 |
| `CHECK (id = 1)` | 去掉，改 `PRIMARY KEY (project_id)` | **12 张表必须改** |

**索引必备**：每张业务表 `(tenant_id, project_id)` 组合索引，否则多租户查询会全表扫。

### 5.3 数据迁移工具

现有桌面用户有真实数据（SQLite 文件）。提供迁移工具：

```
go run ./cmd/migrate-sqlite --db data/yibiao.sqlite --tenant <id> --project <name>
```

读取 SQLite（`modernc.org/sqlite` 纯 Go，免 CGO）→ 转 PG。按「一个 SQLite 文件 = 一个项目」处理。

---

## 六、Word 处理链路

### 6.1 整本导出（核心价值，必须做到位）

桌面版流程：

```
technicalPlanExport.cjs
  ├─ 读目录树（Store）
  ├─ 逐节点读 HTML（Agent 会话工作区）
  ├─ 拼成完整 HTML
  ├─ 商务模版回填（C# renderBusinessTemplate）
  └─ C# insert-body-html → docx
      └─ C# 生成目录域、页码域、书签
```

Web 版：Go 重写前半段（读数据 + 拼 HTML），后半段原样调 C#。

**页码测量是难点**：桌面版用隐藏窗口跑 `docx-editor` 测量，Web 版改 Chromium sidecar。这是 `technicalPlanToc.cjs` 的逻辑，需要仔细移植。

### 6.2 布局测量（格式自检）

`contentGenerationLayout.cjs` 在隐藏窗口注入 preload，调 `@docx-editor.dev/core/layout` 的 `resolveDefaultSurfaceMeasurer`（需要 Canvas 2D context 做字体度量）。

Web 版方案：
1. Chromium sidecar 打开一个空白页
2. 注入 `docx-editor` 的 ESM bundle + 一个暴露 `readLayout(bytes)` 的脚本
3. Go 通过 CDP `Runtime.evaluate` 传文件字节（base64 或 blob URL），取回测量结果

**注意**：`patches/@docx-editor.dev+core+2.15.0.patch` 必须一起带过去，否则行距和表格分页行为与 Word 不一致。

### 6.3 Word → PDF（部署在 Linux 的代价）

`officePdfExport.mjs` 顺序尝试：Word COM → WPS COM → LibreOffice。

Linux 容器只有 LibreOffice 一条路。影响：

| 场景 | 影响程度 |
|---|---|
| 双层 PDF 转换 | **可用**，但复杂排版（页框、浮动图、文本框）可能有偏差 |
| 招标文件 doc → docx | 可用 |
| 整本导出 | **不受影响**（不经过 PDF 转换） |
| 暗标检查 | 不受影响（只读 docx） |

**必须向客户说明**：Linux 下转 PDF 的排版还原不如 Word。如果客户对标书 PDF 排版要求高，这一项要评估。

**缓解方案**：保留一条「远程 Windows 转换器」的可选路径——Go 把文件传给一个独立的 Windows worker（可以就是一台内网 Windows 机器跑个小服务），转完取回。接口抽象好，前期不实现。

---

## 七、多租户 / 会员 / 积分预留

你说前期单公司定制，后期公网运营。架构上必须现在就埋好，否则后面改起来伤筋动骨。

### 7.1 现在就要做的（单租户也要）

- **所有业务表带 `tenant_id`**：单租户时全填默认租户 ID，代码路径与多租户完全一致
- **所有查询强制带 `tenant_id` 条件**：写在 sqlc query 里，不靠应用层记得加
- **文件路径带租户**：`/data/tenants/<tenant_id>/...`
- **JWT 带 `tenant_id` + `user_id`**：中间件解析后注入 context
- **积分扣减走中间层**：AI 调用、图片生成、Word 导出三个消耗点，统一经过 `billing.Consume(ctx, kind, amount)`。单租户时是 no-op 或只记录

### 7.2 会员与积分模型建议

```
plans            等级定义：free / pro / enterprise，含 token 配额、并发数、功能开关
subscriptions    用户订阅：plan_id, start_at, end_at, status
credit_accounts  余额：user_id, balance
credit_ledgers   流水：user_id, delta, kind, ref_id, balance_after, created_at
usage_records    用量明细：user_id, kind(token/image/export), amount, model, project_id
```

**积分扣减必须幂等**：用 `ref_id`（如 `task_id + step`）做唯一约束，避免重试重复扣。

**计费时机**：AI 请求是按 token 计，必须**在流式响应结束时**扣，中途断开按已用量扣。这是 `aiService.cjs` 里已有的 token 统计逻辑（`textTokenStatsStore`），移过来即可。

### 7.3 现在不做但别堵死的

- 支付（微信/支付宝）：只留 `orders` 表结构和回调接口占位
- 发票：桌面版有 `official_invoice_info`，单租户时可保留
- 租户自助注册：前期只有管理员创建

---

## 八、分阶段实施计划

「不怕时间、要细致」的前提下，建议按**能独立验收的垂直切片**推进，不要横向一次迁一层。

### 阶段 0：地基（2-3 周）

| 任务 | 产出 |
|---|---|
| Go 项目骨架 | 目录结构、配置、日志、错误处理、中间件 |
| PG schema 设计 + goose 迁移 | 全部 56 张表的新 schema |
| sqlc 配置 + 前 10 张核心表 query | 验证工具链 |
| 认证（注册/登录/JWT/中间件） | 单租户版 |
| 对象存储抽象 + 本地实现 | 上传下载 |
| **bridge 适配层骨架** | `web.ts` 的 invoke/event 通用实现 |
| Echo + SSE 基础设施 | 事件总线（内存版，单实例够用） |

**验收**：能用账号登录，前端能调到 `/api/config/load` 并拿到数据。

### 阶段 1：技术方案主链路（6-8 周）

这是核心价值，必须第一个完整打通。

| 任务 | 依赖 |
|---|---|
| 招标文件上传 + 解析（docx/pdf → markdown） | 文件服务、LibreOffice |
| `technical_plan_*` 全部 Store | sqlc |
| AI 服务（文本 + 图片，流式，队列，重试，token 统计） | aiService 移植 |
| Node Agent Sidecar + Pi SDK 接入 | 关键路径 |
| Prompt 迁移（全部 `*AgentConfig.cjs` + `resources/`） | 原样搬 |
| 标书分析任务 | Agent + Store |
| 目录生成任务 V2 | 最复杂，1806 行 |
| 全局事实任务 | |
| 正文生成任务 | 最复杂，3113 行 |
| C# helper 进程池 | |
| Chromium sidecar（mermaid + 布局测量） | |
| 整本导出 | C# + 页码测量 |

**验收**：从上传招标文件到导出完整标书 docx，与桌面版输出**逐页比对**一致。

### 阶段 2：商务标 + 配套（4-5 周）

| 任务 |
|---|
| 投标模版提取（`templateExtractionTask`，993 行，最绕的分组并发逻辑） |
| 商务模版填写（`businessTemplateFillTask`，1336 行） |
| 规则和页码编制（`rulePageCompileTask`，617 行） |
| 导出格式模板（`export_templates` + 格式自检） |

**验收**：一个完整投标项目（技术标 + 商务标 + 目录页码）能导出。

### 阶段 3：知识库 + 资信库（3-4 周）

| 任务 |
|---|
| 知识库（11 张表 + 解析 pipeline + 匹配） |
| 资信库（6 张表 + 图片管理） |

### 阶段 4：检查类（4-5 周，可延后）

| 任务 |
|---|
| 废标项检查（1745 行） |
| 标书查重（987 行 Store + 算法） |
| 暗标检查（依赖 C# `blind-check-*` + 图片灰度） |

### 阶段 5：多租户 + 计费（3-4 周）

| 任务 |
|---|
| 租户管理、用户审批、模块权限 |
| 会员等级、积分、用量统计 |
| 管理后台 |

### 阶段 6：运维与上线（2-3 周）

| 任务 |
|---|
| Docker 镜像（Go + Node + .NET + Chromium + LibreOffice） |
| 监控、日志聚合、备份 |
| 压力测试（重点：C# helper 池、Chromium 并发） |

**总计约 24-32 周**（单人全职）。如果并行投入 2-3 人，可压到 14-18 周，但 Agent 和正文生成这条链路建议一人从头跟到尾，避免交接损失。

### 阶段依赖图

```
阶段0 ──▶ 阶段1 ──▶ 阶段2 ──▶ 阶段3
             │         │
             └─────────┴──▶ 阶段4（可并行）
                          │
阶段5（等阶段2完成后可并行）──┘
                          ▼
                       阶段6
```

---

## 九、功能取舍建议

桌面版有些功能在 Web 场景下价值低、成本高，建议**明确砍掉**，避免拖累主线。

| 功能 | 建议 | 理由 |
|---|---|---|
| 插件系统（`pluginService`） | **砍掉** | 插件是本地代码执行，Web 下是安全灾难；且依赖外部市场 |
| 自动更新（`updateService`） | **砍掉** | Web 无需 |
| 官方账号/捐赠/发票 | **延后或砍掉** | 与核心价值无关，公网运营时重新设计 |
| 开发者工具页面（6 个） | **砍掉** | 内部调试用，不对外开放 |
| 双层 PDF（`dualLayerPdf`） | **降级保留** | 依赖 Office COM，Linux 下能力受限，放阶段 4 之后 |
| 可行研究报告（`feasibility-report`） | **延后** | 独立业务线，3 张表 + 508 行任务，可后置 |
| 投放资源页（`resources`） | 保留但简化 | 只是外链列表 |
| 公告/更新日志 | **简化** | 保留公告，更新日志改静态页 |

**砍掉这些能省约 6-8 周。**

---

## 十、风险清单

按严重程度排序，每条都给出应对。

### R1（严重）Pi Agent SDK 不可移植

**风险**：`@earendil-works/pi-coding-agent` 是闭源 npm 包。如果作者停止维护、改协议、或限制商用，整个 Agent 链路会断。

**证据**：`services/pi/` 5,390 行全是围绕这个 SDK 的包装，业务价值高度依赖它。

**应对**：
1. 短期：sidecar 模式已经把它隔离成一个可替换组件
2. 中期：在 sidecar 内部定义稳定的 `AgentRuntime` 接口（`createSession` / `continue` / `abort` / `answer`），SDK 只做具体实现
3. 长期：如果 SDK 出问题，可换成同样模式的替代（Claude Agent SDK、或自研轻量 Agent 循环）。接口不变，Go 侧零改动

**现在就要做的**：把 sidecar 的 HTTP 接口设计成通用 Agent 语义，不要暴露 Pi 特有的概念（`task_complete`、`fixed_tool_list` 这些要包一层）。

### R2（严重）Word 导出的一致性

**风险**：整本导出是产品核心价值，牵涉 C# 23k 行 + 页码测量 + 商务模版回填。任何一环在 Go 侧重写时出偏差，导出的标书就废了。

**应对**：
1. **建立黄金样本回归**：用桌面版对 5-10 份真实标书导出 docx，作为基线
2. Go 版导出结果与基线做**结构级 diff**（解压比 XML，不比二进制）
3. 页码测量单独验证：桌面版和 Chromium 版对同一文档测出的页数必须一致
4. `patches/` 里的 docx-editor 补丁必须一起移植

### R3（严重）数据模型无项目概念

**风险**：12 张 `CHECK (id = 1)` 单例表。如果只是机械翻译 schema，Web 版会变成「一个部署只能有一个项目」。

**应对**：阶段 0 就把 schema 设计对，不要「先 1:1 迁，后面再加 project_id」。加 project_id 会让所有 query、所有索引、所有唯一约束全部重来。

### R4（中）C# helper 并发

**风险**：桌面版一次一个任务。Web 多用户并发时，池化设计不当会阻塞或内存爆掉。

**应对**：
1. 每项目一个长期 helper 进程（复用，减少启动开销）
2. 池上限可配，超限排队并推送排队事件给前端
3. 长任务（整本导出）用独立进程，不占池
4. 进程崩溃自动重启 + 任务失败上报
5. 压测目标：8 个并发导出不失败

### R5（中）Agent 任务的长连接与超时

**风险**：正文生成跑 30 分钟以上。HTTP 长连接、SSE、反代超时都会踩。

**应对**：
1. 任务**异步化**：`POST /tasks` 立即返回 task_id，前端轮询/订阅
2. SSE 只推进度，不承载任务生命周期
3. 反代（nginx）关掉 `proxy_read_timeout`，或设为 1 小时
4. 心跳：SSE 每 15 秒发 comment 保活
5. **任务状态必须落库**，服务重启后能恢复（桌面版已有此设计，`checkpointTask`）

### R6（中）Linux 下 Word 转 PDF 的排版偏差

**风险**：见 §6.3。复杂标书转 PDF 后可能错位。

**应对**：明确告知客户限制；保留 Windows worker 扩展点；PDF 相关功能标注「建议用 Word 打开确认」。

### R7（中）AGPL 合规

**风险**：AGPL-3.0 only。公网提供服务必须向用户提供完整源码。

**应对**：
1. 现在就在网站页脚和「关于」页加源码链接（指向你 fork 的仓库）
2. 修改过的部分同样要开源，不能用 AGPL + 商业双许可（除非拿到原作者授权）
3. 如果将来要做闭源 SaaS，**先找原作者谈商业授权**，这是唯一合法路径
4. NOTICE 文件必须保留原作者署名

### R8（低）前端 43k 行的隐性耦合

**风险**：虽然 grep 显示 0 处直接 import，但可能有依赖 Electron 行为的隐性假设（如 `window.open`、`localStorage` 限制、快捷键）。

**应对**：阶段 0 就把前端跑在浏览器里，逐个页面点一遍，列出问题清单。

### R9（低）提示词与模型行为漂移

**风险**：桌面版 Prompt 是针对特定模型调优的（README 提到 `gpt-5.6-terra`、`deepseek-flash`）。Web 版如果换模型，生成质量可能下降。

**应对**：Prompt **逐字搬迁，不改一个字**。先保证同样的 Prompt + 同样的模型得到同样结果，再考虑优化。

---

## 十一、目录结构建议

```
yibiao-web/
├── cmd/
│   ├── server/            # Go 主服务入口
│   └── migrate-sqlite/    # SQLite → PG 迁移工具
├── internal/
│   ├── config/
│   ├── http/              # Echo 路由、中间件、handler
│   │   ├── middleware/    # 认证、租户、限流、日志
│   │   └── handler/       # 按业务域分文件
│   ├── domain/            # 业务模型（非 DB 模型）
│   ├── store/             # sqlc 生成的 query + 手写事务
│   ├── service/           # 业务逻辑
│   │   ├── ai/            # 模型调用、队列、token 统计
│   │   ├── billing/       # 积分、用量
│   │   ├── task/          # 任务编排、状态机
│   │   ├── agent/         # sidecar 客户端 + 工具反向注册
│   │   ├── doc/           # 文档解析、转 markdown
│   │   ├── word/          # C# helper 池、导出编排
│   │   ├── render/        # Chromium sidecar 客户端
│   │   └── storage/       # 文件存储抽象
│   └── events/            # 事件总线、SSE
├── pkg/
│   └── openxml/           # C# helper 协议客户端（可复用）
├── migrations/            # goose SQL
├── sqlc/                  # sqlc.yaml + query/*.sql
├── sidecars/
│   ├── agent/             # Node：Pi SDK 包装（从 client/electron/services/pi 迁）
│   │   ├── src/
│   │   ├── package.json
│   │   └── Dockerfile
│   └── openxmlhelper/     # C#：原样复制
├── web/                   # 前端（从 client/src 迁）
│   ├── src/
│   │   ├── features/      # 原样
│   │   ├── shared/
│   │   │   └── bridge/    # 新：desktop.ts / web.ts
│   │   └── app/
│   └── package.json
├── deploy/
│   ├── docker-compose.yml
│   ├── Dockerfile         # 多阶段：Go + .NET + Node + Chromium
│   └── nginx.conf
└── docs/
```

**前端为什么放在同一仓库**：`shared/types/ipc.ts` 那 1,015 行类型定义是前后端契约的唯一来源。放一起才能保证同步。

---

## 十二、和 jdcome 版本的取舍

参考它的地方：

| 可取之处 | 采纳 |
|---|---|
| Fastify + Prisma + PG 的「服务端权威」思路 | 采纳，换成 Go |
| 多用户 + 模块权限 + 项目作用域（`X-Project-Id`） | 采纳 |
| SSE 推任务进度 | 采纳 |
| 浏览器端不存 API Key | 采纳 |
| Nginx 静态 + 反代 `/api` 的部署形态 | 采纳 |
| 内置使用文档（seed 进 DB） | 采纳，成本低价值高 |
| AGPL 合规说明 | 采纳，直接借鉴措辞 |

不采纳的地方：

| 它的做法 | 为什么不用 |
|---|---|
| **完全重写前端页面** | 它重写了 `TechnicalPlanHome` 等（63k vs 桌面版 74k），功能倒退。我们直接复用桌面版 |
| **不要 Pi Agent，用简化 Agent** | 导致生成质量明显下降，这是它的核心短板 |
| **Prisma** | 56 张表 + 复杂 JOIN，Prisma 的关系定义会成为负担。Go 侧用 sqlc |
| **Apache/其他 License 表述** | 它是 AGPL-3.0 only，与原项目一致，无冲突 |

---

## 十三、立即可做的第一步

不要先写代码，先做**三件验证性的事**，每件都能在 1-2 天内出结论：

### 验证 A：前端能否在浏览器跑起来

```bash
cd client
# 临时把 preload 换成 mock，直接 vite dev
npm run dev:vite-only
```

打开 `http://127.0.0.1:5173`，逐个页面点，记录：
- 哪些页面白屏（说明有隐性 Electron 依赖）
- 哪些操作报错（说明 bridge 设计有同步假设）

**产出**：一份「前端可移植性清单」，决定是否需要阶段 0 增加改造工作。

### 验证 B：C# helper 在 Linux 容器里能否跑

```bash
cd openxmlhelper
dotnet publish -c Release -r linux-x64 --self-contained
# 在 docker 里跑 ping 和 list-blocks
```

**产出**：确认 .NET 10 自包含发布在 Linux 下可用，`DocumentFormat.OpenXml` 无平台依赖。

### 验证 C：Pi SDK 能否脱离 Electron 跑

写一个 20 行的 Node 脚本，直接 `import('@earendil-works/pi-coding-agent')`，创建 session，发一句 prompt，看能否跑通（不经过 Electron）。

**产出**：确认 sidecar 方案可行。这是整个架构的基石，**必须先验证**。

---

## 附：关键文件对照索引

迁移时按这个表找代码：

| 桌面版路径 | Web 版归属 | 备注 |
|---|---|---|
| `client/electron/services/*Store.cjs` | `internal/store/` | sqlc |
| `client/electron/services/aiService.cjs` | `internal/service/ai/` | 重点 |
| `client/electron/services/taskService.cjs` | `internal/service/task/` | 状态机 |
| `client/electron/services/pi/` | `sidecars/agent/src/` | 近乎原样 |
| `client/electron/services/agent/` | `sidecars/agent/src/proxy/` | 改造成 Go 回调 |
| `client/electron/services/*Task.cjs` | `internal/service/<domain>/` | Prompt 原样 |
| `client/electron/services/technicalPlanExport.cjs` | `internal/service/word/` | |
| `client/electron/services/localImageRenderService.cjs` | `internal/service/render/` | chromedp |
| `client/electron/services/contentGenerationLayout.cjs` | `internal/service/render/` | chromedp |
| `client/electron/resources/` | `sidecars/agent/resources/` | Agent 指令 |
| `client/src/**` | `web/src/**` | 原样 + bridge |
| `client/src/shared/types/ipc.ts` | `web/src/shared/bridge/types.ts` | 契约源 |
| `client/patches/` | `sidecars/agent/patches/` | 必须带 |
| `client/vendor/agent-tools/` | `sidecars/agent/vendor/` | rg/fd/jq |
| `openxmlhelper/` | `sidecars/openxmlhelper/` | 原样 |
| `sql/workspace_schema.sql` | `migrations/*.sql` | 重写 |
| `analytics/` | 独立保留 | 埋点服务不动 |
