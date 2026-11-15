/** 验证提问弹窗 UI：直接推事件，看弹窗是否出现 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const API = 'http://127.0.0.1:8080';

const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(1200);
if (await page.evaluate(() => Boolean(document.querySelector('.yb-login-card')))) {
  await page.fill('.yb-login-field input[type="text"]', 'admin@local');
  await page.fill('.yb-login-field input[type="password"]', 'admin12345');
  await page.click('.yb-login-submit');
  await page.waitForSelector('.app-shell', { timeout: 20000 });
}
await page.waitForTimeout(4000);
console.log('页面已就绪');

// 用页面的 token 调测试接口
const pushed = await page.evaluate(async () => {
  try {
    return await window.yibiaoBridge ? null : null;
  } catch { return null; }
});

// 直接用 fetch（带页面 token）
const result = await page.evaluate(async (api) => {
  const token = localStorage.getItem('yb_access_token');
  const pid = localStorage.getItem('yb_current_project');
  const r = await fetch(`${api}/api/agent/test-push-question`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      'X-Project-Id': pid || '',
    },
    body: JSON.stringify({
      question: '请确认项目预算金额',
      options: ['860 万元', '850 万元'],
      tenantId: localStorage.getItem('yb_tenant_id') || '',
      userId: JSON.parse(localStorage.getItem('yb_user') || '{}').id || '',
      projectId: pid || '',
    }),
  });
  return await r.json();
}, API);
console.log('推送结果:', JSON.stringify(result).slice(0, 150));

// 等弹窗出现
let appeared = false;
for (let i = 0; i < 20; i++) {
  await page.waitForTimeout(1000);
  const info = await page.evaluate(() => {
    const dialogs = document.querySelectorAll('[role="dialog"]');
    const text = document.body.innerText;
    return {
      count: dialogs.length,
      hasQ: text.includes('请确认项目预算金额'),
      hasOpt1: text.includes('860 万元'),
      hasOpt2: text.includes('850 万元'),
    };
  });
  if (info.hasQ) {
    console.log(`\n✓ 弹窗出现（第 ${i + 1} 秒）`);
    console.log(`  dialog 元素: ${info.count}`);
    console.log(`  含问题文本: ${info.hasQ}`);
    console.log(`  含选项 1: ${info.hasOpt1}`);
    console.log(`  含选项 2: ${info.hasOpt2}`);
    appeared = true;
    await page.screenshot({ path: '/tmp/shots/dialog.png' });
    break;
  }
}

if (!appeared) {
  console.log('\n❌ 弹窗未出现');
  const text = await page.evaluate(() => document.body.innerText.slice(0, 300));
  console.log('页面文本:', text);
}

await browser.close();
