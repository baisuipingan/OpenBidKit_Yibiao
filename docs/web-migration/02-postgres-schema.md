# PostgreSQL Schema 迁移设计

> 源：`sql/workspace_schema.sql`（62 张表，`user_version = 44`）
> 目标：PostgreSQL 17，支持多租户 + 多项目

---

## 一、核心结论：不能 1:1 翻译

### 1.1 三处结构性缺陷

**缺陷 1：12 张表是单例（`CHECK (id = 1)`）**

```
technical_plan_meta              technical_plan_generation_config
duplicate_check_meta             rejection_check_meta
rejection_check_extraction       credential_library_profile
feasibility_report_meta          official_invoice_info
blind_check_meta                 blind_check_results
blind_check_reviews              blind_check_fix_results
```

这 12 张表设计上只允许存在一行。桌面版的隐含假设是「一份安装 = 一个工作区 = 一个投标项目」。

**Web 版必须改成 `PRIMARY KEY (project_id)`**，否则一个部署只能有一个项目。

**缺陷 2：全库只有 1 处 `project_id`**

而且那个是 `credential_library_projects`（语义是「历史投标业绩」，不是项目分组）。

所以 **62 张表里 0 张有真正的项目作用域**。这是最大的重写点。

**缺陷 3：主键混用自增与业务 ID**

- 10 张表用 `INTEGER PRIMARY KEY AUTOINCREMENT`（日志、流水类）
- 其余用 `TEXT PRIMARY KEY`（业务 ID，实际是 UUID 或语义键）
- `technical_plan_tasks` 用 `type TEXT PRIMARY KEY`（任务类型当主键，一个类型只能有一个任务）

第三种最麻烦：`type` 当主键意味着「同一类型任务全局只有一个」。Web 多用户并发时，A 用户在跑正文生成，B 用户就不能跑。**必须改成 `PRIMARY KEY (project_id, type)`**。

### 1.2 字段类型分布

| 类型 | 数量 | 迁移注意 |
|---|---|---|
| TEXT | 488 | 大部分可直译；53 个 JSON 列改 JSONB；时间列改 TIMESTAMPTZ |
| INTEGER | 125 | 布尔语义的改 BOOLEAN；自增改 IDENTITY |
| REAL | 8 | 改 DOUBLE PRECISION |
| BLOB | 0 | 无二进制列，文件都在磁盘 |

### 1.3 时间列处理

`technicalPlanStore.cjs` 统一用 `new Date().toISOString()`（JS ISO 格式，带 `Z`）。

**推荐**：列类型 `TIMESTAMPTZ`，Go 侧用 `time.Time`。写入 `ISO8601` 字符串时 PG 能自动解析，读出时按 RFC3339 序列化，与前端 `new Date(x)` 兼容。

**不要**改成 `TEXT` 保留原样——那样无法做时间范围查询和排序优化。

---

## 二、目标结构

### 2.1 四层架构

```
Layer 0  租户与身份
         tenants, users, memberships, invites

Layer 1  计费
         plans, subscriptions, credit_accounts, credit_ledgers,
         usage_records, orders

Layer 2  项目（隔离边界）
         projects, project_members

Layer 3  业务（62 张原表，全部加 tenant_id，项目级加 project_id）
         技术方案 11 / 查重 14 / 废标 8 / 知识库 9 / 资信库 6
         / 导出模板 1 / 可研 3 / 暗标 8 / 任务日志 1
```

### 2.2 隔离规则

| 表组 | tenant_id | project_id | 说明 |
|---|---|---|---|
| 技术方案（`technical_plan_*`） | ✓ | ✓ | 每个项目一份 |
| 标书查重（`duplicate_check_*`） | ✓ | ✓ | 每个项目一份 |
| 废标检查（`rejection_check_*`） | ✓ | ✓ | 每个项目一份 |
| 暗标检查（`blind_check_*`） | ✓ | ✓ | 每个项目一份 |
| 可研报告（`feasibility_report_*`） | ✓ | ✓ | 每个项目一份 |
| 知识库（`knowledge_*`） | ✓ | ✗ | **租户共享**，跨项目复用 |
| 资信库（`credential_library_*`） | ✓ | ✗ | **租户共享** |
| 导出模板（`export_templates`） | ✓ | ✓ | 项目级 + 租户级共享（用 `scope` 区分） |
| `task_logs` | ✓ | ✓ | |
| `official_invoice_info` | ✓ | ✗ | 用户级 |

