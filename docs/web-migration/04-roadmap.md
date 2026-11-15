# 分阶段实施路线图

> 前提：单人全职，边做边学
> 总量：约 **26-34 周**（含缓冲）
> 原则：每阶段交付可验收的垂直切片，不做横向的一次性搬迁

---

## 一、先纠正一个认知

「从桌面版迁移」不是「把 JS 翻译成 Go」。真实情况是三种不同的工作：

| 工作类型 | 占比 | 特点 |
|---|---|---|
| **翻译**：逻辑已知，换语言实现 | 约 40% | 机械但量大，如 Store、纯计算工具 |
| **重构**：逻辑要改，因为架构变了 | 约 35% | 风险高，如任务系统、多租户、文件路径 |
| **搬运**：原样复制 | 约 25% | 近乎零成本，如 Prompt、C# 助手、前端 UI |

**最大的认知陷阱**：以为 56,816 行 Node 代码要全部翻译。实际上：

- 8,950 行 Store → 用 sqlc 重写，实际手写 SQL 约 3,000 行（省 2/3）
- 5,390 行 pi 包装 → 搬过去改接口，约 500 行改动
- 14,170 行业务任务 → 翻译但可以分批，Prompt 部分零成本
- 3,053 行 exportService → 核心路径不碰（走 C#），只需搬 ~800 行编排
- 8,000+ 行间接删除（插件/更新/捐赠/官方账号/开发者工具）

**净翻译量约 20,000-24,000 行 Go**，加上新增的认证/计费/任务系统约 8,000 行，总量约 **28,000-32,000 行 Go**。

按熟练 Go 开发者日均 150-200 行有效代码（含调试、测试），约 160-200 人日。加设计、联调、验收，**26-34 周是合理估计**。

---

## 二、阶段划分总览

```
阶段 0  地基与验证           3 周   ← 必须第一个做，且必须先做验证
阶段 1  技术方案主链路        8 周   ← 核心价值，最关键
阶段 2  商务标 + 导出完整化    5 周
阶段 3  知识库 + 资信库       4 周
阶段 4  检查类功能           5 周   ← 可延后
阶段 5  多租户 + 计费         4 周   ← 前后端可并行
阶段 6  运维与上线           3 周
─────────────────────────────────
                        合计 32 周
```

**关键路径**：阶段 0 的全部验证 → 阶段 1 的 Agent 接入 → 阶段 1 的正文生成 → 阶段 2 的导出。

---

## 阶段 0：地基与验证（3 周）

### 为什么单独设这一个阶段

因为有三个「不知道能不能行」的问题必须在写业务代码前回答。任何一个不成立，整个方案要改。

### 任务清单

| # | 任务 | 产出 | 预估 |
|---|---|---|---|
| 0.1 | **验证：前端能否在浏览器跑** | 见下 | 1 天 |
| 0.2 | **验证：C# 助手能否 Linux 编译运行** | 见下 | 1 天 |
| 0.3 | **验证：headless Chromium 跑 docx-editor 布局测量** | 见下 | 2 天 |
| 0.4 | 验证：chromedp 驱动 mermaid 转图 | 一个能出 PNG 的 Go 程序 | 1 天 |
| 0.5 | Go 项目骨架 | 目录、配置、日志、错误、中间件 | 3 天 |
| 0.6 | PG schema 设计与 goose 迁移 | 全部 62 张表目标结构 | 4 天 |
| 0.7 | sqlc 配置 + 前 10 张核心表 query | 验证工具链 | 2 天 |
| 0.8 | 认证（登录/JWT/刷新/中间件） | 能登录拿到 token | 3 天 |
| 0.9 | 对象存储抽象 + 本地实现 | 上传/下载/删除 | 2 天 |
| 0.10 | 文件上传接口 + `upload://` 伪路径机制 | 支持 12 处前端零改动 | 2 天 |
| 0.11 | SSE 事件总线（内存版） | 支持 topic 订阅/分发 | 2 天 |
| 0.12 | `web.ts` bridge 适配层骨架（Proxy 版） | 能调通任意一个接口 | 3 天 |

### 三个验证的具体做法

**0.1 前端跑通**

```bash
cd client
npm ci                                              # 30+ 分钟
npx vite --host 127.0.0.1 --port 5173               # 不启 Electron

# 另开终端，注入 mock bridge 让页面能渲染
# 在 index.html 里临时加：
# <script>
#   window.yibiao = new Proxy({}, { get: () => () => new Promise(()=>{}) });
# </script>
```

