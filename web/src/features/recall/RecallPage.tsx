import {
  Alert,
  Button,
  Card,
  Collapse,
  Col,
  DatePicker,
  Descriptions,
  Divider,
  Empty,
  Form,
  Input,
  InputNumber,
  Row,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { Dayjs } from 'dayjs';
import { useMutation } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { errorMessage } from '../../shared/api/errors';
import { normalizeRecallRequest } from '../../shared/api/query-keys';
import {
  activeMemoryStates,
  memoryKinds,
  type ArtifactItem,
  type ConflictSummary,
  type MemoryKind,
  type MemoryState,
  type NormalizedRecallRequest,
  type SearchDiagnostics,
  type SearchItem,
  type SearchResponse,
} from '../../shared/api/types';
import { codeDefinition, relatedReasonText } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, toRFC3339 } from '../../shared/lib/time';
import { JsonView } from '../../shared/ui/JsonView';
import { MarkdownView } from '../../shared/ui/MarkdownView';
import { MatchHighlight } from '../../shared/ui/MatchHighlight';
import { StatusTag } from '../../shared/ui/StatusTag';
import { UnsavedChangesGuard } from '../../shared/ui/UnsavedChangesGuard';
import { WarningList } from '../../shared/ui/WarningList';
import { recallMemories } from './api';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { IdText } from '../../shared/ui/IdText';
import { isRecord } from '../../shared/api/client';

type RecallLayer = 'atomic' | 'summary' | 'experience' | 'skill';

interface RecallFormValues {
  query?: string;
  scopeKey?: string;
  layers: RecallLayer[];
  mode: 'keyword' | 'hybrid';
  topK: number;
  maxContextTokens?: number | null;
  explain?: boolean;
  kinds?: MemoryKind[];
  states?: MemoryState[];
  createdByApp?: string;
  updatedAfter?: Dayjs | null;
  updatedBefore?: Dayjs | null;
  asOf?: Dayjs | null;
  eventAt?: Dayjs | null;
}

const initialValues: RecallFormValues = {
  query: '',
  scopeKey: '',
  layers: ['atomic', 'summary', 'experience', 'skill'],
  mode: 'keyword',
  topK: 20,
  maxContextTokens: null,
  explain: false,
  kinds: [],
  states: [],
  createdByApp: '',
  updatedAfter: null,
  updatedBefore: null,
  asOf: null,
  eventAt: null,
};

const layerOptions: Array<{ value: RecallLayer; label: string }> = [
  { value: 'atomic', label: '记忆' },
  { value: 'summary', label: '摘要 / 概览 / 偏好与习惯' },
  { value: 'experience', label: '经验案例' },
  { value: 'skill', label: '操作指南' },
];

function requestFromForm(values: RecallFormValues): NormalizedRecallRequest {
  return normalizeRecallRequest({
    query: values.query ?? '',
    scopeKey: values.scopeKey,
    layers: values.layers,
    mode: values.mode,
    topK: values.topK,
    maxContextTokens: values.maxContextTokens ?? undefined,
    explain: Boolean(values.explain),
    filters: {
      kinds: values.kinds,
      states: values.states,
      asOf: toRFC3339(values.asOf),
      eventAt: toRFC3339(values.eventAt),
      createdByApp: values.createdByApp,
      updatedAfter: toRFC3339(values.updatedAfter),
      updatedBefore: toRFC3339(values.updatedBefore),
    },
  });
}

