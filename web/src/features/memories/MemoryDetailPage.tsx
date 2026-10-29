import {
  Alert,
  App,
  Button,
  Card,
  Descriptions,
  Empty,
  Result,
  Select,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  clearAfterPermanentRemoval,
  invalidateAfterMemoryChange,
} from '../../shared/api/cache-effects';
import { errorMessage, isApiError, knownCodeMessage } from '../../shared/api/errors';
import type { ForgetResponse, Memory, Revision } from '../../shared/api/types';
import { revisionOperationLabels } from '../../shared/lib/codes';
import { routes, versionParam } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { DangerActionDialog } from '../../shared/ui/DangerActionDialog';
import { IdText } from '../../shared/ui/IdText';
import { QueryState } from '../../shared/ui/QueryState';
import { RelationTable } from '../../shared/ui/RelationTable';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { RemovalSummary } from '../../shared/ui/RemovalSummary';
import {
  deleteMemory,
  forgetMemory,
  purgeMemory,
  restoreMemoryVersion,
  setMemoryState,
} from './api';
import { EvidenceTable } from './components/EvidenceTable';
import { MemoryDiff } from './components/MemoryDiff';
import { MemorySnapshot } from './components/MemorySnapshot';
import { useMemory, useMemoryEvidence, useMemoryHistory, useMemoryRevision } from './queries';

type RemovalOutcome =
  | { kind: 'purge'; id: string; purged: boolean }
  | { kind: 'forget'; response: ForgetResponse };

function historyColumns(
  currentVersion: number,
  onView: (version: number) => void,
  onRestore: (version: number) => void,
): ColumnsType<Revision> {
  return [
    {
      title: '版本',
      dataIndex: 'version',
      width: 80,
      render: (value: number) => (
        <Space size={4}>
          <span>v{value}</span>
          {value === currentVersion ? <Tag color="success">当前</Tag> : null}
        </Space>
      ),
    },
    {
      title: '操作',
      dataIndex: 'operation',
      width: 130,
      render: (value: string) => <Tag>{revisionOperationLabels[value]?.label ?? value}</Tag>,
    },
    {
      title: '原因',
      dataIndex: 'reason',
      render: (value: string | undefined) => value || '—',
    },
    {
      title: '记录时间',
      dataIndex: 'created_at',
      width: 180,
      render: (value: string) => (
        <span title={timestampTitle(value)}>{formatTimestamp(value)}</span>
      ),
    },
    {
      title: '操作',
      key: 'actions',
      width: 190,
      render: (_, item) => (
        <Space>
          <Button size="small" onClick={() => onView(item.version)}>
            查看
          </Button>
          {item.version !== currentVersion ? (
            <Button size="small" danger onClick={() => onRestore(item.version)}>
              恢复为新版本
            </Button>
          ) : null}
        </Space>
      ),
    },
  ];
}

