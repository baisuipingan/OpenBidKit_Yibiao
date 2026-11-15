# 存储架构：调研与重构方案

> 触发：用户质疑「没有文件存储设计？个人资产库？对象存储还是沙箱？」
> 结论：**质疑成立，原设计有重大缺陷。本文档给出调研结论和改造方案。**
> 日期：2026-11-14

---

## 一、原设计的缺陷（诚实盘点）

### 1.1 五个硬伤

| # | 缺陷 | 代码证据 | 后果 |
|---|---|---|---|
| 1 | **没有存储抽象层** | `internal/service/storage/` 是空目录 | 文件操作散落各处，无法统一加权限、配额、清理 |
| 2 | **数据库存裸路径** | `tender_markdown_path TEXT` | 存储迁移（本地→S3）要改所有数据；路径无 scope，越权风险 |
| 3 | **Agent 工作区与业务数据混在一起** | `/data/agent/tenants/...` 同时放临时文件和产物 | 临时文件永远堆积；产物没进资产库 |
| 4 | **没有「资产」概念** | 产物只是磁盘上的文件，没有 ID、版本、归属 | 用户看不到历史产物；不能引用、分享、版本回滚 |
| 5 | **临时工作区不清理** | 每次任务一个目录，从不删除 | 磁盘无限增长（已验证：跑了 4 次任务留了 4 个目录） |

### 1.2 一个更隐蔽的问题

`validate.go` 的 `readItemResult` 用**文件名硬编码探测**：

```go
candidates := []string{
    filepath.Join(workspace, "items", item.ID+"."+extFor(item.Output)),
    filepath.Join(workspace, "items", item.ID+".md"),      // 猜
    filepath.Join(workspace, "items", item.ID+".json"),    // 猜
    filepath.Join(workspace, item.ID+"."+extFor(item.Output)), // 再猜
}
```

这意味着**产物位置是靠约定猜的，不是契约**。Agent 换个目录写就读不到。

---

## 二、云端 Agent 的存储怎么做（调研结论）

调研了 Manus、Vercel Sandbox、E2B、Google ADK、Claude Agent SDK、LangChain Deep Agents，以及多篇 2026 年的架构分析。

### 2.1 最关键的一句话

> **"A common agent-platform mistake is using the sandbox directory as a database."**
> — XBSTACK《AI Agent Sandbox Design》
>
> **"生产级 Agent 需要明确的四层状态平面，不能揉成一团"**
> — Applied Technology Index《2026 Comparative Analysis》

### 2.2 四层状态平面（核心模型）

这是所有成熟方案共同收敛到的模型：

| 层 | 内容 | 存储 | 生命周期 |
|---|---|---|---|
| **会话状态** | 消息、工具调用、审批记录 | PG（已有） | 永久 |
| **工作区状态** | 临时目录、克隆的仓库、缓存、中间文件 | 本地磁盘 / 沙箱 | **任务结束即销毁** |
| **产物状态** | 命名输出：最终文档、报告、图片 | **对象存储** | 长期，带版本 |
| **业务状态** | 已提交的流程步骤、外部副作用 | PG（已有） | 永久 |

**我原来的设计把「工作区」和「产物」混成了一个目录，这是根本错误。**

### 2.3 各家的具体做法

#### Manus

- 每个任务一个**独立 Firecracker 微虚拟机**（不是容器）
- 沙箱内含：用户上传的附件、执行产物、配置
- **生命周期**：创建 → 休眠 → 唤醒 → 回收（免费 7 天 / Pro 21 天）
- **回收后重建时只恢复"重要文件"**：Manus 产物、上传附件、Slides/WebDev
  **中间代码和临时文件不恢复**
- 沙箱数据快照到对象存储（S3），唤醒时拉取

**关键洞察**：Manus 明确区分「重要产物」和「中间文件」，恢复策略不同。这正是我缺的。

#### Google ADK Artifact Service

最清晰的抽象，值得直接抄：

```
Artifact = (name, version) 唯一标识的不可变 blob

namespace:
  - session 级：仅当前会话可见，随会话清理
  - user: 前缀：跨会话，用户级资产库

存储后端可插拔：
  - InMemory（测试）
  - GCS / S3（生产）

关键设计：
  - 只在事件日志里记 (name, version) 引用，不存字节
  - 会话状态保持很小
  - 版本号自增，永不原地覆盖
```

