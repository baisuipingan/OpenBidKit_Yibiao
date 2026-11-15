/** 验证项目切换器 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
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
await page.waitForTimeout(3500);

const switcher = await page.evaluate(() => {
  const el = document.querySelector('.project-switcher-trigger');
  return { exists: Boolean(el), text: el?.textContent?.trim() || '' };
});
console.log('切换器存在:', switcher.exists ? '是' : '否');
console.log('显示项目名:', switcher.text || '(空)');

if (switcher.exists) {
  await page.click('.project-switcher-trigger');
  await page.waitForTimeout(1500);

  const dialog = await page.evaluate(() => {
    const d = document.querySelector('.project-dialog');
    if (!d) return { exists: false };
    const items = Array.from(document.querySelectorAll('.project-item'));
    return {
      exists: true,
      title: d.querySelector('.project-dialog-title')?.textContent || '',
      itemCount: items.length,
      firstMeta: items[0]?.querySelector('.project-item-meta')?.textContent || '',
      hasCreate: Array.from(d.querySelectorAll('button')).some((b) => (b.textContent || '').includes('新建项目')),
      hasRename: Array.from(d.querySelectorAll('button')).some((b) => (b.textContent || '').includes('改名')),
    };
  });

  console.log('');
  console.log('弹窗出现:', dialog.exists ? '是' : '否');
  console.log('标题:', dialog.title);
  console.log('项目数:', dialog.itemCount);
  console.log('首项信息:', dialog.firstMeta);
  console.log('有新建项目:', dialog.hasCreate ? '是' : '否');
  console.log('有改名:', dialog.hasRename ? '是' : '否');

  await page.screenshot({ path: '/tmp/shots/project-dialog.png' });
}

console.log('');
console.log('JS 错误:', errors.length ? errors.join(' | ') : '无');
console.log('接口失败:', fails.length ? [...new Set(fails)].join(' | ') : '无');

await browser.close();
