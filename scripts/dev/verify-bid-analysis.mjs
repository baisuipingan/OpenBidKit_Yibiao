/**
 * 验证招标文件分析 Agent。
 *
 * 这是标书垂类能力的核心验证：真实招标文件 → Agent 自主分析 → 结构化产物。
 *
 * 用法：
 *   AI_BASE_URL=... AI_API_KEY=... AI_MODEL=... node scripts/verify-bid-analysis.mjs
 */

import { setTimeout as sleep } from 'node:timers/promises';
import fs from 'node:fs/promises';

const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const TENDER = process.env.TENDER || '/workspace/data/testdata/tender.md';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || '';

const log = (...a) => console.log(...a);
const fail = (m) => {
  console.error('❌', m);
  process.exitCode = 1;
};

if (!AI_BASE_URL || !AI_API_KEY) {
  console.error('缺少 AI_BASE_URL / AI_API_KEY');
  process.exit(1);
}

// 1. 登录
log('=== 1. 登录 ===');
const loginRes = await fetch(`${API}/api/auth/login`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const loginBody = await loginRes.json();
if (!loginBody.ok) {
  fail(`登录失败: ${JSON.stringify(loginBody)}`);
  process.exit(1);
}
const token = loginBody.data.accessToken;
log('✓ 已登录');

const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${token}`,
  'X-Project-Id': PROJECT,
};

// 2. 启动分析
log('\n=== 2. 启动招标文件分析 ===');
log('  招标文件:', TENDER);
log('  模型:', AI_MODEL);

const startRes = await fetch(`${API}/api/bid-analysis/start`, {
  method: 'POST',
  headers,
  body: JSON.stringify({
    provider: {
      baseUrl: AI_BASE_URL,
      apiKey: AI_API_KEY,
      model: AI_MODEL,
    },
    tenderMarkdownPath: TENDER,
    // 只跑必填项，缩短验证时间
    items: [
      { id: 'projectInfo', label: '项目信息', output: 'json', required: true,
        goals: '提取项目名称、项目编号、项目类型、项目预算、项目地址。',
        schema: '{"project_name":"项目名称","project_number":"项目编号","project_type":"项目类型","project_budget":"项目预算","project_address":"项目地址"}' },
      { id: 'invalidBidItems', label: '无效投标与废标项', output: 'markdown', required: true,
        goals: '区分「无效投标」和「废标项」，从招标文件中提取。用 Markdown 列表，不要表格。' },
      { id: 'scoringCriteria', label: '评分标准', output: 'markdown', required: true,
        goals: '提取技术分、商务分、价格分的评分规则和分值。用 Markdown 列表。' },
    ],
  }),
});

const startBody = await startRes.json();
if (!startBody.ok) {
  fail(`启动失败: ${JSON.stringify(startBody)}`);
  process.exit(1);
}
log('✓ 任务已启动');
log('  taskId:', startBody.data.taskId);

const taskId = startBody.data.taskId;

// 3. 订阅事件
log('\n=== 3. 订阅事件流 ===');

const events = [];
let finished = false;

const eventTask = (async () => {
  const res = await fetch(`${API}/api/bid-analysis/events`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ taskId, since: 0 }),
  });

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    const blocks = buffer.split('\n\n');
    buffer = blocks.pop() || '';

    for (const block of blocks) {
      const lines = block.split('\n');
      const eventLine = lines.find((l) => l.startsWith('event: '));
      const dataLine = lines.find((l) => l.startsWith('data: '));
      if (!dataLine) continue;

      const eventName = eventLine ? eventLine.slice(7).trim() : '';

      // 后端用 writeSSE("done", ...) 发送结束信号，事件名是 done
      if (eventName === 'done') {
        finished = true;
        return;
      }
      if (eventName === 'error') {
        const e = JSON.parse(dataLine.slice(6));
        fail(`任务出错: ${e.message}`);
        finished = true;
        return;
      }

      try {
        const ev = JSON.parse(dataLine.slice(6));
        events.push(ev);

        // 打印关键事件
        if (ev.type === 'tool_start') {
          log(`  [工具] ${ev.toolName}${ev.args?.path ? ': ' + ev.args.path : ''}`);
        } else if (ev.type === 'stage_start') {
          log(`  [阶段] ${ev.stageId}`);
        } else if (ev.type === 'submission_retry') {
          log(`  [修复] 第 ${ev.attempt} 次，问题 ${ev.issues?.length || 0} 个`);
        } else if (ev.type === 'issues_accepted') {
          log(`  [接受] 保留 ${ev.count} 个质量问题`);
        } else if (ev.type === 'task_end') {
          log(`  [结束] ${ev.status}${ev.error ? ': ' + ev.error : ''}`);
        } else if (ev.type === 'execution_retry') {
          log(`  [重试] 第 ${ev.attempt}/${ev.maximum} 次: ${String(ev.error).slice(0, 80)}`);
        }
      } catch {
        /* 忽略 */
      }
    }
  }
})();

// 4. 等待完成
log('\n=== 4. 等待分析完成（最多 10 分钟）===');
const deadline = Date.now() + 600000;
while (Date.now() < deadline && !finished) {
  await sleep(2000);
}

if (!finished) {
  fail('分析超时未完成');
}

// 5. 查看产物
log('\n=== 5. 检查 Agent 产物 ===');
try {
  const { execSync } = await import('node:child_process');
  const base = `/workspace/data/agent/tenants/${loginBody.data.user.tenantId}/projects/${PROJECT}`;

  // 找最新的 agent 目录
  const dirs = execSync(`find ${base} -maxdepth 4 -type d -name "items" 2>/dev/null | head -1`)
    .toString()
    .trim();

  if (!dirs) {
    fail('未找到产物目录');
  } else {
    const files = execSync(`ls -la ${dirs} 2>/dev/null`).toString();
    log('产物文件:');
    log(files);

    // 打印每个文件的内容
    const list = execSync(`ls ${dirs}/*.md ${dirs}/*.json 2>/dev/null || true`)
      .toString()
      .trim()
      .split('\n')
      .filter(Boolean);

    for (const f of list) {
      log(`\n--- ${f.split('/').pop()} ---`);
      const content = await fs.readFile(f, 'utf-8');
      log(content.slice(0, 800));
    }
  }
} catch (e) {
  log('检查产物时出错:', e.message);
}

// 6. 统计
log('\n=== 6. 事件统计 ===');
const byType = {};
for (const e of events) byType[e.type] = (byType[e.type] || 0) + 1;
log(JSON.stringify(byType, null, 2));

if (!process.exitCode) {
  log('\n' + '='.repeat(60));
  log('✅ 招标文件分析 Agent 验证完成');
  log('='.repeat(60));
}
