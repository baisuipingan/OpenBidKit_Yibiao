# UI 重设计续作报告：三项深度改造（2026-11-15）

承接 `15-ui-redesign-implementation.md` 中「有意未做」的三项，全部完成。

---

## 一、配置页签改竖排导航

### 改了什么

**结构**：从「7 个横排页签 + 下方面板」改为「208px 左侧竖排导航 + 右侧面板」。

```
改前：
┌────────────────────────────────────────────┐
│ 写嘛 | 我有方案 | 知识库 | 写多少 | 插图吗…  │ ← 最后一个被切
├────────────────────────────────────────────┤
│  面板（宽度被页签挤压）                       │
└────────────────────────────────────────────┘

改后：
┌──────────────┬─────────────────────────────┐
│ 生成范围      │                             │
│ 原方案参考    │   面板 856px                │
│ 知识库引用    │   （宽度充足）               │
│ 字数控制 ◀    │                             │
│ 配图设置      │                             │
│ 生成策略      │                             │
│ 导出模板      │                             │
│ ─────────    │                             │
│ 4/7 已配置    │                             │
└──────────────┴─────────────────────────────┘
```

**关键实现**：
- `.generation-settings-shell` 从 `grid-template-rows: auto auto minmax(0,1fr)` 改为 `grid-template-columns: 208px minmax(0,1fr)`
- 头部 `grid-column: 1 / -1` 跨两列
- 每项显示「标签 + 当前值摘要」，不再压缩成一行
- 左下角显示总完成度进度条
- 新增 `tabConfigured` 判断（真实配置状态，不是「摘要非空」）

**响应式**：≤1180px 自动退回横排，避免左侧导航挤压主面板。

### 顺带修复的文案

| 位置 | 改前 | 改后 |
|---|---|---|
| 页签 | 写嘛 / 长嘛样 | 生成范围 / 导出模板 |
| 补全模式 | 胡咧咧模式 | AI 补全（自动填充） |
| 补全模式 | 别招欠模式 | 笼统表述（推荐） |
| 补全模式 | 放着我来模式 | 标记待填写 |
| 短名 | 胡咧咧 / 别招欠 / 放着我来 | AI 补全 / 笼统表述 / 待填写 |
| 提示文案 | 「长嘛样」 | 「生成设置 → 导出模板」 |

**「胡咧咧模式」的描述也重写了**：原文是「AI 会编辑不存在的人名」，改为明确的风险提示「可能生成不存在的人名、业绩或型号，投标前必须逐项核对。适用于内部讨论稿，不建议直接用于正式投标。」

### 补充的影响说明卡片

右侧面板新增「当前选择的影响」：

```
当前选择的影响  [完整标]
目录结构   按招标文件「投标文件组成」逐条响应，涵盖商务、技术、资格全部要求
章节数量   通常 12-18 章，与招标文件要求一一对应
适用场景   招标文件明确要求技术标包含商务内容的项目
```

把抽象的选项名翻译成具体后果，同时填补右侧空白。

### 验证

```
竖排导航: 7 项 | 208px | 4/7 已配置 | 影响卡 ✓ | 口语残留 无
主面板宽度: 856px（原被页签挤压）
```

---

## 二、知识库改表格视图

### 改了什么

**结构**：从卡片列表改为 6 列表格。

```
改前（每条占一整块）：
┌──────────────────────────────────────┐
│ ⠿ [MD] 技术方案测试.md  完成          │
│        5 条知识 2026-11-14            │
│                        [Markdown][删除]│
└──────────────────────────────────────┘

改后（一行一条）：
 文档名称              知识条目  状态  更新时间    操作
 ⠿ [MD] 技术方案测试.md    5 条   完成  2026-11-14  ⋯
```

**列定义**：`26px 手柄 | 1fr 文档名 | 76px 条目数 | 70px 状态 | 96px 时间 | 128px 操作`

### 三次踩坑与修正

**坑 1：`display: contents` 打乱列顺序**

最初想用 `display: contents` 让卡片的内部元素直接参与 grid，结果 `doc-ext` 和 `title` 各占一列，整体错位一格：

```
x=654  doc-ext（本该和 title 同列）
x=952  title（被挤到第 3 列）
```

**修法**：放弃 CSS 强行重排，改为在 JSX 里直接构建表格行结构——每个单元格就是 grid 的直接子项，列顺序由 DOM 决定。

**坑 2：表头被拉到 276px 高**

`.knowledge-table-head` 是 `.knowledge-document-panel`（grid，有 `minmax(0,1fr)`）的子项，不设高度会被拉伸：

```
head: top=258 h=276  ← 只有一行文字，却有 276px 高
```

**修法**：`height: auto; min-height: 0; align-self: start; grid-template-rows: auto`。

**坑 3：表头与数据行之间 244px 空白**

panel 的 `grid-template-rows` 只分配了两行（`auto minmax(0,1fr)`），但实际有三个子项（页头 / 表头 / 列表），导致行分配错乱。

**修法**：改为 `auto auto minmax(0, 1fr)` 三行。

### 保留的能力

改造中**没有丢任何功能**：

| 能力 | 状态 |
|---|---|
| 拖拽排序（跨文件夹） | ✓ 保留 |
| 悬停显示操作按钮 | ✓ 保留（opacity 0→1） |
| 点击文件名打开条目 | ✓ 保留 |
| 处理中进度条 | ✓ 保留（跨列展示） |
| 错误信息 | ✓ 保留（跨列红色小字） |
| 开发者调试信息 | ✓ 保留（跨列等宽字体） |
| 分页（加载更多） | ✓ 保留 |
| 窄屏响应式 | ✓ 退回卡片布局 |

### 验证

