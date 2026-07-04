export { default as kleinPreset } from './tailwind.preset';

export const KLEIN_TOKENS = {
  primary: 'var(--klein-600)',
  primaryGradient: 'var(--klein-gradient)',
  glow: 'var(--klein-glow)',
} as const;

export type ThemeMode = 'light' | 'dark' | 'system';

// 系统主题变化时的实时同步：mode = 'system' 才生效。
// 调用 applyThemeMode 时统一卸载旧 listener，避免叠加。
let systemThemeListenerCleanup: (() => void) | null = null;

export function applyThemeMode(mode: ThemeMode) {
  if (typeof window === 'undefined') return;
  const root = document.documentElement;

  if (systemThemeListenerCleanup) {
    systemThemeListenerCleanup();
    systemThemeListenerCleanup = null;
  }

  if (mode === 'system') {
    const mql = window.matchMedia('(prefers-color-scheme: dark)');
    const sync = (matches: boolean) => {
      root.dataset.theme = matches ? 'dark' : 'light';
    };
    sync(mql.matches);
    const handler = (e: MediaQueryListEvent) => sync(e.matches);
    if (typeof mql.addEventListener === 'function') {
      mql.addEventListener('change', handler);
      systemThemeListenerCleanup = () => mql.removeEventListener('change', handler);
    } else if (typeof (mql as { addListener?: (cb: (e: MediaQueryListEvent) => void) => void }).addListener === 'function') {
      // Safari < 14 fallback
      const legacy = mql as { addListener: (cb: (e: MediaQueryListEvent) => void) => void; removeListener: (cb: (e: MediaQueryListEvent) => void) => void };
      legacy.addListener(handler);
      systemThemeListenerCleanup = () => legacy.removeListener(handler);
    }
    return;
  }

  root.dataset.theme = mode;
}