**这套抽象正好对应「个人资产库」的需求。**

#### Vercel Sandbox

- 默认持久化：会话停止时自动快照文件系统，恢复时还原
- 但**只快照文件系统，不保存内存**
- 有「Drives」概念：工作文件独立于沙箱存在，可跨 Agent 共享/fork

#### Claude Agent SDK

- 每个 Agent 一个子进程 + 工作目录 + 本地 transcript
- `SessionStore` **只镜像 transcript**，不保存工作目录产物
- 官方明确说：产物需要**单独的 volume 或对象存储策略**

**这是个警告案例**：Claude Agent SDK 在这块是缺失的，别学它。

### 2.4 三条可以直接采纳的原则

**原则 1：存储与计算分离**

```
计算层：Agent 沙箱（临时、可销毁、可重建）
存储层：对象存储（持久、版本化、可访问）
```

不要把「沙箱还活着」等同于「数据安全存着」。

**原则 2：产物导出是显式操作，不是自动的**

> Sandbox → Artifact Scanner/Policy → Durable Storage / User Download

Agent 写文件 ≠ 用户能拿到文件。中间要有一步「登记为资产」。

这一步顺便做：类型校验、大小限制、敏感信息扫描、归属记录。

**原则 3：Scope 是安全原语**

> "A path without an authenticated scope is not tenant isolation."

存储 key 必须内嵌 `tenant_id` + `user_id`，且由服务端从登录态填充，不能信任请求参数。

### 2.5 关于「沙箱」：我们现在需要吗

| 方案 | 隔离级别 | 成本 | 我们是否需要 |
|---|---|---|---|
| Firecracker 微 VM（Manus/E2B） | 硬件级 | 高，需 KVM | **暂时不需要** |
| 容器（Docker） | 进程级 | 中 | 已有（开发容器） |
| 目录隔离 + 路径校验 | 无 | 低 | **当前够用** |
| 独立进程 + cwd 限制 | 弱 | 低 | Agent sidecar 已是 |

**判断依据**：我们的 Agent 跑的是**我们自己的 Prompt**，不是用户任意代码。Agent 的 `bash` 工具是我们主动给的，`cwd` 已经限制在工作区。

**风险等级**：低。不同于 Manus 让用户「写代码建网站」，我们是固定领域的标书生成。

**但有两个必须做的**：
1. `bash` 工具的 `cwd` 强制锁定在工作区内（已验证 sidecar 的 `spawnHook` 有做）
2. 补充：禁止 `..` 逃逸，禁止访问工作区外路径

**什么时候要上沙箱**：如果将来开放「用户自定义 Prompt/插件」，就必须上。现在不需要。

---

## 三、重构方案

### 3.1 目标架构

```
┌─────────────────────────────────────────────────────────┐
│  业务数据（PG）                                          │
│  projects / technical_plan_* / knowledge_* / ...         │
│  只存 asset_id 引用，不存路径                            │
└─────────────────────────────────────────────────────────┘
                          │ asset_id
                          ▼
┌─────────────────────────────────────────────────────────┐
│  资产层（Artifact Service）★新建                        │
│  assets 表：id / tenant / project / owner / name /       │
│             version / kind / mime / size / hash /        │
│             storage_key / created_by_task / metadata     │
└─────────────────────────────────────────────────────────┘
                          │ storage_key
                          ▼
┌─────────────────────────────────────────────────────────┐
│  存储后端（Storage Backend）★新建接口                    │
│  ├─ LocalFS（开发 / 单机部署）                           │
│  └─ S3/MinIO（生产，多实例）                             │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│  工作区（Ephemeral Workspace）★临时，任务结束销毁        │
│  /data/workspaces/<task_id>/                             │
│  Agent 在这里读写，产出后显式「提交为资产」              │
└─────────────────────────────────────────────────────────┘
```

### 3.2 核心实体：Asset

