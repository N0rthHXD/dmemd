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
import {
  jobStageLabels,
  qualityOutcomeLabels,
  reasonText,
} from '../../shared/lib/codes';
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
import { isTerminalStatus } from './api';
import { useJob } from './queries';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { shortId } from '../../shared/lib/display';

const extractionLabels: Array<[keyof Job['extraction_stats'], string]> = [
  ['raw_candidates', '原始候选'],
  ['parsed_candidates', '解析候选'],
  ['normalized_candidates', '规范化候选'],
  ['normalizations', '规范化次数'],
  ['repair_attempts', '修复尝试'],
  ['repaired_candidates', '修复候选'],
  ['repair_succeeded', '修复成功'],
  ['repair_failed', '修复失败'],
  ['repair_timed_out', '修复超时'],
  ['repair_invalid_output', '修复输出无效'],
  ['excluded_candidates', '明确排除'],
  ['isolated_candidates', '隔离候选'],
  ['accepted_candidates', '接受候选'],
  ['skipped_candidates', '跳过候选'],
  ['memory_writes', '记忆写入'],
  ['extraction_chunks', '提取分块'],
  ['extraction_chunk_failures', '分块失败'],
  ['dropped_candidates', '丢弃候选'],
  ['cross_chunk_duplicates', '跨块重复'],
  ['context_only_candidates', '仅上下文候选'],
];

const timingLabels: Array<[keyof Job['timings'], string]> = [
  ['queue_age_ms', '队列等待'],
  ['extraction_ms', '提取'],
  ['normalization_ms', '规范化'],
  ['repair_ms', '修复'],
  ['context_ms', '上下文'],
  ['decision_ms', '决策'],
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
              查看相关内容 · {shortId(value)}
            </Link>
          ))}
        </Space>
      ) : (
        '无'
      )}
    </Descriptions.Item>
  );
}