```
表头: 6 列 ✓ | 表头-数据间距: 2px（原 244px）
各列位置: 616/654/952/1056/1122/1230（完全对齐）
hover 操作列: opacity 1 ✓
拖拽手柄: opacity 0.55 ✓
点击文件名: ✓ 已打开条目
JS 错误: 无
```

---

## 三、空态组件统一

### 改了什么

**增强 `shared/ui/EmptyState.tsx`**，保持向后兼容（旧用法仍可用）：

新增属性：
- `tone`: `neutral | info | success | warning | error` — 决定图标与配色
- `size`: `card | page` — 面板内 vs 整页
- `icon`: 自定义图标
- `tip`: 底部补充提示

**三段式结构**：
```
[图标 46px]
标题
一句话说明为什么这里没内容
[主操作按钮]  ← 关键：必须给出下一步
─────────────
💡 补充提示
```

### 替换的页面

| 页面 | 处数 | 说明 |
|---|---|---|
| 模板页 | 3 | 加载态 / 我的模板空 / 预览区空 |
| 查重页 | 8 | 元数据/目录/正文/图片 × (等待/无重复) |
| 知识库 | 1 | 升级为 `size="page"` + `tip` |

**查重页的关键改进**：把「无重复」从空态改成 `tone="success"` + 绿勾图标。之前用灰扑扑的空态表达「好消息」，反而让用户以为没跑成功。

### 设计规范

```tsx
// 未配置 / 无数据
<EmptyState title="还没有文档" hint="上传后 AI 会自动引用" tone="info" icon="▦">
  <button className="primary-action">上传文档</button>
</EmptyState>

// 正面结果
<EmptyState title="未发现重复句子" tone="success" icon="✓" size="page" />

// 加载中
<EmptyState title="正在读取" hint="请稍候..." tone="info" icon="◌" />
```

### 验证

```
空态数量: 1（模板页当前可见）
有图标: ✓
有操作区: ✓
```

---

## 四、额外发现并修复的缺陷

### 后端步骤白名单与前端类型完全对不上

**现象**：`update-step` 报 `无效的步骤: generation-settings`，页面无法进入生成设置。

**根因**：白名单写的是 `content-generation` / `export`，但前端 `TechnicalPlanStep` 类型是 `generation-settings` / `content-edit` / `expand`。

| 前端类型 | 后端白名单（改前） | 匹配 |
|---|---|---|
| `document-analysis` | `document-analysis` | ✓ |
| `generation-settings` | — | ✗ |
| `bid-analysis` | `bid-analysis` | ✓ |
| `outline-generation` | `outline-generation` | ✓ |
| `global-facts` | `global-facts` | ✓ |
| `content-edit` | `content-generation` | ✗ |
| `expand` | `export` | ✗ |

**修复**：
1. 白名单对齐前端类型
2. 加旧值映射 `content-generation → content-edit`、`export → content-edit`、`documents → document-analysis`

**教训**：这是上一轮加白名单时留下的坑——凭记忆写了值，没核对前端类型定义。**任何跨端的枚举都必须以一端为准并写注释指明来源。**

---

## 五、验收

### 自动化检查

```
① 生成设置竖排: 7 项 | 208px | 4/7 已配置 | 影响卡 ✓ | 口语残留 无
② 知识库表格: 6 列表头 | 表头-数据间距 2px
③ 空态统一: 有图标 ✓ 有操作区 ✓
JS 错误: 无
```

### 全站回归

```
18 通过 / 0 失败
```

### 构建

```
tsc --noEmit   通过
vite build     ✓ 13-14s
```

---

## 六、改动文件清单

### 新增（1）

| 文件 | 说明 |
|---|---|
| `client/src/styles/component-process-stepbar.css` 已存在 | — |

本轮无新增文件，全部为改造。

### 修改（10）

| 文件 | 改动 |
|---|---|
| `client/src/features/technical-plan/pages/GenerationSettingsPage.tsx` | 竖排导航、完成度、影响卡片、选项文案专业化 |
| `client/src/features/technical-plan/pages/ContentEditPage.tsx` | 清理「长嘛样」文案 |
| `client/src/features/technical-plan/pages/TechnicalPlanHome.tsx` | 清理「长嘛样」文案 |
| `client/src/features/knowledge-base/pages/KnowledgeBasePage.tsx` | 表格视图重构 + 空态升级 |
| `client/src/features/duplicate-check/pages/DuplicateCheckPage.tsx` | 8 处空态统一 |
| `client/src/features/export-format/pages/MyTemplatesPage.tsx` | 3 处空态统一 |
| `client/src/shared/ui/EmptyState.tsx` | 增强：tone / size / icon / tip |
| `client/src/shared/ui/index.ts` | 导出新类型 |
| `client/src/styles/shared-components.css` | 空态三段式样式 |
| `client/src/styles/feature-technical-plan.css` | 竖排导航样式 |
| `client/src/styles/feature-knowledge-base.css` | 表格视图样式 |
| `yibiao-web/internal/service/technical_plan.go` | 步骤白名单对齐 + 旧值映射 |

---

## 七、三项全部完成后的整体状态

| 方案项 | 状态 |
|---|---|
| 全局步骤条 | ✓ |
| Toast 去重 | ✓ |
| 操作栏固定 | ✓ |
| 目录页侧栏折叠 | ✓ |
| 模板/知识库/查重空态 | ✓ |
| 配置页签竖排 | ✓ |
| 文案专业化 | ✓ |
| 目录标题去重 | ✓ |
| 设置页 Web 化 | ✓ |
| 登录 Logo | ✓ |
| 产物库行操作 | ✓ |
| 资信库分组 | ✓ |
| **知识库表格视图** | ✓ |
| **空态组件统一** | ✓ |

`14-ui-redesign.md` 里的方案项全部落地，无遗留。
