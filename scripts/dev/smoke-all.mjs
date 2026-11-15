/**
 * 全站冒烟测试（一级菜单 + 二级页面）
 *
 * 用途：改完代码后快速确认没有把页面搞崩。
 *
 * 检查三类问题：
 *   1. 白屏（.app-shell 消失）
 *   2. JS 运行时错误
 *   3. 接口 4xx/5xx
 *
 * 用法（在服务器容器内）：
 *   node scripts/dev/smoke-all.mjs
 *
 * 前置：API(8080) + Vite(5173) 已启动，见 run-all.sh --bg
 */
import { chromium } from 'playwright';

const BASE = process.env.SMOKE_BASE || 'http://127.0.0.1:5173';
const EMAIL = process.env.SMOKE_EMAIL || 'admin@local';
const PASSWORD = process.env.SMOKE_PASSWORD || 'admin12345';

const MENUS = ['内容生成', '模版设置', '知识库', '标书检查', '其他功能', '设置'];
const SUB_WHITELIST = {
  内容生成: ['投标文件', '可行性研究报告Beta'],
  知识库: ['文档知识库', '资信库', '图片知识库'],
  标书检查: ['标书查重', '废标项检查', '暗标合规检查', 'AI评标'],
  其他功能: ['Word批量转换（双层PDF、图片）', '插件管理', '产物库', '精选资源'],
};

const browser = await chromium.launch({ args: ['--no-sandbox'] });
const page = await browser.newPage({ viewport: { width: 1680, height: 1200 } });

const errors = [];
const apiFails = [];
page.on('pageerror', (e) => errors.push(e.message.slice(0, 140)));
page.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400 && r.status() !== 401) {
    apiFails.push(`${r.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
  }
});

await page.goto(BASE, { waitUntil: 'networkidle' });
await page.waitForTimeout(1500);

if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', EMAIL);
  await page.fill('.yb-login-field input[type="password"]', PASSWORD);
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 25000 });
}
await page.waitForTimeout(3000);

let pass = 0;
let fail = 0;
const failures = [];

function record(ok, label, detail) {
  if (ok) {
    pass++;
    console.log(`  ✓ ${label}`);
  } else {
    fail++;
    failures.push({ label, detail });
    console.log(`  ✗ ${label} → ${detail}`);
  }
}

for (const menu of MENUS) {
  console.log(`\n【${menu}】`);
  errors.length = 0;
  apiFails.length = 0;

  const menuBtn = page.locator('.sidebar').getByText(menu, { exact: true }).first();
  if (await menuBtn.count() === 0) {
    record(false, menu, '侧边栏找不到该菜单');
    continue;
  }
  await menuBtn.click({ timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(2500);
  record(errors.length === 0 && apiFails.length === 0, `${menu} 入口`,
    [...errors, ...apiFails].slice(0, 2).join(' | '));

  // 读取主区二级卡片
  const cards = await page.evaluate(() =>
    Array.from(document.querySelectorAll('.secondary-menu-row'))
      .map((r) => r.querySelector('strong')?.textContent?.trim() || '')
      .filter(Boolean),
  );

  const expected = SUB_WHITELIST[menu] || [];
  if (expected.length > 0 && cards.length === 0) {
    record(false, `${menu} 二级列表`, '二级入口为空');
    continue;
  }

  for (const card of cards) {
    errors.length = 0;
    apiFails.length = 0;

    // 回列表页（上一次点进去后已切走）
    if (!await page.evaluate(() => Boolean(document.querySelector('.secondary-menu-list-panel')))) {
      await page.locator('.sidebar').getByText(menu, { exact: true }).first().click({ timeout: 5000 }).catch(() => {});
      await page.waitForTimeout(1800);
    }

    const row = page.locator('.secondary-menu-row').filter({ hasText: card }).first();
    if (await row.count() === 0) {
      record(false, card, '找不到卡片');
      continue;
    }
    await row.click({ timeout: 5000 }).catch(() => {});
    await page.waitForTimeout(4000);

    const info = await page.evaluate(() => {
      const t = document.body.innerText || '';
      return {
        len: t.length,
        hasApp: Boolean(document.querySelector('.app-shell')),
        stillList: Boolean(document.querySelector('.secondary-menu-list-panel')),
        hasBadText: /undefined|NaN|\[object Object\]/.test(t.slice(0, 3000)),
      };
    });

    const noError = errors.length === 0 && apiFails.length === 0;
    // 「未进入」+ 无报错 = notice 占位项（点击只弹提示），算正常
    const noticeOnly = info.stillList && noError;
    const ok = info.hasApp && noError && !info.hasBadText
      && (noticeOnly || (!info.stillList && info.len > 200));

    const label = `${card} ${noticeOnly ? '(未开放提示)' : info.stillList ? '(未进入)' : `(${info.len} 字)`}`;
    record(ok, label,
      [info.hasBadText ? '页面含 undefined/NaN' : '', ...errors, ...apiFails].filter(Boolean).slice(0, 2).join(' | '));
  }
}

console.log(`\n${'='.repeat(50)}`);
console.log(`结果：${pass} 通过 / ${fail} 失败`);
if (fail > 0) {
  console.log('\n失败明细：');
  for (const f of failures) console.log(`  - ${f.label}: ${f.detail}`);
}

await browser.close();
process.exit(fail > 0 ? 1 : 0);
