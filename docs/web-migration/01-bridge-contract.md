# bridge → HTTP 接口契约映射

> 来源：`client/electron/preload.cjs` 逐行解析（256 个方法 / 35 个命名空间）
> 目标：Go 后端 REST + SSE 接口
> 用途：前后端并行开发的唯一契约依据

---

## 一、映射规则

### 1.1 命名转换

```
window.yibiao.<ns>.<method>(args)   →   POST /api/<ns-kebab>/<method-kebab>
```

| bridge | HTTP |
|---|---|
| `technicalPlan.loadState()` | `POST /api/technical-plan/load-state` |
| `knowledgeBase.uploadDocuments(files)` | `POST /api/knowledge-base/upload-documents` |
| `tasks.startContentGeneration(opts)` | `POST /api/tasks/start-content-generation` |

**为什么全用 POST**：现有 256 个方法语义混杂（读、写、动作），强行拆 REST 动词会让 `web.ts` 里出现 256 条 if-else 映射表，失去「统一实现」的意义。全 POST + 资源路径，`web.ts` 只需一行 `fetch`。

**代价**：不满足 REST 纯正主义。收益：适配层 30 行搞定，且与桌面版调用语义 100% 等价。这个取舍值得。

**例外**：文件下载、SSE、对象存储走标准 GET（见 §4）。

### 1.2 请求/响应信封

```jsonc
// 请求
POST /api/technical-plan/save-outline
Content-Type: application/json
Authorization: Bearer <jwt>
X-Tenant-Id: <tenant>        // 单租户时可省，中间件填默认
X-Project-Id: <project>      // 项目作用域接口必带

{ "reason": "edit", "outlineData": {...} }

// 成功响应（200）
{ "ok": true, "data": <原 bridge 返回值> }

// 业务失败（200，与桌面版行为一致）
{ "ok": false, "error": { "code": "TASK_RUNNING", "message": "正文任务运行中", "detail": {...} } }
```

**关键设计：业务失败也返回 HTTP 200。** 桌面版 bridge 的失败通过 Promise reject 表达，前端已有大量 `try/catch` 依赖错误对象结构（`error.code`、`error.message`）。若用 HTTP 4xx，`fetch` 不会 reject，前端全部要改。

只有**认证失败（401）、权限不足（403）、服务端崩溃（500）、请求体非法（400）** 用非 200 状态码——这些情况下前端本来也没有业务逻辑可走。

`web.ts` 的封装：

```ts
async function invoke(ns: string, method: string, args: unknown[]) {
  const res = await fetch(`/api/${kebab(ns)}/${kebab(method)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(args.length === 1 ? args[0] : args),
  });
  if (res.status === 401) { redirectToLogin(); throw new Error('登录已过期'); }
  const body = await res.json();
  if (!body.ok) throw Object.assign(new Error(body.error.message), body.error);
  return body.data;
}
```

### 1.3 事件映射

30 个 `onXxx(callback) => unsubscribe` 映射为 SSE：

```
GET /api/events?topics=task,agent,knowledge-base
Accept: text/event-stream

event: task
data: {"taskId":"...","status":"running","progress":42,...}

event: agent
data: {"sessionId":"...","status":"thinking"}
```

`web.ts` 里维护**一个**共享 EventSource，按 topic 分发给已注册的回调：

```ts
const listeners = new Map<string, Set<(d: any) => void>>();
let es: EventSource | null = null;

