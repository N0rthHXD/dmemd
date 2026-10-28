import { Alert, App, Descriptions, Input, Modal, Typography } from 'antd';
import type { ReactNode } from 'react';
import { useEffect, useState } from 'react';
import { errorMessage } from '../api/errors';
import { TechnicalDetails } from './TechnicalDetails';
import { IdText } from './IdText';

interface DangerActionDialogProps<T> {
  open: boolean;
  title: string;
  targetType: string;
  targetId: string;
  targetName?: string;
  impacts: ReactNode;
  confirmText?: string;
  confirmLabel?: string;
  onClose: () => void;
  action: () => Promise<T>;
  onSuccess: (result: T) => void;
  children?: ReactNode;
}

/**
 * Explicit irreversible-action dialog. Callers provide the real action; this
 * component never derives an API call from a string label.
 */
export function DangerActionDialog<T>({
  open,
  title,
  targetType,
  targetId,
  targetName,
  impacts,
  confirmText,
  confirmLabel = '永久执行',
  onClose,
  action,
  onSuccess,
  children,
}: DangerActionDialogProps<T>) {
  const { message } = App.useApp();
  const expected = !confirmText || confirmText === targetId ? '确认永久删除' : confirmText;
  const [typed, setTyped] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (open) {
      setTyped('');
      setError(null);
      setPending(false);
    }
  }, [open]);

  const run = async () => {
    if (typed !== expected) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      const result = await action();
      onSuccess(result);
      message.success('服务端已确认操作完成');
      onClose();
    } catch (cause) {
      setError(cause);
    } finally {
      setPending(false);
    }
  };

  return (
    <Modal
      open={open}
      title={title}
      okText={confirmLabel}
      cancelText="取消"
      okButtonProps={{ danger: true, disabled: typed !== expected, loading: pending }}
      cancelButtonProps={{ disabled: pending }}
      maskClosable={!pending}
      closable={!pending}
      onOk={() => void run()}
      onCancel={onClose}
      destroyOnHidden
    >
      <Alert
        type="error"
        showIcon
        message="此操作不可恢复"
        description={impacts}
        style={{ marginBottom: 16 }}
      />
      <Descriptions size="small" column={1} bordered style={{ marginBottom: 16 }}>
        <Descriptions.Item label="目标类型">{targetType}</Descriptions.Item>
        <Descriptions.Item label="内容">{targetName || '当前选中的内容'} <IdText value={targetId} /></Descriptions.Item>
      </Descriptions>
      <TechnicalDetails><Typography.Text copyable>{targetId}</Typography.Text></TechnicalDetails>
      {children}
      <Typography.Paragraph>
        请输入确认文字 <Typography.Text code>{expected}</Typography.Text>：
      </Typography.Paragraph>
      <Input
        value={typed}
        status={typed && typed !== expected ? 'error' : undefined}
        placeholder={expected}
        disabled={pending}
        onChange={(event) => setTyped(event.target.value)}
        onPressEnter={() => void run()}
      />
      {error ? (
        <Alert
          style={{ marginTop: 12 }}
          type="error"
          showIcon
          message="操作失败，服务端未确认成功"
          description={errorMessage(error)}
        />
      ) : null}
    </Modal>
  );
}
