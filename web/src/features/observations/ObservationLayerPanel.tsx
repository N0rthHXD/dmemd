import { Alert, Button, Card, Input, Select, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage } from '../../shared/api/errors';
import {
  observationKinds,
  observationStatuses,
  type Observation,
  type ObservationKind,
  type ObservationStatus,
} from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { ResponsiveList } from '../../shared/ui/ResponsiveList';
import { StatusTag } from '../../shared/ui/StatusTag';
import { useObservations } from './queries';

const columns: ColumnsType<Observation> = [
  {
    title: '原始观察',
    dataIndex: 'content',
    render: (_, item) => (
      <Space direction="vertical" size={0} style={{ maxWidth: 460 }}>
        <Link to={routes.observation(item.id)}>
          <Typography.Text ellipsis>{item.content}</Typography.Text>
        </Link>
        <Typography.Text type="secondary">
          来源 {item.source_id.slice(0, 8)} · 角色 {item.origin_role || '未知'}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: '类型',
    dataIndex: 'kind',
    width: 100,
    render: (value: string) => <StatusTag category="memoryKind" value={value} />,
  },
  {
    title: '正文模式',
    dataIndex: 'content_mode',
    width: 130,
    render: (value: string) => <StatusTag category="observationContentMode" value={value} />,
  },
  {
    title: '状态',
    dataIndex: 'status',
    width: 110,
    render: (value: string) => <StatusTag category="observationStatus" value={value} />,
  },
  {
    title: '所属范围',
    dataIndex: 'scope_key',
    width: 160,
    render: (value: string | undefined) =>
      value ? <Tag>{value}</Tag> : <Typography.Text type="secondary">未知</Typography.Text>,
  },
  {
    title: '创建时间',
    dataIndex: 'created_at',
    width: 180,
    render: (value: string) => (
      <span title={timestampTitle(value)}>{formatTimestamp(value)}</span>
    ),
  },
];

export function ObservationLayerPanel() {
  const [sourceId, setSourceId] = useState('');
  const [scopeKey, setScopeKey] = useState('');
  const [kind, setKind] = useState<ObservationKind | undefined>();
  const [status, setStatus] = useState<ObservationStatus | undefined>();
  const [limit, setLimit] = useState(50);
  const observations = useObservations({ sourceId, scopeKey, kind, status, limit });
  const items = observations.data ?? [];

  return (
    <Card
      title={`L0 原始观察（${items.length}）`}
      extra={
        <Space wrap>
          <Input.Search
            allowClear
            placeholder="来源 ID"
            style={{ width: 220 }}
            onSearch={setSourceId}
          />
          <Input.Search
            allowClear
            placeholder="所属范围"
            style={{ width: 180 }}
            onSearch={setScopeKey}
          />
          <Select
            allowClear
            placeholder="类型"
            style={{ width: 120 }}
            value={kind}
            options={observationKinds.map((value) => ({
              value,
              label: codeDefinition('memoryKind', value).label,
            }))}
            onChange={(value: ObservationKind | undefined) => setKind(value)}
          />
          <Select
            allowClear
            placeholder="状态"
            style={{ width: 130 }}
            value={status}
            options={observationStatuses.map((value) => ({
              value,
              label: codeDefinition('observationStatus', value).label,
            }))}
            onChange={(value: ObservationStatus | undefined) => setStatus(value)}
          />
          <Select
            style={{ width: 120 }}
            value={limit}
            options={[20, 50, 100, 200].map((value) => ({ value, label: `最多 ${value}` }))}
            onChange={setLimit}
          />
          <Button onClick={() => void observations.refetch()} loading={observations.isFetching}>
            刷新
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        原始观察是首次提取形成的不可变 L0 记录，不代表已经形成的当前记忆。客户端不能直接修改观察内容。
      </Typography.Paragraph>
      {observations.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message="观察列表读取失败"
          description={errorMessage(observations.error)}
        />
      ) : null}
      <ResponsiveList<Observation>
        items={items}
        rowKey={(item) => item.id}
        loading={observations.isPending}
        emptyText="没有符合条件的原始观察"
        table={
          <Table<Observation>
            rowKey="id"
            size="small"
            loading={observations.isPending}
            columns={columns}
            dataSource={items}
            pagination={false}
            scroll={{ x: 1100 }}
            locale={{ emptyText: '没有符合条件的原始观察' }}
          />
        }
        card={(item) => (
          <Card
            size="small"
            title={<Link to={routes.observation(item.id)}>{item.content || '（空观察）'}</Link>}
            extra={<span title={timestampTitle(item.created_at)}>{formatTimestamp(item.created_at)}</span>}
          >
            <Space wrap size={4}>
              <StatusTag category="memoryKind" value={item.kind} />
              <StatusTag category="observationContentMode" value={item.content_mode} />
              <StatusTag category="observationStatus" value={item.status} />
              {item.scope_key ? <Tag>{item.scope_key}</Tag> : null}
            </Space>
          </Card>
        )}
      />
    </Card>
  );
}
