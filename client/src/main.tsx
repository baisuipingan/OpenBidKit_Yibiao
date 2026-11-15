import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import AppProviders from './app/providers/AppProviders';
import AuthGate from './app/web/AuthGate';
import WorkspaceDatabaseGate from './app/WorkspaceDatabaseGate';
import DeveloperTokenStatsWindow from './features/developer/pages/DeveloperTokenStatsWindow';
import PiAgentMonitorWindow from './features/developer/pages/PiAgentMonitorWindow';
import './styles.css';

const windowMode = new URLSearchParams(window.location.search).get('window');

if (windowMode === 'dual-layer-pdf-worker') {
  // 批量转双层 PDF 的隐藏工作窗口：没有界面，只按需加载处理脚本。
  void import('./features/dual-layer-pdf/worker/workerMain');
} else {
  createRoot(document.getElementById('root') as HTMLElement).render(
    <React.StrictMode>
      {windowMode === 'token-stats' ? (
        <DeveloperTokenStatsWindow />
      ) : windowMode === 'agent-monitor' ? (
        <PiAgentMonitorWindow />
      ) : (
        <AppProviders>
          {/* AuthGate 在桌面版直接透传；Web 版无令牌时显示登录页 */}
          <AuthGate>
            <WorkspaceDatabaseGate>
              <App />
            </WorkspaceDatabaseGate>
          </AuthGate>
        </AppProviders>
      )}
    </React.StrictMode>
  );
}
