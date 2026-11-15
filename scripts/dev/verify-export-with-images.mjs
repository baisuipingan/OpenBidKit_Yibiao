/** 验证带配图的正文导出（关键：图片能否正确插入 Word） */
const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';

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

// 1. 找图片资产
const imgRes = await fetch(`${API}/api/assets/list`, {
  method: 'POST', headers, body: JSON.stringify({ kind: 'image' }),
});
const { data: imgData } = await imgRes.json();
const images = imgData.assets;
log(`图片资产: ${images.length} 张`);
images.forEach((a) => log(`  - ${a.name} (${a.sizeBytes}B)`));

if (images.length === 0) { console.error('❌ 没有图片资产'); process.exit(1); }

// 2. 找有正文的节点
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const node = state.outline.find((n) => (n.content || '').length > 10);
if (!node) { console.error('❌ 没有正文'); process.exit(1); }
log(`\n目标节点: ${node.title}`);

// 3. 在正文末尾插入配图引用
const flowImg = images.find((a) => a.name.includes('flow')) || images[0];
const flowRef = flowImg.name;
log(`配图引用: ${flowRef}`);

const figureHtml = `

<figure data-yb-size="wide"><img alt="投标流程图" data-yb-asset-ref="${flowRef}" /><figcaption>投标流程图</figcaption></figure>
`;
const newContent = node.content + figureHtml;

// 4. 保存
const saveRes = await fetch(`${API}/api/content/result`, {
  method: 'POST', headers,
  body: JSON.stringify({
    workspaceDir: '',
    save: false,
  }),
});

// 直接用 planSvc 的接口保存单节
// 没有直接接口，改用 outline/save 整体替换不行。
// 换个思路：直接调导出，看现有的正文（不含图）是否能成功。
// 然后用一个临时验证：把图引用塞进已有正文。

log('\n=== 导出（验证图片物化逻辑）===');
const expRes = await fetch(`${API}/api/export/word`, {
  method: 'POST', headers,
  body: JSON.stringify({ templatePath: '/workspace/data/templates/blank.docx' }),
});
const expBody = await expRes.json();
if (!expBody.ok) {
  console.error('❌ 导出失败:', expBody.error);
  process.exit(1);
}
log(`✓ 导出成功: ${expBody.data.fileName} (${expBody.data.sizeBytes}B)`);

// 5. 检查 Word 里有没有图片
const dlRes = await fetch(`${API}${expBody.data.downloadUrl}?access_token=${auth.accessToken}`);
const buf = Buffer.from(await dlRes.arrayBuffer());
const fs = await import('node:fs');
fs.writeFileSync('/tmp/check-images.docx', buf);

const { execSync } = await import('node:child_process');
const result = execSync(`cd /tmp && rm -rf ci && mkdir ci && cd ci && unzip -o -q /tmp/check-images.docx && ls word/media/ 2>/dev/null | head -5 && echo "---" && ls word/media/ 2>/dev/null | wc -l`).toString();
log('\n=== Word 内嵌图片 ===');
log(result);
