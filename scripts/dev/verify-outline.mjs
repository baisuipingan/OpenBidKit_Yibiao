/**
 * 验证目录生成 Agent。
 */
const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || '';

const log = (...a) => console.log(...a);
const fail = (m) => { console.error('❌', m); process.exitCode = 1; };

const loginRes = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const loginBody = await loginRes.json();
if (!loginBody.ok) { fail('登录失败'); process.exit(1); }
log('✓ 已登录');

const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${loginBody.data.accessToken}`,
  'X-Project-Id': PROJECT,
};

log('\n=== 启动目录生成 ===');
const startRes = await fetch(`${API}/api/outline/generate`, {
  method: 'POST', headers,
  body: JSON.stringify({
    provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL },
    tenderMarkdownPath: '/workspace/data/testdata/tender.md',
    projectName: '某市人民医院信息化系统建设项目',
    maxLevels: 3,
  }),
});
const startBody = await startRes.json();
if (!startBody.ok) { fail(`启动失败: ${JSON.stringify(startBody)}`); process.exit(1); }

const taskId = startBody.data.taskId;
log(`✓ 任务已启动: ${taskId}`);

log('\n=== 订阅进度 ===');
const evRes = await fetch(`${API}/api/outline/events`, {
  method: 'POST', headers,
  body: JSON.stringify({ taskId, since: 0 }),
});

const reader = evRes.body.getReader();
const decoder = new TextDecoder();
let buffer = '';
let finished = false;
const toolCalls = [];

const readLoop = async () => {
while (true) {
  const { done, value } = await reader.read();
  if (done) break;
  buffer += decoder.decode(value, { stream: true });

  const blocks = buffer.split('\n\n');
  buffer = blocks.pop() || '';

  for (const block of blocks) {
    const lines = block.split('\n');
    const evLine = lines.find((l) => l.startsWith('event: '));
    const dataLine = lines.find((l) => l.startsWith('data: '));
    if (!dataLine) continue;

    const evName = evLine ? evLine.slice(7).trim() : '';
    let payload;
    try { payload = JSON.parse(dataLine.slice(6)); } catch { continue; }

    if (evName === 'done') {
      log(`\n[完成] status=${payload.status} events=${payload.eventCount}`);
      finished = true;
      return;
    }
    if (evName === 'error') { fail(payload.message); finished = true; return; }

    if (evName === 'agent') {
      if (payload.type === 'tool_start') {
        toolCalls.push(payload.toolName);
        log(`  [工具] ${payload.toolName}`);
      } else if (payload.type === 'task_end') {
        log(`  [结束] ${payload.status}${payload.error ? ': ' + payload.error : ''}`);
      }
    }
  }
  if (finished) break;
}
};
await readLoop();

log('\n=== 检查产物 ===');
const { execSync } = await import('node:child_process');
const ws = execSync(`find /workspace/data/agent -maxdepth 1 -type d -newermt '-10 minutes' | grep -v -E '_agents|_sessions' | head -1`).toString().trim();
if (!ws) { fail('未找到工作区'); }
else {
  log(`工作区: ${ws}`);
  try {
    const files = execSync(`ls -la ${ws}`).toString();
    log(files);
    const outlineJson = execSync(`cat ${ws}/outline.json 2>/dev/null | head -60`).toString();
    if (outlineJson.trim()) {
      log('--- outline.json ---');
      log(outlineJson);
    }
  } catch (e) { log('读取产物失败:', e.message); }
}

if (!process.exitCode) {
  log('\n' + '='.repeat(56));
  log('✅ 目录生成验证完成');
  log(`   工具序列: ${toolCalls.join(' → ')}`);
  log('='.repeat(56));
}
