/**
 * 验证空转检测在真实 Agent 任务中生效。
 *
 * 构造一个「诱导空转」的任务：让 Agent 反复找一个不存在的东西。
 * 预期：检测到空转 → 注入提示 → Agent 跳出。
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

// 诱导空转：让 Agent 找一个绝不存在的东西
const prompt = `请在工作区里找到名为 "unicorn-config-xyz.json" 的文件并读取它的内容。

要求：
1. 必须找到这个文件
2. 用 bash 或 read 反复尝试
3. 找到后把内容写到 found.txt

注意：这个文件确实存在于某个位置，请多试几次不同路径。`;

log('=== 发起诱导空转的任务 ===');
const res = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST', headers,
  body: JSON.stringify({ provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL }, prompt }),
});

const reader = res.body.getReader();
const decoder = new TextDecoder();
let buf = '';
let done = false;
const toolCalls = [];
const stallEvents = [];
let nudgeApplied = false;

const loop = async () => {
  while (!done) {
    const { done: rd, value } = await reader.read();
    if (rd) break;
    buf += decoder.decode(value, { stream: true });
    const blocks = buf.split('\n\n');
    buf = blocks.pop() || '';
    for (const b of blocks) {
      const lines = b.split('\n');
      const evLine = lines.find((l) => l.startsWith('event: '));
      const dataLine = lines.find((l) => l.startsWith('data: '));
      if (!dataLine) continue;
      const evName = evLine ? evLine.slice(7).trim() : '';
      let p; try { p = JSON.parse(dataLine.slice(6)); } catch { continue; }
      if (evName === 'done') { done = true; return; }
      if (evName === 'error') { log('任务错误:', p.message); done = true; return; }
      if (evName === 'agent') {
        if (p.type === 'tool_start') toolCalls.push(p.toolName);
        else if (p.type === 'stall_detected') {
          stallEvents.push(p);
          log(`  🔴 [空转检出] ${p.reason}（level ${p.level}）`);
        }
        else if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
log(`工具调用总数: ${toolCalls.length}`);
log(`空转检出次数: ${stallEvents.length}`);

if (stallEvents.length > 0) {
  log('\n✅ 空转检测生效');
  for (const e of stallEvents) {
    log(`  - ${e.reason}`);
  }
  // 关键：检出后 Agent 应该停止打转
  if (toolCalls.length < 30) {
    log(`✅ 跳出了空转（共 ${toolCalls.length} 次工具调用，未失控）`);
  } else {
    log(`⚠️ 工具调用较多（${toolCalls.length} 次），可能未完全跳出`);
  }
} else {
  log('\n⚠️ 未检出空转（Agent 可能自己跳出了，或任务太短）');
  log(`工具序列: ${toolCalls.slice(0, 12).join(' → ')}`);
}