**验收**：登录页/主界面能渲染出来，侧边栏能点，控制台除数据请求失败外无其他错误。

**产出**：一份「页面可渲染性清单」，列出哪些页面白屏、哪些报错。

---

**0.2 C# Linux 编译**

```bash
cd openxmlhelper
dotnet publish src/OpenXmlHelper -c Release -r linux-x64 --self-contained \
  -p:PublishSingleFile=true -p:InvariantGlobalization=false -o ./publish-linux

# 验证
docker run --rm -v $PWD/publish-linux:/app:ro -v /tmp/ws:/ws \
  mcr.microsoft.com/dotnet/runtime:10.0 /app/openxmlhelper --workspace /ws
```

**验收**：`ping` 动作返回 `ok:true`，`list-blocks` 能读一个真实 docx。

---

**0.3 布局测量（最关键）**

这是唯一可能推翻方案的点。

```go
// 伪代码
browser := rod.New().MustConnect()
page := browser.MustPage("about:blank")
page.MustEval(`async () => {
  const core = await import('/static/docx-editor/index.mjs');
  const measurer = core.layout.resolveDefaultSurfaceMeasurer(96/72, {
    context: document.createElement('canvas').getContext('2d')
  });
  window.measure = (bytes) => {
    const opened = core.export.openDocumentForExport(bytes, { measurer: measurer.measurer });
    return opened.session.layout();
  };
}`)
result := page.MustEval(`window.measure(...)`, docxBytes)
// 断言：页数与桌面版一致
```

**验收**：对同一份 docx，Chromium 版测出的 `pages.length` 与桌面版一致。

**如果不通过**，备选方案：
- 用 LibreOffice 转 PDF，从 PDF 读页数（精度略低但可行）
- 放弃自动页码，导出时提示用户在 Word 里「更新域」

---

### 阶段 0 出口标准

- [ ] 三个验证全部有明确结论（通过/不通过/备选方案）
- [ ] 能用账号登录，前端能调到 `/api/config/load`
- [ ] 数据库迁移脚本能建出全部表
- [ ] 上传一个 docx 到项目，能列出文件

**如果 0.3 不通过，暂停，重新评估导出方案再继续。**

---

## 阶段 1：技术方案主链路（8 周）

### 目标

**从上传招标文件 → 生成完整技术方案 → 导出 Word，端到端跑通。**

这一条链路走通，产品就有核心价值了。其余功能都是围绕它的补强。

### 任务分组

#### 1A 文档导入与解析（1 周）

| 任务 | 来源 |
|---|---|
| 招标文件上传（docx/pdf/doc） | `fileService.cjs` 866 行 |
| LibreOffice headless 转换 | `doc2markdown/convert.mjs` 1,633 行 |
| docx → markdown（mammoth 等价） | 需要 Go 库或调 Node helper |
| PDF → markdown | `pdfcpu` 或调 helper |
| 多标段检测 `checkBidSections` | `bidSectionExtractionTask` 265 行 |

**注意**：`doc2markdown/convert.mjs` 1,633 行里有很多是格式探测和 fallback 逻辑。**建议先原样保留为一个小的 Node helper**（只做 doc→docx 和格式探测），Go 只调它。理由是：这部分逻辑纯碎、边界多、用 Go 重写性价比低。

#### 1B Store 层（1.5 周）

| 表组 | 行数 |
|---|---|
| `technical_plan_*`（11 张） | `technicalPlanStore.cjs` 2,929 行 |
| `task_logs`、任务表 | 分散 |

**重点**：`saveOutline()` 的 `reason` 协议（`replace`/`edit`/`delete`/`add-root`/`add-child`/`sort`）。这是最容易搬错的地方，必须写单元测试覆盖 6 种 reason 的清理语义。

#### 1C AI 服务（1.5 周）

| 任务 | 来源 |
|---|---|
| HTTP 客户端（OpenAI 兼容 + Gemini） | `aiService.cjs` 2,839 行 |
| 流式解析（SSE） | 同上 |
| 文本/图片双队列 | 同上 |
| 重试与错误分类 | `aiHttpError.cjs`、`aiRequestQueue.cjs` |
| Token 统计 + 积分扣减 | `textTokenStatsStore` + 新增计费 |
| 图片压缩 | `imageCompression.cjs`（nativeImage → Go `image` 包） |

