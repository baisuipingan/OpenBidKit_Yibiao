/**
 * 验证 C（方案 2）：用 Vite 打包后再在 headless Chromium 里跑布局测量。
 *
 * 为什么需要打包：
 *   @docx-editor.dev/core 的 dist 是 ESM，但内部 import 的是裸模块名（如 'fflate'），
 *   浏览器无法直接解析。必须经 bundler 把依赖内联进来。
 *   这也是服务端 Chromium 方案的真实形态：预先打包一份测量脚本，注入页面执行。
 *
 * 用法：
 *   node scripts/dev/verify-layout-bundled.mjs
 */

import { chromium } from 'playwright';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { fileURLToPath } from 'node:url';

const DOCX = process.env.DOCX || '/tmp/openxml-ws/sample.docx';
const CLIENT_DIR = process.env.CLIENT_DIR || '/workspace/client';
const CACHE = process.env.CACHE || '/tmp/layout-bundle';
const PORT = Number(process.env.PORT || 5199);

const log = (...a) => console.log(...a);
const fail = (msg) => {
  console.error('❌', msg);
  process.exitCode = 1;
};

// ---------- 1. 用 Vite 的 build API 打包测量入口 ----------
log('=== 步骤 1: 用 Vite 打包测量入口 ===');

fs.mkdirSync(CACHE, { recursive: true });

// 关键：entry 必须放在 client/ 内，否则 vite/rollup 解析不到 node_modules
const entryDir = path.join(CLIENT_DIR, '.yb-layout-entry');
fs.mkdirSync(entryDir, { recursive: true });
const entry = path.join(entryDir, 'entry.js');
fs.writeFileSync(
  entry,
  `
import { resolveDefaultSurfaceMeasurer } from '@docx-editor.dev/core/layout';
import { openDocumentForExport } from '@docx-editor.dev/core/export';

// 暴露给页面调用。参数用普通数组传字节，避免跨 evaluate 传 Uint8Array。
window.__ybMeasure = async function (bytes) {
  await document.fonts.ready;
  const ctx = document.createElement('canvas').getContext('2d');
  const metrics = resolveDefaultSurfaceMeasurer(96 / 72, { context: ctx });

  const meta = {
    producer: metrics.producer,
    hasMeasurer: typeof metrics.measurer === 'function',
  };
  if (metrics.producer !== 'canvas-measurer') {
    return { ok: false, stage: 'measurer', meta };
  }

  const opened = openDocumentForExport(new Uint8Array(bytes), {
    measurer: metrics.measurer,
    producer: metrics.producer,
  });
  if (!opened.ok) {
    return { ok: false, stage: 'open', meta, detail: JSON.stringify(opened).slice(0, 400) };
  }

  try {
    const lay = await opened.session.layout();
    return {
      ok: true,
      meta,
      pageCount: lay.pages ? lay.pages.length : 0,
      pageSizes: (lay.pages || []).slice(0, 3).map(p => ({
        index: p.index, width: p.width, height: p.height,
      })),
      destinationCount: (lay.destinations || []).length,
    };
  } finally {
    opened.session.dispose();
  }
};

window.__ybReady = true;
`,
  'utf-8',
);

// 用 vite build 打包成 IIFE，浏览器可直接 <script> 加载
const vitePkg = path.join(CLIENT_DIR, 'node_modules/vite');
if (!fs.existsSync(vitePkg)) {
  fail(`找不到 vite: ${vitePkg}`);
  process.exit(1);
}

const { build } = await import(path.join(vitePkg, 'dist/node/index.js'));

try {
  await build({
    root: CLIENT_DIR, // 让 vite 能解析 node_modules
    logLevel: 'warn',
    build: {
      write: true,
      outDir: path.join(CACHE, 'out'),
      emptyOutDir: true,
      lib: {
        entry,
        name: 'YBMeasure',
        formats: ['iife'],
        fileName: () => 'measure.js',
      },
      minify: false,
      rollupOptions: {
        // 全部内联，不留外部依赖
        external: [],
      },
    },
  });
  log('✓ 打包完成');
} catch (e) {
  fail(`打包失败: ${e.message}`);
  console.error((e.stack || '').slice(0, 800));
  process.exit(1);
}

const bundlePath = path.join(CACHE, 'out/measure.js');
if (!fs.existsSync(bundlePath)) {
  fail(`打包产物不存在: ${bundlePath}`);
  process.exit(1);
}
const bundle = fs.readFileSync(bundlePath, 'utf-8');
const bundleSizeKB = (bundle.length / 1024).toFixed(0);
log(`  产物: ${bundlePath}（${bundleSizeKB} KB）`);

// ---------- 2. 起最小服务器 ----------
const server = http.createServer((req, res) => {
  const url = (req.url || '/').split('?')[0];
  if (url === '/' || url === '/index.html') {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end('<!doctype html><html><head><meta charset="utf-8"></head><body></body></html>');
    return;
  }
  if (url === '/measure.js') {
    res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8' });
    res.end(bundle);
    return;
  }
  res.writeHead(404).end('not found');
});
await new Promise((r) => server.listen(PORT, '127.0.0.1', r));
log(`静态服务器: http://127.0.0.1:${PORT}/`);

// ---------- 3. 浏览器验证 ----------
log('\n=== 步骤 2: headless Chromium 加载并测量 ===');

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});
const page = await browser.newPage();

const pageLogs = [];
page.on('console', (m) => pageLogs.push(`[${m.type()}] ${m.text()}`));
page.on('pageerror', (e) => pageLogs.push(`[pageerror] ${e.message}`));

await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: 'domcontentloaded' });
await page.addScriptTag({ url: `http://127.0.0.1:${PORT}/measure.js` });

const ready = await page.evaluate(() => window.__ybReady === true && typeof window.__ybMeasure === 'function');
if (!ready) {
  fail('测量脚本未就绪（__ybMeasure 不存在）');
} else {
  log('✓ 测量脚本注入成功');
}

if (!fs.existsSync(DOCX)) {
  fail(`测试文档不存在: ${DOCX}`);
} else {
  const bytes = Array.from(fs.readFileSync(DOCX));
  log(`  测试文档: ${DOCX}（${bytes.length} bytes）`);

  const result = await page.evaluate((b) => window.__ybMeasure(b), bytes);
  log('\n=== 测量结果 ===');
  log(JSON.stringify(result, null, 2));

  if (result?.ok) {
    log('\n' + '='.repeat(64));
    log('✅ 验证 C 通过');
    log(`   producer  = ${result.meta.producer}（必须 canvas-measurer）`);
    log(`   页数      = ${result.pageCount}`);
    log(`   书签数    = ${result.destinationCount}`);
    log('   → headless Chromium 可以替代桌面版的隐藏 BrowserWindow');
    log('='.repeat(64));
  } else {
    fail(`测量失败: ${JSON.stringify(result).slice(0, 600)}`);
  }
}

if (pageLogs.length) {
  log('\n=== 浏览器控制台 ===');
  pageLogs.slice(0, 15).forEach((l) => log(l.slice(0, 300)));
}

await browser.close();
server.close();
