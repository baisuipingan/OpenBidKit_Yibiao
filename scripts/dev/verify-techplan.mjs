/** 验证技术方案页：进入 → 看步骤 → 看目录 */
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const errors = [];
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message.slice(0, 150)}`));

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(1500);
if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
}
await page.waitForTimeout(2000);

// 点二级菜单第一项（投标文件）
const rows = page.locator('.secondary-menu-row');
console.log('二级菜单项数:', await rows.count());
if (await rows.count() > 0) {
  await rows.first().click();
  await page.waitForTimeout(6000);
}

const state = await page.evaluate(() => ({
  text: (document.body.innerText || '').slice(0, 1500),
  stepItems: Array.from(document.querySelectorAll('[class*="step"]')).map(e => (e.textContent || '').trim().slice(0, 30)).filter(Boolean).slice(0, 10),
}));

console.log('\n=== 页面内容 ===');
console.log(state.text);
console.log('\n=== 错误 ===');
console.log(errors.length ? errors.slice(0, 5).join('\n') : '（无）');

await page.screenshot({ path: '/tmp/shots/techplan.png', fullPage: false });
await browser.close();