function subscribe(topic: string, cb: (d: any) => void) {
  if (!es) connect();
  (listeners.get(topic) ?? listeners.set(topic, new Set()).get(topic)!).add(cb);
  return () => { listeners.get(topic)?.delete(cb); };
}
```

**优势**：桌面版 21 个事件各自一个 `ipcRenderer.on`，Web 版只有 1 条 SSE 连接。更省资源，且断线自动重连由 EventSource 内置。

**注意**：SSE 需要 `Last-Event-ID` 支持做断线续传。每个事件带递增 id，服务端保留最近 N 条（或按 task 存 DB 后重放）。

---

## 二、完整方法映射表

标注说明：
- **P0** = 阶段 1 必须（技术方案主链路）
- **P1** = 阶段 2-3
- **P2** = 阶段 4+
- **砍** = 建议不做（见总体方案 §9）

### 2.1 ROOT（5 个）

> ⚠️ **2026-11-13 实测修正**：ROOT 层不止方法，还混了 **6 个事件订阅**。
> 完整 ROOT 成员共 11 个，最初解析 preload 时被漏掉了。见下表。

| bridge | 类型 | HTTP 路径 | 优先级 | 备注 |
|---|---|---|---|---|
| `appName` | 常量 | — | — | 前端本地常量 |
| `platform` | 常量 | — | — | 返回 `'web'` |
| `getVersion` | 方法 | `POST /api/root/get-version` | P0 | 返回服务版本，**必须 Promise** |
| `openExternal` | 方法 | `POST /api/root/open-external` | P0 | Web 版前端直接 `window.open`，可 no-op |
| `checkUpdate` | 方法 | — | 砍 | 无客户端更新 |
| `getUpdateDownloadUrl` | 方法 | — | 砍 | 同上 |
| `quitAndInstall` | 方法 | — | 砍 | 同上 |
| `getGpuHardwareAccelerationStatus` | 方法 | — | 砍 | 桌面 GPU 概念 |
| `saveGpuHardwareAccelerationPreference` | 方法 | — | 砍 | 同上 |
| `startGpuHardwareAccelerationTrial` | 方法 | — | 砍 | 同上 |
| `relaunchWithGpuHardwareAccelerationDisabled` | 方法 | — | 砍 | 同上 |
| `onUpdateProgress` | **事件** | — | 砍 | 无更新 |
| `onUpdateDownloaded` | **事件** | — | 砍 | 同上 |
| `onUpdateError` | **事件** | — | 砍 | 同上 |
| `onPluginUpdatesAvailable` | **事件** | — | 砍 | 插件砍掉 |

**前端处理**：`web.ts` 里这 10 个砍掉的返回固定值，让前端静默：

```ts
checkUpdate: () => Promise.resolve({ hasUpdate: false, latestVersion: '', currentVersion: VERSION }),
getUpdateDownloadUrl: () => Promise.resolve(''),
quitAndInstall: () => Promise.resolve({ success: false, message: 'Web 版无需安装' }),
getGpuHardwareAccelerationStatus: () => Promise.resolve({ enabled: true, configured: true, restartRequired: false }),
saveGpuHardwareAccelerationPreference: () => Promise.resolve({ success: true, enabled: true, configured: true, restartRequired: false }),
startGpuHardwareAccelerationTrial: () => Promise.resolve({ success: true }),
relaunchWithGpuHardwareAccelerationDisabled: () => Promise.resolve({ success: true }),
onUpdateProgress: () => () => {},
onUpdateDownloaded: () => () => {},
onUpdateError: () => () => {},
onPluginUpdatesAvailable: () => () => {},
```

**教训**：不要假设 bridge 是「纯命名空间 + 方法」的规整结构。它有一层混在 root 上的方法和事件，容易漏。迁移时**按 `getPathForFile` 的实际调用点逐个验证**，不要只按目录结构推断。

### 2.1.1 返回值契约（实测得出，必须遵守）

用 mock bridge 实测暴露的三条硬约束：

| 约束 | 说明 | 违反后果 |
|---|---|---|
| **所有方法返回 Promise** | 前端对 ROOT 方法也调 `.catch()` | `getVersion(...).catch is not a function` |
| **「可能没有」返回 `null`，不是 `{}`** | 如 `agent.getPendingQuestion()` | `question?.options.find()` 崩溃 |
| **集合返回 `[]`，不是 `null`** | 如 `tasks.getActiveTasks()` | 前端 `.map` / `.length` 崩溃 |

**Go 侧实现要求**：严格按桌面版的真实返回结构，不能图省事返回空对象。每条接口的返回形态以 `src/shared/types/ipc.ts`（1,015 行）为准。

`client/src/shared/bridge/mock.ts` 里的 `EXPLICIT` 表已经记录了 30 个关键接口的精确返回形态，可以直接当实现清单用。

### 2.2 requiredOnlineServices（11 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getStatus` | `POST /api/online-services/get-status` | P0 | 返回各在线服务可用性 |
| `getLatestVersion` | — | 砍 | 无客户端更新 |
| `getUpdateDownloadUrl` | — | 砍 | 同上 |
| `checkUpdate` | — | 砍 | 同上 |
| `startUpdate` | — | 砍 | 同上 |
| `quitAndInstall` | — | 砍 | 同上 |
| `onUpdateProgress` | — | 砍 | 同上 |
| `onUpdateDownloaded` | — | 砍 | 同上 |
| `onUpdateError` | — | 砍 | 同上 |
| `onPluginUpdatesAvailable` | — | 砍 | 插件砍掉 |
| `openExternal` | `POST /api/online-services/open-external` | P0 | 前端 `window.open` 即可，此接口可 no-op |

### 2.3 agent（10 个）★关键

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `run` | `POST /api/agent/run` | P0 | 启动 Agent 任务（内部转 sidecar） |
| `restart` | `POST /api/agent/restart` | P0 | |
| `getStatus` | `POST /api/agent/get-status` | P0 | |
| `answerQuestion` | `POST /api/agent/answer-question` | P0 | Agent 提问应答 |
| `getPendingQuestion` | `POST /api/agent/get-pending-question` | P0 | |
| `suppressQuestionAutoAnswer` | `POST /api/agent/suppress-question-auto-answer` | P1 | |
| `selfCheck` | `POST /api/agent/self-check` | P2 | 诊断工具，可后置 |
| `exportSelfCheckReport` | `POST /api/agent/export-self-check-report` | P2 | |
| `onStatus` | `GET /api/events` topic=`agent.status` | P0 | |
| `onQuestion` | `GET /api/events` topic=`agent.question` | P0 | |

