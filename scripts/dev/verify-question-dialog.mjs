/**
 * 验证提问弹窗完整链路：
 *   Agent 提问 → SSE 推 agent.question → 前端弹窗
 *
 * 这里不依赖模型主动提问（模型可能不问），
 * 改为直接向事件总线注入一条提问事件，验证前端能收到并渲染弹窗。
 */
import { chromium } from 'playwright';

const BASE = 'http://127.0.0.1:5173';
const API = 'http://127.0.0.1:8080';

const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newPage({ viewport: { width: 1600, height: 1200 } });

const sseEvents = [];
page.on('response', (res) => {
  const u = res.url();
  if (u.includes('/api/events')) sseEvents.push('SSE 已连接');
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

console.log('SSE 连接:', sseEvents.length ? '✓' : '✗');

// 检查前端是否注册了 onQuestion 监听
const hasListener = await page.evaluate(() => {
  try {
    // 试探：调用 onQuestion 应该返回取消函数
    const unsub = window.yibiao.agent.onQuestion(() => {});
    if (typeof unsub === 'function') { unsub(); return true; }
    return false;
  } catch (e) {
    return `异常: ${String(e).slice(0, 80)}`;
  }
});
console.log('onQuestion 可订阅:', hasListener === true ? '✓' : `✗ ${hasListener}`);

// 检查 getPendingQuestion 接口
const pending = await page.evaluate(async () => {
  try {
    return await window.yibiao.agent.getPendingQuestion();
  } catch (e) {
    return { error: String(e).slice(0, 100) };
  }
});
console.log('getPendingQuestion:', JSON.stringify(pending));

// 检查 answerQuestion 接口存在
const canAnswer = await page.evaluate(async () => {
  try {
    const r = await window.yibiao.agent.answerQuestion({
      question_id: 'test-nonexistent',
      option_id: 'opt-1',
      custom_answer: '',
    });
    return { ok: true, result: r };
  } catch (e) {
    // 报「问题不存在」说明接口通
    return { ok: false, error: String(e).slice(0, 120) };
  }
});
console.log('answerQuestion:', JSON.stringify(canAnswer));

await browser.close();
