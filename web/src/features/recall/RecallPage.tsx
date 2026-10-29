import {
  Alert,
  Button,
  Card,
  Collapse,
  Col,
  DatePicker,
  Descriptions,
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
import type { ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { isRecord } from '../../shared/api/client';
import { errorMessage } from '../../shared/api/errors';
import { normalizeRecallRequest } from '../../shared/api/query-keys';
import {
  activeMemoryStates,
  memoryKinds,
  recallLayers,
  recallQueryModes,
  type ConflictSummary,
  type MemoryKind,
  type MemoryState,
  type NormalizedRecallRequest,
  type RecallDerivedItem,
  type RecallLayer,
  type RecallObservationItem,
  type RecallQueryMode,
  type RecallResponse,
  type SearchDiagnostics,
  type SearchItem,
} from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp, toRFC3339 } from '../../shared/lib/time';
import { JsonView } from '../../shared/ui/JsonView';
import { MarkdownView } from '../../shared/ui/MarkdownView';
import { RelationTable } from '../../shared/ui/RelationTable';
import { MatchHighlight } from '../../shared/ui/MatchHighlight';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { UnsavedChangesGuard } from '../../shared/ui/UnsavedChangesGuard';
import { WarningList } from '../../shared/ui/WarningList';
import { recallMemories } from './api';

interface RecallFormValues {
  query?: string;
  scopeKey?: string;
  queryMode?: RecallQueryMode;
  layers: RecallLayer[];
  mode: 'keyword' | 'hybrid';
  derivedStates: Array<'active' | 'stale'>;
  topK: number;
  maxContextTokens?: number | null;
  explain?: boolean;
  kinds?: MemoryKind[];
  states?: MemoryState[];
  updatedAfter?: Dayjs | null;
  updatedBefore?: Dayjs | null;
}

const initialValues: RecallFormValues = {
  query: '',
  scopeKey: '',
  queryMode: undefined,
  layers: [],
  mode: 'keyword',
  derivedStates: ['active'],
  topK: 20,
  maxContextTokens: null,
  explain: false,
  kinds: [],
  states: [],
  updatedAfter: null,
  updatedBefore: null,
};

function requestFromForm(values: RecallFormValues): NormalizedRecallRequest {
  return normalizeRecallRequest({
    query: values.query ?? '',
    scopeKey: values.scopeKey,
    queryMode: values.queryMode,
    layers: values.layers,
    mode: values.mode,
    derivedStates: values.derivedStates,
    topK: values.topK,
    maxContextTokens: values.maxContextTokens ?? undefined,
    explain: Boolean(values.explain),
    filters: {
      kinds: values.kinds,
      states: values.states,
      updatedAfter: toRFC3339(values.updatedAfter),
      updatedBefore: toRFC3339(values.updatedBefore),
    },
  });
}

function EvidenceLinks({ items }: { items: Array<{ id: string; source_id: string }> }) {
  if (items.length === 0) {
    return <Typography.Text type="secondary">无证据引用</Typography.Text>;
  }
  return (
    <Space wrap>
      {items.map((ref, index) => (
        <Link key={ref.id} to={routes.source(ref.source_id)}>
          来源 {index + 1}
        </Link>
      ))}
    </Space>
  );
}

function ExplanationBlock({ item }: { item: SearchItem | RecallObservationItem }) {
  if (!item.explanation) {
    return null;
  }
  const explanation = item.explanation;
  return (
    <TechnicalDetails label="排序详情（不是真实性置信度）">
      <Descriptions size="small" bordered column={1}>
        <Descriptions.Item label="排序方式">{explanation.ranking}</Descriptions.Item>
        {explanation.field_weights ? (
          <Descriptions.Item label="字段权重">{explanation.field_weights.join(' / ')}</Descriptions.Item>
        ) : null}
        {explanation.keyword ? (
          <Descriptions.Item label="关键词">
            名次 {explanation.keyword.rank} · BM25 {explanation.keyword.bm25.toFixed(6)}
          </Descriptions.Item>
        ) : null}
        {explanation.semantic ? (
          <Descriptions.Item label="语义">
            名次 {explanation.semantic.rank} · 余弦 {explanation.semantic.cosine.toFixed(6)}
          </Descriptions.Item>
        ) : null}
      </Descriptions>
    </TechnicalDetails>
  );
}

function ObservationItemCard({ item }: { item: RecallObservationItem }) {
  const observation = item.observation;
  return (
    <Card
      size="small"
      style={{ marginBottom: 12 }}
      title={
        <Space wrap>
          <Link to={routes.observation(observation.id)}>L0 观察</Link>
          <StatusTag category="memoryKind" value={observation.kind} />
          <StatusTag category="observationStatus" value={observation.status} />
          <Tag>{observation.source_id.slice(0, 8)}</Tag>
        </Space>
      }
    >
      <Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>
        <MatchHighlight text={item.snippet} terms={item.matched_terms} />
      </Typography.Paragraph>
      <Space wrap size={[12, 4]}>
        <Typography.Text type="secondary">
          {observation.scope_key || '范围未知'} · 角色 {observation.origin_role || '未知'}
        </Typography.Text>
        {item.matched_terms.length > 0 ? (
          <Typography.Text type="secondary">命中：{item.matched_terms.join('、')}</Typography.Text>
        ) : null}
      </Space>
      <Collapse
        ghost
        items={[
          {
            key: 'evidence',
            label: `证据引用（${item.evidence_refs.length}）`,
            children: <EvidenceLinks items={item.evidence_refs} />,
          },
        ]}
      />
    </Card>
  );
}

function MemoryItemCard({ item }: { item: SearchItem }) {
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
          <Tag>v{memory.version}</Tag>
        </Space>
      }
    >
      <Typography.Paragraph style={{ whiteSpace: 'pre-wrap' }}>
        <MatchHighlight text={item.snippet} terms={item.matched_terms} />
      </Typography.Paragraph>
      <Space wrap size={[12, 4]}>
        {memory.scope_key ? <Tag>{memory.scope_key}</Tag> : null}
        {item.matched_terms.length > 0 ? (
          <Typography.Text type="secondary">命中：{item.matched_terms.join('、')}</Typography.Text>
        ) : null}
      </Space>
      {item.conflict_refs.length > 0 ? (
        <Space style={{ marginTop: 12 }} wrap>
          <Typography.Text type="warning">涉及冲突：</Typography.Text>
          {item.conflict_refs.map((conflict) => (
            <Link key={conflict.id} to={routes.conflict(conflict.id)}>
              <Tag color="warning">{codeDefinition('conflictReason', conflict.reason_code).label}</Tag>
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
            children: <EvidenceLinks items={item.evidence_refs} />,
          },
          ...(item.explanation
            ? [
                {
                  key: 'explanation',
                  label: '排序解释',
                  children: <ExplanationBlock item={item} />,
                },
              ]
            : []),
        ]}
      />
    </Card>
  );
}

function DerivedItemCard({ item }: { item: RecallDerivedItem }) {
  const artifact = item.artifact;
  const disclaimer =
    artifact.kind === 'experience_case'
      ? '经验案例来自应用报告，不代表独立执行验证。'
      : artifact.kind === 'skill'
        ? '操作指南仅供参考，不代表执行成功或安全性已验证。'
        : undefined;
  return (
    <Card
      size="small"
      style={{ marginBottom: 12 }}
      title={
        <Space wrap>
          <Link to={routes.artifactVersion(artifact.id, item.revision.version)}>
            {item.revision.title || '（无标题）'}
          </Link>
          <StatusTag category="artifactLayer" value={artifact.layer} />
          <StatusTag category="artifactKind" value={artifact.kind} />
          <StatusTag category="artifactState" value={artifact.state} />
          <Tag>v{item.revision.version}</Tag>
          {item.revision.truncated ? <Tag color="warning">输入截断</Tag> : null}
        </Space>
      }
    >
      {disclaimer ? <Alert type="info" showIcon message={disclaimer} style={{ marginBottom: 12 }} /> : null}
      <MarkdownView content={item.revision.content} />
      <Collapse
        style={{ marginTop: 8 }}
        items={[
          {
            key: 'dependencies',
            label: `依赖输入（${item.dependencies.length}）`,
            children:
              item.dependencies.length > 0 ? (
                <Table
                  rowKey={(dependency) => `${dependency.type}:${dependency.id}:${dependency.version}`}
                  size="small"
                  pagination={false}
                  dataSource={item.dependencies}
                  columns={[
                    { title: '类型', dataIndex: 'type', width: 120 },
                    {
                      title: '输入',
                      dataIndex: 'id',
                      render: (value: string, dependency) =>
                        dependency.type === 'observation' ? (
                          <Link to={routes.observation(value)}>{value}</Link>
                        ) : dependency.type === 'memory' ? (
                          <Link to={routes.memory(value)}>{value}</Link>
                        ) : (
                          <span>{value}</span>
                        ),
                    },
                    { title: '版本', dataIndex: 'version', width: 80 },
                  ]}
                />
              ) : (
                <Typography.Text type="secondary">无依赖记录</Typography.Text>
              ),
          },
          {
            key: 'payload',
            label: '技术详情',
            children: <JsonView value={item.revision.payload} maxHeight={240} emptyText="无 payload" />,
          },
        ]}
      />
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        更新于 {formatTimestamp(artifact.updated_at)}
      </Typography.Text>
    </Card>
  );
}

const conflictColumns: ColumnsType<ConflictSummary> = [
  {
    title: '冲突',
    dataIndex: 'id',
    render: (_, item) => <Link to={routes.conflict(item.id)}>查看需要核对的内容</Link>,
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
    ['observation_candidates', '观察候选'],
    ['memory_candidates', '记忆候选'],
    ['derived_candidates', '派生候选'],
    ['semantic_candidates', '语义候选'],
    ['semantic_excluded', '语义排除'],
    ['term_candidates', '词项候选'],
    ['kind_filter_excluded', '类型过滤排除'],
    ['state_filter_excluded', '状态过滤排除'],
    ['ranking_candidates', '参与排序候选'],
    ['returned_items', '最终返回'],
    ['context_budget_truncated', '预算截断'],
    ['semantic_degradation', '语义降级'],
  ];
  return labels
    .filter(([key]) => diagnostics[key] !== undefined)
    .map(([key, label]) => ({ key, label, value: String(diagnostics[key]) }));
}

function Partition({
  title,
  description,
  empty,
  count,
  children,
}: {
  title: string;
  description: string;
  empty: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <Card title={`${title}（${count}）`} style={{ marginTop: 16 }}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {description}
      </Typography.Paragraph>
      {count === 0 ? <Empty description={empty} image={Empty.PRESENTED_IMAGE_SIMPLE} /> : children}
    </Card>
  );
}

export default function RecallPage() {
  const location = useLocation();
  const incomingQuery =
    isRecord(location.state) && typeof location.state.query === 'string' ? location.state.query : '';
  const [form] = Form.useForm<RecallFormValues>();
  const [lastResult, setLastResult] = useState<RecallResponse | null>(null);
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

  const hasResults = lastResult
    ? lastResult.observations.length +
        lastResult.memories.length +
        lastResult.l1.length +
        lastResult.l2.length +
        lastResult.l3.length +
        lastResult.l4.length +
        lastResult.conflicts.length >
      0
    : false;

  return (
    <>
      <UnsavedChangesGuard when={touched} />
      <Typography.Title level={3}>分层召回</Typography.Title>
      <Typography.Paragraph type="secondary">
        分别查询 L0 原始观察、当前规范记忆和 L1-L4 派生记忆。各层独立排序，不把不同层级混成一个结论。
      </Typography.Paragraph>

      <Card title="查询" style={{ marginBottom: 16 }}>
        <Form<RecallFormValues>
          form={form}
          layout="vertical"
          initialValues={{ ...initialValues, query: incomingQuery }}
          onFinish={() => mutation.mutate(requestFromForm(form.getFieldsValue(true)))}
          onValuesChange={() => {
            setTouched(true);
            if (lastResult) {
              setFormChanged(true);
            }
          }}
        >
          <Row gutter={16}>
            <Col xs={24} lg={16}>
              <Form.Item name="query" label="关键词或问题">
                <Input.TextArea rows={2} placeholder="例如：回答语言偏好" />
              </Form.Item>
            </Col>
            <Col xs={24} lg={8}>
              <Form.Item name="scopeKey" label="所属范围（可选）">
                <Input placeholder="例如 project:dmemd" allowClear />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col xs={24} md={8}>
              <Form.Item name="queryMode" label="查询目标">
                <Select
                  allowClear
                  placeholder="不指定：返回全部层级"
                  options={recallQueryModes.map((value) => ({
                    value,
                    label: {
                      overview: '概览',
                      detail: '细节',
                      history: '历史',
                      conflict: '冲突',
                      guidance: '指导',
                    }[value],
                  }))}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item name="layers" label="层级（留空使用查询目标默认值）">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="全部层级"
                  options={recallLayers.map((value) => ({
                    value,
                    label: {
                      observations: 'L0 观察',
                      memories: '当前记忆',
                      l1: 'L1 局部聚合',
                      l2: 'L2 领域聚合',
                      l3: 'L3 全局聚合',
                      l4: 'L4 指导',
                    }[value],
                  }))}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={4}>
              <Form.Item name="mode" label="检索方式">
                <Select
                  options={[
                    { value: 'keyword', label: '关键词' },
                    { value: 'hybrid', label: '关键词 + 语义' },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={4}>
              <Form.Item name="topK" label="每层上限">
                <InputNumber min={0} max={100} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col xs={24} md={6}>
              <Form.Item name="derivedStates" label="派生状态">
                <Select
                  mode="multiple"
                  options={[
                    { value: 'active', label: '可用' },
                    { value: 'stale', label: '已失效（显式查看）' },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={6}>
              <Form.Item name="kinds" label="记忆类型过滤">
                <Select
                  mode="multiple"
                  allowClear
                  options={memoryKinds.map((value) => ({
                    value,
                    label: codeDefinition('memoryKind', value).label,
                  }))}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={6}>
              <Form.Item name="states" label="记忆状态过滤">
                <Select
                  mode="multiple"
                  allowClear
                  placeholder="默认使用中"
                  options={activeMemoryStates.map((value) => ({
                    value,
                    label: codeDefinition('memoryState', value).label,
                  }))}
                />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={16}>
            <Col xs={24} md={8}>
              <Form.Item name="updatedAfter" label="更新时间起点">
                <DatePicker showTime style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item name="updatedBefore" label="更新时间终点">
                <DatePicker showTime style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="解释与上下文预算">
                <Space>
                  <Form.Item name="explain" valuePropName="checked" noStyle>
                    <Switch checkedChildren="解释" unCheckedChildren="解释" defaultChecked={false} />
                  </Form.Item>
                  <Form.Item name="maxContextTokens" noStyle>
                    <InputNumber min={0} placeholder="上下文预算" style={{ width: 150 }} />
                  </Form.Item>
                </Space>
              </Form.Item>
            </Col>
          </Row>
          <Space wrap>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              搜索
            </Button>
            <Button
              onClick={() => {
                form.resetFields();
                form.setFieldsValue({ ...initialValues, query: incomingQuery });
                setLastResult(null);
                setLastRequest(null);
                setTouched(false);
              }}
            >
              清空
            </Button>
          </Space>
        </Form>
      </Card>

      {mutation.isError ? (
        <Alert
          type="error"
          showIcon
          message="召回失败"
          description={errorMessage(mutation.error)}
        />
      ) : null}

      {lastResult ? (
        <>
          {formChanged ? (
            <Alert
              type="info"
              showIcon
              message="表单已修改，下面仍是上一次提交的结果"
              style={{ marginTop: 12 }}
            />
          ) : null}
          {!hasResults ? <Empty description="没有召回结果" style={{ marginTop: 24 }} /> : null}
          <Partition
            title="L0 原始观察"
            description="首次提取形成的不可变原始记录。它们不等于已经形成的当前记忆。"
            empty="没有匹配的原始观察"
            count={lastResult.observations.length}
          >
            {lastResult.observations.map((item) => (
              <ObservationItemCard key={item.observation.id} item={item} />
            ))}
          </Partition>
          <Partition
            title="当前规范记忆"
            description="异步治理后形成的当前视图；已采纳不表示真实性经过外部验证。"
            empty="没有匹配的当前记忆"
            count={lastResult.memories.length}
          >
            {lastResult.memories.map((item) => (
              <MemoryItemCard key={`${item.memory.id}:${item.memory.version}`} item={item} />
            ))}
          </Partition>
          {(
            [
              ['L1 局部聚合', lastResult.l1, '没有匹配的 L1 聚合'],
              ['L2 领域聚合', lastResult.l2, '没有匹配的 L2 聚合'],
              ['L3 全局聚合', lastResult.l3, '没有匹配的 L3 聚合'],
              ['L4 指导', lastResult.l4, '没有匹配的 L4 指导'],
            ] as Array<[string, RecallDerivedItem[], string]>
          ).map(([title, items, empty]) => (
            <Partition
              key={title}
              title={title}
              description="派生内容由代码保存的依赖驱动，已失效内容默认不作为当前结论返回。"
              empty={empty}
              count={items.length}
            >
              {items.map((item) => (
                <DerivedItemCard
                  key={`${item.artifact.id}:${item.revision.version}`}
                  item={item}
                />
              ))}
            </Partition>
          ))}

          {lastResult.relations.length > 0 ? (
            <Card title={`关系（${lastResult.relations.length}）`} style={{ marginTop: 16 }}>
              <RelationTable items={lastResult.relations} />
            </Card>
          ) : null}

          <Card title={`冲突（${lastResult.conflicts.length}）`} style={{ marginTop: 16 }}>
            {lastResult.conflicts.length === 0 ? (
              <Empty description="没有未决冲突" image={Empty.PRESENTED_IMAGE_SIMPLE} />
            ) : (
              <Table
                rowKey="id"
                size="small"
                columns={conflictColumns}
                dataSource={lastResult.conflicts}
                pagination={false}
              />
            )}
          </Card>

          <WarningList codes={lastResult.warnings} title="本次搜索提示" />

          {lastResult.diagnostics ? (
            <TechnicalDetails label="检索诊断">
              <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
                {diagnosticsRows(lastResult.diagnostics).map((row) => (
                  <Descriptions.Item key={row.key} label={row.label}>
                    {row.value}
                  </Descriptions.Item>
                ))}
              </Descriptions>
              {lastRequest ? (
                <JsonView value={lastRequest} maxHeight={300} emptyText="无请求快照" />
              ) : null}
            </TechnicalDetails>
          ) : null}
        </>
      ) : null}

      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        L0 观察、当前记忆和 L1-L4 派生内容分区展示；高层内容缺失或失效时不会阻塞下层查询。
      </Typography.Paragraph>
    </>
  );
}
