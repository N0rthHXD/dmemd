import { Alert, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { ReactNode } from 'react';
import type { Memory } from '../../../shared/api/types';
import { codeDefinition } from '../../../shared/lib/codes';

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

function buildRows(current: Memory, historical: Memory): DiffRow[] {
  const rows: Array<Omit<DiffRow, 'changed'> & { changed: boolean }> = [
    { key: 'title', field: '标题', current: text(current.title), historical: text(historical.title), changed: current.title !== historical.title },
    {
      key: 'kind',
      field: '类型',
      current: codeDefinition('memoryKind', current.kind).label,
      historical: codeDefinition('memoryKind', historical.kind).label,
      changed: current.kind !== historical.kind,
    },
    {
      key: 'state',
      field: '保存状态',
      current: codeDefinition('memoryState', current.state).label,
      historical: codeDefinition('memoryState', historical.state).label,
      changed: current.state !== historical.state,
    },
    {
      key: 'assertion',
      field: '采纳情况',
      current: codeDefinition('assertion', current.assertion_status).label,
      historical: codeDefinition('assertion', historical.assertion_status).label,
      changed: current.assertion_status !== historical.assertion_status,
    },
    {
      key: 'scope',
      field: '所属范围',
      current: text(current.scope_key),
      historical: text(historical.scope_key),
      changed: current.scope_key !== historical.scope_key,
    },
    {
      key: 'content',
      field: '正文',
      current: text(current.content),
      historical: text(historical.content),
      changed: current.content !== historical.content,
    },
  ];
  return rows;
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
