import * as Dialog from '@radix-ui/react-dialog';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ProjectItem } from '../shared/types/ipc';
import { useToast } from '../shared/ui';

/**
 * 项目切换器。
 *
 * 放在侧边栏 logo 下方 —— 项目是全局作用域，用户随时要能看见
 * "我现在在哪个项目里"，就像 IDE 显示当前工作区一样。
 *
 * Web 版独有：桌面版单机单项目，没有这个概念。
 */

interface ProjectSwitcherProps {
  /** 项目切换后回调（页面需要重新加载数据） */
  onSwitch?: (project: ProjectItem) => void;
}

/** 步骤的中文名。 */
const STEP_LABEL: Record<string, string> = {
  'document-analysis': '解析招标文件',
  'generation-settings': '生成设置',
  'bid-analysis': '评分分析',
  'outline-generation': '生成目录',
  'global-facts': '全局事实',
  'content-edit': '生成正文',
  expand: '扩写',
};

function stepLabel(step?: string): string {
  if (!step) return '未开始';
  return STEP_LABEL[step] || step;
}

export default function ProjectSwitcher({ onSwitch }: ProjectSwitcherProps) {
  const [projects, setProjects] = useState<ProjectItem[]>([]);
  const [current, setCurrent] = useState<ProjectItem | null>(null);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [renaming, setRenaming] = useState('');
  const [renameValue, setRenameValue] = useState('');
  const renameInputRef = useRef<HTMLInputElement>(null);
  const { showToast } = useToast();

  const load = useCallback(async () => {
    try {
      const [cur, listResult] = await Promise.all([
        window.yibiao.projects.current({}),
        window.yibiao.projects.list(),
      ]);
      setCurrent(cur);
      setProjects(listResult?.items || []);
    } catch (error) {
      // 静默失败：项目切换器不是关键路径，不让它打断用户
      console.warn('[项目] 加载失败', error);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const handleSelect = async (project: ProjectItem) => {
    if (project.id === current?.id) {
      setOpen(false);
      return;
    }
    setLoading(true);
    try {
      await window.yibiao.projects.select({ projectId: project.id });
      // 写入 localStorage（bridge 的 ensureProject 会读这里）
      localStorage.setItem('yb_current_project', project.id);
      setCurrent(project);
      setOpen(false);
      showToast(`已切换到「${project.name}」`, 'success');
      // 通知上层重新加载数据
      onSwitch?.(project);
      // 切换项目相当于换了整个工作区，最稳妥是重新加载页面
      // （避免各页面残留上一个项目的状态）
      window.setTimeout(() => window.location.reload(), 400);
    } catch (error) {
      showToast(`切换失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = async () => {
    const name = window.prompt('新项目名称', `新项目 ${new Date().toLocaleDateString('zh-CN')}`);
    if (!name || !name.trim()) return;
    setLoading(true);
    try {
      const created = await window.yibiao.projects.create({ name: name.trim() });
      showToast(`已创建「${created.name}」`, 'success');
      await load();
      await handleSelect(created);
    } catch (error) {
      showToast(`创建失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    } finally {
      setLoading(false);
    }
  };

  const startRename = (project: ProjectItem) => {
    setRenaming(project.id);
    setRenameValue(project.name);
    window.setTimeout(() => renameInputRef.current?.select(), 50);
  };

  const submitRename = async () => {
    if (!renaming || !renameValue.trim()) {
      setRenaming('');
      return;
    }
    try {
      await window.yibiao.projects.rename({ projectId: renaming, name: renameValue.trim() });
      showToast('已重命名', 'success');
      setRenaming('');
      await load();
    } catch (error) {
      showToast(`重命名失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    }
  };

  const handleDelete = async (project: ProjectItem) => {
    if (projects.length <= 1) {
      showToast('不能删除最后一个项目', 'error');
      return;
    }
    if (!window.confirm(`确定删除「${project.name}」？\n\n该项目下的招标文件、目录、正文和产物都会一并移除。`)) {
      return;
    }
    try {
      await window.yibiao.projects.remove({ projectId: project.id });
      showToast('已删除', 'success');
      // 删的如果是当前项目，切到第一个
      if (project.id === current?.id) {
        const next = projects.find((p) => p.id !== project.id);
        if (next) await handleSelect(next);
      } else {
        await load();
      }
    } catch (error) {
      showToast(`删除失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    }
  };

  // 加载中或没有项目时不渲染，避免占位跳动
  if (!current) return null;

  return (
    <div className="project-switcher">
      <button
        type="button"
        className="project-switcher-trigger"
        onClick={() => setOpen(true)}
        title={`当前项目：${current.name}`}
      >
        <span className="project-switcher-icon" aria-hidden>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
          </svg>
        </span>
        <span className="project-switcher-name">{current.name}</span>
        <span className="project-switcher-caret" aria-hidden>
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
            <path d="M6 9l6 6 6-6" />
          </svg>
        </span>
      </button>

      <Dialog.Root open={open} onOpenChange={setOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="project-dialog-overlay" />
          <Dialog.Content className="project-dialog">
            <Dialog.Title className="project-dialog-title">切换项目</Dialog.Title>
            <Dialog.Description className="project-dialog-desc">
              每个项目有独立的招标文件、目录、正文和产物
            </Dialog.Description>

            <ul className="project-list">
              {projects.map((project) => {
                const isCurrent = project.id === current.id;
                return (
                  <li key={project.id} className={`project-item${isCurrent ? ' is-current' : ''}`}>
                    {renaming === project.id ? (
                      <input
                        ref={renameInputRef}
                        className="project-rename-input"
                        value={renameValue}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onBlur={() => void submitRename()}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') void submitRename();
                          if (e.key === 'Escape') setRenaming('');
                        }}
                      />
                    ) : (
                      <button
                        type="button"
                        className="project-item-main"
                        onClick={() => void handleSelect(project)}
                        disabled={loading}
                      >
                        <span className="project-item-name">
                          {project.name}
                          {isCurrent ? <span className="project-item-badge">当前</span> : null}
                          {project.runningTasks > 0 ? (
                            <span className="project-item-running" title={`${project.runningTasks} 个任务运行中`}>
                              进行中
                            </span>
                          ) : null}
                        </span>
                        <span className="project-item-meta">
                          {project.tenderFileName ? (
                            <>
                              <span>{project.tenderFileName}</span>
                              <span>·</span>
                            </>
                          ) : (
                            <>
                              <span>未导入招标文件</span>
                              <span>·</span>
                            </>
                          )}
                          <span>{project.outlineNodeCount} 节</span>
                          <span>·</span>
                          <span>{project.contentCount} 节已成文</span>
                          <span>·</span>
                          <span>{stepLabel(project.step)}</span>
                        </span>
                      </button>
                    )}

                    <div className="project-item-actions">
                      <button
                        type="button"
                        className="project-item-action"
                        onClick={() => startRename(project)}
                        title="重命名"
                      >
                        改名
                      </button>
                      {projects.length > 1 ? (
                        <button
                          type="button"
                          className="project-item-action is-danger"
                          onClick={() => void handleDelete(project)}
                          title="删除项目"
                        >
                          删除
                        </button>
                      ) : null}
                    </div>
                  </li>
                );
              })}
            </ul>

            {projects.length === 1 ? (
              <p className="project-dialog-hint">
                新建项目后可以在这里切换。每个项目的招标文件、目录和正文互相独立。
              </p>
            ) : null}

            <div className="project-dialog-footer">
              <button type="button" className="secondary-action" onClick={() => void handleCreate()} disabled={loading}>
                新建项目
              </button>
              <Dialog.Close asChild>
                <button type="button" className="primary-action">
                  关闭
                </button>
              </Dialog.Close>
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </div>
  );
}
