import {
  Alert,
  Button,
  Card,
  Input,
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
import { normalizeTargetFilters } from '../../shared/api/query-keys';
import {
  artifactKinds,
  targetStatuses,
  type ArtifactKind,
  type ArtifactTarget,
  type ArtifactTargetFilters,
  type TargetStatus,
} from '../../shared/api/types';
import { codeDefinition, reasonText } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatSeconds, formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { ResponsiveList } from '../../shared/ui/ResponsiveList';
import { StatusTag } from '../../shared/ui/StatusTag';
import { useArtifactTargets } from './queries';
import { artifactName } from '../../shared/lib/display';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { ArtifactNavigation } from './components/ArtifactNavigation';

const limitOptions = [20, 50, 100, 200];

function readFilters(params: URLSearchParams): ArtifactTargetFilters {
  const kind = params.get('kind');
  const status = params.get('status');
  const rawLimit = Number(params.get('limit'));
  return normalizeTargetFilters({
    scopeKey: params.get('scope_key') ?? '',
    kind: artifactKinds.includes(kind as ArtifactKind) ? (kind as ArtifactKind) : undefined,
    status: targetStatuses.includes(status as TargetStatus) ? (status as TargetStatus) : undefined,
    reasonCode: params.get('reason_code') ?? '',
    limit: limitOptions.includes(rawLimit) ? rawLimit : 50,
  });
}

function writeFilters(filters: ArtifactTargetFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.scopeKey) {
    params.set('scope_key', filters.scopeKey);
  }
  if (filters.kind) {
    params.set('kind', filters.kind);
  }
  if (filters.status) {
    params.set('status', filters.status);
  }
  if (filters.reasonCode) {
    params.set('reason_code', filters.reasonCode);
  }
  if (filters.limit !== 50) {
    params.set('limit', String(filters.limit));
  }
  return params;
}

const columns: ColumnsType<ArtifactTarget> = [
  {
    title: '正在整理的内容',
    dataIndex: 'target_key',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        <Space wrap>
          <StatusTag category="artifactKind" value={item.kind} />
          <Typography.Text strong>{artifactName(item)}</Typography.Text>
        </Space>
        <Typography.Text type="secondary">
          {item.scope_key || '范围未指定'} · {item.topic || '主题未指定'}
        </Typography.Text>
      </Space>
    ),
  },
  {
    title: '整理状态',
    dataIndex: 'status',
    width: 130,
    render: (value: string) => <StatusTag category="targetStatus" value={value} />,
  },
  {
    title: '原因',
    dataIndex: 'reason_code',
    width: 190,
    render: (value: string | undefined) =>
      value ? reasonText(value) : '—',
  },
  {
    title: '可用版本',
    dataIndex: 'has_usable_version',
    width: 130,
    render: (_, item) => (
      <Space direction="vertical" size={2}>
        <Tag color={item.has_usable_version ? 'success' : 'default'}>
          {item.has_usable_version ? '已有内容版本' : '尚无可用版本'}
        </Tag>
        {item.artifact ? (
          <Space direction="vertical" size={0}>
            <Link to={routes.artifact(item.artifact.id)}>查看整理内容</Link>
            <StatusTag category="artifactState" value={item.artifact.state} />
          </Space>
        ) : (
          <Typography.Text type="secondary">尚未生成内容</Typography.Text>
        )}
      </Space>
    ),
  },
  {
    title: '处理情况',
    dataIndex: 'job',
    width: 230,
    render: (_, item) =>
      item.job ? (
        <Space direction="vertical" size={2}>
          <StatusTag category="jobStatus" value={item.job.status} />
          {item.job.wait_reason ? <span>等待：{reasonText(item.job.wait_reason)}</span> : null}
          {item.job.last_error_code ? (
            <span>错误：{reasonText(item.job.last_error_code) ?? item.job.last_error_code}</span>
          ) : null}
          <TechnicalDetails label="处理详情"><Typography.Text type="secondary">
            尝试 {item.job.attempts} · 连续失败 {item.job.consecutive_failures}
          </Typography.Text>
          <Typography.Text type="secondary">
            已等待 {formatSeconds(item.job.age_seconds)} · 距上次变化 {formatSeconds(item.job.stale_seconds)}
          </Typography.Text></TechnicalDetails>
        </Space>
      ) : (
        <Typography.Text type="secondary">当前无处理安排</Typography.Text>
      ),
  },
  {
    title: '评估 / 更新时间',
    dataIndex: 'updated_at',
    width: 190,
    render: (_, item) => (
      <Space direction="vertical" size={2}>
        <span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>
        <Typography.Text type="secondary">
          评估：{formatTimestamp(item.evaluated_at)}
        </Typography.Text>
      </Space>
    ),
  },
];