### 2.4 ai（5 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `chat` | `POST /api/ai/chat` | P0 | 流式走 SSE 响应体 |
| `requestJson` | `POST /api/ai/request-json` | P0 | |
| `testImageModel` | `POST /api/ai/test-image-model` | P1 | |
| `onHttpError` | `GET /api/events` topic=`ai.http-error` | P0 | |
| `onBalanceInsufficient` | `GET /api/events` topic=`ai.balance` | P1 | 改积分不足 |

**`ai.chat` 的流式处理**：不能走通用信封，要单独实现。

```
POST /api/ai/chat
→ 200 text/event-stream
   data: {"delta":"..."}
   data: {"usage":{"prompt_tokens":100,"completion_tokens":50}}
   data: {"done":true}
```

`web.ts` 用 `fetch` + `ReadableStream` 手工解析（EventSource 不支持 POST）。

### 2.5 autoConfirmation（3 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getState` | `POST /api/auto-confirmation/get-state` | P1 | |
| `setEnabled` | `POST /api/auto-confirmation/set-enabled` | P1 | 用户级设置 |
| `onChanged` | `GET /api/events` topic=`auto-confirmation` | P1 | |

**多用户注意**：桌面版这是全局开关。Web 版必须**按用户 + 按任务**隔离，不能一个用户开启影响别人。

### 2.6 blindCheck（16 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `loadState` | `POST /api/blind-check/load-state` | P2 |
| `importFile` | `POST /api/blind-check/import-file` | P2 |
| `removeFile` | `POST /api/blind-check/remove-file` | P2 |
| `clear` | `POST /api/blind-check/clear` | P2 |
| `getInputSignature` | `POST /api/blind-check/get-input-signature` | P2 |
| `getTenderSources` | `POST /api/blind-check/get-tender-sources` | P2 |
| `getCredentialWords` | `POST /api/blind-check/get-credential-words` | P2 |
| `setUseCredentialWords` | `POST /api/blind-check/set-use-credential-words` | P2 |
| `listPresets` | `POST /api/blind-check/list-presets` | P2 |
| `createPreset` | `POST /api/blind-check/create-preset` | P2 |
| `updatePreset` | `POST /api/blind-check/update-preset` | P2 |
| `deletePreset` | `POST /api/blind-check/delete-preset` | P2 |
| `selectPreset` | `POST /api/blind-check/select-preset` | P2 |
| `saveFixRules` | `POST /api/blind-check/save-fix-rules` | P2 |
| `saveUiState` | `POST /api/blind-check/save-ui-state` | P2 |
| `saveFixedDocument` | 见下 | P2 |

**`saveFixedDocument` 特殊**：桌面版弹保存对话框写本地文件。Web 版改为 `GET /api/blind-check/fixed-document?id=xxx` 下载。

### 2.7 config（5 个）★关键

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `load` | `POST /api/config/load` | P0 | 返回配置（**必须脱敏 API Key**） |
| `save` | `POST /api/config/save` | P0 | |
| `listModels` | `POST /api/config/list-models` | P0 | |
| `getModelInfo` | `POST /api/config/get-model-info` | P0 | |
| `openConfigFolder` | — | 砍 | 无本地文件夹概念 |

**配置分层重构**（重要）：

桌面版 `user_config.json` 混了三种东西，Web 版必须拆开：

| 原内容 | Web 归属 | 存储 |
|---|---|---|
| 模型 API Key、Base URL | **服务端密钥** | PG `tenant_secrets`，加密 |
| 模型选择、解析器选择、并发数 | **用户偏好** | PG `user_settings` |
| 导出样式、字体、页边距 | **租户/项目配置** | PG `export_templates` |
| 开发者开关 | 删除 | — |

`config.load` 的返回必须**不含明文 Key**，只返回 `has_api_key: true` 之类。这是与桌面版最大的语义差异，前端设置页要相应调整。

### 2.8 credentialLibrary（13 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `load` | `POST /api/credential-library/load` | P1 |
| `saveProfile` | `POST /api/credential-library/save-profile` | P1 |
| `saveCertificate` / `deleteCertificate` | `POST /api/credential-library/save-certificate` / `delete-certificate` | P1 |
| `saveEmployee` / `deleteEmployee` | 同上模式 | P1 |
| `saveProject` / `deleteProject` | 同上模式 | P1 |
| `saveOtherMaterial` / `deleteOtherMaterial` | 同上模式 | P1 |
| `deleteImage` | `POST /api/credential-library/delete-image` | P1 |
| `addProfileImages` | `POST /api/credential-library/add-profile-images` | P1 | **改 multipart 上传** |
| `importTestData` | — | 砍 | 测试数据 |

**`addProfileImages` 改造**：桌面版传本地路径数组。Web 版：

```
POST /api/credential-library/add-profile-images
Content-Type: multipart/form-data
X-Field-Key: <fieldKey>
files[]: <binary>
```

前端需改 `CredentialLibraryPage.tsx` 的 247 行、393 行（这两处本来就对 `File` 操作，改动小）。

### 2.9 database（2 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getStatus` | `POST /api/database/get-status` | P0 | 返回迁移状态 |
| `onStatus` | `GET /api/events` topic=`database.status` | P0 | Web 端意义小，可返回 fixed ready |

