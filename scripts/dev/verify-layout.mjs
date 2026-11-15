/**
 * 验证 C：headless Chromium 能否跑通 docx-editor 的布局测量。
 *
 * 这是整个迁移方案里唯一可能推翻架构的点：
 *   桌面版用隐藏 BrowserWindow 提供 document.fonts + Canvas 2D 做字体度量，
 *   Web 版必须用 headless Chromium 复现同样的环境。
 *
 * 判定标准（从桌面版 wordLayoutPreload.cjs 提取）：
 *   1. document.fonts.ready 可用
 *   2. canvas.getContext('2d') 可用
 *   3. resolveDefaultSurfaceMeasurer 返回的 producer === 'canvas-measurer'
 *      （这一条最关键：如果降级成近似测量，页数会算错）
 *   4. openDocumentForExport 能解析真实 docx
 *   5. session.layout() 能返回 pages 数组
 *
 * 用法：
 *   node scripts/dev/verify-layout.mjs
 *   DOCX=/tmp/openxml-ws/sample.docx node scripts/dev/verify-layout.mjs
 */

import { chromium } from 'playwright';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';

const DOCX = process.env.DOCX || '/tmp/openxml-ws/sample.docx';
const CLIENT_DIR = process.env.CLIENT_DIR || '/workspace/client';
const PORT = Number(process.env.PORT || 5199);

const log = (...a) => console.log(...a);
const fail = (msg) => {
  console.error('❌', msg);
  process.exitCode = 1;
};

// ---------- 1. 起一个最小静态服务器，把 docx-editor 的 dist 暴露出去 ----------
// 关键：必须从同源加载 ESM，不能用 file:// （CORS 会拦）
const distDir = path.join(CLIENT_DIR, 'node_modules/@docx-editor.dev/core/dist');

if (!fs.existsSync(distDir)) {
  fail(`找不到 docx-editor dist: ${distDir}`);
  process.exit(1);
}

const MIME = {
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.wasm': 'application/wasm',
};

const server = http.createServer((req, res) => {
  const url = (req.url || '/').split('?')[0];

  if (url === '/' || url === '/index.html') {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end('<!doctype html><html><head><meta charset="utf-8"></head><body></body></html>');
    return;
  }

  const rel = url.replace(/^\/dist\//, '');
  const file = path.join(distDir, rel);

  // 防路径穿越
  if (!file.startsWith(distDir)) {
    res.writeHead(403).end('forbidden');
    return;
  }

  fs.readFile(file, (err, data) => {
    if (err) {
      res.writeHead(404).end('not found: ' + rel);
      return;
    }
    res.writeHead(200, {
      'Content-Type': MIME[path.extname(file)] || 'application/octet-stream',
      'Access-Control-Allow-Origin': '*',
    });
    res.end(data);
  });
});

await new Promise((r) => server.listen(PORT, '127.0.0.1', r));
log(`静态服务器: http://127.0.0.1:${PORT}/  →  ${distDir}`);

// ---------- 2. 启动 headless Chromium ----------
const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
});
const page = await browser.newPage();

const pageLogs = [];
page.on('console', (m) => pageLogs.push(`[${m.type()}] ${m.text()}`));
page.on('pageerror', (e) => pageLogs.push(`[pageerror] ${e.message}`));

await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: 'domcontentloaded' });

// ---------- 3. 逐项验证 ----------
log('\n=== 步骤 1: 检查浏览器环境 ===');

const env = await page.evaluate(async () => {
  const out = {
    hasFonts: typeof document.fonts === 'object',
    hasFontsReady: false,
    hasCanvas: false,
    canvasWorks: false,
    ua: navigator.userAgent.slice(0, 60),
  };

  if (document.fonts) {
    await document.fonts.ready;
    out.hasFontsReady = true;
  }

  const c = document.createElement('canvas');
  out.hasCanvas = typeof c.getContext === 'function';
  if (out.hasCanvas) {
    const ctx = c.getContext('2d');
    out.canvasWorks = !!ctx && typeof ctx.measureText === 'function';
    if (out.canvasWorks) {
      // 关键：能否测出真实中文字体宽度（说明字体真的加载了）
      ctx.font = '16px "Noto Serif CJK SC"';
      out.cjkWidth = ctx.measureText('中文测试').width;
      ctx.font = '16px sans-serif';
      out.fallbackWidth = ctx.measureText('中文测试').width;
    }
  }
  return out;
});

