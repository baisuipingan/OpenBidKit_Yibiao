import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

/**
 * Web 版迁移验证专用配置（不改动原 vite.config.ts）。
 *
 * 与桌面版的差异：
 *  - host 改为 0.0.0.0，允许容器外/公网访问
 *  - 增加 /api 代理到 Go 服务（迁移完成后由 Go 提供）
 *  - HMR 走公网地址，避免浏览器连不上
 *
 * 用法：npx vite --config vite.web.config.ts
 */
// 公网地址必须通过环境变量传入，不要硬编码服务器 IP（仓库是公开的）。
// 本地开发不传时回落到 localhost。
const PUBLIC_HOST = process.env.PUBLIC_HOST || 'localhost';
const PUBLIC_PORT = Number(process.env.PUBLIC_PORT || 5180);
// vite 与 Go 服务都在同一个容器里，用容器内端口 8080
// （宿主机映射是 8090，只在容器外访问时才用）
const API_TARGET = process.env.API_TARGET || 'http://127.0.0.1:8080';

export default defineConfig({
  base: './',
  plugins: [react()],
  define: {
    // bridge/web.ts 用到，构建期替换成字面量
    __APP_VERSION__: JSON.stringify(process.env.APP_VERSION || '0.1.0-web'),
  },
  optimizeDeps: {
    include: ['pdfjs-dist/legacy/build/pdf.mjs'],
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    strictPort: true,
    // Cloudflare quick tunnel Host 每次变化；允许 *.trycloudflare.com 以便公网演示
    allowedHosts: ['.trycloudflare.com', 'localhost', '127.0.0.1'],
    // 公网访问时 HMR 的 websocket 必须指回外部地址，否则热更新失效。
    // 关键：容器内没有公网 IP，不能让 vite 自己去 bind 那个地址，
    // 所以要同时指定 clientPort（浏览器连的端口）和 path，
    // 让 websocket 复用 vite 自己的监听端口。
    hmr: {
      host: PUBLIC_HOST,
      clientPort: PUBLIC_PORT,
      protocol: 'ws',
    },
    fs: {
      allow: ['.'],
    },
    proxy: {
      '/api': {
        target: API_TARGET,
        changeOrigin: true,
        ws: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
});
