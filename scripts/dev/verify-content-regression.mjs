/** 回归测试：确认加了空转/空闲监控后，正文生成仍正常 */
const API = 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';
const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': PROJECT };

const { data: state } = await (await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' })).json();
const has = new Set();
for (const n of state.outline) if (n.parentNodeId) has.add(n.parentNodeId);
const empty = state.outline.filter((n) => !has.has(n.nodeId) && (!n.content || n.content.length < 20)).slice(0, 1);

log(`测试节: ${empty[0].title}`);

const start = await fetch(`${API}/api/content/generate`, {
  method: 'POST', headers,
  body: JSON.stringify({
    tenderMarkdownPath: '/workspace/data/testdata/tender.md',
    targetWordsPerSection: 300,
    nodeIds: [empty[0].nodeId],
  }),
});
const sb = await start.json();
if (!sb.ok) { console.error('❌ 启动失败:', sb.error); process.exit(1); }
log(`✓ 已启动: ${sb.data.taskId}`);

const evRes = await fetch(`${API}/api/content/events`, {
  method: 'POST', headers,
  body: JSON.stringify({ taskId: sb.data.taskId, since: 0 }),
});

const reader = evRes.body.getReader();
const dec = new TextDecoder();
let buf = '';
let done = false;
const tools = [];
let stalls = 0;

const loop = async () => {
  while (!done) {
    const { done: rd, value } = await reader.read();
    if (rd) break;
    buf += dec.decode(value, { stream: true });
    const blocks = buf.split('\n\n');
    buf = blocks.pop() || '';
    for (const b of blocks) {
      const lines = b.split('\n');
      const ev = lines.find((l) => l.startsWith('event: '));
      const dt = lines.find((l) => l.startsWith('data: '));
      if (!dt) continue;
      const name = ev ? ev.slice(7).trim() : '';
      let p; try { p = JSON.parse(dt.slice(6)); } catch { continue; }
      if (name === 'done') { log(`[完成] ${p.status}`); done = true; return; }
      if (name === 'agent') {
        if (p.type === 'tool_start') tools.push(p.toolName);
        else if (p.type === 'stall_detected') { stalls++; log(`  🔴 空转: ${p.reason}`); }
        else if (p.type === 'idle_timeout') log(`  ⏱ 空闲超时: ${p.idleMs}ms`);
        else if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 结果 ===');
log(`工具调用: ${tools.length} 次`);
log(`空转检出: ${stalls} 次`);
if (tools.length > 0 && stalls === 0) {
  log('✅ 回归通过：正文生成正常，无误判空转');
} else if (stalls > 0) {
  log('⚠️ 有误判，需调阈值');
}
