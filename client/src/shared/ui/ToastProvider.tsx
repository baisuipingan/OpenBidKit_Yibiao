import * as Toast from '@radix-ui/react-toast';
import { createContext, useCallback, useContext, useMemo, useReducer, useRef, type ReactNode } from 'react';

export type ToastType = 'success' | 'error' | 'info';

export interface ToastAction {
  label: string;
  onClick?: () => void | Promise<void>;
  close?: boolean;
  variant?: 'primary' | 'secondary';
}

export interface ToastOptions {
  /**
   * 是否参与去重（默认 true）。
   *
   * 相同文案 + 类型在 3 秒内只显示一次，避免同一错误被
   * 局部 catch 和全局兜底各弹一遍。
   * 设为 false 时强制显示（如用户手动重试的场景）。
   */
  dedupe?: boolean;
  title?: string;
  duration?: number;
  persistent?: boolean;
  actions?: ToastAction[];
}

interface ToastItem {
  id: number;
  title?: string;
  message: string;
  type: ToastType;
  duration: number;
  actions?: ToastAction[];
}

interface ToastContextValue {
  showToast: (message: string, type?: ToastType, options?: ToastOptions) => number;
  dismissToast: (id: number) => void;
}

interface ToastState {
  items: ToastItem[];
  sessionId: number;
}

type ToastStateAction =
  | { type: 'add'; item: ToastItem }
  | { type: 'dismiss'; id: number };

const ToastContext = createContext<ToastContextValue | null>(null);

let toastId = 0;

const toastTitleMap: Record<ToastType, string> = {
  success: '完成',
  error: '出错了',
  info: '提示',
};

const getToastDuration = (type: ToastType) => (type === 'error' ? 5000 : 2000);
const persistentToastDuration = 2147483647;

const initialToastState: ToastState = {
  items: [],
  sessionId: 0,
};

// 管理提示列表，并在新一批提示开始时刷新 Radix 提示层，清除上一批遗留的暂停状态。
function toastReducer(state: ToastState, action: ToastStateAction): ToastState {
  if (action.type === 'add') {
    return {
      items: [...state.items, action.item],
      sessionId: state.items.length === 0 ? state.sessionId + 1 : state.sessionId,
    };
  }

  const items = state.items.filter((item) => item.id !== action.id);
  return items.length === state.items.length ? state : { ...state, items };
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [{ items: toasts, sessionId }, dispatch] = useReducer(toastReducer, initialToastState);

  /**
   * 去重记录：文案+类型 → 上次显示时间。
   *
   * 背景：同一个错误常常被两处代码各弹一次（局部 catch + 全局兜底），
   * 用户看到两个一模一样的提示，既噪音大又像是重复报了两次错。
   *
   * 策略：同样的文案+类型，在窗口期内只显示一次。
   * 窗口取 3 秒 —— 同一操作产生的重复提示通常前后差几百毫秒，
   * 而用户 3 秒后主动再触发同一操作时应该正常提示。
   */
  const recentToastsRef = useRef<Map<string, number>>(new Map());

  const dismissToast = useCallback((id: number) => {
    dispatch({ type: 'dismiss', id });
  }, []);

  const showToast = useCallback((message: string, type: ToastType = 'info', options: ToastOptions = {}) => {
    // 去重：相同文案+类型在 3 秒内只显示一次。
    // options.dedupe === false 可强制显示（如用户手动重试）。
    if (options.dedupe !== false) {
      const key = `${type}::${options.title || ''}::${message}`;
      const now = Date.now();
      const last = recentToastsRef.current.get(key);
      if (last && now - last < 3000) {
        return -1; // 已显示过，跳过
      }
      recentToastsRef.current.set(key, now);

      // 顺手清理过期记录，避免 Map 无限增长
      if (recentToastsRef.current.size > 40) {
        for (const [k, ts] of recentToastsRef.current) {
          if (now - ts > 10000) recentToastsRef.current.delete(k);
        }
      }
    }

    const id = ++toastId;
    dispatch({
      type: 'add',
      item: {
        id,
        title: options.title,
        message,
        type,
        duration: options.persistent ? persistentToastDuration : options.duration || getToastDuration(type),
        actions: options.actions,
      },
    });
    return id;
  }, []);

  const handleActionClick = useCallback((item: ToastItem, action: ToastAction) => {
    const shouldClose = action.close !== false;
    try {
      const result = action.onClick?.();
      if (result && typeof (result as Promise<void>).then === 'function') {
        void (result as Promise<void>)
          .catch((error) => console.warn('Toast 操作执行失败', error))
          .finally(() => {
            if (shouldClose) {
              dismissToast(item.id);
            }
          });
        return;
      }
    } catch (error) {
      console.warn('Toast 操作执行失败', error);
    }

    if (shouldClose) {
      dismissToast(item.id);
    }
  }, [dismissToast]);

  const value = useMemo(() => ({ showToast, dismissToast }), [showToast, dismissToast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <Toast.Provider key={sessionId} swipeDirection="right">
        {toasts.map((item) => (
          <Toast.Root
            className={`app-toast is-${item.type}${item.actions?.length ? ' has-actions' : ''}`}
            duration={item.duration}
            key={item.id}
            onOpenChange={(open) => {
              if (!open) {
                dismissToast(item.id);
              }
            }}
          >
            <Toast.Title className="app-toast-title">{item.title || toastTitleMap[item.type]}</Toast.Title>
            <Toast.Description className="app-toast-description">{item.message}</Toast.Description>
            {item.actions?.length ? (
              <div className="app-toast-actions">
                {item.actions.map((action) => (
                  <button
                    type="button"
                    className={`app-toast-action is-${action.variant || 'secondary'}`}
                    key={action.label}
                    onClick={() => handleActionClick(item, action)}
                  >
                    {action.label}
                  </button>
                ))}
              </div>
            ) : null}
            <Toast.Close className="app-toast-close" aria-label="关闭提示">×</Toast.Close>
          </Toast.Root>
        ))}
        <Toast.Viewport className="app-toast-viewport" />
      </Toast.Provider>
    </ToastContext.Provider>
  );
}

export function useToast() {
  const context = useContext(ToastContext);

  if (!context) {
    throw new Error('useToast 必须在 ToastProvider 内使用');
  }

  return context;
}
