/**
 * 渲染 Sidecar：把 Mermaid 源码 / HTML 片段转成 PNG。
 *
 * 为什么单独一个进程：
 *   Go 没有等价能力。浏览器渲染是唯一能保证「所见即所得」的方式。
 *   桌面版用隐藏 BrowserWindow 做这件事（localImageRenderService.cjs），
 *   Web 版用 Playwright + headless Chromium。
 *
 * 复用的浏览器：
 *   与 dev 环境已装的 chromium 共用（PLAYWRIGHT_BROWSERS_PATH）。
 *   生产镜像里也需要装（见 deploy/Dockerfile.dev）。
 *
 * 接口：
 *   POST /render/mermaid  { code, width?, theme? }  → { png: base64, width, height }
 *   POST /render/html     { html, width, height? }  → { png: base64, width, height }
 *   GET  /health
 */

import { createServer } from 'node:http';
import { chromium } from 'playwright';
import fs from 'node:fs/promises';
import path from 'node:path';

const PORT = Number(process.env.RENDER_PORT || 7071);
const HOST = process.env.RENDER_HOST || '127.0.0.1';
const INTERNAL_TOKEN = process.env.AGENT_INTERNAL_TOKEN || '';
// Mermaid 脚本路径（从 client 的 node_modules 读）
const MERMAID_SCRIPT =
  process.env.MERMAID_SCRIPT || '/workspace/client/node_modules/mermaid/dist/mermaid.min.js';

/** 浏览器实例：全局复用，避免每次渲染都启动 */
let browser = null;
/** 并发控制：同时最多 2 个渲染（每页约 100MB 内存） */
const MAX_CONCURRENT = Number(process.env.RENDER_CONCURRENCY || 2);
let running = 0;
const waiting = [];

function log(...args) {
  console.log('[render]', ...args);
}

// ---------- 浏览器管理 ----------

async function getBrowser() {
  if (browser && browser.isConnected()) return browser;

  log('启动 Chromium…');
  browser = await chromium.launch({
    args: [
      '--no-sandbox',
      '--disable-dev-shm-usage',
      '--disable-gpu',
      '--font-render-hinting=none',
    ],
  });

  browser.on('disconnected', () => {
    log('Chromium 已断开');
    browser = null;
  });

  return browser;
}

/** 信号量：限制并发渲染数 */
async function acquire() {
  if (running < MAX_CONCURRENT) {
    running++;
    return;
  }
  await new Promise((resolve) => waiting.push(resolve));
  running++;
}

function release() {
  running--;
  const next = waiting.shift();
  if (next) next();
}

// ---------- 渲染实现 ----------

/**
 * Mermaid 转 PNG。
 *
 * 做法：
 *   1. 打开空白页
 *   2. 注入 mermaid 脚本
 *   3. 调用 mermaid.render 拿 SVG
 *   4. 把 SVG 放进页面，按实际尺寸截图
 *
 * 关键：必须用真实浏览器渲染，因为 Mermaid 依赖 DOM 测量文字尺寸
 * 来计算节点大小。纯 SVG 生成器算不准中文宽度。
 */
