import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Col,
  Collapse,
  DatePicker,
  Descriptions,
  Empty,
  Form,
  Input,
  Radio,
  Result,
  Row,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { Dayjs } from 'dayjs';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { invalidateAfterConflictChange } from '../../shared/api/cache-effects';
import { errorMessage, isApiError, knownCodeMessage } from '../../shared/api/errors';
import type {
  ConflictAction,
  ConflictEvent,
  ConflictResolveRequest,
  Evidence,
  Memory,
} from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import {
  formatTemporal,
  formatTimestamp,
  formatValidInterval,
  toRFC3339,
} from '../../shared/lib/time';
import { JsonView } from '../../shared/ui/JsonView';
import { StatusTag } from '../../shared/ui/StatusTag';
import { IdText } from '../../shared/ui/IdText';
import { UnsavedChangesGuard } from '../../shared/ui/UnsavedChangesGuard';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { resolveConflict } from './api';
import { useConflict, useConflictMembers } from './queries';

interface TimelineRowValues {
  memoryId?: string;
  content?: string;
  validFrom?: Dayjs | null;
  validTo?: Dayjs | null;
}

interface ResolveFormValues {
  chosenMemberId?: string;
  confirmed?: boolean;
  timeline?: TimelineRowValues[];
  subject?: string;
  predicate?: string;
  scope?: string;
  cardinality?: 'single' | 'multiple' | 'unknown';
  reason?: string;
}

interface StaleDraft {
  body: ConflictResolveRequest;
  at: string;
}

const eventColumns: ColumnsType<ConflictEvent> = [
  {
    title: '动作',
    dataIndex: 'action',
    width: 120,
    render: (value: string) => <Tag>{value}</Tag>,
  },
  {
    title: '记忆',
    dataIndex: 'memory_id',
    render: (value: string | undefined) => (value ? <IdText value={value} /> : '—'),
  },
  {
    title: '版本',
    dataIndex: 'version',
    width: 80,
    render: (value: number | undefined) => (value ? `v${value}` : '—'),
  },
  {
    title: '时间',
    dataIndex: 'created_at',
    width: 190,
    render: (value: string) => formatTimestamp(value),
  },
];

function safeJSON(value: string | undefined): unknown {
  if (!value) {
    return null;
  }
  try {
    return JSON.parse(value) as unknown;
  } catch {
    return value;
  }
}

function MemberEvidence({ items }: { items: Evidence[] }) {
  if (items.length === 0) {
    return <Typography.Text type="secondary">无证据</Typography.Text>;
  }
  return (
    <Space direction="vertical" size={6} style={{ width: '100%' }}>
      {items.map((item) => (
        <Card key={item.id} size="small" type="inner">
          <Space direction="vertical" size={2}>
            <Link to={routes.source(item.source_id)}>查看来源原文</Link>
            {item.redacted ? (
              <Tag color="default">原文已清除，不展示旧片段</Tag>
            ) : (
              <Typography.Text style={{ whiteSpace: 'pre-wrap' }}>
                {item.quote || '无片段'}
              </Typography.Text>
            )}
            {!item.redacted && item.location ? (
              <TechnicalDetails label="片段位置"><Typography.Text code>
                {item.location.field || '字段未知'} [{item.location.start},{item.location.end})
              </Typography.Text></TechnicalDetails>
            ) : null}
          </Space>
        </Card>
      ))}
    </Space>
  );
}

