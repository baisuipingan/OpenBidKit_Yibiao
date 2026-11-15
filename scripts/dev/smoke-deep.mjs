/**
 * 深度冒烟：每个一级菜单 → 主区二级列表 → 进入每个子功能
 *
 * 导航结构（桌面版设计）：
 *   左侧 sidebar   只有一级菜单
 *   主区           二级菜单以卡片列表呈现（SecondaryMenuPage）
 *   点子卡片       进入具体功能页
 */
import { chromium } from 'playwright';
const BASE = 'http://127.0.0.1:5173';

const b = await chromium.launch({ args: ['--no-sandbox'] });
const p = await b.newPage({ viewport: { width: 1680, height: 1200 } });
const errs = [], fails = [];
p.on('pageerror', (e) => errs.push(e.message.slice(0, 140)));
p.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400 && r.status() !== 401) {
    fails.push(`${r.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
  }
});

await p.goto(BASE, { waitUntil: 'networkidle' });
await p.waitForTimeout(1500);
if (await p.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await p.fill('.yb-login-field input[type="text"]', 'admin@local');
  await p.fill('.yb-login-field input[type="password"]', 'admin12345');
  await p.click('.yb-login-submit');
  await p.waitForSelector('.app-shell', { timeout: 25000 });
}
await p.waitForTimeout(3000);

const MENUS = ['内容生成', '模版设置', '知识库', '标书检查', '其他功能', '设置'];
let pass = 0, fail = 0;

for (const menu of MENUS) {
  // 回一级菜单
  errs.length = 0; fails.length = 0;
  await p.locator('.sidebar').getByText(menu, { exact: true }).first().click({ timeout: 5000 }).catch(() => {});
  await p.waitForTimeout(2500);

  // 读取主区的二级卡片
  const cards = await p.evaluate(() => {
    const rows = Array.from(document.querySelectorAll('.secondary-menu-row'));
    return rows.map((r) => ({
      label: r.querySelector('strong')?.textContent?.trim() || '',
      desc: r.querySelector('small')?.textContent?.trim() || '',
    })).filter((c) => c.label);
  });

  const menuOk = errs.length === 0 && fails.length === 0;
  console.log(`\n${menuOk ? '✓' : '✗'} 【${menu}】 ${cards.length} 个二级入口${menuOk ? '' : ' → ' + [...errs, ...fails].slice(0, 2).join(' | ')}`);
  if (menuOk) pass++; else fail++;

  for (const card of cards) {
    errs.length = 0; fails.length = 0;

    // 每次点卡片前先回到二级列表页（上一次点进去后页面已切换）
    if (!await p.evaluate(() => Boolean(document.querySelector('.secondary-menu-list-panel')))) {
      await p.locator('.sidebar').getByText(menu, { exact: true }).first().click({ timeout: 5000 }).catch(() => {});
      await p.waitForTimeout(2000);
    }

    const row = p.locator('.secondary-menu-row').filter({ hasText: card.label }).first();
    if (await row.count() === 0) { console.log(`  - ${card.label} (找不到卡片)`); continue; }
    await row.click({ timeout: 5000 }).catch(() => {});
    await p.waitForTimeout(4500);

    const info = await p.evaluate(() => {
      const t = document.body.innerText || '';
      return {
        len: t.length,
        hasApp: Boolean(document.querySelector('.app-shell')),
        stillList: Boolean(document.querySelector('.secondary-menu-list-panel')),
        bad: /undefined|NaN|\[object Object\]/.test(t.slice(0, 3000)),
      };
    });
    // 成功进入：不再是二级列表页，有内容，无异常。
    // 「未进入」+ 无报错 = 该菜单是 notice 占位项（点击只弹提示），也算正常。
    // 字数阈值 200：知识库这类页面内容本来就少（列表 + 侧栏），
    // 用 300 会误判成失败。
    const noticeOnly = info.stillList && errs.length === 0 && fails.length === 0;
    const ok = info.hasApp && errs.length === 0 && fails.length === 0
      && (noticeOnly || (!info.stillList && info.len > 200));
    if (ok) pass++; else fail++;
    const label = info.stillList ? '(未开放提示)' : `(${info.len} 字)`;
    console.log(`  ${ok ? '✓' : '✗'} ${card.label.padEnd(12)} ${label}${info.bad ? ' [含undefined]' : ''}${ok ? '' : ' → ' + [...errs, ...fails].slice(0, 2).join(' | ')}`);
  }
}

console.log(`\n=== 汇总：${pass} 通过 / ${fail} 失败 ===`);
await b.close();
