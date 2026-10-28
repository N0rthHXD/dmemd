import {
  Alert,
  Button,
  Card,
  Input,
  Space,
  Spin,
  Table,
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
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { IdText } from '../../shared/ui/IdText';
import { StatusTag } from '../../shared/ui/StatusTag';
import { isTerminalStatus } from './api';
import { useJob } from './queries';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';

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
  return (
    <Space size={4}>
      <StatusTag category="jobStatus" value={job.data.status} />
      {job.data.quality_outcome ? (
        <Tooltip title="业务结果，独立于调度状态">
          <Tag>
            <StatusTag category="qualityOutcome" value={job.data.quality_outcome} />
          </Tag>
        </Tooltip>
      ) : null}
    </Space>
  );
}

export default function JobsPage() {
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
      title: '提交记录',
      dataIndex: 'job_id',
      render: (value: string) => (
        <Space direction="vertical" size={0}>
          <Link to={routes.job(value)}>查看处理详情</Link>
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
      title: '当前状态',
      key: 'live',
      width: 220,
      render: (_, item) => <LiveJobStatus jobId={item.job_id} />,
    },
    {
      title: '',
      key: 'actions',
      width: 100,
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
      <Typography.Title level={3}>处理记录</Typography.Title>
      <Typography.Paragraph type="secondary">
        查看本次打开页面期间提交的内容。刷新页面后此列表会清空，已保存的详情链接仍可使用。
      </Typography.Paragraph>

      <TechnicalDetails label="通过编号找回处理记录"><Card
        title="通过处理编号查询"
        size="small"
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
            placeholder="粘贴已保存的处理编号"
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
      </Card></TechnicalDetails>

      {receipts.isError ? (
        <Alert type="error" showIcon message={errorMessage(receipts.error)} />
      ) : null}

      <Card
        title={`本次提交（${items.length}）`}
        extra={
          <Typography.Text type="secondary">
            未完成的处理会自动更新状态
          </Typography.Text>
        }
      >
        <Table<IngestionSessionReceipt>
          rowKey="job_id"
          size="small"
          columns={columns}
          dataSource={items}
          loading={receipts.isPending}
          pagination={{ pageSize: 10, showSizeChanger: false, showTotal: (total) => `共 ${total} 条本会话回执` }}
          scroll={{ x: 1100 }}
          locale={{ emptyText: '本会话还没有提交任务；可先提交内容或按任务 ID 查询。' }}
        />
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          处理完成不代表提交的所有内容都被保留。打开详情可查看实际保存的记忆和需要确认的事项。
        </Typography.Paragraph>
      </Card>
    </>
  );
}