function MemberCard({
  member,
  detectedVersion,
  currentVersion,
  snapshot,
  evidence,
  mismatch,
  loading,
}: {
  member: string;
  detectedVersion: number;
  currentVersion: number | undefined;
  snapshot: Memory | undefined;
  evidence: Evidence[];
  mismatch: boolean;
  loading: boolean;
}) {
  if (loading) {
    return (
      <Card size="small" title="正在读取相关记忆">
        <Spin size="small" /> 正在读取成员快照…
      </Card>
    );
  }
  if (!snapshot) {
    return (
      <Card size="small" title="相关记忆暂不可用">
        <Alert type="error" showIcon message="成员快照缺失，不能拿部分成员裁决" />
      </Card>
    );
  }
  return (
    <Card
      size="small"
      title={
        <Space wrap>
          <Link to={routes.memoryVersion(snapshot.id, snapshot.version)}>{snapshot.title || '（无标题）'}</Link>
          <StatusTag category="assertion" value={snapshot.assertion_status} />
          <StatusTag category="memoryState" value={snapshot.state} />
          {mismatch ? <Tag color="error">版本已变化</Tag> : <Tag color="success">版本一致</Tag>}
        </Space>
      }
    >
      <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
        <Descriptions.Item label="类型">
          <StatusTag category="memoryKind" value={snapshot.kind} />
        </Descriptions.Item>
        <Descriptions.Item label="所属范围">
          {snapshot.scope_key || '未知'}
        </Descriptions.Item>
        <Descriptions.Item label="观测时间">
          {formatTimestamp(snapshot.observed_at)}{snapshot.observed_at_basis === 'recorded' ? '（按接收时间记录）' : ''}
        </Descriptions.Item>
        <Descriptions.Item label="事件时间">
          {formatTemporal(snapshot.event_time, snapshot.temporal.event)}
        </Descriptions.Item>
        <Descriptions.Item label="有效区间">
          {formatValidInterval(
            snapshot.valid_from,
            snapshot.valid_to,
            snapshot.temporal.valid_from,
            snapshot.temporal.valid_to,
          )}
        </Descriptions.Item>
        <Descriptions.Item label="正文" span={2}>
          <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', marginBottom: 0 }}>
            {snapshot.content}
          </Typography.Paragraph>
        </Descriptions.Item>
        <Descriptions.Item label="主题" span={2}>
          {snapshot.topics.length > 0 ? snapshot.topics.join('、') : '无'}
        </Descriptions.Item>
      </Descriptions>
      <TechnicalDetails label="编号与版本检查"><IdText value={member} /> · 发现问题时第 {detectedVersion} 版 · 当前第 {currentVersion ?? '未知'} 版 · 本次读取第 {snapshot.version} 版</TechnicalDetails>
      <div style={{ marginTop: 10 }}>
        <Typography.Text strong>证据</Typography.Text>
        <div style={{ marginTop: 6 }}>
          <MemberEvidence items={evidence} />
        </div>
      </div>
    </Card>
  );
}

