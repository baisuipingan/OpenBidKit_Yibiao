/**
 * Web 版登录守卫。
 *
 * 职责：
 *   1. 判断是否需要登录（桌面版直接放行）
 *   2. 无令牌时显示登录页
 *   3. 启动时校验令牌有效性（调 /auth/me）
 *   4. 令牌失效时自动回到登录页
 *   5. 演示旁路：服务端 DEMO_AUTH_BYPASS 打开时跳过登录页
 *
 * 只在 Web 模式下渲染登录页；桌面版的 window.yibiao 由 preload 注入，
 * 不存在「登录」这一步，本组件会直接透传 children。
 *
 * 演示旁路说明：
 *   登录页、令牌刷新、租户字段都保留。关掉服务端 DEMO_AUTH_BYPASS 后，
 *   本守卫会再次要求登录，无需再改前端业务代码。
 */
import { useEffect, useState, type ReactNode } from 'react';

import LoginPage from './LoginPage';
import {
  clearTokens,
  getAccessToken,
  setDemoAuthBypass,
  setUnauthorizedHandler,
} from '../../shared/bridge/web';

const API_BASE = import.meta.env.VITE_API_BASE || '/api';

type Phase = 'checking' | 'need-login' | 'ready';

interface AuthGateProps {
  children: ReactNode;
}

interface BootstrapBody {
  ok?: boolean;
  data?: {
    demoAuthBypass?: boolean;
  };
}

/** 是否运行在 Web 模式（非 Electron）。 */
function isWebRuntime(): boolean {
  return typeof window !== 'undefined' && (window as unknown as { yibiao?: { platform?: string } }).yibiao?.platform === 'web';
}

export default function AuthGate({ children }: AuthGateProps) {
  const [phase, setPhase] = useState<Phase>('checking');

  useEffect(() => {
    if (!isWebRuntime()) {
      setPhase('ready');
      return;
    }

    let cancelled = false;

    const enterDemo = () => {
      setDemoAuthBypass(true);
      // 演示旁路：401 不应再踢回登录页（服务端会注入默认身份）
      setUnauthorizedHandler(() => {
        console.warn('[auth] 演示旁路模式下收到 401，已忽略登录跳转');
      });
      setPhase('ready');
    };

    const enterNormalAuth = () => {
      setDemoAuthBypass(false);
      setUnauthorizedHandler(() => {
        clearTokens();
        setPhase('need-login');
      });

      const token = getAccessToken();
      if (!token) {
        setPhase('need-login');
        return;
      }

      fetch(`${API_BASE}/auth/me`, {
        headers: { Authorization: `Bearer ${token}` },
      })
        .then((res) => {
          if (cancelled) return;
          if (res.status === 401) {
            clearTokens();
            setPhase('need-login');
          } else {
            setPhase('ready');
          }
        })
        .catch(() => {
          // 网络异常不强制登出，让业务页面自己报错
          if (!cancelled) setPhase('ready');
        });
    };

    // 先问服务端是否处于演示旁路，再决定要不要登录页。
    fetch(`${API_BASE}/public/bootstrap`)
      .then((res) => res.json() as Promise<BootstrapBody>)
      .then((body) => {
        if (cancelled) return;
        if (body?.ok && body.data?.demoAuthBypass) {
          enterDemo();
          return;
        }
        enterNormalAuth();
      })
      .catch(() => {
        // bootstrap 失败时保守走正常登录，避免误开放
        if (!cancelled) enterNormalAuth();
      });

    return () => {
      cancelled = true;
    };
  }, []);

  if (!isWebRuntime()) return <>{children}</>;

  if (phase === 'checking') {
    return (
      <div className="yb-login-page">
        <div className="yb-login-loading">正在校验登录状态…</div>
      </div>
    );
  }

  if (phase === 'need-login') {
    return <LoginPage onSuccess={() => setPhase('ready')} />;
  }

  return <>{children}</>;
}
