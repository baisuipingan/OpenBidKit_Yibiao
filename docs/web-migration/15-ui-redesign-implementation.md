# UI 重设计实施报告（2026-11-15）

按 `14-ui-redesign.md` 的方案分四批实施完毕。

## 一、实施清单

### 第一批：流程骨架（P0）

#### 1.1 全局步骤条

**新增文件**
- `client/src/shared/ui/ProcessStepBar.tsx` — 组件（3.2 KB）
- `client/src/styles/component-process-stepbar.css` — 样式（4.3 KB）

**功能**
```
①──②──③──④──⑤──⑥──⑦
✓  ✓  ✓  ●  ○  ○  ○
```

- 状态：`done`（绿实心 + 勾）/ `active`（蓝实心 + 光晕）/ `pending`（灰空心）/ `error`（红实心 + 感叹号）
- 每步显示**摘要**：字数、分析项数、节点数、章节进度
- 已完成步骤可点击回跳，hover 有缩放反馈
- 吸顶显示（`position: sticky`）

**接入位置**：`TechnicalPlanHome.tsx`
- 自动排除 `expand`（扩写是子流程，不在主链路）
- 自动跳过 `global-facts`（当目录无 AI 小节时）
- 步骤条 `margin-inline: -44px` 撑满 `content-shell` 的 padding，真正贴到顶部和两侧

**验证**：
```
步骤数: 6
✓ 选择标书      4,182 字
✓ 生成设置      已配置
✓ 招标文件解析   20 项
● 目录生成      101 节点   ← 当前
○ 全局事实设定   2 条
○ 生成正文      0 / 86
位置: top=0（已吸顶）
```

#### 1.2 Toast 去重

**改动**：`ToastProvider.tsx`
- 新增 `recentToastsRef`（Map），记录「类型 + 标题 + 文案 → 时间」
- 相同内容 3 秒内只显示一次，返回 `-1` 表示跳过
- 新增 `ToastOptions.dedupe`，可显式关闭（如用户手动重试）
- Map 超 40 条时清理 10 秒前的过期记录，避免内存增长

**为什么这样修**：同一错误常被局部 `catch` 和全局兜底各弹一次。与其找出所有重复调用点（容易漏），不如在出口统一拦截。

#### 1.3 操作栏改固定定位

**改动**：`shared-components.css`
- `position: absolute` → `position: fixed`
- `bottom: 28px` → `bottom: 20px`，水平居中
- 新增 `.has-page-toolbar { padding-bottom: 88px }`

**验证**：
```
工具条: position=fixed 距底=20px
内容区底部留白: 88px
与工具条重叠的元素: 0   ← 不再遮挡
```

---

### 第二批：空间利用（P0）

#### 2.1 目录页侧栏可折叠

**改动**：`OutlineEditPage.tsx` + `feature-technical-plan.css`

- 新增 `progressRailCollapsed` 状态，**默认收起**
- 收起态渲染成 48px 竖条：进度环（`conic-gradient` 画）+ 竖排文字
- 展开态右上角有收起按钮

**效果**：
```
收起时: 48px  676px  340px   ← 目录树 676px
展开时: 300px 424px  340px   ← 目录树 424px

目录树多出 252px，长标题不再换行。
```

#### 2.2 三个页面空态重构

**模板页**
- 发现原代码**已有**系统预设分组渲染逻辑，只是数据库里没数据
- 从 `DEFAULT_EXPORT_FORMAT` 生成 3 个预设写入数据库：
  - **标准投标模板**：默认配置，宋体小四 1.5 倍行距
  - **政府项目模板**：仿宋三号，页边距 3.7/3.5/2.8/2.6，页码 `— {page} —`
  - **暗标专用模板**：无页眉页脚、无页码、无标题边框（身份信息零暴露）

**知识库**
- 新增顶部 4 个统计卡：文档总数 / 文件夹 / 知识条目 / 内容块
- 文档列表下方加引导卡「继续补充企业素材」，填充剩余空白

**查重结果**
- 无重复时不再用占满屏的空态
- 改为成功卡片：绿色对勾 + 结论 + 说明 + 下一步引导
- `running` 状态保持原空态文案

**验证**：
```
知识库统计卡: 4 个（1 文档 / 1 文件夹 / 5 知识条目 / 6 内容块）
模板页: 3 张系统预设卡片
```

