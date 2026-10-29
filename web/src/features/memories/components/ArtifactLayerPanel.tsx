import { Alert, Button, Card, Select, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage } from '../../../shared/api/errors';
import {
  artifactStates,
  type ArtifactKind,
  type ArtifactMeta,
  type ArtifactState,
} from '../../../shared/api/types';
import { codeDefinition, reasonText } from '../../../shared/lib/codes';
import { artifactName } from '../../../shared/lib/display';
import { routes } from '../../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../../shared/lib/time';
import { ResponsiveList } from '../../../shared/ui/ResponsiveList';
import { StatusTag } from '../../../shared/ui/StatusTag';
import { useArtifactLayer } from '../../artifacts/queries';

const columns: ColumnsType<ArtifactMeta> = [
  {
    title: '整理内容',
    dataIndex: 'id',
    render: (_, item) => (
      <Space direction="vertical" size={0} style={{ maxWidth: 380 }}>
        <Link to={routes.artifact(item.id)}>{artifactName(item)}</Link>
        <Typography.Text type="secondary" ellipsis>
          {item.scope_key || '范围未指定'} · {item.topic || '主题未指定'}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: '层级',
    dataIndex: 'layer',
    width: 120,
    render: (value: string) => <StatusTag category="artifactLayer" value={value} />,
  },
  {
    title: '类型',
    dataIndex: 'kind',
    width: 130,
    render: (value: string) => <StatusTag category="artifactKind" value={value} />,
  },
  {
    title: '状态',
    dataIndex: 'state',
    width: 110,
    render: (value: string) => <StatusTag category="artifactState" value={value} />,
  },
  {
    title: '当前版本',
    dataIndex: 'current_version',
    width: 110,
    render: (value: number) =>
      value > 0 ? `v${value}` : <Tag color="default">尚无内容</Tag>,
  },
  {
    title: '说明',
    dataIndex: 'reason_code',
    render: (value: string | undefined) => (value ? reasonText(value) : '—'),
  },
  {
    title: '更新时间',
    dataIndex: 'updated_at',
    width: 190,
    render: (value: string) => (
      <span title={timestampTitle(value)}>{formatTimestamp(value)}</span>
    ),
  },
];

export function ArtifactLayerPanel({
  kinds,
  title,
  description,
  emptyText,
  limit = 50,
}: {
  kinds: ArtifactKind[];
  title: string;
  description: string;
  emptyText: string;
  limit?: number;
}) {
  const [state, setState] = useState<ArtifactState | undefined>();
  const artifacts = useArtifactLayer(kinds, state, limit);
  const items = artifacts.data ?? [];

  return (
    <Card
      title={`${title}（${items.length}）`}
      extra={
        <Space wrap>
          <Select
            allowClear
            placeholder="状态：全部"
            style={{ width: 150 }}
            value={state}
            options={artifactStates.map((value) => ({
              value,
              label: codeDefinition('artifactState', value).label,
            }))}
            onChange={(value: ArtifactState | undefined) => setState(value)}
          />
          <Button onClick={() => void artifacts.refetch()} loading={artifacts.isFetching}>
            刷新
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {description}
      </Typography.Paragraph>

      {artifacts.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={artifacts.data ? 'warning' : 'error'}
          showIcon
          message={
            artifacts.data ? '暂时无法刷新，显示上次读取的内容' : '分层内容读取失败'
          }
          description={errorMessage(artifacts.error)}
          action={
            <Button size="small" onClick={() => void artifacts.refetch()}>
              重试
            </Button>
          }
        />
      ) : null}

      <ResponsiveList<ArtifactMeta>
        items={items}
        rowKey={(item) => item.id}
        loading={artifacts.isPending}
        emptyText={emptyText}
        table={
          <Table<ArtifactMeta>
            rowKey="id"
            size="small"
            loading={artifacts.isPending}
            columns={columns}
            dataSource={items}
            pagination={false}
            scroll={{ x: 1080 }}
            locale={{ emptyText }}
          />
        }
        card={(item) => (
          <Card
            size="small"
            title={<Link to={routes.artifact(item.id)}>{artifactName(item)}</Link>}
            extra={
              <span title={timestampTitle(item.updated_at)}>
                {formatTimestamp(item.updated_at)}
              </span>
            }
          >
            <Space direction="vertical" size={6} style={{ width: '100%' }}>
              <Space wrap size={4}>
                <StatusTag category="artifactLayer" value={item.layer} />
                <StatusTag category="artifactKind" value={item.kind} />
                <StatusTag category="artifactState" value={item.state} />
                {item.current_version > 0 ? (
                  <Tag>v{item.current_version}</Tag>
                ) : (
                  <Tag>尚无内容</Tag>
                )}
              </Space>
              <Typography.Text type="secondary">
                {item.scope_key || '范围未指定'} · {item.topic || '主题未指定'}
              </Typography.Text>
              {item.reason_code ? (
                <Typography.Text type="secondary">
                  {reasonText(item.reason_code)}
                </Typography.Text>
              ) : null}
            </Space>
          </Card>
        )}
      />

      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
        每类最多显示 {limit} 项。需要更完整的筛选时，请前往
        <Link to={routes.artifacts}>自动整理</Link>。
      </Typography.Paragraph>
    </Card>
  );
}
