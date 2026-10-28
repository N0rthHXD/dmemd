import { App } from 'antd';
import { useEffect, useRef } from 'react';
import { useBlocker } from 'react-router-dom';

/**
 * Warns before leaving a dirty form. Works for in-app navigation through the
 * data router and for browser reload/close.
 */
export function UnsavedChangesGuard({ when }: { when: boolean }) {
  const blocker = useBlocker(when);
  const { modal } = App.useApp();
  const dialogOpen = useRef(false);

  useEffect(() => {
    const handler = (event: BeforeUnloadEvent) => {
      if (!when) {
        return;
      }
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [when]);

  useEffect(() => {
    if (blocker.state !== 'blocked') {
      dialogOpen.current = false;
      return;
    }
    if (dialogOpen.current) {
      return;
    }
    dialogOpen.current = true;
    modal.confirm({
      title: '离开未提交的表单？',
      content: '当前输入尚未提交，离开后不会自动保存草稿。',
      okText: '离开',
      cancelText: '继续编辑',
      onOk: () => blocker.proceed(),
      onCancel: () => blocker.reset(),
    });
  }, [blocker, modal]);

  return null;
}
