/**
 * Agent Sidecar HTTP 服务。
 *
 * 只做一件事：把 Pi SDK 的 Agent 能力暴露成 HTTP 接口，供 Go 主服务调用。
 * 不持有业务状态，不连数据库，不做鉴权（只在回环地址监听）。
 *
 * 接口设计原则：
 *   - 用通用 Agent 语义，不暴露 Pi 特有的概念（task_complete / fixed_tool_list 等）
 *   - 万一将来换掉 Pi SDK，Go 侧不需要改
 *   - 长任务用 session + SSE 事件流，不用长轮询
 */

import { createServer } from 'node:http';
import { answerQuestion, listPendingQuestions, clearSessionQuestions } from './askuser.mjs';
import { randomUUID } from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';

import { createPiSession } from './session.mjs';
import { runTask } from './runner.mjs';

const PORT = Number(process.env.AGENT_PORT || 7070);
const HOST = process.env.AGENT_HOST || '127.0.0.1';
const ROOT = process.env.AGENT_WORKSPACE_ROOT || '/workspace/data/agent';
// 内部令牌：Go 与 sidecar 之间用，防止本机其他进程误调
const INTERNAL_TOKEN = process.env.AGENT_INTERNAL_TOKEN || '';

/** 活跃会话表：sessionId → { session, controller, events, task } */
const sessions = new Map();

// ---------- HTTP 工具 ----------

function json(res, status, body) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(payload),
  });
  res.end(payload);
}

function fail(res, status, code, message, detail) {
  json(res, status, { ok: false, error: { code, message, detail } });
}

function ok(res, data) {
  json(res, 200, { ok: true, data });
}

async function readBody(req, limit = 32 * 1024 * 1024) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new Error('请求体过大');
    chunks.push(chunk);
  }
  if (!chunks.length) return {};
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function authorized(req) {
  if (!INTERNAL_TOKEN) return true; // 未配置时不校验（仅开发）
  const h = req.headers.authorization || '';
  return h === `Bearer ${INTERNAL_TOKEN}`;
}

function log(...args) {
  console.log(`[agent]`, ...args);
}

// ---------- 路由 ----------

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://${HOST}`);

  try {
    // 健康检查不需要令牌
    if (url.pathname === '/health') {
      return json(res, 200, {
        status: 'ok',
        sessions: sessions.size,
        pid: process.pid,
        uptime: Math.round(process.uptime()),
      });
    }

    if (!authorized(req)) {
      return fail(res, 401, 'UNAUTHORIZED', '缺少或错误的内部令牌');
    }

    // POST /sessions —— 创建会话（不启动任务）
    if (req.method === 'POST' && url.pathname === '/sessions') {
      return await handleCreateSession(req, res);
    }

    // POST /sessions/:id/run —— 启动任务（带阶段定义）
    const runMatch = url.pathname.match(/^\/sessions\/([^/]+)\/run$/);
    if (req.method === 'POST' && runMatch) {
      return await handleRun(req, res, runMatch[1]);
    }

    // GET /sessions/:id/events —— SSE 事件流
    const eventsMatch = url.pathname.match(/^\/sessions\/([^/]+)\/events$/);
    if (req.method === 'GET' && eventsMatch) {
      return await handleEvents(req, res, eventsMatch[1]);
    }

    // POST /sessions/:id/cancel —— 取消
    const cancelMatch = url.pathname.match(/^\/sessions\/([^/]+)\/cancel$/);
    if (req.method === 'POST' && cancelMatch) {
      return await handleCancel(res, cancelMatch[1]);
    }

    // DELETE /sessions/:id —— 销毁会话
    const delMatch = url.pathname.match(/^\/sessions\/([^/]+)$/);
    if (req.method === 'DELETE' && delMatch) {
      return await handleDelete(res, delMatch[1]);
    }

    // POST /sessions/:id/answer —— 回答 Agent 提问
    // 待答问题列表（前端重连后恢复用）
    const pendingMatch = url.pathname.match(/^\/sessions\/([^/]+)\/questions$/);
    if (req.method === 'GET' && pendingMatch) {
      return ok(res, { questions: listPendingQuestions(pendingMatch[1]) });
    }

    const answerMatch = url.pathname.match(/^\/sessions\/([^/]+)\/answer$/);
    if (req.method === 'POST' && answerMatch) {
      return await handleAnswer(req, res, answerMatch[1]);
    }

    // GET /sessions —— 列出活跃会话
    if (req.method === 'GET' && url.pathname === '/sessions') {
      return ok(res, {
        sessions: [...sessions.entries()].map(([id, s]) => ({
          sessionId: id,
          running: Boolean(s.task),
          eventCount: s.events.length,
          createdAt: s.createdAt,
        })),
      });
    }

    return fail(res, 404, 'NOT_FOUND', `未实现的接口: ${req.method} ${url.pathname}`);
  } catch (error) {
    log('请求处理异常:', error?.message);
    if (!res.headersSent) {
      return fail(res, 500, 'INTERNAL', error?.message || '内部错误');
    }
    try {
      res.end();
    } catch {
      /* 忽略 */
    }
  }
});

