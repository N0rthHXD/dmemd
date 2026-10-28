import { Popover, Typography } from 'antd';
import { shortId } from '../lib/display';

export function IdText({
  value,
  maxWidth = 220,
}: {
  value: string | null | undefined;
  maxWidth?: number;
}) {
  if (!value) {
    return <Typography.Text type="secondary">—</Typography.Text>;
  }
  return (
    <Popover trigger="click" content={<Typography.Text copyable={{ text: value }} style={{ overflowWrap: 'anywhere', maxWidth: 320, display: 'inline-block' }}>{value}</Typography.Text>} title="完整编号">
      <Typography.Text
        type="secondary"
        copyable={{ text: value, tooltips: ['复制完整编号', '已复制'] }}
        style={{ maxWidth, overflowWrap: 'anywhere', fontSize: 12 }}
      >
        <button type="button" className="identifier-button" aria-label="查看完整编号">{shortId(value)}</button>
      </Typography.Text>
    </Popover>
  );
}
