/**
 * Pi Session 工厂。
 *
 * 移植自桌面版 client/electron/services/pi/piSessionFactory.cjs，
 * 改动：
 *   - 去掉 Electron 依赖（原本也没有，只是外层调用在 Main 进程）
 *   - proxyInfo 改为直接的 provider 配置（Go 侧提供 baseURL + token）
 *   - 去掉业务工具注入（改由 Go 通过 create_tools 注册，见 tools 模块）
 *   - workspaceInstructions 改为从配置读
 *
 * 核心不变的部分（这些是标书垂类 Agent 能力的来源，不能改）：
 *   - 模型注册方式（openai-completions 兼容层）
 *   - SettingsManager 的 retry/compaction/images 配置
 *   - DefaultResourceLoader 的隔离策略（noExtensions/noSkills/noPromptTemplates）
 *   - 工具注册与阶段门禁机制
 *   - SessionManager 的持久化方式
 */

import { Type } from 'typebox';
import { createAskUserTool } from './askuser.mjs';

import { createCallbackTools } from './tools.mjs';

/** 原生工具。不随业务阶段收缩。 */
export const NATIVE_AGENT_TOOLS = ['read', 'write', 'edit', 'bash', 'grep', 'find', 'ls'];

let piModulesPromise = null;

/** 延迟加载 Pi SDK（ESM）。 */
export function loadPiModules() {
  if (!piModulesPromise) {
    piModulesPromise = Promise.all([
      import('@earendil-works/pi-coding-agent'),
      import('@earendil-works/pi-ai'),
    ]).then(([codingAgent, piAi]) => ({ codingAgent, piAi }));
  }
  return piModulesPromise;
}

function normalizeContextLimit(value) {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 400000;
}

function normalizeOutputLimit(contextLength) {
  return Math.min(32768, normalizeContextLimit(contextLength));
}

/**
 * 创建 Pi Session。
 *
 * @param {object} opts
 * @param {string} opts.workspaceDir      Agent 工作目录（每个任务一个）
 * @param {string} [opts.sessionsDir]     会话持久化目录（跨重启恢复用）
 * @param {string} [opts.sessionFile]     已有会话文件，用于恢复
 * @param {object} opts.provider          { baseUrl, apiKey, model }
 * @param {object} [opts.modelConfig]     { contextLengthLimit }
 * @param {string} opts.workspaceInstructions  工作区约定（AGENTS.md 内容）
 * @param {string[]} [opts.activeTools]   初始启用的工具名
 * @param {Array} [opts.customTools]      业务方通过 create_tools 注册的工具定义
 * @param {boolean} [opts.fixedToolList]  固定工具清单（只切换门禁，不切换 tools）
 * @param {boolean} [opts.summaryEnabled] 是否启用结束总结
 * @param {string} [opts.shellPath]       自定义 shell
 * @param {object} [opts.env]             额外环境变量
 */
