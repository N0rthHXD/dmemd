import { Card, Space, Typography } from 'antd';
import { isRecord } from '../api/client';
import { JsonView } from './JsonView';
import { TechnicalDetails } from './TechnicalDetails';

export function SourceContent({ value }: { value: unknown }) {
  if (typeof value === 'string') {
    return <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{value}</Typography.Paragraph>;
  }
  const messages = isRecord(value) ? value.messages : value;
  if (Array.isArray(messages) && messages.length > 0 && messages.every((message) => isRecord(message) && typeof message.content === 'string')) {
    return <Space direction="vertical" size={12} style={{ width: '100%' }}>{messages.map((message, index) => {
      const role = typeof message.role === 'string' ? message.role : '';
      const labels: Record<string, string> = { user: '用户', assistant: '助手', system: '系统', tool: '工具' };
      return <Card size="small" key={index} title={labels[role] || '对话内容'}><Typography.Paragraph style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{message.content as string}</Typography.Paragraph></Card>;
    })}<TechnicalDetails label="查看原始内容"><JsonView value={value} maxHeight={520} /></TechnicalDetails></Space>;
  }
  return <><Typography.Paragraph type="secondary">这份来源包含结构化资料，可展开查看原始内容。</Typography.Paragraph><TechnicalDetails label="查看原始内容"><JsonView value={value} maxHeight={520} /></TechnicalDetails></>;
}