**关键决定：知识库和资信库是租户级，不是项目级。** 桌面版整个工作区就一份，Web 版每个租户一份，所有项目共用——这符合「企业资料复用」的产品定位。如果做成项目级，用户每开一个项目都要重录一遍资质，体验会崩。

但要注意权限：租户内普通成员能否改共享知识库？建议加 `knowledge_*` 表的 `created_by` + 权限检查，或简单点：只有管理员能改，成员只读。

---

## 三、控制面 DDL

```sql
-- ============ Layer 0：租户与身份 ============

CREATE TABLE tenants (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug          TEXT NOT NULL UNIQUE,          -- 用于子域名/URL
  name          TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'active', -- active | suspended
  -- 租户级默认配置（导出样式、默认模型等）
  settings      JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email         TEXT NOT NULL,
  phone         TEXT,
  password_hash TEXT NOT NULL,                  -- bcrypt
  display_name  TEXT NOT NULL DEFAULT '',
  avatar_url    TEXT,
  role          TEXT NOT NULL DEFAULT 'member', -- owner | admin | member
  status        TEXT NOT NULL DEFAULT 'pending',-- pending | active | disabled
  must_change_password BOOLEAN NOT NULL DEFAULT false,
  settings      JSONB NOT NULL DEFAULT '{}',    -- 用户偏好（模型选择、主题等）
  last_login_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);

CREATE INDEX idx_users_tenant ON users(tenant_id, status);
CREATE INDEX idx_users_email ON users(email);
```

**关于「用户是否跨租户」**：设计成 `users.tenant_id` 单租户归属，不搞 `memberships` 多对多。理由：你的场景是「给一个公司定制」，然后「公网运营」——公网运营时用户通常也只属于一个组织。多对多会带来「当前在哪个租户」的切换状态，复杂度不值。

如果将来真需要一人多租户，加 `memberships` 表能平滑演进（`users` 保留为身份，权限走 `memberships`）。

### 3.1 认证与权限

```sql
CREATE TABLE invites (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email      TEXT NOT NULL,
  role       TEXT NOT NULL DEFAULT 'member',
  token      TEXT NOT NULL UNIQUE,
  invited_by UUID REFERENCES users(id),
  expires_at TIMESTAMPTZ NOT NULL,
  used_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,              -- refresh token 的 hash
  user_agent TEXT,
  ip         INET,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user ON sessions(user_id) WHERE revoked_at IS NULL;
```

**JWT 策略**：
- Access token：15 分钟，无状态，含 `user_id` / `tenant_id` / `role`
- Refresh token：7 天，存 `sessions` 表，可撤销
- 前端 `web.ts` 自动处理 401 → refresh → 重试一次

### 3.2 项目

```sql
CREATE TABLE projects (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  code         TEXT,                            -- 项目编号
  description  TEXT,
  status       TEXT NOT NULL DEFAULT 'active',  -- active | archived
  created_by   UUID REFERENCES users(id),
  -- 创建时固化的生成配置模板，避免每个项目重复配置
  config_template JSONB,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at   TIMESTAMPTZ                      -- 软删除
);

CREATE INDEX idx_projects_tenant ON projects(tenant_id, status) WHERE deleted_at IS NULL;

CREATE TABLE project_members (
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'editor',    -- owner | editor | viewer
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);
```

**软删除的必要性**：投标项目有合规价值（可能要保存几年），误删需要恢复。但同时要能真正清理（`storage.clean`）。用 `deleted_at` 软删 + 定时任务物理清理超过 N 天的。

---

## 四、计费面 DDL