function AtomicItem({ item }: { item: SearchItem }) {
  const memory = item.memory;
  return (
    <Card
      size="small"
      style={{ marginBottom: 12 }}
      title={
        <Space wrap>
          <Link to={routes.memoryVersion(memory.id, memory.version)}>
            {memory.title || '（无标题）'}
          </Link>
          <StatusTag category="memoryKind" value={memory.kind} />
          <StatusTag category="memoryState" value={memory.state} />
          <StatusTag category="assertion" value={memory.assertion_status} />
          <Tag color={item.temporal_status === 'unknown' ? 'warning' : 'default'}>
            {item.temporal_status === 'unknown' ? '有效区间未知' : '在已知区间内'}
          </Tag>
        </Space>
      }
    >
      <Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>
        <MatchHighlight text={item.snippet} terms={item.matched_terms} />
      </Typography.Paragraph>
      <Space wrap size={[12, 4]}>
        <Typography.Text type="secondary">v{memory.version}</Typography.Text>
        {memory.scope_key ? <Tag>{memory.scope_key}</Tag> : null}
        {memory.topics.map((topic) => (
          <Tag key={topic}>{topic}</Tag>
        ))}
        {item.matched_terms.length > 0 ? (
          <Typography.Text type="secondary">
            命中：{item.matched_terms.join('、')}
          </Typography.Text>
        ) : null}
      </Space>
      {item.related ? (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={`关联结果：${item.related.reason_codes.map(relatedReasonText).join('、')}`}
          description={
            <Space wrap>
              相关记忆：
              {item.related.anchor_memory_ids.map((memoryId, index) => (
                <Link key={memoryId} to={routes.memory(memoryId)}>
                  查看记忆 {index + 1}
                </Link>
              ))}
            </Space>
          }
        />
      ) : null}
      {item.cluster ? (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message="重复表述只做了合并展示，不代表数据已合并"
          description={
            <Space direction="vertical" size={2}>
              <span>证据数量：{item.cluster.evidence_count}</span>
              <Space wrap>
                成员：
                {item.cluster.member_ids.map((memoryId, index) => (
                  <Link key={memoryId} to={routes.memory(memoryId)}>
                    相关记忆 {index + 1}
                  </Link>
                ))}
              </Space>
            </Space>
          }
        />
      ) : null}
      {item.conflict_refs.length > 0 ? (
        <Space style={{ marginTop: 12 }} wrap>
          <Typography.Text type="warning">涉及冲突：</Typography.Text>
          {item.conflict_refs.map((conflict) => (
            <Link key={conflict.id} to={routes.conflict(conflict.id)}>
              <Tag color="warning">{codeDefinition('conflictReason', conflict.reason_code).label}</Tag>
              查看核对事项
            </Link>
          ))}
        </Space>
      ) : null}
      <Collapse
        ghost
        style={{ marginTop: 8 }}
        items={[
          {
            key: 'evidence',
            label: `证据引用（${item.evidence_refs.length}）`,
            children:
              item.evidence_refs.length > 0 ? (
                <Space wrap>
                  {item.evidence_refs.map((ref, index) => (
                    <Link key={ref.id} to={routes.source(ref.source_id)}>
                      来源 {index + 1}
                    </Link>
                  ))}
                </Space>
              ) : (
                <Typography.Text type="secondary">无证据引用</Typography.Text>
              ),
          },
          ...(item.explanation
            ? [
                {
                  key: 'explanation',
                  label: '排序解释（不是真实性置信度）',
                   children: (
                     <TechnicalDetails label="相关度与排序详情">
                    <Descriptions size="small" bordered column={1}>
                      <Descriptions.Item label="排序方式">
                        {item.explanation.ranking}
                      </Descriptions.Item>
                      {item.explanation.field_weights ? (
                        <Descriptions.Item label="字段权重">
                          {item.explanation.field_weights.join(' / ')}
                        </Descriptions.Item>
                      ) : null}
                      {item.explanation.keyword ? (
                        <Descriptions.Item label="关键词">
                          名次 {item.explanation.keyword.rank} · BM25{' '}
                          {item.explanation.keyword.bm25.toFixed(6)}
                        </Descriptions.Item>
                      ) : null}
                      {item.explanation.semantic ? (
                        <Descriptions.Item label="语义">
                          名次 {item.explanation.semantic.rank} · 余弦{' '}
                          {item.explanation.semantic.cosine.toFixed(6)}
                        </Descriptions.Item>
                      ) : null}
                    </Descriptions></TechnicalDetails>
                  ),
                },
              ]
            : []),
        ]}
      />
    </Card>
  );
}

