/**
 * 验证「检查 → 修复 → 复检」闭环。
 *
 * 预期：修复后矛盾应该消失（或大幅减少）。
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

// 1. 注入矛盾
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const hasChild = new Set();
for (const n of state.outline) if (n.parentNodeId) hasChild.add(n.parentNodeId);
const leaves = state.outline.filter((n) => !hasChild.has(n.nodeId)).slice(0, 2);

const sec1 = `# ${leaves[0].title}

## 项目工期

本项目总工期为 90 个日历天。项目经理由张建国担任。

## 质量目标

系统可用性达到 99.9%。`;

const sec2 = `# ${leaves[1].title}

## 实施计划

整体建设周期为三个月。项目团队由张建军担任项目经理。

## 技术指标

核心系统可用性不低于 99.99%。`;

for (const [i, sec] of [sec1, sec2].entries()) {
  await fetch(`${API}/api/technical-plan/save-section-content`, {
    method: 'POST', headers,
    body: JSON.stringify({ nodeId: leaves[i].nodeId, content: sec }),
  });
}
log('✓ 已注入含 3 处矛盾的正文');

const nodeIds = leaves.map((l) => l.nodeId);

// 2. 检查
log('\n=== 第 1 次检查 ===');
const check1 = await fetch(`${API}/api/consistency/check`, {
  method: 'POST', headers, body: JSON.stringify({ nodeIds }),
});
const r1 = (await check1.json()).data;
log(`检出问题: ${r1.issues.length} 个（high ${r1.highCount} / medium ${r1.mediumCount}）`);
for (const iss of r1.issues) log(`  [${iss.severity}] ${iss.subject}: ${iss.description}`);

if (r1.issues.length === 0) { console.error('❌ 没检出问题，无法测试修复'); process.exit(1); }

// 3. 修复
log('\n=== 修复（apply=true 直接回写）===');
const t0 = Date.now();
const repairRes = await fetch(`${API}/api/consistency/repair`, {
  method: 'POST', headers,
  body: JSON.stringify({ issues: r1.issues, facts: r1.facts, apply: true }),
});
const rep = (await repairRes.json()).data;
log(`耗时: ${rep.durationMs}ms（总 ${Date.now() - t0}ms）`);
log(`修复成功: ${rep.fixedCount} 节，失败: ${rep.failedCount} 节，回写: ${rep.appliedCount} 节`);

for (const r of rep.repairs) {
  if (r.error) {
    log(`  ✗ ${r.title}: ${r.error}`);
  } else if (r.changed) {
    log(`  ✓ ${r.title} 已修改:`);
    for (const c of r.changes || []) log(`      - ${c}`);
  } else {
    log(`  - ${r.title} 无需修改`);
  }
}

// 4. 复检
log('\n=== 第 2 次检查（复检）===');
const check2 = await fetch(`${API}/api/consistency/check`, {
  method: 'POST', headers, body: JSON.stringify({ nodeIds }),
});
const r2 = (await check2.json()).data;
log(`检出问题: ${r2.issues.length} 个（high ${r2.highCount} / medium ${r2.mediumCount}）`);
for (const iss of r2.issues) log(`  [${iss.severity}] ${iss.subject}: ${iss.description}`);

// 5. 断言
log('\n=== 验证 ===');
const before = r1.issues.length;
const after = r2.issues.length;
log(`修复前: ${before} 个问题 → 修复后: ${after} 个问题`);

if (after < before) {
  log(`✅ 修复有效（减少 ${before - after} 个）`);
  if (after === 0) log('✅ 全部矛盾已消除');
} else {
  console.error(`❌ 修复无效（问题数未减少）`);
  process.exitCode = 1;
}

// 6. 打印修复后的正文对比
log('\n=== 修复后的第 1 节 ===');
const state2 = (await (await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' })).json()).data;
const n1 = state2.outline.find((n) => n.nodeId === nodeIds[0]);
log(n1.content);
