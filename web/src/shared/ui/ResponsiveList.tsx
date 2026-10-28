import { Empty, Grid, Space, Spin } from 'antd';
import type { ReactNode } from 'react';

/**
 * Desktop tables stay tables; narrow screens get the same rows as cards instead
 * of forcing horizontal scrolling.
 */
export function ResponsiveList<T>({
  items,
  rowKey,
  table,
  card,
  loading,
  emptyText,
}: {
  items: T[];
  rowKey: (item: T) => string;
  table: ReactNode;
  card: (item: T) => ReactNode;
  loading?: boolean;
  emptyText: string;
}) {
  const screens = Grid.useBreakpoint();
  const narrow = screens.md === false;

  if (!narrow) {
    return table;
  }
  return (
    <Spin spinning={Boolean(loading)}>
      {items.length === 0 ? (
        <Empty description={emptyText} />
      ) : (
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          {items.map((item) => (
            <div key={rowKey(item)}>{card(item)}</div>
          ))}
        </Space>
      )}
    </Spin>
  );
}