// ---------- Handler 实现 ----------

async function handleCreateSession(req, res) {
  const body = await readBody(req);

  const {
    workspaceDir,
    sessionsDir,
    sessionFile,
    provider,
    modelConfig,
    workspaceInstructions,
    activeTools,
    customTools,
    fixedToolList,
    summaryEnabled,
    agentDir,
  } = body || {};

  if (!workspaceDir) return fail(res, 400, 'INVALID_ARGUMENT', '缺少 workspaceDir');
  if (!provider?.baseUrl) return fail(res, 400, 'INVALID_ARGUMENT', '缺少 provider.baseUrl');
  if (!provider?.apiKey) return fail(res, 400, 'INVALID_ARGUMENT', '缺少 provider.apiKey');

  // 工作目录必须在白名单根目录下，防路径穿越
  const resolved = path.resolve(workspaceDir);
  const rootResolved = path.resolve(ROOT);
  if (!resolved.startsWith(rootResolved + path.sep) && resolved !== rootResolved) {
    return fail(res, 403, 'FORBIDDEN', `工作目录必须在 ${rootResolved} 之下`);
  }

  const sessionId = randomUUID();
  await fs.mkdir(resolved, { recursive: true });

  // 诊断：确认 Go 是否传了工具声明
  const toolCount = Array.isArray(customTools) ? customTools.length : 0;
  log(`[create] 会话 ${sessionId} 收到 ${toolCount} 个自定义工具${toolCount ? ': ' + customTools.map(t => t?.name).join(',') : '（未传！）'}`);

  // agentDir 必须存在：Pi SDK 内部会对它做路径处理，undefined 会崩。
  // 每个会话独立一个，避免互相读取对方的状态。
  const resolvedAgentDir = agentDir
    ? path.resolve(agentDir)
    : path.join(ROOT, '_agents', sessionId);
  await fs.mkdir(resolvedAgentDir, { recursive: true });

  try {
    const created = await createPiSession({
      workspaceDir: resolved,
      sessionsDir: sessionsDir ? path.resolve(sessionsDir) : undefined,
      sessionFile,
      provider,
      modelConfig,
      workspaceInstructions,
      activeTools,
      customTools,
      fixedToolList,
      summaryEnabled,
      agentDir: resolvedAgentDir,
      // 工具回调需要令牌，与 Go 侧校验一致
      internalToken: INTERNAL_TOKEN,
    });

    sessions.set(sessionId, {
      session: created,
      controller: null,
      events: [],
      task: null,
      createdAt: new Date().toISOString(),
      workspaceDir: resolved,
    });

    // 回填会话标识：工具回调需要它来反查上下文
    if (typeof created.setIdentity === 'function') {
      created.setIdentity(sessionId, '');
    }

    log(`会话已创建 ${sessionId} @ ${resolved}`);
    log(`  实际注册工具: ${(created.snapshot?.activeTools || []).join(',')}`);
    return ok(res, { sessionId, snapshot: created.snapshot });
  } catch (error) {
    log('创建会话失败:', error?.message);
    return fail(res, 500, 'SESSION_CREATE_FAILED', error?.message || '创建会话失败');
  }
}

