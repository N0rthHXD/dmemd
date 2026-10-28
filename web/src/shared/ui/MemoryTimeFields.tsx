import { Descriptions, Tooltip, Typography } from 'antd';
import type { Memory, TemporalDetail } from '../api/types';
import { basisLabels, precisionLabels } from '../lib/codes';
import { formatTemporal, formatTimestamp, formatValidInterval } from '../lib/time';

function BasisText({ value, detail }: { value: string; detail: TemporalDetail | undefined }) {
  const label = basisLabels[value]?.label ?? value;
  return (
    <Typography.Text type={value === 'recorded' ? 'warning' : 'secondary'}>
      {label}
      {detail?.precision ? ` · 精度 ${precisionLabels[detail.precision]?.label ?? detail.precision}` : ''}
      {detail && !detail.timezone_known ? ' · 无时区' : ''}
    </Typography.Text>
  );
}

function ExactTime({ value }: { value: string | null | undefined }) {
  return (
    <Tooltip title={`UTC 原值：${value || '未知'}`}>
      <span>{formatTimestamp(value)}</span>
    </Tooltip>
  );
}

export function MemoryTimeFields({ memory }: { memory: Pick<Memory,
  | 'observed_at'
  | 'observed_at_basis'
  | 'event_time'
  | 'event_time_basis'
  | 'valid_from'
  | 'valid_to'
  | 'valid_from_basis'
  | 'valid_to_basis'
  | 'temporal'
  | 'created_at'
  | 'updated_at'
> }) {
  const observed = formatTemporal(memory.observed_at, memory.temporal?.observed);
  return (
    <Descriptions size="small" bordered column={{ xs: 1, md: 2 }}>
      <Descriptions.Item label="记录时间">
        <ExactTime value={memory.created_at} />
        <br />
        <Typography.Text type="secondary">
          更新于 <ExactTime value={memory.updated_at} />
        </Typography.Text>
      </Descriptions.Item>
      <Descriptions.Item label="观测 / 陈述时间">
        {observed}
        <br />
        <BasisText value={memory.observed_at_basis} detail={memory.temporal?.observed} />
        {memory.observed_at_basis === 'recorded' ? (
          <Typography.Paragraph type="warning" style={{ margin: '4px 0 0' }}>
            接收时间回填，不能当作事件发生时间或事实更新先后的依据。
          </Typography.Paragraph>
        ) : null}
      </Descriptions.Item>
      <Descriptions.Item label="事件时间">
        {formatTemporal(memory.event_time, memory.temporal?.event)}
        <br />
        <BasisText value={memory.event_time_basis} detail={memory.temporal?.event} />
      </Descriptions.Item>
       <Descriptions.Item label="适用时间（含开始、不含结束）">
        {formatValidInterval(
          memory.valid_from,
          memory.valid_to,
          memory.temporal?.valid_from,
          memory.temporal?.valid_to,
        )}
        <br />
        <Typography.Text type="secondary">
          起点 {basisLabels[memory.valid_from_basis]?.label ?? memory.valid_from_basis}；终点{' '}
          {basisLabels[memory.valid_to_basis]?.label ?? memory.valid_to_basis}
        </Typography.Text>
      </Descriptions.Item>
    </Descriptions>
  );
}
