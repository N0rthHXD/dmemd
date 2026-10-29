import { Descriptions, Space, Typography } from 'antd';
import type { ForgetResponse } from '../api/types';
import { IdText } from './IdText';
import { TechnicalDetails } from './TechnicalDetails';

export function RemovalSummary({ result }: { result: ForgetResponse }) {
  const groups: Array<[string, string[]]> = [
    ['已删除记忆', result.memory_ids], ['已删除来源', result.source_ids],
    ['已删除报告', result.experience_ids],
  ];
  return <>
    <Descriptions bordered size="small" column={{ xs: 1, md: 2 }}>{groups.map(([label, ids]) => <Descriptions.Item label={label} key={label}>{ids.length} 项</Descriptions.Item>)}</Descriptions>
    <TechnicalDetails label="实际处理的完整记录">{groups.map(([label, ids]) => <div key={label}><Typography.Paragraph strong>{label}</Typography.Paragraph><Space wrap>{ids.length ? ids.map((id) => <IdText key={id} value={id} />) : '无'}</Space></div>)}
    </TechnicalDetails>
  </>;
}
