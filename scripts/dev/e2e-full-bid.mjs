/**
 * 端到端全流程：招标文件 → 交付标书
 *
 * 真实点界面，不用 API 直连。
 * 每一步截图 + 记录耗时/错误。
 */
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://127.0.0.1:5173';
const TENDER = process.env.TENDER_PATH || '/tmp/bidtest/tender.docx';
const SHOT_DIR = '/tmp/shots/e2e2';
const LONG = 15 * 60 * 1000;

fs.rmSync(SHOT_DIR, { recursive: true, force: true });
fs.mkdirSync(SHOT_DIR, { recursive: true });

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1680, height: 1100 } });

const errors = [], apiFails = [], apiLog = [];
page.on('pageerror', (e) => errors.push(e.message.slice(0, 180)));
page.on('response', (r) => {
  const u = r.url();
  if (!u.includes('/api/')) return;
  const p = u.split('/api/')[1]?.split('?')[0] || '';
  apiLog.push(`${r.status()} ${p}`);
  if (r.status() >= 400 && r.status() !== 401) apiFails.push(`${r.status()} ${p}`);
});

let n = 0;
const shot = async (name) => page.screenshot({ path: `${SHOT_DIR}/${String(++n).padStart(2, '0')}-${name}.png` }).catch(() => {});
const log = (m) => console.log(`[${new Date().toISOString().slice(11, 19)}] ${m}`);
const text = () => page.evaluate(() => (document.body.innerText || ''));

const steps = [];
async function step(name, fn) {
  const t0 = Date.now();
  log(`▶ ${name}`);
  errors.length = 0;
  apiFails.length = 0;
  let res;
  try { res = await fn(); } catch (e) { res = { ok: false, reason: String(e).slice(0, 150) }; }
  const errs = [...new Set(errors)].slice(0, 2);
  const fails = [...new Set(apiFails)].slice(0, 2);
  const ok = res?.ok !== false && errs.length === 0 && fails.length === 0;
  const rec = { name, ok, sec: ((Date.now() - t0) / 1000).toFixed(1), reason: res?.reason, errs, fails };
  steps.push(rec);
  log(`${ok ? '✓' : '✗'} ${name} (${rec.sec}s)${rec.reason ? ' → ' + rec.reason : ''}`);
  if (errs.length) log(`   JS: ${errs.join(' | ')}`);
  if (fails.length) log(`   接口: ${fails.join(' | ')}`);
  await shot(name.replace(/[^\w\u4e00-\u9fa5]+/g, '-').slice(0, 20));
  return res;
}

// 等待某个条件成立
async function waitFor(desc, predicate, timeout = LONG) {
  const t0 = Date.now();
  let last = '';
  while (Date.now() - t0 < timeout) {
    await page.waitForTimeout(4000);
    const t = await text();
    if (t !== last) { last = t; }
    if (await predicate(t)) return { ok: true, reason: `${desc}（${((Date.now() - t0) / 1000).toFixed(0)}s）` };
    if (/出错了|解析失败|生成失败/.test(t)) {
      const m = t.match(/(出错了|解析失败|生成失败)[^\n]{0,80}/);
      return { ok: false, reason: m ? m[0] : '页面报错' };
    }
  }
  return { ok: false, reason: `${desc} 超时` };
}

// ---------- 1 ----------
await step('登录', async () => {
  await page.goto(BASE, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  if (await page.$('.yb-login-card')) {
    await page.fill('.yb-login-field input[type="text"]', 'admin@local');
    await page.fill('.yb-login-field input[type="password"]', 'admin12345');
    await page.click('.yb-login-submit');
    await page.waitForSelector('.app-shell', { timeout: 25000 });
  }
  await page.waitForTimeout(2000);
  return { ok: true };
});

// ---------- 2 ----------
await step('进入投标文件', async () => {
  await page.locator('.sidebar').getByText('内容生成', { exact: true }).first().click();
  await page.waitForTimeout(1800);
  await page.locator('.secondary-menu-row').filter({ hasText: '投标文件' }).first().click();
  await page.waitForTimeout(4500);
  return { ok: true, reason: ((await text()).match(/STEP\s*0\d/) || ['?'])[0] };
});

// ---------- 3: 回到第一步 ----------
await step('回到第一步（选择标书）', async () => {
  // 点「首页」回 STEP 01
  for (let i = 0; i < 6; i++) {
    const t = await text();
    if (/STEP 01/.test(t)) return { ok: true };
    const prev = page.getByText('上一步', { exact: true }).first();
    const home = page.getByText('首页', { exact: true }).first();
    if (await prev.count() > 0) await prev.click().catch(() => {});
    else if (await home.count() > 0) await home.click().catch(() => {});
    await page.waitForTimeout(2500);
  }
  const t = await text();
  return /STEP 01/.test(t) ? { ok: true } : { ok: false, reason: '无法回到 STEP 01' };
});

// ---------- 4: 上传招标文件（关键） ----------
await step('上传招标文件', async () => {
  // 用点击「上传」按钮触发 bridge 的浏览器选文件
  // Playwright 用 filechooser 事件捕获
  const chooserPromise = page.waitForEvent('filechooser', { timeout: 15000 }).catch(() => null);

  const uploadBtn = page.getByRole('button', { name: /^上传$|^继续上传$|选择招标文件/ }).first();
  if (await uploadBtn.count() === 0) return { ok: false, reason: '找不到上传按钮' };
  await uploadBtn.click();

  const chooser = await chooserPromise;
  if (!chooser) {
    // 兜底：直接找 input
    const inp = page.locator('input[type="file"]').first();
    if (await inp.count() > 0) {
      await inp.setInputFiles(TENDER);
      return { ok: true, reason: '通过 input 上传' };
    }
    return { ok: false, reason: '未弹出文件选择框' };
  }
  await chooser.setFiles(TENDER);
  return { ok: true, reason: '通过 filechooser 上传' };
});

// ---------- 5: 等解析 ----------
await step('等待招标文件解析', async () => {
  const r = await waitFor('解析完成', (t) =>
    /招标文件内容|解析完成|项目概况/.test(t) && !/尚未导入招标文件/.test(t));
  if (!r.ok) return r;
  const chars = await page.evaluate(() => {
    const m = (document.body.innerText || '').match(/(\d+)\s*字/);
    return m ? m[1] : '?';
  });
  return { ok: true, reason: `解析完成，${chars} 字` };
});

// ---------- 6: 进入下一步 ----------
await step('进入生成设置', async () => {
  const next = page.getByText('下一步', { exact: true }).first();
  if (await next.count() === 0) return { ok: false, reason: '找不到下一步' };
  await next.click();
  await page.waitForTimeout(4000);
  const t = await text();
  return /STEP 02|生成设置/.test(t) ? { ok: true } : { ok: false, reason: '未进入生成设置' };
});

console.log('\n=== 当前进度 ===');
for (const s of steps) console.log(`${s.ok ? '✓' : '✗'} ${s.name}${s.reason ? ' — ' + s.reason : ''}`);
console.log('\n最近接口:', [...new Set(apiLog.slice(-15))].join(', '));
fs.writeFileSync(`${SHOT_DIR}/report.json`, JSON.stringify({ steps, apiLog: apiLog.slice(-80) }, null, 2));
await browser.close();