```sql
-- ============ Layer 1：会员与积分 ============

CREATE TABLE plans (
  id             TEXT PRIMARY KEY,              -- free | pro | enterprise
  name           TEXT NOT NULL,
  -- 配额：NULL 表示不限
  monthly_tokens BIGINT,
  monthly_images INTEGER,
  monthly_exports INTEGER,
  max_projects   INTEGER,
  max_concurrent_tasks INTEGER NOT NULL DEFAULT 1,
  -- 功能开关
  features       JSONB NOT NULL DEFAULT '{}',   -- {"blindCheck":true,"dualLayerPdf":false}
  price_cents    INTEGER NOT NULL DEFAULT 0,
  billing_period TEXT NOT NULL DEFAULT 'month', -- month | year | none
  sort_order     INTEGER NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE subscriptions (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  plan_id    TEXT NOT NULL REFERENCES plans(id),
  status     TEXT NOT NULL DEFAULT 'active',    -- active | past_due | canceled | expired
  start_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  end_at     TIMESTAMPTZ,
  auto_renew BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_subs_tenant ON subscriptions(tenant_id, status);

-- 积分账户：租户级（企业统一充值，成员共用）
CREATE TABLE credit_accounts (
  tenant_id     UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  balance       BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0),
  -- 当期用量（月度重置）
  period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
  tokens_used   BIGINT NOT NULL DEFAULT 0,
  images_used   INTEGER NOT NULL DEFAULT 0,
  exports_used  INTEGER NOT NULL DEFAULT 0,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 积分流水：append-only，绝不 UPDATE/DELETE
CREATE TABLE credit_ledgers (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id       UUID REFERENCES users(id),
  delta         BIGINT NOT NULL,                -- 负数=消费，正数=充值
  balance_after BIGINT NOT NULL,
  kind          TEXT NOT NULL,                  -- recharge | consume | refund | grant | expire
  -- 幂等键：同一个业务事件只能扣一次
  ref_id        TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  metadata      JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, ref_id)                    -- ★ 幂等保证
);

CREATE INDEX idx_ledgers_tenant_time ON credit_ledgers(tenant_id, created_at DESC);

-- 用量明细：按次记录，用于统计和核对
CREATE TABLE usage_records (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id      UUID REFERENCES users(id),
  project_id   UUID REFERENCES projects(id) ON DELETE SET NULL,
  kind         TEXT NOT NULL,                   -- text | image | export | agent
  model        TEXT,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens     INTEGER NOT NULL DEFAULT 0,
  amount       BIGINT NOT NULL DEFAULT 0,       -- 折算的积分
  ref_id       TEXT,                            -- task_id / job_id
  metadata     JSONB NOT NULL DEFAULT '{}',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_usage_tenant_time ON usage_records(tenant_id, created_at DESC);
CREATE INDEX idx_usage_project ON usage_records(project_id, created_at DESC);
CREATE INDEX idx_usage_ref ON usage_records(ref_id);
```

### 4.1 积分扣减的并发正确性

这是最容易出错的地方。必须用一条 SQL 完成「扣减 + 记流水」：

```sql
-- 1. 原子扣减，余额不足时影响行数为 0
WITH deduct AS (
  UPDATE credit_accounts
     SET balance = balance - $2, updated_at = now()
   WHERE tenant_id = $1 AND balance >= $2
  RETURNING balance
)
-- 2. 记流水
INSERT INTO credit_ledgers (tenant_id, user_id, delta, balance_after, kind, ref_id, metadata)
SELECT $1, $3, -$2, deduct.balance, 'consume', $4, $5
  FROM deduct
RETURNING balance_after;
```

`UNIQUE (tenant_id, ref_id)` 保证重试不会重复扣——第二次插入违反唯一约束，整个事务回滚，余额自动恢复。

**`ref_id` 的构造规则**（必须全局唯一标识一次业务消耗）：

