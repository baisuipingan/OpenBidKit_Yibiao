/**
 * 任务运行器：驱动一个 Agent 任务从开始到结束。
 *
 * 移植自桌面版 piRuntimeService.cjs 的核心循环，简化点：
 *   - 去掉 Electron 相关的窗口、监控、持久化（那些由 Go 侧负责）
 *   - 保留：会话订阅、阶段推进、提交校验、重试、取消
 *
 * 关键设计（不能简化）：
 *   1. 每个阶段单独跑，阶段间可切换工具集
 *   2. 阶段内循环：prompt → 等 Agent 结束 → 校验产物 → 决定接受/修复/停止
 *   3. 执行异常与提交问题分开计费，前者限次，后者按进展策略
 *   4. 全程可取消，取消后不再 checkpoint
 */

import path from 'node:path';
import { createSubmissionPolicy, buildSubmissionRepairPrompt, buildExecutionRetryPrompt } from './submission.mjs';
import { createStallDetector, buildStallNudge } from './stall.mjs';

const DEFAULT_TIMEOUT_MS = 15 * 60 * 1000;
/**
 * 无活动超时：多久没有产生任何事件就判定卡死。
 *
 * 为什么需要这个：
 *   整体超时（15 分钟）太粗——任务可能正常跑了 12 分钟然后卡死，
 *   再等 3 分钟才报错，用户白等。
 *   而「长时间没有任何事件」本身就是卡死的强信号。
 */
const DEFAULT_IDLE_TIMEOUT_MS = 3 * 60 * 1000;

/**
 * 运行一个任务。
 *
 * @param {object} opts
 * @param {object} opts.session       createPiSession 返回的对象
 * @param {Array}  opts.stages        阶段定义 [{ id, prompt, tools?, validate? }]
 * @param {string} [opts.taskId]
 * @param {AbortSignal} [opts.signal]  取消信号
 * @param {(event: object) => void} [opts.onEvent]  事件回调（推给 Go）
 */
