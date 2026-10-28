import { Card, Descriptions, Space, Tag, Typography } from 'antd';
import { Link } from 'react-router-dom';
import type { Memory } from '../../../shared/api/types';
import { codeDefinition } from '../../../shared/lib/codes';
import { routes } from '../../../shared/lib/routes';
import { JsonView } from '../../../shared/ui/JsonView';
import { MarkdownView } from '../../../shared/ui/MarkdownView';
import { MemoryTimeFields } from '../../../shared/ui/MemoryTimeFields';
import { StatusTag } from '../../../shared/ui/StatusTag';
import { IdText } from '../../../shared/ui/IdText';
import { TechnicalDetails } from '../../../shared/ui/TechnicalDetails';

export function MemorySnapshot({ memory }: { memory: Memory }) {
  return <Space direction="vertical" size={20} style={{ width: '100%' }}>
    <Card title="记忆内容">
      <Space wrap style={{ marginBottom: 16 }}><StatusTag category="memoryKind" value={memory.kind} /><StatusTag category="memoryState" value={memory.state} /><StatusTag category="assertion" value={memory.assertion_status} />
        {memory.phase && memory.phase !== 'unknown' ? <StatusTag category="memoryPhase" value={memory.phase} /> : null}
      </Space>
      <MarkdownView content={memory.content} />
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>需要调整内容时，请使用“提交更正”，补充准确的信息与上下文。</Typography.Paragraph>
    </Card>
    <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
      <Descriptions.Item label="所属范围">{memory.scope_key || '未指定'}</Descriptions.Item>
      <Descriptions.Item label="来源">{memory.origin_source_id ? <Link to={routes.source(memory.origin_source_id)}>查看来源原文</Link> : '未提供来源'}</Descriptions.Item>
      <Descriptions.Item label="主题">{memory.topics.length ? <Space wrap>{memory.topics.map((topic) => <Tag key={topic}>{topic}</Tag>)}</Space> : '未分类'}</Descriptions.Item>
      <Descriptions.Item label="来源应用">{memory.created_by_app || '未提供'}</Descriptions.Item>
    </Descriptions>
    {memory.procedure ? <Card title="操作步骤">
      {memory.procedure.goal ? <Typography.Paragraph><strong>要完成的事：</strong>{memory.procedure.goal}</Typography.Paragraph> : null}
      {memory.procedure.steps?.length ? <ol>{memory.procedure.steps.map((step, index) => <li key={index}>{step}</li>)}</ol> : null}
      <Descriptions column={1} size="small">
        <Descriptions.Item label="开始前需要">{memory.procedure.preconditions?.join('；') || '未提供'}</Descriptions.Item>
        <Descriptions.Item label="完成标志">{memory.procedure.completion_conditions?.join('；') || '未提供'}</Descriptions.Item>
        <Descriptions.Item label="遇到问题时">{memory.procedure.failure_branches?.join('；') || '未提供'}</Descriptions.Item>
      </Descriptions>
    </Card> : null}
    <Card title="时间信息"><MemoryTimeFields memory={memory} /></Card>
    <TechnicalDetails>
      <Descriptions size="small" column={{ xs: 1, md: 2 }}>
        <Descriptions.Item label="记忆编号"><IdText value={memory.id} /></Descriptions.Item>
        <Descriptions.Item label="版本">第 {memory.version} 版</Descriptions.Item>
        <Descriptions.Item label="同一事项编号"><IdText value={memory.fact_group_id} /></Descriptions.Item>
        <Descriptions.Item label="适用时长"><StatusTag category="durability" value={memory.durability} /></Descriptions.Item>
        <Descriptions.Item label="保留原因">{codeDefinition('retention', memory.retention_reason).label}（不会自动过期）</Descriptions.Item>
        <Descriptions.Item label="原始主题">{memory.raw_topics.join('、') || '无'}</Descriptions.Item>
      </Descriptions>
      <Typography.Title level={5}>补充条件</Typography.Title><JsonView value={memory.qualifiers} emptyText="无补充条件" />
      {memory.procedure ? <JsonView value={memory.procedure} /> : null}
    </TechnicalDetails>
  </Space>;
}
