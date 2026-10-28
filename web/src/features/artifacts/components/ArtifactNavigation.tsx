import { Tabs, Typography } from 'antd';
import { useNavigate } from 'react-router-dom';
import { routes } from '../../../shared/lib/routes';

export function ArtifactNavigation({ active }: { active: 'results' | 'progress' }) {
  const navigate = useNavigate();
  return <div className="page-intro">
    <Typography.Title level={3}>自动整理</Typography.Title>
    <Typography.Paragraph type="secondary">把记忆整理成摘要、经验和操作指南，保留来源，方便回顾。</Typography.Paragraph>
    <Tabs activeKey={active} onChange={(key) => navigate(key === 'progress' ? routes.artifactTargets : routes.artifacts)} items={[{ key: 'results', label: '整理结果' }, { key: 'progress', label: '整理进度' }]} />
  </div>;
}