```sql
CREATE TABLE assets (
    id            UUID PRIMARY KEY,
    tenant_id     UUID NOT NULL,
    project_id    UUID,              -- 可空：用户级资产（如知识库文档）
    owner_id      UUID NOT NULL,     -- 谁创建的

    -- 身份
    name          TEXT NOT NULL,     -- 逻辑名，如 "tender.md"、"items/projectInfo.json"
    version       INTEGER NOT NULL,  -- 自增，永不覆盖
    kind          TEXT NOT NULL,     -- tender | plan | outline | content | image | export | ...

    -- 存储
    storage_key   TEXT NOT NULL,     -- 后端内的键（相对路径 / S3 key）
    mime_type     TEXT NOT NULL,
    size_bytes    BIGINT NOT NULL,
    content_hash  TEXT NOT NULL,     -- SHA-256，用于去重和完整性

    -- 溯源
    source        TEXT NOT NULL,     -- upload | agent | export | import
    task_id       UUID,              -- 哪个 Agent 任务产出的
    metadata      JSONB NOT NULL DEFAULT '{}',

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ,

    UNIQUE (tenant_id, project_id, name, version)
);

CREATE INDEX idx_assets_scope ON assets (tenant_id, project_id, kind)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_assets_task ON assets (task_id) WHERE task_id IS NOT NULL;
```

**关键设计点**：

1. **`(name, version)` 唯一**：与 ADK 一致。同名再写就是 version+1，历史永不丢。
2. **`storage_key` 与 `id` 分离**：换后端只改 key 生成规则，业务代码不动。
3. **`content_hash`**：秒传、去重、完整性校验。
4. **`task_id`**：能追溯「这份产物是哪个 Agent 任务生成的」。
5. **`deleted_at`**：软删，配合清理任务物理删除。

### 3.3 Storage 接口

```go
// Package storage 提供后端无关的对象存储。
type Backend interface {
    // Put 写入对象，返回存储键。
    Put(ctx context.Context, key string, r io.Reader, size int64, mime string) error
    // Get 读取对象。
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    // Delete 删除对象（物理）。
    Delete(ctx context.Context, key string) error
    // Stat 查对象大小与修改时间。
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    // SignedURL 生成临时下载链接（S3 预签名 / 本地则返回服务路由）。
    SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}
```

两个实现：
- `LocalFS`：开发与单机部署，路径 `<root>/<tenant>/<project>/<key>`
- `S3`：生产，`s3://<bucket>/<tenant>/<project>/<key>`

**Key 生成规则**（关键，内嵌 scope）：

```
<tenant_id>/<project_id 或 _user>/<kind>/<asset_id>/v<version>/<filename>
```

例：
```
8bfe9282.../f77d5870.../tender/3f2a.../v1/tender.md
8bfe9282.../f77d5870.../outline/9c1b.../v1/items__projectInfo.json
```

**为什么不用 name 做路径**：name 可能有 `/`、中文、特殊字符，且有安全风险（`../../`）。用 `asset_id` 做目录，`name` 只存数据库。

### 3.4 Artifact Service

```go
// Package artifact 管理版本化产物。
type Service struct {
    q       *store.Queries
    backend storage.Backend
}

// Put 保存一个新版本，返回资产记录。
func (s *Service) Put(ctx context.Context, req PutRequest) (*Asset, error)

// Get 读最新版本（或指定版本）。
func (s *Service) Get(ctx context.Context, scope Scope, name string, version int) (io.ReadCloser, *Asset, error)

// List 列出某作用域下的资产。
func (s *Service) List(ctx context.Context, scope Scope, kind string) ([]Asset, error)

// CommitWorkspace 把工作区目录里的文件批量提交为资产。
// 这是 Agent 任务结束时的关键一步。
func (s *Service) CommitWorkspace(ctx context.Context, scope Scope, workspaceDir string, rules CommitRules) ([]Asset, error)
```

`CommitWorkspace` 是核心，它实现调研里说的「显式导出」：

```go
type CommitRules struct {
    TaskID   string
    Kind     string            // 资产类别
    Include  []string          // glob 白名单，如 ["*.md", "items/*.json"]
    Exclude  []string          // 如 ["**/node_modules/**", "*.log"]
    MaxBytes int64             // 单文件上限
    MaxTotal int64             // 总量上限
}
```

