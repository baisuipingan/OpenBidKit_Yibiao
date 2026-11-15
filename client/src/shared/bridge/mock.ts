/**
 * Web 版迁移验证用的 mock bridge。
 *
 * 目的：在没有 Electron、没有 Go 后端的情况下，让前端页面能渲染出来，
 *      从而暴露「哪些页面依赖 Electron 的同步行为」「哪些页面会白屏」。
 *
 * 用法：在 index.html 里、main.tsx 之前以 <script type="module"> 引入。
 *      迁移完成后删除，改用 src/shared/bridge/web.ts。
 *
 * 设计要点：
 *  - 用 Proxy 自动生成所有命名空间和方法，不逐个手写
 *  - 所有方法返回 resolved Promise，默认值是空对象/空数组
 *  - onXxx 事件订阅返回空取消函数
 *  - 通过 localStorage 开关控制，方便对比开/关时的表现
 */

const DBG = (...args: unknown[]) => {
  if (localStorage.getItem('yb_mock_verbose') === '1') console.log('[mock-bridge]', ...args);
};

/**
 * 每个方法的默认返回值。
 *
 * 这里不只是「猜一个空值」——它同时是 Web 版 API 的**返回结构清单**。
 * 每次发现前端因返回值形态不符而崩溃，就在这里补一条精确的默认值，
 * 迁移时直接对照这张表实现 Go 接口。
 */
const EXPLICIT: Record<string, unknown> = {
  // 无待确认问题时必须返回 null，返回 {} 会让 question?.options.find() 崩
  'agent.getPendingQuestion': null,
  'agent.getStatus': { status: 'idle' },
  'agent.selfCheck': { ok: true, steps: [] },
  'autoConfirmation.getState': { enabled: false },
  'database.getStatus': { ready: true, version: 44, status: 'ready' },
  'workspace-database.getStatus': { ready: true, version: 44 },
  'config.load': {},
  'config.listModels': [],
  'config.getModelInfo': { name: '', context_length: 0 },
  'requiredOnlineServices.getStatus': {},
  'tasks.getActiveTasks': [],
  'technicalPlan.loadState': null,
  'technicalPlan.loadGenerationConfig': null,
  'technicalPlan.getBusinessFillReview': null,
  'knowledgeBase.list': { folders: [], documents: [] },
  'credentialLibrary.load': { profile: null, certificates: [], employees: [], projects: [], otherMaterials: [] },
  'templates.list': [],
  'storage.getUsage': { total_bytes: 0, file_count: 0 },
  'systemFonts.list': [],
  'export.exportWord': { success: false, message: 'mock: 未实现导出' },
  'blindCheck.loadState': { step: 'upload' },
  'duplicateCheck.loadState': { step: 'upload' },
  'rejectionCheck.loadState': { step: 'upload' },
  'feasibilityReport.loadState': { step: 'upload' },
  'dualLayerPdf.getState': { running: false, items: [], outputs: { pdf: true, image: false } },
  'dualLayerPdf.getEnvironment': { available: false },
};

function defaultValue(ns: string, method: string): unknown {
  const key = `${ns}.${method}`;
  if (key in EXPLICIT) return EXPLICIT[key];

  const m = method.toLowerCase();
  // 已知返回集合的方法统一给空数组，避免前端 .map/.length 崩
  if (m.startsWith('list') || m === 'getactivetasks' || m === 'listpresets') return [];
  // 状态类给空对象（前端通常用 a?.b 访问，不会崩）
  if (m.startsWith('getstate') || m.endsWith('state')) return {};
  if (m.startsWith('load') || m.startsWith('get') || m.startsWith('read')) return null;
  if (m.startsWith('save') || m.startsWith('update') || m.startsWith('set')) return { success: true };
  if (m.startsWith('start') || m.startsWith('run')) return { taskId: 'mock-task', success: true };
  if (m.startsWith('clear') || m.startsWith('remove') || m.startsWith('delete')) return { success: true };
  DBG('未定义默认值:', ns, method);
  return null;
}

const eventLog: Array<[string, string]> = [];