async function renderMermaid(code, opts = {}) {
  const { theme = 'default', scale = 2, maxAspectRatio = 0 } = opts;

  const b = await getBrowser();
  const page = await b.newPage({
    viewport: { width: 1200, height: 800 },
    deviceScaleFactor: scale,
  });

  try {
    // 读取 mermaid 脚本内容，直接注入（避免 file:// 跨域）
    let scriptContent;
    try {
      scriptContent = await fs.readFile(MERMAID_SCRIPT, 'utf-8');
    } catch (e) {
      throw new Error(`读取 mermaid 脚本失败: ${e.message}`);
    }

    await page.setContent('<!doctype html><html><head><meta charset="utf-8"></head><body></body></html>');
    await page.addScriptTag({ content: scriptContent });

    // 在页面里渲染
    const result = await page.evaluate(
      async ({ code, theme, maxAspectRatio }) => {
        try {
          window.mermaid = window.mermaid || {};
          window.mermaid.initialize({
            startOnLoad: false,
            theme,
            securityLevel: 'strict',
            // 中文字体
            fontFamily: '"Noto Sans CJK SC", "Microsoft YaHei", sans-serif',
            // 让 Mermaid 自己收紧布局：
            //   nodeSpacing/rankSpacing 调小能让宽图变紧凑
            flowchart: {
              nodeSpacing: 30,
              rankSpacing: 40,
              curve: 'basis',
              // 关键：htmlLabels 关闭时用 SVG text，尺寸更好算
              htmlLabels: true,
              // useMaxWidth 让图自适应容器宽度而不是无限铺开
              useMaxWidth: true,
            },
          });

          const id = 'm' + Date.now();
          const { svg } = await window.mermaid.render(id, code);

          // 把 SVG 插进页面，便于测量
          const host = document.createElement('div');
          host.id = 'host';
          host.style.cssText = 'display:inline-block;padding:16px;background:#fff;';
          host.innerHTML = svg;
          document.body.style.margin = '0';
          document.body.appendChild(host);

          // 等字体加载
          await document.fonts.ready;

          const svgEl = host.querySelector('svg');
          if (!svgEl) return { ok: false, error: '渲染后没有 SVG' };

          // 移除固定宽高，让它自适应内容（否则可能被裁切）
          const vb = (svgEl.getAttribute('viewBox') || '').split(/[\s,]+/).map(Number);
          let w = vb[2] || parseFloat(svgEl.getAttribute('width')) || 800;
          let h = vb[3] || parseFloat(svgEl.getAttribute('height')) || 600;

          svgEl.removeAttribute('width');
          svgEl.removeAttribute('height');
          svgEl.style.width = w + 'px';
          svgEl.style.height = h + 'px';
          svgEl.style.maxWidth = 'none';

          // 宽高比检查：只报告，不改图。
          //
          // **为什么不在这一步硬改**：
          //   压缩或拉伸会让图形失真、文字变形。
          //   正确的地方是**生成 Mermaid 源码时**就控制节点数量和方向：
          //     - 节点太多 → 拆成两张图
          //     - 横向过宽 → 改用 graph LR（横向布局反而更紧凑）
          //   这一步只负责把数据回传，让上层决定要不要重画。
          const aspect = w / h;

          return {
            ok: true,
            width: Math.ceil(w) + 32,
            height: Math.ceil(h) + 32,
            aspectRatio: Math.round(aspect * 100) / 100,
            tooWide: maxAspectRatio > 0 && aspect > maxAspectRatio,
          };
        } catch (e) {
          return { ok: false, error: e?.message || String(e) };
        }
      },
      { code, theme, maxAspectRatio },
    );

    if (!result.ok) {
      throw new Error(result.error || 'Mermaid 渲染失败');
    }

    const el = await page.$('#host');
    if (!el) throw new Error('找不到渲染容器');

    const png = await el.screenshot({ type: 'png', omitBackground: false });

    log(`mermaid 尺寸 ${result.width}x${result.height} 宽高比 ${result.aspectRatio}${result.tooWide ? ' ⚠️ 过宽' : ''}`);

    return {
      png: png.toString('base64'),
      width: result.width,
      height: result.height,
      aspectRatio: result.aspectRatio,
      tooWide: result.tooWide,
      mimeType: 'image/png',
    };
  } finally {
    await page.close().catch(() => {});
  }
}

/**
 * HTML 转 PNG。
 *
 * 用途：Agent 生成 HTML 片段（如组织架构图、流程图），转成图片插入 Word。
 * 桌面版支持这种方式，因为它比 AI 生图更精确（数据来自正文）。
 */
