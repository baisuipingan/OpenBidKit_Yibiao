/** 验证前端 getPathForFile → 后台上传 → 业务接口可用 */
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const p = await b.newPage();

const uploads = [];
p.on('response', (res) => {
  if (res.url().includes('/api/upload/')) {
    uploads.push(`${res.status()} ${res.url().split('/api/')[1]}`);
  }
});

await p.goto(BASE, { waitUntil: 'networkidle' });
await p.waitForTimeout(1500);
if (await p.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await p.fill('.yb-login-field input[type=text]', 'admin@local');
  await p.fill('.yb-login-field input[type=password]', 'admin12345');
  await p.click('.yb-login-submit');
  await p.waitForSelector('.app-shell', { timeout: 20000 });
}

// 测 getPathForFile
const result = await p.evaluate(async () => {
  const content = '# 测试文档\n\n这是一段测试内容，用于验证上传链路。';
  const file = new File([content], 'bridge-test.md', { type: 'text/markdown' });
  const pseudo = window.yibiao.file.getPathForFile(file);
  // 等后台上传完成
  await new Promise((r) => setTimeout(r, 2000));
  return { pseudo, startsWithUpload: pseudo.startsWith('upload://') };
});

console.log('=== getPathForFile 结果 ===');
console.log(JSON.stringify(result, null, 2));

console.log('\n=== 上传请求 ===');
console.log(uploads.length ? uploads.join('\n') : '（未捕获到，可能已在上方 evaluate 前完成）');

await b.close();
