/**
 * UI 审计：逐页截图，供设计评估用。
 * 统一 1440x900（常见笔记本分辨率），不用超宽屏以免美化布局问题。
 */
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://127.0.0.1:5173';
const DIR = '/tmp/shots/ui';
fs.rmSync(DIR, { recursive: true, force: true });
fs.mkdirSync(DIR, { recursive: true });

const PAGES = [
  ['01-login', null, null],
  ['02-bid-step01', '内容生成', '投标文件'],
  ['03-bid-step02', '内容生成', '投标文件'],
  ['04-bid-step03', '内容生成', '投标文件'],
  ['05-bid-step04', '内容生成', '投标文件'],
  ['06-feasibility', '内容生成', '可行性研究报告Beta'],
  ['07-templates', '模版设置', null],
  ['08-kb-docs', '知识库', '文档知识库'],
  ['09-kb-credential', '知识库', '资信库'],
  ['10-dup-check', '标书检查', '标书查重'],
  ['11-rejection', '标书检查', '废标项检查'],
  ['12-blind', '标书检查', '暗标合规检查'],
  ['13-assets', '其他功能', '产物库'],
  ['14-settings', '设置', null],
];

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });

// 登录页先截
await page.goto(BASE, { waitUntil: 'networkidle' });
await page.waitForTimeout(2500);
await page.screenshot({ path: `${DIR}/01-login.png` });

if (await page.$('.yb-login-card')) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 25000 });
}
await page.waitForTimeout(3000);

for (const [name, menu, sub] of PAGES.slice(1)) {
  if (!menu) continue;
  await page.locator('.sidebar').getByText(menu, { exact: true }).first().click({ timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(2200);
  if (sub) {
    const row = page.locator('.secondary-menu-row').filter({ hasText: sub }).first();
    if (await row.count() > 0) { await row.click().catch(() => {}); await page.waitForTimeout(4500); }
  }
  await page.screenshot({ path: `${DIR}/${name}.png` });
}

// 投标文件页多截几步（关键流程）
await page.locator('.sidebar').getByText('内容生成', { exact: true }).first().click().catch(() => {});
await page.waitForTimeout(2000);
await page.locator('.secondary-menu-row').filter({ hasText: '投标文件' }).first().click().catch(() => {});
await page.waitForTimeout(4500);

// 回 STEP01
for (let i = 0; i < 6; i++) {
  const t = await page.evaluate(() => document.body.innerText || '');
  if (/STEP 01/.test(t)) break;
  const prev = page.getByText('上一步', { exact: true }).first();
  if (await prev.count() > 0) { await prev.click().catch(() => {}); await page.waitForTimeout(2200); } else break;
}
await page.screenshot({ path: `${DIR}/02-bid-step01.png` });

for (let i = 2; i <= 5; i++) {
  const next = page.getByText('下一步', { exact: true }).first();
  if (await next.count() === 0) break;
  await next.click().catch(() => {});
  await page.waitForTimeout(5000);
  await page.screenshot({ path: `${DIR}/0${i}-bid-step0${i}.png` });
}

console.log('截图完成:', fs.readdirSync(DIR).length, '张');
await browser.close();
