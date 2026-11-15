/**
 * Web 版 bridge：把桌面版的 window.yibiao 调用转成 HTTP 请求。
 *
 * 设计目标：前端 389 处调用零改动。做到这一点的关键是：
 *   1. 方法名 → kebab-case 路径，规则统一
 *   2. 用 Proxy 自动生成全部 256 个方法，不手写映射表
 *   3. 业务失败用 HTTP 200 + { ok:false } 表达，与桌面版 Promise reject 语义对齐
 *
 * 契约见 docs/web-migration/01-bridge-contract.md。
 */

/* eslint-disable @typescript-eslint/no-explicit-any */

const API_BASE = import.meta.env.VITE_API_BASE || '/api';

// ---------- 令牌管理 ----------

const TOKEN_KEY = 'yb_access_token';
const REFRESH_KEY = 'yb_refresh_token';

export function getAccessToken(): string {
  return localStorage.getItem(TOKEN_KEY) || '';
}

/**
 * 演示旁路开关（由 AuthGate 根据 /public/bootstrap.demoAuthBypass 写入）。
 *
 * 打开后：
 *   - 允许无 access token 发起业务请求（服务端会注入默认管理员）
 *   - 401 不再强制跳登录页
 * 关闭后行为与正式登录一致。登录/租户代码仍保留。
 */
let demoAuthBypass = false;

export function setDemoAuthBypass(enabled: boolean) {
  demoAuthBypass = enabled;
}

export function isDemoAuthBypass(): boolean {
  return demoAuthBypass;
}

export function setTokens(access: string, refresh: string) {
  localStorage.setItem(TOKEN_KEY, access);
  localStorage.setItem(REFRESH_KEY, refresh);

  // 关键：登录后要重建 SSE 连接。
  //
  // 为什么：AgentQuestionDialogProvider 等 Provider 在登录前就挂载了，
  // 那时没有 token，ensureEventSource 直接 return 且不会重试。
  // 结果就是登录后永远收不到推送（提问弹窗、任务进度全失效）。
  if (connectedTopics.size > 0) {
    eventSource?.close();
    eventSource = null;
    ensureEventSource();
  }
}

export function clearTokens() {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(REFRESH_KEY);

  // 退出登录必须断开 SSE，否则会用旧 token 继续收事件
  eventSource?.close();
  eventSource = null;
}

/** 当前项目 ID。技术方案等业务接口需要它做作用域隔离。 */
const PROJECT_KEY = 'yb_current_project';

export function getProjectId(): string {
  return localStorage.getItem(PROJECT_KEY) || '';
}

export function setProjectId(id: string) {
  localStorage.setItem(PROJECT_KEY, id);
}

/**
 * 确保项目 ID 已就绪。
 *
 * 为什么需要：
 *   Web 版是多项目环境，所有业务接口都要 X-Project-Id。
 *   但前端是从桌面版移植的，没有「选择项目」的概念。
 *
 *   所以在首次请求前先拿一个项目（没有就自动创建），
 *   前端代码完全不用改。
 */
let projectReady: Promise<string> | null = null;

async function ensureProject(): Promise<string> {
  const existing = getProjectId();
  if (existing) {
    // 已缓存的项目 ID 直接用。
    //
    // 注意：不做存在性校验 —— 每个请求都校验会多一次往返，
    // 而且项目被删的情况极少。真失效时业务接口会报错，
    // 由调用方处理（见下面 ensureProjectValid 的场景）。
    return existing;
  }

  // 并发请求只发一次
  if (projectReady) return projectReady;

  const attempt = (async () => {
    const token = getAccessToken();
    // 没有 token 且非演示旁路时不发请求（登录前会 401，白白浪费）
    if (!token && !demoAuthBypass) return '';

    try {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
      };
      if (token) headers.Authorization = `Bearer ${token}`;

      const res = await fetch(`${API_BASE}/projects/current`, {
        method: 'POST',
        headers,
        body: '{}',
      });
      if (!res.ok) {
        // 401 说明 token 还没就绪或已过期，不缓存失败结果
        if (res.status !== 401) {
          console.warn('[bridge] 获取项目失败', res.status);
        }
        return '';
      }
      const body = await res.json();
      const id = body?.data?.id || '';
      if (id) {
        setProjectId(id);
        console.log('[bridge] 当前项目:', id, body.data.name);
      }
      return id;
    } catch (e) {
      console.warn('[bridge] 获取项目异常', e);
      return '';
    }
  })();

  projectReady = attempt;

  const id = await attempt;

  // 关键：没拿到就清掉缓存，下次请求会重试。
  // 否则一个失败的 Promise 会被永久复用，项目 ID 永远拿不到。
  if (!id) {
    projectReady = null;
  }

  return id;
}

/**
 * 校验当前项目是否仍然有效。
 *
 * 场景：用户在另一个标签页删了这个项目，或者本地缓存指向的项目
 * 已经被删除。这时业务接口会报 404 / 400，体验很差。
 *
 * 在应用启动时调一次，失效就重新取。
 */
export async function ensureProjectValid(): Promise<string> {
  const existing = getProjectId();
  const token = getAccessToken();
  if (!existing || (!token && !demoAuthBypass)) return ensureProject();

  try {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Project-Id': existing,
    };
    if (token) headers.Authorization = `Bearer ${token}`;

    const res = await fetch(`${API_BASE}/projects/select`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ projectId: existing }),
    });
    if (res.ok) return existing;
  } catch {
    /* 网络问题，保持现状 */
    return existing;
  }

  // 项目不存在：清掉缓存重新取
  console.warn('[bridge] 缓存的项项目已失效，重新获取');
  clearProject();
  return ensureProject();
}

/** 清除项目（切换账号时调用）。 */
export function clearProject() {
  localStorage.removeItem(PROJECT_KEY);
  projectReady = null;
}

// ---------- HTTP 基础 ----------

