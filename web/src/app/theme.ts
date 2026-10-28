import { theme } from 'antd';
import type { ThemeConfig } from 'antd';

export type ThemeMode = 'light' | 'dark';
export type Density = 'comfortable' | 'compact';

export function buildTheme(mode: ThemeMode, density: Density): ThemeConfig {
  return {
    algorithm: [
      mode === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm,
      ...(density === 'compact' ? [theme.compactAlgorithm] : []),
    ],
    token: {
      colorPrimary: '#5264d9',
      borderRadius: 10,
      colorBgLayout: mode === 'dark' ? '#101217' : '#f5f6fa',
      colorBgContainer: mode === 'dark' ? '#191c24' : '#ffffff',
      fontSize: 14,
      lineHeight: 1.65,
      fontFamily:
        '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
    },
    components: {
      Layout: {
        headerBg: mode === 'dark' ? '#191c24' : '#ffffff',
        siderBg: mode === 'dark' ? '#191c24' : '#ffffff',
      },
      Table: {
        cellPaddingBlockSM: density === 'compact' ? 4 : 8,
      },
    },
  };
}
