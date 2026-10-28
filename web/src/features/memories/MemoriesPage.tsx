import {
  Alert,
  Button,
  Card,
  DatePicker,
  Input,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { Dayjs } from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../shared/api/errors';
import { normalizeMemoryFilters } from '../../shared/api/query-keys';
import {
  assertionStatuses,
  memoryKinds,
  memoryStates,
  type Memory,
} from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, fromRFC3339, timestampTitle, toRFC3339 } from '../../shared/lib/time';
import { ResponsiveList } from '../../shared/ui/ResponsiveList';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { ArtifactLayerPanel } from './components/ArtifactLayerPanel';
import {
  isDefaultMemoryFilters,
  memoryLimitOptions,
  readMemoryFilters,
  writeMemoryFilters,
} from './filters';
import { useMemories } from './queries';

const { RangePicker } = DatePicker;

type LibraryLayer = 'memories' | 'summaries' | 'experiences' | 'skills';

function TextFilter({
  value,
  placeholder,
  onCommit,
}: {
  value: string;
  placeholder: string;
  onCommit: (value: string) => void;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return (
    <Input.Search
      allowClear
      value={draft}
      placeholder={placeholder}
      style={{ width: 210 }}
      onChange={(event) => {
        const next = event.target.value;
        setDraft(next);
        if (!next && value) {
          onCommit('');
        }
      }}
      onSearch={(next) => onCommit(next)}
    />
  );
}

const columns: ColumnsType<Memory> = [
  {
    title: '记忆',
    dataIndex: 'title',
    render: (_, item) => (
      <Space direction="vertical" size={0} style={{ maxWidth: 380 }}>
        <Link to={routes.memory(item.id)}>{item.title || '（无标题）'}</Link>
        <Typography.Text type="secondary" ellipsis>
          {item.content}
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
    title: '保存状态',
    dataIndex: 'state',
    width: 110,
    render: (value: string) => <StatusTag category="memoryState" value={value} />,
  },
  {
    title: '采纳情况',
    dataIndex: 'assertion_status',
    width: 110,
    render: (value: string) => <StatusTag category="assertion" value={value} />,
  },
  {
    title: '所属范围',
    dataIndex: 'scope_key',
    width: 150,
    render: (value: string | undefined) =>
      value ? <Tag>{value}</Tag> : <Typography.Text type="secondary">未知</Typography.Text>,
  },
  {
    title: '更新时间',
    dataIndex: 'updated_at',
    width: 180,
    render: (value: string) => (
      <span title={timestampTitle(value)}>{formatTimestamp(value)}</span>
    ),
  },
];

function MemoryLayerPanel() {
  const [searchParams, setSearchParams] = useSearchParams();
  const filters = useMemo(
    () => normalizeMemoryFilters(readMemoryFilters(searchParams)),
    [searchParams],
  );
  const memories = useMemories(filters);

  const update = (patch: Partial<typeof filters>) => {
    setSearchParams(writeMemoryFilters({ ...filters, ...patch }), { replace: true });
  };

  const rangeValue: [Dayjs | null, Dayjs | null] | null =
    filters.updatedAfter || filters.updatedBefore
      ? [fromRFC3339(filters.updatedAfter), fromRFC3339(filters.updatedBefore)]
      : null;

  const items = memories.data ?? [];
  const atLimit = items.length >= filters.limit;

  return (
    <>
      <Card size="small" title="筛选" style={{ marginBottom: 16 }}>
        <Space wrap size={[12, 12]}>
          <Select
            mode="multiple"
            allowClear
            placeholder="类型：不限制"
            style={{ minWidth: 190 }}
            value={filters.kinds}
            options={memoryKinds.map((kind) => ({
              value: kind,
              label: codeDefinition('memoryKind', kind).label,
            }))}
            onChange={(kinds) => update({ kinds })}
          />
          <Select
            mode="multiple"
            allowClear
            placeholder="保存状态：使用中和已归档"
            style={{ minWidth: 250 }}
            value={filters.states}
            options={memoryStates.map((state) => ({
              value: state,
              label: codeDefinition('memoryState', state).label,
            }))}
            onChange={(states) => update({ states })}
          />
          <Select
            mode="multiple"
            allowClear
            placeholder="采纳情况：不限"
            style={{ minWidth: 190 }}
            value={filters.assertionStatuses}
            options={assertionStatuses.map((status) => ({
              value: status,
              label: codeDefinition('assertion', status).label,
            }))}
            onChange={(assertionStatuses) => update({ assertionStatuses })}
          />
          <RangePicker
            showTime
            allowEmpty={[true, true]}
            value={rangeValue}
            placeholder={['更新时间起点', '更新时间终点']}
            onChange={(value) =>
              update({
                updatedAfter: toRFC3339(value?.[0]),
                updatedBefore: toRFC3339(value?.[1]),
              })
            }
          />
          <Select
            style={{ width: 130 }}
            value={filters.limit}
            options={memoryLimitOptions.map((limit) => ({
              value: limit,
              label: `最多 ${limit} 条`,
            }))}
            onChange={(limit) => update({ limit })}
          />
          <Button
            disabled={isDefaultMemoryFilters(filters)}
            onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}
          >
            清空筛选
          </Button>
        </Space>
        <TechnicalDetails label="更多筛选">
          <Space wrap><TextFilter value={filters.createdByApp} placeholder="来源应用" onCommit={(createdByApp) => update({ createdByApp })} />
          <TextFilter value={filters.factGroupId} placeholder="同一事项编号" onCommit={(factGroupId) => update({ factGroupId })} /></Space>
        </TechnicalDetails>
        <Typography.Paragraph type="secondary" style={{ margin: '12px 0 0' }}>
          默认显示使用中和已归档的记忆。选择“已删除”可查看能够从历史恢复的内容；需要查找正文时，请使用“搜索记忆”。
        </Typography.Paragraph>
      </Card>

      {memories.isError && memories.data !== undefined ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message="刷新失败，正在显示上次成功读取的列表"
          description={errorMessage(memories.error)}
          action={
            <Button size="small" onClick={() => void memories.refetch()}>
              重新读取
            </Button>
          }
        />
      ) : null}
      {memories.isError && memories.data === undefined ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message="记忆列表读取失败"
          description={errorMessage(memories.error)}
          action={
            <Button size="small" onClick={() => void memories.refetch()}>
              重试
            </Button>
          }
        />
      ) : null}

      <Card
        title={`最近更新的 ${items.length} 条（最多 ${filters.limit} 条）`}
        extra={
          <Space wrap>
            {atLimit ? (
              <Tag color="warning">已达到本次条数上限，可能还有更多记录未显示</Tag>
            ) : null}
            <Button
              type="text"
              onClick={() => void memories.refetch()}
              loading={memories.isFetching}
            >
              刷新
            </Button>
          </Space>
        }
      >
        <ResponsiveList<Memory>
          items={items}
          rowKey={(item) => item.id}
          loading={memories.isPending}
          emptyText="本次筛选没有返回记录"
          table={
            <Table<Memory>
              rowKey="id"
              size="small"
              loading={memories.isPending}
              columns={columns}
              dataSource={items}
              pagination={false}
              scroll={{ x: 1080 }}
              locale={{ emptyText: '本次筛选没有返回记录' }}
            />
          }
          card={(item) => (
            <Card
              size="small"
              title={<Link to={routes.memory(item.id)}>{item.title || '（无标题）'}</Link>}
              extra={<span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>}
            >
              <Space direction="vertical" size={6} style={{ width: '100%' }}>
                <Space wrap size={4}>
                  <StatusTag category="memoryKind" value={item.kind} />
                  <StatusTag category="memoryState" value={item.state} />
                  <StatusTag category="assertion" value={item.assertion_status} />
                  <Tag>v{item.version}</Tag>
                </Space>
                <Typography.Paragraph ellipsis={{ rows: 3 }} style={{ margin: 0 }}>
                  {item.content}
                </Typography.Paragraph>
                <Typography.Text type="secondary">
                  所属范围：{item.scope_key || '未指定'}
                </Typography.Text>
              </Space>
            </Card>
          )}
        />
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          这是有限条数的最近记录，不是全库总数，也没有 page/offset/cursor 分页。
        </Typography.Paragraph>
      </Card>
    </>
  );
}