---

### 第三批：专业感（P1）

#### 3.1 配置页签文案专业化

**改动**：`generationSettingsTabs.ts`

| 改前 | 改后 |
|---|---|
| 写嘛 | 生成范围 |
| 我有方案 | 原方案参考 |
| 知识库 | 知识库引用 |
| 写多少 | 字数控制 |
| 插图吗 | 配图设置 |
| 怎么写 | 生成策略 |
| 长嘛样 | 导出模板 |

同时给每个页签加 `description`，悬停 tooltip 显示「页签名 + 说明 + 当前值」。

#### 3.2 目录标题去重

**根因**：数据库 `title` 已带编号（`第五章 质量管理体系与措施`），前端 `formatOutlineTitle(number, title)` 又拼了一次 `number`。

**修复**：`shared/utils/outlineNumbering.ts`
- 新增 `titleHasOwnNumber()`，识别 3 类自带编号：
  - `第X章/节/条` 开头
  - 数字编号开头（`1.1 ` / `1、`）
  - 括号编号开头（`（一）` / `(1)`）
- 自带编号时直接返回 title，不拼 number
- 追加双重防护：`number` 与 `title` 开头重复时去重

**验证**：
```
章标题重复: 0 ✓
节标题重复: 0 ✓
第一章 编制说明与项目理解   ← 不再双写
```

#### 3.3 设置页桌面项条件渲染

**改动**：`SettingsPage.tsx`
- 引入 `isWebBridge`
- 「更新与系统」组（自动更新渠道 + GPU 加速）Web 版隐藏
- 「关于」页「自动更新」卡片 Web 版隐藏
- 「存储空间」文案改为服务端语义

**验证**：
```
自动更新渠道（应隐藏）: ✓ 已隐藏
GPU 硬件加速（应隐藏）: ✓ 已隐藏
存储空间（应显示）: ✓ 显示
存储文案: 查看服务器上的数据占用，清理临时文件、缓存和日志
```

---

### 第四批：收尾细节（P2）

#### 4.1 登录页 Logo

把 5 个字的「标书工作台」塞进 48px 方块 → 改成单字「标」，字号 22px + 文字阴影。

#### 4.2 产物库行操作

每行 88px 主色「下载」按钮 → 30px 图标按钮（`↓`），hover 才变实心。

**验证**：`iconBtns: 31 | 旧大按钮: 0`

#### 4.3 资信库表单分组

20+ 字段平铺 → 插入 5 个分组标题：

```
工商登记信息
联系方式
经营与资质
规模指标
地址与经营范围
```

顶部加完成度「共 10 项，已填 X 项」。

---

## 二、实施中发现并修复的额外缺陷

### 单参数方法的字段名不匹配（真实 400 错误）

**现象**：每次进入技术方案页，`update-step` 返回 400「步骤不能为空」。

**根因**：bridge 通用逻辑把单参数包成 `{ value: step }`：

```ts
} else if (args.length === 1) {
  body = typeof args[0] === 'object' ? args[0] : { value: args[0] };
}
```

但后端 Bind 的是具名字段 `step`，拿不到值。

**修复**：在 `POSITIONAL_ARGS` 里补 7 条显式映射：

```ts
'technicalPlan.updateStep': ['step'],
'feasibilityReport.updateStep': ['step'],
'technicalPlan.selectBidSection': ['sectionId'],
'technicalPlan.readTenderSourceMarkdown': ['sourceId'],
'technicalPlan.removeTenderDocument': ['sourceId'],
'technicalPlan.saveSectionContent': ['nodeId', 'content'],
```

**教训**：这类问题类型检查发现不了（TS 签名是对的），单测也覆盖不到（要真点界面）。

---

## 三、验收结果

### 自动化检查

```
{"name":"投标文件","steps":6,"titleDup":false,"toolbarPos":"fixed","railCollapsed":true}
{"name":"生成设置","hasSlang":false}
{"name":"知识库","stats":4,"hasHint":true}
{"name":"资信库","groups":["工商登记信息","联系方式","经营与资质","规模指标","地址与经营范围"],"completeness":true}
{"name":"模板","systemCards":true,"presetNames":4}
{"name":"产物库","iconBtns":31,"oldBigBtns":0}
{"name":"设置","hasDesktopOnly":false}

接口异常: 无
```