| 场景 | ref_id |
|---|---|
| 文本生成 | `<task_id>:<stage>:<attempt>` |
| 图片生成 | `<task_id>:image:<image_id>` |
| Word 导出 | `<export_job_id>` |
| Agent 调用 | `<session_id>:<turn>` |

### 4.2 计费时机

| 消耗类型 | 扣减时机 | 说明 |
|---|---|---|
| 文本 token（流式） | 流结束收到 `usage` 后 | 中途断开按已收 delta 估算 |
| 图片生成 | 返回图片后 | 失败不扣 |
| Word 导出 | 成功生成文件后 | 失败不扣 |
| Agent | 每次工具调用结束 | 按 token 累加 |

**预扣 vs 后扣**：建议**后扣**。预扣需要估算 token 数，估多了要退、估少了会透支。后扣的风险是余额不够时已经消耗了，用「余额小于阈值时拒绝新任务」防范（比如余额 < 1000 积分不允许启动正文生成）。

---

## 五、业务面改造规则

### 5.1 单例表改造

以 `technical_plan_meta` 为例：

```sql
-- 原
CREATE TABLE technical_plan_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  step TEXT NOT NULL DEFAULT 'document-analysis',
  -- ... 36 列
);

-- 新
CREATE TABLE technical_plan_meta (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  step       TEXT NOT NULL DEFAULT 'document-analysis',
  -- ... 36 列，类型按规则调整
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 冗余 tenant_id 虽然能从 projects 关联出来，但每个查询都 JOIN 一次不值得
CREATE INDEX idx_tp_meta_tenant ON technical_plan_meta(tenant_id);
```

**为什么保留冗余 `tenant_id`**：所有查询都要带租户过滤（防止越权），如果每次都 JOIN `projects` 才能过滤，SQL 会变复杂且慢。冗余一列，配合 `CHECK` 或触发器保证一致性，是标准做法。

**一致性保证**：用触发器

```sql
CREATE OR REPLACE FUNCTION check_tenant_match() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.tenant_id <> (SELECT tenant_id FROM projects WHERE id = NEW.project_id) THEN
    RAISE EXCEPTION 'tenant_id mismatch for project %', NEW.project_id;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER trg_tp_meta_tenant
  BEFORE INSERT OR UPDATE ON technical_plan_meta
  FOR EACH ROW EXECUTE FUNCTION check_tenant_match();
```

或者简单点：**只允许通过 Go 代码写入**，Go 侧从 context 取 `tenant_id`，不信任请求体。这样不需要触发器。**推荐后者**，少一层运行时开销。

### 5.2 任务表改造

`technical_plan_tasks` 原设计：

```sql
CREATE TABLE technical_plan_tasks (
  type TEXT PRIMARY KEY,      -- ← 问题所在
  task_id TEXT NOT NULL,
  status TEXT NOT NULL,
  ...
);
```

`type` 当主键 = 每种任务全局唯一。Web 版：

```sql
CREATE TABLE technical_plan_tasks (
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  task_id    UUID NOT NULL,
  status     TEXT NOT NULL,          -- pending|running|pausing|paused|success|error|canceled
  progress   INTEGER NOT NULL DEFAULT 0,
  stage      TEXT,
  stats      JSONB,
  error      TEXT,
  pause_requested BOOLEAN NOT NULL DEFAULT false,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, type)     -- ★ 每项目每类型一个
);

CREATE INDEX idx_tp_tasks_status ON technical_plan_tasks(tenant_id, status);
```

**并发控制**：桌面版「组内互斥」（同组任务不能同时跑）。用 `PRIMARY KEY (project_id, type)` 天然实现——插入冲突即表示同类型已在跑。

但注意：`content-generation` 和 `content-section-regeneration` 共用同一类型（见开发说明）。迁移时保持这个语义。

### 5.3 JSON 列 → JSONB

53 个 `*_json TEXT` 列全部改 `JSONB`。收益：

- 可以查询内部字段：`WHERE stats_json->>'image_count' > '10'`
- 自动校验 JSON 格式（脏数据写不进去）
- 存储更紧凑

