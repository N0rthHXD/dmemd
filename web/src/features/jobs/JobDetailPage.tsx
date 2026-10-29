import {
  Alert,
  App,
  Button,
  Card,
  Collapse,
  Descriptions,
  Result,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef } from 'react';
import { Link, useParams } from 'react-router-dom';
import { invalidateAfterJobTerminal } from '../../shared/api/cache-effects';
import { errorMessage, isApiError } from '../../shared/api/errors';
import type { Job } from '../../shared/api/types';
import { qualityOutcomeLabels, reasonText } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import {
  formatMilliseconds,
  formatSeconds,
  formatTimestamp,
} from '../../shared/lib/time';
import { IdText } from '../../shared/ui/IdText';
import { JsonView } from '../../shared/ui/JsonView';
import { StatusTag } from '../../shared/ui/StatusTag';
import { WarningList } from '../../shared/ui/WarningList';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { useJob } from './queries';
import { isTerminalStatus } from './api';

const extractionLabels: Array<[keyof Job['extraction_stats'], string]> = [
  ['items', '提取条目'],
  ['observations', '保存的观察'],
  ['normalized_observations', '规范化正文'],
  ['pending_observations', '待绑定证据'],
  ['invalid_items', '无效条目'],
  ['duplicate_observations', '同源重复'],
  ['chunks', '分块数'],
  ['chunk_failures', '分块失败'],
];

const timingLabels: Array<[keyof Job['timings'], string]> = [
  ['queue_age_ms', '队列等待'],
  ['extraction_ms', '提取'],
  ['normalization_ms', '观察构造'],
  ['commit_ms', '提交'],
  ['total_ms', '合计'],
];

function RefList({
  title,
  values,
  to,
}: {
  title: string;
  values: string[];
  to: (value: string) => string;
}) {
  return (
    <Descriptions.Item label={title}>
      {values.length > 0 ? (
        <Space direction="vertical" size={2}>
          {values.map((value) => (
            <Link key={value} to={to(value)}>
              查看 {value}
            </Link>
          ))}
        </Space>
      ) : (
        '无'
      )}
    </Descriptions.Item>
  );
}

