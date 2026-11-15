/** 检查技术方案各页能否显示已有数据 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1400 } });

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
await page.waitForTimeout(2000);

// 检查 localStorage 里的项目 ID
const pid = await page.evaluate(() => localStorage.getItem('yb_current_project'));
console.log('项目 ID 已注入:', pid ? `${pid.slice(0, 8)}...` : '✗ 未注入');

// 检查 load-state 是否成功
const stateOk = await page.evaluate(async () => {
  try {
    const r = await window.yibiao.technicalPlan.loadState();
    return {
      ok: true,
      outlineCount: r?.outline?.length || 0,
      tenderFile: r?.tenderFileName || '',
      hasContent: (r?.outline || []).filter((n) => n.content).length,
    };
  } catch (e) {
    return { ok: false, error: String(e).slice(0, 100) };
  }
});
console.log('load-state:', JSON.stringify(stateOk));

// 检查配置加载
const cfgOk = await page.evaluate(async () => {
  try {
    const c = await window.yibiao.config.load();
    return { ok: true, provider: c?.text_model_provider, model: c?.model_name, hasKey: Boolean(c?.api_key) };
  } catch (e) {
    return { ok: false, error: String(e).slice(0, 100) };
  }
});
console.log('config.load:', JSON.stringify(cfgOk));

// 检查资产列表
const assetOk = await page.evaluate(async () => {
  try {
    const a = await window.yibiao.assets.list({ kind: 'image' });
    return { ok: true, count: a?.assets?.length || 0 };
  } catch (e) {
    return { ok: false, error: String(e).slice(0, 100) };
  }
});
console.log('assets.list:', JSON.stringify(assetOk));

console.log('\n接口失败:', fails.length ? [...new Set(fails)].join(' | ') : '无');
await page.screenshot({ path: '/tmp/shots/step-detail.png', fullPage: false });
await browser.close();
