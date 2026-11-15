/**
 * bridge 入口：按运行环境选择实现。
 *
 * 三种模式：
 *   1. Electron 桌面版：window.yibiao 由 preload 注入，本模块不生效
 *   2. 浏览器 + 后端：用 web.ts（HTTP + SSE）
 *   3. 浏览器 + 无后端：用 mock.ts（仅 UI 调试）
 *
 * 由 index.html 里的首屏脚本调用 install() 完成注入，
 * 这样 main.tsx 里的 window.yibiao 判断不需要改。
 */

import { bridge as webBridge, ensureProjectValid } from './web';

export type BridgeMode = 'desktop' | 'web' | 'mock';

/** 检测当前环境。 */
export function detectMode(): BridgeMode {
  if (typeof window === 'undefined') return 'mock';

  const w = window as unknown as Record<string, unknown>;
  // Electron preload 注入的对象带 platform 且是 electron
  if (w.yibiao && (w.yibiao as { platform?: string }).platform !== 'web') {
    return 'desktop';
  }
  // URL 参数强制 mock，方便纯前端调试
  const params = new URLSearchParams(window.location.search);
  if (params.get('mock') === '1') return 'mock';

  return 'web';
}

/** 安装 bridge。已存在（桌面版）时直接返回。 */
export async function install(): Promise<BridgeMode> {
  const w = window as unknown as Record<string, unknown>;

  if (w.yibiao && (w.yibiao as { platform?: string }).platform !== 'web') {
    return 'desktop';
  }

  const mode = detectMode();

  if (mode === 'mock') {
    await import('./mock');
    return 'mock';
  }

  w.yibiao = webBridge;

  // 校验缓存的项项目是否还有效（可能已被删除）。
  // 失败不阻塞启动 —— 业务接口自己会报错，但提前修好体验更好。
  void ensureProjectValid().catch(() => undefined);

  return 'web';
}
