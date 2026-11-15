# Go 后端开发说明

> 位置：`yibiao-web/`
> 状态：阶段 0 - 1 过渡期，已打通「登录 → 业务接口 → 数据库」完整链路

---

## 一、当前实现范围

| 模块 | 状态 | 说明 |
|---|---|---|
| 配置加载 | ✅ | 环境变量，dev/prod 校验分离 |
| 日志 | ✅ | slog，dev 文本 / prod JSON |
| 数据库连接 | ✅ | pgxpool，含健康检查 |
| 迁移 | ✅ | goose + embed，二进制自带 SQL |
| 认证 | ✅ | JWT + bcrypt + 刷新令牌轮换 |
| 事件总线 | ✅ | 进程内，按 tenant/user 隔离 |
| SSE | ✅ | 单端点多 topic，带心跳 |
| 技术方案 | 🟡 | 只有 loadState / updateStep |
| 项目 | 🟡 | 只有 CLI 创建，无 HTTP 接口 |
| AI 服务 | ⬜ | 未开始 |
| Agent sidecar | ⬜ | 未开始 |
| 文件上传 | ⬜ | 未开始 |
| Word 导出 | ⬜ | 未开始 |

代码规模：约 2,600 行 Go（不含 sqlc 生成的 `internal/store/`）。

---

## 二、目录结构

```
yibiao-web/
├── cmd/
│   ├── server/main.go      服务入口：配置 → 日志 → DB → 迁移 → HTTP → 优雅退出
│   └── cli/main.go         开发工具：建项目/建用户/列项目
├── internal/
│   ├── config/             环境变量配置与校验
│   ├── observ/             slog logger + context 传递
│   ├── db/                 连接池 + goose 迁移（含 migrations/）
│   ├── auth/               密码哈希、JWT、context 身份传递
│   ├── events/             事件总线（SSE 的数据源）
│   ├── bootstrap/          首次启动初始化默认租户/管理员
│   ├── store/              sqlc 自动生成，不手改
│   ├── service/            业务逻辑（事务边界在这层）
│   └── http/
│       ├── httpx/          请求/响应信封、错误定义
│       ├── middleware/     认证、请求ID、日志、panic恢复
│       ├── handler/        参数解析与响应封装
│       └── server/         路由装配
├── sqlc/
│   ├── sqlc.yaml
│   └── query/*.sql         SQL 定义（唯一真相）
└── run-api.sh              本地启动脚本
```

**依赖方向**：handler → service → store。store 无业务逻辑，service 不碰 HTTP。

---

## 三、关键设计决策

### 3.1 业务失败返回 HTTP 200

这是**最重要的约定**，与 REST 惯例相反，但必须这样做。

```jsonc
// 成功
{ "ok": true, "data": {...} }

// 业务失败（HTTP 200）
{ "ok": false, "error": { "code": "NOT_FOUND", "message": "..." } }
```

**原因**：桌面版 bridge 用 Promise reject 传递业务错误，前端 60 个文件里有大量 `try/catch` 依赖 `error.code`。如果用 HTTP 4xx，`fetch` 不会 reject，前端全部要改。

只有这些用非 200：
- `401` 未登录/令牌失效
- `403` 权限不足
- `400` 请求体格式错误
- `500` 服务崩溃

**实现位置**：`internal/http/httpx/response.go` 的 `ErrorHandler`。所有 handler 直接 `return err`，统一汇聚。

### 3.2 租户 ID 只从令牌取

```go
// middleware/auth.go
c.Set(CtxTenantID, claims.TenantID)   // 只从 JWT
```

**绝不**从 `X-Tenant-Id` 请求头或请求体读取。这是越权防护的第一道闸。

业务层每个入口都要调 `ensureProject()`：

```go
// service/technical_plan.go
func (s *TechnicalPlanService) ensureProject(ctx, tenantID, projectID) error {
    _, err := s.q.GetProject(ctx, store.GetProjectParams{ID: projectID, TenantID: tenantID})
    if errors.Is(err, pgx.ErrNoRows) {
        return httpx.NotFound("项目不存在或无权访问")  // 不区分不存在与无权限
    }
    ...
}
```

