/**
 * 验证 Agent sidecar 端到端能力。
 *
 * 测什么：
 *   1. 能创建会话（Pi SDK 正确初始化）
 *   2. 能跑一个真实任务（调真实模型）
 *   3. 能通过工具读写文件
 *   4. 事件流能推送
 *   5. 提交校验回调生效
 *
 * 用法（需要真实模型 API）：
 *   AI_BASE_URL=https://... AI_API_KEY=sk-... AI_MODEL=gpt-4o-mini \
 *     node scripts/verify-agent.mjs
 */

import { setTimeout as sleep } from 'node:timers/promises';

const SIDECAR = process.env.SIDECAR || 'http://127.0.0.1:7070';
const INTERNAL_TOKEN = process.env.AGENT_INTERNAL_TOKEN || '';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || 'gpt-4o-mini';
const WORKSPACE = process.env.WORKSPACE || '/workspace/data/agent/verify-' + Date.now();

const log = (...a) => console.log(...a);
const fail = (m) => {
  console.error('❌', m);
  process.exitCode = 1;
};

if (!AI_BASE_URL || !AI_API_KEY) {
  console.error('缺少 AI_BASE_URL / AI_API_KEY 环境变量');
  process.exit(1);
}

const headers = {
  'Content-Type': 'application/json',
  ...(INTERNAL_TOKEN ? { Authorization: `Bearer ${INTERNAL_TOKEN}` } : {}),
};

async function api(path, body, method = 'POST') {
  const res = await fetch(`${SIDECAR}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  const payload = await res.json();
  if (!payload.ok) throw new Error(`${payload.error?.code}: ${payload.error?.message}`);
  return payload.data;
}

// ---------- 1. 创建会话 ----------
log('=== 1. 创建 Agent 会话 ===');
log('  工作目录:', WORKSPACE);
log('  模型:', AI_MODEL, '@', AI_BASE_URL);

const created = await api('/sessions', {
  workspaceDir: WORKSPACE,
  provider: {
    baseUrl: AI_BASE_URL.replace(/\/+$/, '') + (AI_BASE_URL.endsWith('/v1') ? '' : '/v1'),
    apiKey: AI_API_KEY,
    model: AI_MODEL,
  },
  modelConfig: { contextLengthLimit: 128000 },
  workspaceInstructions: [
    '你是投标文件编写助手。',
    '工作目录下有一个 output.md 文件，需要你写入内容。',
    '写入完成后不要输出总结，直接结束。',
  ].join('\n'),
  activeTools: ['read', 'write', 'bash', 'ls'],
  summaryEnabled: false,
});

log('✓ 会话已创建');
log('  sessionId:', created.sessionId);
log('  SDK 版本:', created.snapshot.sdkVersion);
log('  模型:', JSON.stringify(created.snapshot.model));

const sessionId = created.sessionId;

// ---------- 2. 连事件流 ----------
log('\n=== 2. 连接事件流 ===');

const events = [];
const eventAbort = new AbortController();

const eventPromise = (async () => {
  try {
    const res = await fetch(`${SIDECAR}/sessions/${sessionId}/events`, {
      headers,
      signal: eventAbort.signal,
    });
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // 解析 SSE
      const blocks = buffer.split('\n\n');
      buffer = blocks.pop() || '';
      for (const block of blocks) {
        const dataLine = block.split('\n').find((l) => l.startsWith('data: '));
        if (!dataLine) continue;
        try {
          const ev = JSON.parse(dataLine.slice(6));
          events.push(ev);
          if (ev.type === 'tool_start') {
            log(`  [事件] 调用工具: ${ev.toolName}`);
          } else if (ev.type === 'tool_end') {
            log(`  [事件] 工具完成: ${ev.toolName}${ev.isError ? ' (失败)' : ''}`);
          } else if (ev.type === 'stage_start') {
            log(`  [事件] 阶段开始: ${ev.stageId}`);
          } else if (ev.type === 'task_end') {
            log(`  [事件] 任务结束: ${ev.status}`);
          } else if (ev.type === 'submission_retry') {
            log(`  [事件] 提交修复 #${ev.attempt} (${ev.mode})`);
          } else if (ev.type === 'assistant_end') {
            log(`  [事件] 助手回复: ${String(ev.text).slice(0, 100)}`);
          }
        } catch {
          /* 非 JSON 行 */
        }
      }
    }
  } catch (e) {
    if (e.name !== 'AbortError') log('  事件流异常:', e.message);
  }
})();

await sleep(500);

// ---------- 3. 启动任务 ----------
log('\n=== 3. 启动任务（真实调用模型）===');

const started = await api(`/sessions/${sessionId}/run`, {
  taskId: 'verify-task-1',
  stages: [
    {
      id: 'write-output',
      prompt: [
        '请在工作目录下创建 output.md，内容为：',
        '',
        '# 测试标题',
        '',
        '这是一段由 Agent 生成的测试内容。',
        '',
        '要求：只写这几行，不要额外内容。写完就结束。',
      ].join('\n'),
      tools: ['read', 'write', 'bash', 'ls'],
      maxRetries: 2,
    },
  ],
  timeoutMs: 180000,
});

log('✓ 任务已启动, taskId:', started.taskId);

// ---------- 4. 等任务结束 ----------
log('\n=== 4. 等待任务完成（最多 3 分钟）===');

const deadline = Date.now() + 180000;
let taskEnded = false;
while (Date.now() < deadline) {
  const endEv = events.find((e) => e.type === 'task_end');
  if (endEv) {
    taskEnded = true;
    log('✓ 任务已结束, 状态:', endEv.status);
    if (endEv.error) log('  错误:', endEv.error);
    break;
  }
  await sleep(1000);
}

if (!taskEnded) fail('任务超时未结束');

eventAbort.abort();
await eventPromise.catch(() => {});

// ---------- 5. 检查产物 ----------
log('\n=== 5. 检查产物文件 ===');

try {
  const fs = await import('node:fs/promises');
  const content = await fs.readFile(`${WORKSPACE}/output.md`, 'utf-8');
  log('✓ output.md 已生成，内容：');
  log('---');
  log(content);
  log('---');

  if (!content.includes('测试标题')) {
    fail('产物内容不符合预期');
  }
} catch (e) {
  fail(`产物文件读取失败: ${e.message}`);
}

// ---------- 6. 事件统计 ----------
log('\n=== 6. 事件统计 ===');
const byType = {};
for (const e of events) byType[e.type] = (byType[e.type] || 0) + 1;
log(JSON.stringify(byType, null, 2));

// 清理
await api(`/sessions/${sessionId}`, null, 'DELETE').catch(() => {});

if (!process.exitCode) {
  log('\n' + '='.repeat(60));
  log('✅ Agent sidecar 端到端验证通过');
  log('='.repeat(60));
}
