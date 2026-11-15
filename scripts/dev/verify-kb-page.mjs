/** 验证知识库页面 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const errors = [];
const fails = [];
page.on('pageerror', (e) => errors.push(e.message.slice(0, 120)));
page.on('response', (res) => {
  const u = res.url();
  if (u.includes('/api/') && res.status() >= 400 && res.status() !== 401) {
    fails.push(`${res.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
  }
});

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(1200);
if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
}
await page.waitForTimeout(3000);

// 点「知识库」
await page.getByText('知识库', { exact: false }).first().click();
await page.waitForTimeout(2500);

// 点「文档知识库」
const kbLink = page.getByText('文档知识库', { exact: false });
if (await kbLink.count() > 0) {
  await kbLink.first().click();
  await page.waitForTimeout(4000);
}

const info = await page.evaluate(() => {
  const text = document.body.innerText || '';
  return {
    hasTitle: text.includes('知识库'),
    hasFolders: text.includes('医疗行业素材'),
    hasDoc: text.includes('技术方案测试'),
    textHead: text.slice(text.indexOf('知识库'), text.indexOf('知识库') + 400),
    folderCount: document.querySelectorAll('[class*="folder"]').length,
  };
});

console.log('知识库标题:', info.hasTitle ? '是' : '否');
console.log('显示文件夹:', info.hasFolders ? '是' : '否');
console.log('显示文档:', info.hasDoc ? '是' : '否');
console.log('\n=== 页面内容 ===');
console.log(info.textHead);
console.log('\nJS 错误:', errors.length ? errors.slice(0, 3).join(' | ') : '无');
console.log('接口失败:', fails.length ? [...new Set(fails)].slice(0, 6).join(' | ') : '无');

await page.screenshot({ path: '/tmp/shots/kb-page.png' });
await browser.close();
