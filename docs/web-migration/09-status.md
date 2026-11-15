# 项目状态总览

> 更新：2026-11-14
> 用途：一眼看清「已完成什么、缺什么、下一步做什么」

---

## 一句话状态

**全链路已打通且带配图（产品可用）**：
配模型 → 上传招标 → Agent 分析 → 目录 → 正文 → **配图生成** → **导出 Word（含图）** → 资产库

实测完整链路：
- 真实招标文件 → 17 章 96 节点目录（17 秒）
- 2 节正文（703 字）
- Agent 自动生成 2 张配图（Mermaid 流程图 21.7KB + HTML 组织架构图 15.0KB）
- Word 导出 36,711 字节，内嵌 2 张图，尺寸 11.7×7.8cm（wide）+ 9.5×9.5cm（square）

**P1 已补**：一致性检查（检出+修复）、空转检测、空闲超时、任务持久化。

**P2 已补**：配图编排（确定性算法）、Agent 提问应答。

**P3 已补**：前端界面完全接通（接口失败 28 → 0），数据契约对齐。

**P4 已补**：提问弹窗、产物库页面。

**P5 已补**：多项目切换（UI + 进度摘要 + 数据隔离验证）。

**P6 已补**：真正的任务恢复（复用工作区 + 跳过已完成小节）。

**P7 已补**：知识库完整移植（解析/切块/过滤/AI 提炼/搜索）。

**P8 已补**：正文生成引用知识库（自动匹配 + Prompt 注入 + 前端选择器）。

**P9 已补**：标书查重（四级归一化 + 核心片段包含度 + 主题分类目录分析）。

**P10 已补**：废标项检查 + 错别字检查 + 逻辑谬误检查（三类全通）。

**P11 已补**：暗标合规检查 + 自动修复（复用 C# 引擎，检查→修复闭环）。

**三大检查模块（查重/废标/暗标）全部完成。**

---

## 一、能力对照表

| 能力 | 桌面版 | Web 版 | 差距 |
|---|---|---|---|
| 招标文件解析（18 项） | ✅ 批量并发 | ✅ Agent 自主 | **Web 版更优**（Agent 能读全文） |
| 目录生成 | ✅ 5 阶段 | ✅ 单阶段 | 缺子目录细化、叶子调整、复核 |
| 正文生成 | ✅ 受限 HTML + 配图 + 表格 | 🟡 Markdown | **主要差距**（生成方式） |
| 图片生成 | ✅ AI/Mermaid/HTML 三种 | ✅ **Mermaid + HTML 已通** | AI 生图代码就绪未实测 |
| Word 导出 | ✅ C# 助手 | ✅ **已打通** | 结构/样式/表格正确，粗体受限 HTML 不支持 |
| 一致性检查 | ✅ | ✅ **检出+自动修复** | 桌面版多轮台账，Web 版单轮 |
| 字数控制 | ✅ | ✅ | 持平 |
| 知识库 | ✅ | ⬜ | 缺 |
| 标书查重 | ✅ | ⬜ | 缺 |
| 暗标检查 | ✅ | ⬜ | 缺 |

---

## 二、Web 版独有优势（比桌面版好）

| 优势 | 说明 |
|---|---|
| **多用户 + 权限** | 桌面版单机单用户 |
| **项目隔离** | 一个部署多个项目，桌面版一安装一项目 |
| **资产库** | 版本化、可追溯、可分享，桌面版只有本地文件 |
| **S3 对象存储** | 可扩展、可备份，桌面版本地磁盘 |
| **Agent 工具回调** | 业务逻辑在 Go（可测试），桌面版工具在 Node（强耦合） |
| **任务可观测** | 事件流 + 状态查询，桌面版靠日志 |

---

## 三、代码规模

| 部分 | 行数 |
|---|---|
| Go 主服务 | 36,202 |
| Node sidecar（agent + render） | 2,710 |
| sqlc 生成 | ~4,000（不计入） |

### 模块分布

```
internal/service/
├── storage/      1,015  S3 + LocalFS（含软链接防护）
├── artifact/       978  资产库（版本化、CommitWorkspace）
├── agent/        2,090  sidecar 客户端 + bidanalysis + outline + content
├── agenttool/      574  工具注册表 + 4 个工具
├── upload/         281  伪路径上传
├── settings/       400  配置 + API Key 加密
├── docparse/       470  docx/pdf → markdown
├── textutil/       174  字数统计（标书口径）
├── imageplan/      450  配图编排（评分 + 名额分配）
│   （agent/content 新增 resume.go 断点扫描）
├── knowledge/    2,050  知识库（切块 + 过滤 + 提炼 + 匹配）
├── duplicate/    2,450  标书查重（归一化 + 核心包含度 + 主题匹配）
├── rejection/    1,840  废标项检查（AI 条款抽取 + 逐条核验）
├── blind/        2,700  暗标检查 + 自动修复（预设 + 调 C# 规则引擎）
│   （handler 层新增 project.go 项目管理、compat.go 桌面版接口兼容）
├── consistency/    850  一致性检查 + 自动修复
├── taskrun/        320  任务运行记录（持久化）
├── openxml/        700  C# 助手进程池 + 受限 HTML 生成
├── render/         240  渲染 sidecar 客户端（Mermaid/HTML）
├── image/          520  配图生成编排 + AI 生图
├── export/         400  Word 导出编排（含图片物化）
├── workspace/      252  临时工作区生命周期
└── cron/            90  定时清理
internal/http/    2,391  路由 + 7 个 handler
internal/{auth,config,db,events,observ,bootstrap}  ~800
```