### 全站回归

```
18 通过 / 0 失败
```

### 构建

```
tsc --noEmit   通过
vite build     ✓ built in 13.50s
```

---

## 四、改动文件清单

### 新增（2）

| 文件 | 行数 | 用途 |
|---|---|---|
| `client/src/shared/ui/ProcessStepBar.tsx` | 约 100 | 步骤条组件 |
| `client/src/styles/component-process-stepbar.css` | 约 190 | 步骤条样式 |

### 修改（13）

| 文件 | 改动 |
|---|---|
| `client/src/features/technical-plan/pages/TechnicalPlanHome.tsx` | 接入步骤条、步骤摘要计算、`has-page-toolbar`、`countOutlineNodes` |
| `client/src/features/technical-plan/pages/OutlineEditPage.tsx` | 侧栏折叠状态与渲染 |
| `client/src/features/technical-plan/pages/GenerationSettingsPage.tsx` | tooltip 加 description |
| `client/src/features/technical-plan/generationSettingsTabs.ts` | 文案专业化 + description 字段 |
| `client/src/features/knowledge-base/pages/KnowledgeBasePage.tsx` | 统计卡 + 引导卡 |
| `client/src/features/duplicate-check/pages/DuplicateCheckPage.tsx` | 成功卡片 |
| `client/src/features/assets/pages/AssetsPage.tsx` | 图标按钮 |
| `client/src/features/credential-library/pages/CredentialLibraryPage.tsx` | 分组 + 完成度 |
| `client/src/features/settings/pages/SettingsPage.tsx` | Web 条件渲染 |
| `client/src/app/web/LoginPage.tsx` | Logo 单字 |
| `client/src/shared/ui/ToastProvider.tsx` | 去重机制 |
| `client/src/shared/utils/outlineNumbering.ts` | 标题去重 |
| `client/src/shared/bridge/web.ts` | 单参数字段映射 |
| `client/src/styles.css` | 导入新样式 |
| `client/src/styles/shared-components.css` | 工具条固定定位 |
| `client/src/styles/feature-technical-plan.css` | 侧栏折叠样式 |
| `client/src/styles/feature-assets.css` | 知识库统计 + 产物库图标 |
| `client/src/styles/feature-duplicate-check.css` | 成功卡片样式 |
| `client/src/styles/feature-credential-library.css` | 分组标题样式 |
| `client/src/styles/web-login.css` | Logo 样式 |

### 数据

| 变更 | 说明 |
|---|---|
| `export_templates` 插入 3 行 | 系统预设模板（`is_system = true`） |

---

## 五、未做的部分（有意为之）

| 方案项 | 状态 | 原因 |
|---|---|---|
| 配置页签改竖排 | 未做 | 现为横排 + 摘要副标题，改造需重写 `GenerationSettingsPage` 的标签容器与键盘导航，收益不如风险大 |
| 知识库改表格视图 | 部分做 | 加了统计卡 + 引导卡（解决空荡感）；改表格需重构 1886 行页面的列表、拖拽、分页，风险高 |
| 空态统一组件 | 未做 | 各页面已有自己的空态实现，强行统一会引入大量回归 |

这三项如果要做，建议单独排期，并配套完整的回归测试。

---

## 六、视觉对比

| 页面 | 改前 | 改后 |
|---|---|---|
| 投标文件 | 只有 `STEP 04` 小字，不知总共几步 | 顶部 6 步进度条，每步带摘要 |
| 目录页 | 左栏 300px 空框，目录树被挤 | 左栏 48px 细条，目录树 676px |
| 生成设置 | 「写嘛 / 长嘛样」 | 「生成范围 / 导出模板」 |
| 知识库 | 一条文档占满屏 | 4 个统计卡 + 引导卡 |
| 模板 | 两个大白块 | 3 张系统预设卡片 |
| 查重无重复 | 大图标 + 两行字 | 成功卡片 + 下一步引导 |
| 产物库 | 每行大蓝按钮 | 图标按钮，文件名成焦点 |
| 登录页 | Logo 文字挤压 | 单字「标」 |
| 设置 | 显示 AtomGit / GPU 加速 | Web 版隐藏 |
