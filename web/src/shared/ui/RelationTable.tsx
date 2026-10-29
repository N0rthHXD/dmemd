import { Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { Link } from 'react-router-dom';
import type { Relation } from '../api/types';
import { relationKindLabels } from '../lib/codes';
import { routes } from '../lib/routes';
import { formatTimestamp } from '../lib/time';

function endpointLink(type: string, id: string, version: number) {
  if (type === 'observation') {
    return <Link to={routes.observation(id)}>观察 {id.slice(0, 8)}</Link>;
  }
  if (type === 'memory') {
    return (
      <Link to={version > 0 ? routes.memoryVersion(id, version) : routes.memory(id)}>
        记忆 {id.slice(0, 8)}
      </Link>
    );
  }
  return <span>{type}:{id}</span>;
}

const columns: ColumnsType<Relation> = [
  {
    title: '起点',
    dataIndex: 'from_id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        {endpointLink(item.from_type, item.from_id, item.from_version)}
        <Typography.Text type="secondary">
          {item.from_type}{item.from_version > 0 ? ` · v${item.from_version}` : ''}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: '关系',
    dataIndex: 'kind',
    width: 130,
    render: (value: string) => <Tag>{relationKindLabels[value]?.label ?? value}</Tag>,
  },
  {
    title: '终点',
    dataIndex: 'to_id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        {endpointLink(item.to_type, item.to_id, item.to_version)}
        <Typography.Text type="secondary">
          {item.to_type}{item.to_version > 0 ? ` · v${item.to_version}` : ''}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: '创建时间',
    dataIndex: 'created_at',
    width: 180,
    render: (value: string) => formatTimestamp(value),
  },
];

export function RelationTable({ items }: { items: Relation[] }) {
  return (
    <Table<Relation>
      rowKey="id"
      size="small"
      columns={columns}
      dataSource={items}
      pagination={false}
      scroll={{ x: 900 }}
      locale={{ emptyText: '无关系' }}
    />
  );
}
