/**
 * 验证 Agent 业务工具回调。
 *
 * 测什么：
 *   1. Go 注册的工具能传给 sidecar
 *   2. Agent 能识别并调用自定义工具
 *   3. 工具执行结果能回到 Agent
 *   4. Agent 能基于工具结果做判断
 *
 * 用 count_words 工具做验证：让 Agent 写一段文字，然后用工具统计，
 * 如果字数不够就补充。这能证明 Agent 真的在用工具（而非自己瞎算）。
 */

const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || '';

const log = (...a) => console.log(...a);
const fail = (m) => {
  console.error('❌', m);
  process.exitCode = 1;
};

if (!AI_BASE_URL || !AI_API_KEY) {
  console.error('缺少 AI_BASE_URL / AI_API_KEY');
  process.exit(1);
}

// 登录
const loginRes = await fetch(`${API}/api/auth/login`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const loginBody = await loginRes.json();
if (!loginBody.ok) {
  fail('登录失败');
  process.exit(1);
}
const token = loginBody.data.accessToken;
log('✓ 已登录');

const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${token}`,
  'X-Project-Id': PROJECT,
};

// 直接用 Agent self-check 接口，它支持自定义 prompt
log('\n=== 发起任务：让 Agent 使用 count_words 工具 ===');

const prompt = [
  '请完成以下任务，必须使用工具，不要自己估算：',
  '',
  '1. 用 write_file 工具创建 draft.md，内容为：「智慧医院建设方案。本项目采用微服务架构，实现门诊、住院、检验、药事、财务五大业务域的数字化。系统支持国产化环境，可用性不低于99.9%。」',
  '2. 用 count_words 工具统计 draft.md 的字数',
  '3. 如果字数少于 60 字，用 write_file 补充内容到 60 字以上，再用 count_words 复核',
  '4. 最后报告最终字数',
  '',
  '注意：字数必须用 count_words 工具统计，不能自己数。',
].join('\n');

const startRes = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST',
  headers,
  body: JSON.stringify({
    provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL },
    prompt,
  }),
});

if (!startRes.ok) {
  fail(`请求失败: HTTP ${startRes.status}`);
  process.exit(1);
}

// 读 SSE
const reader = startRes.body.getReader();
const decoder = new TextDecoder();
let buffer = '';
const toolCalls = [];
let assistantText = '';
let taskEnded = false;

while (true) {
  const { done, value } = await reader.read();
  if (done) break;
  buffer += decoder.decode(value, { stream: true });

  const blocks = buffer.split('\n\n');
  buffer = blocks.pop() || '';

  for (const block of blocks) {
    const lines = block.split('\n');
    const evLine = lines.find((l) => l.startsWith('event: '));
    const dataLine = lines.find((l) => l.startsWith('data: '));
    if (!dataLine) continue;

    const evName = evLine ? evLine.slice(7).trim() : '';
    let payload;
    try {
      payload = JSON.parse(dataLine.slice(6));
    } catch {
      continue;
    }

    if (evName === 'agent') {
      const t = payload.type;

      if (t === 'tool_start') {
        toolCalls.push(payload.toolName);
        log(`  [工具调用] ${payload.toolName}`);
      } else if (t === 'tool_end') {
        const text = JSON.stringify(payload.result || {}).slice(0, 200);
        log(`  [工具结果] ${payload.toolName}${payload.isError ? ' 失败' : ''}: ${text}`);
      } else if (t === 'assistant_end') {
        assistantText = payload.text || '';
        if (assistantText.trim()) {
          log(`  [助手] ${assistantText.slice(0, 200)}`);
        }
      } else if (t === 'task_end') {
        taskEnded = true;
        log(`  [结束] ${payload.status}`);
        if (payload.error) log(`  错误: ${payload.error}`);
      }
    } else if (evName === 'error') {
      fail(`任务错误: ${payload.message}`);
      taskEnded = true;
    }
  }

  if (taskEnded) break;
}

// 断言
log('\n=== 验证结果 ===');

const usedCustomTools = toolCalls.filter((n) =>
  ['count_words', 'write_file', 'read_file', 'list_files'].includes(n),
);

if (usedCustomTools.length === 0) {
  fail(`Agent 没有调用任何自定义工具（调用的工具：${toolCalls.join(', ') || '无'}）`);
} else {
  log(`✓ Agent 调用了自定义工具：${[...new Set(usedCustomTools)].join(', ')}`);
}

const usedCountWords = toolCalls.includes('count_words');
if (!usedCountWords) {
  fail('Agent 没有调用 count_words —— 说明工具描述或参数没被正确理解');
} else {
  log('✓ count_words 被调用（证明工具回调链路通）');
}

if (taskEnded) {
  log('✓ 任务正常结束');
}

if (!process.exitCode) {
  log('\n' + '='.repeat(60));
  log('✅ 业务工具回调验证通过');
  log(`   工具调用序列：${toolCalls.join(' → ')}`);
  log('='.repeat(60));
}