/** 业务错误。结构与桌面版 IPC 抛出的错误对齐，前端的 catch 逻辑不用改。 */
export class BridgeError extends Error {
  code: string;
  detail?: unknown;

  constructor(code: string, message: string, detail?: unknown) {
    super(message);
    this.name = 'BridgeError';
    this.code = code;
    this.detail = detail;
  }
}

interface Envelope<T> {
  ok: boolean;
  data?: T;
  error?: { code: string; message: string; detail?: unknown };
}

/** 刷新令牌的并发控制：多个请求同时 401 时只发一次刷新。 */
let refreshPromise: Promise<boolean> | null = null;

async function tryRefresh(): Promise<boolean> {
  if (refreshPromise) return refreshPromise;

  refreshPromise = (async () => {
    const refresh = localStorage.getItem(REFRESH_KEY);
    if (!refresh) return false;

    try {
      const res = await fetch(`${API_BASE}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken: refresh }),
      });
      if (!res.ok) return false;

      const body = (await res.json()) as Envelope<{ accessToken: string; refreshToken: string }>;
      if (!body.ok || !body.data) return false;

      setTokens(body.data.accessToken, body.data.refreshToken);
      return true;
    } catch {
      return false;
    } finally {
      refreshPromise = null;
    }
  })();

  return refreshPromise;
}

/** 通知外层跳登录页。由 App 注册，避免 bridge 直接依赖路由。 */
let onUnauthorized: (() => void) | null = null;

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

async function request<T>(path: string, body?: unknown, opts?: { retry?: boolean }): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };

  const token = getAccessToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  // 业务接口需要项目作用域。
  // 首次请求时自动获取（没有项目会自动创建），前端代码不用关心。
  const needsProject = !path.startsWith('/projects') &&
    !path.startsWith('/auth') &&
    !path.startsWith('/config') &&
    !path.startsWith('/upload') &&
    !path.startsWith('/health') &&
    !path.startsWith('/public');

  if (needsProject) {
    const pid = await ensureProject();
    if (pid) headers['X-Project-Id'] = pid;
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  // 401：尝试刷新一次，失败则跳登录（演示旁路除外）
  if (res.status === 401 && opts?.retry !== false) {
    if (demoAuthBypass) {
      throw new BridgeError('UNAUTHORIZED', '演示旁路下请求仍返回 401，请检查服务端 DEMO_AUTH_BYPASS');
    }

    const refreshed = await tryRefresh();
    if (refreshed) return request<T>(path, body, { retry: false });

    clearTokens();
    onUnauthorized?.();
    throw new BridgeError('UNAUTHORIZED', '登录已过期，请重新登录');
  }

  let envelope: Envelope<T>;
  try {
    envelope = (await res.json()) as Envelope<T>;
  } catch {
    throw new BridgeError('INVALID_RESPONSE', `服务响应格式错误（HTTP ${res.status}）`);
  }

  if (!envelope.ok) {
    const err = envelope.error || { code: 'UNKNOWN', message: '未知错误' };
    const message = String(err.message || '').trim() || err.code || '未知错误';
    throw new BridgeError(err.code || 'UNKNOWN', message, err.detail);
  }

  return envelope.data as T;
}

// ---------- 文件上传 ----------

/**
 * 上传队列：伪路径 → 上传 Promise。
 *
 * 桌面版 getPathForFile 是同步的（返回本地路径），
 * Web 版也必须同步返回（伪路径），同时后台上传。
 * 业务接口调用时由服务端等待上传完成。
 */
const uploadQueue = new Map<string, Promise<string>>();
/** 同一个 File 对象复用同一个伪路径（避免重复上传） */
const uploadedFiles = new WeakMap<File, string>();

/** 预登记 + 上传，返回伪路径。
 *
 * 关键约束：**伪路径里的 ID 必须与后端一致**。
 *
 * 原实现用客户端随机 ID 造伪路径，但后端 Prepare 会自己生成一个
 * 不同的 ID 存进 entries —— 于是 import-tender-document 拿伪路径去
 * Resolve 时查不到，报 "上传文件不存在或已过期"。
 *
 * 解法：把客户端生成的 ID 透传给后端 Prepare，让两边用同一个 ID。
 * 这样 getPathForFile 可以同步返回正确的伪路径。
 */
function enqueueUpload(file: File): string {
  const localId =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID().replace(/-/g, '')
      : Math.random().toString(16).slice(2).padEnd(32, '0');

  const pseudo = `upload://${localId}`;

  const task = (async () => {
    try {
      // 1. 预登记（把 localId 传过去，保证两边 ID 一致）
      const prep = await request<{ id: string; pseudoPath: string }>('/upload/prepare', {
        fileName: file.name,
        size: file.size,
        uploadId: localId,
      });

      // 2. 上传内容（multipart，不能走 request 的 JSON 封装）
      const form = new FormData();
      form.append('file', file, file.name);

      const res = await fetch(`${API_BASE}/upload/raw`, {
        method: 'POST',
        headers: {
          ...(getAccessToken() ? { Authorization: `Bearer ${getAccessToken()}` } : {}),
          'X-Upload-Id': prep.id || localId,
        },
        body: form,
      });

      if (!res.ok) {
        throw new BridgeError('UPLOAD_FAILED', `上传失败（HTTP ${res.status}）`);
      }
      return pseudo;
    } catch (e) {
      console.error('[bridge] 上传失败', file.name, e);
      throw e;
    }
  })();

  uploadQueue.set(pseudo, task);
  return pseudo;
}


/** 等待某个伪路径的上传完成。 */
export async function waitForUpload(pseudoPath: string): Promise<string> {
  const task = uploadQueue.get(pseudoPath);
  if (!task) return pseudoPath;
  return task;
}

// ---------- 命名转换 ----------

/** camelCase → kebab-case。loadState → load-state */
function kebab(s: string): string {
  return s.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase();
}

// ---------- SSE ----------

type Listener = (data: any) => void;

const listeners = new Map<string, Set<Listener>>();
let eventSource: EventSource | null = null;
let connectedTopics = new Set<string>();

function topicKey(topic: string) {
  return topic;
}

function ensureEventSource() {
  const topics = [...connectedTopics];
  if (topics.length === 0) return;

  const token = getAccessToken();
  // 没有 token 就不建连。setTokens 会在登录后重新调用这里。
  if (!token) return;

  // EventSource 不能设请求头，令牌走 query（后端 bearerToken 已兼容）
  const url = `${API_BASE}/events?topics=${encodeURIComponent(topics.join(','))}&access_token=${encodeURIComponent(token)}`;

  eventSource?.close();
  eventSource = new EventSource(url);

  eventSource.onopen = () => {
    // 连上后才算真正订阅成功
    console.log('[bridge] SSE 已连接，topics:', topics.join(','));
  };

  eventSource.onmessage = () => {
    /* 默认事件不用 */
  };

  // 每个 topic 一个 event listener
  topics.forEach((topic) => {
    eventSource!.addEventListener(topic, (ev) => {
      const set = listeners.get(topicKey(topic));
      if (!set) return;
      let data: any;
      try {
        data = JSON.parse((ev as MessageEvent).data);
      } catch {
        data = (ev as MessageEvent).data;
      }
      set.forEach((fn) => {
        try {
          fn(data);
        } catch (e) {
          console.error('[bridge] 事件回调异常', topic, e);
        }
      });
    });
  });

  eventSource.onerror = () => {
    // EventSource 会自动重连。这里只记日志，不手动处理。
    console.warn('[bridge] SSE 连接中断，浏览器将自动重连');
  };
}

function subscribe(topic: string, cb: Listener): () => void {
  if (!listeners.has(topic)) {
    listeners.set(topic, new Set());
    if (!connectedTopics.has(topic)) {
      connectedTopics.add(topic);
      // 新 topic 需要重连才能订阅
      ensureEventSource();
    }
  }
  listeners.get(topic)!.add(cb);

  // 惰性建连
  if (!eventSource) ensureEventSource();

  return () => {
    const set = listeners.get(topic);
    if (!set) return;
    set.delete(cb);
    // 不主动关闭连接：重连成本高，且 topic 可能很快又被订阅
  };
}

/**
 * 事件订阅映射：bridge 的 onXxx → SSE topic。
 *
 * 桌面版有 30 个 onXxx，Web 版合并成 12 个 topic。
 * 见 docs/web-migration/01-bridge-contract.md §4。
 *
 * 未列出的 onXxx 返回空取消函数（避免前端崩溃）。
 */
const EVENT_TOPIC: Record<string, string> = {
  'tasks.onTaskEvent': 'task',
  'agent.onStatus': 'agent.status',
  'agent.onQuestion': 'agent.question',
  'ai.onHttpError': 'ai.http-error',
  'ai.onBalanceInsufficient': 'ai.balance',
  'autoConfirmation.onChanged': 'auto-confirmation',
  'knowledgeBase.onEvent': 'knowledge-base',
  'export.onWordExportProgress': 'export.progress',
  'dualLayerPdf.onEvent': 'dual-layer-pdf',
  'database.onStatus': 'database.status',
  'developerTokenStats.onChanged': 'usage.changed',
  'developerAgentMonitor.onEvent': 'agent.monitor',
  'notice.onChanged': 'notice',
};


/**
 * 弹浏览器文件选择框并等用户选完。
 *
 * 为什么需要：桌面版的「点按钮上传」会调主进程弹系统文件框，
 * Web 端没有这个能力。浏览器只能通过 <input type="file"> 选文件，
 * 而且必须在用户点击的手势上下文里触发。
 *
 * 注意：用户取消时 input 的 change 事件不触发，
 * 所以用 window 的 focus 事件兜底（选完或取消都会回到窗口）。
 */
function pickFilesFromBrowser(accept: string): Promise<File[]> {
  return new Promise((resolve) => {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = accept;
    input.multiple = true;
    input.style.cssText = 'position:fixed;left:-9999px;width:1px;height:1px;opacity:0';

    let settled = false;
    const finish = (files: File[]) => {
      if (settled) return;
      settled = true;
      input.remove();
      window.removeEventListener('focus', onFocus);
      resolve(files);
    };

    const onFocus = () => {
      // 回到窗口后给 change 一点时间；没 change 说明用户取消了
      setTimeout(() => finish([]), 600);
    };

    input.addEventListener('change', () => {
      finish(Array.from(input.files || []));
    });

    document.body.appendChild(input);
    input.click();
    // 取消场景：焦点回窗口时收尾
    setTimeout(() => window.addEventListener('focus', onFocus, { once: true }), 0);
  });
}

// ---------- Web 端不适用 / 降级接口 ----------
//
// 这些接口在桌面版承担本地能力（授权、插件、字体、系统信息），
// Web 端没有对应实现。统一返回合理的默认值，
// 避免调用时抛 404 让页面报错或弹错。

const WEB_NOOP_HANDLERS: Record<string, (...args: any[]) => unknown> = {
  /** 授权相关：Web 版由服务端统一鉴权，不做本地授权校验 */
  'license.getStatus': () => Promise.resolve({ status: 'activated', type: 'web', expireAt: null, daysLeft: null }),
  'license.refresh': () => Promise.resolve({ status: 'activated', type: 'web', expireAt: null, daysLeft: null }),
  'license.importOfflineFile': () => Promise.reject(new Error('Web 版不支持离线授权')),
  'license.activateOfflineCode': () => Promise.reject(new Error('Web 版不支持离线授权')),

  /** 服务可用性：Web 版所有能力都在线 */
  'available.getStatus': () => Promise.resolve({ available: true, reason: '' }),
  'requiredOnlineServices.getStatus': () => Promise.resolve({ ok: true, services: [] }),

  /**
   * 自动回答设置：Web 版默认关闭（每步都让人确认更安全）。
   *
   * 前端有 catch 兜底，不打这个接口也不会坏；
   * 但缺了会有一堆 401 噪音，影响排查真问题。
   */
  'autoConfirmation.getState': () => Promise.resolve({ enabled: false }),
  'autoConfirmation.setEnabled': (enabled: unknown) => Promise.resolve({ enabled: Boolean(enabled) }),

  /** 界面跳转：Web 版由路由管理 */
  'ui.setCurrentView': () => Promise.resolve(),

  /** 配置读取（前端只读免授权天数、过期弹窗开关） */
  'config.freeLicenseDays': () => Promise.resolve(0),
  'config.expirePopupEnabled': () => Promise.resolve(false),
  'config.expirePopupDismissible': () => Promise.resolve(true),
  /** 桌面版打开 userData 配置目录；Web 版无本地目录 */
  'config.openConfigFolder': () => Promise.resolve({ success: true, message: 'Web 版无本地配置目录' }),

  /** 智能体自检报告：桌面版写文件；Web 版触发浏览器下载 */
  'agent.exportSelfCheckReport': (payload: unknown) => {
    const report = payload && typeof payload === 'object' ? payload as Record<string, unknown> : {};
    const text = typeof report.detail_text === 'string' && report.detail_text
      ? report.detail_text
      : JSON.stringify(report, null, 2);
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
    const link = document.createElement('a');
    link.href = URL.createObjectURL(blob);
    link.download = `pi-agent-self-check-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.txt`;
    link.click();
    URL.revokeObjectURL(link.href);
    return Promise.resolve({ success: true, message: '智能体自检报告已下载', path: link.download });
  },

  /** 开发者独立窗口：Web 版不提供 */
  'developerTokenStats.openWindow': () => Promise.resolve({ success: false, message: 'Web 版不支持独立开发者窗口' }),
  'developerAgentMonitor.openWindow': () => Promise.resolve({ success: false, message: 'Web 版不支持独立开发者窗口' }),

  /** 插件：Web 版不支持本地插件 */
  'plugins.getAvailablePlugins': () => Promise.resolve([]),
  'plugins.install': () => Promise.reject(new Error('Web 版不支持安装本地插件')),
  'plugins.installOffline': () => Promise.reject(new Error('Web 版不支持安装本地插件')),
  'plugins.uninstall': () => Promise.reject(new Error('Web 版不支持卸载本地插件')),
  'plugins.enable': () => Promise.reject(new Error('Web 版不支持启用本地插件')),
  'plugins.disable': () => Promise.reject(new Error('Web 版不支持禁用本地插件')),
  'plugins.update': () => Promise.reject(new Error('Web 版不支持更新本地插件')),
  'plugins.checkUpdates': () => Promise.resolve([]),
  'plugins.updateAll': () => Promise.resolve({ updated: 0 }),
  'plugins.openConfig': () => Promise.resolve(),
  'plugins.refreshMarket': () => Promise.resolve([]),
  'plugins.clearUpdateFailedState': () => Promise.resolve(),
  'plugins.notifyEvent': () => Promise.resolve(),

  /** 依赖检测：Web 版无需本地依赖 */
  'dependencies.check': () => Promise.resolve({ ok: true, missing: [] }),
  'dependencies.list': () => Promise.resolve([]),
  'dependencies.install': () => Promise.resolve({ success: true }),

  /** 系统字体：Web 端用浏览器字体，返回常用中文字体 */
  'systemFonts.list': () => Promise.resolve(['宋体', '黑体', '楷体', '仿宋', '微软雅黑', '等线']),

  /** 开发者工具：Web 版不提供 */
  'developerLayoutFigure.getStatus': () => Promise.resolve({ enabled: false }),
  'developerExpansionReplaceTest.run': () => Promise.resolve({ results: [] }),

  /** 捐赠 / 公众号：桌面版专属 */
  'donation.getInfo': () => Promise.resolve(null),
  'officialAccount.getInfo': () => Promise.resolve(null),

  /**
   * 可行性研究报告（Web 版）。
   *
   * 状态、资料解析、目录、正文全部落库，
   * 只有 AI 生成（分析/目录/正文）走 agent 服务。
   */
  'feasibilityReport.loadState': () => request('/feasibility-report/load-state', {}),
  'feasibilityReport.importSourceDocuments': async (filePaths?: string[]) => {
    let paths = filePaths;
    if (!paths || paths.length === 0) {
      const files = await pickFilesFromBrowser('.doc,.docx,.pdf,.xls,.xlsx,.wps,.txt,.md');
      if (files.length === 0) return { success: false, message: '未选择文件', sourceFiles: [] };
      paths = files.map((f) => window.yibiao.file.getPathForFile(f) as string).filter(Boolean);
      await Promise.all(paths.map((p) => waitForUpload(p)));
    }
    return request('/feasibility-report/import-source-documents', { filePaths: paths });
  },
  'feasibilityReport.removeSourceDocument': (sourceId: string) =>
    request('/feasibility-report/remove-source-document', { sourceId }),
  'feasibilityReport.readSourceMarkdown': (sourceId: string) =>
    request('/feasibility-report/read-source-markdown', { sourceId }),
  'feasibilityReport.readCombinedSourceMarkdown': () =>
    request('/feasibility-report/read-combined-source-markdown', {}),
  'feasibilityReport.updateStep': (step: string) =>
    request('/feasibility-report/update-step', { step }),
  'feasibilityReport.saveProjectInfo': (projectInfo: unknown) =>
    request('/feasibility-report/save-project-info', projectInfo as Record<string, unknown>),
  'feasibilityReport.saveAnalysis': (markdown: string) =>
    request('/feasibility-report/save-analysis', { markdown }),
  'feasibilityReport.saveOutlineConfig': (payload: unknown) =>
    request('/feasibility-report/save-outline-config', payload as Record<string, unknown>),
  'feasibilityReport.saveOutline': (payload: unknown) =>
    request('/feasibility-report/save-outline', payload as Record<string, unknown>),
  'feasibilityReport.saveKeyParameters': (markdown: string) =>
    request('/feasibility-report/save-key-parameters', { markdown }),
  'feasibilityReport.saveChapterContent': (payload: unknown) =>
    request('/feasibility-report/save-chapter-content', payload as Record<string, unknown>),
  'feasibilityReport.clear': () => request('/feasibility-report/clear', {}),

  /**
   * 资信库（Web 版）。
   *
   * 数据存服务端：企业档案 + 证书/人员/业绩/材料/图片。
   * 保存类接口返回完整快照，前端直接 setSnapshot。
   */
  'credentialLibrary.load': () => request('/credential-library/load', {}),
  'credentialLibrary.saveProfile': (partial: unknown) =>
    request('/credential-library/save-profile', partial as Record<string, unknown>),
  'credentialLibrary.addProfileImages': (fieldKey: unknown, filePaths: unknown) =>
    request('/credential-library/add-profile-images', { fieldKey, filePaths }),
  'credentialLibrary.deleteImage': (imageId: unknown) =>
    request('/credential-library/delete-image', { imageId }),
  'credentialLibrary.saveCertificate': (payload: unknown) =>
    request('/credential-library/save-certificate', payload as Record<string, unknown>),
  'credentialLibrary.deleteCertificate': (id: unknown) =>
    request('/credential-library/delete-certificate', { id }),
  'credentialLibrary.saveEmployee': (payload: unknown) =>
    request('/credential-library/save-employee', payload as Record<string, unknown>),
  'credentialLibrary.deleteEmployee': (id: unknown) =>
    request('/credential-library/delete-employee', { id }),
  'credentialLibrary.saveProject': (payload: unknown) =>
    request('/credential-library/save-project', payload as Record<string, unknown>),
  'credentialLibrary.deleteProject': (id: unknown) =>
    request('/credential-library/delete-project', { id }),
  'credentialLibrary.saveOtherMaterial': (payload: unknown) =>
    request('/credential-library/save-other-material', payload as Record<string, unknown>),
  'credentialLibrary.deleteOtherMaterial': (id: unknown) =>
    request('/credential-library/delete-other-material', { id }),
  'credentialLibrary.importTestData': () => request('/credential-library/import-test-data', {}),

  /** 存储路径：Web 端由服务端管理 */
  'storage.getPaths': () => Promise.resolve({ dataDir: '', tempDir: '' }),
  'storage.openDataDir': () => Promise.resolve(),
};

// ---------- ROOT 层 ----------

const ROOT_METHOD_PATH: Record<string, string> = {
  getVersion: '/root/get-version',
};

const ROOT_LOCAL: Record<string, (...args: any[]) => unknown> = {
  // Web 版无此概念，返回「已启用」让提示不出现
  getGpuHardwareAccelerationStatus: () => ({ enabled: true, configured: true, restartRequired: false }),
  saveGpuHardwareAccelerationPreference: () => ({ success: true, enabled: true, configured: true, restartRequired: false }),
  startGpuHardwareAccelerationTrial: () => ({ success: true }),
  relaunchWithGpuHardwareAccelerationDisabled: () => ({ success: true }),
  // 更新相关：Web 版整块砍掉
  checkUpdate: () => ({ hasUpdate: false, latestVersion: '', currentVersion: __APP_VERSION__ }),
  getUpdateDownloadUrl: () => '',
  quitAndInstall: () => ({ success: false, message: 'Web 版无需安装' }),
  openExternal: (url: string) => {
    // 只允许 http/https，防 javascript: 伪协议
    if (/^https?:\/\//i.test(url)) window.open(url, '_blank', 'noopener,noreferrer');
    return { success: true };
  },
};

// ---------- 命名空间 ----------

/**
 * Web 版不提供的命名空间。
 *
 * database：桌面版用它等待本地 SQLite 迁移完成，Web 版数据库在服务端、
 *           进程启动时已迁移完毕，不存在这个概念。不暴露可让
 *           WorkspaceDatabaseGate 走「无 database 即放行」分支。
 */
const OMITTED_NAMESPACES = new Set(['database', 'workspace-database']);

/**
 * 命名空间级的本地实现。
 *
 * 为什么需要：
 *   有些方法不适合走通用「POST /api/ns/method-kebab」——
 *   例如 assets.download 要返回一个可直接放进 <a href> 的 URL，
 *   而不是发一次 POST 拿 JSON。
 *
 * 键格式：`命名空间.方法名`
 */
const NS_LOCAL: Record<string, (...args: any[]) => unknown> = {
  // Web 端不适用的接口先铺底，后面同名实现会覆盖
  ...WEB_NOOP_HANDLERS,

  /**
   * 导出废标检查报告（Web 版适配）。
   */
  'rejectionCheck.exportExcel': () => {
    const token = getAccessToken();
    const projectId = getProjectId();
    return fetch(`${API_BASE}/rejection-check/export-excel`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(projectId ? { 'X-Project-Id': projectId } : {}),
      },
      body: '{}',
    }).then(async (res) => {
      if (!res.ok) throw new Error(`导出失败（HTTP ${res.status}）`);
      const blob = await res.blob();
      const link = document.createElement('a');
      link.href = URL.createObjectURL(blob);
      link.download = 'rejection-check-report.csv';
      link.click();
      URL.revokeObjectURL(link.href);
      return { success: true, filePath: 'rejection-check-report.csv' };
    });
  },

  /**
   * 选择文件（Web 版适配）。
   *
   * 桌面版弹系统文件选择框；Web 端返回空数组，
   * 由页面自己的 <input type="file"> 负责选文件。
   */
  'file.selectDuplicateCheckFiles': () => Promise.resolve([]),
  'file.selectBlindCheckFile': () => Promise.resolve(''),
  'file.selectBlindCheckTenderFile': () => Promise.resolve(''),

  /**
   * 导出模板 CRUD（Web 版适配）。
   *
   * 系统预设由前端 DEFAULT_EXPORT_FORMAT 提供，后端只存用户自建模板。
   */
  /**
   * 导入招标文件（Web 版）。
   *
   * 桌面版：不传 filePaths 时后端弹系统文件选择框。
   * Web 端：浏览器做不到（需用户手势），改为**弹隐藏的 <input type="file">**，
   * 用户选完文件 → 上传得到伪路径 → 再调接口。
   *
   * 这是「点击上传」这条路径能走通的关键 ——
   * 拖拽上传走 onDropFiles，本来就有 File 对象；
   * 只有点按钮才有这个问题。
   */
  'technicalPlan.importTenderDocument': async (filePaths?: string[]) => {
    let paths = filePaths;
    if (!paths || paths.length === 0) {
      const files = await pickFilesFromBrowser('.doc,.docx,.pdf,.wps,.txt,.md');
      if (files.length === 0) {
        return { success: false, message: '未选择文件' };
      }
      paths = files.map((f) => window.yibiao.file.getPathForFile(f) as string).filter(Boolean);
      // 关键：等上传真正完成再调接口。
      // 否则后端 Resolve 时文件还没落盘，会报"文件尚不可用"。
      await Promise.all(paths.map((p) => waitForUpload(p)));
    }
    return request('/technical-plan/import-tender-document', { filePaths: paths });
  },

  /**
   * 导入原方案文件（Web 版）：同 importTenderDocument，
   * 无参时弹浏览器文件选择框。
   */
  'technicalPlan.importOriginalPlanDocument': async (filePaths?: string[]) => {
    let paths = filePaths;
    if (!paths || paths.length === 0) {
      const files = await pickFilesFromBrowser('.doc,.docx,.pdf,.wps,.txt,.md');
      if (files.length === 0) return { success: false, message: '未选择文件' };
      paths = files.map((f) => window.yibiao.file.getPathForFile(f) as string).filter(Boolean);
      await Promise.all(paths.map((p) => waitForUpload(p)));
    }
    return request('/technical-plan/import-original-plan-document', { filePaths: paths });
  },

  'templates.list': () => request('/templates/list', {}),
  'templates.get': (templateId: string) => request('/templates/get', { templateId }),
  'templates.create': (config: unknown) => request('/templates/create', { config }),
  'templates.update': (templateId: string, config: unknown) =>
    request('/templates/update', { templateId, config }),
  'templates.delete': (templateId: string) => request('/templates/delete', { templateId }),
  'templates.duplicate': (templateId: string) => request('/templates/duplicate', { templateId }),

  /**
   * 模板样张预览：后端暂不生成，返回空让前端走降级。
   */
  'templates.renderPreview': () => Promise.resolve({ key: '', bytes: new Uint8Array(), roles: [] }),

  /**
   * 盲检预设列表（Web 版适配）。
   */
  'blindCheck.listPresets': () => request('/blind-check/list-presets', {}),

  /**
   * 我的预设管理：Web 版暂不支持自定义预设，返回内置列表。
   */
  'blindCheck.createPreset': () => request('/blind-check/list-presets', {}),
  'blindCheck.updatePreset': () => request('/blind-check/list-presets', {}),
  'blindCheck.deletePreset': () => request('/blind-check/list-presets', {}),

  /**
   * 招标来源（盲检里用来抽敏感词）。
   * Web 版暂未实现，返回空。
   */
  'blindCheck.getTenderSources': () => Promise.resolve({ items: [] }),

  /**
   * 下载修复后的文档（Web 版适配）。
   */
  'blindCheck.saveFixedDocument': () => {
    const token = getAccessToken();
    const projectId = getProjectId();
    return fetch(`${API_BASE}/blind-check/save-fixed-document`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(projectId ? { 'X-Project-Id': projectId } : {}),
      },
      body: '{}',
    }).then(async (res) => {
      if (!res.ok) throw new Error(`下载失败（HTTP ${res.status}）`);
      const blob = await res.blob();
      const link = document.createElement('a');
      link.href = URL.createObjectURL(blob);
      link.download = 'fixed-document.docx';
      link.click();
      URL.revokeObjectURL(link.href);
      return { success: true, filePath: 'fixed-document.docx' };
    });
  },

  /**
   * 启动盲检（Web 版适配）。
   */
  'tasks.startBlindCheck': () => request('/blind-check/start-check', {}),

  /**
   * 启动盲检自动修复（Web 版适配）。
   *
   * 后端会校验修复是否引入新错误，有则拒绝交付并报错。
   */
  'tasks.startBlindCheckFix': (payload: unknown) => {
    const p = (payload || {}) as Record<string, any>;
    return request('/blind-check/start-fix', {
      fixRules: p.fixRules || p.rules || [],
    });
  },

  /**
   * 从招标文件生成预设（AI 抽取）：Web 版暂未实现。
   */
  'tasks.startBlindCheckPresetExtraction': () => Promise.reject(new Error('从招标文件生成预设功能暂未开放')),

  /**
   * 从技术方案导入招标文件（Web 版适配）。
   *
   * 用户在技术方案里已经导入过招标文件，这里直接复用，
   * 不用让用户再传一遍。
   */
  'rejectionCheck.importTenderFromTechnicalPlan': async () => {
    // 从技术方案状态读招标文件路径
    const state = await request('/technical-plan/load-state', {}).catch(() => null);
    const path = (state as any)?.data?.tenderMarkdownPath || (state as any)?.tenderMarkdownPath;
    if (!path) {
      throw new Error('技术方案中还没有导入招标文件');
    }
    return request('/rejection-check/import-document', {
      role: 'tender',
      filePaths: [path],
    });
  },

  /**
   * 抽取招标文件的无效与废标项（Web 版适配）。
   */
  'tasks.startRejectionItemsExtraction': () => request('/rejection-check/extract', {}),

  /**
   * 启动废标项检查（Web 版适配）。
   */
  'tasks.startRejectionCheck': (payload: unknown) => {
    const p = (payload || {}) as Record<string, any>;
    return request('/rejection-check/start-check', {
      checkOptions: p.checkOptions || p.runOptions || {
        rejectionCheck: true,
        typoCheck: false,
        logicCheck: false,
      },
    });
  },

  /**
   * 启动查重分析（Web 版适配）。
   *
   * 桌面版是后台任务，Web 版复用 save-files + analyze：
   * 先存文件清单（顺带触发解析），分析在后端异步跑。
   */
  'tasks.startDuplicateAnalysis': (payload: unknown) => {
    const p = (payload || {}) as Record<string, any>;

    return request('/duplicate-check/save-files', {
      tenderFile: p.tenderFile || null,
      tenderFiles: p.tenderFiles || [],
      bidFiles: p.bidFiles || [],
      step: 'analysis',
      activeAnalysisTab: 'metadata',
    });
  },

  /**
   * 导出查重报告（Web 版适配）。
   *
   * 后端返回 CSV 文件流，用带 token 的直链让浏览器下载。
   */
  'duplicateCheck.exportExcel': () => {
    const token = getAccessToken();
    const projectId = getProjectId();
    const url = `${API_BASE}/duplicate-check/export-excel`;
    // 用 fetch + blob 触发下载（需要带 Authorization 头，直链不行）
    return fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(projectId ? { 'X-Project-Id': projectId } : {}),
      },
      body: '{}',
    })
      .then(async (res) => {
        if (!res.ok) throw new Error(`导出失败（HTTP ${res.status}）`);
        const blob = await res.blob();
        const link = document.createElement('a');
        link.href = URL.createObjectURL(blob);
        link.download = 'duplicate-check-report.csv';
        link.click();
        URL.revokeObjectURL(link.href);
        return { success: true, filePath: 'duplicate-check-report.csv' };
      });
  },

  /**
   * 保存生成配置（Web 版适配）。
   *
   * 桌面版的 saveGenerationConfig 是个大杂烩接口，
   * 每次只传一个字段（referenceKnowledgeDocumentIds / globalFactsMode / ...）。
   * Web 后端按字段拆成不同接口。
   */
  'technicalPlan.saveGenerationConfig': (payload: unknown) => {
    const p = (payload || {}) as Record<string, any>;
    // 统一走后端 save-generation-config：局部字段合并后回传完整配置，
    // 避免返回 {} 导致 TechnicalPlanHome 把 outlineMode 等写成 undefined 白屏。
    return request('/technical-plan/save-generation-config', p);
  },

  /**
   * 正文生成（Web 版适配）。
   *
   * 桌面版 params 结构很大（generationOptions 里有 12 个字段），
   * Web 后端只需要其中一部分。这里做映射，把前端参数转成后端契约。
   */
  'tasks.startContentGeneration': (payload: unknown) => {
    const p = (payload || {}) as Record<string, any>;
    const options = (p.generationOptions || {}) as Record<string, any>;
    const word = (p.outlineWordControlSnapshot || p.wordControl || options.outlineWordControlSnapshot || {}) as Record<string, any>;

    // 配图：配图 0% / 无 时必须 disableImages，不能仅靠 type 开关（默认 mermaid/html 常为 true）。
    const imageQuantity = Number(options.imageQuantity ?? 0);
    const typeEnabled = Boolean(options.useAiImages || options.useMermaidImages || options.useHtmlImages);
    const useImages = imageQuantity > 0 && typeEnabled;

    const body: Record<string, unknown> = {
      force: Boolean(p.regenerate),
      disableImages: !useImages,
      // 知识库引用（Web 版新增能力）
      disableKnowledge: options.useKnowledge === false,
    };

    // 字数：有全文总量时只传 totalWords，让服务端按叶子均分；不要再带 sectionWords 当每节目标。
    const totalWords = Number(p.totalWords ?? word.minimumWords ?? word.maximumWords ?? 0);
    const sectionWords = Number(p.targetWordsPerSection ?? word.sectionWords ?? 0);
    if (totalWords > 0) {
      body.totalWords = totalWords;
    } else if (sectionWords > 0) {
      body.targetWordsPerSection = sectionWords;
    }

    if (useImages) {
      body.totalImages = imageQuantity;
    } else {
      body.totalImages = 0;
    }

    // 图片类型
    if (Array.isArray(options.htmlImageTypes)) {
      body.enabledImageKinds = options.htmlImageTypes;
    } else if (typeof options.htmlImageTypes === 'string') {
      body.enabledImageKinds = options.htmlImageTypes.split(/[、,，]/).map((s: string) => s.trim()).filter(Boolean);
    }

    return request('/content/generate', body);
  },

  // 产物下载：拼一个带 token 的直链，浏览器可以直接下载
  'assets.download': (assetId: string) => {
    const token = getAccessToken();
    const base = (import.meta as any).env?.VITE_API_BASE || '';
    return `${base}/api/assets/${encodeURIComponent(assetId)}/download?access_token=${encodeURIComponent(token)}`;
  },
};