**迁移注意**：SQLite 里可能存了 `NULL` 或空字符串 `''`。导入时要转成 `'{}'` 或 `NULL`：

```sql
UPDATE technical_plan_meta
   SET tender_files_json = NULLIF(tender_files_json, '')::jsonb
 WHERE tender_files_json IS NOT NULL;
```

**不要**为了 JSONB 就拆表。这些 JSON 列大多对应 Agent 的结构化输出，拆表反而增加读写复杂度。保持 JSONB，只在需要按某字段查询时加 GIN 索引：

```sql
CREATE INDEX idx_tp_meta_bid_sections ON technical_plan_meta USING GIN (bid_sections_json);
```

### 5.4 布尔列

125 个 INTEGER 里，几十个是 0/1 语义。识别规则：名字以 `is_` / `use_` / `has_` 开头，或以 `_enabled` / `_requested` 结尾。

```sql
-- 例：technical_plan_generation_config
use_ai_images        INTEGER NOT NULL DEFAULT 1  →  use_ai_images        BOOLEAN NOT NULL DEFAULT true
use_mermaid_images   INTEGER NOT NULL DEFAULT 1  →  use_mermaid_images   BOOLEAN NOT NULL DEFAULT true
word_count_repair    INTEGER NOT NULL DEFAULT 0  →  word_count_repair    BOOLEAN NOT NULL DEFAULT false
layout_check         INTEGER NOT NULL DEFAULT 0  →  layout_check         BOOLEAN NOT NULL DEFAULT false
pause_requested      INTEGER NOT NULL DEFAULT 0  →  pause_requested      BOOLEAN NOT NULL DEFAULT false
```

**注意 `word_count_repair` 这类**：桌面版有些「开关」字段实际可能存 `2` 表示「自动」，要逐个看 Store 代码确认，不能机械转 BOOLEAN。**这类字段建议保留 INTEGER**，加大注释说明取值。

### 5.5 文件路径

桌面版路径是绝对路径（`userData/workspace/technical-plan/xxx.md`）。Web 版必须变成**相对存储键**：

```
原：/Users/x/Library/.../workspace/technical-plan/tender-original.md
新：technical-plan/tender-original.md
```

存储根目录由配置提供：

```go
type Storage struct {
    root string  // /data/tenants/<tenant>/projects/<project>/
}
func (s *Storage) Resolve(key string) (string, error) {
    // 防路径穿越：key 不能含 ..、不能绝对路径
}
```

**所有 `*_path` / `*_markdown_path` 列的现有值都需要在迁移时重写**。这是迁移工具的关键工作。

---

## 六、任务系统（PG 队列）

桌面版 `taskService.cjs` 是内存 + Store 混合。Web 版改成 PG 表 + 长轮询 worker。

```sql
CREATE TABLE jobs (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,          -- bid-analysis | outline | content | export | ...
  payload      JSONB NOT NULL,
  status       TEXT NOT NULL DEFAULT 'queued',  -- queued|running|done|failed|canceled
  priority     INTEGER NOT NULL DEFAULT 0,
  attempts     INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 2,
  -- 抢占控制
  locked_by    TEXT,                   -- worker id
  locked_at    TIMESTAMPTZ,
  timeout_at   TIMESTAMPTZ,
  -- 结果
  result       JSONB,
  error        TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ
);

CREATE INDEX idx_jobs_claim ON jobs(priority DESC, created_at) WHERE status = 'queued';
CREATE INDEX idx_jobs_project ON jobs(project_id, status);
CREATE INDEX idx_jobs_locked ON jobs(locked_at) WHERE status = 'running';
```

**抢占查询**（多 worker 安全）：

```sql
UPDATE jobs
   SET status = 'running',
       locked_by = $1,
       locked_at = now(),
       started_at = now(),
       timeout_at = now() + interval '2 hours',
       attempts = attempts + 1
 WHERE id = (
   SELECT id FROM jobs
    WHERE status = 'queued'
    ORDER BY priority DESC, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
 )
RETURNING *;
```

**为什么不用 Redis**：