export default function ArtifactTargetsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const filters = useMemo(() => readFilters(searchParams), [searchParams]);
  const targets = useArtifactTargets(filters);

  const items = useMemo(() => {
    const map = new Map<string, ArtifactTarget>();
    for (const page of targets.data?.pages ?? []) {
      for (const item of page.items) {
        map.set(`${item.kind}:${item.target_key}`, item);
      }
    }
    return [...map.values()];
  }, [targets.data]);

  const activeCount = items.filter(
    (item) => item.status === 'queued' || item.status === 'running' || item.status === 'waiting',
  ).length;

  const update = (patch: Partial<ArtifactTargetFilters>) => {
    setSearchParams(writeFilters({ ...filters, ...patch }), { replace: true });
  };

  return (
    <>
      <ArtifactNavigation active="progress" />
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} wrap>
        <div>
          <Typography.Title level={4} style={{ marginBottom: 0 }}>
            整理进度
          </Typography.Title>
          <Typography.Text type="secondary">
            查看哪些内容正在整理，以及暂未生成的原因。旧内容需要更新时，不会用于默认搜索。
          </Typography.Text>
        </div>
        <Space wrap>
          <Link to={routes.artifacts}>
            <Button>查看整理结果</Button>
          </Link>
          <Button onClick={() => void targets.refetch()} loading={targets.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      <Card size="small" title="筛选" style={{ marginBottom: 16 }}>
        <Space wrap>
          <Input.Search
            key={`scope-${filters.scopeKey}`}
            defaultValue={filters.scopeKey}
            allowClear
            placeholder="所属范围"
            style={{ width: 200 }}
            onSearch={(scopeKey) => update({ scopeKey })}
            onChange={(event) => {
              if (!event.target.value) {
                update({ scopeKey: '' });
              }
            }}
          />
          <Select
            allowClear
            placeholder="类型：不限制"
            style={{ width: 160 }}
            value={filters.kind}
            options={artifactKinds.map((kind) => ({
              value: kind,
              label: codeDefinition('artifactKind', kind).label,
            }))}
            onChange={(kind: ArtifactKind | undefined) => update({ kind })}
          />
          <Select
            allowClear
            placeholder="状态：不限制"
            style={{ width: 190 }}
            value={filters.status}
            options={targetStatuses.map((status) => ({
              value: status,
              label: codeDefinition('targetStatus', status).label,
            }))}
            onChange={(status: TargetStatus | undefined) => update({ status })}
          />
          <Select
            style={{ width: 130 }}
            value={filters.limit}
            options={limitOptions.map((limit) => ({
              value: limit,
              label: `每页 ${limit} 条`,
            }))}
            onChange={(limit) => update({ limit })}
          />
          <Button onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}>
            清空筛选
          </Button>
        </Space>
        <TechnicalDetails label="更多筛选">
          <Input.Search key={`reason-${filters.reasonCode}`} defaultValue={filters.reasonCode} allowClear placeholder="原因编号（可选）" style={{ maxWidth: 320 }}
            onSearch={(reasonCode) => update({ reasonCode })} onChange={(event) => { if (!event.target.value) update({ reasonCode: '' }); }} />
        </TechnicalDetails>
      </Card>

      {targets.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={targets.data ? 'warning' : 'error'}
          showIcon
          message={targets.data ? '暂时无法刷新，显示上次读取的内容' : '整理进度读取失败'}
          description={errorMessage(targets.error)}
          action={
            <Button size="small" onClick={() => void targets.refetch()}>
              重试
            </Button>
          }
        />
      ) : null}

      <Card
        title={`已加载 ${items.length} 项${activeCount > 0 ? `，${activeCount} 项待完成` : ''}`}
        extra={
          <Typography.Text type="secondary">
            {activeCount > 0 ? '处理进展会自动更新' : '可刷新查看最新情况'}
          </Typography.Text>
        }
      >
        <ResponsiveList<ArtifactTarget>
          items={items}
          rowKey={(item) => `${item.kind}:${item.target_key}`}
          loading={targets.isPending}
          emptyText="当前没有符合条件的整理记录"
          table={
            <Table<ArtifactTarget>
              rowKey={(item) => `${item.kind}:${item.target_key}`}
              size="small"
              loading={targets.isPending}
              columns={columns}
              dataSource={items}
              pagination={false}
              scroll={{ x: 1250 }}
              locale={{ emptyText: '当前没有符合条件的整理记录' }}
            />
          }
          card={(item) => (
            <Card
              size="small"
              title={artifactName(item)}
              extra={<span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>}
            >
              <Space direction="vertical" size={6} style={{ width: '100%' }}>
                <Space wrap size={4}>
                  <StatusTag category="targetStatus" value={item.status} />
                  {item.reason_code ? (
                    <Tag>{reasonText(item.reason_code) ?? item.reason_code}</Tag>
                  ) : null}
                </Space>
                <Typography.Text type="secondary">
                  {item.scope_key || '范围未指定'} · {item.topic || '主题未指定'}
                </Typography.Text>
                <Space wrap size={4}>
                  <Tag color={item.has_usable_version ? 'success' : 'default'}>
                    {item.has_usable_version ? '已有内容版本' : '尚无可用版本'}
                  </Tag>
                  {item.artifact ? (
                    <>
                      <Link to={routes.artifact(item.artifact.id)}>查看整理内容</Link>
                      <StatusTag category="artifactState" value={item.artifact.state} />
                    </>
                  ) : (
                    <Typography.Text type="secondary">尚未生成内容</Typography.Text>
                  )}
                </Space>
                {item.job ? (
                  <Space wrap size={4}>
                    <StatusTag category="jobStatus" value={item.job.status} />
                    <Typography.Text type="secondary">
                      已尝试 {item.job.attempts} 次
                    </Typography.Text>
                  </Space>
                ) : (
                  <Typography.Text type="secondary">无任务</Typography.Text>
                )}
              </Space>
            </Card>
          )}
        />
        <Space style={{ marginTop: 12 }} wrap>
          <Button
            onClick={() => void targets.fetchNextPage()}
            loading={targets.isFetchingNextPage}
            disabled={!targets.hasNextPage}
          >
            {targets.hasNextPage ? '加载更多' : '没有更多内容'}
          </Button>
          <Typography.Text type="secondary">
            尚未生成的内容会在完成后出现在整理结果中。
          </Typography.Text>
        </Space>
      </Card>
    </>
  );
}
