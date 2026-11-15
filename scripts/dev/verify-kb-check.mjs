import { chromium } from 'playwright';
const BASE = 'http://127.0.0.1:5173';
const b = await chromium.launch({ args: ['--no-sandbox'] });
const p = await b.newPage({ viewport: { width: 1600, height: 1200 } });
const reqs = [];
p.on('request', (r) => {
  if (r.url().includes('/api/technical-plan/save-reference')) reqs.push(r.postData());
});

await p.goto(BASE, { waitUntil: 'networkidle' });
await p.waitForTimeout(1200);
if (await p.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await p.fill('.yb-login-field input[type="text"]', 'admin@local');
  await p.fill('.yb-login-field input[type="password"]', 'admin12345');
  await p.click('.yb-login-submit');
  await p.waitForSelector('.app-shell', { timeout: 20000 });
}
await p.waitForTimeout(2000);
await p.getByText('投标文件', { exact: false }).first().click();
await p.waitForTimeout(4500);

// 点知识库页签
await p.locator('.generation-settings-tab', { hasText: '知识库' }).first().click();
await p.waitForTimeout(2500);

// 勾选文档
const cb = p.locator('input[type="checkbox"]').first();
if (await cb.count() > 0) {
  await cb.check();
  await p.waitForTimeout(3000);
  console.log('已勾选文档');
  console.log('保存请求:', reqs.length > 0 ? reqs[0] : '(未发出)');

  const after = await p.evaluate(() => {
    const t = document.body.innerText || '';
    return { selected: t.match(/已选择 (\d+) 个文档/)?.[1] || '?', snippet: t.slice(t.indexOf('本次已选'), t.indexOf('本次已选') + 200) };
  });
  console.log('选择状态:', after.selected, '个文档');
  console.log('右栏内容:', after.snippet.replace(/\n+/g, ' | ').slice(0, 150));
}
await p.screenshot({ path: '/tmp/shots/kb-checked.png' });
await b.close();
