import {
  DatabaseOutlined, DashboardOutlined, FieldTimeOutlined, MenuFoldOutlined,
  MenuUnfoldOutlined, MoonOutlined, PartitionOutlined, PlusCircleOutlined,
  SearchOutlined, SettingOutlined, SunOutlined, WarningOutlined,
} from '@ant-design/icons';
import { Breadcrumb, Button, Drawer, Grid, Layout, Menu, Popover, Segmented, Space, Typography } from 'antd';
import { useMemo, useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { useSessionExperiences, useSessionIngestions } from '../../shared/api/session-receipts';
import { routes } from '../../shared/lib/routes';
import { usePreferences } from '../preferences';
import styles from './AppLayout.module.css';

const { Header, Sider, Content } = Layout;
const menuItems = [
  { type: 'group' as const, label: '日常使用', children: [
    { key: routes.overview, icon: <DashboardOutlined />, label: '首页' },
    { key: routes.memories, icon: <DatabaseOutlined />, label: '记忆库' },
    { key: routes.recall, icon: <SearchOutlined />, label: '搜索记忆' },
    { key: routes.newIngestion, icon: <PlusCircleOutlined />, label: '添加内容' },
  ] },
  { type: 'group' as const, label: '内容整理', children: [
    { key: routes.artifacts, icon: <PartitionOutlined />, label: '自动整理' },
    { key: routes.conflicts, icon: <WarningOutlined />, label: '内容核对' },
    { key: routes.jobs, icon: <FieldTimeOutlined />, label: '处理记录' },
  ] },
  { type: 'divider' as const },
  { key: routes.system, icon: <SettingOutlined />, label: '服务状态' },
];
const routeLabels: Record<string, string> = {
  overview: '首页', memories: '记忆库', recall: '搜索记忆', ingestions: '添加内容',
  jobs: '处理记录', conflicts: '内容核对', artifacts: '自动整理',
  'artifact-targets': '整理进度', system: '服务状态', sources: '来源原文',
};
const validParentPaths = new Set<string>([
  routes.overview, routes.memories, routes.recall, routes.jobs, routes.conflicts,
  routes.artifacts, routes.artifactTargets, routes.system,
]);

export default function AppLayout() {
  const location = useLocation();
  const navigate = useNavigate();
  const screens = Grid.useBreakpoint();
  const mobile = !screens.lg;
  const [collapsed, setCollapsed] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { mode, density, toggleMode, toggleDensity } = usePreferences();
  useSessionIngestions();
  useSessionExperiences();
  const firstSegment = location.pathname.split('/').filter(Boolean)[0] ?? 'overview';
  const selectedKey = firstSegment === 'ingestions' ? routes.newIngestion
    : firstSegment === 'artifact-targets' ? routes.artifacts : `/${firstSegment}`;
  const breadcrumbs = useMemo(() => location.pathname.split('/').filter(Boolean).map((part, index, parts) => {
    const href = `/${parts.slice(0, index + 1).join('/')}`;
    const label = routeLabels[part] ?? (index === 0 ? '页面' : '详情');
    return { key: href, title: index === parts.length - 1 || !validParentPaths.has(href) ? label : <Link to={href}>{label}</Link> };
  }), [location.pathname]);
  const navigation = (
    <Menu mode="inline" inlineCollapsed={!mobile && collapsed} selectedKeys={[selectedKey]} items={menuItems}
      onClick={({ key }) => { navigate(key); setDrawerOpen(false); }} />
  );
  const appearance = (
    <Space direction="vertical" size={16}>
      <div><Typography.Paragraph>页面主题</Typography.Paragraph>
        <Segmented value={mode} options={[{ value: 'light', label: '浅色', icon: <SunOutlined /> }, { value: 'dark', label: '深色', icon: <MoonOutlined /> }]} onChange={(value) => { if (value !== mode) toggleMode(); }} />
      </div>
      <div><Typography.Paragraph>显示密度</Typography.Paragraph>
        <Segmented value={density} options={[{ value: 'comfortable', label: '舒适' }, { value: 'compact', label: '紧凑' }]} onChange={(value) => { if (value !== density) toggleDensity(); }} />
      </div>
    </Space>
  );
  return (
    <Layout className={styles.layout}>
      {!mobile ? <Sider className={styles.sider} theme="light" width={232} collapsedWidth={72} collapsed={collapsed} trigger={null}>
        <div className={styles.brand}><span className={styles.logoMark}>d</span>{!collapsed ? <span className={styles.brandText}>记忆工作台</span> : null}</div>
        <div className={styles.navigation}>{navigation}</div>
        <div className={styles.siderBottom}>
          <Popover content={appearance} title="外观设置" trigger="click" placement="rightBottom"><Button type="text" icon={<SettingOutlined />} aria-label="外观设置">{!collapsed ? '外观设置' : null}</Button></Popover>
          <Button type="text" icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} aria-label={collapsed ? '展开导航' : '收起导航'} onClick={() => setCollapsed(!collapsed)}>{!collapsed ? '收起导航' : null}</Button>
        </div>
      </Sider> : null}
      <Drawer title="记忆工作台" placement="left" width={280} open={mobile && drawerOpen} onClose={() => setDrawerOpen(false)}>
        {navigation}<Popover content={appearance} title="外观设置" trigger="click"><Button type="text" icon={<SettingOutlined />}>外观设置</Button></Popover>
      </Drawer>
      <Layout className={styles.workspace}>
        <Header className={styles.header}>
          {mobile ? <Button type="text" icon={<MenuUnfoldOutlined />} aria-label="打开导航" onClick={() => setDrawerOpen(true)} /> : null}
          <Breadcrumb items={breadcrumbs} />
          <Typography.Text type="secondary" className={styles.headerHint}>让记忆有迹可循</Typography.Text>
        </Header>
        <Content className={styles.content}><div className={styles.page}><Outlet /></div>
          <footer className={styles.footer}>dmemd · 本地记忆工作台</footer>
        </Content>
      </Layout>
    </Layout>
  );
}