export async function runTask(opts) {
  const {
    session,
    stages,
    taskId = '',
    signal,
    onEvent = () => {},
    timeoutMs = DEFAULT_TIMEOUT_MS,
    idleTimeoutMs = DEFAULT_IDLE_TIMEOUT_MS,
    // 任务工作区。产物校验要相对这个目录，
    // 不能相对 sidecar 进程的 cwd —— 两者不是同一个目录。
    workspaceDir = '',
  } = opts;

  if (!Array.isArray(stages) || stages.length === 0) {
    throw new Error('任务至少需要一个阶段');
  }

  const state = {
    taskId,
    status: 'running',
    stageIndex: 0,
    stageId: stages[0].id || '',
    retryCount: 0,
    modelRetryCount: 0,
    assistantText: '',
    acceptedIssues: [],
    validationResult: null,
    startedAt: new Date().toISOString(),
  };

  const emit = (type, payload = {}) => {
    try {
      onEvent({ type, at: new Date().toISOString(), taskId, ...payload });
    } catch (e) {
      // 事件回调失败不能影响任务
      console.error('[runner] 事件回调异常', e?.message);
    }
  };

  const throwIfAborted = () => {
    if (signal?.aborted) {
      const reason = signal.reason;
      const err = new Error(typeof reason === 'string' ? reason : reason?.message || '任务已取消');
      err.aborted = true;
      throw err;
    }
  };

  // --- 订阅会话事件 ---
  let streamedText = '';

  // 活动时间戳：任何会话事件都更新，供空闲超时监控读取。
  // 用对象承载是因为要在闭包里共享可变状态。
  const activity = { at: Date.now() };

  const unsubscribe = session.session.subscribe((event) => {
    // 收到任何事件就刷新活动时间
    activity.at = Date.now();

    switch (event.type) {
      case 'message_start':
        if (event.message?.role === 'assistant') streamedText = '';
        break;

      case 'message_update':
        if (event.assistantMessageEvent?.type === 'text_delta') {
          const delta = event.assistantMessageEvent.delta || '';
          streamedText += delta;
          emit('assistant_delta', { delta });
        }
        break;

      case 'message_end':
        if (event.message?.role === 'assistant') {
          const text = extractMessageText(event.message) || streamedText.trim();
          streamedText = '';
          state.assistantText = text;
          emit('assistant_end', { text });
        }
        break;

      case 'tool_execution_start': {
        const args = normalizeValue(event.args);

        emit('tool_start', {
          toolCallId: event.toolCallId || '',
          toolName: event.toolName || '',
          args,
        });

        // 空转检测：记录这次调用，判断是否在原地打转
        if (stallDetector) {
          const verdict = stallDetector.record({ tool: event.toolName || '', args });
          if (verdict.stalled && verdict.shouldNudge) {
            const nudgeCount = stallDetector.markNudged();
            if (nudgeCount <= 2) {
              stallMessages.push(
                buildStallNudge(verdict.reason, verdict.nudgeLevel, state.stage),
              );
              emit('stall_detected', {
                reason: verdict.reason,
                level: verdict.nudgeLevel,
                nudgeCount,
                toolName: event.toolName || '',
              });
            }
          }
        }
        break;
      }

      case 'tool_execution_end':
        emit('tool_end', {
          toolCallId: event.toolCallId || '',
          toolName: event.toolName || '',
          result: normalizeValue(event.result),
          isError: Boolean(event.isError),
        });
        break;

      case 'auto_retry_start':
        state.modelRetryCount += 1;
        emit('model_retry_start', {
          attempt: event.attempt,
          maximum: event.maxAttempts,
          delayMs: event.delayMs,
          message: String(event.errorMessage || '').slice(0, 300),
          count: state.modelRetryCount,
        });
        break;

      case 'auto_retry_end':
        emit('model_retry_end', {
          attempt: event.attempt,
          success: Boolean(event.success),
          message: String(event.finalError || '').slice(0, 300),
          count: state.modelRetryCount,
        });
        break;

      case 'agent_start':
      case 'agent_end':
      case 'agent_settled':
      case 'turn_start':
      case 'turn_end':
      case 'compaction_start':
      case 'compaction_end':
        emit(event.type, {});
        break;

      default:
        break;
    }
  });

  // --- 取消绑定 ---
  const onAbort = () => {
    try {
      session.session?.abortCompaction?.();
    } catch {
      /* 忽略 */
    }
    void session.session?.abort?.().catch(() => {});
  };
  signal?.addEventListener('abort', onAbort, { once: true });

  let completed = false;

  // 把 emit 注入 session，供 ask_user 工具推送问题给前端
  if (typeof session.setEmit === 'function') {
    session.setEmit(emit);
  }

  // 空转检测器：跨阶段复用（同一任务内的打转行为要累计）
  const stallDetector = createStallDetector();
  // （活动时间戳在订阅处已声明为 activity 对象）
  // 待注入的空转提示（下一轮 prompt 时带上）
  const stallMessages = [];

  try {
    // --- 阶段循环 ---
    for (let stageIndex = 0; stageIndex < stages.length; stageIndex += 1) {
      throwIfAborted();

      const stage = stages[stageIndex];
      state.stageIndex = stageIndex;
      state.stageId = stage.id || `stage-${stageIndex}`;

      // 切换本阶段工具集
      if (Array.isArray(stage.tools) && stage.tools.length) {
        session.setActiveTools(stage.tools);
      }

      emit('stage_start', {
        stageId: state.stageId,
        stageIndex,
        stageCount: stages.length,
      });

      // 本阶段的提交策略实例（阶段间重置）
      const policy = createSubmissionPolicy();

      // 新阶段：重置本轮空转计数（保留跨阶段的 nudge 次数）
      stallDetector.startRound();

      let stagePrompt = stage.prompt;

      /** 把待注入的空转提示拼到 prompt 前面 */
      const applyStallNudges = (base) => {
        if (!stallMessages.length) return base;
        const prefix = stallMessages.join('\n\n');
        stallMessages.length = 0;
        return `${prefix}\n\n---\n\n${base}`;
      };
      let executionRetries = 0;
      const maxExecutionRetries = stage.maxRetries ?? 3;
      let stageDone = false;

      // 阶段内循环
      while (!stageDone) {
        throwIfAborted();

        let report;
        try {
          emit('prompt_start', {
            stageId: state.stageId,
            attempt: executionRetries,
            promptLength: stagePrompt.length,
          });

          // 关键：这一步会阻塞直到 Agent 结束本回合
          // 每轮开始时重置本轮的重复计数
          stallDetector.startRound();

          // 应用待注入的空转提示
          const effectivePrompt = applyStallNudges(stagePrompt);

          // 带空闲监控的执行：
          // 只要有事件（工具调用、文本输出）就重置计时。
          // 长时间没有任何事件 → 判定卡死，抛错让上层重试。
          await withIdleTimeout(
            session.session.prompt(effectivePrompt, { expandPromptTemplates: false }),
            { idleMs: idleTimeoutMs, totalMs: timeoutMs },
            () => activity.at,
            () => {
              emit('idle_timeout', {
                stageId: state.stageId,
                idleMs: idleTimeoutMs,
                lastTool: stallDetector.lastRecordedTool || '',
              });
            },
          );

          throwIfAborted();

          // 检查助手消息里是否有错误
          const assistantError = getAssistantError(session.session.messages);
          if (assistantError) {
            const err = new Error(assistantError);
            err.assistantError = true;
            throw err;
          }

          // 校验产物。
          // 即使业务没提供 validate，也要做最基本的「产物文件是否存在」检查，
          // 否则模型什么都没干也会被判成功。
          if (typeof stage.validate === 'function') {
            report = await stage.validate({
              stageId: state.stageId,
              assistantText: state.assistantText,
              attempt: executionRetries,
              taskId,
            });
          } else if (stage.outputFile) {
            // 相对路径要基于任务工作区解析。
            //
            // 为什么：Agent 在 workspaceDir 下写文件（SDK 的 cwd 是 workspaceDir），
            // 但 sidecar 进程自己的 cwd 是 sidecars/agent。
            // 直接用相对路径检查会永远找不到 —— 任务明明成功了却判失败。
            const outputPath = path.isAbsolute(stage.outputFile)
              ? stage.outputFile
              : path.join(workspaceDir || process.cwd(), stage.outputFile);
            const exists = await fileExists(outputPath);
            report = exists
              ? { value: null, issues: [] }
              : {
                  value: null,
                  issues: [
                    {
                      severity: 'blocking',
                      message: `产物文件 ${stage.outputFile} 未生成，请完成任务要求。`,
                    },
                  ],
                };
          } else {
            report = { value: null, issues: [] };
          }
        } catch (error) {
          if (error.aborted || signal?.aborted) {
            throwIfAborted();
            throw error;
          }

          // 执行异常：有次数上限
          if (executionRetries >= maxExecutionRetries) {
            emit('stage_failed', { stageId: state.stageId, error: String(error?.message || error) });
            throw error;
          }
          executionRetries += 1;

          stagePrompt = buildExecutionRetryPrompt(
            stage.outputFile || '',
            error,
            executionRetries,
            maxExecutionRetries,
          );
          emit('execution_retry', {
            stageId: state.stageId,
            attempt: executionRetries,
            maximum: maxExecutionRetries,
            error: String(error?.message || error).slice(0, 500),
          });
          continue;
        }

        // 提交策略判断：接受 / 修复 / 停止
        const decision = policy.inspect(report);

        if (decision.action === 'stop') {
          const blocking = report.issues.filter((i) => i.severity === 'blocking');
          const err = new Error(
            `已按最低完成目标再修复一轮，仍存在阻塞流程的问题，任务已停止：\n${blocking
              .map((i) => i.message)
              .join('\n')}`,
          );
          err.validationFailed = true;
          err.issues = report.issues;
          emit('stage_blocked', {
            stageId: state.stageId,
            issues: report.issues,
          });
          throw err;
        }

        if (decision.action === 'accept') {
          state.validationResult = report.value;
          if (report.issues.length) {
            state.acceptedIssues.push({ stage: state.stageId, issues: report.issues });
            emit('issues_accepted', {
              stageId: state.stageId,
              count: report.issues.length,
              issues: report.issues,
            });
          }
          stageDone = true;
          break;
        }

        // action === 'repair'
        state.retryCount += 1;
        stagePrompt = buildSubmissionRepairPrompt(
          report,
          decision,
          typeof stage.buildRepairPrompt === 'function'
            ? await stage.buildRepairPrompt(report, decision)
            : '',
        );
        emit('submission_retry', {
          stageId: state.stageId,
          mode: decision.mode,
          attempt: decision.attempt,
          issues: report.issues,
        });
      }

      emit('stage_end', {
        stageId: state.stageId,
        stageIndex,
        retries: policy.attempts,
      });
    }

    // 全部阶段完成
    state.status = 'success';
    completed = true;
    emit('task_end', {
      status: 'success',
      retryCount: state.retryCount,
      modelRetryCount: state.modelRetryCount,
      acceptedIssues: state.acceptedIssues,
    });

    return {
      success: true,
      taskId,
      validationResult: state.validationResult,
      assistantText: state.assistantText,
      retryCount: state.retryCount,
      modelRetryCount: state.modelRetryCount,
      acceptedIssues: state.acceptedIssues,
    };
  } catch (error) {
    state.status = error.aborted ? 'canceled' : 'error';
    emit('task_end', {
      status: state.status,
      error: String(error?.message || error),
      retryCount: state.retryCount,
    });
    throw error;
  } finally {
    signal?.removeEventListener('abort', onAbort);
    try {
      unsubscribe?.();
    } catch {
      /* 忽略 */
    }
    if (!completed) {
      // 未正常完成时释放会话资源
      try {
        await session.session?.dispose?.();
      } catch {
        /* 忽略 */
      }
    }
  }
}