**流程**：
```
Agent 任务结束
  → 扫描工作区，按 Include/Exclude 筛选
  → 逐个文件：算 hash、查重、上传、登记 assets
  → 返回资产列表
  → 业务层把 asset_id 写进业务表
```

### 3.5 工作区生命周期

```
任务开始 → 创建 /data/workspaces/<task_id>/
         → 把输入资产「物化」到工作区（从 Storage 下载）
         → Agent 在工作区读写
任务结束 → CommitWorkspace 提交产物
         → 延迟 1 小时删除工作区（留时间给人排查）
任务失败 → 保留工作区 24 小时（供诊断）
```

**清理任务**（定时）：扫 `/data/workspaces/` 下超过 TTL 的目录，删除。

### 3.6 数据库改造

原来：
```sql
tender_markdown_path TEXT           -- 裸路径
```

改成：
```sql
tender_asset_id UUID REFERENCES assets(id)   -- 引用
```

**要不要保留 path 字段做兼容**？不要。现在只有测试数据，改了就是。

### 3.7 前端影响

`readTenderMarkdown` 这类接口原本返回内容，改成从资产服务读，前端感知不到。

新增需要的接口：
- `GET /api/assets/:id` — 下载资产
- `POST /api/assets/list` — 列出项目资产（产物列表页）
- `POST /api/assets/delete` — 删除

---

## 四、关于「沙箱」的结论

**现在不做，但要预留。**

| 现在 | 将来 |
|---|---|
| Agent 的 bash 限制在 `cwd` | 换成独立容器 |
| 路径校验防 `..` | 容器级隔离 |
| 单租户信任 | 多租户 + 用户自定义 Prompt 时要严格隔离 |

**触发上沙箱的条件**（明确写下来，避免以后拍脑袋）：

1. 开放用户自定义 Prompt 或插件
2. 多租户且租户间不完全互信
3. Agent 需要访问外部网络（现在只需要模型 API）

**当前风险可接受的理由**：Agent 跑的是我们写的 Prompt，bash 是内部工具，
cwd 已锁定，路径有校验。跟 Manus「让用户写代码建网站」的威胁模型完全不同。

---

## 五、实施顺序

| 阶段 | 内容 | 工作量 |
|---|---|---|
| **1** | Storage 接口 + LocalFS 实现 + 单元测试 | 1 天 |
| **2** | assets 表迁移 + Artifact Service（Put/Get/List） | 1 天 |
| **3** | 工作区生命周期（临时目录 + 清理） | 0.5 天 |
| **4** | `CommitWorkspace`（Agent 产物入库） | 1 天 |
| **5** | 改造招标分析：走资产库，不写裸路径 | 0.5 天 |
| **6** | 前端产物列表 + 下载接口 | 0.5 天 |
| **7** | S3 后端实现（可选，生产前做） | 1 天 |

**总计约 5.5 天**。先做 1-5，让存储层立起来；S3 留到部署生产前。

---

## 六、这次调研给我的三条教训

**教训 1：不要跳过「存储层」直接写业务**

我为了快速验证 Agent，直接 `os.WriteFile` 写目录。看似快，实际上把技术债挖得很深——后面每加一个功能都要重新想「文件放哪」。

**教训 2：抽象要建在第一次需要它之前**

`internal/service/storage/` 目录是我建的，但一直空着。建了不用等于没建，反而给人「已经想到了」的错觉。

**教训 3：先看别人怎么踩坑**

Manus 的「回收后只恢复重要文件」、ADK 的「(name, version) 不可变」、XBSTACK 的「别把沙箱当数据库」——这些都是踩过坑的总结。花半天调研省掉的返工，远超这半天成本。


---

## 七、实施结果（2026-11-14 完成）

### 7.1 已交付

| 模块 | 文件 | 状态 |
|---|---|---|
| 存储接口 + LocalFS | `internal/service/storage/{storage,local}.go` | ✅ 含单元测试 |
| 资产服务 | `internal/service/artifact/{service,workspace}.go` | ✅ 含单元测试 |
| 工作区管理 | `internal/service/workspace/manager.go` | ✅ |
| 资产表迁移 | `internal/db/migrations/00003_assets.sql` | ✅ 已执行 |
| 资产 HTTP 接口 | `internal/http/handler/asset.go` | ✅ |
| 招标分析接入 | `internal/service/agent/bidanalysis/runner.go` | ✅ |

