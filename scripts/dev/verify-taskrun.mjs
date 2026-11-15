/** 验证任务运行记录持久化 */
const API = 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';
const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': PROJECT };

// 1. 看当前记录
log('=== 1. 任务历史 ===');
const list = await fetch(`${API}/api/task-runs/list`, { method: 'POST', headers, body: JSON.stringify({ limit: 10 }) });
const lb = await list.json();
if (!lb.ok) { console.error('查询失败:', lb.error); process.exit(1); }
log(`共 ${lb.data.count} 条记录`);
for (const it of lb.data.items) {
  log(`  [${it.status}] ${it.kind} — ${it.taskId.slice(0, 8)}  启动 ${it.startedAt}${it.error ? '  错误: ' + it.error : ''}`);
}

// 2. 跑一个任务
log('\n=== 2. 启动新任务 ===');
const { data: state } = await (await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' })).json();
const has = new Set();
for (const n of state.outline) if (n.parentNodeId) has.add(n.parentNodeId);
const empty = state.outline.filter((n) => !has.has(n.nodeId) && (!n.content || n.content.length < 20)).slice(0, 1);
log(`目标节: ${empty[0].title}`);

const start = await fetch(`${API}/api/content/generate`, {
  method: 'POST', headers,
  body: JSON.stringify({
    tenderMarkdownPath: '/workspace/data/testdata/tender.md',
    targetWordsPerSection: 200,
    nodeIds: [empty[0].nodeId],
  }),
});
const sb = await start.json();
if (!sb.ok) { console.error('启动失败:', sb.error); process.exit(1); }
const taskId = sb.data.taskId;
log(`✓ 任务已启动: ${taskId}`);

// 3. 立刻查是否记录了
await new Promise((r) => setTimeout(r, 2000));
log('\n=== 3. 立即查询该任务 ===');
const get1 = await fetch(`${API}/api/task-runs/get`, { method: 'POST', headers, body: JSON.stringify({ taskId }) });
const g1 = await get1.json();
if (!g1.ok) {
  console.error('❌ 任务记录未创建:', g1.error);
  process.exitCode = 1;
} else {
  log(`✓ 记录存在: status=${g1.data.status}, kind=${g1.data.kind}`);
  log(`  sessionId: ${g1.data.sessionId.slice(0, 8)}...`);
  log(`  workspace: ${g1.data.workspaceDir}`);
  log(`  心跳: ${g1.data.heartbeatAt}`);
}

// 4. 等任务完成
log('\n=== 4. 等任务完成 ===');
for (let i = 0; i < 60; i++) {
  await new Promise((r) => setTimeout(r, 3000));
  const r = await (await fetch(`${API}/api/task-runs/get`, { method: 'POST', headers, body: JSON.stringify({ taskId }) })).json();
  if (r.ok && r.data.status !== 'running') {
    log(`✓ 最终状态: ${r.data.status}`);
    if (r.data.finishedAt) log(`  完成时间: ${r.data.finishedAt}`);
    if (r.data.error) log(`  错误: ${r.data.error}`);
    break;
  }
  if (i % 5 === 0) log(`  等待中… status=${r.data?.status}`);
}

// 5. 运行中的任务
log('\n=== 5. 运行中的任务 ===');
const run = await (await fetch(`${API}/api/task-runs/running`, { method: 'POST', headers, body: '{}' })).json();
log(`运行中: ${run.data.count} 个`);
for (const it of run.data.items) {
  log(`  ${it.kind} — ${it.taskId.slice(0, 8)}  stale=${it.stale}`);
}
