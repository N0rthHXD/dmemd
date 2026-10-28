import { Tag, Tooltip, Typography } from 'antd';
import type { ReactNode } from 'react';
import { codeDefinition, codeDescription, type Tone } from '../lib/codes';

const toneColors: Record<Tone, string> = {
  success: 'success',
  processing: 'processing',
  warning: 'warning',
  error: 'error',
  default: 'default',
  purple: 'purple',
  cyan: 'cyan',
  blue: 'blue',
};

interface StatusTagProps {
  category: string;
  value: string | undefined;
  showRawCode?: boolean;
}

export function StatusTag({ category, value, showRawCode = false }: StatusTagProps) {
  const definition = codeDefinition(category, value);
  const description = codeDescription(category, value);
  const tag = (
    <Tag color={toneColors[definition.tone ?? 'default']}>
      {definition.label}
      {showRawCode && value ? ` · ${value}` : ''}
    </Tag>
  );
  return description ? <Tooltip title={description}>{tag}</Tooltip> : tag;
}

export function CodeLabel({ category, value }: { category: string; value: string | undefined }) {
  const definition = codeDefinition(category, value);
  return <span>{value ? definition.label : '未知'}</span>;
}

export function CodeText({ children }: { children: ReactNode }) {
  return <Typography.Text code>{children}</Typography.Text>;
}