/**
 * 启动任务。
 *
 * 请求体：
 * {
 *   taskId: string,
 *   stages: [
 *     {
 *       id: string,
 *       prompt: string,
 *       tools?: string[],          // 本阶段启用的工具名
 *       maxRetries?: number,       // 执行异常重试上限（默认 3）
 *       outputFile?: string,
 *       // 校验钩子由 Go 侧实现，这里通过 HTTP 回调（见 validateUrl）
 *       validateUrl?: string,
 *       buildRepairPromptUrl?: string,
 *     }
 *   ],
 *   timeoutMs?: number
 * }
 *
 * 校验回调机制：sidecar 不实现业务校验逻辑（那是 Go 的事），
 * 每次 Agent 结束一轮就 POST 到 Go 的 validateUrl，拿到 { issues, value, progress, minimumGoal }。
 * 这样业务规则留在 Go，sidecar 保持通用。
 */
async function handleRun(req, res, sessionId) {
  const entry = sessions.get(sessionId);
  if (!entry) return fail(res, 404, 'SESSION_NOT_FOUND', '会话不存在');
  if (entry.task) return fail(res, 409, 'TASK_RUNNING', '该会话已有任务在运行');

  const body = await readBody(req);
  const { taskId = randomUUID(), stages, timeoutMs } = body || {};

  if (!Array.isArray(stages) || stages.length === 0) {
    return fail(res, 400, 'INVALID_ARGUMENT', 'stages 不能为空');
  }

  const controller = new AbortController();
  entry.controller = controller;
  entry.events = [];
  entry.listeners ??= new Set();

  // 更新 taskId：工具回调要带它，Go 侧据此反查任务
  if (typeof entry.session.setIdentity === 'function') {
    entry.session.setIdentity(sessionId, taskId);
  }

  // 把 HTTP 回调形式的校验包装成 runTask 需要的函数
  const wrappedStages = stages.map((stage) => ({
    ...stage,
    validate: stage.validateUrl
      ? async (ctx) => {
          const resp = await fetch(stage.validateUrl, {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
              ...(INTERNAL_TOKEN ? { Authorization: `Bearer ${INTERNAL_TOKEN}` } : {}),
            },
            body: JSON.stringify({ ...ctx, taskId, sessionId }),
          });
          if (!resp.ok) {
            throw new Error(`校验回调失败: HTTP ${resp.status}`);
          }
          const payload = await resp.json();
          if (!payload.ok) throw new Error(payload.error?.message || '校验回调返回失败');
          return payload.data;
        }
      : undefined,
    buildRepairPrompt: stage.buildRepairPromptUrl
      ? async (report, decision) => {
          const resp = await fetch(stage.buildRepairPromptUrl, {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
              ...(INTERNAL_TOKEN ? { Authorization: `Bearer ${INTERNAL_TOKEN}` } : {}),
            },
            body: JSON.stringify({ report, decision, taskId, sessionId }),
          });
          if (!resp.ok) return '';
          const payload = await resp.json();
          return payload.ok ? payload.data?.prompt || '' : '';
        }
      : undefined,
  }));

  // 异步执行，立即返回
  entry.task = runTask({
    session: entry.session,
    stages: wrappedStages,
    taskId,
    // 产物校验要相对任务工作区，不能相对 sidecar 进程的 cwd
    workspaceDir: entry.workspaceDir,
    signal: controller.signal,
    timeoutMs,
    onEvent: (ev) => {
      entry.events.push(ev);
      // 限制内存里保留的事件数，避免长任务把内存吃满
      if (entry.events.length > 2000) entry.events.splice(0, 500);
      entry.listeners?.forEach((fn) => fn(ev));
    },
  })
    .then((result) => {
      log(`任务完成 ${taskId}`);
      return result;
    })
    .catch((error) => {
      log(`任务失败 ${taskId}: ${error?.message}`);
      throw error;
    })
    .finally(() => {
      entry.task = null;
    });

  // 避免未处理拒绝告警（客户端通过事件流知道结果）
  entry.task.catch(() => {});

  return ok(res, { sessionId, taskId, started: true });
}

