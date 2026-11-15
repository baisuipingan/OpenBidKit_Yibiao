/**
 * 端到端验证：浏览器 → Go API → PostgreSQL。
 *
 * 验证内容：
 *   1. web bridge 正确挂载（不是 mock）
 *   2. 登录能拿到 token
 *   3. 能通过 HTTP 调通 technical-plan/load-state
 *   4. 错误处理正确
 *
 * 用法：
 *   BASE=http://127.0.0.1:5173 node scripts/dev/verify-e2e.mjs
 */

import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:5173';
const TENANT = process.env.TENANT || '8bfe9282-6715-48b5-845e-170585dc75ae';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const EMAIL = process.env.EMAIL || 'admin@local';
const PASSWORD = process.env.PASSWORD || 'admin12345';

const log = (...a) => console.log(...a);
const fail = (m) => {
  console.error('❌', m);
  process.exitCode = 1;
};

const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage();

const errors = [];
page.on('pageerror', (e) => errors.push(`[pageerror] ${e.message}`));
page.on('console', (m) => {
  if (m.type() === 'error') errors.push(`[console.error] ${m.text().slice(0, 300)}`);
});

log('=== 1. 打开页面 ===');
await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });

const env = await page.evaluate(() => ({
  hasBridge: typeof window.yibiao === 'object' && window.yibiao !== null,
  isMock: window.yibiao?.__mock === true,
  platform: window.yibiao?.platform,
  appName: window.yibiao?.appName,
}));
log(JSON.stringify(env, null, 2));

if (!env.hasBridge) fail('bridge 未挂载');
if (env.isMock) fail('仍是 mock 模式，应为 web');
if (env.platform !== 'web') fail(`platform 应为 web，实际 ${env.platform}`);

log('\n=== 2. 登录 ===');
const loginResult = await page.evaluate(
  async ({ base, tenant, email, password }) => {
    const res = await fetch(`${base}/api/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tenantId: tenant, email, password }),
    });
    const body = await res.json();
    return { status: res.status, body };
  },
  { base: BASE, tenant: TENANT, email: EMAIL, password: PASSWORD },
);

if (!loginResult.body?.ok) {
  fail(`登录失败: ${JSON.stringify(loginResult.body)}`);
} else {
  log('✓ 登录成功');
  log('  user:', loginResult.body.data.user.email);
  log('  role:', loginResult.body.data.user.role);
}

// 把 token 存进 localStorage，模拟真实登录流程
await page.evaluate(
  ({ tokens, project }) => {
    localStorage.setItem('yb_access_token', tokens.accessToken);
    localStorage.setItem('yb_refresh_token', tokens.refreshToken);
    localStorage.setItem('yb_current_project', project);
  },
  { tokens: loginResult.body.data, project: PROJECT },
);

log('\n=== 3. 通过 bridge 调真实接口 ===');
const loadResult = await page.evaluate(async () => {
  try {
    const data = await window.yibiao.technicalPlan.loadState();
    return { ok: true, data };
  } catch (e) {
    return { ok: false, code: e.code, message: e.message };
  }
});

if (!loadResult.ok) {
  fail(`loadState 失败: ${loadResult.code} ${loadResult.message}`);
} else {
  log('✓ technicalPlan.loadState() 返回真实数据');
  log('  step:', loadResult.data.step);
  log('  outline 节点数:', loadResult.data.outline?.length ?? 0);
  log('  contentSections 数:', loadResult.data.contentSections?.length ?? 0);
}

log('\n=== 4. 验证错误处理 ===');
const errResult = await page.evaluate(async () => {
  localStorage.setItem('yb_current_project', '00000000-0000-0000-0000-000000000000');
  try {
    await window.yibiao.technicalPlan.loadState();
    return { threw: false };
  } catch (e) {
    return { threw: true, code: e.code, message: e.message, name: e.name };
  } finally {
    localStorage.setItem('yb_current_project', 'f77d5870-aafc-47a1-a267-27f7f881869b');
  }
});

if (!errResult.threw) {
  fail('越权访问应该抛错但没有');
} else {
  log('✓ 错误正确抛出');
  log('  name:', errResult.name);
  log('  code:', errResult.code);
  log('  message:', errResult.message);
}

log('\n=== 5. 页面控制台错误 ===');
if (errors.length === 0) {
  log('（无）');
} else {
  errors.slice(0, 10).forEach((e) => log(e));
}

await page.screenshot({ path: '/tmp/shots/e2e.png' });
log('\n截图: /tmp/shots/e2e.png');

await browser.close();

if (!process.exitCode) {
  log('\n' + '='.repeat(56));
  log('✅ 端到端验证通过：浏览器 → Go → PostgreSQL');
  log('='.repeat(56));
}
