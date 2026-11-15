/** 知识库完整 UI 验证：列表、条目、搜索 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const fails = [];
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

await page.getByText('知识库', { exact: false }).first().click();
await page.waitForTimeout(2000);
const kbLink = page.getByText('文档知识库', { exact: false });
if (await kbLink.count() > 0) { await kbLink.first().click(); await page.waitForTimeout(4000); }

// 检查页面
const state1 = await page.evaluate(() => {
  const t = document.body.innerText || '';
  return {
    folder: t.includes('医疗行业素材'),
    doc: t.includes('技术方案测试'),
    items: t.includes('5') && t.includes('知识'),
    text: t.slice(t.indexOf('知识库'), t.indexOf('知识库') + 300),
  };
});
console.log('=== 列表页 ===');
console.log('文件夹:', state1.folder ? '✓' : '✗');
console.log('文档:', state1.doc ? '✓' : '✗');

// 点文档看条目
const docEl = page.getByText('技术方案测试', { exact: false }).first();
if (await docEl.count() > 0) {
  await docEl.click();
  await page.waitForTimeout(3000);
}

const state2 = await page.evaluate(() => {
  const t = document.body.innerText || '';
  return {
    hasItems: t.includes('智慧医疗系统'),
    hasK0001: t.includes('K0001') || t.includes('建设目标'),
    text: t.slice(0, 600),
  };
});
console.log('\n=== 文档详情 ===');
console.log('显示知识条目:', state2.hasItems ? '✓' : '✗');

// 测试搜索
const searchInput = page.locator('input[placeholder*="搜索"], input[placeholder*="检索"]').first();
if (await searchInput.count() > 0) {
  await searchInput.fill('微服务');
  await page.waitForTimeout(500);
  await searchInput.press('Enter');
  await page.waitForTimeout(3000);

  const state3 = await page.evaluate(() => {
    const t = document.body.innerText || '';
    return { hasResult: t.includes('微服务') || t.includes('设计原则'), count: (t.match(/设计原则/g) || []).length };
  });
  console.log('\n=== 搜索"微服务" ===');
  console.log('有结果:', state3.hasResult ? '✓' : '✗');
  await page.screenshot({ path: '/tmp/shots/kb-search.png' });
}

console.log('\n接口失败:', fails.length ? [...new Set(fails)].slice(0, 5).join(' | ') : '无');
await page.screenshot({ path: '/tmp/shots/kb-final.png' });
await browser.close();