**注意**：故意不区分「项目不存在」和「无权访问」，避免泄露项目存在性。

### 3.3 单例表改为 project_id 主键

桌面版 12 张表是 `CHECK (id = 1)`，只能存一行。已全部改成：

```sql
-- 桌面版
CREATE TABLE technical_plan_meta (id INTEGER PRIMARY KEY CHECK (id = 1), ...);

-- Web 版
CREATE TABLE technical_plan_meta (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    ...
);
```

**冗余 `tenant_id` 的理由**：每个查询都要带租户过滤，如果每次 JOIN `projects` 才能拿到 `tenant_id`，SQL 会变复杂且慢。冗余一列、由 service 层保证一致（不信任请求体）是标准做法。

### 3.4 任务表改用组合主键

```sql
-- 桌面版：一个类型全局唯一
CREATE TABLE technical_plan_tasks (type TEXT PRIMARY KEY, ...);

-- Web 版：每个项目每个类型一个
CREATE TABLE technical_plan_tasks (
    project_id UUID NOT NULL,
    type       TEXT NOT NULL,
    ...
    PRIMARY KEY (project_id, type)
);
```

不改的话，A 用户跑正文生成时 B 用户就启动不了。

### 3.5 JSONB 而不是 TEXT

桌面版 53 个 `*_json TEXT` 列全部改 `JSONB`。收益：可查询内部字段、自动校验格式、存储更紧凑。

Go 侧 sqlc 映射成 `[]byte`，service 层用 `rawJSON()` 转成 `any` 给前端：

```go
func rawJSON(b []byte) any {
    if len(b) == 0 { return map[string]any{} }
    var v any
    if err := json.Unmarshal(b, &v); err != nil { return map[string]any{} }
    if v == nil { return map[string]any{} }
    return v
}
```

**注意**：解析失败返回 `{}` 而不是 `nil`，否则前端拿到 `null` 会崩。

### 3.6 时间列用 TIMESTAMPTZ

桌面版存 ISO 字符串（`new Date().toISOString()`）。Web 版用原生 `TIMESTAMPTZ`，能范围查询和索引。

sqlc 对**可空** timestamptz 生成 `pgtype.Timestamptz`（非 `*time.Time`，因为 `emit_pointers_for_null_types: false`）。取用时要判 `.Valid`：

```go
if meta.TenderImportedAt.Valid {
    s := meta.TenderImportedAt.Time.UTC().Format(time.RFC3339)
    st.TenderImportedAt = &s
}
```

### 3.7 事件总线按 scope 隔离

```go
type Scope struct {
    TenantID  string
    UserID    string   // 空 = 租户内广播
    ProjectID string   // 空 = 租户级事件
}
```

匹配规则（`events/bus.go` 的 `matchScope`）：
- tenant 必须相等（强制，任一为空则不匹配）
- 事件带 `UserID` 时只发给该用户
- 事件带 `ProjectID` 时订阅者也必须限定该项目

**缓冲区满了丢最旧事件，不阻塞发布者**。SSE 端会在下次心跳让客户端刷新全量状态。

---

## 四、常用命令

```bash
# 进容器
ssh -t srv253 "docker exec -it yibiao-dev-workspace bash"

# 编译
cd /workspace/yibiao-web && go build ./...

# 启动（前台，代码改动后需重启）
bash run-api.sh

# 后台启动
bash run-api.sh --bg
tail -f /tmp/api.log

# 停止
bash run-api.sh --stop

# 只跑迁移
bash run-api.sh --migrate

# 重新生成 sqlc 代码（改了 query/*.sql 或 migrations 后）
cd sqlc && sqlc generate

# 建测试项目
go run ./cmd/cli create-project --name "测试项目"
go run ./cmd/cli list-projects

# 建测试用户
go run ./cmd/cli create-user --email a@b.com --password 12345678 --role admin
```

### 改 schema 的流程

1. 新增迁移文件 `internal/db/migrations/NNNNN_xxx.sql`（**不要改已有迁移**）
2. 更新 `sqlc/query/*.sql`
3. `cd sqlc && sqlc generate`
4. `go build ./...`
5. 重启服务（迁移自动执行）
6. 把生成的 `internal/store/` 拉回本机让 IDE 正常

