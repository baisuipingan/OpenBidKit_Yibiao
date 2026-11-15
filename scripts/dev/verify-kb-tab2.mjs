import { chromium } from 'playwright';
const BASE = 'http://127.0.0.1:5173';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const p = await b.newPage({ viewport: { width: 1600, height: 1200 } });
const fails = [];
p.on('response', (r) => {
  const u = r.url();
  if (u.includes('/api/') && r.status() >= 400 && r.status() !== 401) fails.push(`${r.status()} ${u.split('/api/')[1]?.split('?')[0]}`);
});

await p.goto(BASE, { waitUntil: 'networkidle' });
await p.waitForTimeout(1200);
if (await p.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await p.fill('.yb-login-field input[type="text"]', 'admin@local');
  await p.fill('.yb-login-field input[type="password"]', 'admin12345');
  await p.click('.yb-login-submit');
  await p.waitForSelector('.app-shell', { timeout: 20000 });
}
await p.waitForTimeout(2500);
await p.getByText('投标文件', { exact: false }).first().click();
await p.waitForTimeout(5000);

// 看页签
const tabs = await p.evaluate(() => Array.from(document.querySelectorAll('.generation-settings-tab')).map((e) => e.textContent?.trim() || ''));
console.log('页签:', tabs.join(' | ') || '(无)');

// 点知识库页签
const kb = p.locator('.generation-settings-tab', { hasText: '知识库' }).first();
if (await kb.count() > 0) {
  await kb.click();
  await p.waitForTimeout(3000);

  const info = await p.evaluate(() => {
    const t = document.body.innerText || '';
    const i = t.indexOf('参考知识库') >= 0 ? t.indexOf('参考知识库') : t.indexOf('知识库文档');
    return {
      hasDoc: t.includes('技术方案测试'),
      hasFolder: t.includes('医疗行业素材'),
      hasReference: t.includes('参考知识库') || t.includes('选择知识库'),
      checkboxes: document.querySelectorAll('input[type="checkbox"]').length,
      snippet: i >= 0 ? t.slice(i, i + 400) : t.slice(0, 400),
    };
  });
  console.log('\n=== 知识库页签 ===');
  console.log('显示文档:', info.hasDoc ? '✓' : '✗');
  console.log('显示文件夹:', info.hasFolder ? '✓' : '✗');
  console.log('复选框:', info.checkboxes);
  console.log('\n内容:');
  console.log(info.snippet.replace(/\n+/g, ' | ').slice(0, 350));
}
console.log('\n接口失败:', fails.length ? [...new Set(fails)].slice(0, 4).join(' | ') : '无');
await p.screenshot({ path: '/tmp/shots/kb-tab2.png' });
await b.close();
