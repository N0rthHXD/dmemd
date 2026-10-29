import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Select,
  Space,
  Typography,
} from 'antd';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useSessionExperiences, recordSessionExperience } from '../../../shared/api/session-receipts';
import { isRecord } from '../../../shared/api/client';
import { clearAfterPermanentRemoval, invalidateAfterExperienceChange } from '../../../shared/api/cache-effects';
import { errorMessage } from '../../../shared/api/errors';
import type {
  ExperienceReceipt,
  ForgetResponse,
} from '../../../shared/api/types';
import { experienceOutcomeLabels } from '../../../shared/lib/codes';
import { formatTimestamp } from '../../../shared/lib/time';
import { DangerActionDialog } from '../../../shared/ui/DangerActionDialog';
import { IdText } from '../../../shared/ui/IdText';
import { StatusTag } from '../../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../../shared/ui/TechnicalDetails';
import { RemovalSummary } from '../../../shared/ui/RemovalSummary';
import { forgetExperience, retractExperience, submitExperience } from '../api';

interface SkillContext {
  id: string;
  version?: number;
}

interface ReportFormValues {
  goal?: string;
  conditions?: Array<{ value?: string }>;
  actions?: Array<{ value?: string }>;
  outcome?: 'success' | 'failure' | 'unknown' | 'partial_success';
  runId?: string;
  appId?: string;
  externalId?: string;
  scopeKey?: string;
  skillId?: string;
  skillVersion?: number | null;
  content?: string;
}

function receiptCard(receipt: ExperienceReceipt) {
  return (
    <Descriptions size="small" bordered column={1}>
      <Descriptions.Item label="报告 ID">
        <IdText value={receipt.id} maxWidth={300} />
      </Descriptions.Item>
      <Descriptions.Item label="来源 ID">
        <IdText value={receipt.source_id} maxWidth={300} />
      </Descriptions.Item>
      <Descriptions.Item label="提交去重编号">{receipt.run_id ? <IdText value={receipt.run_id} /> : '未提供'}</Descriptions.Item>
      <Descriptions.Item label="结果">
        <StatusTag category="experienceOutcome" value={receipt.outcome} />
      </Descriptions.Item>
      <Descriptions.Item label="状态">
        <StatusTag category="experienceState" value={receipt.state} />
      </Descriptions.Item>
      <Descriptions.Item label="创建 / 更新">
        {formatTimestamp(receipt.created_at)} / {formatTimestamp(receipt.updated_at)}
      </Descriptions.Item>
    </Descriptions>
  );
}

