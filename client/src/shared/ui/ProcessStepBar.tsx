import { useMemo } from 'react';

/** 步骤状态 */
export type ProcessStepStatus = 'done' | 'active' | 'pending' | 'error';

export interface ProcessStep {
  /** 步骤标识（与业务 step 值对应） */
  key: string;
  /** 步骤显示名，如「选择标书」 */
  label: string;
  /** 状态副标题，如「4,182 字」「101 节点」。为空则不显示 */
  meta?: string;
  /** 状态，不传时由 current 推导 */
  status?: ProcessStepStatus;
  /** 出错时的提示（显示在 meta 位置，且用红色） */
  errorText?: string;
}

interface ProcessStepBarProps {
  steps: ProcessStep[];
  /** 当前步骤 key。用于推导 done / active / pending */
  currentKey: string;
  /** 允许点击回跳的步骤（通常是已完成的） */
  onStepClick?: (key: string) => void;
  /** 是否禁用所有交互（如任务运行中） */
  disabled?: boolean;
}

/**
 * 横向步骤条：显示多步流程的全局进度。
 *
 * 为什么需要：技术方案有 7 步，中间还有多次 AI 长等待。
 * 每个页面只显示「STEP 03」这种局部标识时，用户不知道总共几步、
 * 还剩几步。长等待场景下尤其容易怀疑是不是卡住了。
 *
 * 状态推导规则：
 *   - 显式传了 status 就用（支持 error）
 *   - 当前步骤 → active
 *   - 当前步骤之前 → done
 *   - 当前步骤之后 → pending
 */
function ProcessStepBar({ steps, currentKey, onStepClick, disabled = false }: ProcessStepBarProps) {
  const resolved = useMemo(() => {
    const currentIndex = steps.findIndex((s) => s.key === currentKey);

    return steps.map((step, index) => {
      let status: ProcessStepStatus;
      if (step.status) {
        status = step.status;
      } else if (index < currentIndex) {
        status = 'done';
      } else if (index === currentIndex) {
        status = 'active';
      } else {
        status = 'pending';
      }
      return { ...step, status };
    });
  }, [steps, currentKey]);

  return (
    <div className="process-stepbar" role="list" aria-label="流程步骤">
      {resolved.map((step, index) => {
        const clickable = !disabled && step.status === 'done' && Boolean(onStepClick);
        const isLast = index === resolved.length - 1;

        return (
          <div
            key={step.key}
            role="listitem"
            className={`process-step is-${step.status}${clickable ? ' is-clickable' : ''}${isLast ? ' is-last' : ''}`}
            onClick={clickable ? () => onStepClick?.(step.key) : undefined}
            title={clickable ? `回到「${step.label}」` : undefined}
          >
            <div className="process-step-dot" aria-hidden="true">
              {step.status === 'done' ? '✓' : step.status === 'error' ? '!' : index + 1}
            </div>
            <div className="process-step-label">{step.label}</div>
            {step.errorText ? (
              <div className="process-step-meta is-error">{step.errorText}</div>
            ) : step.meta ? (
              <div className="process-step-meta">{step.meta}</div>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

export default ProcessStepBar;
