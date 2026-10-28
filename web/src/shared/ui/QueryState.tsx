import { Alert, Button, Empty, Spin } from 'antd';
import type { ReactNode } from 'react';
import { errorMessage } from '../api/errors';

export interface QueryLike<T> {
  data: T | undefined;
  error: unknown;
  isError: boolean;
  isPending: boolean;
  isFetching?: boolean;
  refetch: () => unknown;
}

interface QueryStateProps<T> {
  query: QueryLike<T>;
  children: (data: T) => ReactNode;
  isEmpty?: (data: T) => boolean;
  empty?: ReactNode;
  loadingText?: string;
}

export function QueryState<T>({
  query,
  children,
  isEmpty,
  empty,
  loadingText = '正在读取…',
}: QueryStateProps<T>) {
  if (query.isPending && query.data === undefined) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <Spin tip={loadingText}>
          <div style={{ minHeight: 48 }} />
        </Spin>
      </div>
    );
  }
  if (query.data === undefined) {
    if (query.isError) {
      return (
        <Alert
          type="error"
          showIcon
          message="读取失败"
          description={errorMessage(query.error)}
          action={
            <Button size="small" onClick={() => void query.refetch()}>
              重试
            </Button>
          }
        />
      );
    }
    return <Empty description={empty ?? '暂无数据'} />;
  }
  if (isEmpty?.(query.data)) {
    return <Empty description={empty ?? '暂无数据'} />;
  }
  return (
    <>
      {query.isError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message="后台刷新失败，正在显示上次成功读取的数据"
          description={errorMessage(query.error)}
          action={
            <Button size="small" onClick={() => void query.refetch()}>
              重新读取
            </Button>
          }
        />
      ) : null}
      {children(query.data)}
    </>
  );
}
