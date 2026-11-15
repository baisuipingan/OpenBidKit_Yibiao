/// <reference types="vite/client" />

/** 构建时注入的版本号，由 vite define 提供。 */
declare const __APP_VERSION__: string;

interface ImportMetaEnv {
  /** API 基地址，默认 /api（走 vite 代理或同源部署）。 */
  readonly VITE_API_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
