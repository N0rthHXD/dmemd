import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Collapse,
  Descriptions,
  Empty,
  Form,
  Input,
  Radio,
  Result,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { invalidateAfterConflictChange } from '../../shared/api/cache-effects';
import { errorMessage, isApiError } from '../../shared/api/errors';
import type {
  ConflictAction,
  ConflictEvent,
  ConflictResolveRequest,
  Evidence,
  Memory,
} from '../../shared/api/types';
import { codeDefinition } from '../../shared/lib/codes';
import { routes } from '../../shared/lib/routes';
import { formatTimestamp } from '../../shared/lib/time';
import { IdText } from '../../shared/ui/IdText';
import { StatusTag } from '../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { UnsavedChangesGuard } from '../../shared/ui/UnsavedChangesGuard';
import { resolveConflict } from './api';
import type { MemberSnapshot } from './api';
import { useConflict, useConflictMembers } from './queries';

interface ResolveFormValues {
  chosenMemberId?: string;
  confirmed?: boolean;
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
          <Link to={routes.memoryVersion(snapshot.id, snapshot.version)}>
            {snapshot.title || '（无标题）'}
          </Link>
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
        <Descriptions.Item label="所属范围">{snapshot.scope_key || '未知'}</Descriptions.Item>
        <Descriptions.Item label="正文" span={2}>
          <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', marginBottom: 0 }}>
            {snapshot.content}
          </Typography.Paragraph>
        </Descriptions.Item>
      </Descriptions>
      <TechnicalDetails label="编号与版本检查">
        <IdText value={member} /> · 发现问题时第 {detectedVersion} 版 · 当前第{' '}
        {currentVersion ?? '未知'} 版 · 本次读取第 {snapshot.version} 版
      </TechnicalDetails>
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
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const conflictQuery = useConflict(id);
  const conflict = conflictQuery.data;
  const memberIds = useMemo(
    () => (conflict?.members ?? []).map((member) => member.memory_id),
    [conflict],
  );
  const members = useConflictMembers(id, conflict?.revision ?? 0, memberIds);

  const snapshotMap = useMemo(() => {
    const map = new Map<string, MemberSnapshot>();
    (members.data ?? []).forEach((entry, index) => {
      map.set(memberIds[index] ?? '', entry);
    });
    return map;
  }, [members.data, memberIds]);

  const memberVersions = useMemo(
    () =>
      (conflict?.members ?? []).flatMap((member) => {
        const snapshot = snapshotMap.get(member.memory_id)?.memory;
        return snapshot ? [{ memory_id: member.memory_id, version: snapshot.version }] : [];
      }),
    [conflict?.members, snapshotMap],
  );

  const resolveMutation = useMutation({
    mutationFn: (body: ConflictResolveRequest) => resolveConflict(id, body),
    onSuccess: async () => {
      setStaleDraft(null);
      await invalidateAfterConflictChange(queryClient);
      message.success('裁决已保存');
    },
    onError: (error) => {
      if (isApiError(error) && error.code === 'conflict_stale') {
        setStaleDraft({
          body: {
            revision: conflict?.revision ?? 0,
            action,
            member_versions: memberVersions,
            reason: form.getFieldValue('reason')?.trim() || undefined,
          },
          at: new Date().toISOString(),
        });
      }
    },
  });

