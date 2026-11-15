/** 验证恢复提示条 UI */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

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
await page.waitForTimeout(3000);

// 进技术方案
const rows = page.locator('.secondary-menu-row');
if (await rows.count() > 0) { await rows.first().click(); await page.waitForTimeout(6000); }

const info = await page.evaluate(() => {
  const banner = document.querySelector('.resume-banner');
  const text = document.body.innerText || '';
  return {
    hasBanner: Boolean(banner),
    bannerTitle: banner?.querySelector('.resume-banner-title')?.textContent || '',
    items: Array.from(document.querySelectorAll('.resume-banner-item')).map((el) => el.textContent?.trim() || ''),
    hasAction: Array.from(document.querySelectorAll('.resume-banner-action')).length,
    pageIsContent: text.includes('正文生成'),
    apiCalled: false,
  };
});

console.log('页面在正文生成:', info.pageIsContent ? '是' : '否');
console.log('恢复提示条:', info.hasBanner ? '显示' : '未显示');
if (info.hasBanner) {
  console.log('标题:', info.bannerTitle);
  console.log('条目:', info.items.length);
  info.items.slice(0, 2).forEach((t) => console.log('  -', t));
  console.log('恢复按钮:', info.hasAction);
  await page.screenshot({ path: '/tmp/shots/resume-banner.png' });
}

console.log('接口失败:', fails.length ? [...new Set(fails)].join(' | ') : '无');
await browser.close();
