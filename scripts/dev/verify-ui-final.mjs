/** 最终 UI 验证：招标文件、目录、正文、配图是否都能在界面显示 */
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

const rows = page.locator('.secondary-menu-row');
if (await rows.count() > 0) { await rows.first().click(); await page.waitForTimeout(6000); }

const info = await page.evaluate(() => {
  const t = document.body.innerText || '';
  return {
    // 是否显示招标文件已导入
    showsTender: t.includes('tender.md') || t.includes('已导入') || t.includes('招标文件内容'),
    // 是否还是「尚未导入」
    stillEmpty: t.includes('尚未导入招标文件'),
    // 关键词检测
    hasProjectName: t.includes('某市人民医院'),
    textHead: t.slice(t.indexOf('STEP') > 0 ? t.indexOf('STEP') : 0).slice(0, 500),
  };
});

console.log('=== 界面状态 ===');
console.log('显示招标文件名:', info.showsTender ? '✓' : '✗');
console.log('仍显示「尚未导入」:', info.stillEmpty ? '✗ 是（问题）' : '✓ 否');
console.log('显示项目名:', info.hasProjectName ? '✓' : '✗');
console.log('\n=== 页面文本 ===');
console.log(info.textHead);
console.log('\n接口失败:', fails.length ? [...new Set(fails)].join(' | ') : '无');

await page.screenshot({ path: '/tmp/shots/final.png', fullPage: false });
await browser.close();
