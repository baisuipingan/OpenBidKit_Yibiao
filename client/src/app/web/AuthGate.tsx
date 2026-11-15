/**
 * Web 版登录守卫。
 *
 * 职责：
 *   1. 判断是否需要登录（桌面版直接放行）
 *   2. 无令牌时显示登录页
 *   3. 启动时校验令牌有效性（调 /auth/me）
 *   4. 令牌失效时自动回到登录页
 *
 * 只在 Web 模式下渲染登录页；桌面版的 window.yibiao 由 preload 注入，
 * 不存在「登录」这一步，本组件会直接透传 children。
 */
import { useEffect, useState, type ReactNode } from 'react';

import LoginPage from './LoginPage';
import { clearTokens, getAccessToken, setUnauthorizedHandler } from '../../shared/bridge/web';

const API_BASE = import.meta.env.VITE_API_BASE || '/api';

type Phase = 'checking' | 'need-login' | 'ready';

interface AuthGateProps {
  children: ReactNode;
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

    // bridge 检测到 401 时通知回到登录页
    setUnauthorizedHandler(() => {
      clearTokens();
      setPhase('need-login');
    });

    const token = getAccessToken();
    if (!token) {
      setPhase('need-login');
      return;
    }

    // 校验令牌是否还有效
    let cancelled = false;
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
