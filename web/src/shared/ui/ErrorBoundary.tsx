import { Button, Result } from 'antd';
import type { ErrorInfo, ReactNode } from 'react';
import { Component } from 'react';
import { useRouteError } from 'react-router-dom';

interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('页面渲染失败', error, info);
  }

  render() {
    if (this.state.error) {
      return (
        <Result
          status="500"
          title="页面渲染失败"
          subTitle={this.state.error.message}
          extra={
            <Button type="primary" onClick={() => window.location.reload()}>
              重新加载页面
            </Button>
          }
        />
      );
    }
    return this.props.children;
  }
}

export function RouteErrorElement() {
  const error = useRouteError();
  const message = error instanceof Error ? error.message : '未知路由错误';
  return (
    <Result
      status="500"
      title="页面加载失败"
      subTitle={message}
      extra={
        <Button type="primary" onClick={() => window.location.reload()}>
          重新加载页面
        </Button>
      }
    />
  );
}
