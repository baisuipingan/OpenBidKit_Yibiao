/**
 * Web 版登录页。
 *
 * 只在 Web 模式下渲染（桌面版没有登录概念）。
 * 由 AuthGate 控制是否显示。
 */
import { useEffect, useState, type FormEvent } from 'react';

interface LoginPageProps {
  onSuccess: () => void;
}

const API_BASE = import.meta.env.VITE_API_BASE || '/api';

interface BootstrapInfo {
  tenantId: string;
  tenantName: string;
  multiTenant: boolean;
  appName: string;
}

export default function LoginPage({ onSuccess }: LoginPageProps) {
  const [email, setEmail] = useState('admin@local');
  const [password, setPassword] = useState('');
  const [tenantId, setTenantId] = useState(localStorage.getItem('yb_tenant_id') || '');
  const [bootstrap, setBootstrap] = useState<BootstrapInfo | null>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  // 启动时拉取部署信息：单租户部署直接拿到租户 ID，用户不必手填
  useEffect(() => {
    let cancelled = false;
    fetch(`${API_BASE}/public/bootstrap`)
      .then((res) => res.json())
      .then((body) => {
        if (cancelled || !body?.ok) return;
        const info = body.data as BootstrapInfo;
        setBootstrap(info);
        if (info.tenantId) setTenantId(info.tenantId);
        // 多租户部署才需要显示租户输入
        if (info.multiTenant) setShowAdvanced(true);
      })
      .catch(() => {
        // 拉取失败不阻塞登录，用户可手动填
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    try {
      const res = await fetch(`${API_BASE}/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password, tenantId: tenantId || undefined }),
      });

      const body = await res.json().catch(() => null);

      if (!res.ok || !body?.ok) {
        setError(body?.error?.message || `登录失败（HTTP ${res.status}）`);
        return;
      }

      const { accessToken, refreshToken, user } = body.data;
      localStorage.setItem('yb_access_token', accessToken);
      localStorage.setItem('yb_refresh_token', refreshToken);
      localStorage.setItem('yb_tenant_id', user.tenantId);
      localStorage.setItem('yb_user', JSON.stringify(user));

      onSuccess();
    } catch (e) {
      setError(e instanceof Error ? e.message : '网络连接失败');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="yb-login-page">
      <form className="yb-login-card" onSubmit={submit}>
        <div className="yb-login-brand">
          <div className="yb-login-mark" aria-hidden="true">标</div>
          <div>
            <h1>{bootstrap?.appName || '投标工具箱'}</h1>
            <p>
              {bootstrap?.multiTenant
                ? '登录以使用标书编制工作台'
                : `登录 ${bootstrap?.tenantName || ''} 工作区`}
            </p>
          </div>
        </div>

        <label className="yb-login-field">
          <span>邮箱</span>
          <input
            type="text"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="username"
            required
          />
        </label>

        <label className="yb-login-field">
          <span>密码</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </label>

        {showAdvanced ? (
          <label className="yb-login-field">
            <span>租户 ID（单租户部署可留空）</span>
            <input
              type="text"
              value={tenantId}
              onChange={(e) => setTenantId(e.target.value)}
              placeholder="留空则使用默认租户"
            />
          </label>
        ) : (
          <button
            type="button"
            className="yb-login-link"
            onClick={() => setShowAdvanced(true)}
          >
            高级选项
          </button>
        )}

        {error && (
          <div className="yb-login-error" role="alert">
            {error}
          </div>
        )}

        <button type="submit" className="yb-login-submit" disabled={loading}>
          {loading ? '登录中…' : '登录'}
        </button>
      </form>
    </div>
  );
}