- 任务量级：几十个并发长任务，不是高 QPS 短任务
- 需要持久化：正文生成跑 30 分钟，服务重启不能丢
- 需要查询：用户要看到任务历史、用量统计
- PG 的 `SKIP LOCKED` 完全够用，少一个运维组件

**僵尸任务清理**：定时任务扫 `status='running' AND timeout_at < now()`，重置为 `queued`（`attempts < max_attempts`）或标 `failed`。

---

## 七、迁移工具设计

### 7.1 命令

```bash
# 导入一个 SQLite 工作区为一个项目
go run ./cmd/migrate-sqlite \
  --sqlite /path/to/yibiao.sqlite \
  --tenant <tenant-uuid> \
  --project-name "XX医院智能化项目" \
  --data-dir /path/to/workspace \      # 文件目录
  --dry-run

# 批量
go run ./cmd/migrate-sqlite --batch ./migrations.csv
```

### 7.2 处理流程

```
1. 读 SQLite（modernc.org/sqlite，纯 Go 免 CGO）
2. 校验 user_version（44），低于则拒绝或先跑桌面版升级
3. 创建 project 记录
4. 按表顺序导入（先父表后子表，处理外键）
   ├─ 单例行：id=1 → project_id
   ├─ 业务行：补 tenant_id + project_id
   ├─ JSON 列：'' → NULL，'xxx' → 'xxx'::jsonb
   ├─ 时间列：TEXT ISO → TIMESTAMPTZ
   ├─ 布尔列：0/1 → false/true（按字段白名单）
   └─ 路径列：绝对路径 → 相对键
5. 复制文件：workspace/ → /data/tenants/<t>/projects/<p>/
6. 校验：行数比对、外键完整、路径存在性
7. 输出报告
```

### 7.3 关键代码骨架

```go
type TableMigrator struct {
    Name      string
    Singleton bool                  // 是否单例表
    BoolCols  []string              // 需转 BOOLEAN 的列
    TimeCols  []string              // 需转 TIMESTAMPTZ 的列
    JSONCols  []string              // 需转 JSONB 的列
    PathCols  []string              // 需重写路径的列
    Skip      bool                  // 砍掉不迁的表
}

var tables = []TableMigrator{
    {Name: "technical_plan_meta", Singleton: true,
     TimeCols: []string{"created_at", "updated_at", "tender_imported_at", "original_plan_imported_at"},
     JSONCols: []string{"tender_files_json", "bid_sections_json", "outline_word_control_snapshot_json", "content_generation_runtime_json", "pending_tender_sections_json"},
     PathCols: []string{"tender_markdown_path", "tender_original_markdown_path", "original_plan_markdown_path", "pending_tender_markdown_path"}},
    // ...
}
```

### 7.4 不迁移的表

| 表 | 原因 |
|---|---|
| `official_invoice_info` | 发票功能砍掉 |
| `blind_check_*`（8 张） | 阶段 4 才做，但如果用户已有数据，可以先迁（结构不变） |

**建议全迁**，即使功能延后。数据比代码重要，先存着，功能上线就能用。

---

## 八、索引策略

每个业务表必备：

```sql
-- 租户过滤（几乎所有查询都要）
CREATE INDEX idx_<table>_tenant ON <table>(tenant_id);

-- 项目过滤（项目级表）
CREATE INDEX idx_<table>_project ON <table>(project_id);

-- 组合（最常用）
CREATE INDEX idx_<table>_scope ON <table>(tenant_id, project_id);
```

**避免过度索引**：PG 的写放大会因为索引多而变严重。技术方案正文生成时，`technical_plan_outline_nodes` 会被频繁 UPSERT，索引只留 `(project_id, parent_node_id, sort_order)` 一个就够。

---

## 九、迁移清单（62 张表逐一）

