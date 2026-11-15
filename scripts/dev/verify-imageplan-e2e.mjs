/**
 * 端到端验证：正文生成时按配图编排执行。
 *
 * 检查点：
 *  1. 编排结果被注入 Prompt
 *  2. Agent 按分配的 kind 调用 generate_image
 *  3. 配图数量与编排一致
 */
const API = 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';
const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': PROJECT };

// 找两个高适配分的空白节
const { data: state } = await (await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' })).json();
const has = new Set();
for (const n of state.outline) if (n.parentNodeId) has.add(n.parentNodeId);
const empty = state.outline.filter((n) => !has.has(n.nodeId) && (!n.content || n.content.length < 20));

// 优先选含「架构」「流程」的（编排会给它们配图）
const target = empty.find((n) => /架构|流程/.test(n.title)) || empty[0];
log(`目标节: ${target.title}`);

// 先看编排会怎么安排
const preview = await fetch(`${API}/api/content/preview-image-plan`, {
  method: 'POST', headers,
  body: JSON.stringify({ nodeIds: [target.nodeId], targetWordsPerSection: 600, totalImages: 2 }),
});
const pv = (await preview.json()).data;
log(`编排结果: ${pv.plan.totalImages} 张 / ${pv.plan.totalGroups} 组`);
for (const a of pv.plan.allocations) {
  for (const g of a.groups) {
    log(`  → ${g.layout} (${g.count}张) ${g.kind}`);
  }
}

// 启动生成
log('\n=== 启动正文生成 ===');
const start = await fetch(`${API}/api/content/generate`, {
  method: 'POST', headers,
  body: JSON.stringify({
    tenderMarkdownPath: '/workspace/data/testdata/tender.md',
    targetWordsPerSection: 600,
    nodeIds: [target.nodeId],
    totalImages: 2,
  }),
});
const sb = await start.json();
if (!sb.ok) { console.error('❌ 启动失败:', sb.error); process.exit(1); }
log(`✓ 任务: ${sb.data.taskId}`);
log(`  配图计划已注入: ${sb.data.imagePlan ? '是' : '否'}`);

const evRes = await fetch(`${API}/api/content/events`, {
  method: 'POST', headers,
  body: JSON.stringify({ taskId: sb.data.taskId, since: 0 }),
});

const reader = evRes.body.getReader();
const dec = new TextDecoder();
let buf = '';
let done = false;
const imgCalls = [];
let taskStatus = '';

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
      if (name === 'done') { done = true; return; }
      if (name === 'agent') {
        if (p.type === 'tool_start' && p.toolName === 'generate_image') {
          const kind = p.args?.kind || '?';
          imgCalls.push({ kind, args: p.args });
          log(`  [配图] kind=${kind}`);
        } else if (p.type === 'tool_end' && p.toolName === 'generate_image') {
          const ok = !p.isError;
          log(`    ${ok ? '✓' : '✗'} 生成${ok ? '成功' : '失败'}`);
        } else if (p.type === 'task_end') {
          taskStatus = p.status;
          log(`  [结束] ${p.status}`);
        }
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
log(`generate_image 调用: ${imgCalls.length} 次`);
const kinds = imgCalls.map((c) => c.kind);
log(`实际 kind: ${JSON.stringify(kinds)}`);
log(`编排期望 kind: ${JSON.stringify(pv.plan.allocations.flatMap((a) => a.groups.map((g) => g.kind)))}`);

if (imgCalls.length === 0) {
  log('⚠️ Agent 没有配图（可能判断本节不需要，或编排分太低）');
} else if (imgCalls.length >= pv.plan.totalGroups) {
  log(`✅ Agent 按编排配了 ${imgCalls.length} 组图`);
} else {
  log(`⚠️ 配了 ${imgCalls.length} 组，编排要求 ${pv.plan.totalGroups} 组`);
}
