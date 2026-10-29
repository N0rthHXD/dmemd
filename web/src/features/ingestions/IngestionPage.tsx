import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  Radio,
  Row,
  Space,
  Tag,
  Typography,
} from 'antd';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { isRecord } from '../../shared/api/client';
import { errorMessage, isApiError } from '../../shared/api/errors';
import { recordSessionIngestion } from '../../shared/api/session-receipts';
import type { SubmitResponse } from '../../shared/api/types';
import { routes } from '../../shared/lib/routes';
import { IdText } from '../../shared/ui/IdText';
import { StatusTag } from '../../shared/ui/StatusTag';
import { UnsavedChangesGuard } from '../../shared/ui/UnsavedChangesGuard';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { submitIngestion } from './api';

interface FormValues {
  text?: string;
  appId?: string;
  externalId?: string;
  scopeKey?: string;
}

interface MemoryContext {
  id: string;
  version: number | null;
  title: string;
  content: string;
  scopeKey?: string;
}

function readMemoryContext(state: unknown): MemoryContext | null {
  if (!isRecord(state) || !isRecord(state.memoryContext)) {
    return null;
  }
  const context = state.memoryContext;
  if (typeof context.id !== 'string') {
    return null;
  }
  return {
    id: context.id,
    version: typeof context.version === 'number' ? context.version : null,
    title: typeof context.title === 'string' ? context.title : '',
    content: typeof context.content === 'string' ? context.content : '',
    scopeKey: typeof context.scope_key === 'string' ? context.scope_key : undefined,
  };
}

