import {
  Alert,
  Button,
  Card,
  Input,
  Select,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { invalidateAfterJobTerminal } from '../../shared/api/cache-effects';
import { errorMessage } from '../../shared/api/errors';
import type { IngestionSessionReceipt } from '../../shared/api/session-receipts';
import { useSessionIngestions } from '../../shared/api/session-receipts';
import type { GovernanceJob, Job, JobCategory } from '../../shared/api/types';
import { jobCategoryLabels, reasonText } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { IdText } from '../../shared/ui/IdText';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { isTerminalStatus } from './api';
import { useJob, useGovernanceJobs, useJobs } from './queries';

function LiveJobStatus({ jobId }: { jobId: string }) {
  const job = useJob(jobId);
  const queryClient = useQueryClient();
  const previousStatus = useRef<string | null>(null);
  useEffect(() => {
    const status = job.data?.status;
    if (!status) {
      return;
    }
    const previous = previousStatus.current;
    previousStatus.current = status;
    if (previous && previous !== status && isTerminalStatus(status)) {
      void invalidateAfterJobTerminal(queryClient);
    }
  }, [job.data?.status, queryClient]);
  if (job.isError) {
    return (
      <Tooltip title={errorMessage(job.error)}>
        <Tag color="error">读取失败</Tag>
      </Tooltip>
    );
  }
  if (!job.data) {
    return <Spin size="small" />;
  }
  return <StatusTag category="jobStage" value={job.data.stage} />;
}

const extractionColumns: ColumnsType<Job> = [
  {
    title: '任务',
    dataIndex: 'id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        <Link to={routes.job(item.id)}>{item.id.slice(0, 8)}</Link>
        <IdText value={item.id} maxWidth={240} />
      </Space>
    ),
  },
  {
    title: '来源',
    dataIndex: 'source_id',
    render: (value: string) => <Link to={routes.source(value)}>查看来源</Link>,
  },
  {
    title: '阶段',
    dataIndex: 'stage',
    width: 160,
    render: (value: string) => <StatusTag category="jobStage" value={value} />,
  },
  {
    title: '调度状态',
    dataIndex: 'status',
    width: 120,
    render: (value: string) => <StatusTag category="jobStatus" value={value} />,
  },
  {
    title: '观察数',
    dataIndex: 'observation_ids',
    width: 100,
    render: (value: string[]) => value.length,
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

function GovernanceJobTable({ category }: { category: JobCategory }) {
  const [status, setStatus] = useState('');
  const [limit, setLimit] = useState(50);
  const jobs = useGovernanceJobs({
    category,
    status: (status || undefined) as GovernanceJob['status'] | undefined,
    targetKey: '',
    limit,
  });

  const items = jobs.data ?? [];
  const columns: ColumnsType<GovernanceJob> = [
    {
      title: '任务',
      dataIndex: 'id',
      render: (_, item) => (
        <Space direction="vertical" size={0}>
          <span>{item.kind}</span>
          <IdText value={item.id} maxWidth={240} />
        </Space>
      ),
    },
    {
      title: '目标',
      dataIndex: 'target_key',
      render: (value: string) => <Typography.Text code>{value}</Typography.Text>,
    },
    {
      title: '阶段',
      dataIndex: 'stage',
      width: 170,
      render: (value: string) => <StatusTag category="jobStage" value={value} />,
    },
    {
      title: '调度状态',
      dataIndex: 'status',
      width: 120,
      render: (value: string) => <StatusTag category="jobStatus" value={value} />,
    },
    {
      title: '尝试',
      dataIndex: 'attempts',
      width: 80,
    },
    {
      title: '失败',
      dataIndex: 'consecutive_failures',
      width: 80,
    },
    {
      title: '原因',
      dataIndex: 'last_error_code',
      render: (value: string | undefined) => (value ? reasonText(value) ?? value : '—'),
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

  return (
    <Card
      size="small"
      title={`${jobCategoryLabels[category].label}任务（${items.length}）`}
      extra={
        <Space wrap>
          <Select
            allowClear
            placeholder="状态：全部"
            style={{ width: 150 }}
            value={status || undefined}
            options={[
              'queued',
              'running',
              'waiting',
              'succeeded',
              'failed',
              'cancelled',
            ].map((value) => ({ value, label: value }))}
            onChange={(value) => setStatus(value ?? '')}
          />
          <Select
            style={{ width: 130 }}
            value={limit}
            options={[20, 50, 100, 200].map((value) => ({
              value,
              label: `最多 ${value} 条`,
            }))}
            onChange={setLimit}
          />
          <Button onClick={() => void jobs.refetch()} loading={jobs.isFetching}>
            刷新
          </Button>
        </Space>
      }
    >
      {jobs.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message="任务列表读取失败"
          description={errorMessage(jobs.error)}
        />
      ) : null}
      <Table<GovernanceJob>
        rowKey="id"
        size="small"
        loading={jobs.isPending}
        columns={columns}
        dataSource={items}
        pagination={false}
        scroll={{ x: 1100 }}
        locale={{ emptyText: '暂无任务' }}
      />
    </Card>
  );
}

function SessionJobs() {
  const navigate = useNavigate();
  const receipts = useSessionIngestions();
  const [manualId, setManualId] = useState('');

  const columns: ColumnsType<IngestionSessionReceipt> = [
    {
      title: '接收时间',
      dataIndex: 'received_at',
      width: 190,
      render: (value: string) => (
        <span title={timestampTitle(value)}>{formatTimestamp(value)}</span>
      ),
    },
    {
      title: '提取任务',
      dataIndex: 'job_id',
      render: (value: string) => (
        <Space direction="vertical" size={0}>
          <Link to={routes.job(value)}>查看提取详情</Link>
          <IdText value={value} maxWidth={260} />
        </Space>
      ),
    },
    {
      title: '来源',
      dataIndex: 'source_id',
      render: (value: string) => <Link to={routes.source(value)}>查看来源原文</Link>,
    },
    {
      title: '接收时状态',
      dataIndex: 'status',
      width: 120,
      render: (value: string) => <StatusTag category="jobStatus" value={value} />,
    },
    {
      title: '当前阶段',
      key: 'live',
      width: 170,
      render: (_, item) => <LiveJobStatus jobId={item.job_id} />,
    },
    {
      title: '',
      key: 'actions',
      width: 120,
      render: (_, item) => (
        <Link to={routes.job(item.job_id)}>
          <Button size="small">详情</Button>
        </Link>
      ),
    },
  ];

  const items = receipts.data ?? [];
  return (
    <>
      <Card
        size="small"
        title="通过编号查询"
        style={{ marginBottom: 16 }}
        extra={
          <Link to={routes.newIngestion}>
            <Button type="primary">提交新内容</Button>
          </Link>
        }
      >
        <Space.Compact style={{ width: '100%', maxWidth: 620 }}>
          <Input
            value={manualId}
            placeholder="粘贴已保存的提取任务编号"
            onChange={(event) => setManualId(event.target.value)}
            onPressEnter={() => {
              if (manualId.trim()) {
                navigate(routes.job(manualId.trim()));
              }
            }}
          />
          <Button
            type="primary"
            disabled={!manualId.trim()}
            onClick={() => navigate(routes.job(manualId.trim()))}
          >
            查询
          </Button>
        </Space.Compact>
      </Card>
      {receipts.isError ? (
        <Alert type="error" showIcon message={errorMessage(receipts.error)} />
      ) : null}
      <Table<IngestionSessionReceipt>
        rowKey="job_id"
        size="small"
        columns={columns}
        dataSource={items}
        loading={receipts.isPending}
        pagination={{ pageSize: 10, showSizeChanger: false }}
        scroll={{ x: 900 }}
        locale={{ emptyText: '本会话还没有提交任务' }}
      />
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
        提取完成只表示 L0 原始观察已保存。当前记忆与派生内容可能在后台继续治理和聚合。
      </Typography.Paragraph>
    </>
  );
}

function RecentExtractionJobs() {
  const [status, setStatus] = useState('');
  const [limit, setLimit] = useState(50);
  const jobs = useJobs(status, limit);
  const items = jobs.data ?? [];
  return (
    <Card
      size="small"
      title={`最近提取任务（${items.length}）`}
      extra={
        <Space wrap>
          <Select
            allowClear
            placeholder="状态：全部"
            style={{ width: 150 }}
            value={status || undefined}
            options={['queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'].map(
              (value) => ({ value, label: value }),
            )}
            onChange={(value) => setStatus(value ?? '')}
          />
          <Select
            style={{ width: 130 }}
            value={limit}
            options={[20, 50, 100, 200].map((value) => ({
              value,
              label: `最多 ${value} 条`,
            }))}
            onChange={setLimit}
          />
          <Button onClick={() => void jobs.refetch()} loading={jobs.isFetching}>
            刷新
          </Button>
        </Space>
      }
    >
      {jobs.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message="提取任务列表读取失败"
          description={errorMessage(jobs.error)}
        />
      ) : null}
      <Table<Job>
        rowKey="id"
        size="small"
        loading={jobs.isPending}
        columns={extractionColumns}
        dataSource={items}
        pagination={false}
        scroll={{ x: 1000 }}
        locale={{ emptyText: '暂无提取任务' }}
      />
    </Card>
  );
}

export default function JobsPage() {
  return (
    <>
      <Typography.Title level={3}>处理任务</Typography.Title>
      <Typography.Paragraph type="secondary">
        提取、治理和聚合是三个独立的后台阶段。这里按阶段展示任务，不用一个任务伪装成整条链路已完成。
      </Typography.Paragraph>
      <Tabs
        items={[
          {
            key: 'extraction',
            label: '提取任务',
            children: (
              <Space direction="vertical" size={16} style={{ width: '100%' }}>
                <Card size="small" title="本会话提交">
                  <SessionJobs />
                </Card>
                <RecentExtractionJobs />
              </Space>
            ),
          },
          {
            key: 'governance',
            label: '治理任务',
            children: <GovernanceJobTable category="governance" />,
          },
          {
            key: 'aggregation',
            label: '聚合任务',
            children: <GovernanceJobTable category="aggregation" />,
          },
        ]}
      />
      <TechnicalDetails label="阶段说明">
        <Typography.Paragraph>
          提取任务只保存 L0 原始观察。治理任务把观察整理为当前记忆，聚合任务逐层生成 L1 到 L4
          派生记忆。一个阶段结束不代表后续阶段已完成。
        </Typography.Paragraph>
      </TechnicalDetails>
    </>
  );
}
