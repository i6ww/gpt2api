import { useCallback, useState } from 'react';
import { AlertTriangle, Info, Trash2 } from 'lucide-react';

export type ConfirmTone = 'primary' | 'danger' | 'warning';

export type ConfirmDialogProps = {
  open: boolean;
  title: string;
  description: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: ConfirmTone;
  loading?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
};

const TONE_BTN: Record<ConfirmTone, string> = {
  primary: 'btn btn-primary btn-sm',
  danger: 'btn btn-danger btn-sm',
  warning: 'btn btn-primary btn-sm',
};

const TONE_ICON: Record<ConfirmTone, { wrap: string; node: React.ReactNode }> = {
  primary: {
    wrap: 'bg-info-soft text-klein-500',
    node: <Info size={16} />,
  },
  danger: {
    wrap: 'bg-danger-soft text-danger',
    node: <Trash2 size={16} />,
  },
  warning: {
    wrap: 'bg-warning-soft text-warning',
    node: <AlertTriangle size={16} />,
  },
};

/**
 * 通用确认对话框。统一替代 window.confirm，保证全后台风格一致。
 *
 * 推荐通过 useConfirm() hook 调用；若需要更细粒度控制（loading 来自外层 mutation
 * 状态、按钮文案动态变化等），也可以直接渲染本组件。
 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = '确认',
  cancelLabel = '取消',
  tone = 'primary',
  loading = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  if (!open) return null;
  const icon = TONE_ICON[tone];
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-4">
      <button
        className="absolute inset-0 cursor-default"
        type="button"
        aria-label="关闭"
        onClick={loading ? undefined : onCancel}
      />
      <div className="relative w-full max-w-md overflow-hidden rounded-md border border-border bg-surface-1 shadow-3">
        <div className="flex items-start gap-3 px-4 pt-4">
          <span
            className={'mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-full ' + icon.wrap}
          >
            {icon.node}
          </span>
          <div className="min-w-0 flex-1">
            <h3 className="text-small font-semibold text-text-primary">{title}</h3>
            <div className="mt-1 text-tiny leading-5 text-text-secondary">{description}</div>
          </div>
        </div>
        <div className="mt-4 flex justify-end gap-2 border-t border-border bg-surface-2/40 px-4 py-2.5">
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={onCancel}
            disabled={loading}
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            className={TONE_BTN[tone]}
            onClick={onConfirm}
            disabled={loading}
          >
            {loading ? '处理中…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

type ConfirmOptions = Omit<ConfirmDialogProps, 'open' | 'onConfirm' | 'onCancel' | 'loading'>;

type AskState = ConfirmOptions & { resolve: (ok: boolean) => void };

/**
 * 函数式调用：`const ok = await confirm({ title, description, tone })`。
 *
 * 在组件里使用：
 *
 *   const { confirm, dialog } = useConfirm();
 *   ...
 *   if (await confirm({ title: '删除？', tone: 'danger' })) doDelete();
 *   ...
 *   return <>...{dialog}</>;
 */
export function useConfirm() {
  const [state, setState] = useState<AskState | null>(null);

  const confirm = useCallback((opts: ConfirmOptions) => {
    return new Promise<boolean>((resolve) => {
      setState({ ...opts, resolve });
    });
  }, []);

  const handleCancel = useCallback(() => {
    state?.resolve(false);
    setState(null);
  }, [state]);

  const handleConfirm = useCallback(() => {
    state?.resolve(true);
    setState(null);
  }, [state]);

  const dialog = state ? (
    <ConfirmDialog
      open
      title={state.title}
      description={state.description}
      confirmLabel={state.confirmLabel}
      cancelLabel={state.cancelLabel}
      tone={state.tone}
      onConfirm={handleConfirm}
      onCancel={handleCancel}
    />
  ) : null;

  return { confirm, dialog };
}
