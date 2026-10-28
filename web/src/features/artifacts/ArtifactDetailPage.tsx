import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  InputNumber,
  Result,
  Space,
  Tag,
  Typography,
} from 'antd';
import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { errorMessage, isApiError } from '../../shared/api/errors';
import { reasonText } from '../../shared/lib/codes';
import { routes, versionParam } from '../../shared/lib/routes';
import { formatTimestamp, timestampTitle } from '../../shared/lib/time';
import { IdText } from '../../shared/ui/IdText';
import { JsonView } from '../../shared/ui/JsonView';
import { MarkdownView } from '../../shared/ui/MarkdownView';
import { StatusTag } from '../../shared/ui/StatusTag';
import { ReportActions } from './components/ReportActions';
import { SkillSection } from './components/SkillSection';
import { useArtifactDetail } from './queries';
import { TechnicalDetails } from '../../shared/ui/TechnicalDetails';
import { artifactName } from '../../shared/lib/display';

export default function ArtifactDetailPage() {
  const { id = '' } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const version = versionParam(searchParams.get('version'));
  const [versionDraft, setVersionDraft] = useState<number | null>(version);
  const detail = useArtifactDetail(id, version);

  useEffect(() => {
    setVersionDraft(version);
  }, [version]);

  if (detail.isError && !detail.data) {
    if (isApiError(detail.error) && detail.error.status === 404) {
      return (
        <Result
          status="404"
          title={version !== null ? `第 ${version} 版内容不存在` : '整理内容不存在'}
          subTitle="可能尚未生成，或已被永久清除。请返回整理结果查看。"
          extra={
            <Space>
              <Link to={routes.artifacts}>
                <Button type="primary">返回整理结果</Button>
              </Link>
              {version !== null ? (
                <Button onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}>
                  查看当前版本
                </Button>
              ) : null}
            </Space>
          }
        />
      );
    }
    return (
      <Alert
        type="error"
        showIcon
        message="整理内容读取失败"
        description={errorMessage(detail.error)}
        action={<Button onClick={() => void detail.refetch()}>重试</Button>}
      />
    );
  }

  if (!detail.data) {
    return <Card loading title="正在读取整理内容…" />;
  }

  const { artifact, revision } = detail.data;
  const selectedHistorical = version !== null && revision?.version !== artifact.current_version;

  const goToVersion = () => {
    if (!versionDraft || versionDraft < 1) {
      setSearchParams(new URLSearchParams(), { replace: true });
      return;
    }
    if (versionDraft === artifact.current_version) {
      setSearchParams(new URLSearchParams(), { replace: true });
      return;
    }
    setSearchParams({ version: String(versionDraft) }, { replace: true });
  };

  return (
    <>
      <Space style={{ width: '100%', justifyContent: 'space-between', marginBottom: 8 }} wrap>
        <div>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            {revision?.title || artifactName(artifact)}
          </Typography.Title>
          <Space wrap>
            <StatusTag category="artifactKind" value={artifact.kind} />
            <StatusTag category="artifactState" value={artifact.state} />
            <Tag>当前 v{artifact.current_version}</Tag>
            {version !== null ? <Tag color="warning">历史 v{version}</Tag> : null}
          </Space>
        </div>
        <Space wrap>
          <Link to={routes.artifacts}>
            <Button>返回列表</Button>
          </Link>
          {version !== null ? (
            <Button onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}>
              回到当前
            </Button>
          ) : null}
          <Button onClick={() => void detail.refetch()} loading={detail.isFetching}>
            刷新
          </Button>
        </Space>
      </Space>

      {detail.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message="刷新失败，显示上次成功读取的详情"
          description={errorMessage(detail.error)}
        />
      ) : null}

      {selectedHistorical ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message={`正在查看第 ${revision?.version} 版历史内容；当前状态：${artifact.state === 'active' ? '可用' : artifact.state === 'stale' ? '需要更新' : '已停用'}`}
          action={
            <Button
              size="small"
              onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}
            >
              回到当前
            </Button>
          }
        />
      ) : null}

      <TechnicalDetails label="内容信息与编号"><Card title="内容信息" style={{ marginBottom: 16 }}>
        <Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
          <Descriptions.Item label="ID">
            <IdText value={artifact.id} maxWidth={320} />
          </Descriptions.Item>
          <Descriptions.Item label="类型">
            <StatusTag category="artifactKind" value={artifact.kind} />
          </Descriptions.Item>
           <Descriptions.Item label="内部编号">{artifact.target_key}</Descriptions.Item>
           <Descriptions.Item label="所属范围">{artifact.scope_key || '未指定'}</Descriptions.Item>
          <Descriptions.Item label="主题">{artifact.topic || '未知'}</Descriptions.Item>
          <Descriptions.Item label="状态">
            <StatusTag category="artifactState" value={artifact.state} />
          </Descriptions.Item>
          <Descriptions.Item label="当前版本">
            {artifact.current_version > 0 ? `v${artifact.current_version}` : '尚无内容'}
          </Descriptions.Item>
          <Descriptions.Item label="原因码">
            {artifact.reason_code ? `${reasonText(artifact.reason_code)}（${artifact.reason_code}）` : '无'}
          </Descriptions.Item>
          <Descriptions.Item label="更新时间">
            <span title={timestampTitle(artifact.updated_at)}>{formatTimestamp(artifact.updated_at)}</span>
          </Descriptions.Item>
        </Descriptions>
      </Card></TechnicalDetails>

      <TechnicalDetails label="查看历史版本"><Card
        title="读取指定历史版本"
        size="small"
        style={{ marginBottom: 16 }}
        extra={
          <Space>
            <InputNumber
              min={artifact.current_version > 0 ? 1 : 0}
              max={artifact.current_version || undefined}
              value={versionDraft}
              onChange={(value) => setVersionDraft(typeof value === 'number' ? value : null)}
              placeholder={`1～${artifact.current_version}`}
            />
            <Button onClick={goToVersion} disabled={artifact.current_version === 0}>
              查看版本
            </Button>
            {version !== null ? (
              <Button onClick={() => setSearchParams(new URLSearchParams(), { replace: true })}>
                当前
              </Button>
            ) : null}
          </Space>
        }
      >
        <Typography.Text type="secondary">
          输入版本号查看过去的内容。历史版本仅供回顾，不代表当前仍在使用。
        </Typography.Text>
      </Card></TechnicalDetails>

      {revision ? (
        <>
          {artifact.kind === 'experience_case' ? (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message="经验案例来自应用报告，不是独立执行验证"
            />
          ) : null}
          {artifact.kind === 'skill' ? (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message="操作指南仅供参考，不代表已验证执行成功或安全性"
              description="可以阅读、复制或下载指南，页面不会执行或安装它。"
            />
          ) : null}
          {revision.truncated ? (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 16 }}
              message="这份内容只使用了部分资料"
              description="内容较多时，系统可能只选用其中一部分。可展开技术详情查看选用了哪些资料。"
            />
          ) : null}
          <Card
            title={
              <Space wrap>
                <span>{revision.title || '（无标题）'}</span>
                <Tag>v{revision.version}</Tag>
                {revision.reason_code ? <Tag>{reasonText(revision.reason_code)}</Tag> : null}
              </Space>
            }
          >
            <Typography.Title level={5}>正文</Typography.Title>
            <MarkdownView content={revision.content} />
          </Card>

          <TechnicalDetails label="反馈使用结果"><Card title="使用结果反馈" style={{ marginTop: 16 }}>
            <ReportActions
              skillContext={
                artifact.kind === 'skill'
                  ? { id: artifact.id, version: revision.version }
                  : undefined
              }
            />
          </Card></TechnicalDetails>

          <Card title="来源依据" style={{ marginTop: 16 }}>
            <TechnicalDetails label="生成详情"><Descriptions size="small" bordered column={{ xs: 1, md: 2, xl: 3 }}>
              <Descriptions.Item label="生成器">{revision.generator}</Descriptions.Item>
              <Descriptions.Item label="生成器版本">{revision.generator_version}</Descriptions.Item>
              <Descriptions.Item label="协议版本">{revision.protocol_version}</Descriptions.Item>
              <Descriptions.Item label="Taxonomy 版本">{revision.taxonomy_version}</Descriptions.Item>
              <Descriptions.Item label="生成 run_id">
                <IdText value={revision.run_id} maxWidth={320} />
              </Descriptions.Item>
              <Descriptions.Item label="内容 hash">{revision.content_hash}</Descriptions.Item>
              <Descriptions.Item label="创建时间">
                {formatTimestamp(revision.created_at)}
              </Descriptions.Item>
              <Descriptions.Item label="输入截断">
                {revision.truncated ? '是' : '否'}
              </Descriptions.Item>
              <Descriptions.Item label="状态">
                <StatusTag category="artifactState" value={artifact.state} />
              </Descriptions.Item>
            </Descriptions></TechnicalDetails>
            <Typography.Title level={5} style={{ marginTop: 16 }}>
              参考内容
            </Typography.Title>
            <Descriptions size="small" bordered column={1}>
              <Descriptions.Item label="相关记忆">
                {revision.memory_refs.length > 0 ? (
                  <Space wrap>
                    {revision.memory_refs.map((ref, index) => (
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
              </Descriptions.Item>
              <Descriptions.Item label="使用结果报告">
                {revision.run_refs.length > 0
                  ? <Space wrap>{revision.run_refs.map((ref) => <IdText key={ref.experience_id} value={ref.experience_id} />)}</Space>
                  : '无'}
              </Descriptions.Item>
            </Descriptions>
            <Alert
              style={{ marginTop: 12 }}
              type="info"
              showIcon
              message="参考记忆链接会打开整理时使用的版本"
              description="使用结果报告目前无法直接阅读；如需撤销，可在“反馈使用结果”中选择已有报告或输入报告编号。"
            />
          </Card>

          <TechnicalDetails label="技术详情与原始数据"><Card title="内容说明" style={{ marginTop: 16 }}>
            <JsonView value={revision.payload} maxHeight={420} emptyText="无 payload" />
            <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
              payload 按已知字段展示，未知结构作为只读 JSON；请区分输入选择、截断、引用完整性和语义覆盖，
              不能只看一个“质量通过”。
            </Typography.Paragraph>
          </Card>

          <Card title="来源记录" style={{ marginTop: 16 }}>
            <JsonView value={revision.provenance} maxHeight={420} emptyText="无 provenance" />
          </Card></TechnicalDetails>

          {artifact.kind === 'skill' ? (
            <SkillSection
              artifactId={artifact.id}
              revisionVersion={revision.version}
              artifactState={artifact.state}
              currentVersion={artifact.current_version}
            />
          ) : null}
        </>
      ) : (
        <Card>
          <Empty description={version === null ? '该对象尚无可用修订' : `该历史版本没有返回内容`} />
          <Typography.Paragraph type="secondary" style={{ textAlign: 'center', marginBottom: 0 }}>
            可以前往“整理进度”查看尚未生成的原因。相关资料变化后，系统会按后台规则重新整理。
          </Typography.Paragraph>
        </Card>
      )}

      <Typography.Paragraph type="secondary" style={{ marginTop: 16 }}>
        需要更新或已停用的内容仅供回顾，不参与默认搜索。
      </Typography.Paragraph>
    </>
  );
}
