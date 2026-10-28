import { Alert, Space } from 'antd';
import { Link } from 'react-router-dom';
import { routes } from '../lib/routes';
import { TechnicalDetails } from './TechnicalDetails';
import { warningText } from '../lib/codes';

export function WarningList({
  codes,
  title = '服务警告',
}: {
  codes: string[] | undefined;
  title?: string;
}) {
  if (!codes || codes.length === 0) {
    return null;
  }
  return (
    <Alert
      type="warning"
      showIcon
      message={title}
      description={
        <Space direction="vertical" size={4} style={{ width: '100%' }}>
          {[...new Set(codes.map((code) => code.startsWith('semantic_') ? 'semantic_unavailable' : code))].map((code) => (
            <div key={code}>
              <span>{warningText(code)}</span>
              {code === 'semantic_unavailable' ? <> <Link to={routes.system}>查看服务状态</Link></> : null}
            </div>
          ))}
          <TechnicalDetails label="查看提示详情">{codes.join('、')}</TechnicalDetails>
        </Space>
      }
    />
  );
}