**Web 版简化**：数据库迁移在服务启动时完成，前端不需要 `WorkspaceDatabaseGate` 的等待逻辑。但为兼容前端结构，`getStatus` 直接返回 `{ ready: true, version: N }`。

### 2.10 dependencies（4 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getStatus` | `POST /api/dependencies/get-status` | P2 | |
| `install` | `POST /api/dependencies/install` | P2 | 服务端预装，前端只查状态 |
| `uninstall` | — | 砍 | 服务端管理 |
| `onProgress` | — | 砍 | 服务端预装无进度 |

**建议**：依赖管理在 Web 版变成**运维概念**，不是用户功能。OCR 组件、LibreOffice 打包进镜像，前端不展示这个页面。

### 2.11 developer*（13 个）

| 范围 | 建议 |
|---|---|
| `developerAgentMonitor.*`（5） | 砍，或改成管理员专属只读页 |
| `developerExpansionReplaceTest.run`（1） | 砍 |
| `developerLayoutFigure.*`（3） | 砍 |
| `developerTokenStats.*`（4） | 改造为「用量统计」页（多租户必需） |

`developerTokenStats` 的 4 个方法保留，重命名：

| bridge | HTTP | 说明 |
|---|---|---|
| `get` | `POST /api/usage/get` | 用户自己的 token 用量 |
| `reset` | `POST /api/usage/reset` | 管理员 |
| `openWindow` | — | 前端路由跳转 |
| `onChanged` | `GET /api/events` topic=`usage.changed` | |

### 2.12 donation（6 个）→ 砍

全部砍掉。公网运营时用会员/积分体系替代。

### 2.13 dualLayerPdf（14 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `addFiles` | `POST /api/dual-layer-pdf/add-files` | P2 | **改 multipart** |
| `removeItem` | `POST /api/dual-layer-pdf/remove-item` | P2 | |
| `clear` | `POST /api/dual-layer-pdf/clear` | P2 | |
| `getState` | `POST /api/dual-layer-pdf/get-state` | P2 | |
| `getEnvironment` | `POST /api/dual-layer-pdf/get-environment` | P2 | 返回服务端可用转换器 |
| `setOutputs` | `POST /api/dual-layer-pdf/set-outputs` | P2 | |
| `resetOutputDir` | — | 砍 | 无本地目录 |
| `selectFiles` | — | 换实现 | 前端 `<input multiple>` |
| `selectOutputDir` | — | 砍 | |
| `start` | `POST /api/dual-layer-pdf/start` | P2 | |
| `cancel` | `POST /api/dual-layer-pdf/cancel` | P2 | |
| `retryItem` | `POST /api/dual-layer-pdf/retry-item` | P2 | |
| `showInFolder` | — | 改下载 | |
| `onEvent` | `GET /api/events` topic=`dual-layer-pdf` | P2 | |

**这个功能整体建议延后**：Linux 下 COM 路径失效，只能 LibreOffice，产出质量存疑。而且要搬 `pdfWriter.cjs`（手写 PDF 双层文字层）+ OCR 组件，成本高、收益低。

### 2.14 duplicateCheck（6 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `loadState` | `POST /api/duplicate-check/load-state` | P2 |
| `saveFiles` | `POST /api/duplicate-check/save-files` | P2 |
| `updateState` | `POST /api/duplicate-check/update-state` | P2 |
| `saveUiState` | `POST /api/duplicate-check/save-ui-state` | P2 |
| `clear` | `POST /api/duplicate-check/clear` | P2 |
| `exportExcel` | `GET /api/duplicate-check/export-excel` | P2 |

### 2.15 export（3 个）★关键

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `exportWord` | `POST /api/export/word` | P0 | 返回 `{ jobId }`，完成后 SSE 通知 |
| `openFile` | `GET /api/export/download/:jobId` | P0 | 浏览器下载 |
| `onWordExportProgress` | `GET /api/events` topic=`export.progress` | P0 | |

**导出流程改造**（桌面版是同步返回路径）：

```
桌面版：exportWord(payload) → { success, path }
Web 版：exportWord(payload) → { jobId }
        ↓ SSE export.progress
        { jobId, phase:'building', percent:40 }
        ↓
        { jobId, phase:'success', downloadUrl:'/api/export/download/xxx' }
前端：window.location = downloadUrl
```

**改动点**：`src/features/technical-plan/` 里调用导出的地方（约 3-5 处）要改成接收 downloadUrl。这个改动不可避免。

### 2.16 feasibilityReport（13 个）

| 优先级 | 说明 |
|---|---|
| **P2 / 延后** | 独立业务线，3 张表 + 508 行任务，与标书主线关系弱 |

全部按命名转换映射，`importSourceDocuments` 改 multipart，`readSourceMarkdown` 改 GET 下载。