  const submit = (values: ResolveFormValues) => {
    if (!conflict) {
      return;
    }
    let body: ConflictResolveRequest;
    try {
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
      } else {
        if (!values.confirmed) {
          throw new Error('请确认这些记忆全部不采用');
        }
        body = {
          revision: conflict.revision,
          action,
          member_versions: memberVersions,
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
          extra={
            <Link to={routes.conflicts}>
              <Button type="primary">返回列表</Button>
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
      />
    );
  }

  if (!conflict) {
    return <Card loading title="正在读取冲突…" />;
  }

  return (
    <>
      <UnsavedChangesGuard when={form.isFieldsTouched()} />
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
            onClick={() => {
              setStaleDraft(null);
              void conflictQuery.refetch();
            }}
            loading={conflictQuery.isFetching}
          >
            刷新内容
          </Button>
        </Space>
      </Space>

      {staleDraft ? (
        <Alert
          style={{ marginTop: 12 }}
          type="warning"
          showIcon
          message="读取后内容已变化，原裁决未提交"
          description={`检测时间：${formatTimestamp(staleDraft.at)}。请重新读取并确认成员版本后再提交。`}
          action={
            <Button size="small" onClick={() => setStaleDraft(null)}>
              关闭
            </Button>
          }
        />
      ) : null}

      <TechnicalDetails label="核对记录详情">
        <Card style={{ marginTop: 16 }}>
          <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
            <Descriptions.Item label="原因码">
              {codeDefinition('conflictReason', conflict.reason_code).label}（{conflict.reason_code}）
            </Descriptions.Item>
            <Descriptions.Item label="创建时间">{formatTimestamp(conflict.created_at)}</Descriptions.Item>
            <Descriptions.Item label="更新时间">{formatTimestamp(conflict.updated_at)}</Descriptions.Item>
            <Descriptions.Item label="修订号">{conflict.revision}</Descriptions.Item>
          </Descriptions>
        </Card>
      </TechnicalDetails>

      {conflict.status === 'resolved' ? (
        <>
          <Alert style={{ marginTop: 16 }} type="success" showIcon message="此项已处理，下面保留处理记录供回顾" />
          <Card title="处理记录" style={{ marginTop: 16 }}>
            <Descriptions size="small" bordered column={1}>
              <Descriptions.Item label="处理结果">
                <TechnicalDetails label="查看结构化记录">
                  <Typography.Paragraph style={{ marginBottom: 0 }}>
                    {safeJSON(conflict.resolution) === null
                      ? '无'
                      : JSON.stringify(safeJSON(conflict.resolution))}
                  </Typography.Paragraph>
                </TechnicalDetails>
              </Descriptions.Item>
            </Descriptions>
          </Card>
          <Card title="审计事件" style={{ marginTop: 16 }}>
            {(conflict.events ?? []).length > 0 ? (
              <Table
                rowKey="id"
                size="small"
                columns={eventColumns}
                dataSource={conflict.events ?? []}
                pagination={false}
              />
            ) : (
              <Empty description="无审计事件" />
            )}
          </Card>
        </>
      ) : (
        <>
          <Card title="冲突成员" style={{ marginTop: 16 }}>
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              {conflict.members.map((member) => {
                const snapshot = snapshotMap.get(member.memory_id)?.memory;
                return (
                  <MemberCard
                    key={`${member.memory_id}-${member.version}`}
                    member={member.memory_id}
                    detectedVersion={member.version}
                    currentVersion={member.current_version}
                    snapshot={snapshot}
                    evidence={snapshotMap.get(member.memory_id)?.evidence ?? []}
                    mismatch={Boolean(snapshot && snapshot.version !== member.version)}
                    loading={members.isPending}
                  />
                );
              })}
            </Space>
          </Card>

          <Card title="提交裁决" style={{ marginTop: 16 }}>
            <Form<ResolveFormValues>
              form={form}
              layout="vertical"
              initialValues={{ action }}
              onFinish={submit}
            >
              <Form.Item label="处理方式">
                <Radio.Group
                  value={action}
                  onChange={(event) => setAction(event.target.value as ConflictAction)}
                >
                  <Radio value="choose">采用其中一条</Radio>
                  <Radio value="reject_all">全部不采用</Radio>
                </Radio.Group>
              </Form.Item>

              {action === 'choose' ? (
                <Form.Item
                  name="chosenMemberId"
                  label="要采用的记忆"
                  rules={[{ required: true, message: '请选择一条要采用的记忆' }]}
                >
                  <Radio.Group>
                    <Space direction="vertical">
                      {conflict.members.map((member) => {
                        const snapshot = snapshotMap.get(member.memory_id)?.memory;
                        return (
                          <Radio key={member.memory_id} value={member.memory_id}>
                            {snapshot?.title || snapshot?.content || member.memory_id}
                          </Radio>
                        );
                      })}
                    </Space>
                  </Radio.Group>
                </Form.Item>
              ) : (
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
              )}

              <Form.Item name="reason" label="裁决说明（可选）">
                <Input.TextArea rows={2} placeholder="说明为什么采用或否定这些内容" />
              </Form.Item>
              <Typography.Paragraph type="secondary">
                处理动作会作为不可变审计事件保存，不复制原始模型推理。
              </Typography.Paragraph>

              {actionError ? (
                <Alert
                  type="error"
                  showIcon
                  message={actionError instanceof Error ? actionError.message : '请求未提交'}
                  style={{ marginBottom: 12 }}
                />
              ) : null}

              <Button
                type="primary"
                htmlType="submit"
                danger={action === 'reject_all'}
                loading={resolveMutation.isPending}
              >
                提交裁决
              </Button>
            </Form>
          </Card>
        </>
      )}

      <Collapse
        style={{ marginTop: 16 }}
        items={[
          {
            key: 'raw',
            label: '原始记录（只读）',
            children: <TechnicalDetails label="冲突 JSON">{JSON.stringify(conflict, null, 2)}</TechnicalDetails>,
          },
        ]}
      />
    </>
  );
}