export default function JobDetailPage() {
  const { id = '' } = useParams();
  const job = useJob(id);
  const queryClient = useQueryClient();
  const { message } = App.useApp();
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
      message.info('提取已结束，原始观察已保存或任务已终止');
    }
  }, [job.data?.status, queryClient, message]);

  if (job.isError && !job.data) {
    if (isApiError(job.error) && job.error.status === 404) {
      return (
        <Result
          status="404"
          title="提取任务不存在"
          subTitle="请核对任务 ID，或回到处理任务页面重新查询。"
          extra={
            <Link to={routes.jobs}>
              <Button type="primary">返回处理任务</Button>
            </Link>
          }
        />
      );
    }
    return (
      <Alert
        type="error"
        showIcon
        message="任务读取失败"
        description={errorMessage(job.error)}
        action={<Button onClick={() => void job.refetch()}>重试</Button>}
      />
    );
  }

  if (!job.data) {
    return <Card loading title="正在读取任务…" />;
  }

  const data = job.data;
  const terminal = isTerminalStatus(data.status);
  const intervalText =
    data.status === 'waiting' ? '每 10 秒' : data.status === 'queued' || data.status === 'running' ? '每 2 秒' : '';
  const quality = data.quality_outcome
    ? qualityOutcomeLabels[data.quality_outcome]
    : undefined;

  return (
    <>
      <Space style={{ width: '100%', justifyContent: 'space-between' }} align="start" wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            提取任务详情
          </Typography.Title>
          <Space wrap>
            <StatusTag category="jobStatus" value={data.status} />
            <StatusTag category="jobStage" value={data.stage} />
            {!terminal && intervalText ? <Tag color="processing">自动读取 {intervalText}</Tag> : null}
          </Space>
        </div>
        <Space wrap>
          <Link to={routes.jobs}>
            <Button>返回任务列表</Button>
          </Link>
          <Button onClick={() => void job.refetch()} loading={job.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      {job.isError ? (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message="自动刷新失败，正在显示上次成功读取的任务"
          description={errorMessage(job.error)}
        />
      ) : null}

      <Alert
        style={{ marginTop: 12 }}
        type="info"
        showIcon
        message="提取任务只报告 L0 原始观察"
        description="治理与聚合是独立的后台任务。提取完成后，当前记忆可能仍在治理中；请到任务列表查看治理和聚合分区。"
      />

      {data.status === 'waiting' ? (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message="任务处于等待，尚未结束"
          description={
            <Space direction="vertical" size={2}>
              <span>等待原因：{reasonText(data.wait_reason) ?? '尚未提供'}</span>
              <span>诊断阶段：{data.diagnostic_stage || '未知'}</span>
              <span>下次尝试：{formatTimestamp(data.next_attempt_at)}</span>
            </Space>
          }
        />
      ) : null}

      {data.status === 'succeeded' ? (
        <Alert
          style={{ marginTop: 12 }}
          type={data.quality_outcome === 'empty' || data.quality_outcome === 'partial' ? 'warning' : 'success'}
          showIcon
          message={
            data.quality_outcome === 'empty'
              ? '提取已完成，没有形成观察'
              : data.quality_outcome === 'partial'
                ? '提取已完成，部分内容仍待处理'
                : '提取已完成，原始观察已保存'
          }
          description={quality?.description ?? '原始观察已保存，不代表治理已完成。'}
        />
      ) : null}

      {data.error ? (
        <Alert
          style={{ marginTop: 12 }}
          type="error"
          showIcon
          message="提取遇到了问题"
          description={
            <>
              <span>{reasonText(data.reason_code) ?? '暂时无法完成提取。'}</span>
              <TechnicalDetails label="查看失败详情">{data.error}</TechnicalDetails>
            </>
          }
        />
      ) : null}

      <WarningList codes={data.warnings} title="任务警告" />

      <Card title="提取结果" style={{ marginTop: 16 }}>
        <Descriptions size="small" column={1}>
          <Descriptions.Item label="来源">
            <Link to={routes.source(data.source_id)}>查看提交的原文</Link>
          </Descriptions.Item>
          <RefList title="保存的原始观察" values={data.observation_ids} to={routes.observation} />
        </Descriptions>
        <TechnicalDetails label="时间与模型用量">
          <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
            <Descriptions.Item label="任务编号"><IdText value={data.id} /></Descriptions.Item>
            <Descriptions.Item label="调度状态"><StatusTag category="jobStatus" value={data.status} /></Descriptions.Item>
            <Descriptions.Item label="生命周期阶段"><StatusTag category="jobStage" value={data.stage} /></Descriptions.Item>
            <Descriptions.Item label="诊断阶段">{data.diagnostic_stage || '无'}</Descriptions.Item>
            <Descriptions.Item label="尝试次数">{data.attempts}</Descriptions.Item>
            <Descriptions.Item label="协议失败次数">{data.protocol_failures}</Descriptions.Item>
            <Descriptions.Item label="等待原因"><StatusTag category="waitReason" value={data.wait_reason} /></Descriptions.Item>
            <Descriptions.Item label="原因码">{data.reason_code ? reasonText(data.reason_code) : '无'}</Descriptions.Item>
            <Descriptions.Item label="下次尝试">{formatTimestamp(data.next_attempt_at)}</Descriptions.Item>
            <Descriptions.Item label="队列年龄">{formatSeconds(data.queue_age_seconds)}</Descriptions.Item>
            <Descriptions.Item label="状态停滞">{formatSeconds(data.stale_seconds)}</Descriptions.Item>
            <Descriptions.Item label="模型调用">{data.model_usage.calls}</Descriptions.Item>
            <Descriptions.Item label="输入 tokens">{data.model_usage.input_tokens}</Descriptions.Item>
            <Descriptions.Item label="输出 tokens">{data.model_usage.output_tokens}</Descriptions.Item>
            <Descriptions.Item label="总 tokens">{data.model_usage.total_tokens}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{formatTimestamp(data.created_at)}</Descriptions.Item>
            <Descriptions.Item label="更新时间">{formatTimestamp(data.updated_at)}</Descriptions.Item>
            <Descriptions.Item label="开始时间">{formatTimestamp(data.started_at)}</Descriptions.Item>
            <Descriptions.Item label="结束时间">{formatTimestamp(data.finished_at)}</Descriptions.Item>
          </Descriptions>
        </TechnicalDetails>
      </Card>

      <TechnicalDetails label="提取统计与诊断">
        <Collapse
          style={{ marginTop: 16 }}
          items={[
            {
              key: 'extraction',
              label: '提取统计',
              children: (
                <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
                  {extractionLabels.map(([key, label]) => (
                    <Descriptions.Item key={key} label={label}>
                      {data.extraction_stats[key]}
                    </Descriptions.Item>
                  ))}
                </Descriptions>
              ),
            },
            {
              key: 'timings',
              label: '耗时',
              children: (
                <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
                  {timingLabels.map(([key, label]) => (
                    <Descriptions.Item key={key} label={label}>
                      {formatMilliseconds(data.timings[key])}
                    </Descriptions.Item>
                  ))}
                </Descriptions>
              ),
            },
            {
              key: 'diagnostics',
              label: `提取诊断（${data.diagnostics.length}）`,
              children: (
                <Table
                  rowKey={(_, index) => String(index)}
                  size="small"
                  pagination={false}
                  dataSource={data.diagnostics}
                  columns={[
                    { title: '字段', dataIndex: 'field', render: (value: string) => value || '—' },
                    { title: '代码', dataIndex: 'code' },
                    { title: '阶段', dataIndex: 'stage' },
                    { title: '处置', dataIndex: 'disposition' },
                  ]}
                  locale={{ emptyText: '无提取诊断' }}
                />
              ),
            },
            {
              key: 'raw',
              label: '原始诊断结构（只读 JSON）',
              children: (
                <JsonView
                  value={{
                    extraction_stats: data.extraction_stats,
                    timings: data.timings,
                    model_usage: data.model_usage,
                    diagnostics: data.diagnostics,
                  }}
                />
              ),
            },
          ]}
        />
      </TechnicalDetails>

      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        提交于 {formatTimestamp(data.created_at)}。等待中的提取会按后台规则自动继续。
      </Typography.Paragraph>
    </>
  );
}