### 2.17 file（4 个）★关键

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getPathForFile` | — | **特殊处理** | 见下 |
| `selectBlindCheckFile` | — | 砍 | 前端 `<input type=file>` |
| `selectBlindCheckTenderFile` | — | 砍 | 同上 |
| `selectDuplicateCheckFiles` | — | 砍 | 同上 |

**`getPathForFile` 的处理方案**（重要，涉及 12 处调用）：

方案 A（推荐）：**虚拟路径 + 自动上传**

```ts
// web.ts
getPathForFile(file: File): string {
  const id = crypto.randomUUID();
  uploadQueue.enqueue(id, file);        // 后台上传
  return `upload://${id}`;              // 返回伪路径
}
```

后端收到以 `upload://` 开头的路径时，查上传表拿到实际文件。点击「导入」时如果上传未完成，后端返回 `UPLOAD_PENDING`，前端提示等待。

**优点**：前端 12 处调用零改动，交互与桌面版一致（拖进来就能用）。

方案 B：改 12 处调用点，改成显式 `await upload(file)`。
**缺点**：要改 12 个文件，且要处理 UI 加载态。

**选 A。** 上传队列用 `Promise` 数组管理，`web.ts` 内部维护 `Map<id, Promise<fileId>>`，`importFile` 等接口调用时后端自动等待。

### 2.18 knowledgeBase（15 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `list` | `POST /api/knowledge-base/list` | P1 |
| `createFolder` / `deleteFolder` / `renameFolder` / `reorderFolder` | 同模式 | P1 |
| `uploadDocuments` | `POST /api/knowledge-base/upload-documents` | P1（multipart） |
| `deleteDocument` / `moveDocument` / `retryDocument` | 同模式 | P1 |
| `readMarkdown` | `GET /api/knowledge-base/read-markdown?id=` | P1 |
| `readItems` / `readAnalysis` | `POST .../read-items` / `.../read-analysis` | P1 |
| `search` | `POST /api/knowledge-base/search` | P1 |
| `startMatching` | `POST /api/knowledge-base/start-matching` | P1 |
| `onEvent` | SSE topic=`knowledge-base` | P1 |

### 2.19 license（4 个）→ 改造

| bridge | Web 替代 |
|---|---|
| `getStatus` | `POST /api/billing/get-status` → 返回会员等级 + 积分 |
| `refresh` | `POST /api/billing/refresh` |
| `activateOfflineCode` | `POST /api/billing/redeem-code` → 兑换码 |
| `importOfflineFile` | 砍 |

### 2.20 notice（3 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `syncCurrent` | `POST /api/notice/sync-current` | P0 | 公告 |
| `prepareMedia` / `releaseMedia` | — | 砍 | 本地缓存概念，Web 改预签名 URL |

公告图片直接返回 CDN/对象存储 URL，不做 prepare/release。

### 2.21 officialAccount（18 个）→ 大部分砍

| 保留 | 说明 |
|---|---|
| 无 | 建议全部砍掉，公网运营时重做 |

如需保留发票信息，只留 `getInvoiceInfo` / `saveInvoiceInfo` 两个。

### 2.22 plugins（13 个）→ 全砍

理由见总体方案 §9：插件是本地代码执行，Web 下是安全灾难，且依赖外部市场（`analytics.agnet.top`）。

前端 `src/features/plugins/` 整个目录删除。

### 2.23 rejectionCheck（8 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `loadState` / `saveUiState` / `updateState` | 同模式 | P2 |
| `importDocument` | `POST /api/rejection-check/import-document`（multipart） | P2 |
| `importTenderFromTechnicalPlan` | `POST .../import-tender-from-technical-plan` | P2 |
| `removeDocument` / `clear` | 同模式 | P2 |
| `exportExcel` | `GET /api/rejection-check/export-excel` | P2 |

### 2.24 storage（2 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `getUsage` | `POST /api/storage/get-usage` | P1 | 租户配额用量 |
| `clean` | `POST /api/storage/clean` | P1 | 管理员清理 |

### 2.25 systemFonts（1 个）★关键

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `list` | `POST /api/system-fonts/list` | P0 | **返回服务端可用中文字体** |

**重要变化**：桌面版返回本机字体。Web 版必须返回**服务器容器内装的字体**，且导出用的字体必须与之一致。

前端 `src/features/export-format/` 里字体选择下拉要能反映真实可用字体。建议：

- 镜像里预装一套中文字体（思源宋体/黑体、方正系列如有授权）
- `list` 从 `fontconfig` 读（`fc-list :lang=zh`）
- 前端若选了服务器没有的字体，导出时降级并 warning

### 2.26 tasks（25 个）★核心

