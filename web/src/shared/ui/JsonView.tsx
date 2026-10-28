import { Typography } from 'antd';
import { useMemo } from 'react';

export function JsonView({
  value,
  maxHeight = 360,
  emptyText = '无内容',
}: {
  value: unknown;
  maxHeight?: number;
  emptyText?: string;
}) {
  const text = useMemo(() => {
    if (value === undefined || value === null) {
      return '';
    }
    if (typeof value === 'string') {
      return value;
    }
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }, [value]);

  if (!text) {
    return <Typography.Text type="secondary">{emptyText}</Typography.Text>;
  }
  return (
    <pre
      style={{
        margin: 0,
        padding: 12,
        maxHeight,
        overflow: 'auto',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-word',
        background: 'rgba(127, 127, 127, 0.08)',
        borderRadius: 6,
        fontSize: 12,
        lineHeight: 1.6,
      }}
    >
      {text}
    </pre>
  );
}