function retentionRows(values: Record<string, number> | undefined) {
  return Object.entries(values ?? {}).map(([reason, count]) => ({
    key: reason,
    reason,
    label: reasonText(reason) ?? reason,
    count,
  }));
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
      message.info('处理已结束，相关内容已更新');
    }
  }, [job.data?.status, queryClient, message]);

  if (job.isError && !job.data) {
    if (isApiError(job.error) && job.error.status === 404) {
      return (
        <Result
          status="404"
          title="任务不存在"
          subTitle="没有全局任务列表；请核对任务 ID，或回到本会话任务与手动查询入口。"
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
            处理详情
          </Typography.Title>
          <Space wrap>
            <StatusTag category="jobStatus" value={data.status} />
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

      {data.status === 'waiting' ? (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message="任务处于等待，尚未结束"
          description={
            <Space direction="vertical" size={2}>
              <span>等待原因：{reasonText(data.wait_reason) ?? '尚未提供'}</span>
              <span>阶段：{data.stage ? jobStageLabels[data.stage]?.label ?? data.stage : '未知'}</span>
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
              ? '处理已完成，没有提取到需要保存的内容'
              : data.quality_outcome === 'partial'
                ? '处理已完成，部分内容需要进一步核对'
                : '处理已完成'
          }
          description={
            quality?.description ??
            '本次处理已经结束，不代表所有内容都已保存，也不代表内容真实性经过验证。'
          }
        />
      ) : null}

      {data.error ? (
        <Alert
          style={{ marginTop: 12 }}
          type="error"
          showIcon
          message="处理遇到了问题"
          description={<><span>{reasonText(data.reason_code) ?? '暂时无法完成处理，请查看详细原因。'}</span><TechnicalDetails label="查看失败详情">{data.error}</TechnicalDetails></>}
        />
      ) : null}

      <WarningList codes={data.warnings} title="任务警告" />

      <Card title="处理结果" style={{ marginTop: 16 }}>
        <Descriptions size="small" column={1}>
          <Descriptions.Item label="提交时间">{formatTimestamp(data.created_at)}</Descriptions.Item>
          <Descriptions.Item label="来源"><Link to={routes.source(data.source_id)}>查看提交的原文</Link></Descriptions.Item>
          <RefList title="保存或更新的记忆" values={data.memory_ids} to={routes.memory} />
          <RefList title="需要核对的事项" values={data.conflict_ids} to={routes.conflict} />
          <RefList title="待确认的记忆" values={data.pending_memory_ids} to={routes.memory} />
        </Descriptions>
        <TechnicalDetails label="处理时间与诊断详情">
        <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
          <Descriptions.Item label="处理编号"><IdText value={data.id} /></Descriptions.Item>
          <Descriptions.Item label="状态">
            <StatusTag category="jobStatus" value={data.status} />
          </Descriptions.Item>
          <Descriptions.Item label="尝试次数">{data.attempts}</Descriptions.Item>
          <Descriptions.Item label="协议失败次数">{data.protocol_failures}</Descriptions.Item>
          <Descriptions.Item label="等待原因">
            <StatusTag category="waitReason" value={data.wait_reason} />
          </Descriptions.Item>
          <Descriptions.Item label="阶段">
            {data.stage ? jobStageLabels[data.stage]?.label ?? data.stage : '无'}
          </Descriptions.Item>
          <Descriptions.Item label="原因码">
            {data.reason_code ? (
              <Space direction="vertical" size={0}>
                <span>{reasonText(data.reason_code)}</span>
                <Typography.Text code>{data.reason_code}</Typography.Text>
              </Space>
            ) : (
              '无'
            )}
          </Descriptions.Item>
          <Descriptions.Item label="下次尝试">
            {formatTimestamp(data.next_attempt_at)}
          </Descriptions.Item>
          <Descriptions.Item label="重试原因">{data.retry_reason || '无'}</Descriptions.Item>
          <Descriptions.Item label="业务结果">
            {data.quality_outcome ? (
              <span>
                <StatusTag category="qualityOutcome" value={data.quality_outcome} />
                <Typography.Text type="secondary"> {quality?.description}</Typography.Text>
              </span>
            ) : (
              '未返回'
            )}
          </Descriptions.Item>
          <Descriptions.Item label="队列年龄">{formatSeconds(data.queue_age_seconds)}</Descriptions.Item>
          <Descriptions.Item label="状态停滞">{formatSeconds(data.stale_seconds)}</Descriptions.Item>
          <Descriptions.Item label="来源">
            <Link to={routes.source(data.source_id)}>{data.source_id}</Link>
          </Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatTimestamp(data.created_at)}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatTimestamp(data.updated_at)}</Descriptions.Item>
          <Descriptions.Item label="开始时间">{formatTimestamp(data.started_at)}</Descriptions.Item>
          <Descriptions.Item label="结束时间">{formatTimestamp(data.finished_at)}</Descriptions.Item>
          <RefList title="写入 / 修改的记忆" values={data.memory_ids} to={routes.memory} />
          <RefList title="关联冲突" values={data.conflict_ids} to={routes.conflict} />
          <Descriptions.Item label="待裁决记忆">
            {data.pending_memory_ids.length > 0 ? (
              <Space direction="vertical" size={2}>
                {data.pending_memory_ids.map((memoryId) => (
                  <Link key={memoryId} to={routes.memory(memoryId)}>
                    {memoryId}
                  </Link>
                ))}
              </Space>
            ) : (
              '无'
            )}
          </Descriptions.Item>
        </Descriptions>
        </TechnicalDetails>
      </Card>

      <TechnicalDetails label="技术详情与处理统计"><Collapse
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
            key: 'retention',
            label: '保留统计',
            children: (
              <Space direction="vertical" size={12} style={{ width: '100%' }}>
                <Descriptions size="small" bordered column={2}>
                  <Descriptions.Item label="保留候选">{data.retention.retained}</Descriptions.Item>
                  <Descriptions.Item label="未保留候选">{data.retention.not_retained}</Descriptions.Item>
                </Descriptions>
                <div>
                  <Typography.Title level={5}>按原因保留</Typography.Title>
                  <Table
                    rowKey="key"
                    size="small"
                    pagination={false}
                    dataSource={retentionRows(data.retention.by_reason)}
                    columns={[
                      { title: '原因', dataIndex: 'label' },
                      { title: '代码', dataIndex: 'reason' },
                      { title: '数量', dataIndex: 'count', width: 100 },
                    ]}
                  />
                </div>
                <div>
                  <Typography.Title level={5}>未保留原因</Typography.Title>
                  <Table
                    rowKey="key"
                    size="small"
                    pagination={false}
                    dataSource={retentionRows(data.retention.not_retained_by_reason)}
                    columns={[
                      { title: '原因', dataIndex: 'label' },
                      { title: '代码', dataIndex: 'reason' },
                      { title: '数量', dataIndex: 'count', width: 100 },
                    ]}
                  />
                </div>
                <Typography.Text type="secondary">
                  保留/未保留是服务派生的诊断，不触发自动删除或 TTL。
                </Typography.Text>
              </Space>
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
            key: 'model',
            label: '模型用量与效率',
            children: (
              <Space direction="vertical" size={12} style={{ width: '100%' }}>
                <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
                  <Descriptions.Item label="调用次数">{data.model_usage.calls}</Descriptions.Item>
                  <Descriptions.Item label="输入 tokens">{data.model_usage.input_tokens}</Descriptions.Item>
                  <Descriptions.Item label="输出 tokens">{data.model_usage.output_tokens}</Descriptions.Item>
                  <Descriptions.Item label="总 tokens">{data.model_usage.total_tokens}</Descriptions.Item>
                </Descriptions>
                <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
                  <Descriptions.Item label="记忆数">{data.efficiency.memories}</Descriptions.Item>
                  <Descriptions.Item label="模型调用">{data.efficiency.model_calls}</Descriptions.Item>
                  <Descriptions.Item label="总 tokens">{data.efficiency.total_tokens}</Descriptions.Item>
                  <Descriptions.Item label="每次记忆调用">
                    {data.efficiency.calls_per_memory.toFixed(3)}
                  </Descriptions.Item>
                  <Descriptions.Item label="每次记忆 tokens">
                    {data.efficiency.tokens_per_memory.toFixed(1)}
                  </Descriptions.Item>
                </Descriptions>
                <Typography.Text type="secondary">
                  没有价格依据，不把 token 数换算为金额。
                </Typography.Text>
              </Space>
            ),
          },
          {
            key: 'diagnostics',
            label: `候选诊断（${data.diagnostics.length}）`,
            children: (
              <Table
                rowKey={(_, index) => String(index)}
                size="small"
                pagination={false}
                dataSource={data.diagnostics}
                columns={[
                  { title: '候选', dataIndex: 'candidate_id', render: (value: string) => value || '整批' },
                  { title: '字段', dataIndex: 'field', render: (value: string) => value || '—' },
                  { title: '代码', dataIndex: 'code' },
                  { title: '阶段', dataIndex: 'stage' },
                  { title: '处置', dataIndex: 'disposition' },
                ]}
                locale={{ emptyText: '无候选诊断' }}
              />
            ),
          },
          {
            key: 'convergence',
            label: `收敛诊断（${data.convergence.length}）`,
            children: (
              <Table
                rowKey={(_, index) => String(index)}
                size="small"
                pagination={false}
                dataSource={data.convergence}
                columns={[
                  { title: '候选', dataIndex: 'candidate_id', render: (value: string) => value || '—' },
                  { title: '事实组', dataIndex: 'group_id', render: (value: string) => value || '—' },
                  { title: '代码', dataIndex: 'code' },
                  {
                    title: '说明',
                    key: 'description',
                    render: (_: unknown, row) => reasonText(row.code) ?? row.code,
                  },
                ]}
                locale={{ emptyText: '无收敛诊断' }}
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
                  retention: data.retention,
                  timings: data.timings,
                  model_usage: data.model_usage,
                  efficiency: data.efficiency,
                  diagnostics: data.diagnostics,
                  convergence: data.convergence,
                }}
              />
            ),
          },
        ]}
      /></TechnicalDetails>

      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        提交于 {formatTimestamp(data.created_at)}。等待中的处理会按后台规则自动继续，无需重复提交。
      </Typography.Paragraph>
    </>
  );
}