export default function IngestionPage() {
  const [form] = Form.useForm<FormValues>();
  const [mode, setMode] = useState<'text' | 'json'>('text');
  const [dirty, setDirty] = useState(false);
  const [parseError, setParseError] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<SubmitResponse | null>(null);
  const location = useLocation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const memoryContext = readMemoryContext(location.state);

  const mutation = useMutation({
    mutationFn: submitIngestion,
    onSuccess: (data) => {
      setReceipt(data);
      setDirty(false);
      setParseError(null);
      form.setFieldsValue({ text: '' });
      recordSessionIngestion(queryClient, {
        source_id: data.source_id,
        job_id: data.job_id,
        status: data.status,
      });
      message.success('服务端已接收，后台任务尚未完成');
    },
  });

  const submit = (values: FormValues) => {
    const raw = values.text ?? '';
    let content: unknown;
    if (mode === 'json') {
      try {
        content = JSON.parse(raw);
      } catch (error) {
        setParseError(
          `JSON 解析失败，未发送请求：${error instanceof Error ? error.message : '格式无效'}`,
        );
        return;
      }
    } else {
      if (!raw.trim()) {
        setParseError('文本内容不能为空，未发送请求。');
        return;
      }
      content = raw;
    }
    setParseError(null);
    setReceipt(null);
    setDirty(true);
    mutation.mutate({
      content,
      appId: values.appId,
      externalId: values.externalId,
      scopeKey: values.scopeKey,
    });
  };

  const uncertain = mutation.isError &&
    isApiError(mutation.error) &&
    (mutation.error.kind === 'timeout' || mutation.error.kind === 'network');

  return (
    <>
      <UnsavedChangesGuard when={dirty} />
      <Typography.Title level={3}>{memoryContext ? '更正记忆' : '添加内容'}</Typography.Title>
      <Typography.Paragraph type="secondary">
        写下想记住的内容。系统会先保存来源，再在后台提取 L0 原始观察，随后异步治理为当前记忆。提交后可继续查看处理进展。
      </Typography.Paragraph>

      {memoryContext ? (
        <Card title="原记忆上下文（仅作参考）" style={{ marginBottom: 16 }}>
          <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
            <Descriptions.Item label="原记忆">
              <Space direction="vertical" size={2}>
                <Link
                  to={
                    memoryContext.version
                      ? routes.memoryVersion(memoryContext.id, memoryContext.version)
                      : routes.memory(memoryContext.id)
                  }
                >
                  打开原记忆
                </Link>
              </Space>
            </Descriptions.Item>
            <Descriptions.Item label="版本">
              {memoryContext.version ? `v${memoryContext.version}` : '未知'}
            </Descriptions.Item>
            <Descriptions.Item label="标题">{memoryContext.title || '（无标题）'}</Descriptions.Item>
            <Descriptions.Item label="所属范围">{memoryContext.scopeKey || '未指定'}</Descriptions.Item>
            <Descriptions.Item label="正文" span={2}>
              <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', marginBottom: 0 }}>
                {memoryContext.content}
              </Typography.Paragraph>
            </Descriptions.Item>
          </Descriptions>
          <Alert
            style={{ marginTop: 12 }}
            type="warning"
            showIcon
            message="更正内容会重新提取和治理，不会直接覆盖原记忆"
            description="请说明准确的信息和适用条件。系统会先形成新的原始观察，再由治理阶段决定新增、补充或形成冲突，请以处理结果为准。"
          />
        </Card>
      ) : null}

      <Row gutter={[16, 16]}>
        <Col xs={24} xl={15}>
          <Card title="提交内容">
            <Form<FormValues>
              form={form}
              layout="vertical"
              onFinish={() => submit(form.getFieldsValue(true))}
              onValuesChange={() => setDirty(true)}
            >
              <TechnicalDetails label="高级输入格式"><Form.Item label="输入格式">
                <Radio.Group
                  value={mode}
                  onChange={(event) => {
                    setMode(event.target.value as 'text' | 'json');
                    setDirty(true);
                    setParseError(null);
                  }}
                  options={[
                    { value: 'text', label: '文本' },
                    { value: 'json', label: 'JSON' },
                  ]}
                />
              </Form.Item></TechnicalDetails>
              <Form.Item
                label={mode === 'text' ? '要记住的内容' : '结构化内容（JSON）'}
                name="text"
                extra={
                  mode === 'text'
                    ? '尽量说明具体的人、事、时间与适用条件，方便以后准确查找。'
                    : '请填写合法的 JSON 内容，格式有误时不会提交。'
                }
              >
                <Input.TextArea
                  rows={12}
                  placeholder={
                    mode === 'text'
                      ? '例如：以后请默认使用中文回答。'
                      : '例如：{"messages":[{"role":"user","content":"..."}]}'
                  }
                />
              </Form.Item>
              {parseError ? (
                <Alert
                  type="error"
                  showIcon
                  message="内容格式错误"
                  description={parseError}
                  style={{ marginBottom: 12 }}
                />
              ) : null}

              <Form.Item label="所属范围（可选）" name="scopeKey" extra="例如某个项目或生活场景；不确定时可以留空。"><Input placeholder="例如 project:dmemd" allowClear /></Form.Item>
              {mode === 'text' ? <Space wrap style={{ marginBottom: 20 }}><Typography.Text type="secondary">试试这些示例：</Typography.Text>{['以后请默认使用中文回答。', '项目例会安排在每周三下午三点。', '修改项目后，先运行检查，再运行测试。'].map((example, index) => <Button size="small" key={example} onClick={() => { form.setFieldsValue({ text: example }); setDirty(true); }}>{['个人偏好', '计划安排', '操作步骤'][index]}</Button>)}</Space> : null}
              <TechnicalDetails label="更多信息"><Row gutter={12}>
                <Col xs={24} md={8}>
                  <Form.Item label="来源应用（可选）" name="appId">
                    <Input placeholder="来源应用名，非认证" allowClear />
                  </Form.Item>
                </Col>
                <Col xs={24} md={8}>
                  <Form.Item label="外部记录编号（可选）" name="externalId">
                    <Input placeholder="外部引用，无唯一性约束" allowClear />
                  </Form.Item>
                </Col>
              </Row></TechnicalDetails>

              <Space wrap>
                <Button
                  type="primary"
                  htmlType="submit"
                  loading={mutation.isPending}
                  disabled={mutation.isPending}
                >
                  {mutation.isPending ? '正在提交…' : '提交内容'}
                </Button>
                <Typography.Text type="secondary">
                  提交期间请勿重复操作。
                </Typography.Text>
              </Space>
            </Form>

            {mutation.isError ? (
              <Alert
                style={{ marginTop: 16 }}
                type={uncertain ? 'warning' : 'error'}
                showIcon
                message={uncertain ? '提交结果未知' : '提交失败'}
                description={
                  uncertain
                    ? '超时或断网可能发生在服务端接收之后。不要自动重发；请先按来源或任务 ID 查询（若已知），再自行决定是否重新提交。'
                    : errorMessage(mutation.error)
                }
              />
            ) : null}
          </Card>
        </Col>
        <Col xs={24} xl={9}>
          {receipt ? (
            <Card title={dirty ? "上次已接收回执（当前表单尚未提交）" : "已接收回执"} style={{ marginBottom: 16 }}>
              <Space direction="vertical" size={10} style={{ width: '100%' }}>
                <Space>
                  <Tag color="success">已接收</Tag>
                  <StatusTag category="jobStatus" value={receipt.status} />
                </Space>
                <Typography.Paragraph>已收到内容，提取任务已排队。处理完成后，可查看保存的 L0 原始观察和后续治理状态。</Typography.Paragraph>
                <TechnicalDetails label="保存回执编号"><Descriptions size="small" column={1} bordered>
                  <Descriptions.Item label="来源 ID">
                    <IdText value={receipt.source_id} maxWidth={300} />
                  </Descriptions.Item>
                  <Descriptions.Item label="任务 ID">
                    <IdText value={receipt.job_id} maxWidth={300} />
                  </Descriptions.Item>
                </Descriptions></TechnicalDetails>
                <Space wrap>
                  <Link to={routes.job(receipt.job_id)}>
                    <Button type="primary">查看处理进度</Button>
                  </Link>
                  <Link to={routes.source(receipt.source_id)}>
                    <Button>查看来源</Button>
                  </Link>
                </Space>
                <Alert
                  type="info"
                  showIcon
                  message="已接收不代表已形成当前记忆"
                  description="系统会先形成原始观察，再由治理阶段决定是否形成或更新当前记忆。"
                />
              </Space>
            </Card>
          ) : null}
          <Card title="提交后会发生什么？">
            <Typography.Paragraph>
              1. 接收内容，保存不可变来源和稳定 span。<br />
              2. 提取阶段只形成 L0 原始观察，不读取已有记忆。<br />
              3. 治理阶段异步形成当前记忆；聚合阶段再逐层生成 L1-L4 派生内容。
            </Typography.Paragraph>
            <Typography.Paragraph>
              本次打开页面期间的提交会出现在处理记录中。刷新页面前，建议保存处理详情链接，方便稍后查看。
            </Typography.Paragraph>
            <Link to={routes.jobs}>查看处理记录 →</Link>
          </Card>
        </Col>
      </Row>
    </>
  );
}