```bash
# 拉回生成代码
rsync -az srv253:/opt/yibiao-dev/workspace/yibiao-web/internal/store/ ./internal/store/
```

---

## 五、默认凭据（开发环境）

| 项 | 值 |
|---|---|
| 租户 ID | 启动日志里打印，或 `GET /api/public/bootstrap` |
| 管理员邮箱 | `admin@local` |
| 管理员密码 | `admin12345` |
| 界面提示 | 无（需手输） |

**首次登录后必须改密码**——`mustChangePassword: true` 已在用户记录里，但改密接口还没实现。

单租户部署时前端会自动从 `/api/public/bootstrap` 拿到租户 ID，用户不用填。

---

## 六、验证清单

改代码后按这个顺序验：

```bash
# 1. 编译
cd /workspace/yibiao-web && go build ./...

# 2. 格式化检查（可选）
gofmt -l . | grep -v internal/store

# 3. 启动并跑迁移
bash run-api.sh --bg && tail -20 /tmp/api.log

# 4. 健康检查
curl -s localhost:8080/health
curl -s localhost:8080/health/ready

# 5. 端到端（浏览器 → Go → PG）
cd /workspace && node scripts/dev/verify-e2e.mjs

# 6. 登录流程
cd /workspace && node scripts/dev/verify-login.mjs
```

**改动涉及认证、租户隔离、错误码时，必须跑 5 和 6。**

---

## 七、已知缺口

| 缺口 | 影响 | 计划 |
|---|---|---|
| 改密接口 | 默认密码无法改 | 阶段 1 初 |
| 项目 HTTP 接口 | 只能用 CLI 建项目 | 阶段 1 |
| 绝大多数业务接口 | 前端 4 个页面能用，其余报 404 | 随功能推进 |
| 令牌黑名单 | 登出后 access token 仍有效到过期 | 可接受（15 分钟） |
| 事件只在进程内 | 多实例部署时跨实例收不到 | 多实例时换 PG LISTEN/NOTIFY |
| 无速率限制 | 登录可被暴力破解 | 上线前加 |
| 无审计日志 | 敏感操作无痕迹 | 上线前加 |

---

## 八、前端如何对接

`client/src/shared/bridge/web.ts` 是适配层，用 Proxy 自动把 `window.yibiao.xxx.yyy()` 转成 `POST /api/xxx/yyy`。

```ts
// 前端调用（与桌面版完全一致）
await window.yibiao.technicalPlan.loadState()

// 实际发出
POST /api/technical-plan/load-state
Authorization: Bearer <token>
X-Project-Id: <uuid>
```

**新增接口时**：只要 Go 侧的路由符合「命名空间 kebab-case / 方法 kebab-case」，前端零改动。

不提供的命名空间（如 `database`）在 `OMITTED_NAMESPACES` 里列出，返回 `undefined`，让前端走「可选能力缺失」分支。

### 前端运行模式

| 模式 | 触发条件 | 行为 |
|---|---|---|
| `desktop` | Electron preload 已注入 | 本模块不生效 |
| `web` | 浏览器 + 有后端 | HTTP + SSE |
| `mock` | URL 加 `?mock=1` | 返回空数据，仅调试 UI |

### 登录守卫

`client/src/app/web/AuthGate.tsx`：Web 模式无令牌时显示登录页，桌面版直接透传。

`database` 命名空间不暴露，让 `WorkspaceDatabaseGate` 走「无 database 即放行」分支——Web 版数据库在服务端，启动时已迁移完毕。

---

## 九、下一步

按路线图推进，优先级：

1. **项目 HTTP 接口**（CRUD）——否则前端无法建项目
2. **文件上传 + 存储抽象**——技术方案第一步是导入招标文件
3. **文档解析**（docx/pdf → markdown）——接 LibreOffice
4. **AI 服务**——所有生成类功能的基础
5. **Agent sidecar**——最难，但核心价值所在

**建议先做 1-3**，因为它们能立刻让技术方案页面产生可见进展；AI 和 Agent 是重头戏，单独开一段时间集中攻。
