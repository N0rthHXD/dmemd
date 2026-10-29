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
import { normalizeArtifactFilters } from '../../shared/api/query-keys';
import {
  artifactKinds,
  artifactStates,
  type ArtifactKind,
  type ArtifactListFilters,
  type ArtifactMeta,
  type ArtifactState,
} from '../../shared/api/types';
import { codeDefinition, reasonText } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { ResponsiveList } from '../../shared/ui/ResponsiveList';
import { StatusTag } from '../../shared/ui/StatusTag';
import { ReportActions } from './components/ReportActions';
import { useArtifacts } from './queries';
import { artifactName } from '../../shared/lib/display';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { ArtifactNavigation } from './components/ArtifactNavigation';

const limitOptions = [20, 50, 100, 200];

function readFilters(params: URLSearchParams): ArtifactListFilters {
  const kind = params.get('kind');
  const state = params.get('state');
  const rawLimit = Number(params.get('limit'));
  return normalizeArtifactFilters({
    scopeKey: params.get('scope_key') ?? '',
    kind: artifactKinds.includes(kind as ArtifactKind) ? (kind as ArtifactKind) : undefined,
    state: artifactStates.includes(state as ArtifactState) ? (state as ArtifactState) : undefined,
    reasonCode: params.get('reason_code') ?? '',
    limit: limitOptions.includes(rawLimit) ? rawLimit : 50,
  });
}

function writeFilters(filters: ArtifactListFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.scopeKey) {
    params.set('scope_key', filters.scopeKey);
  }
  if (filters.kind) {
    params.set('kind', filters.kind);
  }
  if (filters.state) {
    params.set('state', filters.state);
  }
  if (filters.reasonCode) {
    params.set('reason_code', filters.reasonCode);
  }
  if (filters.limit !== 50) {
    params.set('limit', String(filters.limit));
  }
  return params;
}

const columns: ColumnsType<ArtifactMeta> = [
  {
    title: '整理内容',
    dataIndex: 'id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        <Link to={routes.artifact(item.id)}>{artifactName(item)}</Link>
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
    title: '所属范围 / 主题',
    dataIndex: 'scope_key',
    width: 190,
    render: (_, item) => (
      <Space direction="vertical" size={2}>
        <span>{item.scope_key || '范围未指定'}</span>
        <Typography.Text type="secondary">{item.topic || '主题未知'}</Typography.Text>
      </Space>
    ),
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
    render: (value: number) => (value > 0 ? `v${value}` : <Tag color="default">尚无内容</Tag>),
  },
  {
    title: '说明',
    dataIndex: 'reason_code',
    render: (value: string | undefined) =>
      value ? reasonText(value) : '—',
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

export default function ArtifactsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const filters = useMemo(() => readFilters(searchParams), [searchParams]);
  const artifacts = useArtifacts(filters);

  const items = useMemo(() => {
    const map = new Map<string, ArtifactMeta>();
    for (const page of artifacts.data?.pages ?? []) {
      for (const item of page.items) {
        map.set(item.id, item);
      }
    }
    return [...map.values()];
  }, [artifacts.data]);

  const update = (patch: Partial<ArtifactListFilters>) => {
    setSearchParams(writeFilters({ ...filters, ...patch }), { replace: true });
  };

  return (
    <>
      <ArtifactNavigation active="results" />
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} wrap>
        <div>
          <Typography.Title level={4} style={{ marginBottom: 0 }}>
            整理结果
          </Typography.Title>
          <Typography.Text type="secondary">
            选择一项阅读完整内容及其来源依据。
          </Typography.Text>
        </div>
        <Space wrap>
          <Link to={routes.artifactTargets}>
            <Button>查看整理进度</Button>
          </Link>
          <Button onClick={() => void artifacts.refetch()} loading={artifacts.isFetching}>
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
            style={{ width: 150 }}
            value={filters.state}
            options={artifactStates.map((state) => ({
              value: state,
              label: codeDefinition('artifactState', state).label,
            }))}
            onChange={(state: ArtifactState | undefined) => update({ state })}
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

      {artifacts.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={artifacts.data ? 'warning' : 'error'}
          showIcon
          message={artifacts.data ? '暂时无法刷新，显示上次读取的内容' : '整理结果读取失败'}
          description={errorMessage(artifacts.error)}
          action={
            <Button size="small" onClick={() => void artifacts.refetch()}>
              重试
            </Button>
          }
        />
      ) : null}

      <Card title={`已加载 ${items.length} 项整理内容`}>
        <ResponsiveList<ArtifactMeta>
          items={items}
          rowKey={(item) => item.id}
          loading={artifacts.isPending}
           emptyText="还没有符合条件的整理内容，可查看整理进度了解原因"
          table={
            <Table<ArtifactMeta>
              rowKey="id"
              size="small"
              loading={artifacts.isPending}
              columns={columns}
              dataSource={items}
              pagination={false}
              scroll={{ x: 1200 }}
              locale={{ emptyText: '当前没有符合条件的整理内容，可查看整理进度了解原因' }}
            />
          }
          card={(item) => (
            <Card
              size="small"
              title={<Link to={routes.artifact(item.id)}>{artifactName(item)}</Link>}
              extra={<span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>}
            >
              <Space direction="vertical" size={6}>
                <Space wrap size={4}>
                  <StatusTag category="artifactLayer" value={item.layer} />
                  <StatusTag category="artifactKind" value={item.kind} />
                  <StatusTag category="artifactState" value={item.state} />
                  {item.current_version > 0 ? <Tag>v{item.current_version}</Tag> : <Tag>尚无内容</Tag>}
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
        <Space style={{ marginTop: 12 }} wrap>
          <Button
            onClick={() => void artifacts.fetchNextPage()}
            loading={artifacts.isFetchingNextPage}
            disabled={!artifacts.hasNextPage}
          >
            {artifacts.hasNextPage ? '加载更多' : '没有更多内容'}
          </Button>
          <Typography.Text type="secondary">
            更换筛选条件可以查找其他整理内容。
          </Typography.Text>
        </Space>
      </Card>
      <TechnicalDetails label="使用结果反馈"><ReportActions /></TechnicalDetails>
    </>
  );
}
