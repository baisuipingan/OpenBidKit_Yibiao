/** 验证 Agent 调用 generate_image 生成配图 */
const API = process.env.API || 'http://127.0.0.1:8080';
const PROJECT = process.env.PROJECT || 'f77d5870-aafc-47a1-a267-27f7f881869b';
const AI_BASE_URL = process.env.AI_BASE_URL || '';
const AI_API_KEY = process.env.AI_API_KEY || '';
const AI_MODEL = process.env.AI_MODEL || '';

const log = (...a) => console.log(...a);

const login = await fetch(`${API}/api/auth/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email: 'admin@local', password: 'admin12345' }),
});
const { data: auth } = await login.json();
const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${auth.accessToken}`,
  'X-Project-Id': PROJECT,
};

const prompt = [
  '请完成以下任务，必须使用 generate_image 工具：',
  '',
  '1. 用 kind=mermaid 生成一个流程图，展示投标流程：招标文件 → 标书分析 → 目录生成 → 正文生成 → 导出 Word',
  '2. 用 kind=htmlImage 生成一个简单的组织架构图，包含：项目经理 → 技术负责人、实施负责人',
  '',
  '要求：',
  '- mermaid 用 graph TD 语法',
  '- htmlImage 用 div + inline style 画层级结构',
  '- 两个图都要调用 generate_image',
  '- 完成后报告生成的 assetRef',
].join('\n');

const res = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST', headers,
  body: JSON.stringify({
    provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL },
    prompt,
  }),
});

const reader = res.body.getReader();
const decoder = new TextDecoder();
let buf = '';
const toolCalls = [];
const toolResults = [];
let done = false;

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
      if (evName === 'error') { console.error('任务错误:', p.message); done = true; return; }
      if (evName === 'agent') {
        if (p.type === 'tool_start') { toolCalls.push(p.toolName); log(`  [工具] ${p.toolName}`); }
        else if (p.type === 'tool_end') {
          const txt = JSON.stringify(p.result || {}).slice(0, 250);
          if (p.toolName === 'generate_image') toolResults.push(txt);
          log(`  [结果] ${p.toolName}${p.isError ? ' 失败' : ''}: ${txt}`);
        }
        else if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
const used = toolCalls.filter((n) => n === 'generate_image').length;
log(`generate_image 调用次数: ${used}`);
if (used === 0) { console.error('❌ Agent 没有调用 generate_image'); process.exitCode = 1; }
else {
  log('✓ Agent 调用了 generate_image');
  // 检查是否成功
  const failed = toolResults.filter((r) => r.includes('"isError":true') || r.includes('失败'));
  if (failed.length === used) { console.error('❌ 所有调用都失败了'); process.exitCode = 1; }
  else log(`✓ ${used - failed.length}/${used} 次成功`);
}