| bridge | HTTP | 优先级 |
|---|---|---|
| `getActiveTasks` | `POST /api/tasks/get-active` | P0 |
| `onTaskEvent` | SSE topic=`task` | P0 |
| `pauseContentGeneration` | `POST /api/tasks/pause-content-generation` | P0 |
| `confirmOutlineSelection` | `POST /api/tasks/confirm-outline-selection` | P0 |
| `confirmBusinessFillReview` | `POST /api/tasks/confirm-business-fill-review` | P1 |
| `suppressOutlineSelectionAutoConfirmation` | `POST .../suppress-...` | P1 |
| `suppressBusinessFillReviewAutoConfirmation` | 同上 | P1 |
| `startBidAnalysis` | `POST /api/tasks/start-bid-analysis` | P0 |
| `startBidSectionExtraction` | `POST /api/tasks/start-bid-section-extraction` | P0 |
| `startOutlineGeneration` | `POST /api/tasks/start-outline-generation` | P0 |
| `startGlobalFactsGeneration` | `POST /api/tasks/start-global-facts-generation` | P0 |
| `startContentGeneration` | `POST /api/tasks/start-content-generation` | P0 |
| `startRulePageCompile` | `POST /api/tasks/start-rule-page-compile` | P1 |
| `startBidTemplateExtraction` | `POST /api/tasks/start-bid-template-extraction` | P1 |
| `startDuplicateAnalysis` | `POST /api/tasks/start-duplicate-analysis` | P2 |
| `startRejectionCheck` | `POST /api/tasks/start-rejection-check` | P2 |
| `startRejectionItemsExtraction` | `POST /api/tasks/start-rejection-items-extraction` | P2 |
| `startBlindCheck` | `POST /api/tasks/start-blind-check` | P2 |
| `startBlindCheckFix` | `POST /api/tasks/start-blind-check-fix` | P2 |
| `startBlindCheckPresetExtraction` | `POST /api/tasks/start-blind-check-preset-extraction` | P2 |
| `startFeasibility*`（4 个） | 同模式 | P2 |
| `pauseFeasibilityContent` | `POST /api/tasks/pause-feasibility-content` | P2 |

**任务事件载荷**（SSE `event: task`）：

```jsonc
{
  "taskId": "uuid",
  "type": "content-generation",
  "status": "running",       // pending|running|pausing|paused|success|error|canceled
  "stage": "content",        // 阶段标识
  "progress": 42,
  "message": "正在生成第 3/12 节",
  "patch": { ... },          // checkpoint 的业务数据局部 patch
  "updatedAt": "2026-11-13T14:00:00Z"
}
```

**多用户隔离**：SSE 连接必须按 `user_id + tenant_id` 过滤，**不能广播**。桌面版是单进程内存事件，Web 版要在事件总线里带归属信息。

### 2.27 technicalPlan（26 个）★最核心

| bridge | HTTP | 优先级 |
|---|---|---|
| `loadState` | `POST /api/technical-plan/load-state` | P0 |
| `updateStep` | `POST /api/technical-plan/update-step` | P0 |
| `importTenderDocument` | `POST /api/technical-plan/import-tender-document`（multipart） | P0 |
| `removeTenderDocument` | `POST .../remove-tender-document` | P0 |
| `readTenderMarkdown` | `GET /api/technical-plan/read-tender-markdown` | P0 |
| `readTenderSourceMarkdown` | `GET /api/technical-plan/read-tender-source-markdown?id=` | P0 |
| `checkBidSections` | `POST /api/technical-plan/check-bid-sections` | P0 |
| `selectBidSection` | `POST /api/technical-plan/select-bid-section` | P0 |
| `importOriginalPlanDocument` | `POST .../import-original-plan-document`（multipart） | P0 |
| `removeOriginalPlanDocument` | `POST .../remove-original-plan-document` | P0 |
| `loadGenerationConfig` | `POST .../load-generation-config` | P0 |
| `saveGenerationConfig` | `POST .../save-generation-config` | P0 |
| `saveBidAnalysisConfig` | `POST .../save-bid-analysis-config` | P0 |
| `saveContentGenerationOptions` | `POST .../save-content-generation-options` | P0 |
| `saveOutline` | `POST .../save-outline` | P0 |
| `saveOutlineSelection` | `POST .../save-outline-selection` | P0 |
| `saveGlobalFacts` | `POST .../save-global-facts` | P0 |
| `resetContentGeneration` | `POST .../reset-content-generation` | P0 |
| `readContentWord` | `GET /api/technical-plan/read-content-word?sectionId=` | P0 |
| `previewContentWord` | `GET /api/technical-plan/preview-content-word?sectionId=` | P0 |
| `clear` | `POST /api/technical-plan/clear` | P0 |
| `inspectBidTemplate` | `POST .../inspect-bid-template` | P1 |
| `openBidTemplate` | `GET /api/technical-plan/download-bid-template` | P1 |
| `previewBusinessChapterWord` | `GET .../preview-business-chapter-word?sectionId=` | P1 |
| `getBusinessFillReview` | `POST .../get-business-fill-review` | P1 |
| `saveBusinessFillReview` | `POST .../save-business-fill-review` | P1 |

**注意 `saveOutline` 的 `reason` 协议**：`replace` / `edit` / `delete` / `add-root` / `add-child` / `sort`，这是持久化语义，Go 侧必须逐字实现（见 `开发说明.md`）。这是最容易搬错的地方。

### 2.28 templates（7 个）

| bridge | HTTP | 优先级 |
|---|---|---|
| `list` / `get` | `POST /api/templates/list` / `get` | P1 |
| `create` / `update` / `delete` / `duplicate` | 同模式 | P1 |
| `renderPreview` | `POST /api/templates/render-preview` → 返回 docx jobId | P1 |

