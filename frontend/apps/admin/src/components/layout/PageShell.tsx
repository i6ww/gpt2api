import clsx from 'clsx';
import type { ReactNode } from 'react';

/**
 * 通用紧凑型管理后台布局组件 — 蓝色企业 SAAS 风。
 *
 *  PageShell  整页外壳（统一内边距）
 *  PageHeader 页头（icon + 标题 + 右侧操作；不出现长描述）
 *  Toolbar    过滤工具条（input/select/btn 全 h-8 sm 尺寸，一行排开自动 wrap）
 *  Stat       紧凑指标卡（label + value，可带 tone）
 *  StatRow    指标卡 grid 容器（移动 2 列、桌面 6 列）
 *  Section    区块卡（无标题描述，仅 1 行 header + 内容）
 */

export function PageShell({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={clsx('space-y-3 p-3 sm:space-y-4 sm:p-5', className)}>{children}</div>;
}

export function PageHeader({
  icon,
  title,
  right,
  children,
}: {
  icon?: ReactNode;
  title: ReactNode;
  right?: ReactNode;
  /** 兼容老用法：放在 title 后面的副文案，建议尽量不传 */
  children?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 items-center gap-2">
        {icon && <span className="grid size-7 place-items-center rounded-md bg-info-soft text-klein-500">{icon}</span>}
        <h2 className="truncate text-h5 font-semibold text-text-primary">{title}</h2>
        {children && <span className="ml-1 hidden truncate text-tiny text-text-tertiary md:inline">{children}</span>}
      </div>
      {right && <div className="filter-bar flex flex-wrap items-center gap-2">{right}</div>}
    </header>
  );
}

export function Toolbar({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={clsx(
        'filter-bar card flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface-1 px-2.5 py-2',
        className,
      )}
    >
      {children}
    </div>
  );
}

export function ToolbarSpacer() {
  return <div className="ml-auto" />;
}

export function Stat({
  label,
  value,
  tone,
  hint,
}: {
  label: string;
  value: number | string;
  tone?: string;
  hint?: string;
}) {
  return (
    <div className="card flex items-center justify-between rounded-md border border-border bg-surface-1 px-3 py-2">
      <div className="min-w-0">
        <div className="truncate text-tiny text-text-tertiary">{label}</div>
        <div className={clsx('mt-0.5 text-h5 font-semibold leading-tight', tone || 'text-text-primary')}>
          {value}
        </div>
      </div>
      {hint && <span className="ml-2 truncate text-tiny text-text-tertiary">{hint}</span>}
    </div>
  );
}

export function StatRow({ children, cols }: { children: ReactNode; cols?: number }) {
  const c = cols || 6;
  return (
    <div
      className={clsx(
        'grid gap-2',
        c === 4 ? 'grid-cols-2 md:grid-cols-4' : c === 5 ? 'grid-cols-2 md:grid-cols-5' : 'grid-cols-2 sm:grid-cols-3 lg:grid-cols-6',
      )}
    >
      {children}
    </div>
  );
}

export function Section({
  title,
  right,
  children,
  bodyClass,
}: {
  title?: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  bodyClass?: string;
}) {
  return (
    <section className="card overflow-hidden rounded-md border border-border bg-surface-1">
      {(title || right) && (
        <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-surface-1 px-3 py-2">
          {title && <div className="text-small font-semibold text-text-primary">{title}</div>}
          {right && <div className="filter-bar flex flex-wrap items-center gap-2">{right}</div>}
        </header>
      )}
      <div className={clsx('p-3', bodyClass)}>{children}</div>
    </section>
  );
}

/** 紧凑版分页栏。
 *
 * 说明：
 * - total>0 时**始终展示**（即便只有 1 页也展示，方便用户切换"每页条数"）。
 * - 当传入 `onPageSizeChange` 时，左侧出现"每页 N 条"下拉，候选值取
 *   `sizeOptions`（默认 [10,20,50,100]）。
 * - 列表页一般通过 `usePageSize()` hook 拿到 `[size, setSize, options]`，
 *   把 setSize 接入 `onPageSizeChange`、options 接入 `sizeOptions` 即可。
 */
export function Pager({
  total,
  page,
  pageSize,
  onChange,
  onPageSizeChange,
  sizeOptions,
}: {
  total: number;
  page: number;
  pageSize: number;
  onChange: (p: number) => void;
  onPageSizeChange?: (n: number) => void;
  sizeOptions?: number[];
}) {
  if (total <= 0) return null;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const opts = sizeOptions && sizeOptions.length > 0 ? sizeOptions : [10, 20, 50, 100];
  const showSizeSelect = !!onPageSizeChange;
  // 下拉里要包含当前 pageSize（即使不在候选列表中）
  const selectOpts = opts.includes(pageSize) ? opts : [...opts, pageSize].sort((a, b) => a - b);

  // 计算可见页码（最多 7 个：首页 ... 当前-1 当前 当前+1 ... 末页）
  const pageNumbers: (number | 'gap')[] = (() => {
    if (totalPages <= 7) return Array.from({ length: totalPages }, (_, i) => i + 1);
    const arr: (number | 'gap')[] = [1];
    const left = Math.max(2, page - 1);
    const right = Math.min(totalPages - 1, page + 1);
    if (left > 2) arr.push('gap');
    for (let i = left; i <= right; i++) arr.push(i);
    if (right < totalPages - 1) arr.push('gap');
    arr.push(totalPages);
    return arr;
  })();

  const start = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const end = Math.min(total, page * pageSize);

  return (
    <div className="pager">
      <div className="pager-info">
        共 <strong className="tabular-nums text-text-primary">{total}</strong> 条
        <span className="pager-info-sep">·</span>
        当前 <span className="tabular-nums">{start}</span>–<span className="tabular-nums">{end}</span>
      </div>
      <div className="pager-controls">
        {showSizeSelect && (
          <label className="pager-size">
            <span className="pager-size-label">每页</span>
            <span className="pager-size-select">
              <select
                value={pageSize}
                onChange={(e) => onPageSizeChange!(Number(e.target.value) || pageSize)}
                aria-label="每页条数"
              >
                {selectOpts.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </span>
            <span className="pager-size-label">条</span>
          </label>
        )}
        <nav className="pager-pages" aria-label="分页">
          <button
            type="button"
            className="pager-btn pager-btn-icon"
            disabled={page <= 1}
            onClick={() => onChange(Math.max(1, page - 1))}
            aria-label="上一页"
          >
            ‹
          </button>
          {pageNumbers.map((n, idx) =>
            n === 'gap' ? (
              <span key={`gap-${idx}`} className="pager-gap">…</span>
            ) : (
              <button
                key={n}
                type="button"
                className={clsx('pager-btn', n === page && 'pager-btn-active')}
                onClick={() => onChange(n)}
                aria-current={n === page ? 'page' : undefined}
              >
                {n}
              </button>
            ),
          )}
          <button
            type="button"
            className="pager-btn pager-btn-icon"
            disabled={page >= totalPages}
            onClick={() => onChange(Math.min(totalPages, page + 1))}
            aria-label="下一页"
          >
            ›
          </button>
        </nav>
      </div>
    </div>
  );
}
