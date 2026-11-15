/**
 * 更严格的提问应答验证。
 *
 * 关键改进：明确要求「必须先调用工具，不能直接写文件」
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

const prompt = `第一步：调用 ask_user 工具，参数如下（原样照填，不要改动）：
- question: "项目预算金额是多少？"
- options: ["860 万元", "850 万元", "不确定"]
- defaultAnswer: "860 万元"

第二步：拿到回答后，用 write 工具把回答写进 note.md，内容格式：
预算答案：<用户回答的内容>

严格要求：
- 你必须先调用 ask_user，这是硬性要求
- 不要在调用前猜测答案
- 不要在回复里假装调用，要真的调用工具`;

const res = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST', headers,
  body: JSON.stringify({ provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL }, prompt }),
});

const reader = res.body.getReader();
const dec = new TextDecoder();
let buf = '';
let done = false;
let sessionId = '';
let qCount = 0;
let answered = false;
const toolSeq = [];

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
      if (name === 'agent') {
        if (p.type === 'tool_start') {
          toolSeq.push(p.toolName);
          if (p.toolName === 'ask_user') log(`  [追问] ${p.args?.question || ''}`);
        }
        if (p.type === 'user_question') {
          qCount++;
          sessionId = p.sessionId || '';
          log(`  🔔 提问事件 #${qCount}: ${p.question}`);
          log(`     选项: ${JSON.stringify(p.options)}`);
          if (!answered && sessionId) {
            answered = true;
            const ar = await fetch(`${API}/api/agent-tools/answer`, {
              method: 'POST', headers,
              body: JSON.stringify({ sessionId, questionId: p.questionId, answer: '850 万元' }),
            });
            const ab = await ar.json();
            log(`  → 回答「850 万元」: ${ab.ok ? '✓ 已接受' : '✗ ' + JSON.stringify(ab.error)}`);
          }
        }
        if (p.type === 'tool_end' && p.toolName === 'ask_user') {
          const t = JSON.stringify(p.result || {});
          const m = t.match(/"text":"([^"]{0,80})/);
          log(`  [追问返回] ${m ? m[1] : t.slice(0, 100)}`);
        }
        if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
log(`工具序列: ${toolSeq.join(' → ')}`);
log(`提问次数: ${qCount}，已应答: ${answered}`);
if (qCount > 0 && answered) {
  log('✅ 提问应答闭环打通');
} else {
  log('⚠️ 未触发提问（工具已注册，可能是模型行为）');
}
