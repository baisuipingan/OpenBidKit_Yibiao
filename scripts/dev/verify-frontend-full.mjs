/**
 * 前端完整流程验证：设置模型 → 技术方案页 → 看目录。
 */
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const log = (...a) => console.log(...a);

const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });

const errors = [];
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`));
page.on('console', (m) => {
  if (m.type() === 'error' && !m.text().includes('401') && !m.text().includes('404')) {
    errors.push(`[console] ${m.text().slice(0, 200)}`);
  }
});

log('=== 1. 打开并登录 ===');
await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });

const needLogin = await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')));
if (needLogin) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
  log('✓ 已登录');
} else {
  log('✓ 已是登录态');
}

log('\n=== 2. 进入技术方案页 ===');
// 侧边栏第一个是内容生成
await page.click('.nav-item');
await page.waitForTimeout(3000);

const pageState = await page.evaluate(() => ({
  url: location.href,
  bodyText: (document.body.innerText || '').slice(0, 800),
  hasSidebar: Boolean(document.querySelector('.sidebar')),
  cards: Array.from(document.querySelectorAll('[class*="card"]')).slice(0, 5).map(c => c.className),
}));

log('当前页面文本:');
log(pageState.bodyText);
await page.screenshot({ path: '/tmp/shots/frontend-techplan.png' });

log('\n=== 3. 检查接口调用 ===');
const apiCalls = [];
page.on('response', (res) => {
  if (res.url().includes('/api/')) {
    apiCalls.push(`${res.status()} ${res.url().replace(/.*\/api\//, '')}`);
  }
});

await page.waitForTimeout(2000);
log('API 调用:');
apiCalls.slice(-15).forEach(c => log('  ' + c));

log('\n=== 4. 错误 ===');
if (errors.length === 0) log('（无）');
else errors.slice(0, 8).forEach(e => log(e));

await browser.close();
log('\n截图: /tmp/shots/frontend-techplan.png');
