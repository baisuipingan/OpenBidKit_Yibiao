/** 完整验证：带配图的正文 → 导出 Word → 图片真的嵌入 */
const API = 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';
const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': PROJECT };

// 1. 取图片引用
const imgRes = await fetch(`${API}/api/assets/list`, { method: 'POST', headers, body: JSON.stringify({ kind: 'image' }) });
const { data: imgData } = await imgRes.json();
const flowImg = imgData.assets.find((a) => a.name.includes('flow')) || imgData.assets[0];
const orgImg = imgData.assets.find((a) => a.name.includes('org')) || imgData.assets[1];
log(`图片: ${flowImg?.name} / ${orgImg?.name}`);

// 2. 取有正文的节点
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const nodes = state.outline.filter((n) => (n.content || '').length > 10);
log(`有正文的节点: ${nodes.length}`);

// 3. 给第一节插入配图
const target = nodes[0];
// 去掉已有的 figure 引用，避免重复
let base = target.content.replace(/<figure[\s\S]*?<\/figure>/g, '').trim();

const withFigures = `${base}

<figure data-yb-size="wide"><img alt="投标流程图" data-yb-asset-ref="${flowImg.name}" /><figcaption>图 1-1 投标流程示意</figcaption></figure>

<figure data-yb-size="square"><img alt="项目组织架构" data-yb-asset-ref="${orgImg.name}" /><figcaption>图 1-2 项目组织架构</figcaption></figure>
`;

const saveRes = await fetch(`${API}/api/technical-plan/save-section-content`, {
  method: 'POST', headers,
  body: JSON.stringify({ nodeId: target.nodeId, content: withFigures }),
});
const saveBody = await saveRes.json();
if (!saveBody.ok) { console.error('❌ 保存失败:', saveBody.error); process.exit(1); }
log(`✓ 正文已保存（${saveBody.data.chars} 字，含 2 张配图）`);

// 4. 导出
log('\n=== 导出 ===');
const expRes = await fetch(`${API}/api/export/word`, {
  method: 'POST', headers,
  body: JSON.stringify({ templatePath: '/workspace/data/templates/blank.docx' }),
});
const expBody = await expRes.json();
if (!expBody.ok) { console.error('❌ 导出失败:', expBody.error); process.exit(1); }
log(`✓ ${expBody.data.fileName} (${expBody.data.sizeBytes}B)`);

// 5. 检查 Word 里的图片
const dlRes = await fetch(`${API}${expBody.data.downloadUrl}?access_token=${auth.accessToken}`);
const buf = Buffer.from(await dlRes.arrayBuffer());
const fs = await import('node:fs');
fs.writeFileSync('/tmp/full-check.docx', buf);

const { execSync } = await import('node:child_process');
const media = execSync(`cd /tmp && rm -rf fc && mkdir fc && cd fc && unzip -o -q /tmp/full-check.docx && find . -name "*.png" -o -name "*.jpg" -o -name "*.emf" 2>/dev/null || echo "(无)"`).toString();
log('\n=== Word 内嵌图片 ===');
log(media);

const count = (media.match(/\.(png|jpg|jpeg|emf|wmf)/gi) || []).length;
log(`图片数量: ${count}`);
if (count >= 2) {
  log('\n✅ 配图成功嵌入 Word');
} else {
  console.error(`\n❌ 期望 2 张图，实际 ${count} 张`);
  process.exitCode = 1;
}
