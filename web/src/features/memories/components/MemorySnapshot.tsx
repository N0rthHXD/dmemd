import { Card, Descriptions, Space, Tag, Typography } from 'antd';
import { Link } from 'react-router-dom';
import type { Memory } from '../../../shared/api/types';
import { routes } from '../../../shared/lib/routes';
import { MarkdownView } from '../../../shared/ui/MarkdownView';
import { StatusTag } from '../../../shared/ui/StatusTag';
import { IdText } from '../../../shared/ui/IdText';
import { TechnicalDetails } from '../../../shared/ui/TechnicalDetails';

export function MemorySnapshot({ memory }: { memory: Memory }) {
  return (
    <Space direction="vertical" size={20} style={{ width: '100%' }}>
      <Card title="当前规范记忆">
        <Space wrap style={{ marginBottom: 16 }}>
          <StatusTag category="memoryKind" value={memory.kind} />
          <StatusTag category="memoryState" value={memory.state} />
          <StatusTag category="assertion" value={memory.assertion_status} />
        </Space>
        <MarkdownView content={memory.content} />
        <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
          这是治理后形成的当前视图，不是首次提取结果。需要调整内容时，请提交新内容，由后台重新治理。
        </Typography.Paragraph>
      </Card>
      <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
        <Descriptions.Item label="所属范围">{memory.scope_key || '未指定'}</Descriptions.Item>
        <Descriptions.Item label="来源">
          {memory.origin_source_id ? (
            <Link to={routes.source(memory.origin_source_id)}>查看来源原文</Link>
          ) : (
            '未提供来源'
          )}
        </Descriptions.Item>
      </Descriptions>
      <TechnicalDetails>
        <Descriptions size="small" column={{ xs: 1, md: 2 }}>
          <Descriptions.Item label="记忆编号"><IdText value={memory.id} /></Descriptions.Item>
          <Descriptions.Item label="当前版本">第 {memory.version} 版</Descriptions.Item>
          <Descriptions.Item label="断言状态">
            <StatusTag category="assertion" value={memory.assertion_status} />
          </Descriptions.Item>
          <Descriptions.Item label="保存状态">
            <StatusTag category="memoryState" value={memory.state} />
          </Descriptions.Item>
        </Descriptions>
        {memory.scope_key ? <Tag>{memory.scope_key}</Tag> : null}
      </TechnicalDetails>
    </Space>
  );
}
