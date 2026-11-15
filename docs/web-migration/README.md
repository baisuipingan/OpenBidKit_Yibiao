# Web 版迁移方案索引

> 基线：`client/` 桌面版（2026-11-12，1066 commits）
> 目标：Go 后端 + 浏览器前端，先单公司定制，预留公网多租户
> 创建：2026-11-13

---

## 文档清单

| 文档 | 内容 | 什么时候看 |
|---|---|---|
| [`00-overview.md`](./00-overview.md) | 总体架构、技术选型、进程边界、风险清单 | **先看这个** |
| [`01-bridge-contract.md`](./01-bridge-contract.md) | 256 个 bridge 方法的 HTTP 映射表 | 开始写接口时 |
| [`02-postgres-schema.md`](./02-postgres-schema.md) | 62 张表迁移到 PG 的设计 | 建库时 |
| [`03-feasibility-report.md`](./03-feasibility-report.md) | 三个关键技术验证的实测结果 | 动手前必看 |
| [`04-roadmap.md`](./04-roadmap.md) | 分阶段任务、里程碑、人力配置 | 排期时 |

---

## 核心结论速查

### 架构

```
浏览器（复用 43,860 行 React，改仅约 10 人日）
    │ HTTP + SSE
    ▼
Go 主服务（业务 / 认证 / 数据 / 任务 / 文件）
    ├─ HTTP ──▶ Node Agent Sidecar（Pi SDK，MIT 开源，已实证可独立跑）
    ├─ stdio ─▶ C# OpenXmlHelper（23,172 行，零平台特定代码，原样保留）
    ├─ CDP ──▶ Chromium（mermaid 转图 / Word 布局测量）
    └─ SQL ──▶ PostgreSQL 17
```

### 关键数字

| 项 | 数值 |
|---|---|
| 前端代码 | 43,860 行，**零 Electron 依赖**，可原样复用 |
| 需迁移的 Node 业务代码 | 56,816 行 → 净译约 20-24k 行 Go |
| bridge 接口 | 256 个方法 → 保留约 168 个，砍 68 个 |
| 数据库表 | 62 张（其中 **12 张是单例表，必须改结构**） |
| C# 助手 | 23,172 行，**原样保留** |
| 总工期 | 26-34 周（单人全职） |

### 三个已验证的事实

| 验证 | 结果 |
|---|---|
| 前端可脱离 Electron | ✓ 零 `import electron/fs/path`，零 `window.require` |
| C# 可在 Linux 跑 | ✓ 零 `DllImport`/COM/平台分支，依赖全纯托管 |
| Pi SDK 可独立跑 | ✓ **实测通过**：纯 Node 导入 143 个符号，`ModelRuntime`/`SessionManager`/`defineTool` 全部可用，**MIT 许可** |

### 最大的三个坑

1. **12 张单例表 `CHECK (id = 1)`** —— 数据模型没有项目概念，必须改成 `PRIMARY KEY (project_id)`
2. **`saveOutline()` 的 reason 协议** —— `replace`/`edit`/`delete`/`add-*`/`sort` 的清理语义，最容易搬错
3. **字体映射** —— Linux 没有宋体/黑体，不配 fontconfig 别名会导致页码测量出错

### 建议砍掉的功能

插件系统、自动更新、捐赠、官方账号充值/发票、开发者工具页、GPU 设置、双层 PDF（可后置）

**砍掉省 6-8 周。**

---

## 三步开工

```bash
# 1. 验证前端（半天）
cd client && npm ci
npx vite --host 127.0.0.1 --port 5173    # 不启 Electron，看浏览器能否渲染

# 2. 建 Go 骨架（半天）
mkdir ../yibiao-web && cd ../yibiao-web && go mod init github.com/patrick/yibiao-web

# 3. 写 bridge 适配层原型（1 天）
# 在 client/src/shared/bridge/ 新建 web.ts，用 Proxy 实现，配 mock 后端
```

---

## 与其他文档的关系

- `AGENTS.md`：桌面版开发规范，**迁移时作为行为基准**（哪些约束必须保持）
- `client/开发说明.md`：481 行，桌面版架构真源，**迁移时反复查**
- `openxmlhelper/开发说明.md`：C# 助手协议，**不用改但要看懂**
- `sql/workspace_schema.sql`：62 张表定义，**PG schema 的输入**

---

## 与原方案的差异说明

初版评估时我说 Pi SDK 是「闭源私有、不可移植」，**这个判断错了**：

- 实测：`@earendil-works/pi-coding-agent@0.80.10` 是 **MIT 许可**
- `dist/` 是编译后 JS，`package/` 里有完整 `docs/` 和 `examples/`
- 依赖 19 个包，全纯 Node，无原生绑定
- `services/pi/` 5,390 行**零 Electron 依赖**

所以 sidecar 不只是「权宜之计」，而是**长期可持续的方案**——即使 SDK 停止维护，源码在手可以自己维护。
