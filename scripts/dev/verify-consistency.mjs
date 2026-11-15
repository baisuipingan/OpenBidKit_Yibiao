/**
 * 验证一致性检查：注入故意矛盾的内容，看能否检出。
 *
 * 埋了 3 个矛盾：
 *  1. 工期：90 个日历天 vs 三个月
 *  2. 可用性：99.9% vs 99.99%
 *  3. 项目经理姓名：张建国 vs 张建军
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

// 取两节节点
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const hasChild = new Set();
for (const n of state.outline) if (n.parentNodeId) hasChild.add(n.parentNodeId);
const leaves = state.outline.filter((n) => !hasChild.has(n.nodeId)).slice(0, 2);
if (leaves.length < 2) { console.error('❌ 需要至少 2 个叶子节点'); process.exit(1); }

log('使用节点:', leaves.map((l) => l.title).join(' | '));

// 注入矛盾内容
const sec1 = `# ${leaves[0].title}

## 项目工期安排

本项目总工期为 90 个日历天，自合同签订之日起计算。项目经理由张建国担任，全面负责项目实施。

实施分为三个阶段：需求调研 15 天、系统开发 50 天、试运行 25 天。

## 质量目标

系统可用性达到 99.9%，满足三级医院业务连续性要求。`;
const sec2 = `# ${leaves[1].title}

## 实施计划

整体建设周期为三个月，确保按期交付。项目团队由张建军担任项目经理，统筹协调各方资源。

## 技术指标承诺

核心系统全年可用性不低于 99.99%，关键业务响应时间小于 2 秒。`;

for (const [i, sec] of [sec1, sec2].entries()) {
  const res = await fetch(`${API}/api/technical-plan/save-section-content`, {
    method: 'POST', headers,
    body: JSON.stringify({ nodeId: leaves[i].nodeId, content: sec }),
  });
  const b = await res.json();
  if (!b.ok) { console.error(`❌ 保存第 ${i + 1} 节失败:`, b.error); process.exit(1); }
}
log('✓ 已注入含矛盾的正文（2 节）');
log('  埋的矛盾: 工期(90天 vs 三个月) / 可用性(99.9% vs 99.99%) / 项目经理(张建国 vs 张建军)');

// 执行检查
log('\n=== 开始一致性检查 ===');
const t0 = Date.now();
const res = await fetch(`${API}/api/consistency/check`, {
  method: 'POST', headers,
  body: JSON.stringify({ nodeIds: leaves.map((l) => l.nodeId) }),
});
const body = await res.json();

if (!body.ok) {
  console.error('❌ 检查失败:', body.error);
  process.exit(1);
}

const r = body.data;
log(`\n耗时: ${r.durationMs}ms（总等待 ${Date.now() - t0}ms）`);
log(`检查章节: ${r.checkedSections}，字数: ${r.totalWords}`);
log(`抽取事实: ${r.facts.length} 条`);
log(`发现问题: ${r.issues.length} 条（high ${r.highCount} / medium ${r.mediumCount} / low ${r.lowCount}）`);

if (r.issues.length > 0) {
  log('\n=== 问题详情 ===');
  for (const iss of r.issues) {
    log(`\n[${iss.severity.toUpperCase()}] ${iss.type} — ${iss.subject}`);
    log(`  ${iss.description}`);
    if (iss.suggestion) log(`  建议: ${iss.suggestion}`);
    for (const e of iss.evidence || []) {
      log(`  · ${e.sectionTitle}: "${e.quote}"`);
    }
  }
}

if (r.facts.length > 0) {
  log('\n=== 抽取的事实（前 8 条）===');
  for (const f of r.facts.slice(0, 8)) {
    log(`  [${f.category}] ${f.subject} = ${f.value}  ← ${f.sectionTitle}`);
  }
}

// 断言：至少检出 2 个矛盾
const found = r.issues.length;
log('\n=== 验证 ===');
if (found >= 2) {
  log(`✅ 检出 ${found} 个问题，检查能力正常`);
} else {
  console.error(`❌ 只检出 ${found} 个问题，期望至少 2 个（埋了 3 个）`);
  process.exitCode = 1;
}
