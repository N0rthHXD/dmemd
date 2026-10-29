import { Alert, Card, Descriptions, Empty, Result, Space, Tag, Typography } from 'antd';
import { Link, useParams } from 'react-router-dom';
import { errorMessage, isApiError } from '../../shared/api/errors';
import type { Evidence } from '../../shared/api/types';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { EvidenceTable } from '../memories/components/EvidenceTable';
import { RelationTable } from '../../shared/ui/RelationTable';
import { StatusTag } from '../../shared/ui/StatusTag';
import { useObservation, useObservationEvidence, useObservationRelations } from './queries';

function EvidenceList({ items }: { items: Evidence[] }) {
  if (items.length === 0) {
    return <Empty description="没有代码生成的证据" image={Empty.PRESENTED_IMAGE_SIMPLE} />;
  }
  return <EvidenceTable items={items} />;
}

export default function ObservationDetailPage() {
  const { id = '' } = useParams();
  const observation = useObservation(id);
  const evidence = useObservationEvidence(id);
  const relations = useObservationRelations(id);

  if (observation.isError && !observation.data) {
    if (isApiError(observation.error) && observation.error.status === 404) {
      return (
        <Result
          status="404"
          title="观察记录不存在"
          subTitle="它可能尚未生成，或已经被撤回/删除。"
          extra={
            <Link to={routes.observations}>
              <Tag>返回观察列表</Tag>
            </Link>
          }
        />
      );
    }
    return (
      <Alert
        type="error"
        showIcon
        message="观察读取失败"
        description={errorMessage(observation.error)}
      />
    );
  }

  if (!observation.data) {
    return <Card loading title="正在读取观察…" />;
  }

  const item = observation.data;
  return (
    <>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            L0 原始观察
          </Typography.Title>
          <Space wrap>
            <StatusTag category="memoryKind" value={item.kind} />
            <StatusTag category="observationContentMode" value={item.content_mode} />
            <StatusTag category="observationStatus" value={item.status} />
          </Space>
        </div>
        <Link to={routes.observations}>返回观察列表</Link>
      </Space>

      <Card title="观察内容" style={{ marginBottom: 16 }}>
        <Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>{item.content}</Typography.Paragraph>
        <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
          <Descriptions.Item label="来源">
            <Link to={routes.source(item.source_id)}>查看来源原文与 span</Link>
          </Descriptions.Item>
          <Descriptions.Item label="原始角色">{item.origin_role || '未知'}</Descriptions.Item>
          <Descriptions.Item label="所属范围">{item.scope_key || '未指定'}</Descriptions.Item>
          <Descriptions.Item label="提取协议版本">{item.extraction_version || '未知'}</Descriptions.Item>
          <Descriptions.Item label="创建时间">
            <span title={timestampTitle(item.created_at)}>{formatTimestamp(item.created_at)}</span>
          </Descriptions.Item>
          <Descriptions.Item label="更新时间">
            <span title={timestampTitle(item.updated_at)}>{formatTimestamp(item.updated_at)}</span>
          </Descriptions.Item>
        </Descriptions>
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message="观察内容不可编辑"
          description="L0 原始观察由提取阶段的模型选择与代码绑定生成。需要改变内容时，应提交新的来源并由后续治理处理。"
        />
      </Card>

      <Card title={`代码生成的证据（${evidence.data?.length ?? 0}）`} style={{ marginBottom: 16 }}>
        {evidence.isError ? (
          <Alert type="error" showIcon message={errorMessage(evidence.error)} />
        ) : (
          <EvidenceList items={evidence.data ?? []} />
        )}
      </Card>

      <Card title={`关系（${relations.data?.length ?? 0}）`}>
        {relations.isError ? (
          <Alert type="error" showIcon message={errorMessage(relations.error)} />
        ) : (
          <RelationTable items={relations.data ?? []} />
        )}
      </Card>
    </>
  );
}
