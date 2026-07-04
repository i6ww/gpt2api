import { type ReactNode, useEffect, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';

export function StatCard({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: string;
}) {
  return (
    <div className="card p-3">
      <div className="text-tiny text-text-tertiary">{label}</div>
      <div className={`mt-1 text-h3 ${tone || 'text-text-primary'}`}>{value}</div>
    </div>
  );
}

export function Badge({ label, tone }: { label: string; tone: string }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-tiny ${tone}`}>
      {label}
    </span>
  );
}

export function FlagPill({ ok, label }: { ok: boolean; label: string }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-tiny ${
        ok ? 'bg-success-soft text-success' : 'bg-surface-2 text-text-tertiary'
      }`}
    >
      {ok ? '✓ ' : ''}
      {label}
    </span>
  );
}

export function fmtMs(ms?: number): string {
  if (!ms) return '—';
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// SplitMenuItem toolbar 的下拉菜单项。
export type SplitMenuItem = {
  label: string;
  description?: string;
  tone?: 'danger';
  disabled?: boolean;
  onClick: () => void;
};

// SplitMenu 通用下拉按钮，号池面板（Adobe / Grok / GPT）共用。
//
// 与 Adobe 早期实现一致：
//   - tone="danger" → 整个按钮 + 默认项变红（用于"删除"）
//   - busy=true     → 显示「处理中…」并禁用
//   - 点击外部 / Esc → 自动收起
export function SplitMenu({
  label,
  icon,
  tone,
  busy,
  disabled,
  items,
}: {
  label: string;
  icon: ReactNode;
  tone?: 'danger' | 'default';
  busy?: boolean;
  disabled?: boolean;
  items: SplitMenuItem[];
}) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    };
    const onEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onClick);
    document.addEventListener('keydown', onEsc);
    return () => {
      document.removeEventListener('mousedown', onClick);
      document.removeEventListener('keydown', onEsc);
    };
  }, [open]);

  const btnCls =
    tone === 'danger' ? 'btn btn-outline btn-sm text-danger' : 'btn btn-outline btn-sm';

  return (
    <div ref={wrap} className="relative">
      <button
        className={btnCls}
        disabled={disabled || busy}
        onClick={() => setOpen((v) => !v)}
      >
        {icon}
        {busy ? '处理中…' : label}
        <ChevronDown size={12} className={`transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-60 rounded-md border border-border bg-surface-1 p-1 shadow-lg">
          {items.map((it) => (
            <button
              key={it.label}
              disabled={it.disabled}
              className={`block w-full rounded px-3 py-2 text-left text-small hover:bg-surface-2 disabled:opacity-50 ${
                it.tone === 'danger' ? 'text-danger' : 'text-text-primary'
              }`}
              onClick={() => {
                setOpen(false);
                it.onClick();
              }}
            >
              <div className="font-medium">{it.label}</div>
              {it.description && (
                <div className="text-tiny text-text-tertiary">{it.description}</div>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function ImportDialogShell({
  title,
  description,
  onClose,
  busy,
  onConfirm,
  children,
}: {
  title: string;
  description?: ReactNode;
  onClose: () => void;
  busy?: boolean;
  onConfirm: () => void;
  children: ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">{title}</h3>
          {description && <p className="mt-1 text-small text-text-tertiary">{description}</p>}
        </header>
        {children}
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary btn-md" disabled={busy} onClick={onConfirm}>
            {busy ? '导入中…' : '导入'}
          </button>
        </div>
      </div>
    </div>
  );
}
