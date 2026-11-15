/** UI 改造后验收：逐页截图 + 关键指标检查 */
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://127.0.0.1:5173';
const DIR = '/tmp/shots/ui-final';
fs.rmSync(DIR, { recursive: true, force: true });
fs.mkdirSync(DIR, { recursive: true });

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const fails = [];
page.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400 && r.status() !== 401) fails.push(`${r.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
});

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

const checks = [];

// 投标文件
await page.locator('.sidebar').getByText('内容生成', { exact: true }).first().click();
await page.waitForTimeout(1800);
await page.locator('.secondary-menu-row').filter({ hasText: '投标文件' }).first().click();
await page.waitForTimeout(6000);
await page.screenshot({ path: `${DIR}/02-bid.png` });

checks.push(await page.evaluate(() => {
  const bar = document.querySelector('.process-stepbar');
  const steps = bar?.querySelectorAll('.process-step') || [];
  const dupTitle = /第[一二三四五六七八九十]+章\s*第[一二三四五六七八九十]+章/.test(document.body.innerText || '');
  const toolbar = document.querySelector('.floating-toolbar');
  const toolbarPos = toolbar ? getComputedStyle(toolbar).position : 'none';
  return {
    name: '投标文件',
    steps: steps.length,
    stepLabels: Array.from(steps).map(s => s.querySelector('.process-step-label')?.textContent).filter(Boolean),
    titleDup: dupTitle,
    toolbarPos,
    railCollapsed: document.querySelector('.outline-generation-workspace')?.classList.contains('is-rail-collapsed') ?? null,
  };
}));

// 生成设置
await page.getByText('上一步', { exact: true }).first().click().catch(() => {});
await page.waitForTimeout(4000);
await page.screenshot({ path: `${DIR}/03-settings.png` });
checks.push(await page.evaluate(() => {
  const tabs = Array.from(document.querySelectorAll('.generation-settings-tab-label')).map(e => e.textContent);
  return { name: '生成设置', tabs, hasSlang: /写嘛|长嘛样|插图吗|我有方案/.test(document.body.innerText || '') };
}));

// 知识库
await page.locator('.sidebar').getByText('知识库', { exact: true }).first().click();
await page.waitForTimeout(2000);
await page.locator('.secondary-menu-row').filter({ hasText: '文档知识库' }).first().click();
await page.waitForTimeout(5000);
await page.screenshot({ path: `${DIR}/04-kb.png` });
checks.push(await page.evaluate(() => ({
  name: '知识库',
  stats: document.querySelectorAll('.knowledge-stat').length,
  hasHint: Boolean(document.querySelector('.knowledge-upload-hint')),
})));

// 资信库（先回到知识库一级菜单，二级菜单才会展开）
await page.locator('.sidebar').getByText('知识库', { exact: true }).first().click();
await page.waitForTimeout(2000);
await page.locator('.secondary-menu-row').filter({ hasText: '资信库' }).first().click();
await page.waitForTimeout(4500);
await page.screenshot({ path: `${DIR}/05-credential.png` });
checks.push(await page.evaluate(() => ({
  name: '资信库',
  groups: Array.from(document.querySelectorAll('.credential-field-group')).map(e => e.textContent),
  completeness: /已填 \d+ 项/.test(document.body.innerText || ''),
})));

// 模板
await page.locator('.sidebar').getByText('模版设置', { exact: true }).first().click();
await page.waitForTimeout(4500);
await page.screenshot({ path: `${DIR}/06-templates.png` });
checks.push(await page.evaluate(() => ({
  name: '模板',
  systemCards: /系统预设模板/.test(document.body.innerText || ''),
  presetNames: (document.body.innerText.match(/标准投标模板|政府项目模板|暗标专用模板/g) || []).length,
})));

// 产物库
await page.locator('.sidebar').getByText('其他功能', { exact: true }).first().click();
await page.waitForTimeout(1800);
await page.locator('.secondary-menu-row').filter({ hasText: '产物库' }).first().click();
await page.waitForTimeout(5000);
await page.screenshot({ path: `${DIR}/07-assets.png` });
checks.push(await page.evaluate(() => ({
  name: '产物库',
  iconBtns: document.querySelectorAll('.assets-icon-btn').length,
  oldBigBtns: Array.from(document.querySelectorAll('.primary-action')).filter(b => b.textContent?.trim() === '下载').length,
})));

// 设置
await page.locator('.sidebar').getByText('设置', { exact: true }).first().click();
await page.waitForTimeout(4000);
await page.screenshot({ path: `${DIR}/08-settingspage.png` });
checks.push(await page.evaluate(() => ({
  name: '设置',
  hasDesktopOnly: /自动更新渠道|GPU 硬件加速/.test(document.body.innerText || ''),
})));

console.log('=== 验收结果 ===');
for (const c of checks) {
  console.log(JSON.stringify(c));
}
console.log('\n接口异常:', fails.length ? [...new Set(fails)].slice(0, 3).join(' | ') : '无');
await browser.close();
