/**
 * 验证 Agent 提问应答闭环：
 *  1. Agent 调 ask_user → 收到 user_question 事件
 *  2. 脚本回答 → 调 /api/agent-tools/answer
 *  3. Agent 收到答案 → 继续执行
 */
const API = 'http://127.0.0.1:8080';
const PROJECT = 'f77d5870-aafc-47a1-a267-27f7f881869b';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || '';
const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${auth.accessToken}`, 'X-Project-Id': PROJECT };

// 构造一个必须提问的场景：关键信息缺失且影响重大
const prompt = `你要为客户写一份技术方案的项目概况章节。

但是：招标文件里**没有给出项目预算金额**，而项目概况必须写明预算。

请先用 ask_user 工具问我：
- 问题："项目预算金额是多少？"
- 选项：["860 万元", "850 万元", "不确定，留空"]
- 默认值："不确定，留空"
- 原因："招标文件未给出预算，影响项目概况的完整性"

拿到我的回答后，把答案写进 note.md（格式：预算：<答案>）。

注意：必须先调用 ask_user 并等待回答，不要自己编造预算。`;

const res = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST', headers,
  body: JSON.stringify({ provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL }, prompt }),
});

const reader = res.body.getReader();
const dec = new TextDecoder();
let buf = '';
let done = false;
let sessionId = '';
let questionHandled = 0;
let answered = false;

const loop = async () => {
  while (!done) {
    const { done: rd, value } = await reader.read();
    if (rd) break;
    buf += dec.decode(value, { stream: true });
    const blocks = buf.split('\n\n');
    buf = blocks.pop() || '';
    for (const b of blocks) {
      const lines = b.split('\n');
      const ev = lines.find((l) => l.startsWith('event: '));
      const dt = lines.find((l) => l.startsWith('data: '));
      if (!dt) continue;
      const name = ev ? ev.slice(7).trim() : '';
      let p; try { p = JSON.parse(dt.slice(6)); } catch { continue; }

      if (name === 'done') { done = true; return; }
      if (name === 'error') { log('❌ 任务错误:', p.message); done = true; return; }

      if (name === 'agent') {
        if (p.type === 'tool_start' && p.toolName === 'ask_user') {
          log(`  [追问] ${p.args?.question || ''}`);
        }
        if (p.type === 'user_question') {
          questionHandled++;
          sessionId = p.sessionId || '';
          log(`  🔔 收到提问事件:`);
          log(`     问题: ${p.question}`);
          log(`     选项: ${JSON.stringify(p.options)}`);
          log(`     默认: ${p.defaultAnswer}`);
          log(`     session: ${sessionId.slice(0, 8)}...`);

          // 回答
          if (!answered && sessionId) {
            answered = true;
            const ans = '860 万元';
            log(`  → 回答: ${ans}`);
            const ar = await fetch(`${API}/api/agent-tools/answer`, {
              method: 'POST', headers,
              body: JSON.stringify({ sessionId, questionId: p.questionId, answer: ans }),
            });
            const ab = await ar.json();
            if (ab.ok) log('  ✓ 应答已接受');
            else log('  ✗ 应答失败:', ab.error);
          }
        }
        if (p.type === 'tool_end' && p.toolName === 'ask_user') {
          const txt = JSON.stringify(p.result || {}).slice(0, 150);
          log(`  [追问结果] ${txt}`);
        }
        if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
log(`收到提问: ${questionHandled} 次`);
log(`已应答: ${answered ? '是' : '否'}`);
if (questionHandled > 0 && answered) {
  log('✅ Agent 提问应答闭环打通');
} else if (questionHandled > 0) {
  log('⚠️ 有提问但未成功应答');
} else {
  log('❌ 没有触发提问（可能模型自行判断不需要问）');
}
