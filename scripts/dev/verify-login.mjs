/**
 * 验证登录流程：未登录 → 登录页 → 登录 → 主界面。
 *
 * 用法：node scripts/dev/verify-login.mjs
 */

import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const EMAIL = process.env.EMAIL || 'admin@local';
const PASSWORD = process.env.PASSWORD || 'admin12345';

const OUT = '/tmp/shots';
fs.mkdirSync(OUT, { recursive: true });

const log = (...a) => console.log(...a);
const fail = (m) => {
  console.error('❌', m);
  process.exitCode = 1;
};

const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

const errors = [];
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`));

log('=== 1. 首次访问（应显示登录页）===');
await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(2000);

const loginVisible = await page.evaluate(() => ({
  hasLoginForm: Boolean(document.querySelector('.yb-login-card')),
  hasAppShell: Boolean(document.querySelector('.app-shell')),
  bodyText: (document.body.innerText || '').slice(0, 120),
}));
log(JSON.stringify(loginVisible, null, 2));

if (!loginVisible.hasLoginForm) fail('未显示登录页');
if (loginVisible.hasAppShell) fail('未登录却渲染了主界面');

await page.screenshot({ path: `${OUT}/login-page.png` });

log('\n=== 2. 填写并登录 ===');
await page.fill('.yb-login-field input[type="text"]', EMAIL);
await page.fill('.yb-login-field input[type="password"]', PASSWORD);
await page.click('.yb-login-submit');

// 等待主界面出现
try {
  await page.waitForSelector('.app-shell', { timeout: 20000 });
  log('✓ 登录成功，主界面已渲染');
} catch {
  const txt = await page.evaluate(() => (document.body.innerText || '').slice(0, 300));
  fail(`主界面未出现。当前页面内容: ${txt}`);
}

const afterLogin = await page.evaluate(() => ({
  hasAppShell: Boolean(document.querySelector('.app-shell')),
  hasSidebar: Boolean(document.querySelector('.sidebar')),
  navItems: Array.from(document.querySelectorAll('.nav-copy'))
    .map((el) => el.textContent?.trim())
    .filter(Boolean)
    .slice(0, 8),
  tokenStored: Boolean(localStorage.getItem('yb_access_token')),
}));
log(JSON.stringify(afterLogin, null, 2));

await page.screenshot({ path: `${OUT}/after-login.png` });

log('\n=== 3. 刷新页面（应保持登录）===');
await page.reload({ waitUntil: 'networkidle' });
await page.waitForTimeout(2500);

const afterReload = await page.evaluate(() => ({
  hasAppShell: Boolean(document.querySelector('.app-shell')),
  hasLoginForm: Boolean(document.querySelector('.yb-login-card')),
}));
log(JSON.stringify(afterReload, null, 2));

if (!afterReload.hasAppShell) fail('刷新后丢失登录态');
if (afterReload.hasLoginForm) fail('刷新后又出现登录页');

await page.screenshot({ path: `${OUT}/after-reload.png` });

log('\n=== 4. 页面错误 ===');
if (errors.length === 0) log('（无）');
else errors.slice(0, 10).forEach((e) => log(e));

await browser.close();

if (!process.exitCode) {
  log('\n' + '='.repeat(56));
  log('✅ 登录流程验证通过');
  log('='.repeat(56));
}