**关键**：这是**所有业务的基础**，必须先做且做扎实。建议写 20+ 个单元测试覆盖：正常流、中途断开、401/402/429/500、超时、并发限流。

#### 1D Agent Sidecar（2 周，最高风险）

| 任务 | 来源 |
|---|---|
| 把 `services/pi/` 搬成独立 Node 服务 | 5,390 行，近乎原样 |
| HTTP 接口层（sessions/continue/answer/pause/events） | 新写，约 600 行 |
| 业务工具反向回调到 Go | 新写，约 400 行 |
| 代理到 Go 的 `/v1/chat/completions` | 简化 `agentOpenAiProxy.cjs` 1,514 行 |
| 空转诊断、提交校验保留 | `piStageLoopGuard`、`piSubmissionPolicy` |

**这是整个项目最难的部分。** 建议：

1. 先做一个最小闭环：创建一个 session，发一句 prompt，能收到回答，能调用一个自定义工具
2. 再加工具集、阶段门禁、持久化
3. 最后加诊断、恢复、暂停

**不要在第一步就搬全部功能。**

#### 1E 业务任务（2 周）

| 任务 | 行数 | 复杂度 |
|---|---|---|
| `bidAnalysisTask` | 499 | 中，可批量并行 |
| `outlineGenerationTaskV2` | 1,806 | **高**，多阶段 + 持久 Session |
| `globalFactsTaskV2` | 503 | 中 |
| `contentGenerationTask` | 3,113 | **最高**，13 个模块协同 |
| `contentSectionRegenerationTask` | 239 | 中 |

**建议顺序**：bidAnalysis → globalFacts → outline → content。由简到繁，每完成一个就跑通端到端。

**Prompt 处理**：这是最大的隐藏工作量。Prompt 散落在任务文件里（内联模板字符串），加上：

- `resources/agent-workspace.md`（6 KB）
- `resources/content-generation/受限HTML生成规范.md`（23.6 KB）
- `resources/content-generation/正文模板.html`（11.4 KB）
- `resources/content-generation/配图类型对照表.md`（0.77 KB）

**处理方式**：把 Prompt 提出来存成 `.md` 文件，用 `go:embed` 打包。Go 侧用 `text/template` 渲染变量。

```go
//go:embed prompts/content/*.md
var contentPrompts embed.FS

func loadPrompt(name string, data any) (string, error) {
    tmpl, err := template.New(name).Funcs(sprig.FuncMap()).ParseFS(contentPrompts, "prompts/content/"+name)
    // ...
}
```

**改动风险**：Prompt 里的模板变量从 JS 模板字符串（`${x}`）换成 Go template（`{{.X}}`），必须逐个比对渲染结果。**建议写测试：同一组输入，JS 版和 Go 版渲染出的字符串必须完全相等。**

#### 1F Word 导出（1 周）

| 任务 | 来源 |
|---|---|
| C# helper 进程池 | 新写 |
| 导出编排（读目录 → 拼 HTML → 调 C#） | `technicalPlanExport.cjs` 327 行 |
| 页码测量（Chromium） | `technicalPlanToc.cjs` |
| Mermaid 转图（Chromium） | `localImageRenderService.cjs` |
| 下载接口 | 新写 |

### 阶段 1 出口标准

- [ ] 上传一份真实招标文件，能解析出 18 个解析项
- [ ] 能生成目录，结构与桌面版一致
- [ ] 能生成正文，包含 Mermaid 图、AI 配图
- [ ] 能导出 Word，**与桌面版输出做结构级 diff（解压比 XML），差异清单可解释**
- [ ] Agent 任务中途暂停、恢复、失败重试都能正确工作

**最后一条最重要**。建议准备 3-5 份真实标书作为黄金样本，每次改动都跑一遍。

---

## 阶段 2：商务标 + 导出完整化（5 周）

### 目标

**一个完整的投标项目（技术标 + 商务标 + 目录页码）能导出。**

### 任务

| # | 任务 | 来源 | 预估 |
|---|---|---|---|
| 2.1 | 投标模版提取 | `templateExtractionTask` 993 行 | 2 周 |
| 2.2 | 商务模版填写 | `businessTemplateFillTask` 1,336 行 | 1.5 周 |
| 2.3 | 规则和页码编制 | `rulePageCompileTask` 617 行 | 1 周 |
| 2.4 | 导出格式模板 | `export_templates` + `templateStore` | 0.5 周 |

### 特别提醒：2.1 是最绕的业务逻辑