// ---------- 工具函数 ----------

function extractMessageText(message) {
  const content = message?.content;
  if (!content) return '';
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return content
      .map((part) => (typeof part === 'string' ? part : part?.text || ''))
      .filter(Boolean)
      .join('');
  }
  return '';
}

/**
 * 检测助手消息里的错误。
 *
 * 除了显式 error 字段，还要检查「这一轮什么都没做」的情况：
 * 模型返回空内容、没有工具调用、也没有文本，说明请求实际失败了
 * （无效 API key、网络中断等），必须当成错误，否则会误判任务成功。
 */
function getAssistantError(messages = []) {
  // 显式错误优先
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const m = messages[i];
    if (m?.role === 'assistant' && m?.error) {
      return typeof m.error === 'string' ? m.error : m.error.message || '模型返回错误';
    }
  }

  // 检查最后一条助手消息是否「空回合」
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const m = messages[i];
    if (m?.role !== 'assistant') continue;

    const text = extractMessageText(m).trim();
    const toolCalls = Array.isArray(m?.tool_calls) ? m.tool_calls : [];
    const contentParts = Array.isArray(m?.content) ? m.content : [];
    const hasToolUse = toolCalls.length > 0 || contentParts.some((p) => p?.type === 'tool_use' || p?.type === 'tool-call');

    // 本轮既没文本也没工具调用 = 模型实际没工作
    if (!text && !hasToolUse) {
      return '模型返回了空响应（可能是 API Key 无效、额度不足或服务不可用）';
    }
    break;
  }

  return '';
}

