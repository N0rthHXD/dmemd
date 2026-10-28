import { Alert, App, Button, Card, Descriptions, Space, Typography } from 'antd';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage, isApiError } from '../../../shared/api/errors';
import { routes } from '../../../shared/lib/routes';
import { useSkillContent } from '../queries';
import { StatusTag } from '../../../shared/ui/StatusTag';
import { TechnicalDetails } from '../../../shared/ui/TechnicalDetails';
import { IdText } from '../../../shared/ui/IdText';

export function SkillSection({
  artifactId,
  revisionVersion,
  artifactState,
  currentVersion,
}: {
  artifactId: string;
  revisionVersion: number;
  artifactState: string;
  currentVersion: number;
}) {
  const [requested, setRequested] = useState(false);
  const { message } = App.useApp();
  const contentVersion =
    artifactState === 'active' && revisionVersion === currentVersion ? null : revisionVersion;
  const query = useSkillContent(artifactId, contentVersion, requested);

  const copy = async () => {
    if (!query.data) {
      return;
    }
    try {
      await navigator.clipboard.writeText(query.data.content);
      message.success('已复制 SKILL.md 内容');
    } catch (error) {
      message.error(`复制失败：${error instanceof Error ? error.message : '浏览器拒绝访问剪贴板'}`);
    }
  };

  const download = () => {
    if (!query.data) {
      return;
    }
    const filename = `${query.data.name || query.data.id}-v${query.data.version}.md`.replace(
      /[\\/:*?"<>|\s]+/g,
      '-',
    );
    const blob = new Blob([query.data.content], { type: 'text/markdown;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  };

  return (
    <Card
      title="导出操作指南"
      style={{ marginTop: 16 }}
      extra={
        <Space wrap>
          <Button onClick={() => setRequested(true)} loading={query.isFetching}>
            {query.data ? '刷新内容' : '预览导出内容'}
          </Button>
          <Button disabled={!query.data} onClick={() => void copy()}>
            复制
          </Button>
          <Button disabled={!query.data} onClick={download}>
            下载 .md
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        message="只预览、复制和显式下载，不执行或安装"
        description="导出当前正在阅读的版本。指南仅供参考，不代表内容真实性、执行成功或安全性已验证。"
        style={{ marginBottom: 12 }}
      />
      {!requested ? (
        <Typography.Text type="secondary">点击“预览导出内容”后，可以复制或下载 Markdown 文件。</Typography.Text>
      ) : query.isError ? (
        isApiError(query.error) && query.error.status === 404 ? (
          <Alert
            type="error"
            showIcon
            message="SKILL.md 内容不存在或已被永久清除"
            description="不会把 404 解释为空正文。"
          />
        ) : (
          <Alert
            type="error"
            showIcon
            message="SKILL.md 读取失败"
            description={errorMessage(query.error)}
          />
        )
      ) : query.data ? (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
            <Descriptions.Item label="名称">{query.data.name}</Descriptions.Item>
            <Descriptions.Item label="版本">v{query.data.version}</Descriptions.Item>
            <Descriptions.Item label="状态">
              <StatusTag category="artifactState" value={query.data.state} />
            </Descriptions.Item>
            <Descriptions.Item label="输入截断">
              {query.data.truncated ? '是' : '否'}
            </Descriptions.Item>
            <Descriptions.Item label="描述" span={2}>
              {query.data.description}
            </Descriptions.Item>
            <Descriptions.Item label="记忆引用" span={2}>
              {query.data.memory_refs.length > 0 ? (
                <Space wrap>
                  {query.data.memory_refs.map((ref, index) => (
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
            <Descriptions.Item label="运行引用" span={2}>
              {query.data.run_refs.length > 0
                ? <Space wrap>{query.data.run_refs.map((ref) => <IdText key={ref.experience_id} value={ref.experience_id} />)}</Space>
                : '无'}
            </Descriptions.Item>
          </Descriptions>
          <TechnicalDetails label="导出文件原文"><pre
            style={{
              margin: 0,
              padding: 16,
              maxHeight: 520,
              overflow: 'auto',
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              background: 'rgba(127, 127, 127, 0.08)',
              borderRadius: 8,
              fontSize: 12,
              lineHeight: 1.7,
            }}
          >
            {query.data.content}
          </pre></TechnicalDetails>
        </Space>
      ) : null}
    </Card>
  );
}