export async function createPiSession(opts) {
  const {
    workspaceDir,
    sessionsDir,
    sessionFile,
    provider,
    modelConfig = {},
    workspaceInstructions = '',
    activeTools,
    customTools: businessTools = [],
    fixedToolList = false,
    summaryEnabled = true,
    shellPath = '/bin/bash',
    env = {},
    timeoutMs = 15 * 60 * 1000,
  } = opts;

  const { codingAgent, piAi } = await loadPiModules();

  // --- 模型注册 ---
  // 关键：用 InMemory 存储，完全隔离用户的 Pi 配置目录。
  const credentials = new piAi.InMemoryCredentialStore();
  const modelsStore = new piAi.InMemoryModelsStore();
  const modelRuntime = await codingAgent.ModelRuntime.create({
    credentials,
    modelsStore,
    modelsPath: null,
    allowModelNetwork: false,
  });

  modelRuntime.registerProvider('yibiao', {
    name: 'Yibiao AI',
    baseUrl: provider.baseUrl,
    api: 'openai-completions',
    models: [
      {
        id: provider.model || 'default',
        name: 'Yibiao Current Text Model',
        reasoning: false,
        input: ['text'],
        contextWindow: normalizeContextLimit(modelConfig.contextLengthLimit),
        maxTokens: normalizeOutputLimit(modelConfig.contextLengthLimit),
        // 计费由 Go 侧统计，这里置 0 避免 SDK 内部做成本计算
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        compat: {
          supportsDeveloperRole: false,
          supportsReasoningEffort: false,
          supportsUsageInStreaming: false,
          maxTokensField: 'max_tokens',
        },
      },
    ],
  });

  await modelRuntime.setRuntimeApiKey('yibiao', provider.apiKey);
  const model = modelRuntime.getModel('yibiao', provider.model || 'default');
  if (!model) throw new Error('Pi Agent 模型注册失败');

  // --- Settings ---
  // retry: 由 Pi 会话级重试承担；代理层不重试
  // compaction: 长文本任务必需，否则上下文很快撑爆
  // images.blockImages: 标书正文不传图给模型，省 token
  const settingsManager = codingAgent.SettingsManager.inMemory(
    {
      defaultProvider: 'yibiao',
      defaultModel: provider.model || 'default',
      defaultThinkingLevel: 'off',
      // 不信任项目本地配置，避免注入
      defaultProjectTrust: 'never',
      retry: { enabled: true, provider: { maxRetries: 0, timeoutMs } },
      compaction: { enabled: true },
      images: { autoResize: false, blockImages: true },
      enableInstallTelemetry: false,
      enableAnalytics: false,
      shellPath,
      httpIdleTimeoutMs: timeoutMs,
    },
    { projectTrusted: false },
  );

  // --- ResourceLoader ---
  // 关闭全部外部资源加载：技能、扩展、提示词模板、主题。
  // 只保留我们显式注入的工作区约定，这是隔离性的关键。
  const resourceLoader = new codingAgent.DefaultResourceLoader({
    cwd: workspaceDir,
    agentDir: opts.agentDir,
    settingsManager,
    extensionFactories: [],
    noContextFiles: true,
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    agentsFilesOverride: () => ({
      agentsFiles: [
        {
          path: '<yibiao-agent-workspace>',
          content: workspaceInstructions,
        },
      ],
    }),
    systemPromptOverride: () => undefined,
    appendSystemPromptOverride: () => (summaryEnabled === false ? [PI_NO_SUMMARY_INSTRUCTIONS] : []),
  });
  await resourceLoader.reload();

  // --- 工具 ---
  const bashTool = codingAgent.createBashToolDefinition(workspaceDir, {
    shellPath,
    spawnHook: ({ command, cwd, env: baseEnv }) => ({
      command,
      cwd,
      env: { ...baseEnv, ...env },
    }),
  });

  const nativeToolDefs = summaryEnabled === false
    ? [
        codingAgent.createReadToolDefinition(workspaceDir),
        codingAgent.createEditToolDefinition(workspaceDir),
        codingAgent.createWriteToolDefinition(workspaceDir),
        codingAgent.createFindToolDefinition(workspaceDir),
        codingAgent.createLsToolDefinition(workspaceDir),
        codingAgent.createGrepToolDefinition(workspaceDir),
      ]
    : [];

  // 业务工具：Go 侧传工具声明，这里生成 HTTP 回调工具。
  //
  // 与桌面版的差异：桌面版业务工具直接调业务服务（强耦合），
  // Web 版通过 HTTP 回调，业务逻辑全在 Go。
  // 会话标识在 index.mjs 里生成，这里用可变对象承载，
  // 工具回调时读最新的值（闭包捕获的是对象引用）
  const toolCtx = {
    Type,
    internalToken: opts.internalToken || '',
    timeoutMs: opts.toolTimeoutMs || 120000,
    sessionId: opts.sessionId || '',
    taskId: opts.taskId || '',
  };
  const callbackToolDefs = createCallbackTools(businessTools, toolCtx);
  const wrappedBusinessTools = callbackToolDefs.map((t) => codingAgent.defineTool(t));

  // ask_user：Agent 遇到需要用户决策时提问。
  //
  // 与其他业务工具不同，这个是 sidecar 本地实现（不走 HTTP 回调），
  // 因为它需要挂起当前 Agent 回合等用户回答。
  // emit 在 session 创建时还没有（runner 拿到 session 后才会有事件通道），
  // 用可变对象承载，工具执行时读最新值。
  const askUserCtx = { emit: opts.emit, sessionId: opts.sessionId };
  const askUserTool = codingAgent.defineTool(createAskUserTool(askUserCtx));
  wrappedBusinessTools.push(askUserTool);

  // 一致性检查：activeTools 里声明了但没定义的工具会导致
  // Agent 以为可用、实际调用失败。提前报出来便于排查。
  if (Array.isArray(activeTools) && activeTools.length) {
    const defined = new Set(wrappedBusinessTools.map((t) => t.name));
    const missing = activeTools.filter(
      (n) => !NATIVE_AGENT_TOOLS.includes(n) && !defined.has(n),
    );
    if (missing.length) {
      console.warn(
        `[session] activeTools 里这些工具未定义，Agent 会调用失败: ${missing.join(', ')}`,
      );
    }
  }

  const customTools = [bashTool, ...nativeToolDefs, ...wrappedBusinessTools];

  const defaultToolNames = [...NATIVE_AGENT_TOOLS, ...wrappedBusinessTools.map((t) => t.name)];

  // --- Session ---
  const sessionManager = sessionFile
    ? codingAgent.SessionManager.open(sessionFile, sessionsDir, workspaceDir)
    : sessionsDir
      ? codingAgent.SessionManager.create(workspaceDir, sessionsDir)
      : codingAgent.SessionManager.inMemory(workspaceDir);

  // 阶段门禁：当前阶段不允许的工具，在调用前直接拦下
  let requestedTools = null;
  const withNative = (names) => [...new Set([...NATIVE_AGENT_TOOLS, ...names])];

  const { session } = await codingAgent.createAgentSession({
    cwd: workspaceDir,
    agentDir: opts.agentDir,
    model,
    modelRuntime,
    thinkingLevel: 'off',
    tools: [...new Set([...defaultToolNames, ...(activeTools ? withNative(activeTools) : [])])],
    customTools,
    resourceLoader,
    settingsManager,
    sessionManager,
  });

  requestedTools = withNative(activeTools || defaultToolNames);

  // ask_user 是 sidecar 内置工具，任何阶段都允许调用。
  //
  // 为什么必须显式加入：Go 传来的 ActiveTools 是业务白名单，
  // 只包含原生工具和业务工具，不含 sidecar 自己的工具。
  // 不加这个，Agent 就看不到 ask_user，也就无法向用户提问。
  if (!requestedTools.includes('ask_user')) {
    requestedTools.push('ask_user');
  }

  if (fixedToolList) {
    // 固定清单模式：不让 SDK 切换 tools（保持请求前缀缓存），
    // 改用 beforeToolCall 门禁。
    session.setActiveToolsByName(defaultToolNames);
    session.agent.beforeToolCall = async (context, signal) => {
      if (!requestedTools.includes(context.toolCall.name)) {
        throw new Error(
          `当前阶段不能调用 ${context.toolCall.name}，请只使用当前阶段说明的工具。`,
        );
      }
      return undefined;
    };
  } else {
    session.setActiveToolsByName(requestedTools);
  }

  return {
    session,
    sessionFile: session.sessionFile || sessionManager.getSessionFile() || '',
    /** 设置会话标识，供工具回调使用。由 index.mjs 在拿到 sessionId 后调用。 */
    setIdentity(sessionId, taskId) {
      toolCtx.sessionId = sessionId;
      toolCtx.taskId = taskId;
      // ask_user 工具也需要 sessionId
      askUserCtx.sessionId = sessionId;
    },

    /** 设置事件发射器（runner 拿到后调用）。 */
    setEmit(emitFn) {
      askUserCtx.emit = emitFn;
    },
    /** 切换阶段工具。fixedToolList 模式下只更新门禁。 */
    setActiveTools(toolNames) {
      requestedTools = withNative(toolNames);
      if (!fixedToolList) session.setActiveToolsByName(requestedTools);
    },
    snapshot: {
      sdkVersion: codingAgent.VERSION || '',
      model: {
        provider: model.provider || '',
        id: model.id || '',
        api: model.api || '',
        baseUrl: model.baseUrl || '',
        contextWindow: Number(model.contextWindow || 0),
        maxTokens: Number(model.maxTokens || 0),
      },
      activeTools: fixedToolList ? [...requestedTools] : session.getActiveToolNames(),
    },
  };
}

/**
 * 关闭总结时注入的指令。
 *
 * 移植自桌面版 piSummaryControl.cjs。作用是让 Agent 不要输出
 * "任务已完成" 这类总结性文字，直接以工具调用的结果结束，
 * 省 token 且让程序更易判断结束时机。
 */
export const PI_NO_SUMMARY_INSTRUCTIONS = [
  '## 结束要求',
  '完成任务后不要输出总结、说明或客套话。',
  '直接结束当前回合，程序会根据产物文件判断是否完成。',
].join('\n');