/** 简单超时包装。超时视为执行异常，走重试预算。 */
/**
 * 带空闲监控的执行。
 *
 * 两个超时同时生效：
 *   - idleMs：多久没有任何活动就判定卡死（主要机制）
 *   - totalMs：整体上限（兜底）
 *
 * 为什么这样设计：
 *   任务正常跑 12 分钟后卡死，用整体超时要再等 3 分钟。
 *   用空闲超时，卡死后 3 分钟就报错，用户可以立刻重试。
 */
function withIdleTimeout(promise, { idleMs, totalMs }, getLastActivity, onIdle) {
  return new Promise((resolve, reject) => {
    const start = Date.now();
    let settled = false;

    const finish = (fn, arg) => {
      if (settled) return;
      settled = true;
      clearInterval(ticker);
      fn(arg);
    };

    const ticker = setInterval(() => {
      const now = Date.now();
      const idle = now - getLastActivity();
      const total = now - start;

      if (idle >= idleMs) {
        try { onIdle?.(); } catch { /* 忽略 */ }
        finish(
          reject,
          new Error(
            `Agent 已 ${Math.round(idle / 1000)} 秒没有任何活动，判定为卡死。` +
            `可能是模型无响应或任务陷入死循环。`,
          ),
        );
        return;
      }
      if (total >= totalMs) {
        finish(
          reject,
          new Error(`任务执行超过 ${Math.round(totalMs / 60000)} 分钟上限。`),
        );
      }
    }, 5000); // 5 秒检查一次，开销可忽略

    // 超时时主动中断会话，避免后台继续烧 token
    const origFinish = finish;
    const finishWithAbort = (fn, arg) => {
      if (settled) return;
      try {
        // 只有失败路径才中断
        if (fn === reject && promise?.abort) promise.abort();
      } catch { /* 忽略 */ }
      origFinish(fn, arg);
    };

    Promise.resolve(promise).then(
      (v) => finishWithAbort(resolve, v),
      (e) => finishWithAbort(reject, e),
    );
  });
}

function withTimeout(promise, ms) {
  if (!ms || ms <= 0) return promise;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error(`Agent 执行超时（${Math.round(ms / 1000)} 秒）`));
    }, ms);
    promise.then(
      (v) => {
        clearTimeout(timer);
        resolve(v);
      },
      (e) => {
        clearTimeout(timer);
        reject(e);
      },
    );
  });
}

/** 检查文件是否存在。 */
async function fileExists(filePath) {
  try {
    const fs = await import('node:fs/promises');
    await fs.access(filePath);
    return true;
  } catch {
    return false;
  }
}

/** 限制事件载荷大小，避免推给 Go 的数据过大。 */
function normalizeValue(value, depth = 0) {
  if (depth > 3) return '[truncated]';
  if (value === null || value === undefined) return value;
  if (typeof value === 'string') return value.length > 2000 ? value.slice(0, 2000) + '…' : value;
  if (typeof value !== 'object') return value;
  if (Array.isArray(value)) {
    return value.slice(0, 20).map((v) => normalizeValue(v, depth + 1));
  }
  const out = {};
  for (const [k, v] of Object.entries(value).slice(0, 20)) {
    out[k] = normalizeValue(v, depth + 1);
  }
  return out;
}
