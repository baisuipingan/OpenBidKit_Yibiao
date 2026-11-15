import { useCallback, useEffect, useState } from 'react';
import type { ResumableTask } from '../types/ipc';
import { useToast } from './index';

/**
 * 任务恢复提示条。
 *
 * 场景：服务重启、网络断开、模型报错等导致正文生成中断。
 * 桌面版这些情况只能从头再来；Web 版因为产物都落盘了，
 * 可以从中断处接着做。
 *
 * 放在正文生成页顶部，有可恢复任务时显示，一条不占太多空间。
 */

interface ResumeBannerProps {
  /** 恢复成功后回调（页面重新加载进度） */
  onResumed?: (taskId: string) => void;
  /** 刷新触发（父组件数据变化时重新查一次） */
  refreshKey?: number;
}

/** 相对时间。 */
function relativeTime(iso: string): string {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const diff = Date.now() - t;
  if (diff < 60_000) return '刚刚';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return `${Math.floor(diff / 86_400_000)} 天前`;
}

export default function ResumeBanner({ onResumed, refreshKey }: ResumeBannerProps) {
  const [tasks, setTasks] = useState<ResumableTask[]>([]);
  const [loading, setLoading] = useState(false);
  const [resuming, setResuming] = useState('');
  const [dismissed, setDismissed] = useState(false);
  const { showToast } = useToast();

  const load = useCallback(async () => {
    try {
      const result = await window.yibiao.tasks.listResumable();
      setTasks(result?.items || []);
    } catch {
      // 静默失败：这个提示是增强功能，不该因为它报错打断用户
      setTasks([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const handleResume = async (task: ResumableTask) => {
    setResuming(task.taskId);
    try {
      const result = await window.yibiao.tasks.resumeContentGeneration({
        taskId: task.taskId,
        targetWordsPerSection: 800,
      });
      if (result?.resumed === false) {
        showToast(result.message || '该任务已完成，无需恢复', 'info');
        setTasks((prev) => prev.filter((t) => t.taskId !== task.taskId));
      } else {
        showToast(`已恢复，继续生成剩余 ${result?.pendingCount ?? ''} 节`, 'success');
        onResumed?.(result?.taskId || task.taskId);
      }
    } catch (error) {
      showToast(`恢复失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    } finally {
      setResuming('');
      void load();
    }
  };

  if (dismissed || tasks.length === 0) return null;

  return (
    <div className="resume-banner">
      <div className="resume-banner-icon" aria-hidden>
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M3 12a9 9 0 1 0 3-6.7" />
          <path d="M3 4v5h5" />
        </svg>
      </div>

      <div className="resume-banner-body">
        <div className="resume-banner-title">
          发现 {tasks.length} 个中断的任务，可以接着做
        </div>
        <ul className="resume-banner-list">
          {tasks.slice(0, 3).map((task) => (
            <li key={task.taskId} className="resume-banner-item">
              <span className="resume-banner-text">
                已完成 <b>{task.completedCount}</b> 节
                {task.pendingCount > 0 ? <>，还剩 <b>{task.pendingCount}</b> 节</> : null}
                <span className="resume-banner-dot">·</span>
                {task.totalWords} 字
                <span className="resume-banner-dot">·</span>
                {relativeTime(task.updatedAt)}
              </span>
              <button
                type="button"
                className="resume-banner-action"
                onClick={() => void handleResume(task)}
                disabled={resuming === task.taskId}
              >
                {resuming === task.taskId ? '恢复中…' : '继续生成'}
              </button>
            </li>
          ))}
        </ul>
      </div>

      <button
        type="button"
        className="resume-banner-close"
        onClick={() => setDismissed(true)}
        aria-label="关闭提示"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
          <path d="M18 6L6 18M6 6l12 12" />
        </svg>
      </button>
    </div>
  );
}
