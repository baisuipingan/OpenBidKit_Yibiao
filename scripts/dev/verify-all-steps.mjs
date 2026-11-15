/**
 * 遍历技术方案的全部步骤页，检查渲染与接口。
 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const errors = [];
const fails = [];
page.on('pageerror', (e) => errors.push(e.message.slice(0, 100)));
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
await page.waitForTimeout(1500);

const rows = page.locator('.secondary-menu-row');
if (await rows.count() > 0) { await rows.first().click(); await page.waitForTimeout(5000); }

// 逐步点「下一步」，观察每一步的渲染
const steps = [];
for (let i = 0; i < 6; i++) {
  const state = await page.evaluate(() => {
    const t = document.body.innerText || '';
    const m = t.match(/STEP\s*(\d+)/);
    return {
      step: m ? m[1] : '?',
      head: t.slice(t.indexOf('STEP') > 0 ? t.indexOf('STEP') : 0).slice(0, 120).replace(/\n+/g, ' '),
      hasNext: Array.from(document.querySelectorAll('button')).some((b) => (b.textContent || '').trim() === '下一步'),
    };
  });
  steps.push(state);
  console.log(`STEP ${state.step}: ${state.head.slice(0, 100)}`);

  if (!state.hasNext) { console.log('  （没有下一步按钮，停止）'); break; }

  // 点下一步
  const clicked = await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button')).find((b) => (b.textContent || '').trim() === '下一步');
    if (btn && !btn.disabled) { btn.click(); return true; }
    return false;
  });
  if (!clicked) { console.log('  （下一步被禁用，停止）'); break; }
  await page.waitForTimeout(4000);
}

console.log('\n=== 汇总 ===');
console.log(`走过步骤: ${steps.map((s) => s.step).join(' → ')}`);
console.log(`JS 错误: ${errors.length ? [...new Set(errors)].slice(0, 5).join(' | ') : '无'}`);
console.log(`接口失败（非401）: ${fails.length ? [...new Set(fails)].slice(0, 6).join(' | ') : '无'}`);

await browser.close();
