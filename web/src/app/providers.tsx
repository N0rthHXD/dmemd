import { App as AntApp, ConfigProvider } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import type { ReactNode } from 'react';
import { QueryClientProvider } from '@tanstack/react-query';
import { PreferencesProvider, usePreferences } from './preferences';
import { queryClient } from './query-client';
import { buildTheme } from './theme';

function ThemedProviders({ children }: { children: ReactNode }) {
  const { mode, density } = usePreferences();
  return (
    <ConfigProvider locale={zhCN} theme={buildTheme(mode, density)}>
      <AntApp className={`workspace-theme workspace-theme-${mode}`}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </AntApp>
    </ConfigProvider>
  );
}

export function Providers({ children }: { children: ReactNode }) {
  return (
    <PreferencesProvider>
      <ThemedProviders>{children}</ThemedProviders>
    </PreferencesProvider>
  );
}
