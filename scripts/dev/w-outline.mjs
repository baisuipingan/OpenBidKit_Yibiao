const API = 'http://127.0.0.1:8080';
const H = { 'Content-Type': 'application/json' };
const login = await fetch(`${API}/api/auth/login`, { method: 'POST', headers: H, body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }) });
const { data: auth } = await login.json();
const HH = { ...H, Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': 'f77d5870-aafc-47a1-a267-27f7f881869b' };
const post = async (p, b = {}) => (await fetch(`${API}/api${p}`, { method: 'POST', headers: HH, body: JSON.stringify(b) })).json().catch(() => ({}));

const t0 = Date.now();
let last = -1;
for (let i = 0; i < 60; i++) {
  await new Promise(r => setTimeout(r, 6000));
  const s = await post('/technical-plan/load-state');
  const n = (s.data?.outline || []).length;
  const task = s.data?.outlineGenerationTask;
  process.stdout.write(`\r  ${((Date.now()-t0)/1000).toFixed(0)}s 节点=${n} status=${task?.status ?? '?'}`);
  if (n !== last) last = n;
  // 任务结束标志
  if (task?.status === 'success' || task?.status === 'error' || task?.status === 'failed') {
    console.log(`\n结束: ${task.status} 目录 ${n} 节点`);
    const nodes = s.data?.outline || [];
    if (nodes.length) {
      console.log('章节:');
      for (const nd of nodes.slice(0, 8)) {
        console.log(`  ${' '.repeat(((nd.Level||nd.level||1)-1)*2)}${nd.Number||nd.number||''} ${nd.Title||nd.title}${(nd.Children?.length||nd.children?.length) ? ` (${(nd.Children||nd.children).length}子)` : ''}`);
      }
    }
    break;
  }
  if (Date.now() - t0 > 340000) { console.log('\n超时'); break; }
}
