/**
 * Agent 提问应答机制。
 *
 * 问题：Agent 在长任务中遇到需要用户决策的点（如「没有技术评分标准，
 * 是否按常规结构编制？」），目前只能自己猜或者停下。
 *
 * 桌面版的做法：Agent 输出特定格式的提问，主进程展示给用户，
 * 用户回答后把答案作为新消息注入会话。
 *
 * **Web 版的方案：用工具实现提问**
 *
 *   Agent 调 ask_user 工具 → 工具挂起等待
 *     → Go 侧把问题推给前端（SSE 事件）
 *     → 用户在前端回答 → POST /api/agent-tools/answer
 *     → Go 侧唤醒挂起的 Promise
 *     → 工具返回答案 → Agent 继续
 *
 * **为什么用工具而不是解析对话**：
 *   - 类型安全：问题结构明确（标题/选项/是否必答）
 *   - 阻塞语义清晰：Agent 知道自己在等，不会继续瞎做
 *   - 可超时：等待太久可以自动按默认值继续
 *   - 可并发：多个问题各自独立等待
 */

/** 待答问题：key 是 questionId */
const pendingQuestions = new Map();

/** 问题超时（默认 10 分钟） */
const QUESTION_TIMEOUT_MS = Number(process.env.QUESTION_TIMEOUT_MS || 10 * 60 * 1000);

/**
 * 创建 ask_user 工具的 handler。
 *
 * @param {object} ctx { emit, sessionId }
 */
export function createAskUserTool(ctx) {
  return {
    name: 'ask_user',
    description:
      '向用户提问并等待回答。当遇到需要用户决策的点（例如招标文件信息缺失、' +
      '有多种合理方案但影响重大、需要确认关键数据）时使用。' +
      '**不要**用它询问琐碎细节，能自行判断的不要问。',
    parameters: {
      type: 'object',
      properties: {
        question: {
          type: 'string',
          description: '要问的问题，要具体明确，让用户能直接回答',
        },
        options: {
          type: 'array',
          items: { type: 'string' },
          description: '可选项（可选）。提供选项能让用户更快回答',
        },
        defaultAnswer: {
          type: 'string',
          description: '默认答案（可选）。用户超时未答时使用这个值继续',
        },
        reason: {
          type: 'string',
          description: '为什么需要问（可选）。说明卡在哪里，帮助用户理解',
        },
      },
      required: ['question'],
    },

    execute: async (_toolCallId, params) => {
      const questionId = `q-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

      const question = {
        questionId,
        sessionId: ctx.sessionId || '',
        question: String(params?.question || ''),
        options: Array.isArray(params?.options) ? params.options.map(String) : [],
        defaultAnswer: params?.defaultAnswer ? String(params.defaultAnswer) : '',
        reason: params?.reason ? String(params.reason) : '',
        askedAt: new Date().toISOString(),
      };

      if (!question.question) {
        return {
          content: [{ type: 'text', text: '提问失败：question 不能为空' }],
          isError: true,
        };
      }

      // 建一个挂起的 Promise，等待用户回答
      let resolveFn;
      let rejectFn;
      const answerPromise = new Promise((resolve, reject) => {
        resolveFn = resolve;
        rejectFn = reject;
      });

      pendingQuestions.set(questionId, { question, resolve: resolveFn, reject: rejectFn });

      // 推给前端
      try {
        ctx.emit?.('user_question', question);
      } catch {
        /* 忽略 */
      }

      console.log(`[ask_user] 提问 ${questionId}: ${question.question.slice(0, 60)}`);

      // 等待回答（带超时）
      let timer;
      const timeoutPromise = new Promise((resolve) => {
        timer = setTimeout(() => {
          resolve({ __timeout: true });
        }, QUESTION_TIMEOUT_MS);
      });

      try {
        const result = await Promise.race([answerPromise, timeoutPromise]);

        if (result && result.__timeout) {
          const fallback = question.defaultAnswer;
          console.log(`[ask_user] 提问 ${questionId} 超时${fallback ? `，使用默认值：${fallback}` : ''}`);

          return {
            content: [
              {
                type: 'text',
                text: fallback
                  ? `用户未在时限内回答，按默认值继续：${fallback}`
                  : '用户未在时限内回答。请按你的专业判断继续，并在结果中说明这个假设。',
              },
            ],
            details: { questionId, timedOut: true, usedDefault: Boolean(fallback) },
          };
        }

        const answer = String(result?.answer ?? '');
        console.log(`[ask_user] 提问 ${questionId} 已回答: ${answer.slice(0, 60)}`);

        return {
          content: [{ type: 'text', text: answer || '（用户未填写内容）' }],
          details: { questionId, answered: true },
        };
      } finally {
        clearTimeout(timer);
        pendingQuestions.delete(questionId);
      }
    },
  };
}

/**
 * 回答一个待答问题。
 *
 * @returns {{ok: boolean, error?: string}}
 */
export function answerQuestion(questionId, answer) {
  const pending = pendingQuestions.get(questionId);
  if (!pending) {
    return { ok: false, error: '问题不存在或已超时' };
  }

  pendingQuestions.delete(questionId);
  pending.resolve({ answer: String(answer ?? '') });
  return { ok: true };
}

/** 列出当前会话的待答问题。 */
export function listPendingQuestions(sessionId) {
  const out = [];
  for (const { question } of pendingQuestions.values()) {
    if (!sessionId || question.sessionId === sessionId) {
      out.push(question);
    }
  }
  return out;
}

/** 拒绝/取消一个待答问题（任务中止时用）。 */
export function rejectQuestion(questionId, reason) {
  const pending = pendingQuestions.get(questionId);
  if (!pending) return false;
  pendingQuestions.delete(questionId);
  pending.reject(new Error(reason || '问题已取消'));
  return true;
}

/** 清理某个会话的全部待答问题（会话结束时用）。 */
export function clearSessionQuestions(sessionId, reason) {
  let n = 0;
  for (const [id, { question, reject }] of pendingQuestions.entries()) {
    if (question.sessionId === sessionId) {
      pendingQuestions.delete(id);
      try {
        reject(new Error(reason || '会话已结束'));
      } catch {
        /* 忽略 */
      }
      n++;
    }
  }
  return n;
}