`templateExtractionTask` 的分组并发流程：

```
扫描模板 → 按目录分组成 N 组（每组约 35 个候选）
  ├─ 单组：一个持久 Session 分类并写入
  └─ 多组：各组临时子会话并发分类
      → 程序合并各组分类（同名字段合成、冲突改名〔第N组〕）
      → 一次统稿请求给出合并/改名清单
      → 程序按清单修改，校验失败回退重试
      → 程序调 C# 全文写入
```

这段逻辑的坑在「合并规则」和「统稿清单的校验」，开发说明里写了几千字。**建议先把它当黑盒：输入（候选列表 + 目录）+ 输出（字段清单），用桌面版跑出 10 组真实样本作为回归基线。**

---

## 阶段 3：知识库 + 资信库（4 周）

| # | 任务 | 来源 | 预估 |
|---|---|---|---|
| 3.1 | 知识库 Store + 文件管理 | `knowledgeBaseStore` 1,225 行 | 1 周 |
| 3.2 | 知识库解析 pipeline | `knowledgeBaseService` + prompts | 1.5 周 |
| 3.3 | 知识库匹配（供目录/正文引用） | 同上 | 1 周 |
| 3.4 | 资信库 | `credentialLibraryService` + 6 张表 | 0.5 周 |

**注意**：知识库和资信库是**租户级**，不是项目级（见 schema 文档）。这一步要开始处理跨项目共享的权限问题。

---

## 阶段 4：检查类功能（5 周，可延后）

### 如果时间紧，这一阶段可以整体后置

| # | 任务 | 来源 | 预估 | 建议 |
|---|---|---|---|---|
| 4.1 | 废标项检查 | `rejectionCheckTask` 1,745 行 | 2 周 | 做 |
| 4.2 | 标书查重 | `duplicateCheckStore` 987 行 + 算法 | 2 周 | 做 |
| 4.3 | 暗标检查 | `blindCheck*` 8 表 + 3 任务 | 1.5 周 | 视需求 |
| 4.4 | 双层 PDF | `dualLayerPdf` + pdfWriter + OCR | 3 周+ | **建议砍掉** |

**4.4 为什么建议砍掉**：

- Linux 下 COM 路径失效，只能 LibreOffice
- 要搬 `pdfWriter.cjs`（手写 PDF 双层文字层 + 内嵌 Tesseract 字体）
- 要搬 OCR 组件（onnxruntime-web + PP-OCRv5 模型，约 40-80 MB）
- 收益：客户真的需要双层 PDF 吗？Word 直接给客户更常见

**如果确实要做，单独评估 2-3 周。**

---

## 阶段 5：多租户 + 计费（4 周，可与阶段 2-3 并行）

### 前提

阶段 1 完成，业务链路通了。此时加计费不用返工。

### 任务

| # | 任务 | 预估 |
|---|---|---|
| 5.1 | 租户管理（创建/停用） | 2 天 |
| 5.2 | 用户管理（邀请/审批/角色/模块权限） | 3 天 |
| 5.3 | 会员等级（plans/subscriptions） | 3 天 |
| 5.4 | 积分系统（账户/流水/扣减幂等） | 4 天 |
| 5.5 | 用量统计（按模型/项目/用户） | 3 天 |
| 5.6 | 配额拦截（积分不足拒绝新任务） | 2 天 |
| 5.7 | 管理后台页面 | 3 天 |
| 5.8 | 兑换码 | 1 天 |

### 关键设计点

**5.4 的幂等**：`credit_ledgers` 的 `UNIQUE (tenant_id, ref_id)` 是唯一保证。所有扣减必须走同一个函数：

```go
func (b *Billing) Consume(ctx context.Context, req ConsumeRequest) error {
    // ref_id 必须由调用方提供且全局唯一
    // 内部: UPDATE balance + INSERT ledger 在同一个事务
    // 唯一约束冲突 → 视为已扣过，返回 nil（幂等）
}
```

**5.6 的拦截点**：

| 操作 | 预检条件 |
|---|---|
| 启动正文生成 | 余额 > 预估消耗（按目录字数 × 系数） |
| 生成图片 | 余额 > 单张成本 |
| 导出 | 余额 > 0 |

---

## 阶段 6：运维与上线（3 周）

