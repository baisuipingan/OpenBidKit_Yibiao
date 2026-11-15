/** 验证技术方案页能否显示已有目录 */
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });

const logs = [];
page.on('console', (m) => {
  const t = m.text();
  if (m.type() === 'error' && !t.includes('404') && !t.includes('401')) logs.push(t.slice(0, 200));
});
page.on('pageerror', (e) => logs.push(`[pageerror] ${e.message.slice(0, 200)}`));

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(2000);

// 若在登录页则登录
if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
}

// 点「投标文件」卡片进入技术方案
// 用 nth=0 避免多处文本匹配导致歧义
const card = page.locator('text=投标文件').first();
await card.click({ timeout: 15000 });
await page.waitForTimeout(6000);

const state = await page.evaluate(() => ({
  text: (document.body.innerText || '').slice(0, 1200),
  hasStepNav: Boolean(document.querySelector('[class*="step"]')),
  buttons: Array.from(document.querySelectorAll('button')).map(b => b.textContent?.trim()).filter(Boolean).slice(0, 15),
}));

console.log('=== 页面内容 ===');
console.log(state.text);
console.log('\n=== 按钮 ===');
console.log(state.buttons.join(' | '));

await page.screenshot({ path: '/tmp/shots/outline-page.png', fullPage: false });
console.log('\n=== 错误 ===');
console.log(logs.length ? logs.slice(0, 5).join('\n') : '（无）');

await browser.close();