log(JSON.stringify(env, null, 2));

if (!env.hasFonts || !env.hasFontsReady) fail('document.fonts 不可用');
if (!env.canvasWorks) fail('Canvas 2D 不可用');
if (env.cjkWidth && env.cjkWidth < 20) fail(`中文宽度异常(${env.cjkWidth})，字体可能未加载`);

// ---------- 4. 加载 docx-editor 并测量 ----------
log('\n=== 步骤 2: 加载 docx-editor 布局模块 ===');

const moduleCheck = await page.evaluate(async (port) => {
  try {
    const layout = await import(`http://127.0.0.1:${port}/dist/layout.js`);
    const exp = await import(`http://127.0.0.1:${port}/dist/export.js`);
    return {
      ok: true,
      layoutKeys: Object.keys(layout).slice(0, 20),
      exportKeys: Object.keys(exp).slice(0, 20),
      hasMeasurer: typeof layout.resolveDefaultSurfaceMeasurer === 'function',
      hasOpen: typeof exp.openDocumentForExport === 'function',
    };
  } catch (e) {
    return { ok: false, error: e.message, stack: (e.stack || '').slice(0, 500) };
  }
}, PORT);

log(JSON.stringify(moduleCheck, null, 2));

if (!moduleCheck.ok) {
  fail(`模块加载失败: ${moduleCheck.error}`);
} else {
  // ---------- 5. 实际测量 ----------
  log('\n=== 步骤 3: 用真实 docx 做布局测量 ===');

  if (!fs.existsSync(DOCX)) {
    fail(`测试文档不存在: ${DOCX}`);
  } else {
    const bytes = Array.from(fs.readFileSync(DOCX));

    const result = await page.evaluate(
      async ({ port, bytes }) => {
        try {
          const layout = await import(`http://127.0.0.1:${port}/dist/layout.js`);
          const exp = await import(`http://127.0.0.1:${port}/dist/export.js`);

          await document.fonts.ready;
          const ctx = document.createElement('canvas').getContext('2d');
          const metrics = layout.resolveDefaultSurfaceMeasurer(96 / 72, { context: ctx });

          if (metrics.producer !== 'canvas-measurer') {
            return { ok: false, stage: 'measurer', producer: metrics.producer };
          }

          const opened = exp.openDocumentForExport(new Uint8Array(bytes), {
            measurer: metrics.measurer,
            producer: metrics.producer,
          });

          if (!opened.ok) {
            return { ok: false, stage: 'open', detail: JSON.stringify(opened).slice(0, 400) };
          }

          try {
            const lay = await opened.session.layout();
            return {
              ok: true,
              producer: metrics.producer,
              pageCount: lay.pages?.length ?? 0,
              pageSizes: (lay.pages || []).slice(0, 3).map((p) => ({
                index: p.index,
                width: p.width,
                height: p.height,
              })),
              destinationCount: (lay.destinations || []).length,
            };
          } finally {
            opened.session.dispose();
          }
        } catch (e) {
          return { ok: false, stage: 'layout', error: e.message, stack: (e.stack || '').slice(0, 600) };
        }
      },
      { port: PORT, bytes },
    );

    log(JSON.stringify(result, null, 2));

    if (result.ok) {
      log('\n' + '='.repeat(60));
      log(`✅ 验证 C 通过`);
      log(`   producer = ${result.producer}（必须是 canvas-measurer）`);
      log(`   页数 = ${result.pageCount}`);
      log('   → headless Chromium 可以替代桌面版的隐藏 BrowserWindow');
      log('='.repeat(60));
    } else {
      fail(`布局测量失败: stage=${result.stage} ${result.error || result.detail || result.producer}`);
    }
  }
}

// ---------- 6. 输出页面日志 ----------
if (pageLogs.length) {
  log('\n=== 浏览器控制台 ===');
  pageLogs.slice(0, 20).forEach((l) => log(l.slice(0, 300)));
}

await browser.close();
server.close();