async function handleEvents(req, res, sessionId) {
  const entry = sessions.get(sessionId);
  if (!entry) return fail(res, 404, 'SESSION_NOT_FOUND', '会话不存在');

  res.writeHead(200, {
    'Content-Type': 'text/event-stream; charset=utf-8',
    'Cache-Control': 'no-cache, no-transform',
    Connection: 'keep-alive',
    'X-Accel-Buffering': 'no',
  });

  // 先回放已有事件（客户端可能晚连）
  for (const ev of entry.events) {
    writeEvent(res, ev);
  }
  writeEvent(res, { type: 'ready', at: new Date().toISOString() });

  // 订阅后续事件
  const listener = (ev) => writeEvent(res, ev);
  entry.listeners ??= new Set();
  entry.listeners.add(listener);

  // 心跳保活
  const heartbeat = setInterval(() => {
    try {
      res.write(': ping\n\n');
    } catch {
      /* 连接已断 */
    }
  }, 15000);

  const cleanup = () => {
    clearInterval(heartbeat);
    entry.listeners?.delete(listener);
  };

  req.on('close', cleanup);
  req.on('error', cleanup);
}

function writeEvent(res, ev) {
  try {
    res.write(`event: ${ev.type}\ndata: ${JSON.stringify(ev)}\n\n`);
  } catch {
    /* 连接已断 */
  }
}

async function handleCancel(res, sessionId) {
  const entry = sessions.get(sessionId);
  if (!entry) return fail(res, 404, 'SESSION_NOT_FOUND', '会话不存在');

  entry.controller?.abort('用户取消');
  // 直接中止会话
  try {
    entry.session.session?.abort?.();
  } catch {
    /* 忽略 */
  }
  return ok(res, { canceled: true });
}

async function handleDelete(res, sessionId) {
  const entry = sessions.get(sessionId);
  if (!entry) return ok(res, { deleted: true });

  entry.controller?.abort('会话销毁');
  try {
    await entry.session.session?.dispose?.();
  } catch {
    /* 忽略 */
  }
  sessions.delete(sessionId);
    clearSessionQuestions(sessionId, '会话已销毁');
  log(`会话已销毁 ${sessionId}`);
  return ok(res, { deleted: true });
}

async function handleAnswer(req, res, sessionId) {
  const entry = sessions.get(sessionId);
  if (!entry) return fail(res, 404, 'SESSION_NOT_FOUND', '会话不存在');

  let body;
  try {
    body = await readJson(req);
  } catch {
    return fail(res, 400, 'INVALID_ARGUMENT', '请求体不是合法 JSON');
  }

  const questionId = String(body?.questionId || '');
  const answer = body?.answer;

  if (!questionId) {
    return fail(res, 400, 'INVALID_ARGUMENT', '缺少 questionId');
  }
  if (answer === undefined || answer === null) {
    return fail(res, 400, 'INVALID_ARGUMENT', '缺少 answer');
  }

  const result = answerQuestion(questionId, answer);
  if (!result.ok) {
    return fail(res, 404, 'QUESTION_NOT_FOUND', result.error || '问题不存在');
  }

  log(`已回答问题 ${questionId}`);
  return ok(res, { questionId, accepted: true });
}

// ---------- 启动 ----------

// 工具回调依赖全局 fetch（Node 18+）
if (typeof fetch !== 'function') {
  console.error('[agent] 需要 Node 18+ 提供全局 fetch，当前环境不支持');
  process.exit(1);
}

server.listen(PORT, HOST, () => {
  log(`Agent Sidecar 已启动 http://${HOST}:${PORT}`);
  log(`工作区根目录: ${ROOT}`);
  log(`Node 版本: ${process.version}`);
  log(`内部令牌: ${INTERNAL_TOKEN ? '已配置' : '未配置（仅开发）'}`);
});

process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

async function shutdown() {
  log('收到退出信号，清理会话…');
  for (const [id, entry] of sessions) {
    try {
      entry.controller?.abort('进程退出');
      await entry.session.session?.dispose?.();
    } catch {
      /* 忽略 */
    }
    log(`已清理 ${id}`);
  }
  server.close(() => process.exit(0));
  // 兜底强制退出
  setTimeout(() => process.exit(0), 3000);
}