function DerivedItem({ item }: { item: ArtifactItem }) {
  const disclaimer =
    item.kind === 'experience_case'
      ? '经验案例来自应用报告，不代表独立执行验证。'
      : item.kind === 'skill'
        ? '操作指南仅供参考，不代表执行成功或安全性已验证，页面不会执行或安装它。'
        : undefined;
  return (
    <Card
      size="small"
      style={{ marginBottom: 12 }}
      title={
        <Space wrap>
          <Link to={routes.artifactVersion(item.id, item.version)}>
            {item.title || '（无标题）'}
          </Link>
          <StatusTag category="artifactKind" value={item.kind} />
          <StatusTag category="artifactState" value={item.state} />
          <Tag>v{item.version}</Tag>
          {item.truncated ? <Tag color="warning">输入截断</Tag> : null}
        </Space>
      }
    >
      {disclaimer ? <Alert type="info" showIcon message={disclaimer} style={{ marginBottom: 12 }} /> : null}
      {item.scope_key || item.topic ? (
        <Space wrap style={{ marginBottom: 8 }}>
          {item.scope_key ? <Tag>{item.scope_key}</Tag> : null}
          {item.topic ? <Tag>主题 {item.topic}</Tag> : <Tag color="default">主题未知</Tag>}
        </Space>
      ) : null}
      <MarkdownView content={item.content} />
      <Collapse
        items={[
          {
            key: 'payload',
            label: '技术详情',
            children: <JsonView value={item.payload} maxHeight={240} emptyText="无 payload" />,
          },
          {
            key: 'refs',
            label: '直接引用',
            children: (
              <Space direction="vertical" size={6}>
                <div>
                  <Typography.Text strong>记忆引用：</Typography.Text>
                  {item.memory_refs.length > 0 ? (
                    <Space wrap>
                      {item.memory_refs.map((ref, index) => (
                        <Link
                          key={`${ref.memory_id}-${ref.version}`}
                          to={routes.memoryVersion(ref.memory_id, ref.version)}
                        >
                          参考记忆 {index + 1} · 第 {ref.version} 版
                        </Link>
                      ))}
                    </Space>
                  ) : (
                    '无'
                  )}
                </div>
                <div>
                  <Typography.Text strong>运行引用：</Typography.Text>
                  {item.run_refs.length > 0
                    ? <Space wrap>{item.run_refs.map((ref) => <IdText key={ref.experience_id} value={ref.experience_id} />)}</Space>
                    : '无'}
                </div>
              </Space>
            ),
          },
        ]}
      />
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        更新于 {formatTimestamp(item.updated_at)}
      </Typography.Text>
    </Card>
  );
}

const conflictColumns: ColumnsType<ConflictSummary> = [
  {
    title: '冲突',
    dataIndex: 'id',
    render: (_, item) => (
      <Space direction="vertical" size={0}>
        <Link to={routes.conflict(item.id)}>查看需要核对的内容</Link>
      </Space>
    ),
  },
  {
    title: '原因',
    dataIndex: 'reason_code',
    render: (value: string) => (
      <Tag color="warning">{codeDefinition('conflictReason', value).label}</Tag>
    ),
  },
  {
    title: '成员',
    dataIndex: 'member_ids',
    render: (value: string[]) => `${value.length} 条记忆`,
  },
  {
    title: '已含已采纳',
    dataIndex: 'has_accepted',
    render: (value: boolean) => (value ? '是' : '否'),
  },
];

function diagnosticsRows(diagnostics: SearchDiagnostics) {
  const labels: Array<[keyof SearchDiagnostics, string]> = [
    ['keyword_candidates', '关键词候选'],
    ['semantic_candidates', '语义候选'],
    ['semantic_excluded', '语义排除'],
    ['term_candidates', '词项候选'],
    ['kind_filter_excluded', '类型过滤排除'],
    ['state_filter_excluded', '状态过滤排除'],
    ['time_filter_excluded', '时间过滤排除'],
    ['ranking_candidates', '参与排序候选'],
    ['related_expanded', '关联展开'],
    ['related_excluded', '关联排除'],
    ['returned_items', '最终返回'],
    ['context_budget_truncated', '预算截断'],
    ['semantic_degradation', '语义降级'],
  ];
  return labels
    .filter(([key]) => diagnostics[key] !== undefined)
    .map(([key, label]) => ({
      key,
      label,
      value: String(diagnostics[key]),
    }));
}