export function ReportActions({ skillContext }: { skillContext?: SkillContext }) {
  const { message, notification } = App.useApp();
  const queryClient = useQueryClient();
  const sessionExperiences = useSessionExperiences();
  const [submitOpen, setSubmitOpen] = useState(false);
  const [retractOpen, setRetractOpen] = useState(false);
  const [forgetOpen, setForgetOpen] = useState(false);
  const [receipt, setReceipt] = useState<ExperienceReceipt | null>(null);
  const [retractResult, setRetractResult] = useState<ExperienceReceipt | null>(null);
  const [forgetId, setForgetId] = useState('');
  const [forgetResult, setForgetResult] = useState<ForgetResponse | null>(null);
  const [selectedRetractId, setSelectedRetractId] = useState<string | undefined>();
  const [manualRetractId, setManualRetractId] = useState('');
  const [form] = Form.useForm<ReportFormValues>();

  useEffect(() => {
    if (!submitOpen) {
      return;
    }
    form.resetFields();
    form.setFieldsValue({
      outcome: 'unknown',
      skillId: skillContext?.id,
      skillVersion: skillContext?.version ?? null,
    });
    setReceipt(null);
  }, [submitOpen, skillContext?.id, skillContext?.version, form]);

  const submitMutation = useMutation({
    mutationFn: submitExperience,
    onSuccess: (data) => {
      recordSessionExperience(queryClient, data);
      setReceipt(data);
      message.success('应用结果已记录；它仍是应用报告，不是独立执行验证');
      void invalidateAfterExperienceChange(queryClient);
    },
  });

  const retractMutation = useMutation({
    mutationFn: retractExperience,
    onSuccess: (data) => {
      recordSessionExperience(queryClient, data);
      setRetractResult(data);
      message.success('报告已撤销，依赖内容会按服务端规则失效');
      void invalidateAfterExperienceChange(queryClient);
    },
  });

  const forgetMutation = useMutation({
    mutationFn: () => forgetExperience(forgetId),
    onSuccess: async (data) => {
      setForgetResult(data);
      await clearAfterPermanentRemoval(queryClient, {
        experienceId: forgetId,
        sourceIds: data.source_ids,
        experienceIds: data.experience_ids,
      });
      if (selectedRetractId === forgetId) {
        setSelectedRetractId(undefined);
      }
      if (manualRetractId.trim() === forgetId) {
        setManualRetractId('');
      }
      setRetractResult(null);
      setForgetOpen(false);
      notification.warning({
        message: '应用报告永久遗忘已由服务端确认',
        description: [
          `删除报告 ${data.experience_ids.length} 项`,
          `删除来源 ${data.source_ids.length} 项`,
          `删除记忆 ${data.memory_ids.length} 项`,
        ].join('；'),
        duration: 0,
      });
    },
  });

  const submitReport = (values: ReportFormValues) => {
    if (!values.goal?.trim()) {
      return;
    }
    let content: Record<string, unknown> | undefined;
    if (values.content?.trim()) {
      try {
        const parsed: unknown = JSON.parse(values.content);
        if (!isRecord(parsed)) {
          message.error('可选 content 必须是 JSON 对象');
          return;
        }
        content = parsed;
      } catch (error) {
        message.error(`content JSON 解析失败：${error instanceof Error ? error.message : '格式无效'}`);
        return;
      }
    }
    setReceipt(null);
    submitMutation.mutate({
      goal: values.goal.trim(),
      conditions: (values.conditions ?? [])
        .map((row) => row.value?.trim() ?? '')
        .filter(Boolean),
      actions: (values.actions ?? []).map((row) => row.value?.trim() ?? '').filter(Boolean),
      outcome: values.outcome ?? 'unknown',
      run_id: values.runId?.trim() || undefined,
      app_id: values.appId?.trim() || undefined,
      external_id: values.externalId?.trim() || undefined,
      scope_key: values.scopeKey?.trim() || undefined,
      skill_id: values.skillId?.trim() || undefined,
      skill_version: values.skillVersion ?? undefined,
      content,
    });
  };

  const retractId = manualRetractId.trim() || selectedRetractId || '';
  const recordedReceipts = (sessionExperiences.data ?? []).filter(
    (item) => item.state === 'recorded',
  );

  return (
    <>
      <Space wrap>
        <Button onClick={() => setSubmitOpen(true)}>
          反馈使用结果
        </Button>
        <Button
          onClick={() => {
            setRetractResult(null);
            setRetractOpen(true);
          }}
        >
          撤销已知报告
        </Button>
      </Space>

      <Alert
        style={{ marginTop: 12 }}
        type="info"
        showIcon
        message="应用报告不是独立执行验证"
        description="成功或失败均为提交者报告的结果，不代表已经独立验证。相同去重编号只返回原来的回执，不覆盖或恢复报告。"
      />

      {forgetResult ? (
        <Card size="small" title="永久遗忘报告结果" style={{ marginTop: 12 }}>
          <RemovalSummary result={forgetResult} />
          <TechnicalDetails label="原始删除记录"><Descriptions size="small" column={1} bordered>
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
        </Card>
      ) : null}

      <Modal
        open={submitOpen}
        title="反馈使用结果"
        width={820}
        footer={null}
        onCancel={() => setSubmitOpen(false)}
        destroyOnHidden
      >
        <Alert
          type="info"
          showIcon
          message="这不是执行验证"
          description="报告会保留为来源，并用于后台整理经验案例，不会直接变成记忆。重复提交相同去重编号时，只返回原来的报告。"
          style={{ marginBottom: 16 }}
        />
        <Form<ReportFormValues>
          form={form}
          layout="vertical"
          initialValues={{ outcome: 'unknown' }}
          onFinish={() => submitReport(form.getFieldsValue(true))}
        >
          <Form.Item
            label="这次要完成什么（必填）"
            name="goal"
            rules={[{ required: true, message: '请填写应用目标' }]}
          >
            <Input placeholder="例如：修改 dmemd 后验证" />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} md={8}>
              <Form.Item label="实际结果" name="outcome">
                <Select
                  options={Object.entries(experienceOutcomeLabels).map(([value, definition]) => ({
                    value,
                    label: definition.label,
                  }))}
                />
              </Form.Item>
            </Col>
          </Row>
          <TechnicalDetails label="更多信息与指南关联"><Row gutter={12}>
            <Col xs={24} md={8}>
              <Form.Item label="提交去重编号（可选）" name="runId">
                <Input placeholder="重复返回旧回执" />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="所属范围（可选）" name="scopeKey">
                <Input placeholder="不确定时可留空" />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="来源应用（可选）" name="appId">
                <Input />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="外部记录编号（可选）" name="externalId">
                <Input />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item
                label="操作指南编号（可选）"
                name="skillId"
                extra="如从指南进入，请确认已预填的关联信息。"
              >
                <Input />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="操作指南版本（可选）" name="skillVersion">
                <InputNumber min={0} style={{ width: '100%' }} placeholder="不隐式使用最新版本" />
              </Form.Item>
            </Col>
            <Col xs={24} md={16}>
              <Form.Item
                label="附加资料（可选 JSON 对象）"
                name="content"
                extra="只接受合法 JSON 对象；不会按标点拆分用户内容。"
              >
                <Input.TextArea rows={3} placeholder='例如：{"log":"..."}' />
              </Form.Item>
            </Col>
          </Row></TechnicalDetails>

          <Form.List name="conditions">
            {(fields, { add, remove }) => (
              <div style={{ marginBottom: 16 }}>
                <Typography.Text strong>适用条件</Typography.Text>
                {fields.map((field, index) => (
                  <Space key={field.key} style={{ display: 'flex', marginTop: 8 }}>
                    <Form.Item name={[field.name, 'value']} style={{ marginBottom: 0, flex: 1 }}>
                      <Input placeholder={`条件 ${index + 1}`} />
                    </Form.Item>
                    <Button danger onClick={() => remove(field.name)}>
                      删除
                    </Button>
                  </Space>
                ))}
                <Button type="dashed" onClick={() => add({ value: '' })} style={{ marginTop: 8 }}>
                  添加条件
                </Button>
              </div>
            )}
          </Form.List>

          <Form.List name="actions">
            {(fields, { add, remove }) => (
              <div style={{ marginBottom: 16 }}>
                <Typography.Text strong>采取的步骤</Typography.Text>
                {fields.map((field, index) => (
                  <Space key={field.key} style={{ display: 'flex', marginTop: 8 }}>
                    <Form.Item name={[field.name, 'value']} style={{ marginBottom: 0, flex: 1 }}>
                      <Input placeholder={`操作 ${index + 1}`} />
                    </Form.Item>
                    <Button danger onClick={() => remove(field.name)}>
                      删除
                    </Button>
                  </Space>
                ))}
                <Button type="dashed" onClick={() => add({ value: '' })} style={{ marginTop: 8 }}>
                  添加操作
                </Button>
              </div>
            )}
          </Form.List>

          {submitMutation.isError ? (
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 12 }}
              message="报告提交失败，表单已保留"
              description={errorMessage(submitMutation.error)}
            />
          ) : null}
          {receipt ? (
            <div style={{ marginBottom: 12 }}>
              <Alert type="success" showIcon message="使用结果已记录" />
              <TechnicalDetails label="查看报告回执">{receiptCard(receipt)}</TechnicalDetails>
            </div>
          ) : null}
          <Space wrap>
            <Button
              type="primary"
              htmlType="submit"
              loading={submitMutation.isPending}
              disabled={submitMutation.isPending}
            >
              提交报告
            </Button>
            <Button
              disabled={!receipt}
              onClick={() => {
                if (receipt) {
                  setForgetId(receipt.id);
                  setForgetOpen(true);
                }
              }}
            >
              永久遗忘刚提交的报告
            </Button>
          </Space>
        </Form>
      </Modal>

      <Modal
        open={retractOpen}
        title="撤销应用报告"
        footer={null}
        onCancel={() => setRetractOpen(false)}
        destroyOnHidden
      >
        <Alert
          type="info"
          showIcon
          message="选择已有报告，或粘贴报告编号"
          description="请使用提交报告后收到的编号，不要使用经验案例的编号。撤销后，相关整理内容会重新评估。"
          style={{ marginBottom: 16 }}
        />
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Select
            allowClear
            style={{ width: '100%' }}
            placeholder="从本会话报告回执中选择"
            value={selectedRetractId}
            options={recordedReceipts.map((item) => ({
              value: item.id,
              label: `${formatTimestamp(item.created_at)} · ${experienceOutcomeLabels[item.outcome]?.label ?? '结果未知'}`,
            }))}
            onChange={(value: string | undefined) => {
              setSelectedRetractId(value);
              if (value) {
                setManualRetractId('');
              }
            }}
          />
          <Input
            value={manualRetractId}
            placeholder="或手动输入报告 ID"
            onChange={(event) => {
              setManualRetractId(event.target.value);
              if (event.target.value) {
                setSelectedRetractId(undefined);
              }
            }}
          />
          {retractResult ? (
            <Card size="small" title="撤销回执">
              {receiptCard(retractResult)}
            </Card>
          ) : null}
          {retractMutation.isError ? (
            <Alert
              type="error"
              showIcon
              message="撤销失败"
              description={errorMessage(retractMutation.error)}
            />
          ) : null}
          <Space wrap>
            <Button
              type="primary"
              danger
              disabled={!retractId}
              loading={retractMutation.isPending}
              onClick={() => retractMutation.mutate(retractId)}
            >
              确认撤销
            </Button>
            <Button
              disabled={!retractId}
              onClick={() => {
                setForgetId(retractId);
                setForgetOpen(true);
              }}
            >
              永久遗忘这份报告
            </Button>
          </Space>
          <Typography.Text type="secondary">
            撤销是逻辑阻断，不同于永久遗忘；撤销和成功/失败结果都属于应用报告。
          </Typography.Text>
        </Space>
      </Modal>

      <DangerActionDialog
        open={forgetOpen}
        title="永久遗忘这份应用报告？"
        targetType="使用结果报告"
        targetId={forgetId}
        confirmText={forgetId}
        confirmLabel="永久遗忘"
        impacts={
          <Space direction="vertical" size={2}>
            <span>会删除报告、专属来源及相关自动整理内容，影响可能超出这份报告。</span>
            <span>无法提前精确预览全部影响；完成后会显示实际删除结果。</span>
          </Space>
        }
        onClose={() => setForgetOpen(false)}
        action={() => forgetMutation.mutateAsync()}
        onSuccess={(data) => {
          setForgetResult(data);
          setForgetOpen(false);
        }}
      />
    </>
  );
}
