import { BookOutlined, PlusOutlined, SafetyCertificateOutlined, SearchOutlined } from '@ant-design/icons';
import { Alert, Button, Card, Col, Empty, Input, Row, Skeleton, Space, Tag, Typography } from 'antd';
import { Link, useNavigate } from 'react-router-dom';
import { errorMessage } from '../../shared/api/errors';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp } from '../../shared/lib/time';
import { StatusTag } from '../../shared/ui/StatusTag';
import { useRecentMemories } from './queries';

export default function OverviewPage() {
  const recent = useRecentMemories();
  const navigate = useNavigate();
  const items = recent.data ?? [];
  const attention = items.filter((item) => item.assertion_status === 'pending');
  return (
    <>
      <section className="overview-hero">
        <Typography.Text type="secondary">你的本地记忆工作台</Typography.Text>
        <Typography.Title level={2} style={{ margin: '8px 0' }}>把值得记住的事，留在这里</Typography.Title>
        <Typography.Paragraph type="secondary">回顾最近的记忆、查找需要的信息，也可以添加新的想法与计划。</Typography.Paragraph>
        <Input.Search size="large" prefix={<SearchOutlined />} placeholder="想找什么？输入关键词或一个问题" enterButton="搜索记忆"
          onSearch={(query) => { if (query.trim()) navigate(routes.recall, { state: { query: query.trim(), autoSearch: true } }); }} />
      </section>
      <Row gutter={[16, 16]} style={{ marginBottom: 24 }}>
        {[
          { title: '添加内容', text: '记录偏好、计划或实用知识', to: routes.newIngestion, icon: <PlusOutlined /> },
          { title: '查看自动整理', text: '阅读摘要、经验和操作指南', to: routes.artifacts, icon: <BookOutlined /> },
          { title: '核对内容', text: '对照来源，处理不一致的说法', to: routes.conflicts, icon: <SafetyCertificateOutlined /> },
        ].map((action) => <Col xs={24} md={8} key={action.to}><Link to={action.to}><Card hoverable><div className="quick-action">
          <span className="quick-action-icon">{action.icon}</span><div><Typography.Title level={5}>{action.title}</Typography.Title><Typography.Text type="secondary">{action.text}</Typography.Text></div>
        </div></Card></Link></Col>)}
      </Row>
      <Row gutter={[24, 24]}>
        <Col xs={24} xl={16}>
          <Card title="最近更新的记忆" extra={<Space><Button type="text" onClick={() => void recent.refetch()} loading={recent.isFetching}>刷新</Button><Link to={routes.memories}>查看记忆库</Link></Space>}>
            {recent.isError ? <Alert type={recent.data ? 'warning' : 'error'} showIcon message={recent.data ? '暂时无法更新，以下是上次读取的内容' : '暂时无法读取记忆'} description={errorMessage(recent.error)} action={<Button onClick={() => void recent.refetch()}>重试</Button>} /> : null}
            {recent.isPending ? <Skeleton active paragraph={{ rows: 6 }} /> : items.length ? items.map((item) => <article className="memory-preview" key={item.id}>
              <Link className="memory-preview-title" to={routes.memory(item.id)}>{item.title || '未命名记忆'}</Link>
              <Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }} style={{ marginBottom: 10 }}>{item.content}</Typography.Paragraph>
              <Space size={[8, 6]} wrap><StatusTag category="memoryKind" value={item.kind} /><StatusTag category="memoryState" value={item.state} />
                {item.assertion_status !== 'accepted' ? <StatusTag category="assertion" value={item.assertion_status} /> : null}
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>{formatTimestamp(item.updated_at)}</Typography.Text>
              </Space>
            </article>) : !recent.isError ? <Empty description="还没有可展示的记忆，从一条偏好或计划开始吧"><Link to={routes.newIngestion}><Button type="primary">添加第一条内容</Button></Link></Empty> : null}
            {items.length ? <div className="section-note">显示最近返回的 {items.length} 条记忆，最多 10 条。</div> : null}
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card title="需要关注" style={{ marginBottom: 20 }}>
            {recent.isPending ? <Skeleton active paragraph={{ rows: 2 }} /> : recent.isError && !recent.data ? <Typography.Text type="secondary">读取记忆后，可在这里查看需要确认的内容。</Typography.Text> : attention.length ? <>
              <Tag color="warning">最近显示的记忆中，{attention.length} 条待确认</Tag>
              <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>这些内容还需要进一步核对，不表示一定有误。</Typography.Paragraph>
              {attention.map((item) => <Typography.Paragraph key={item.id}><Link to={routes.memory(item.id)}>{item.title || '查看待确认的记忆'}</Link></Typography.Paragraph>)}
              <Link to={routes.conflicts}>前往内容核对 →</Link>
            </> : <><Typography.Paragraph>最近显示的内容中暂无待确认记忆。</Typography.Paragraph><Link to={routes.conflicts}>查看其他核对事项 →</Link></>}
          </Card>
          <Card title="可以记些什么？">
            <Typography.Paragraph><strong>个人偏好</strong><br /><Typography.Text type="secondary">常用语言、工作习惯和喜欢的表达方式。</Typography.Text></Typography.Paragraph>
            <Typography.Paragraph><strong>计划与约定</strong><br /><Typography.Text type="secondary">重要安排，以及它们适用的时间和条件。</Typography.Text></Typography.Paragraph>
            <Typography.Paragraph style={{ marginBottom: 0 }}><strong>经验与步骤</strong><br /><Typography.Text type="secondary">解决问题的过程，留给下一次需要时参考。</Typography.Text></Typography.Paragraph>
          </Card>
        </Col>
      </Row>
    </>
  );
}