function buildNamespace(ns: string): any {
  return new Proxy(
    {},
    {
      get(_t, prop: string) {
        if (typeof prop !== 'string') return undefined;

        // 命名空间级本地实现优先（如 assets.download 返回直链）
        const nsLocalKey = `${ns}.${prop}`;
        if (nsLocalKey in NS_LOCAL) {
          const impl = NS_LOCAL[nsLocalKey];
          return (...args: unknown[]) => Promise.resolve(impl(...args));
        }

        // file.getPathForFile：同步返回伪路径，后台上传。
        //
        // 这是 Web 版最关键的一处兼容：
        // 桌面版返回本地绝对路径（同步），前端 12 处调用都按同步用。
        // Web 版返回 upload://<id>，同时后台上传，
        // 服务端在业务接口被调用时解析这个伪路径。
        if (ns === 'file' && prop === 'getPathForFile') {
          return (file: File): string => {
            if (!file) return '';
            // 已经上传过的文件（同一 File 对象）复用
            const existing = uploadedFiles.get(file);
            if (existing) return existing;
            const pseudo = enqueueUpload(file);
            uploadedFiles.set(file, pseudo);
            return pseudo;
          };
        }

        // 事件订阅
        if (/^on[A-Z]/.test(prop)) {
          const key = `${ns}.${prop}`;
          const topic = EVENT_TOPIC[key];
          if (!topic) {
            // 未映射的事件：返回空取消函数，前端不会崩
            return () => () => {};
          }
          return (cb: Listener) => subscribe(topic, cb);
        }

        return (...args: unknown[]) => {
          const key = `${ns}.${prop}`;
          const names = POSITIONAL_ARGS[key];

          let body: unknown;
          if (names) {
            // 显式映射：位置参数 → 命名参数
            const obj: Record<string, unknown> = {};
            names.forEach((name, i) => {
              if (i < args.length && args[i] !== undefined) obj[name] = args[i];
            });
            body = obj;
          } else if (args.length === 0) {
            body = {};
          } else if (args.length === 1) {
            // 单参数：对象直接用，基本类型包一层（后端 Bind 需要对象）
            body = typeof args[0] === 'object' && args[0] !== null && !Array.isArray(args[0])
              ? args[0]
              : { value: args[0] };
          } else {
            body = args;
          }

          return request(`/${kebab(ns)}/${kebab(prop)}`, body);
        };
      },
    },
  );
}