| # | 任务 | 预估 |
|---|---|---|
| 6.1 | 多阶段 Dockerfile（Go + .NET + Node + Chromium + LibreOffice） | 3 天 |
| 6.2 | docker-compose（PG + 服务 + Nginx） | 1 天 |
| 6.3 | Nginx 配置（SSE 超时、上传大小、gzip） | 1 天 |
| 6.4 | 备份策略（PG dump + 文件 rsync） | 1 天 |
| 6.5 | 监控（进程、队列深度、错误率） | 2 天 |
| 6.6 | 日志聚合 | 1 天 |
| 6.7 | 压测（8 并发导出、20 并发 Agent） | 3 天 |
| 6.8 | 交付文档 + AGPL 源码公开 | 2 天 |

### Dockerfile 关键点

镜像会很大（Go + .NET runtime + Node + Chromium + LibreOffice + 中文字体），预计 **2-3 GB**。

```dockerfile
# 关键：中文字体必须显式安装，否则导出的 Word 打开后字体错乱
RUN apt-get update && apt-get install -y \
    libreoffice-writer \
    fonts-wqy-zenhei fonts-wqy-microhei \   # 文泉驿
    fonts-noto-cjk \                        # 思源
    chromium \
    && rm -rf /var/lib/apt/lists/*
```

**字体必须与桌面版对齐**。桌面版用系统字体（宋体/黑体/微软雅黑）。Linux 没有这些，要用 fontconfig 做别名映射：

```xml
<!-- /etc/fonts/conf.d/99-yibiao.conf -->
<match target="pattern">
  <test name="family"><string>宋体</string></test>
  <edit name="family" mode="assign" binding="same"><string>Noto Serif CJK SC</string></edit>
</match>
<match target="pattern">
  <test name="family"><string>黑体</string></test>
  <edit name="family" mode="assign" binding="same"><string>Noto Sans CJK SC</string></edit>
</match>
```

**这个映射不做，导出的 Word 在 Windows 上打开字体是对的（因为 docx 里写的是"宋体"），但服务端做格式自检和 PDF 转换时会用错字体，导致页数算错。** 这是个非常隐蔽的坑。

### Nginx 关键配置

```nginx
location /api/events {
    proxy_pass http://go:8080;
    proxy_http_version 1.1;
    proxy_set_header Connection '';
    proxy_buffering off;              # SSE 必须
    proxy_read_timeout 3600s;         # 长任务
    chunked_transfer_encoding off;
}

location /api/upload {
    client_max_body_size 200m;        # 标书可能很大
}

location /api/ {
    proxy_pass http://go:8080;
    proxy_read_timeout 300s;
    proxy_send_timeout 300s;
}
```

---

## 三、并行策略

如果只有你一个人，按上面的顺序串行做。

如果能有 2-3 人，建议这样分工：

```
人员 A（主力，全程）：阶段 0 → 1D Agent → 1E 业务任务 → 2 商务标
人员 B（后端）：     阶段 0 的 0.5-0.11 → 1A/1B/1C → 3 知识库 → 4 检查
人员 C（全栈）：     阶段 1F 导出 → 阶段 5 计费 → 阶段 6 运维
```

**关键约束**：阶段 1 的 Agent 和正文生成这条链路，**必须一个人从头跟到尾**。因为 Prompt、状态机、恢复逻辑高度耦合，交接损失很大。

**前端**：整个项目前端改动只有约 10 人日（见 bridge 文档 §6），可以插空做，不占主线。

---

## 四、里程碑与验收物

| 里程碑 | 时间点 | 验收物 |
|---|---|---|
| M0 地基就绪 | 第 3 周末 | 能登录、能上传、三个验证有结论 |
| M1 Agent 跑通 | 第 6 周末 | 一个 Agent 任务能完成并落库 |
| M2 单章生成 | 第 9 周末 | 能生成一个章节并预览 |
| M3 整本导出 | 第 11 周末 | **端到端导出，与桌面版 diff 通过** |
| M4 商务标完整 | 第 16 周末 | 技术标 + 商务标完整项目导出 |
| M5 知识库可用 | 第 20 周末 | 知识库能引用到正文 |
| M6 检查类完成 | 第 25 周末 | 查重、废标可用 |
| M7 多租户就绪 | 第 29 周末 | 能开第二个租户、积分能扣 |
| M8 上线 | 第 32 周末 | 生产环境跑通，压测通过 |

**M3 是最重要的里程碑**。它证明「Go 重写 + C# 复用 + Chromium 辅助」这条路走得通。**在 M3 之前不要并行太多工作**，集中火力。

---

## 五、每阶段的验证清单

