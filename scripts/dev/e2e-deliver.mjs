/**
 * 端到端交付验证：招标文件 → 标书 docx
 *
 * 真实点界面。AI 生成段落因为 API Key 无效会失败，
 * 这里用直接调用同一套后端接口的方式注入内容，验证「交付」环节。
 */
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://127.0.0.1:5173';
const SHOT = '/tmp/shots/deliver';
fs.rmSync(SHOT, { recursive: true, force: true });
fs.mkdirSync(SHOT, { recursive: true });

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1680, height: 1100 } });
const errors = [], apiFails = [];
page.on('pageerror', (e) => errors.push(e.message.slice(0, 180)));
page.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400 && r.status() !== 401) {
    apiFails.push(`${r.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
  }
});

let n = 0;
const shot = (name) => page.screenshot({ path: `${SHOT}/${String(++n).padStart(2, '0')}-${name}.png` }).catch(() => {});
const txt = () => page.evaluate(() => document.body.innerText || '');

await page.goto(BASE, { waitUntil: 'networkidle' });
await page.waitForTimeout(2000);
if (await page.$('.yb-login-card')) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 25000 });
}
await page.waitForTimeout(2500);

// 进入投标文件
await page.locator('.sidebar').getByText('内容生成', { exact: true }).first().click();
await page.waitForTimeout(1800);
await page.locator('.secondary-menu-row').filter({ hasText: '投标文件' }).first().click();
await page.waitForTimeout(5000);

const t1 = await txt();
console.log('[STEP 01] 文件名:', /tender\.docx/.test(t1) ? '有' : '无', '| 字数:', (t1.match(/(\d{3,})\s*字/) || [])[1] || '?');
await shot('step01');

// 逐页点「下一步」，记录每一步
const steps = [];
for (let i = 0; i < 6; i++) {
  const next = page.getByText('下一步', { exact: true }).first();
  if (await next.count() === 0) break;
  const disabled = await next.evaluate((el) => el.disabled || el.className.includes('disabled')).catch(() => false);
  if (disabled) {
    const tip = await next.evaluate((el) => el.getAttribute('title') || el.closest('[title]')?.getAttribute('title') || '').catch(() => '');
    steps.push({ step: i + 2, advanced: false, reason: tip || '按钮禁用' });
    break;
  }
  await next.click().catch(() => {});
  await page.waitForTimeout(4500);
  const t = await txt();
  const stepLabel = ((t.match(/STEP\s*0\d/) || [])[0]) || '?';
  steps.push({ step: i + 2, advanced: true, label: stepLabel });
  await shot(`step-${i + 2}-${stepLabel.replace(/\s/g, '')}`);
}

console.log('\n[流程推进]');
for (const s of steps) {
  console.log(`  ${s.advanced ? '✓' : '✗'} 第 ${s.step} 步 ${s.advanced ? '→ ' + s.label : '被阻止: ' + s.reason}`);
}

// 产物库看导出结果
await page.locator('.sidebar').getByText('其他功能', { exact: true }).first().click();
await page.waitForTimeout(1800);
await page.locator('.secondary-menu-row').filter({ hasText: '产物库' }).first().click();
await page.waitForTimeout(6000);
const t2 = await txt();
const hasDocx = /\.docx/.test(t2);
console.log('\n[产物库] 有 docx:', hasDocx ? '有' : '无');
if (hasDocx) {
  const m = t2.match(/[^\s]*\.docx[^\s]*/g);
  console.log('  产物:', [...new Set(m || [])].slice(0, 3).join(', '));
}
await shot('assets');

console.log('\n[异常] JS:', errors.length ? [...new Set(errors)].slice(0, 2).join(' | ') : '无');
console.log('[异常] 接口:', apiFails.length ? [...new Set(apiFails)].slice(0, 3).join(' | ') : '无');

await browser.close();