// ---------- 组装 ----------

/**
 * 位置参数 → 命名参数映射。
 *
 * 桌面版有些方法签名是「多个位置参数」（如 uploadDocuments(folderId, paths)），
 * 通用 bridge 会把多参数包成数组 [folderId, paths] 发出去，
 * 后端期望的是 { folderId, filePaths }。
 *
 * 这里显式声明转换规则。
 */
const POSITIONAL_ARGS: Record<string, string[]> = {
  /**
   * 单参数方法必须显式声明字段名。
   *
   * 通用逻辑会把单参数包成 { value: x }，但后端 Bind 的是
   * 具名字段（step / nodeId / sectionId ...），拿不到值就报
   * 「步骤不能为空」这类错误。这类问题只在真点界面时暴露，
   * 类型检查和单测都发现不了。
   */
  'technicalPlan.updateStep': ['step'],
  'feasibilityReport.updateStep': ['step'],
  'technicalPlan.selectBidSection': ['sectionId'],
  'technicalPlan.readTenderSourceMarkdown': ['sourceId'],
  'technicalPlan.removeTenderDocument': ['sourceId'],
  'technicalPlan.saveGlobalFacts': ['globalFacts'],
  'technicalPlan.saveSectionContent': ['nodeId', 'content'],
  'knowledgeBase.uploadDocuments': ['folderId', 'filePaths'],
  'knowledgeBase.renameFolder': ['folderId', 'name'],
  'knowledgeBase.reorderFolder': ['draggedFolderId', 'targetFolderId', 'position'],
  'knowledgeBase.deleteFolder': ['folderId'],
  'knowledgeBase.deleteDocument': ['documentId'],
  'knowledgeBase.moveDocument': ['documentId', 'targetFolderId', 'targetDocumentId', 'position'],
  'knowledgeBase.retryDocument': ['documentId'],
  'knowledgeBase.startMatching': ['documentId', 'batchSize'],
  'knowledgeBase.readMarkdown': ['documentId'],
  'knowledgeBase.readItems': ['documentId'],
  'knowledgeBase.readAnalysis': ['documentId'],
  'blindCheck.importFile': ['path'],
  'blindCheck.selectPreset': ['presetId'],
  'blindCheck.setUseCredentialWords': ['enabled'],
  'blindCheck.saveFixRules': ['rules'],
  'rejectionCheck.importDocument': ['role', 'filePaths'],
  'rejectionCheck.removeDocument': ['role', 'documentId'],
  'projects.select': ['projectId'],
  'projects.create': ['name'],
  'projects.rename': ['projectId', 'name'],
  'projects.remove': ['projectId'],
};

const nsCache = new Map<string, any>();

export const bridge = new Proxy(
  {},
  {
    get(_t, prop: string) {
      if (prop === 'appName') return '标书工作台';
      if (prop === 'platform') return 'web';

      // ROOT 层本地实现
      if (prop in ROOT_LOCAL) {
        return (...args: unknown[]) => Promise.resolve((ROOT_LOCAL[prop] as any)(...args));
      }
      // ROOT 层走 HTTP
      if (prop in ROOT_METHOD_PATH) {
        return (...args: unknown[]) => request(ROOT_METHOD_PATH[prop], args[0] ?? {});
      }
      // ROOT 层事件（全部砍掉，返回空订阅）
      if (/^on[A-Z]/.test(prop)) {
        return () => () => {};
      }

      // 不存在的命名空间返回 undefined，让前端走「可选能力缺失」分支
      if (OMITTED_NAMESPACES.has(prop)) return undefined;

      if (!nsCache.has(prop)) nsCache.set(prop, buildNamespace(prop));
      return nsCache.get(prop);
    },
  },
);

/** 供 main.tsx 判断是否已挂载 */
export const isWebBridge = true;