async function renderHTML(html, opts = {}) {
  const { width = 900, height = 0, scale = 2, backgroundColor = '#ffffff' } = opts;

  const b = await getBrowser();
  const page = await b.newPage({
    viewport: { width: Number(width) || 900, height: Number(height) || 600 },
    deviceScaleFactor: scale,
  });

  try {
    // 包一层基础样式，保证中文渲染正常
    const wrapped = `<!doctype html><html><head><meta charset="utf-8">
<style>
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 16px;
    background: ${backgroundColor};
    font-family: "Noto Sans CJK SC", "Microsoft YaHei", -apple-system, sans-serif;
    font-size: 14px;
    color: #1c2536;
    -webkit-font-smoothing: antialiased;
  }
  table { border-collapse: collapse; }
  th, td { border: 1px solid #dcdff6; padding: 6px 10px; }
</style>
</head><body>${html}</body></html>`;

    await page.setContent(wrapped, { waitUntil: 'networkidle' });
    await page.evaluate(() => document.fonts.ready);

    // 自动高度：量 body 实际高度
    const actualHeight = await page.evaluate(() => {
      const body = document.body;
      return Math.max(body.scrollHeight, body.offsetHeight, 100);
    });

    if (!height) {
      await page.setViewportSize({
        width: Number(width) || 900,
        height: Math.min(actualHeight + 8, 8000),
      });
    }

    const png = await page.screenshot({
      type: 'png',
      fullPage: !height,
      clip: height ? { x: 0, y: 0, width: Number(width), height: Number(height) } : undefined,
    });

    return {
      png: png.toString('base64'),
      width: Number(width) || 900,
      height: height || actualHeight + 8,
      mimeType: 'image/png',
    };
  } finally {
    await page.close().catch(() => {});
  }
}

// ---------- HTTP 服务 ----------

function json(res, status, body) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(payload),
  });
  res.end(payload);
}

async function readBody(req, limit = 4 * 1024 * 1024) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new Error('请求体过大');
    chunks.push(chunk);
  }
  if (!chunks.length) return {};
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function authorized(req) {
  if (!INTERNAL_TOKEN) return true;
  return (req.headers.authorization || '') === `Bearer ${INTERNAL_TOKEN}`;
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://${HOST}`);

  try {
    if (url.pathname === '/health') {
      return json(res, 200, {
        status: 'ok',
        browser: Boolean(browser && browser.isConnected()),
        running,
        queued: waiting.length,
      });
    }

    if (!authorized(req)) {
      return json(res, 401, { ok: false, error: { code: 'UNAUTHORIZED', message: '内部令牌无效' } });
    }

    if (req.method === 'POST' && url.pathname === '/render/mermaid') {
      const body = await readBody(req);
      if (!body.code || typeof body.code !== 'string') {
        return json(res, 400, { ok: false, error: { code: 'INVALID_ARGUMENT', message: '缺少 code' } });
      }

      await acquire();
      try {
        const out = await renderMermaid(body.code, {
          theme: body.theme || 'default',
          scale: body.scale || 2,
          maxAspectRatio: body.maxAspectRatio || 0,
        });
        log(`mermaid 渲染完成 ${out.width}x${out.height}`);
        return json(res, 200, { ok: true, data: out });
      } finally {
        release();
      }
    }

    if (req.method === 'POST' && url.pathname === '/render/html') {
      const body = await readBody(req);
      if (!body.html || typeof body.html !== 'string') {
        return json(res, 400, { ok: false, error: { code: 'INVALID_ARGUMENT', message: '缺少 html' } });
      }

      await acquire();
      try {
        const out = await renderHTML(body.html, {
          width: body.width,
          height: body.height,
          scale: body.scale || 2,
          backgroundColor: body.backgroundColor,
        });
        log(`html 渲染完成 ${out.width}x${out.height}`);
        return json(res, 200, { ok: true, data: out });
      } finally {
        release();
      }
    }

    return json(res, 404, { ok: false, error: { code: 'NOT_FOUND', message: `未实现: ${url.pathname}` } });
  } catch (error) {
    log('请求异常:', error?.message);
    if (!res.headersSent) {
      return json(res, 500, {
        ok: false,
        error: { code: 'RENDER_FAILED', message: error?.message || '渲染失败' },
      });
    }
    try {
      res.end();
    } catch {
      /* 忽略 */
    }
  }
});

server.listen(PORT, HOST, () => {
  log(`渲染 Sidecar 已启动 http://${HOST}:${PORT}`);
  log(`并发上限: ${MAX_CONCURRENT}`);
  log(`Mermaid 脚本: ${MERMAID_SCRIPT}`);
});

process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

async function shutdown() {
  log('退出中…');
  if (browser) {
    await browser.close().catch(() => {});
  }
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 3000);
}