### 2.29 ui（1 个）

| bridge | HTTP | 优先级 | 备注 |
|---|---|---|---|
| `setCurrentView` | — | 砍 | Web 版用真实 URL 路由，不需要 |

---

## 三、统计汇总

| 类别 | 数量 |
|---|---|
| bridge 方法总数 | **256** |
| 保留为 HTTP 接口 | **约 168** |
| 改成 GET 下载 | 约 12 |
| 前端本地实现（无接口） | 约 8 |
| 砍掉 | **约 68** |
| 事件订阅 | 30 → **合并为 1 条 SSE，按 topic 分发** |

**砍掉的 68 个分布**：GPU 4、更新 9、插件 13、捐赠 6、官方账号 18、开发者 9、依赖管理 4、杂项 5。

---

## 四、SSE 事件清单

单一端点 `GET /api/events?topics=...`，按 topic 分发：

| topic | 来源 bridge | 载荷要点 |
|---|---|---|
| `task` | `tasks.onTaskEvent` | taskId, type, status, stage, progress, patch |
| `agent.status` | `agent.onStatus` | sessionId, status, stage, turn |
| `agent.question` | `agent.onQuestion` | sessionId, question, options |
| `agent.monitor` | `developerAgentMonitor.onEvent` | （管理员） |
| `ai.http-error` | `ai.onHttpError` | code, message, retryable |
| `ai.balance` | `ai.onBalanceInsufficient` | 改为积分不足 |
| `auto-confirmation` | `autoConfirmation.onChanged` | sessionId, enabled |
| `knowledge-base` | `knowledgeBase.onEvent` | documentId, phase, progress |
| `export.progress` | `export.onWordExportProgress` | jobId, phase, percent, warnings |
| `dual-layer-pdf` | `dualLayerPdf.onEvent` | state |
| `database.status` | `database.onStatus` | ready, version |
| `usage.changed` | `developerTokenStats.onChanged` | balance, used |
| `dependencies.progress` | `dependencies.onProgress` | （砍） |
| `plugins.updates` | `onPluginUpdatesAvailable` | （砍） |
| `notice` | — | 公告更新 |
| `donation.*` | （砍） | |
| `official-account.*` | （砍） | |

**保留 12 个 topic**。

**SSE 实现要点**：

```go
func (h *EventHandler) Stream(c echo.Context) error {
    topics := parseTopics(c.QueryParam("topics"))
    ch := h.bus.Subscribe(c.Request().Context(), c.Get("user_id").(string), topics)
    defer h.bus.Unsubscribe(ch)

    c.Response().Header().Set("Content-Type", "text/event-stream")
    c.Response().Header().Set("Cache-Control", "no-cache")
    c.Response().Header().Set("X-Accel-Buffering", "no")  // nginx 不缓冲
    c.Response().WriteHeader(200)

    ticker := time.NewTicker(15 * time.Second)  // 心跳
    defer ticker.Stop()

    for {
        select {
        case <-c.Request().Context().Done():
            return nil
        case ev := <-ch:
            fmt.Fprintf(c.Response(), "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Topic, ev.Data)
            c.Response().Flush()
        case <-ticker.C:
            fmt.Fprint(c.Response(), ": ping\n\n")
            c.Response().Flush()
        }
    }
}
```

---

## 五、前端适配层实现

`web/src/shared/bridge/web.ts` 核心（约 200 行）：

```ts
import type { YibiaoBridge } from './types';

const listeners = new Map<string, Set<(d: any) => void>>();
let es: EventSource | null = null;
let seq = 0;

function connect() {
  es = new EventSource(`/api/events?topics=${[...listeners.keys()].join(',')}`);
  es.onmessage = (e) => { /* 按 lastEventId 去重 */ };
}

function subscribe(topic: string, cb: (d: any) => void): () => void {
  if (!es) connect();
  if (!listeners.has(topic)) {
    listeners.set(topic, new Set());
    if (es.readyState === EventSource.OPEN) reconnect();  // 新 topic 要重订阅
  }
  listeners.get(topic)!.add(cb);
  return () => listeners.get(topic)?.delete(cb);
}

async function invoke(ns: string, method: string, args: unknown[]) {
  // 见 §1.2
}

// 用 Proxy 自动生成 256 个方法，不用手写
function buildNamespace(ns: string): any {
  return new Proxy({}, {
    get(_, method: string) {
      if (method.startsWith('on') && method[1] === method[1].toUpperCase()) {
        const topic = topicFor(ns, method);
        return (cb: (d: any) => void) => subscribe(topic, cb);
      }
      return (...args: unknown[]) => invoke(ns, method, args);
    },
  });
}

export const bridge = new Proxy({} as YibiaoBridge, {
  get(_, ns: string) {
    if (ns === 'appName') return '易标投标工具箱';
    if (ns === 'platform') return 'web';
    return buildNamespace(ns);
  },
});
```

**Proxy 的好处**：256 个方法不用手写映射，签名由 `types.ts`（复用桌面版 `ipc.ts` 的 1,015 行）静态保证。新增接口零成本。