### 7.2 实测验证

**产物入库**（4 个文件）：
```
name                      | version | kind      | size | source | hash
--------------------------+---------+-----------+------+--------+--------------
summary.json              |       1 | bid-items |  567 | agent  | a351d3ff8b05
items/scoringCriteria.md  |       1 | bid-items | 1286 | agent  | 55db1742f9b2
items/projectInfo.json    |       1 | bid-items |  328 | agent  | d1a65c1daf21
items/invalidBidItems.md  |       1 | bid-items | 1302 | agent  | f1c7262ba1d5
```

**存储路径结构**（完全符合设计）：
```
<tenant>/<project>/<kind>/<asset_id>/v<version>/<filename>

8bfe9282.../f77d5870.../bid-items/1a286bee.../v1/items/invalidBidItems.md
```

**版本化**（同名再提交产生 v2，互不覆盖）：
```
summary.json | v1 | 567 bytes | deeaf043.../v1/summary.json
summary.json | v2 | 485 bytes | 2fe4c2b7.../v2/summary.json
```

**工作区回收标记**：
```
/workspace/data/agent/<task_id>/.release
内容：2026-11-14T01:56:11+08:00\ntrue
```

**下载接口**：
```
HTTP/1.1 200 OK
Content-Disposition: attachment; filename="summary.json"; filename*=UTF-8''summary.json
Content-Length: 485
Content-Type: application/json
ETag: "bfa4ec05a780bab9"
```

### 7.3 测试覆盖

| 测试 | 覆盖内容 |
|---|---|
| `TestKeyGeneration` | key 格式、用户级 vs 项目级、中文清理 |
| `TestValidateKeyRejectsUnsafe` | 路径穿越、绝对路径、盘符 |
| `TestLocalFSPutGetDelete` | 完整 CRUD、覆盖写、幂等删除 |
| `TestLocalFSRejectsTraversal` | `../` 逃逸拦截 |
| `TestLocalFSSymlinkEscape` | **软链接逃逸拦截**（真实创建软链接测试） |
| `TestHashBytes` | SHA-256 正确性 |
| `TestMatchGlob` | `*` / `**` / 中间通配 |
| `TestDefaultExcludesSkipCommon` | 常见临时文件被排除、业务产物不排除 |

### 7.4 过程中发现并修复的真实 bug

**UTF-8 字节遍历 bug**：`splitPath` 原本按字节遍历（`cur += string(p[i])`），
导致中文字符的每个 UTF-8 字节被当成一个字符，「第一章 项目概述」（8 字）
变成 22 个下划线。改用 `strings.FieldsFunc`（按 rune）后正确得到 8 个。

**这个 bug 如果不修**：中文文件名的存储路径会变得很长且不可预测，
且不同长度的中文名可能生成相同路径（哈希碰撞风险）。

### 7.5 仍然待做

| 项 | 说明 | 优先级 |
|---|---|---|
| S3 后端 | 多实例部署必需 | 上线前 |
| 定时清理任务 | `workspace.Cleanup()` 已实现，还没挂 cron | 中 |
| 旧路径字段迁移 | `tender_markdown_path` 等仍存裸路径 | 中 |
| 前端资产列表页 | 后端接口已就绪 | 中 |
| 资产配额 | 按租户限制总容量 | 多租户时 |

### 7.6 对原设计的修正总结

| 原设计 | 现在 |
|---|---|
| 文件路径裸存在业务表 | 业务表存 `asset_id` 引用 |
| Agent 工作区与产物混一起 | 工作区临时（`/data/agent/<task_id>/`），产物进资产库 |
| 产物永不清理 | 工作区自动回收（成功 1h / 失败 24h） |
| 无版本概念 | `(name, version)` 唯一，永不覆盖 |
| 无去重 | `content_hash` 秒传 |
| 无存储抽象 | `storage.Backend` 接口，LocalFS + S3 可切 |
| 无路径安全 | `ValidateKey` + 软链接穿透校验 |
