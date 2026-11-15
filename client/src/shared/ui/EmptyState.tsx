import type { ReactNode } from 'react';

/** 空态的语义变体：决定图标与配色。 */
export type EmptyStateTone = 'neutral' | 'info' | 'success' | 'warning' | 'error';

/** 空态的尺寸：card 用于面板内，page 用于整页。 */
export type EmptyStateSize = 'card' | 'page';

export interface EmptyStateProps {
  title: ReactNode;
  /** 标题下的说明文案 */
  hint?: ReactNode;
  /** 可选的引导操作，例如直达主操作的按钮 */
  children?: ReactNode;
  className?: string;
  /**
   * 语义变体。默认 neutral。
   * 「无重复」「已完成」这类正面结果用 success，
   * 避免用空态表达好消息。
   */
  tone?: EmptyStateTone;
  /** 尺寸。card 嵌在面板里，page 占满整页。 */
  size?: EmptyStateSize;
  /**
   * 自定义图标（emoji 或 SVG）。
   * 不传时按 tone 使用默认符号。
   */
  icon?: ReactNode;
  /** 底部的补充提示（如「💡 也可以先用系统模板」） */
  tip?: ReactNode;
}

const TONE_ICON: Record<EmptyStateTone, string> = {
  neutral: '○',
  info: 'i',
  success: '✓',
  warning: '!',
  error: '×',
};

/**
 * 统一空态卡片。
 *
 * 设计规范（三段式）：
 *   1. 图标 — 说明这是什么状态
 *   2. 标题 + 说明 — 为什么这里没内容
 *   3. 主操作 — 下一步能做什么（关键，不要只写「暂无数据」）
 *
 * 为什么统一：早期各页面自己写空态，有的占满整屏、有的只有
 * 一行灰字、有的把成功结果也画成空态，观感割裂且用户
 * 常常不知道下一步该点什么。
 */
export default function EmptyState({
  title,
  hint,
  children,
  className,
  tone = 'neutral',
  size = 'card',
  icon,
  tip,
}: EmptyStateProps) {
  const classes = [
    'yb-empty-state',
    `is-${tone}`,
    `is-${size}`,
    className || '',
  ].filter(Boolean).join(' ');

  return (
    <div className={classes}>
      <div className="yb-empty-state-icon" aria-hidden="true">
        {icon ?? TONE_ICON[tone]}
      </div>
      <strong className="yb-empty-state-title">{title}</strong>
      {hint != null ? <span className="yb-empty-state-hint">{hint}</span> : null}
      {children ? <div className="yb-empty-state-actions">{children}</div> : null}
      {tip != null ? <div className="yb-empty-state-tip">{tip}</div> : null}
    </div>
  );
}