export default function RecallPage() {
  const location = useLocation();
  const incomingQuery = isRecord(location.state) && typeof location.state.query === 'string' ? location.state.query : '';
  const [form] = Form.useForm<RecallFormValues>();
  const [lastResult, setLastResult] = useState<SearchResponse | null>(null);
  const [lastRequest, setLastRequest] = useState<NormalizedRecallRequest | null>(null);
  const [formChanged, setFormChanged] = useState(false);
  const [touched, setTouched] = useState(false);
  const autoSearch =
    isRecord(location.state) && location.state.autoSearch === true && incomingQuery.length > 0;
  const autoSearchKey = useRef<string | null>(null);

  const mutation = useMutation({
    mutationFn: recallMemories,
    onSuccess: (data, request) => {
      setLastResult(data);
      setLastRequest(request);
      setFormChanged(false);
      setTouched(false);
    },
  });

  useEffect(() => {
    if (!autoSearch || autoSearchKey.current === location.key) {
      return;
    }
    autoSearchKey.current = location.key;
    form.setFieldsValue({ query: incomingQuery });
    form.submit();
  }, [autoSearch, form, incomingQuery, location.key]);

  const submit = (values: RecallFormValues) => {
    mutation.mutate(requestFromForm(values));
  };

  const hasResults = lastResult
    ? lastResult.memories.length +
        lastResult.summaries.length +
        lastResult.experiences.length +
        lastResult.skills.length +
        lastResult.conflicts.length >
      0
    : false;

  return (
    <>
      <UnsavedChangesGuard when={touched} />
      <Typography.Title level={3}>搜索记忆</Typography.Title>
      <Typography.Paragraph type="secondary">
        输入关键词或一个问题，查找记忆以及自动整理的摘要、经验和操作指南。
      </Typography.Paragraph>

      <Card title="查询" style={{ marginBottom: 16 }}>
        <Form<RecallFormValues>
          form={form}
          layout="vertical"
          initialValues={{ ...initialValues, query: incomingQuery }}
          onFinish={() => submit(form.getFieldsValue(true))}
          onValuesChange={() => {
            setTouched(true);
            if (lastResult) {
              setFormChanged(true);
            }
          }}
        >
          <Row gutter={16}>
            <Col xs={24} lg={14}>
              <Form.Item
                label="关键词 / 自然语言问题"
                name="query"
                rules={[{ required: false }]}
                extra="点击搜索后才会查询；使用自然语言时，可选择关键词与语义搜索。"
              >
                <Input.TextArea rows={3} placeholder="例如：修改项目后应该如何验证" allowClear />
              </Form.Item>
            </Col>
            <Col xs={24} lg={10}>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item label="所属范围" name="scopeKey" extra="指定范围时，也会包含用户全局范围的记忆。">
                    <Input placeholder="例如 project:dmemd，留空不限制" allowClear />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item
                    label="模式"
                    name="mode"
                    extra="语义搜索只用于记忆；自动整理内容按关键词搜索。"
                  >
                    <Select
                      options={[
                        { value: 'keyword', label: '关键词' },
                        { value: 'hybrid', label: '关键词与语义' },
                      ]}
                    />
                  </Form.Item>
                </Col>
                <Col span={24}><TechnicalDetails label="结果数量与排序选项"><Row gutter={12}><Col span={12}>
                  <Form.Item
                    label="每类最多显示"
                    name="topK"
                    rules={[
                      { required: true, message: '请输入每类结果的条数上限' },
                      {
                        type: 'number',
                        min: 1,
                        max: 100,
                        message: '每类最多显示 1～100 条',
                      },
                    ]}
                  >
                    <InputNumber min={1} max={100} style={{ width: '100%' }} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                 <Form.Item label="结果长度限制" name="maxContextTokens">
                    <InputNumber
                      min={1}
                      style={{ width: '100%' }}
                      placeholder="可选，按字符估算"
                    />
                  </Form.Item>
                </Col>
              </Row>
               <Form.Item label="内容类别" name="layers">
                <Select mode="multiple" options={layerOptions} placeholder="留空等同全部四层" />
              </Form.Item>
              <Form.Item
                label="按需排序解释"
                name="explain"
                valuePropName="checked"
                extra="查看相关度和排序过程，仅用于排查搜索效果，不代表内容真实性。"
              >
                <Switch />
              </Form.Item></TechnicalDetails></Col>
              </Row>
            </Col>
          </Row>


          <Collapse
            items={[
              {
                key: 'filters',
                label: '更多条件与时间',
                children: (
                  <Row gutter={16}>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item label="记忆类型" name="kinds">
                        <Select
                          mode="multiple"
                          allowClear
                          placeholder="留空不限制"
                          options={memoryKinds.map((kind) => ({
                            value: kind,
                            label: codeDefinition('memoryKind', kind).label,
                          }))}
                        />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item
                        label="保存状态"
                        name="states"
                        extra="已删除的内容不会出现在搜索结果中。"
                      >
                        <Select
                          mode="multiple"
                          allowClear
                          placeholder="留空使用服务端默认"
                          options={activeMemoryStates.map((state) => ({
                            value: state,
                            label: codeDefinition('memoryState', state).label,
                          }))}
                        />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item label="来源应用" name="createdByApp">
                        <Input allowClear placeholder="留空不限制" />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item label="更新时间起点" name="updatedAfter">
                        <DatePicker showTime style={{ width: '100%' }} placeholder="留空省略" />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item label="更新时间终点" name="updatedBefore">
                        <DatePicker showTime style={{ width: '100%' }} placeholder="留空省略" />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8} />
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item
                        label="在哪个时间适用"
                        name="asOf"
                        extra="按事实适用时间查找，不是查询当时保存了什么。此条件只返回记忆，不返回自动整理内容。"
                      >
                        <DatePicker showTime style={{ width: '100%' }} placeholder="留空省略" />
                      </Form.Item>
                    </Col>
                    <Col xs={24} md={12} lg={8}>
                      <Form.Item
                        label="事件发生时间"
                        name="eventAt"
                        extra="与适用时间不同，按事情实际发生的时间查找。"
                      >
                        <DatePicker showTime style={{ width: '100%' }} placeholder="留空省略" />
                      </Form.Item>
                    </Col>
                  </Row>
                ),
              },
            ]}
          />

          <Divider />
          <Space wrap>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              搜索
            </Button>
            <Button
              onClick={() => {
                form.setFieldsValue(initialValues);
                setTouched(false);
                if (lastResult) {
                  setFormChanged(true);
                }
              }}
            >
              重置表单
            </Button>
            <Typography.Text type="secondary">
              结果按记忆、摘要、经验和指南分类展示。
            </Typography.Text>
          </Space>
        </Form>
      </Card>

      {formChanged && lastResult ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message="以下结果是上一查询的结果，当前表单修改尚未提交"
        />
      ) : null}
      {mutation.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message="本次召回请求失败"
          description={errorMessage(mutation.error)}
          action={
            <Button size="small" onClick={() => void mutation.reset()}>
              清除错误
            </Button>
          }
        />
      ) : null}

      {!lastResult && !mutation.isPending && !mutation.isError ? (
        <Card>
          <Empty description="输入关键词或问题，再点击搜索。也可以试试更具体的项目、时间或事项。" />
        </Card>
      ) : null}

      {lastResult ? (
        <>
          <WarningList codes={lastResult.warnings} title="本次搜索提示" />
          {!hasResults ? (
            <Card style={{ marginTop: 12 }}>
              <Empty description="本次查询已提交，但没有返回任何分区的结果" />
              {lastRequest ? (
                <Typography.Text type="secondary">
                  试着缩短问题、换一个关键词，或减少筛选条件。
                </Typography.Text>
              ) : null}
            </Card>
          ) : null}

          <Card
            title={`记忆（${lastResult.memories.length}）`}
            style={{ marginTop: 16 }}
            extra={<Typography.Text type="secondary">按服务端顺序展示</Typography.Text>}
          >
            {lastResult.memories.length === 0 ? (
              <Empty description="没有找到相关记忆，可尝试其他关键词" />
            ) : (
              lastResult.memories.map((item) => <AtomicItem key={item.memory.id} item={item} />)
            )}
          </Card>
          <Card title={`摘要 / 概览 / 偏好与习惯（${lastResult.summaries.length}）`} style={{ marginTop: 16 }}>
            {lastResult.summaries.length === 0 ? (
              <Empty description="摘要分区没有结果" />
            ) : (
              lastResult.summaries.map((item) => (
                <DerivedItem key={item.id} item={item} />
              ))
            )}
          </Card>
          <Card title={`经验案例（${lastResult.experiences.length}）`} style={{ marginTop: 16 }}>
            {lastResult.experiences.length === 0 ? (
              <Empty description="经验分区没有结果" />
            ) : (
              lastResult.experiences.map((item) => (
                <DerivedItem key={item.id} item={item} />
              ))
            )}
          </Card>
          <Card title={`操作指南（${lastResult.skills.length}）`} style={{ marginTop: 16 }}>
            {lastResult.skills.length === 0 ? (
              <Empty description="没有找到相关操作指南" />
            ) : (
              lastResult.skills.map((item) => <DerivedItem key={item.id} item={item} />)
            )}
          </Card>
          <Card title={`需要核对的内容（${lastResult.conflicts.length}）`} style={{ marginTop: 16 }}>
            <Table<ConflictSummary>
              rowKey="id"
              size="small"
              columns={conflictColumns}
              dataSource={lastResult.conflicts}
              pagination={false}
              locale={{ emptyText: '没有冲突摘要' }}
            />
          </Card>
          {lastResult.diagnostics ? (
            <TechnicalDetails label="技术详情与排序诊断"><Card title="排序诊断" style={{ marginTop: 16 }}>
              <Table
                rowKey="key"
                size="small"
                pagination={false}
                dataSource={diagnosticsRows(lastResult.diagnostics)}
                columns={[
                  { title: '指标', dataIndex: 'label', width: 220 },
                  { title: '值', dataIndex: 'value' },
                ]}
              />
              <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
                这些是有界的阶段计数，不是语义正确性证明。
              </Typography.Paragraph>
            </Card></TechnicalDetails>
          ) : null}
        </>
      ) : null}
    </>
  );
}
