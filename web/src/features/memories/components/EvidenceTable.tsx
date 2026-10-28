import { Alert, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { Link } from 'react-router-dom';
import type { Evidence } from '../../../shared/api/types';
import { routes } from '../../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../../shared/lib/time';
import { IdText } from '../../../shared/ui/IdText';
import { TechnicalDetails } from '../../../shared/ui/TechnicalDetails';

const columns: ColumnsType<Evidence> = [
  {
    title: '来源',
    dataIndex: 'id',
    width: 260,
    render: (_, item) => (
      <div>
        <div>
          <Link to={routes.source(item.source_id)}>查看来源原文</Link>
        </div>
      </div>
    ),
  },
  {
    title: '片段',
    dataIndex: 'quote',
    render: (_, item) =>
      item.redacted ? (
        <Tag color="default">原文已清除，不展示旧片段</Tag>
      ) : item.quote ? (
        <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', margin: 0 }}>
          {item.quote}
        </Typography.Paragraph>
      ) : (
        <Typography.Text type="secondary">无片段</Typography.Text>
      ),
  },
  {
    title: '位置',
    dataIndex: 'location',
    width: 220,
    render: (_, item) =>
      item.redacted || !item.location ? (
        <Typography.Text type="secondary">不可用</Typography.Text>
      ) : (
        <TechnicalDetails label="片段位置"><Typography.Text code>
          {item.location.field || '字段未知'} [{item.location.start},{item.location.end})
        </Typography.Text></TechnicalDetails>
      ),
  },
  {
    title: '提取信息',
    dataIndex: 'model_name',
    width: 150,
    render: (_, item) => <TechnicalDetails label="技术详情"><IdText value={item.id} /><div>{item.model_name || '未提供模型信息'}</div></TechnicalDetails>,
  },
  {
    title: '创建 / 清除',
    dataIndex: 'created_at',
    width: 190,
    render: (_, item) => (
      <div>
        <div title={timestampTitle(item.created_at)}>{formatTimestamp(item.created_at)}</div>
        {item.redacted ? (
          <Tag color="default">已于 {formatTimestamp(item.redacted_at)} 清除</Tag>
        ) : null}
      </div>
    ),
  },
];

export function EvidenceTable({ items }: { items: Evidence[] }) {
  return (
    <>
      {items.some((item) => item.redacted) ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="info"
          showIcon
          message="部分证据原文已清除"
          description="清除后不再展示原文片段和位置；这不代表来源从未存在。"
        />
      ) : null}
      <Table
        rowKey="id"
        size="small"
        columns={columns}
        dataSource={items}
        pagination={false}
        scroll={{ x: 900 }}
        locale={{ emptyText: '证据集为空' }}
      />
    </>
  );
}
