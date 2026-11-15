/** 验证正文生成（小规模：只生成 2 节，快速验证链路）*/
const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';

const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${auth.accessToken}`,
  'X-Project-Id': PROJECT,
};

// 从目录取前 2 个叶子
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();

const hasChild = new Set();
for (const n of state.outline) if (n.parentNodeId) hasChild.add(n.parentNodeId);
const leaves = state.outline.filter((n) => !hasChild.has(n.nodeId)).slice(0, 2);

log('待生成节:', leaves.map((l) => l.title).join(' | '));

const start = await fetch(`${API}/api/content/generate`, {
  method: 'POST', headers,
  body: JSON.stringify({
    tenderMarkdownPath: '/workspace/data/testdata/tender.md',
    targetWordsPerSection: 300,
    nodeIds: leaves.map((l) => l.nodeId),
  }),
});
const startBody = await start.json();
if (!startBody.ok) { console.error('启动失败:', startBody); process.exit(1); }
log('✓ 任务已启动:', startBody.data.taskId);

// 订阅事件
const evRes = await fetch(`${API}/api/content/events`, {
  method: 'POST', headers,
  body: JSON.stringify({ taskId: startBody.data.taskId, since: 0 }),
});

const reader = evRes.body.getReader();
const decoder = new TextDecoder();
let buf = '';
let done = false;

const loop = async () => {
  while (!done) {
    const { done: rdone, value } = await reader.read();
    if (rdone) break;
    buf += decoder.decode(value, { stream: true });
    const blocks = buf.split('\n\n');
    buf = blocks.pop() || '';
    for (const b of blocks) {
      const lines = b.split('\n');
      const evLine = lines.find((l) => l.startsWith('event: '));
      const dataLine = lines.find((l) => l.startsWith('data: '));
      if (!dataLine) continue;
      const evName = evLine ? evLine.slice(7).trim() : '';
      let p; try { p = JSON.parse(dataLine.slice(6)); } catch { continue; }
      if (evName === 'done') { log('\n[完成]', p.status); done = true; return; }
      if (evName === 'agent') {
        if (p.type === 'tool_start') log(`  [工具] ${p.toolName}`);
        else if (p.type === 'task_end') log(`  [结束] ${p.status}${p.error ? ': ' + p.error : ''}`);
      }
    }
  }
};
await loop();
