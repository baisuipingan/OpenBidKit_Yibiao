/**
 * 验证宽高比警告链路：
 * Agent 生成一张过宽的图 → 收到警告 → 应该重画或精简
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

// 故意让 Agent 画一个节点很多的图
const prompt = `请用 generate_image 工具生成一张 mermaid 架构图。

要求：
- 画一个完整的企业级系统架构，包含这些层：
  接入层（自助终端、医护工作站、管理驾驶舱、移动端）
  网关层（路由转发、Token鉴权、限流控制、审计日志）
  业务层（门诊、住院、检验检查、药事、财务运营）
  数据层（主数据、业务数据、数据仓库、报表库）
- 每个模块都要画成一个独立节点
- 节点之间用箭头连接表示调用关系
- 用 graph TD 语法

完成后报告结果。如果收到任何关于图片尺寸或可读性的提示，请说明。

注意：必须真的调用工具，不要只描述。`;

const res = await fetch(`${API}/api/agent/self-check`, {
  method: 'POST', headers,
  body: JSON.stringify({ provider: { baseUrl: AI_BASE_URL, apiKey: AI_API_KEY, model: AI_MODEL }, prompt }),
});

const reader = res.body.getReader();
const dec = new TextDecoder();
let buf = '';
let done = false;
const imgResults = [];
let warnings = 0;
let imgCount = 0;

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
        if (p.type === 'tool_start' && p.toolName === 'generate_image') {
          imgCount++;
          log(`  [生成图 ${imgCount}]`);
        }
        if (p.type === 'tool_end' && p.toolName === 'generate_image') {
          const txt = JSON.stringify(p.result || {});
          if (txt.includes('宽高比') || txt.includes('偏扁')) {
            warnings++;
            log(`    ⚠️ 收到宽高比警告`);
            // 提取警告文本
            const m = txt.match(/这张图宽高比[^"]*/);
            if (m) log(`    ${m[0]}`);
          } else if (!p.isError) {
            log(`    ✓ 正常`);
          } else {
            log(`    ✗ 失败`);
          }
          imgResults.push(txt);
        }
        if (p.type === 'task_end') log(`  [结束] ${p.status}`);
      }
    }
  }
};
await loop();

log('\n=== 验证 ===');
log(`生成图片: ${imgCount} 张`);
log(`收到警告: ${warnings} 次`);
if (warnings > 0) {
  log('✅ 宽高比警告链路生效');
} else if (imgCount > 0) {
  log('ℹ️ 本次生成的图宽高比在限制内（未触发警告）');
} else {
  log('❌ 没有生成图片');
}
