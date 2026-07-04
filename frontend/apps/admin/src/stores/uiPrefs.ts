// 后台 UI 偏好 store。
//
// 目前只承载"分页"相关偏好：
//   - defaultPageSize：来自 system_config.ui.pagination.default_page_size，
//     管理员在系统设置里改了就立刻全局生效。
//   - pageSizeOptions：候选每页条数下拉，例 [10,20,50,100,500,1000]
//   - sessionPageSize：用户在某次会话里通过 Pager 下拉切换过的"覆盖值"，
//     持久化到 localStorage，跨页面共享一份。null 表示未覆盖，回落到 default。
//
// 所有列表页通过 hook `usePageSize()` 读 [size, setSize, options]，
// 这样改一处、全站生效。
import { useEffect } from 'react';
import { create } from 'zustand';

import { systemApi } from '../lib/services';
import type { UIPaginationSettings } from '../lib/types';

const LS_KEY = 'ui.pageSize.session';
const HARD_DEFAULT = 10;
const HARD_OPTIONS = [10, 20, 50, 100, 200, 500, 1000];

function readSavedSize(): number | null {
  try {
    const v = Number(localStorage.getItem(LS_KEY));
    return Number.isFinite(v) && v > 0 ? v : null;
  } catch {
    return null;
  }
}

function writeSavedSize(n: number | null) {
  try {
    if (n == null) localStorage.removeItem(LS_KEY);
    else localStorage.setItem(LS_KEY, String(n));
  } catch {
    /* ignore quota / privacy errors */
  }
}

interface UIPrefsState {
  /** 来自 system_config 的全局默认（管理员可改） */
  defaultPageSize: number;
  /** Pager 下拉候选条数 */
  pageSizeOptions: number[];
  /** 用户本次会话用 Pager 下拉切过的值；null = 未覆盖，跟随 default */
  sessionPageSize: number | null;
  /** 是否已经从后端拉过 system_config（避免每个页面重复 fetch） */
  hydrated: boolean;

  /** 从 system_config 拉一次配置；fail-soft，错误时保留默认值 */
  hydrate: () => Promise<void>;
  /** Pager 下拉切换时调用：写 zustand + localStorage 双源 */
  setSessionPageSize: (n: number) => void;
}

export const useUIPrefs = create<UIPrefsState>((set, get) => ({
  defaultPageSize: HARD_DEFAULT,
  pageSizeOptions: HARD_OPTIONS,
  sessionPageSize: readSavedSize(),
  hydrated: false,

  hydrate: async () => {
    if (get().hydrated) return;
    try {
      const settings = await systemApi.get();
      const cfg = (settings?.['ui.pagination'] ?? {}) as UIPaginationSettings;
      const def = Number(cfg.default_page_size);
      const opts = Array.isArray(cfg.page_size_options)
        ? cfg.page_size_options
            .map((x) => Number(x))
            .filter((x) => Number.isFinite(x) && x > 0)
        : [];
      set({
        defaultPageSize: def > 0 ? def : HARD_DEFAULT,
        pageSizeOptions: opts.length > 0 ? opts : HARD_OPTIONS,
        hydrated: true,
      });
    } catch {
      set({ hydrated: true }); // 失败兜底默认值
    }
  },

  setSessionPageSize: (n) => {
    if (!Number.isFinite(n) || n <= 0) return;
    writeSavedSize(n);
    set({ sessionPageSize: n });
  },
}));

/**
 * usePageSize 返回 `[currentPageSize, setPageSize, pageSizeOptions]`，
 * 列表页用法：
 *
 * ```tsx
 * const [pageSize, setPageSize, sizeOptions] = usePageSize();
 * const [page, setPage] = useState(1);
 * useEffect(() => setPage(1), [pageSize]); // 切 size 时回到第 1 页
 *
 * <Pager total={total} page={page} pageSize={pageSize}
 *        onChange={setPage}
 *        onPageSizeChange={setPageSize}
 *        sizeOptions={sizeOptions} />
 * ```
 *
 * 注意：组件 mount 时如果 store 还没 hydrate，会先返回硬编码 10，
 * 等 hydrate 完成会自动 re-render；列表页不需要写 useEffect。
 */
export function usePageSize(): [number, (n: number) => void, number[]] {
  const { defaultPageSize, pageSizeOptions, sessionPageSize, setSessionPageSize, hydrated, hydrate } = useUIPrefs();
  // 第一次用到时懒加载 — 避免在 main.tsx 强制 fetch（登录前不必要）。
  useEffect(() => {
    if (!hydrated) void hydrate();
  }, [hydrated, hydrate]);
  const size = sessionPageSize ?? defaultPageSize;
  return [size, setSessionPageSize, pageSizeOptions];
}
