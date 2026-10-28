import { Alert, Button, Card, Col, Descriptions, Progress, Row, Space, Tag, Typography } from 'antd';
import { useMutation } from '@tanstack/react-query';
import { errorMessage } from '../../shared/api/errors';
import { formatMilliseconds, formatTimestamp } from '../../shared/lib/time';
import { reasonText } from '../../shared/lib/codes';
import { JsonView } from '../../shared/ui/JsonView';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { probeEmbedding } from './api';
import { useEmbeddingStatus, useSystemHealth, useSystemReadiness } from './queries';

export default function SystemPage() {
  const health = useSystemHealth(true);
  const readiness = useSystemReadiness(true);
  const embedding = useEmbeddingStatus();
  const probe = useMutation({ mutationFn: probeEmbedding });
  const data = embedding.data;
  return (
    <>
      <div className="page-intro"><Typography.Title level={3}>服务状态</Typography.Title>
        <Typography.Paragraph type="secondary">了解本地服务和搜索功能的运行情况。这里只查看状态，不修改配置。</Typography.Paragraph>
        <Button onClick={() => { void health.refetch(); void readiness.refetch(); void embedding.refetch(); }} loading={health.isFetching || readiness.isFetching || embedding.isFetching}>刷新状态</Button>
      </div>
      <Row gutter={[20, 20]}>
        <Col xs={24} lg={8}><Card title="基础服务">
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            <div><Typography.Paragraph>本地连接</Typography.Paragraph><Tag color={health.isError ? 'error' : health.data?.status === 'ok' ? 'success' : 'default'}>{health.isError ? '连接失败' : health.data?.status === 'ok' ? '可以连接' : '正在检查'}</Tag></div>
            <div><Typography.Paragraph>记忆数据</Typography.Paragraph><Tag color={readiness.isError || readiness.data?.status === 'unavailable' ? 'error' : readiness.data ? 'success' : 'default'}>{readiness.isError ? '检查失败' : readiness.data?.status === 'ready' ? '可以读取' : readiness.data ? '暂不可用' : '正在检查'}</Tag></div>
            <Typography.Text type="secondary">基础服务正常并不代表 AI 模型或语义搜索可用。</Typography.Text>
            {health.isError || readiness.isError ? <Alert type="error" showIcon message="请确认本地服务正在运行" description={errorMessage(health.error || readiness.error)} /> : null}
            <TechnicalDetails><Descriptions size="small" column={1}><Descriptions.Item label="连接检查">{health.data?.status ?? '未知'}</Descriptions.Item><Descriptions.Item label="数据检查">{readiness.data?.status ?? '未知'}</Descriptions.Item><Descriptions.Item label="响应状态">{readiness.data?.httpStatus ?? '未知'}</Descriptions.Item></Descriptions></TechnicalDetails>
          </Space>
        </Card></Col>
        <Col xs={24} lg={8}><Card title="AI 内容处理">
          <Space direction="vertical" size={16}>
            <Tag>暂无法确认模型连接</Tag>
            <Typography.Paragraph style={{ margin: 0 }}>添加的内容会在后台提取、核对和整理。具体进展请以每次提交后的处理记录为准。</Typography.Paragraph>
            <Typography.Text type="secondary">目前没有独立的模型连接检查，不能仅凭服务可连接就判断模型可用。</Typography.Text>
          </Space>
        </Card></Col>
        <Col xs={24} lg={8}><Card title="语义搜索" extra={<Button size="small" onClick={() => probe.mutate()} loading={probe.isPending}>检查连接</Button>}>
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {embedding.isError ? <Alert type={data ? 'warning' : 'error'} showIcon message={data ? '暂时无法刷新，显示上次状态' : '暂时无法读取状态'} description={errorMessage(embedding.error)} /> : null}
            {data ? <>
              <StatusTag category="embeddingReason" value={data.reason_code} />
              <Typography.Paragraph style={{ margin: 0 }}>根据意思查找记忆，而不只是匹配关键词。{!data.enabled ? '尚未启用，不影响关键词搜索。' : !data.ready ? '部分内容尚未准备完成，搜索时可能使用关键词结果。' : '当前搜索内容已准备好；实际连接情况可通过检查连接确认。'}</Typography.Paragraph>
              {data.enabled && data.documents > 0 ? <div><Typography.Text type="secondary">内容准备进度</Typography.Text><Progress percent={Math.round(data.coverage * 100)} size="small" /><Typography.Text type="secondary">{data.current_vectors} / {data.documents} 条已准备</Typography.Text></div> : null}
              {data.enabled && data.documents === 0 ? <Typography.Text type="secondary">还没有可准备的内容，添加记忆后会自动处理。</Typography.Text> : null}
              {data.pending_jobs || data.failed_jobs ? <Typography.Text type="secondary">等待处理 {data.pending_jobs} 项 · 处理失败 {data.failed_jobs} 项</Typography.Text> : null}
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>更新于 {formatTimestamp(data.checked_at)}</Typography.Text>
            </> : !embedding.isError ? <Typography.Text type="secondary">正在检查…</Typography.Text> : null}
            {probe.isError ? <Alert type="error" showIcon message="连接检查未完成" description={errorMessage(probe.error)} /> : probe.data ? <Alert type={probe.data.success ? 'success' : probe.data.attempted ? 'warning' : 'info'} showIcon message={probe.data.success ? '连接检查通过' : probe.data.attempted ? '连接检查未通过' : '尚未执行连接检查'} description={<Space direction="vertical"><StatusTag category="probeCode" value={probe.data.code} /><span>检查于 {formatTimestamp(probe.data.checked_at)} · 用时 {formatMilliseconds(probe.data.duration_ms)}</span></Space>} /> : null}
          </Space>
        </Card></Col>
      </Row>
      <TechnicalDetails label="技术详情与原始诊断">
        {data ? <>
          <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
            <Descriptions.Item label="模型">{data.model || '未配置'}</Descriptions.Item><Descriptions.Item label="服务地址">{data.endpoint || '未配置'}</Descriptions.Item>
            <Descriptions.Item label="需要重建">{data.rebuild_required ? '是' : '否'}</Descriptions.Item><Descriptions.Item label="重建原因">{data.rebuild_reason ? reasonText(data.rebuild_reason) : '无'}</Descriptions.Item>
          </Descriptions>
          <JsonView value={data} maxHeight={520} />
        </> : <Typography.Text type="secondary">读取服务状态后显示详细诊断。</Typography.Text>}
        {probe.data ? <JsonView value={probe.data} /> : null}
      </TechnicalDetails>
    </>
  );
}