export default function MemoriesPage() {
  const [activeLayer, setActiveLayer] = useState<LibraryLayer>('memories');

  return (
    <>
      <Space
        align="center"
        style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }}
        wrap
      >
        <div>
          <Typography.Title level={3} style={{ marginBottom: 0 }}>
            记忆库
          </Typography.Title>
          <Typography.Text type="secondary">
            按记忆、摘要与偏好、经验案例和操作指南分层回顾，也可以查看来源和修改历史。
          </Typography.Text>
        </div>
        <Space wrap>
          <Link to={routes.newIngestion}>
            <Button type="primary">提交新内容</Button>
          </Link>
          <Link to={routes.recall}>
            <Button>全文检索</Button>
          </Link>
        </Space>
      </Space>

      <Tabs
        activeKey={activeLayer}
        onChange={(key) => setActiveLayer(key as LibraryLayer)}
        items={[
          {
            key: 'memories',
            label: '记忆',
            children: <MemoryLayerPanel />,
          },
          {
            key: 'summaries',
            label: '摘要 / 概览 / 偏好与习惯',
            children: (
              <ArtifactLayerPanel
                kinds={['topic_summary', 'scope_summary', 'profile']}
                title="摘要 / 概览 / 偏好与习惯"
                description="根据记忆自动整理的主题摘要、范围概览，以及长期偏好与习惯。"
                emptyText="还没有可展示的摘要、概览或偏好。"
              />
            ),
          },
          {
            key: 'experiences',
            label: '经验案例',
            children: (
              <ArtifactLayerPanel
                kinds={['experience_case']}
                title="经验案例"
                description="来自应用报告的经验案例，记录目标、条件、做法和结果，不代表独立执行验证。"
                emptyText="还没有可展示的经验案例。"
              />
            ),
          },
          {
            key: 'skills',
            label: '操作指南',
            children: (
              <ArtifactLayerPanel
                kinds={['skill']}
                title="操作指南"
                description="根据操作步骤整理的参考指南，页面不会执行或安装它，也不代表安全性已验证。"
                emptyText="还没有可展示的操作指南。"
              />
            ),
          },
        ]}
      />
    </>
  );
}