export default function MemoryDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const version = versionParam(searchParams.get('version'));
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();

  const memory = useMemory(id);
  const revision = useMemoryRevision(id, version);
  const [activeTab, setActiveTab] = useState('snapshot');
  const [historyLimit, setHistoryLimit] = useState(50);
  const [restoreError, setRestoreError] = useState<unknown>(null);
  const [removalOutcome, setRemovalOutcome] = useState<RemovalOutcome | null>(null);
  const [purgeOpen, setPurgeOpen] = useState(false);
  const [forgetOpen, setForgetOpen] = useState(false);

  useEffect(() => {
    setActiveTab('snapshot');
  }, [version]);

  const history = useMemoryHistory(id, historyLimit, activeTab === 'history');
  const evidence = useMemoryEvidence(id, null, activeTab === 'evidence' && version === null);

  const stateMutation = useMutation({
    mutationFn: (state: 'active' | 'archived') => setMemoryState(id, state),
    onSuccess: async () => {
      await invalidateAfterMemoryChange(queryClient);
      message.success('保存状态已更新');
    },
  });
  const deleteMutation = useMutation({
    mutationFn: () => deleteMemory(id),
    onSuccess: async () => {
      await invalidateAfterMemoryChange(queryClient);
      message.success('记忆已删除，仍可通过历史版本恢复');
    },
  });
  const restoreMutation = useMutation({
    mutationFn: (target: number) => restoreMemoryVersion(id, target),
    onSuccess: async () => {
      setRestoreError(null);
      await invalidateAfterMemoryChange(queryClient);
      setSearchParams(new URLSearchParams(), { replace: true });
      setActiveTab('snapshot');
      message.success('已从历史恢复为新版本，不会覆盖原有历史');
    },
    onError: (error) => setRestoreError(error),
  });
  const purgeMutation = useMutation({
    mutationFn: () => purgeMemory(id),
    onSuccess: async (data) => {
      await clearAfterPermanentRemoval(queryClient, { memoryId: id });
      setRemovalOutcome({ kind: 'purge', id: data.id, purged: data.purged });
    },
  });
  const forgetMutation = useMutation({
    mutationFn: () => forgetMemory(id),
    onSuccess: async (data) => {
      await clearAfterPermanentRemoval(queryClient, {
        memoryId: id,
        sourceIds: data.source_ids,
        experienceIds: data.experience_ids,
      });
      setRemovalOutcome({ kind: 'forget', response: data });
    },
  });

  const current = memory.data;
  const selected = revision.data;
  const shown: Memory | undefined = version !== null ? selected?.snapshot : current;
  const title = shown?.title || '记忆详情';

  const openStateConfirm = (state: 'active' | 'archived') => {
    modal.confirm({
      title: state === 'archived' ? '归档这条记忆？' : '激活这条记忆？',
      content:
        state === 'archived'
          ? '归档会保留内容与历史，但不再用于默认搜索；派生内容可能需要更新。'
          : '重新启用后，该记忆可以再次用于默认搜索。',
      okText: state === 'archived' ? '归档' : '激活',
      cancelText: '取消',
      onOk: async () => {
        try {
          await stateMutation.mutateAsync(state);
        } catch (error) {
          message.error(errorMessage(error));
          throw error;
        }
      },
    });
  };

  const openDeleteConfirm = () => {
    modal.confirm({
      title: '删除这条记忆？',
      content:
        '删除后不再用于搜索，但仍保留历史与来源依据，可以通过历史版本恢复。这不是永久删除。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        try {
          await deleteMutation.mutateAsync();
        } catch (error) {
          message.error(errorMessage(error));
          throw error;
        }
      },
    });
  };

  const openRestoreConfirm = (targetVersion: number) => {
    modal.confirm({
      title: `从历史版本 v${targetVersion} 恢复？`,
      content:
        '恢复会建立一个新的使用中版本，不覆盖历史，也不会自动采纳之前未采纳的内容。',
      okText: '确认恢复',
      cancelText: '取消',
      onOk: async () => {
        try {
          await restoreMutation.mutateAsync(targetVersion);
        } catch {
          // The page shows the explicit guidance after the modal closes.
        }
      },
    });
  };

  const viewVersion = (targetVersion: number) => {
    setSearchParams({ version: String(targetVersion) }, { replace: true });
    setActiveTab('snapshot');
  };

  const revisionItems = (() => {
    if (!shown) {
      return [];
    }
    return [
      {
        key: 'snapshot',
        label: version !== null ? `历史内容 · 第 ${version} 版` : '当前视图',
        children: (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {version !== null && selected ? (
              <>
                <Alert
                  type="warning"
                  showIcon
                  message={`正在查看历史版本 v${selected.version}，它不是当前视图`}
                  description={
                    <Space direction="vertical" size={2}>
                      <span>
                        操作：{revisionOperationLabels[selected.operation]?.label ?? selected.operation}
                        {selected.reason ? ` · 原因：${selected.reason}` : ''}
                      </span>
                      <span>记录时间：{formatTimestamp(selected.created_at)}</span>
                      <span>当前版本为 v{current?.version ?? '未知'}；历史版本只读。</span>
                    </Space>
                  }
                  action={
                    <Button
                      size="small"
                      onClick={() => {
                        setSearchParams(new URLSearchParams(), { replace: true });
                      }}
                    >
                      回到当前
                    </Button>
                  }
                />
                <MemorySnapshot memory={selected.snapshot} />
                <Card size="small" title="该版本证据">
                  <EvidenceTable items={selected.evidence} />
                </Card>
              </>
            ) : (
              <MemorySnapshot memory={shown} />
            )}
            {version === null ? (
              <>
                <Card title="管理这条记忆" size="small">
                  <Space wrap>
                    {shown.state === 'active' ? (
                      <Button onClick={() => openStateConfirm('archived')} loading={stateMutation.isPending}>
                        归档
                      </Button>
                    ) : null}
                    {shown.state === 'archived' ? (
                      <Button type="primary" onClick={() => openStateConfirm('active')} loading={stateMutation.isPending}>
                        激活
                      </Button>
                    ) : null}
                    {shown.state !== 'deleted' ? (
                      <Button danger onClick={openDeleteConfirm} loading={deleteMutation.isPending}>
                        删除
                      </Button>
                    ) : (
                      <Alert
                        type="info"
                        showIcon
                        message="这条记忆已删除"
                        description="需要找回时，可在“历史版本”中选择删除前的内容并恢复。"
                      />
                    )}
                    <Button
                      onClick={() =>
                        navigate(routes.newIngestion, {
                          state: {
                            memoryContext: {
                              id: shown.id,
                              version: shown.version,
                              title: shown.title,
                              content: shown.content,
                              scope_key: shown.scope_key,
                            },
                          },
                        })
                      }
                    >
                      提交新内容
                    </Button>
                  </Space>
                  <TechnicalDetails label="永久删除（不可恢复）">
                    <Space wrap>
                      <Button danger onClick={() => setPurgeOpen(true)}>永久清除这条记忆</Button>
                      <Button danger onClick={() => setForgetOpen(true)}>永久遗忘及相关内容</Button>
                    </Space>
                    <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>
                      单条清除会保留共享来源；永久遗忘可能连同来源和相关系列内容一起删除。无法提前精确预览全部影响。
                    </Typography.Paragraph>
                  </TechnicalDetails>
                </Card>
              </>
            ) : null}
          </Space>
        ),
      },
      {
        key: 'history',
        label: '历史版本',
        children: (
          <>
            <Space style={{ marginBottom: 12 }} wrap>
              <Typography.Text>历史条数上限</Typography.Text>
              <Select
                value={historyLimit}
                style={{ width: 130 }}
                options={[20, 50, 100, 200].map((value) => ({
                  value,
                  label: `最多 ${value} 条`,
                }))}
                onChange={setHistoryLimit}
              />
            </Space>
            <QueryState query={history} empty="没有历史版本" isEmpty={(data) => data.length === 0}>
              {(items) => (
                <Table<Revision>
                  rowKey="version"
                  size="small"
                  columns={historyColumns(current?.version ?? 0, viewVersion, openRestoreConfirm)}
                  dataSource={items}
                  pagination={false}
                  scroll={{ x: 900 }}
                />
              )}
            </QueryState>
          </>
        ),
      },
      {
        key: 'evidence',
        label: '来源依据',
        children:
          version !== null ? (
            selected ? (
              <EvidenceTable items={selected.evidence} />
            ) : (
              <Spin tip="正在读取历史版本证据…" />
            )
          ) : (
            <QueryState query={evidence} empty="该记忆当前没有证据" isEmpty={(data) => data.length === 0}>
              {(items) => <EvidenceTable items={items} />}
            </QueryState>
          ),
      },
      {
        key: 'references',
        label: '观察来源与派生',
        children: current ? (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            <Card size="small" title={`观察来源（${current.observation_ids.length}）`}>
              {current.observation_ids.length > 0 ? (
                <Space direction="vertical">
                  {current.observation_ids.map((observationID) => (
                    <Link key={observationID} to={routes.observation(observationID)}>
                      <Typography.Text code>{observationID}</Typography.Text>
                    </Link>
                  ))}
                </Space>
              ) : (
                <Empty description="当前版本没有观察来源引用" image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </Card>
            <Card size="small" title={`关系（${current.relations.length}）`}>
              <RelationTable items={current.relations} />
            </Card>
            <Card size="small" title={`未决冲突（${current.conflict_ids.length}）`}>
              {current.conflict_ids.length > 0 ? (
                <Space direction="vertical">
                  {current.conflict_ids.map((conflictID) => (
                    <Link key={conflictID} to={routes.conflict(conflictID)}>
                      查看冲突 {conflictID}
                    </Link>
                  ))}
                </Space>
              ) : (
                <Empty description="没有未决冲突" image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </Card>
            <Card size="small" title={`依赖此记忆的派生内容（${current.derived_dependents.length}）`}>
              {current.derived_dependents.length > 0 ? (
                <Space direction="vertical">
                  {current.derived_dependents.map((item) => (
                    <Space key={item.id} wrap>
                      <Tag>{item.layer.toUpperCase()}</Tag>
                      <Link to={routes.artifact(item.id)}>{item.kind}</Link>
                      <Typography.Text type="secondary">
                        {item.state} · v{item.version}
                      </Typography.Text>
                    </Space>
                  ))}
                </Space>
              ) : (
                <Empty description="没有直接派生依赖" image={Empty.PRESENTED_IMAGE_SIMPLE} />
              )}
            </Card>
          </Space>
        ) : (
          <Empty description="当前版本尚未读取" />
        ),
      },
      ...(version !== null
        ? [
            {
              key: 'diff',
              label: '版本差异',
              children:
                current && selected ? (
                  <MemoryDiff current={current} historical={selected.snapshot} />
                ) : (
                  <Empty description="当前版本与历史版本都读取后才能对照" />
                ),
            },
          ]
        : []),
    ];
  })();

  if (removalOutcome) {
    const forgetResult = removalOutcome.kind === 'forget' ? removalOutcome.response : null;
    return (
      <Result
        status="success"
        title={removalOutcome.kind === 'purge' ? '永久清除已由服务端确认' : '永久遗忘已由服务端确认'}
        subTitle="页面缓存已保守清除；以下以服务端实际返回为准。"
        extra={
          <Link to={routes.memories}>
            <Button>返回记忆库</Button>
          </Link>
        }
      >
        {forgetResult ? (
          <RemovalSummary result={forgetResult} />
        ) : (
          <Typography.Paragraph>这条记忆及其专属数据已永久清除，共享来源保留。</Typography.Paragraph>
        )}
        <TechnicalDetails label="删除记录详情">
          <Descriptions bordered size="small" column={1}>
            <Descriptions.Item label="目标记忆 ID"><IdText value={id} /></Descriptions.Item>
            {removalOutcome.kind === 'purge' ? (
              <Descriptions.Item label="purged">{String(removalOutcome.purged)}</Descriptions.Item>
            ) : null}
            {forgetResult ? (
              <>
                <Descriptions.Item label="实际删除记忆 ID">
                  {forgetResult.memory_ids.length > 0 ? forgetResult.memory_ids.join('、') : '无'}
                </Descriptions.Item>
                <Descriptions.Item label="实际删除来源 ID">
                  {forgetResult.source_ids.length > 0 ? forgetResult.source_ids.join('、') : '无'}
                </Descriptions.Item>
                <Descriptions.Item label="实际删除报告 ID">
                  {forgetResult.experience_ids.length > 0 ? forgetResult.experience_ids.join('、') : '无'}
                </Descriptions.Item>
              </>
            ) : null}
          </Descriptions>
        </TechnicalDetails>
      </Result>
    );
  }

  if (memory.isError && memory.data === undefined) {
    if (isApiError(memory.error) && memory.error.status === 404) {
      return (
        <Result
          status="404"
          title="记忆不存在或已被永久清除"
          subTitle="不能从缓存伪造快照。请返回记忆库重新查找。"
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
        message="记忆详情读取失败"
        description={errorMessage(memory.error)}
        action={<Button onClick={() => void memory.refetch()}>重试</Button>}
      />
    );
  }

  if (memory.isPending && !current) {
    return <Spin tip="正在读取记忆详情…" />;
  }

  return (
    <>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} align="start" wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            {title}
          </Typography.Title>
          <Space wrap>
            {shown ? <Tag>v{shown.version}</Tag> : null}
            {version !== null ? <Tag color="warning">历史视图</Tag> : <Tag color="success">当前视图</Tag>}
          </Space>
        </div>
        <Space wrap>
          <Link to={routes.memories}><Button>返回列表</Button></Link>
          {version !== null ? (
            <Button onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}>
              回到当前
            </Button>
          ) : null}
          <Button onClick={() => void memory.refetch()} loading={memory.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      {restoreError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={isApiError(restoreError) && restoreError.status === 409 ? 'warning' : 'error'}
          showIcon
          message="恢复历史版本失败"
          description={
            knownCodeMessage(isApiError(restoreError) ? restoreError.code : undefined) ??
            errorMessage(restoreError)
          }
          closable
          onClose={() => setRestoreError(null)}
        />
      ) : null}

      {version !== null && revision.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message={`历史版本 v${version} 读取失败`}
          description={errorMessage(revision.error)}
          action={<Button size="small" onClick={() => void revision.refetch()}>重试</Button>}
        />
      ) : null}

      {version !== null && !selected && !revision.isError ? (
        <Spin tip={`正在读取历史版本 v${version}；不会用当前版本替代…`}>
          <div style={{ minHeight: 200 }} />
        </Spin>
      ) : version !== null && revision.isError ? (
        <Empty description="无法展示未成功读取的历史版本" />
      ) : (
        <Tabs activeKey={activeTab} onChange={setActiveTab} items={revisionItems} destroyOnHidden={false} />
      )}

      <DangerActionDialog
        open={purgeOpen}
        title="永久清除这条记忆？"
        targetType="单条记忆"
        targetId={id}
        targetName={title}
        confirmText={id}
        confirmLabel="永久清除"
        impacts="永久清除这条记忆、全部历史和专属来源依据；共享来源保留。无法提前精确预览全部影响，删除后不能恢复。"
        onClose={() => setPurgeOpen(false)}
        action={() => purgeMutation.mutateAsync()}
        onSuccess={(result) => {
          setPurgeOpen(false);
          setRemovalOutcome({ kind: 'purge', id: result.id, purged: result.purged });
        }}
      />

      <DangerActionDialog
        open={forgetOpen}
        title="永久遗忘这条记忆？"
        targetType="记忆及相关内容"
        targetId={id}
        targetName={title}
        confirmText={id}
        confirmLabel="永久遗忘"
        impacts="可能同时删除相关来源和自动整理内容。无法提前精确列出全部影响；完成后会显示实际删除结果。此操作不可恢复。"
        onClose={() => setForgetOpen(false)}
        action={() => forgetMutation.mutateAsync()}
        onSuccess={(result) => {
          setForgetOpen(false);
          setRemovalOutcome({ kind: 'forget', response: result });
        }}
      />
    </>
  );
}