**`topicFor` 映射表**：把 30 个 `onXxx` 映射到 12 个 topic，一张 30 行的常量表。

---

## 六、需要前端改动的地方（完整清单）

| 文件 | 改动 | 工作量 |
|---|---|---|
| 60 个文件里 `window.yibiao` | 改成 `import { bridge as yibiao }` | sed + 人工核对，1 天 |
| `main.tsx` | 去掉 Electron 判断，直接挂载 | 0.5 天 |
| `src/app/AppRouter.tsx` | 改 URL 路由 | 2 天 |
| `src/components/Sidebar.tsx` | 图标映射同步（用 `<a href>`） | 0.5 天 |
| `ContentWordPreview.tsx` | 文件来源改 URL | 0.5 天 |
| 6 处 `selectXxxFiles` | 改 `<input type=file>` | 1 天 |
| `ExportFormatPage.tsx` | 字体列表改服务端 | 1 天 |
| 设置页 | 拆分配置（Key 不返回明文） | 2 天 |
| 导出流程 3-5 处 | 改 downloadUrl | 1 天 |
| 删除 `features/plugins/`、`features/developer/` | 删目录 + 路由 | 0.5 天 |
| GPU 设置区块 | 隐藏 | 0.2 天 |
| **合计** | | **约 10 人日** |

对比一下：如果不做 bridge 适配层，而是像 jdcome 那样重写前端页面，仅技术方案模块就有 250k+ 行（含 CSS 100k），工作量大两个数量级。

---

## 七、Go 侧路由分组

```go
// internal/http/router.go
api := e.Group("/api", authMiddleware, tenantMiddleware)

// P0 核心
api.POST("/config/:method", h.Config.Handle)             // 5 个方法
api.POST("/technical-plan/:method", h.TechPlan.Handle)   // 26 个
api.POST("/tasks/:method", h.Task.Handle)                // 25 个
api.POST("/agent/:method", h.Agent.Handle)               // 10 个
api.POST("/ai/:method", h.AI.Handle)                     // 5 个
api.POST("/online-services/:method", h.Online.Handle)    // 2 个
api.POST("/notice/:method", h.Notice.Handle)             // 1 个
api.POST("/database/:method", h.DB.Handle)               // 1 个
api.POST("/root/:method", h.Root.Handle)                 // 1 个

// P1
api.POST("/knowledge-base/:method", h.KB.Handle)
api.POST("/credential-library/:method", h.Cred.Handle)
api.POST("/templates/:method", h.Tpl.Handle)
api.POST("/storage/:method", h.Storage.Handle)
api.POST("/usage/:method", h.Usage.Handle)
api.POST("/system-fonts/:method", h.Font.Handle)
api.POST("/auto-confirmation/:method", h.AutoConf.Handle)
api.POST("/billing/:method", h.Billing.Handle)           // license 替代

// P2
api.POST("/duplicate-check/:method", h.Dup.Handle)
api.POST("/rejection-check/:method", h.Rej.Handle)
api.POST("/blind-check/:method", h.Blind.Handle)
api.POST("/feasibility-report/:method", h.Feas.Handle)
api.POST("/dual-layer-pdf/:method", h.DLP.Handle)
api.POST("/dependencies/:method", h.Dep.Handle)

// 下载（GET）
api.GET("/export/download/:jobId", h.Export.Download)
api.GET("/technical-plan/read-tender-markdown", h.TechPlan.ReadTenderMD)
// ...

// 流式
api.POST("/ai/chat", h.AI.ChatStream)
api.GET("/events", h.Event.Stream)

// 文件上传（multipart）
api.POST("/upload", h.Upload.Handle)
```

**`:method` 动态路由的设计取舍**：

- 优点：不用为 168 个接口写 168 个 handler 注册，路由表清爽
- 缺点：丢失框架级路由校验，方法名拼错要运行时才发现

**折中方案**：每个 domain 一个 `Handle(c)` 分发器，内部 `switch method`，未知 method 返回 404。这样既保留动态路由的简洁，又有编译期的 handler 完整性（用常量定义 method 名）。

```go
const (
    MethodLoadState = "load-state"
    MethodSaveOutline = "save-outline"
    // ...
)
```

---

## 八、契约测试

**必须做**：写一套 golden test，对同一个请求体，桌面版 bridge 和 Web API 返回**结构一致**的 JSON。

```go
// internal/http/contract_test.go
func TestContract_TechnicalPlanSaveOutline(t *testing.T) {
    cases := loadGoldenCases("testdata/technical-plan/save-outline")
    for _, c := range cases {
        got := callGoAPI(t, c.Request)
        assertJSONEqual(t, c.Expected, got)  // 忽略时间戳
    }
}
```

`testdata` 里的 `Expected` 从桌面版真实运行的响应中导出。

**覆盖范围**：优先做 P0 的 68 个方法（config 5 + technicalPlan 26 + tasks 25 + agent 10 + ai 5 + export 3...）。

这套契约测试是防止「迁移后行为悄悄漂移」的唯一可靠手段，不要省。
