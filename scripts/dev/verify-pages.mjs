/**
 * 遍历技术方案的全部页面，检查渲染是否有错误。
 *
 * 这是判断「前端能不能用」的最直接方式：
 * 页面渲染出来 = 界面可用；报错 = 缺接口或有 bug。
 */
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const errors = [];
const apiCalls = [];
page.on('pageerror', (e) => errors.push(`[js] ${e.message.slice(0, 120)}`));
page.on('response', (res) => {
  const u = res.url();
  if (u.includes('/api/')) {
    apiCalls.push({ status: res.status(), path: u.split('/api/')[1]?.split('?')[0] });
  }
});

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(1500);
if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
}
await page.waitForTimeout(2000);

// 进入技术方案
const rows = page.locator('.secondary-menu-row');
if (await rows.count() > 0) {
  await rows.first().click();
  await page.waitForTimeout(5000);
}

// 抓当前页面信息
const info = await page.evaluate(() => {
  const text = document.body.innerText || '';
  // 找步骤指示
  const stepMatch = text.match(/STEP\s*\d+/g) || [];
  // 找按钮
  const buttons = Array.from(document.querySelectorAll('button'))
    .map((b) => (b.textContent || '').trim())
    .filter((t) => t && t.length < 20)
    .slice(0, 15);
  return { steps: [...new Set(stepMatch)], buttons, textLen: text.length, textHead: text.slice(0, 600) };
});

console.log('=== 页面状态 ===');
console.log('步骤指示:', info.steps.join(', ') || '(无)');
console.log('按钮:', info.buttons.join(' | '));
console.log('\n=== 页面文本 ===');
console.log(info.textHead);
console.log('\n=== JS 错误 ===');
console.log(errors.length ? errors.slice(0, 6).join('\n') : '(无)');
console.log('\n=== API 调用 ===');
const failed = apiCalls.filter((c) => c.status >= 400);
console.log(`总计 ${apiCalls.length} 次，失败 ${failed.length} 次`);
if (failed.length) {
  const uniq = [...new Set(failed.map((f) => `${f.status} ${f.path}`))];
  console.log(uniq.slice(0, 12).join('\n'));
}

await page.screenshot({ path: '/tmp/shots/pages-home.png', fullPage: false });
await browser.close();