---

## 四、已验证的端到端链路

```
1. 配置模型
   POST /api/config/save-text  (API Key 加密入库)
   → 读回时返回 __CONFIGURED__ 占位符，不含明文

2. 上传招标文件
   前端 getPathForFile(file) → upload://<id> (同步)
   后台 POST /api/upload/raw  → 200

3. 导入解析
   POST /api/technical-plan/import-tender-document ["upload://<id>"]
   → 解析 docx → Markdown → 存资产 → 更新元数据

4. 生成目录
   POST /api/tasks/start-outline-generation (前端不传 provider，服务端从配置读)
   → Agent 读招标文件 → 分析评分 → 生成 outline.json (17 章 96 节点)

5. 目录落库
   POST /api/outline/save
   → 分配稳定 UUID → 递归插入 → 深度优先排序

6. 生成正文
   POST /api/content/generate
   → 逐节生成 → 每节落盘 → 清单跟踪
   → 1.1 (360字) / 1.2 (343字)

7. 生成配图（Agent 自己调用）
   generate_image 工具 → Go → 渲染 sidecar / 生图 API
   → 存资产库，返回 assetRef ”figures/<taskId>/xxx.png“

8. 导出 Word（含图）
   POST /api/export/word
   → 读目录+正文 → 转受限 HTML → 物化图片 → C# 助手
   → 36,711 字节 docx，内嵌 2 张图

9. 读取结果
   POST /api/technical-plan/load-state
   → 返回完整状态（招标文件 + 目录 + 正文）
```

---

## 五、下一步优先级

### P0（核心价值）— 已完成 ✅
1. **图片生成**：Mermaid + HTML 已通，AI 生图代码就绪
2. **Word 导出（含图）**：完整链路验证通过

### P1（下一步）
3. **配图编排**：哪些章节配图、什么布局
   - 需要主 Agent 分配名额 + 子 Agent 并行生成
4. **一致性检查**：全文事实、术语、编号的一致性
5. AI 生图实测（需配生图模型）

### P1（稳定性）— 已完成 ✅
4. 空转检测 + 空闲超时
5. 任务持久化 + 僵死标记
6. 一致性检查 + 自动修复

### P2（已完成 ✅）
7. 配图编排：确定性算法（评分 + 名额分配），含宽高比约束
8. Agent 提问应答：ask_user 工具 + 挂起/唤醒机制

### P3（已完成 ✅）
9. 前端界面接通：项目管理 + 数据契约对齐 + 桌面版接口兼容
   - 接口失败 28 → 0
   - 目录树、字数统计、项目名全部正常

### P4（已完成 ✅）
10. 提问弹窗：SSE 推送 + 弹窗渲染全链路打通
11. 产物库页面：新建页面 + IPC 类型 + 样式（Web 版独有）

### P5（已完成 ✅）
12. 多项目切换：侧边栏切换器 + 进度摘要 + 数据隔离验证

### P6（已完成 ✅）
13. 真正的任务恢复：复用旧工作区 + 扫描 manifest 断点 + Prompt 跳过已完成

### P7（已完成 ✅）
14. 知识库：四层数据模型 + 切块过滤 + AI 提炼 + 全文搜索

### P8（已完成 ✅）
15. 正文生成引用知识库：2-gram 关键词提取 + 加权匹配 + Prompt 注入

### P9（已完成 ✅）
16. 标书查重：句子提取 + 四级匹配 + 漏项检出 + 目录主题覆盖

### P10（已完成 ✅）
17. 废标项检查：AI 抽取条款 + 逐条核验 + 双层签名校验

### P11（下一步）
18. 暗标合规检查（桌面版 3,582 行，最大的一块）
19. ~~错别字 / 逻辑谬误检查实测~~（已完成）
20. 查重元数据深度指纹（rsid/OLE）+ 图片感知哈希
21. 知识条目审核 UI / 素材来源标注

---

## 六、运维信息

| 项 | 值 |
|---|---|
| 服务地址 | http://<SERVER_IP>:5180/ |
| API | http://<SERVER_IP>:8090/ |
| 默认账号 | admin@local / admin12345 |
| 启动 | `bash yibiao-web/run-all.sh --bg` |
| 停止 | `bash yibiao-web/run-all.sh --stop` |
| 状态 | `bash yibiao-web/run-all.sh --status` |
| 环境初始化 | `bash yibiao-web/deploy/init-dev-env.sh` |

### 关键环境变量

| 变量 | 说明 |
|---|---|
| `STORAGE_BACKEND` | `local` / `s3` |
| `S3_ENDPOINT` / `S3_BUCKET` | RustFS 地址与桶名 |
| `SECRETS_KEY` | 加密 API Key 的主密钥（生产必须） |
| `JWT_SECRET` | 令牌签名密钥（生产至少 32 字符） |
| `AGENT_SIDECAR_URL` | sidecar 地址，默认 `127.0.0.1:7070` |

### 重启前必查

```bash
# 有运行中的任务时不要重启，会杀掉长任务
curl -s -X POST localhost:8080/api/tasks/get-active \
  -H "Authorization: Bearer <token>" -H "X-Project-Id: <pid>"
```