function buildNamespace(ns: string): Record<string, unknown> {
  return new Proxy({} as Record<string, unknown>, {
    get(_target, prop: string) {
      // 驼峰转下划线方便阅读日志
      if (prop === '__ns') return ns;

      // onXxx / onYyy => 事件订阅，返回取消函数
      if (/^on[A-Z]/.test(prop)) {
        return (cb: (...args: unknown[]) => void) => {
          eventLog.push([ns, prop]);
          DBG('订阅事件:', ns, prop);
          // 不主动触发，让页面自己处理空状态
          return () => DBG('取消订阅:', ns, prop);
        };
      }

      return (...args: unknown[]) => {
        DBG('调用:', ns, prop, args.length ? JSON.stringify(args).slice(0, 120) : '');
        // 这几个同步方法必须返回具体类型，不能返回 Promise
        if (ns === 'file' && prop === 'getPathForFile') {
          const f = args[0] as File | undefined;
          return f ? `upload://mock-${f.name}` : '';
        }
        return Promise.resolve(defaultValue(ns, prop));
      };
    },
  });
}

const cache = new Map<string, unknown>();

/**
 * ROOT 层方法（不在任何命名空间下）。
 *
 * 这是桌面版 bridge 的真实结构：`window.yibiao.getVersion()` 与
 * `window.yibiao.technicalPlan.loadState()` 是同一层级的访问。
 * 迁移成 HTTP 时，ROOT 方法对应 /api/root/<method>。
 *
 * 注意：全部必须返回 Promise（前端会 .catch()），不能用同步值。
 */
const noopUnsub = () => {};

const ROOT_METHODS: Record<string, (...args: unknown[]) => unknown> = {
  // 版本与平台
  getVersion: () => Promise.resolve('0.1.0-web-mock'),

  // GPU 加速：Web 无此概念，返回「已启用」让提示不出现
  getGpuHardwareAccelerationStatus: () =>
    Promise.resolve({ enabled: true, configured: true, restartRequired: false }),
  saveGpuHardwareAccelerationPreference: () =>
    Promise.resolve({ success: true, enabled: true, configured: true, restartRequired: false }),
  startGpuHardwareAccelerationTrial: () => Promise.resolve({ success: true }),
  relaunchWithGpuHardwareAccelerationDisabled: () => Promise.resolve({ success: true }),

  // 更新：Web 版整块砍掉，但前端仍会调用，返回「已是最新」让它静默
  checkUpdate: () => Promise.resolve({ hasUpdate: false, latestVersion: '', currentVersion: '0.1.0-web-mock' }),
  getUpdateDownloadUrl: () => Promise.resolve(''),
  quitAndInstall: () => Promise.resolve({ success: false, message: 'Web 版无需安装' }),

  // 外链：Web 版由前端直接 window.open，这里 no-op
  openExternal: (url: unknown) => {
    DBG('openExternal:', url);
    return Promise.resolve({ success: true });
  },
};

/**
 * ROOT 层事件订阅。
 *
 * 这些在桌面版里也挂在 root 上（不在命名空间内），迁移到 HTTP 后
 * 全部并入 GET /api/events 的不同 topic，或直接砍掉。
 */
const ROOT_EVENTS: Record<string, (cb: (...args: unknown[]) => void) => () => void> = {
  onUpdateProgress: () => noopUnsub,
  onUpdateDownloaded: () => noopUnsub,
  onUpdateError: () => noopUnsub,
  onPluginUpdatesAvailable: () => noopUnsub,
};

Object.assign(ROOT_METHODS, ROOT_EVENTS);

const mockBridge = new Proxy({} as Record<string, unknown>, {
  get(_target, prop: string) {
    if (prop === 'appName') return '标书工作台（Web 验证）';
    if (prop === 'platform') return 'web';
    if (prop === '__mock') return true;

    // ROOT 层方法优先
    if (prop in ROOT_METHODS) return ROOT_METHODS[prop];

    if (!cache.has(prop)) cache.set(prop, buildNamespace(prop));
    return cache.get(prop);
  },
});

// yibiaoClient 是 preload 里另一个暴露对象，前端也有引用
const mockClient = {
  appName: '标书工作台（Web 验证）',
  platform: 'web',
};

// 由 bridge/index.ts 统一注入，这里只导出
export const bridge = mockBridge;
export const yibiaoClient = mockClient;

console.info(
  '%c[mock-bridge] 使用 mock 数据',
  'color:#0a0;font-weight:bold',
  '\n  未连后端，所有调用返回空值。',
  '\n  打开详细日志: localStorage.setItem("yb_mock_verbose","1") 后刷新',
);

// 方便在控制台里看页面到底调了哪些方法
export const __ybMockStats = () => ({ events: eventLog });
