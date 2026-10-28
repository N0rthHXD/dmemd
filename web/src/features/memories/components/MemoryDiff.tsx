import { Alert, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { ReactNode } from 'react';
import type { Memory } from '../../../shared/api/types';
import { codeDefinition } from '../../../shared/lib/codes';
import { formatTemporal, formatValidInterval } from '../../../shared/lib/time';
import { JsonView } from '../../../shared/ui/JsonView';

interface DiffRow {
  key: string;
  field: string;
  current: ReactNode;
  historical: ReactNode;
  changed: boolean;
}

const text = (value: string | null | undefined) =>
  value === null || value === undefined || value === '' ? (
    <Typography.Text type="secondary">未知</Typography.Text>
  ) : (
    <Typography.Text style={{ whiteSpace: 'pre-wrap' }}>{value}</Typography.Text>
  );

const jsonText = (value: unknown) => <JsonView value={value} maxHeight={160} emptyText="无" />;

function normalized(value: unknown): string {
  return JSON.stringify(value ?? null);
}

function buildRows(current: Memory, historical: Memory): DiffRow[] {
  const rows: Omit<DiffRow, 'changed'>[] = [
    { key: 'title', field: '标题', current: text(current.title), historical: text(historical.title) },
    {
      key: 'kind',
      field: '类型',
      current: codeDefinition('memoryKind', current.kind).label,
      historical: codeDefinition('memoryKind', historical.kind).label,
    },
    {
      key: 'state',
      field: '保存状态',
      current: codeDefinition('memoryState', current.state).label,
      historical: codeDefinition('memoryState', historical.state).label,
    },
    {
      key: 'assertion',
      field: '采纳情况',
      current: codeDefinition('assertion', current.assertion_status).label,
      historical: codeDefinition('assertion', historical.assertion_status).label,
    },
    {
      key: 'phase',
      field: '阶段',
      current: codeDefinition('memoryPhase', current.phase).label,
      historical: codeDefinition('memoryPhase', historical.phase).label,
    },
    {
      key: 'durability',
      field: '适用时长',
      current: codeDefinition('durability', current.durability).label,
      historical: codeDefinition('durability', historical.durability).label,
    },
    {
      key: 'scope',
      field: '所属范围',
      current: text(current.scope_key),
      historical: text(historical.scope_key),
    },
    {
      key: 'topics',
      field: '主题',
      current: current.topics.join('、') || '无',
      historical: historical.topics.join('、') || '无',
    },
    {
      key: 'raw_topics',
      field: '原始主题',
      current: current.raw_topics.join('、') || '无',
      historical: historical.raw_topics.join('、') || '无',
    },
    {
      key: 'observed',
      field: '观测时间',
      current: formatTemporal(current.observed_at, current.temporal.observed),
      historical: formatTemporal(historical.observed_at, historical.temporal.observed),
    },
    {
      key: 'event',
      field: '事件时间',
      current: formatTemporal(current.event_time, current.temporal.event),
      historical: formatTemporal(historical.event_time, historical.temporal.event),
    },
    {
      key: 'valid',
      field: '有效区间',
      current: formatValidInterval(
        current.valid_from,
        current.valid_to,
        current.temporal.valid_from,
        current.temporal.valid_to,
      ),
      historical: formatValidInterval(
        historical.valid_from,
        historical.valid_to,
        historical.temporal.valid_from,
        historical.temporal.valid_to,
      ),
    },
    {
      key: 'fact_group',
      field: '事实组',
      current: current.fact_group_id || '无',
      historical: historical.fact_group_id || '无',
    },
    {
      key: 'qualifiers',
      field: '结构化限定',
      current: jsonText(current.qualifiers),
      historical: jsonText(historical.qualifiers),
    },
    {
      key: 'procedure',
      field: '流程结构',
      current: jsonText(current.procedure),
      historical: jsonText(historical.procedure),
    },
  ];

  const valueFor = (row: (typeof rows)[number]) => {
    switch (row.key) {
      case 'kind':
        return current.kind === historical.kind;
      case 'state':
        return current.state === historical.state;
      case 'assertion':
        return current.assertion_status === historical.assertion_status;
      case 'phase':
        return current.phase === historical.phase;
      case 'durability':
        return current.durability === historical.durability;
      case 'scope':
        return current.scope_key === historical.scope_key;
      case 'topics':
        return normalized(current.topics) === normalized(historical.topics);
      case 'raw_topics':
        return normalized(current.raw_topics) === normalized(historical.raw_topics);
      case 'observed':
        return current.observed_at === historical.observed_at;
      case 'event':
        return normalized([current.event_time, current.temporal.event]) ===
          normalized([historical.event_time, historical.temporal.event]);
      case 'valid':
        return (
          normalized([current.valid_from, current.valid_to, current.temporal.valid_from, current.temporal.valid_to]) ===
          normalized([
            historical.valid_from,
            historical.valid_to,
            historical.temporal.valid_from,
            historical.temporal.valid_to,
          ])
        );
      case 'fact_group':
        return current.fact_group_id === historical.fact_group_id;
      case 'qualifiers':
        return normalized(current.qualifiers) === normalized(historical.qualifiers);
      case 'procedure':
        return normalized(current.procedure) === normalized(historical.procedure);
      default:
        return true;
    }
  };

  const contentChanged = current.content !== historical.content;
  return [
    ...rows.map((row) => ({ ...row, changed: !valueFor(row) })),
    {
      key: 'content',
      field: '正文',
      current: <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{current.content}</Typography.Paragraph>,
      historical: <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{historical.content}</Typography.Paragraph>,
      changed: contentChanged,
    },
  ];
}

const columns: ColumnsType<DiffRow> = [
  {
    title: '字段',
    dataIndex: 'field',
    width: 130,
    render: (value: string, row) => (
      <span>
        {value} {row.changed ? <Tag color="warning">变化</Tag> : null}
      </span>
    ),
  },
  { title: '当前版本', dataIndex: 'current', width: '45%' },
  { title: '历史版本', dataIndex: 'historical', width: '45%' },
];

export function MemoryDiff({ current, historical }: { current: Memory; historical: Memory }) {
  const rows = buildRows(current, historical);
  const changed = rows.filter((row) => row.changed).length;
  return (
    <>
      <Alert
        style={{ marginBottom: 12 }}
        type={changed > 0 ? 'warning' : 'info'}
        showIcon
        message={changed > 0 ? `本次对照有 ${changed} 个字段不同` : '当前版本与所选历史版本无字段差异'}
        description="普通文本对照，不推断差异原因。"
      />
      <Table
        rowKey="key"
        size="small"
        columns={columns}
        dataSource={rows}
        pagination={false}
        scroll={{ x: 900 }}
      />
    </>
  );
}