export default function ConflictDetailPage() {
  const { id = '' } = useParams();
  const [form] = Form.useForm<ResolveFormValues>();
  const [action, setAction] = useState<ConflictAction>('choose');
  const [actionError, setActionError] = useState<unknown>(null);
  const [staleDraft, setStaleDraft] = useState<StaleDraft | null>(null);
  const [reloadRequired, setReloadRequired] = useState(false);
  const [dirty, setDirty] = useState(false);
  const { message } = App.useApp();
  const queryClient = useQueryClient();

  const conflictQuery = useConflict(id);
  const conflict = conflictQuery.data;
  const memberIds = useMemo(
    () => conflict?.members.map((member) => member.memory_id) ?? [],
    [conflict],
  );
  const membersQuery = useConflictMembers(
    id,
    conflict?.revision,
    memberIds,
    conflict?.status === 'open',
  );

  const snapshotMap = useMemo(() => {
    const map = new Map<string, { memory: Memory; evidence: Evidence[] }>();
    (membersQuery.data ?? []).forEach((snapshot) => {
      map.set(snapshot.memory.id, snapshot);
    });
    return map;
  }, [membersQuery.data]);

  const memberIssues = useMemo(() => {
    if (
      !conflict ||
      conflict.status !== 'open' ||
      membersQuery.isPending ||
      membersQuery.isError
    ) {
      return [];
    }
    return conflict.members.flatMap((member) => {
      const snapshot = snapshotMap.get(member.memory_id);
      if (!snapshot) {
        return [`成员 ${member.memory_id} 快照缺失`];
      }
      const expected = member.current_version && member.current_version > 0
        ? member.current_version
        : member.version;
      if (snapshot.memory.version !== expected) {
        return [
          `成员 ${member.memory_id} 实际读取 v${snapshot.memory.version}，详情期望 v${expected}`,
        ];
      }
      return [];
    });
  }, [conflict, snapshotMap, membersQuery.isPending, membersQuery.isError]);

  const membersReady =
    conflict?.status === 'open' &&
    !membersQuery.isPending &&
    !membersQuery.isError &&
    conflict.members.length > 0 &&
    memberIssues.length === 0;

  const resolveMutation = useMutation({
    mutationFn: (body: ConflictResolveRequest) => resolveConflict(id, body),
    onSuccess: async () => {
      await invalidateAfterConflictChange(queryClient);
      setActionError(null);
      setReloadRequired(false);
      setStaleDraft(null);
      message.success('冲突已按提交内容裁决');
      form.resetFields();
      setAction('choose');
      setDirty(false);
    },
    onError: (error, body) => {
      setActionError(error);
      if (isApiError(error) && error.status === 409) {
        setStaleDraft({ body, at: new Date().toISOString() });
        setReloadRequired(true);
      }
    },
  });

  const reloadAfterStale = async () => {
    setReloadRequired(false);
    setActionError(null);
    form.resetFields();
    setAction('choose');
    setDirty(false);
    await conflictQuery.refetch();
    if (conflict?.status === 'open') {
      await membersQuery.refetch();
    }
  };

  const submit = (values: ResolveFormValues) => {
    if (!conflict || !membersReady) {
      return;
    }

    let body: ConflictResolveRequest;
    try {
      const memberVersions = conflict.members.map((member) => {
        const snapshot = snapshotMap.get(member.memory_id);
        if (!snapshot) {
          throw new Error('成员快照缺失');
        }
        return { memory_id: member.memory_id, version: snapshot.memory.version };
      });
      if (action === 'choose') {
        if (!values.chosenMemberId) {
          throw new Error('请选择一条要采用的记忆');
        }
        body = {
          revision: conflict.revision,
          action,
          member_ids: [values.chosenMemberId],
          member_versions: memberVersions,
        };
      } else if (action === 'reject_all') {
        if (!values.confirmed) {
          throw new Error('请确认这些记忆全部不采用');
        }
        body = {
          revision: conflict.revision,
          action,
          member_versions: memberVersions,
        };
      } else if (action === 'separate_scope') {
        if (!values.subject?.trim() || !values.predicate?.trim() || !values.scope?.trim() || !values.cardinality) {
          throw new Error('请完整填写描述对象、属性、适用情况和允许的结果数量');
        }
        body = {
          revision: conflict.revision,
          action,
          member_versions: memberVersions,
          separate_group: {
            subject: values.subject.trim(),
            predicate: values.predicate.trim(),
            scope: values.scope.trim(),
            cardinality: values.cardinality,
          },
        };
      } else {
        const segments = values.timeline ?? [];
        if (segments.length === 0) {
          throw new Error('请至少填写一个时间段');
        }
        let previousFrom: number | null = null;
        let previousTo: number | null = null;
        const timeline = segments.map((segment, index) => {
          const from = toRFC3339(segment.validFrom);
          const to = toRFC3339(segment.validTo);
          if (!from || !to || !segment.validFrom || !segment.validTo) {
            throw new Error(`第 ${index + 1} 段起止时间不完整`);
          }
          if (segment.validFrom.valueOf() >= segment.validTo.valueOf()) {
            throw new Error(`第 ${index + 1} 段开始时间必须早于结束时间`);
          }
          if (previousFrom !== null && segment.validFrom.valueOf() < previousFrom) {
            throw new Error('时间分段必须按输入顺序递增，不会自动排序');
          }
          if (previousTo !== null && previousTo > segment.validFrom.valueOf()) {
            throw new Error('时间分段不能重叠，不会自动补界或调整');
          }
          if (!segment.memoryId && !segment.content?.trim()) {
            throw new Error(`第 ${index + 1} 段未选择已有记忆，需要填写内容`);
          }
          previousFrom = segment.validFrom.valueOf();
          previousTo = segment.validTo.valueOf();
          return {
            memory_id: segment.memoryId || undefined,
            content: segment.content?.trim() || undefined,
            valid_from: from,
            valid_to: to,
          };
        });
        body = {
          revision: conflict.revision,
          action,
          member_versions: memberVersions,
          timeline,
        };
      }
      if (values.reason?.trim()) {
        body.reason = values.reason.trim();
      }
    } catch (error) {
      setActionError(error);
      return;
    }
    setActionError(null);
    resolveMutation.mutate(body);
  };

  if (conflictQuery.isError && !conflict) {
    if (isApiError(conflictQuery.error) && conflictQuery.error.status === 404) {
      return (
        <Result
          status="404"
          title="冲突不存在"
          subTitle="可能已被解决、删除或永久清除。"
          extra={
            <Link to={routes.conflicts}>
              <Button type="primary">返回内容核对</Button>
            </Link>
          }
        />
      );
    }
    return (
      <Alert
        type="error"
        showIcon
        message="冲突读取失败"
        description={errorMessage(conflictQuery.error)}
        action={<Button onClick={() => void conflictQuery.refetch()}>重试</Button>}
      />
    );
  }

  if (!conflict) {
    return <Card loading title="正在读取冲突…" />;
  }

  const actionLabel = codeDefinition('conflictAction', action).label;

  return (
    <>
      <UnsavedChangesGuard when={dirty && conflict.status === 'open'} />
      <Space style={{ width: '100%', justifyContent: 'space-between' }} align="start" wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            核对相关内容
          </Typography.Title>
          <Space wrap>
            <StatusTag category="conflictStatus" value={conflict.status} />
            <Tag color="warning">{codeDefinition('conflictReason', conflict.reason_code).label}</Tag>
          </Space>
        </div>
        <Space wrap>
          <Link to={routes.conflicts}>
            <Button>返回列表</Button>
          </Link>
          <Button
            onClick={() => void (reloadRequired ? reloadAfterStale() : conflictQuery.refetch())}
            loading={conflictQuery.isFetching}
          >
            刷新内容
          </Button>
        </Space>
      </Space>

      <TechnicalDetails label="核对记录详情"><Card style={{ marginTop: 16 }}>
        <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
          <Descriptions.Item label="事实组 ID">{conflict.fact_group_id || '无'}</Descriptions.Item>
          <Descriptions.Item label="原因码">
            {codeDefinition('conflictReason', conflict.reason_code).label}（{conflict.reason_code}）
          </Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatTimestamp(conflict.created_at)}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatTimestamp(conflict.updated_at)}</Descriptions.Item>
        </Descriptions>
      </Card></TechnicalDetails>

      {conflict.status === 'resolved' ? (
        <>
          <Alert
            style={{ marginTop: 16 }}
            type="success"
            showIcon
            message="此项已处理，下面保留处理记录供回顾"
            description="已处理的事项不能重复提交。可通过相关记忆查看当前内容。"
          />
          <TechnicalDetails label="查看处理详情"><Card title="处理结果" style={{ marginTop: 16 }}>
            <JsonView value={safeJSON(conflict.resolution)} maxHeight={400} />
          </Card></TechnicalDetails>
          <Card title="成员（按检测记录）" style={{ marginTop: 16 }}>
            <Table
              rowKey="memory_id"
              size="small"
              pagination={false}
              dataSource={conflict.members}
              columns={[
                {
                  title: '记忆',
                  dataIndex: 'memory_id',
                  render: (value: string) => (
                    <Link to={routes.memory(value)}>查看相关记忆</Link>
                  ),
                },
                {
                  title: '检测版本',
                  dataIndex: 'version',
                  render: (value: number) => `v${value}`,
                },
                {
                  title: '当前版本',
                  dataIndex: 'current_version',
                  render: (value: number | undefined) => (value ? `v${value}` : '未提供'),
                },
              ]}
            />
          </Card>
        </>
      ) : (
        <>
          <Card title="对照内容与来源" style={{ marginTop: 16 }}>
            <Alert
              type="info"
              showIcon
              message={codeDefinition('conflictReason', conflict.reason_code).label}
              description="请结合时间、适用情况和来源判断。为避免处理过时信息，全部内容读取完成后才能确认。"
              style={{ marginBottom: 12 }}
            />
            {membersQuery.isError ? (
              <Alert
                type="error"
                showIcon
                message="成员快照读取失败，禁止提交裁决"
                description={errorMessage(membersQuery.error)}
                action={<Button onClick={() => void membersQuery.refetch()}>重新读取成员</Button>}
              />
            ) : null}
            {memberIssues.length > 0 ? (
              <Alert
                type="error"
                showIcon
                style={{ marginBottom: 12 }}
                message="成员版本检查未通过，禁止提交裁决"
                description={
                  <ul style={{ margin: 0, paddingLeft: 20 }}>
                    {memberIssues.map((issue) => (
                      <li key={issue}>{issue}</li>
                    ))}
                  </ul>
                }
                action={<Button onClick={() => void reloadAfterStale()}>重新加载</Button>}
              />
            ) : null}
            <Row gutter={[12, 12]}>
              {conflict.members.map((member) => {
                const snapshot = snapshotMap.get(member.memory_id);
                const expected = member.current_version && member.current_version > 0
                  ? member.current_version
                  : member.version;
                return (
                  <Col xs={24} xl={12} key={member.memory_id}>
                    <MemberCard
                      member={member.memory_id}
                      detectedVersion={member.version}
                      currentVersion={member.current_version}
                      snapshot={snapshot?.memory}
                      evidence={snapshot?.evidence ?? []}
                      mismatch={Boolean(snapshot && snapshot.memory.version !== expected)}
                      loading={membersQuery.isPending}
                    />
                  </Col>
                );
              })}
            </Row>
          </Card>

          {reloadRequired ? (
            <Alert
              style={{ marginTop: 16 }}
              type="warning"
              showIcon
              message="版本或冲突已变化，旧草稿已冻结，不能自动重发"
              description={
                <Space direction="vertical" size={8} style={{ width: '100%' }}>
                  <span>{knownCodeMessage(isApiError(actionError) ? actionError.code : undefined) ?? errorMessage(actionError)}</span>
                  <Button onClick={() => void reloadAfterStale()}>重新加载冲突与全部成员</Button>
                  <Typography.Text type="secondary">
                    重新加载会清空当前表单；请重新检查快照并确认后再提交。
                  </Typography.Text>
                </Space>
              }
            />
          ) : actionError ? (
            <Alert
              style={{ marginTop: 16 }}
              type="error"
              showIcon
              message="裁决请求失败，表单已保留"
              description={
                <Space direction="vertical" size={2}>
                  <span>{errorMessage(actionError)}</span>
                  {isApiError(actionError) && actionError.code ? (
                    <Typography.Text code>{actionError.code}</Typography.Text>
                  ) : null}
                </Space>
              }
              closable
              onClose={() => setActionError(null)}
            />
          ) : null}

          {staleDraft ? (
            <Collapse
              style={{ marginTop: 16 }}
              items={[
                {
                  key: 'draft',
                  label: `冻结的旧草稿（${formatTimestamp(staleDraft.at)}，仅供参考，不会自动重发）`,
                  children: <JsonView value={staleDraft.body} maxHeight={320} />,
                },
              ]}
            />
          ) : null}

          <Card title="决定如何保留" style={{ marginTop: 16 }}>
            <Form<ResolveFormValues>
              form={form}
              layout="vertical"
              onFinish={submit}
              onValuesChange={() => setDirty(true)}
              initialValues={{ timeline: [{}] }}
            >
              <Form.Item label="动作">
                <Radio.Group
                  value={action}
                  onChange={(event) => {
                    setAction(event.target.value as ConflictAction);
                    setActionError(null);
                    setDirty(true);
                  }}
                  options={[
                    { value: 'choose', label: '采用其中一条' },
                    { value: 'reject_all', label: '全部不采用' },
                    { value: 'timeline', label: '按不同时间保留' },
                    { value: 'separate_scope', label: '按适用情况重新分组' },
                  ]}
                />
              </Form.Item>

              <Alert
                style={{ marginBottom: 16 }}
                type="info"
                showIcon
                message={`动作摘要：${actionLabel}`}
                description={
                  action === 'choose'
                    ? '采用选中的记忆，其他说法不再采纳。如果内容分别适用于不同时间，请按时间保留。'
                    : action === 'reject_all'
                      ? '这些记忆都不会再被采纳，请确认没有需要保留的说法。'
                      : action === 'timeline'
                        ? '为每个时间段选择已有记忆或填写新内容。未被时间段覆盖的旧说法不再采纳。'
                        : '为这些内容整体重新指定事项与适用情况，不会更改记忆的所属范围，也不保证待确认内容自动被采纳。'
                }
              />

              {action === 'choose' ? (
                <Form.Item
                  label="选择要采纳的成员"
                  name="chosenMemberId"
                  rules={[{ required: true, message: '请选择一个成员' }]}
                >
                  <Select
                    placeholder="选择一个成员"
                    options={conflict.members.map((member) => {
                      const snapshot = snapshotMap.get(member.memory_id);
                      return {
                        value: member.memory_id,
                        label: `${snapshot?.memory.title || '相关记忆'} · 第 ${snapshot?.memory.version ?? '未读取'} 版`,
                      };
                    })}
                  />
                </Form.Item>
              ) : null}

              {action === 'reject_all' ? (
                <Form.Item
                  name="confirmed"
                  valuePropName="checked"
                  rules={[
                    {
                      validator: (_, value: boolean) =>
                        value ? Promise.resolve() : Promise.reject(new Error('请明确确认否定全部成员')),
                    },
                  ]}
                >
                  <Checkbox>我确认否定该冲突的全部成员</Checkbox>
                </Form.Item>
              ) : null}

              {action === 'separate_scope' ? (
                <Row gutter={12}>
                  <Col xs={24} md={12}>
                    <Form.Item
                      label="描述的是谁或什么"
                      name="subject"
                      rules={[{ required: true, message: '请填写描述对象' }]}
                    >
                      <Input />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={12}>
                    <Form.Item
                      label="描述的属性或行为"
                      name="predicate"
                      rules={[{ required: true, message: '请填写属性或行为' }]}
                    >
                      <Input />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={16}>
                    <Form.Item
                      label="具体适用情况（不会修改所属范围）"
                      name="scope"
                      rules={[{ required: true, message: '请填写事实场景' }]}
                    >
                      <Input placeholder="例如：在项目 A 的部署场景" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={8}>
                    <Form.Item
                      label="同一情况下允许多少个结果"
                      name="cardinality"
                      rules={[{ required: true, message: '请选择允许的结果数量' }]}
                    >
                      <Select
                        options={[
                          { value: 'single', label: '一个' },
                          { value: 'multiple', label: '多个' },
                          { value: 'unknown', label: '尚不确定' },
                        ]}
                      />
                    </Form.Item>
                  </Col>
                </Row>
              ) : null}

              {action === 'timeline' ? (
                <Form.List name="timeline">
                  {(fields, { add, remove }) => (
                    <Space direction="vertical" size={12} style={{ width: '100%' }}>
                      <Alert
                        type="warning"
                        showIcon
                        message="时间分段必须完整、递增且不重叠"
                        description="请按先后顺序填写完整的起止时间，不要重叠。每段可以选择已有记忆；不选择时，需要填写新的内容。"
                      />
                      {fields.map((field, index) => (
                        <Card
                          key={field.key}
                          size="small"
                          title={`分段 ${index + 1}`}
                          extra={
                            fields.length > 1 ? (
                              <Button danger size="small" onClick={() => remove(field.name)}>
                                删除
                              </Button>
                            ) : null
                          }
                        >
                          <Row gutter={12}>
                            <Col xs={24} md={12}>
                              <Form.Item label="引用已有成员（可选）" name={[field.name, 'memoryId']}>
                                <Select
                                  allowClear
                                  placeholder="不选表示新分段"
                                  options={conflict.members.map((member) => ({
                                    value: member.memory_id,
                                    label: `${snapshotMap.get(member.memory_id)?.memory.title || '相关记忆'} · 第 ${member.version} 版`,
                                  }))}
                                />
                              </Form.Item>
                            </Col>
                            <Col xs={24} md={12}>
                              <Form.Item
                                label="正文（新分段必填，已有成员可选修改）"
                                name={[field.name, 'content']}
                                rules={[
                                  {
                                    validator: (_, value: string | undefined) => {
                                      const row = form.getFieldValue([
                                        'timeline',
                                        field.name,
                                      ]) as TimelineRowValues | undefined;
                                      if (!row?.memoryId && !value?.trim()) {
                                        return Promise.reject(
                                          new Error('未选择已有记忆时，请填写内容'),
                                        );
                                      }
                                      return Promise.resolve();
                                    },
                                  },
                                ]}
                              >
                                <Input.TextArea rows={2} />
                              </Form.Item>
                            </Col>
                            <Col xs={24} md={12}>
                              <Form.Item
                                label="开始适用时间"
                                name={[field.name, 'validFrom']}
                                rules={[{ required: true, message: '请选择开始时间' }]}
                              >
                                <DatePicker showTime style={{ width: '100%' }} />
                              </Form.Item>
                            </Col>
                            <Col xs={24} md={12}>
                              <Form.Item
                                label="结束适用时间"
                                name={[field.name, 'validTo']}
                                rules={[{ required: true, message: '请选择结束时间' }]}
                              >
                                <DatePicker showTime style={{ width: '100%' }} />
                              </Form.Item>
                            </Col>
                          </Row>
                        </Card>
                      ))}
                      <Button
                        onClick={() => {
                          add({});
                          setDirty(true);
                        }}
                        type="dashed"
                      >
                        添加分段
                      </Button>
                    </Space>
                  )}
                </Form.List>
              ) : null}

              <Form.Item label="处理说明（可选）" name="reason" style={{ marginTop: 16 }}>
                <Input.TextArea rows={2} placeholder="可选的人类裁决说明" />
              </Form.Item>

              <Space wrap>
                <Button
                  type="primary"
                  htmlType="submit"
                  danger={action === 'reject_all'}
                  disabled={!membersReady || reloadRequired}
                  loading={resolveMutation.isPending}
                >
                  确认：{actionLabel}
                </Button>
                <Typography.Text type="secondary">
                  如果内容在处理期间发生变化，需要重新查看后确认。
                </Typography.Text>
              </Space>
            </Form>
          </Card>
        </>
      )}

      <TechnicalDetails label="处理历史与技术详情"><Card title="处理历史" style={{ marginTop: 16 }}>
        <Table<ConflictEvent>
          rowKey="id"
          size="small"
          columns={eventColumns}
          dataSource={conflict.events ?? []}
          pagination={false}
          locale={{ emptyText: '无审计事件' }}
        />
      </Card></TechnicalDetails>

      {conflict.status === 'open' && !membersReady && !membersQuery.isPending ? (
        <Empty description="成员快照尚未全部通过版本检查，裁决表单不可提交" style={{ marginTop: 16 }} />
      ) : null}

      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        核对结果只代表如何保留和使用这些内容，不代表经过独立的真实性验证。
      </Typography.Paragraph>
    </>
  );
}
