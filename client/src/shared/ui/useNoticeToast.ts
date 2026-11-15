import { useCallback } from 'react';
import type { AppMenuNotice } from '../types/navigation';
import { useToast } from './ToastProvider';

export const githubStarNotice: AppMenuNotice = {
  message: '正在开发中，在github给作者点个star，可以加速开发。',
  actionLabel: '点此直达',
  externalUrl: 'https://github.com/FB208/OpenBidKit_Yibiao',
};

/**
 * Web 版专属提示：本地能力在浏览器里做不到。
 *
 * 与 githubStarNotice 的区别：那些是"还没开发"，
 * 这个是"浏览器环境本身不具备该能力"（需要本地文件系统、
 * 调用本机 Office/Ghostscript 等），不该承诺开发进度。
 */
export const webUnavailableNotice: AppMenuNotice = {
  message: '该功能需要读取本机文件或调用本地程序，当前网页版无法提供，请使用桌面版。',
};

// 以提示条展示未开放功能说明，有外部链接时附带直达按钮。
export function useNoticeToast() {
  const { showToast } = useToast();

  return useCallback((notice: AppMenuNotice) => {
    const { externalUrl } = notice;
    showToast(notice.message, 'info', {
      duration: 7000,
      actions: externalUrl ? [
        {
          label: notice.actionLabel || '打开链接',
          variant: 'primary',
          onClick: () => openExternalUrl(externalUrl),
        },
      ] : undefined,
    });
  }, [showToast]);
}

async function openExternalUrl(url: string) {
  if (window.yibiao?.openExternal) {
    await window.yibiao.openExternal(url);
    return;
  }

  window.open(url, '_blank', 'noopener,noreferrer');
}
