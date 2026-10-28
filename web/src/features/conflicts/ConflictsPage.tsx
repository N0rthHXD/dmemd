import {
  Alert,
  Button,
  Card,
  Segmented,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../shared/api/errors';
import { normalizeConflictFilters } from '../../shared/api/query-keys';
import type { Conflict } from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { ResponsiveList } from '../../shared/ui/ResponsiveList';
import { StatusTag } from '../../shared/ui/StatusTag';
import { useConflicts } from './queries';

const columns: ColumnsType<Conflict> = [
  {
    title: '需要核对的内容',
    dataIndex: 'id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        <Link to={routes.conflict(item.id)}>{codeDefinition('conflictReason', item.reason_code).label}</Link>
        <Typography.Text type="secondary">涉及 {item.members.length} 条记忆，查看说法与来源</Typography.Text>
      </Space>
    ),
  },
  {
    title: '状态',
    dataIndex: 'status',
    width: 110,
    render: (value: string) => <StatusTag category="conflictStatus" value={value} />,
  },
  {
    title: '原因',
    dataIndex: 'reason_code',
    width: 140,
    render: (value: string) => (
      <Tag color="warning">{codeDefinition('conflictReason', value).label}</Tag>
    ),
  },
  {
    title: '相关记忆',
    dataIndex: 'members',
    width: 100,
    render: (value: Conflict['members']) => value.length,
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

export default function ConflictsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const status = searchParams.get('status') === 'resolved' ? 'resolved' : 'open';
  const rawLimit = Number(searchParams.get('limit'));
  const limit = [20, 50, 100, 200].includes(rawLimit) ? rawLimit : 50;
  const filters = useMemo(
    () => normalizeConflictFilters({ status, limit }),
    [status, limit],
  );
  const conflicts = useConflicts(filters);
  const items = conflicts.data ?? [];

  const update = (nextStatus: 'open' | 'resolved', nextLimit: number) => {
    const params = new URLSearchParams();
    if (nextStatus !== 'open') {
      params.set('status', nextStatus);
    }
    if (nextLimit !== 50) {
      params.set('limit', String(nextLimit));
    }
    setSearchParams(params, { replace: true });
  };

  return (
    <>
      <Space
        style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }}
        wrap
      >
        <div>
          <Typography.Title level={3} style={{ marginBottom: 0 }}>
            内容核对
          </Typography.Title>
          <Typography.Text type="secondary">
            有些记忆的说法、时间或适用情况不一致。对照来源后，决定如何保留。
          </Typography.Text>
        </div>
        <Space wrap>
          <Segmented
            value={status}
            options={[
              { value: 'open', label: '待核对' },
              { value: 'resolved', label: '已解决' },
            ]}
            onChange={(value) => update(value as 'open' | 'resolved', limit)}
          />
          <Select
            value={limit}
            style={{ width: 130 }}
            options={[20, 50, 100, 200].map((value) => ({
              value,
              label: `最多 ${value} 条`,
            }))}
            onChange={(value) => update(status, value)}
          />
          <Button onClick={() => void conflicts.refetch()} loading={conflicts.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      {conflicts.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={conflicts.data ? 'warning' : 'error'}
          showIcon
          message={conflicts.data ? '刷新失败，显示上次成功读取的冲突' : '冲突列表读取失败'}
          description={errorMessage(conflicts.error)}
          action={
            <Button size="small" onClick={() => void conflicts.refetch()}>
              重试
            </Button>
          }
        />
      ) : null}

      <Card
        title={`${status === 'open' ? '待核对' : '已处理'}事项（${items.length}）`}
        extra={
          items.length >= limit ? (
            <Tag color="warning">已达到本次条数上限，可能还有更多记录</Tag>
          ) : null
        }
      >
        <ResponsiveList<Conflict>
          items={items}
          rowKey={(item) => item.id}
          loading={conflicts.isPending}
          emptyText={status === 'open' ? '本次没有返回需要核对的内容' : '本次没有返回已处理的记录'}
          table={
            <Table<Conflict>
              rowKey="id"
              size="small"
              loading={conflicts.isPending}
              columns={columns}
              dataSource={items}
              pagination={false}
              scroll={{ x: 900 }}
              locale={{ emptyText: status === 'open' ? '没有未决冲突' : '没有已解决冲突' }}
            />
          }
          card={(item) => (
            <Card
              size="small"
              title={<Link to={routes.conflict(item.id)}>{codeDefinition('conflictReason', item.reason_code).label}</Link>}
              extra={<span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>}
            >
              <Space direction="vertical" size={6}>
                <Space wrap size={4}>
                  <StatusTag category="conflictStatus" value={item.status} />
                  <Tag color="warning">
                    {codeDefinition('conflictReason', item.reason_code).label}
                  </Tag>
                  <Tag>涉及 {item.members.length} 条记忆</Tag>
                </Space>
                <Typography.Text type="secondary">
                  打开详情，对照内容与来源后确认。
                </Typography.Text>
              </Space>
            </Card>
          )}
        />
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          显示最近返回的记录，最多 {limit} 项；不代表全部历史。
        </Typography.Paragraph>
      </Card>
    </>
  );
}
