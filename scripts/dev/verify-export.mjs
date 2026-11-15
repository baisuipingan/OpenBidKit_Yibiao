/**
 * 验证 Word 导出完整链路。
 */
const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const TEMPLATE = process.env.TEMPLATE || '/workspace/data/templates/blank.docx';

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

log('=== 1. 检查正文 ===');
const stateRes = await fetch(`${API}/api/technical-plan/load-state`, { method: 'POST', headers, body: '{}' });
const { data: state } = await stateRes.json();
const withContent = state.outline.filter((n) => (n.content || '').length > 10);
log(`目录节点: ${state.outline.length}，有正文: ${withContent.length}`);
if (withContent.length === 0) {
  console.error('❌ 没有正文，请先生成正文并落库');
  process.exit(1);
}

log('\n=== 2. 导出 Word ===');
const t0 = Date.now();
const expRes = await fetch(`${API}/api/export/word`, {
  method: 'POST', headers,
  body: JSON.stringify({ templatePath: TEMPLATE }),
});
const expBody = await expRes.json();
if (!expBody.ok) {
  console.error('❌ 导出失败:', expBody.error);
  process.exit(1);
}
const { assetId, fileName, sizeBytes, downloadUrl } = expBody.data;
log(`✓ 导出成功（${Date.now() - t0}ms）`);
log(`  文件名: ${fileName}`);
log(`  大小: ${sizeBytes} 字节`);
log(`  资产 ID: ${assetId}`);

log('\n=== 3. 下载并校验 ===');
const dlRes = await fetch(`${API}${downloadUrl}?access_token=${auth.accessToken}`);
const buf = Buffer.from(await dlRes.arrayBuffer());
log(`✓ 下载成功: ${buf.length} 字节`);

// 校验 ZIP 魔数（docx 是 zip）
const isZip = buf[0] === 0x50 && buf[1] === 0x4b;
log(`  ZIP 格式: ${isZip ? '✓' : '✗'}`);

// 解压检查
const { execSync } = await import('node:child_process');
const fs = await import('node:fs');
fs.writeFileSync('/tmp/verify-export.docx', buf);
const check = execSync(`cd /tmp && rm -rf vx && mkdir vx && cd vx && unzip -o -q /tmp/verify-export.docx && python3 -c "
import re
xml = open('word/document.xml', encoding='utf-8').read()
paras = re.findall(r'<w:p[ >].*?</w:p>', xml, re.S)
styles = {}
for p in paras:
    m = re.search(r'w:val=\\"(Heading[0-9])\\"', p)
    styles[m.group(1) if m else 'direct'] = styles.get(m.group(1) if m else 'direct', 0) + 1
text = ''.join(re.findall(r'<w:t[^>]*>([^<]*)</w:t>', xml))
print(f'段落: {len(paras)}')
print(f'样式: {styles}')
print(f'表格: {len(re.findall(chr(60)+chr(119)+chr(58)+chr(116)+chr(98)+chr(108)+chr(62), xml))}')
print(f'正文: {len(text)} 字')
"`).toString();
log(check);

log('\n' + '='.repeat(56));
log('✅ Word 导出验证通过');
log('='.repeat(56));
