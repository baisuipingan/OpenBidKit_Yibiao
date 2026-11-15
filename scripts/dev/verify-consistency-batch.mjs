/**
 * 验证分批能力：用大量章节检查分批逻辑是否正确。
 *
 * 用现有的 64 个叶子节点（大部分没正文，只有少数有），
 * 并人为塞满内容测试分批边界。
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

const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const hasChild = new Set();
for (const n of state.outline) if (n.parentNodeId) hasChild.add(n.parentNodeId);
const leaves = state.outline.filter((n) => !hasChild.has(n.nodeId));

log(`叶子节点总数: ${leaves.length}`);

// 给前 6 个叶子填内容（测试多节合并成一批）
const filled = leaves.slice(0, 6);
for (let i = 0; i < filled.length; i++) {
  const content = `# ${filled[i].title}

## 本章要点

本项目第 ${i + 1} 部分的实施要点。统一工期为 90 个日历天，项目经理为张建国。
系统可用性达到 99.9%，保障业务连续性。

## 具体措施

采用微服务架构，分 ${8 + i} 个服务模块部署，每个模块独立扩容。
数据层采用主备架构，故障切换时间小于 30 秒。
`;
  const res = await fetch(`${API}/api/technical-plan/save-section-content`, {
    method: 'POST', headers,
    body: JSON.stringify({ nodeId: filled[i].nodeId, content }),
  });
  const b = await res.json();
  if (!b.ok) { console.error(`保存失败:`, b.error); process.exit(1); }
}
log(`✓ 已填充 ${filled.length} 节（内容一致，应无矛盾）`);

// 用小批次强制分批
log('\n=== 小批次检查（强制分批，每批 300 字）===');
const res = await fetch(`${API}/api/consistency/check`, {
  method: 'POST', headers,
  body: JSON.stringify({
    maxWordsPerBatch: 300,
    nodeIds: filled.map((f) => f.nodeId),
  }),
});
const body = await res.json();
if (!body.ok) { console.error('❌', body.error); process.exit(1); }

const r = body.data;
log(`检查章节: ${r.checkedSections}，字数: ${r.totalWords}`);
log(`抽取事实: ${r.facts.length} 条`);
log(`发现问题: ${r.issues.length} 条（high ${r.highCount} / medium ${r.mediumCount} / low ${r.lowCount}）`);

// 检查是否有「服务模块数」的假矛盾（8/9/10/11/12/13 个 — 这些是不同章节的独立数字，不该报）
const falsePositives = r.issues.filter((i) => i.subject.includes('服务') || i.subject.includes('模块'));
if (falsePositives.length > 0) {
  log('\n⚠️ 可能的误报:');
  for (const fp of falsePositives) log(`  ${fp.subject}: ${fp.description}`);
} else {
  log('✓ 没有误报「服务模块数」（不同章节的分项数字不当成矛盾）');
}

if (r.issues.length === 0) {
  log('\n✅ 分批处理正常，无误报');
} else {
  log(`\n⚠️ 检出 ${r.issues.length} 个问题（需人工确认是否误报）`);
}
