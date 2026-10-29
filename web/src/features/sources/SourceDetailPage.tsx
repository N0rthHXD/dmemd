import {
  Alert,
  App,
  Button,
  Card,
  Descriptions,
  Result,
  Space,
  Tag,
  Typography,
} from 'antd';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  clearAfterPermanentRemoval,
  clearAfterSourceRedaction,
} from '../../shared/api/cache-effects';
import { errorMessage, isApiError } from '../../shared/api/errors';
import type { ForgetResponse, RedactResponse } from '../../shared/api/types';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { DangerActionDialog } from '../../shared/ui/DangerActionDialog';
import { IdText } from '../../shared/ui/IdText';
import { SourceContent } from '../../shared/ui/SourceContent';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { RemovalSummary } from '../../shared/ui/RemovalSummary';
import { forgetSource, redactSource } from './api';
import { useSource } from './queries';
import { useObservations } from '../observations/queries';

export default function SourceDetailPage() {
  const { id = '' } = useParams();
  const source = useSource(id);
  const observations = useObservations({ sourceId: id, scopeKey: '', limit: 50 });
  const queryClient = useQueryClient();
  const { message, modal } = App.useApp();
  const [redactResult, setRedactResult] = useState<RedactResponse | null>(null);
  const [forgetOpen, setForgetOpen] = useState(false);
  const [forgetResult, setForgetResult] = useState<ForgetResponse | null>(null);

  const redactMutation = useMutation({
    mutationFn: () => redactSource(id),
    onSuccess: async (data) => {
      setRedactResult(data);
      await clearAfterSourceRedaction(queryClient, id);
      message.success('来源原文已清除；已形成的记忆正文保留');
    },
  });

  const forgetMutation = useMutation({
    mutationFn: () => forgetSource(id),
    onSuccess: async (data) => {
      setForgetResult(data);
      await clearAfterPermanentRemoval(queryClient, {
        sourceId: id,
        sourceIds: data.source_ids,
        experienceIds: data.experience_ids,
      });
    },
  });

  const confirmRedact = () => {
    modal.confirm({
      title: '清除来源原文？',
      content:
        '会清除原文和引用片段，取消尚未完成的处理，但已经保存的记忆仍然保留。原文清除后无法找回，这不等于永久遗忘相关记忆。',
      okText: '清除原文',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await redactMutation.mutateAsync();
        } catch (error) {
          message.error(errorMessage(error));
          throw error;
        }
      },
    });
  };

  if (forgetResult) {
    return (
      <Result
        status="success"
        title="来源永久遗忘已由服务端确认"
        subTitle="页面业务缓存已保守清除；以下以服务端实际返回为准。"
        extra={
          <Space>
            <Link to={routes.memories}>
              <Button>打开记忆库</Button>
            </Link>
            <Link to={routes.artifacts}>
              <Button>查看自动整理</Button>
            </Link>
          </Space>
        }
      >
        <RemovalSummary result={forgetResult} />
        <TechnicalDetails label="查看来源编号"><Descriptions bordered size="small" column={1}>
          <Descriptions.Item label="请求来源 ID">
            <IdText value={id} />
          </Descriptions.Item>
          <Descriptions.Item label="实际删除记忆 ID">
            {forgetResult.memory_ids.length ? forgetResult.memory_ids.join('、') : '无'}
          </Descriptions.Item>
          <Descriptions.Item label="实际删除来源 ID">
            {forgetResult.source_ids.length ? forgetResult.source_ids.join('、') : '无'}
          </Descriptions.Item>
          <Descriptions.Item label="实际删除报告 ID">
            {forgetResult.experience_ids.length ? forgetResult.experience_ids.join('、') : '无'}
          </Descriptions.Item>
        </Descriptions></TechnicalDetails>
      </Result>
    );
  }

  if (source.isError && !source.data) {
    if (isApiError(source.error) && source.error.status === 404) {
      return (
        <Result
          status="404"
          title="来源不存在或已被永久清理"
          subTitle="来源可能已被清除或永久遗忘；不会从缓存复原原文片段。"
          extra={
            <Link to={routes.memories}>
              <Button type="primary">返回记忆库</Button>
            </Link>
          }
        />
      );
    }
    return (
      <Alert
        type="error"
        showIcon
        message="来源读取失败"
        description={errorMessage(source.error)}
        action={<Button onClick={() => void source.refetch()}>重试</Button>}
      />
    );
  }

  if (!source.data) {
    return <Card loading title="正在读取来源…" />;
  }

  const data = source.data;
  const isRedacted = data.redacted || Boolean(redactResult);

  return (
    <>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            来源原文
          </Typography.Title>
          <Space wrap>
            {isRedacted ? <Tag color="default">原文已清除</Tag> : <Tag color="success">原文可用</Tag>}
          </Space>
        </div>
        <Space wrap>
          <Link to={routes.memories}>
            <Button>返回记忆库</Button>
          </Link>
          <Button onClick={() => void source.refetch()} loading={source.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      {source.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message="刷新失败，显示上次成功读取的来源信息"
          description={errorMessage(source.error)}
        />
      ) : null}

      <Card title="来源信息" style={{ marginBottom: 16 }}>
        <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
          <Descriptions.Item label="来源应用">{data.app_id || '未提供'}</Descriptions.Item>
          <Descriptions.Item label="所属范围">{data.scope_key || '未指定'}</Descriptions.Item>
          <Descriptions.Item label="创建时间">
            <span title={timestampTitle(data.created_at)}>{formatTimestamp(data.created_at)}</span>
          </Descriptions.Item>
          <Descriptions.Item label="清除时间">
            {isRedacted ? data.redacted_at ? formatTimestamp(data.redacted_at) : '原文已清除，时间以服务记录为准' : '未清除'}
          </Descriptions.Item>
        </Descriptions>
        <TechnicalDetails label="来源编号"><IdText value={data.id} /><Typography.Paragraph>外部记录编号：{data.external_id || '未提供'}</Typography.Paragraph></TechnicalDetails>
      </Card>

      <Card title={`由该来源形成的 L0 观察（${observations.data?.length ?? 0}）`} style={{ marginBottom: 16 }}>
        {observations.isError ? (
          <Alert type="error" showIcon message={errorMessage(observations.error)} />
        ) : observations.data && observations.data.length > 0 ? (
          <Space direction="vertical" size={8} style={{ width: '100%' }}>
            {observations.data.map((item) => (
              <Card key={item.id} size="small" type="inner">
                <Space direction="vertical" size={4} style={{ width: '100%' }}>
                  <Space wrap>
                    <Link to={routes.observation(item.id)}>
                      <Typography.Text>{item.content}</Typography.Text>
                    </Link>
                    <StatusTag category="memoryKind" value={item.kind} />
                    <StatusTag category="observationStatus" value={item.status} />
                  </Space>
                  <Typography.Text type="secondary">
                    {item.content_mode} · {item.scope_key || '范围未知'}
                  </Typography.Text>
                </Space>
              </Card>
            ))}
          </Space>
        ) : (
          <Typography.Text type="secondary">
            还没有形成可展示的观察；提取任务可能尚未完成。
          </Typography.Text>
        )}
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0, marginTop: 12 }}>
          清除来源原文会撤回或删除这些观察，并使依赖它们的当前记忆和派生内容失效；已经形成的记忆正文不会因为清除原文而被自动重写。
        </Typography.Paragraph>
      </Card>

      <Card title={`稳定 Span（${data.segments.length}）`} style={{ marginBottom: 16 }}>
        {data.segments.length > 0 ? (
          <Descriptions size="small" bordered column={1}>
            {data.segments.map((segment) => (
              <Descriptions.Item
                key={segment.segment_id}
                label={<IdText value={segment.segment_id} maxWidth={160} />}
              >
                <Space direction="vertical" size={2} style={{ width: '100%' }}>
                  <Typography.Text style={{ whiteSpace: 'pre-wrap' }}>{segment.text}</Typography.Text>
                  <Typography.Text type="secondary">
                    {segment.role || '角色未知'} · {segment.field || '字段未知'} · [{segment.start}, {segment.end})
                    {segment.message_id ? ` · message ${segment.message_id}` : ''}
                  </Typography.Text>
                </Space>
              </Descriptions.Item>
            ))}
          </Descriptions>
        ) : (
          <Alert
            type="info"
            showIcon
            message={isRedacted ? '原文已清除，无法重建 span' : '没有可展示的 span'}
            description="span 由来源内容和遍历顺序确定性生成；模型只能选择 span ID，不能提供原文或偏移。"
          />
        )}
      </Card>

      <Card title="原始内容" style={{ marginBottom: 16 }}>
        {isRedacted ? (
          <Alert
            type="info"
            showIcon
            message="原文已清除"
            description="已不再保留原文片段，但已经保存的记忆仍然存在。"
          />
        ) : data.content === undefined ? (
          <Alert
            type="warning"
            showIcon
            message="暂时没有读取到原文"
            description="这不表示原文为空，可以刷新页面或查看服务状态。"
          />
        ) : (
          <SourceContent value={data.content} />
        )}
      </Card>

      {redactResult ? (
        <Card title="清除原文结果" style={{ marginBottom: 16 }}>
          <Descriptions size="small" bordered column={1}>
            <Descriptions.Item label="来源 ID">
              <IdText value={redactResult.id} />
            </Descriptions.Item>
            <Descriptions.Item label="已清除">{String(redactResult.redacted)}</Descriptions.Item>
            <Descriptions.Item label="取消的未完成任务">
              {redactResult.cancelled_jobs}
            </Descriptions.Item>
            <Descriptions.Item label="撤回的观察数">
              {redactResult.retracted_observations}
            </Descriptions.Item>
            <Descriptions.Item label="失效的派生内容">
              {redactResult.invalidated_derived_ids.length > 0
                ? redactResult.invalidated_derived_ids.join('、')
                : '无'}
            </Descriptions.Item>
          </Descriptions>
          <Alert
            style={{ marginTop: 12 }}
            type="info"
            showIcon
            message="这不是永久遗忘"
            description="来源记录和已经形成的记忆仍然保留。如需一并清除，请查看下方的永久遗忘操作。"
          />
        </Card>
      ) : null}

      {redactMutation.isError ? (
        <Alert
          style={{ marginBottom: 16 }}
          type="error"
          showIcon
          message="清除原文失败，服务端未确认成功"
          description={errorMessage(redactMutation.error)}
        />
      ) : null}

      <TechnicalDetails label="删除与永久遗忘（请谨慎操作）"><Card title="删除来源资料" style={{ marginBottom: 16 }}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            message="清除来源原文 ≠ 永久遗忘"
            description="清除原文会保留已形成的记忆；永久遗忘还会删除相关记忆、报告和自动整理内容，且无法恢复。"
          />
          <Space wrap>
            <Button danger disabled={isRedacted} onClick={confirmRedact}>
              清除来源原文
            </Button>
            <Button danger type="dashed" onClick={() => setForgetOpen(true)}>
              永久遗忘来源及相关内容
            </Button>
          </Space>
          <Typography.Text type="secondary">
            无法提前精确预览全部影响，完成后会显示实际删除结果。如操作超时，请先确认结果，不要重复提交。
          </Typography.Text>
        </Space>
      </Card></TechnicalDetails>

      <DangerActionDialog
        open={forgetOpen}
        title="永久遗忘这个来源？"
        targetType="来源及相关内容"
        targetId={id}
        targetName={`${data.app_id || '来源原文'} · ${formatTimestamp(data.created_at)}`}
        confirmText={id}
        confirmLabel="永久遗忘"
        impacts="会删除该来源及关联记忆、应用报告、核对记录和自动整理内容，可能超出当前页面可见范围。此操作不可恢复。"
        onClose={() => setForgetOpen(false)}
        action={() => forgetMutation.mutateAsync()}
        onSuccess={(result) => {
          setForgetResult(result);
          setForgetOpen(false);
        }}
      />
    </>
  );
}