| # | 原表 | 目标表 | 作用域 | 特殊处理 |
|---|---|---|---|---|
| 1 | technical_plan_meta | 同名 | project | 单例→PK(project_id)，时间列，路径列 |
| 2 | technical_plan_generation_config | 同名 | project | 单例→PK，布尔列 |
| 3 | technical_plan_generation_bid_tasks | 同名 | project | 加 project_id |
| 4 | technical_plan_generation_reference_docs | 同名 | project | 加 project_id |
| 5 | technical_plan_tasks | 同名 | project | PK(type)→PK(project_id,type) |
| 6 | task_logs | 同名 | project | 自增→IDENTITY |
| 7 | technical_plan_bid_items | 同名 | project | 加 project_id |
| 8 | technical_plan_outline_nodes | 同名 | project | 加 project_id，自引用外键保留 |
| 9 | technical_plan_content_sections | 同名 | project | 含 node_id 外键，先迁节点 |
| 10 | technical_plan_content_plans | 同名 | project | 同上 |
| 11 | technical_plan_global_fact_groups | 同名 | project | 加 project_id |
| 12-25 | duplicate_check_*（14） | 同名 | project | meta 单例→PK，自增→IDENTITY |
| 26-33 | rejection_check_*（8） | 同名 | project | meta/extraction 单例→PK |
| 34-42 | knowledge_*（9） | 同名 | **tenant** | 不加 project_id |
| 43-48 | credential_library_*（6） | 同名 | **tenant** | profile 单例→PK(tenant_id) |
| 49 | export_templates | 同名 | project+tenant | 加 scope 字段 |
| 50-52 | feasibility_report_*（3） | 同名 | project | meta 单例→PK |
| 53 | official_invoice_info | — | — | 砍掉 |
| 54-62 | blind_check_*（8） | 同名 | project | meta/results/reviews/fix_results 单例→PK |

**外键顺序**（导入时必须遵守）：

```
technical_plan_outline_nodes  →  technical_plan_content_sections
                              →  technical_plan_content_plans
```

其余表无表间外键（除 `credential_library_projects` 被 `credential_library_images` 引用）。

---

## 十、goose 迁移组织

```
migrations/
├── 00001_tenants_users.sql
├── 00002_projects.sql
├── 00003_billing.sql
├── 00004_jobs.sql
├── 00005_technical_plan.sql
├── 00006_duplicate_check.sql
├── 00007_rejection_check.sql
├── 00008_knowledge_base.sql
├── 00009_credential_library.sql
├── 00010_templates_feasibility.sql
├── 00011_blind_check.sql
└── 00012_seed_plans.sql
```

**每个迁移必须可回滚**（写 `-- +goose Down`）。虽然生产很少回滚，但开发期频繁重建，`down` 能省大量时间。

---

## 十一、常见坑

| 坑 | 表现 | 规避 |
|---|---|---|
| JSON 列存了空字符串 | `invalid input syntax for type json` | 迁移时 `NULLIF(col,'')` |
| 时间列存了非 ISO 格式 | `invalid input syntax for type timestamp` | 迁移前 `SELECT DISTINCT length(col)` 排查 |
| 单例表有多行 | `duplicate key` | 迁移前 `SELECT COUNT(*)` 校验，>1 说明数据异常，报错不猜 |
| 路径含反斜杠（Windows） | 路径解析失败 | 迁移时统一 `filepath.ToSlash` |
| 路径是绝对路径 | Web 找不到文件 | 按 `workspace/` 前缀截断 |
| 自增 ID 直接搬 | 序列不同步，后续插入冲突 | 迁移后 `SELECT setval(...)` 重置序列 |
| `CHECK (id=1)` 残留 | 插入第二行报错 | 全部删掉，用 PK 替代 |
| BOOLEAN 机械转换 | `word_count_repair=2` 语义丢失 | 白名单转换，可疑字段保留 INTEGER |
| `type` 当 PK | 多用户互斥 | 改组合主键 |
| 缺少 tenant 过滤 | 越权读别人数据 | sqlc query 全部强制带 tenant_id |

**最后一条最重要**。建议在 sqlc 层做强制：所有 query 命名以 `-- name: XxxByTenant`，参数第一个必须是 `tenantID`，Code Review 时检查。