沿用桌面版的验证习惯，但换成 Go 的方式：

| 改动 | 验证 |
|---|---|
| Go 业务逻辑 | `go test ./...`，关键函数覆盖 > 70% |
| SQL / sqlc | 在真实 PG 上跑迁移 + 集成测试 |
| Store 层 | 每个 Store 一组 CRUD 测试 + 边界（空、并发、外键） |
| API | 契约测试（golden JSON 对比） |
| Agent sidecar | 端到端：真实模型跑通一个完整任务 |
| Word 导出 | 结构级 diff + Word 打开人工核对 |
| 前端 | `npm run build` + 手工点测 |
| 部署 | docker-compose up 后跑冒烟脚本 |

**必须新建的自动化**：

1. **契约测试**：防止 bridge 语义漂移
2. **导出黄金样本回归**：3-5 份真实标书，每次改动跑 diff
3. **Prompt 渲染一致性测试**：JS 版和 Go 版渲染结果逐字对比
4. **任务状态机测试**：启动/暂停/恢复/失败重试/重启恢复

这四套测试是**长期维护的基础**，值得花 2 周建。

---

## 六、阶段 0 立刻可以开始的三件事

如果现在就要动手，按这个顺序：

### 第一件：跑前端（1 天）

```bash
cd client && npm ci
npx vite --host 127.0.0.1 --port 5173
```

看浏览器里能不能渲染。这一步能暴露所有前端隐藏问题。

### 第二件：建 Go 骨架（0.5 天）

```bash
mkdir yibiao-web && cd yibiao-web
go mod init github.com/patrick/yibiao-web
# 按 00-overview.md 的目录结构建目录
```

先只做一件事：`GET /health` 返回 200。

### 第三件：写 bridge 适配层原型（1 天）

在 `client/src` 里新建 `shared/bridge/web.ts`，用 Proxy 实现，配一个 mock 后端，验证前端能通过 HTTP 拿到数据。

**这三件加起来 2.5 天，能验证整个方案的可行性。**

---

## 七、风险与应对（按阶段）

| 阶段 | 风险 | 应对 |
|---|---|---|
| 0 | 布局测量不通过 | 提前验证，备选 LibreOffice 转 PDF 测页数 |
| 0 | 前端白屏原因复杂 | 逐个页面排查，允许保留少量 Electron-only 代码用 `if` 分支 |
| 1 | Agent sidecar 难 | 先做最小闭环，不加全部功能 |
| 1 | Prompt 迁移出错 | 渲染一致性测试 |
| 1 | 正文生成状态机复杂 | 保留桌面版的 checkpoint 设计，不要简化 |
| 1 | 导出结果不一致 | 黄金样本 diff，M3 前集中攻坚 |
| 2 | 模版提取分组逻辑 | 当黑盒，用真实样本做回归 |
| 4 | 双层 PDF 成本高 | 砍掉或延后 |
| 5 | 计费幂等出错 | 唯一约束 + 事务，写并发测试 |
| 6 | 字体映射导致页数错 | 显式配 fontconfig 别名，验证测量一致 |

---

## 八、明确不做的事

| 不做 | 理由 |
|---|---|
| 插件系统 | Web 下是安全灾难，且依赖外部市场 |
| 自动更新 | Web 无客户端 |
| 捐赠 | 无意义 |
| 官方账号充值/发票 | 与核心价值无关，公网时重做 |
| 开发者工具页（6 个） | 内部调试用 |
| GPU 硬件加速 | 桌面概念 |
| 本地文件对话框 | 浏览器无此能力 |
| 双窗口（token 统计/Agent 监控） | 改路由页或删 |

砍掉这些**省约 6-8 周**，本文的 32 周估计已经扣除了这部分。

---

## 九、AGPL 合规动作（贯穿全程）

不是最后才做，**要从第一天就做对**：

| 时间点 | 动作 |
|---|---|
| 项目创建时 | fork 官方仓库，保留 `LICENSE` 和 `NOTICE` 原样 |
| 每次提交 | 提交记录保留原作者信息 |
| 前端开发时 | 页脚加「源码」链接指向你的 fork 仓库 |
| 部署上线前 | 确认链接可访问，且源码是当前运行版本 |
| 后续改动 | 修改过的地方同样开源（AGPL 要求衍生作品同许可） |

**唯一例外**：如果将来要做闭源商业版，必须找原作者（`fb208@outlook.com`）谈商业授权。这是唯一的合法路径，不要抱侥幸。
