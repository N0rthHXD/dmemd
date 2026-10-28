import { createContext, useContext, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import type { Density, ThemeMode } from './theme';

interface Preferences {
  mode: ThemeMode;
  density: Density;
  toggleMode: () => void;
  toggleDensity: () => void;
}

const STORAGE_KEY = 'dmemd.ui.preferences';

const PreferencesContext = createContext<Preferences | null>(null);

function readPreferences(): { mode: ThemeMode; density: Density } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (typeof parsed === 'object' && parsed !== null) {
        const record = parsed as Record<string, unknown>;
        return {
          mode: record.mode === 'dark' ? 'dark' : 'light',
          density: record.density === 'compact' ? 'compact' : 'comfortable',
        };
      }
    }
  } catch {
    // Ignore malformed local preferences; only non-sensitive view settings live here.
  }
  return { mode: 'light', density: 'comfortable' };
}

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const [preferences, setPreferences] = useState(readPreferences);

  const value = useMemo<Preferences>(
    () => ({
      mode: preferences.mode,
      density: preferences.density,
      toggleMode: () =>
        setPreferences((current) => {
          const next = { ...current, mode: current.mode === 'dark' ? 'light' : 'dark' } as const;
          localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
          return next;
        }),
      toggleDensity: () =>
        setPreferences((current) => {
          const next = {
            ...current,
            density: current.density === 'compact' ? 'comfortable' : 'compact',
          } as const;
          localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
          return next;
        }),
    }),
    [preferences],
  );

  return <PreferencesContext.Provider value={value}>{children}</PreferencesContext.Provider>;
}

export function usePreferences(): Preferences {
  const value = useContext(PreferencesContext);
  if (!value) {
    throw new Error('usePreferences 必须在 PreferencesProvider 内使用');
  }
  return value;
}
