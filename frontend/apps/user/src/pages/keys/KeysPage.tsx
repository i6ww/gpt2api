import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { Plus, Copy, Check, Trash2, Power, X, KeyRound, BarChart3 } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ApiError } from '../../lib/api';
import { fmtRelative } from '../../lib/format';
import { keysApi } from '../../lib/services';
import type { APIKeyCreated, APIKeyStat } from '../../lib/types';
import { toast } from '../../stores/toast';
import { useConfirm } from '../../components/ConfirmDialog';

const STATUS_ENABLED = 1;

// scope / preset 选项的标签改成 i18n key（实际渲染时 t(key)）。
const SCOPE_OPTIONS: { value: string; labelKey: string }[] = [
  { value: 'image,video,chat,music', labelKey: 'keys.scope_all' },
  { value: 'image', labelKey: 'keys.scope_image' },
  { value: 'video', labelKey: 'keys.scope_video' },
  { value: 'music', labelKey: 'keys.scope_music' },
  { value: 'image,video', labelKey: 'keys.scope_image_video' },
];

// 预设时间段。'custom' 走自定义起止；'all' 不传 since/until。
type RangePreset = 'today' | '7d' | '30d' | 'all' | 'custom';

const PRESET_OPTIONS: { value: RangePreset; labelKey: string }[] = [
  { value: 'today', labelKey: 'keys.preset_today' },
  { value: '7d', labelKey: 'keys.preset_7d' },
  { value: '30d', labelKey: 'keys.preset_30d' },
  { value: 'all', labelKey: 'keys.preset_all' },
  { value: 'custom', labelKey: 'keys.preset_custom' },
];

// resolveRange 把 preset + 自定义日期解析成后端能吃的 unix 秒区间。
//
// 注意：自定义日期是用户本地时区下的日期串（YYYY-MM-DD），转 Date 时也按本地解析，
// 这样用户选 2026-05-13 看到的是当地一整天 00:00 ~ 23:59。后端按 UTC 存，
// 但 generation_task.created_at 也是 UTC，闭区间过滤不会漏数据。
function resolveRange(preset: RangePreset, customFrom: string, customTo: string): { since?: number; until?: number } {
  const now = new Date();
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0);
  switch (preset) {
    case 'today':
      return { since: Math.floor(startOfToday.getTime() / 1000) };
    case '7d':
      return { since: Math.floor((Date.now() - 7 * 86400 * 1000) / 1000) };
    case '30d':
      return { since: Math.floor((Date.now() - 30 * 86400 * 1000) / 1000) };
    case 'all':
      return {};
    case 'custom': {
      const r: { since?: number; until?: number } = {};
      if (customFrom) {
        const d = new Date(customFrom + 'T00:00:00');
        if (!Number.isNaN(d.getTime())) r.since = Math.floor(d.getTime() / 1000);
      }
      if (customTo) {
        const d = new Date(customTo + 'T23:59:59');
        if (!Number.isNaN(d.getTime())) r.until = Math.floor(d.getTime() / 1000);
      }
      return r;
    }
  }
}

const EMPTY_STAT: APIKeyStat = {
  key_id: 0,
  call_total: 0,
  call_succeeded: 0,
  call_failed: 0,
  consumed_points: 0,
  refunded_points: 0,
};

function fmtPoints(n: number): string {
  if (!Number.isFinite(n) || n === 0) return '0';
  if (Math.abs(n) >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (Math.abs(n) >= 10_000) return (n / 1_000).toFixed(1) + 'k';
  return n.toLocaleString();
}

export default function KeysPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [showCreate, setShowCreate] = useState(false);
  const [createdKey, setCreatedKey] = useState<APIKeyCreated | null>(null);
  const [preset, setPreset] = useState<RangePreset>('7d');
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');

  const range = useMemo(() => resolveRange(preset, customFrom, customTo), [preset, customFrom, customTo]);

  const listQ = useQuery({
    queryKey: ['keys'],
    queryFn: () => keysApi.list(),
  });

  const statsQ = useQuery({
    queryKey: ['keys.stats', range.since ?? 0, range.until ?? 0],
    queryFn: () => keysApi.stats(range),
    // 自定义日期时如果用户没输完整就不要疯狂请求；其他 preset 直接 fetch。
    enabled: preset !== 'custom' || !!customFrom || !!customTo,
  });

  const createMut = useMutation({
    mutationFn: keysApi.create,
    onSuccess: (data) => {
      setCreatedKey(data);
      setShowCreate(false);
      qc.invalidateQueries({ queryKey: ['keys'] });
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('keys.create_fail')),
  });

  const toggleMut = useMutation({
    mutationFn: keysApi.toggle,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['keys'] });
      toast.success(t('keys.toggle_success'));
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('keys.toggle_fail')),
  });

  const removeMut = useMutation({
    mutationFn: keysApi.remove,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['keys'] });
      qc.invalidateQueries({ queryKey: ['keys.stats'] });
      toast.success(t('keys.del_success'));
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('keys.del_fail')),
  });

  const keys = listQ.data ?? [];
  // 通过 keyID 把统计 join 到每行。
  const statByKey = useMemo(() => {
    const m = new Map<number, APIKeyStat>();
    statsQ.data?.per_key.forEach((s) => m.set(s.key_id, s));
    return m;
  }, [statsQ.data]);
  const total = statsQ.data?.total ?? EMPTY_STAT;

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <h1 className="page-title">{t('keys.title')}</h1>
          <p className="page-subtitle">{t('keys.subtitle')}</p>
        </div>
        <button className="btn btn-primary btn-lg" onClick={() => setShowCreate(true)}>
          <Plus size={18} /> {t('keys.create_btn')}
        </button>
      </header>

      {createdKey && <CreatedKeyCard data={createdKey} onClose={() => setCreatedKey(null)} />}

      {/* ============ 统计汇总 + 时间筛选 ============ */}
      <section className="card card-section mb-4">
        <header className="section-header mb-3">
          <h3 className="section-title flex items-center gap-2">
            <BarChart3 size={16} className="text-klein-500" />
            {t('keys.stats_title')}
          </h3>
          <div className="flex flex-wrap items-center gap-2">
            <select
              className="select select-sm"
              value={preset}
              onChange={(e) => setPreset(e.target.value as RangePreset)}
            >
              {PRESET_OPTIONS.map((p) => (
                <option key={p.value} value={p.value}>
                  {t(p.labelKey)}
                </option>
              ))}
            </select>
            {preset === 'custom' && (
              <>
                <input
                  type="date"
                  className="input input-sm"
                  value={customFrom}
                  max={customTo || undefined}
                  onChange={(e) => setCustomFrom(e.target.value)}
                />
                <span className="text-text-tertiary text-small">→</span>
                <input
                  type="date"
                  className="input input-sm"
                  value={customTo}
                  min={customFrom || undefined}
                  onChange={(e) => setCustomTo(e.target.value)}
                />
              </>
            )}
          </div>
        </header>
        <div className="grid gap-3 grid-cols-2 md:grid-cols-4">
          <StatTile
            label={t('keys.stat_calls')}
            value={fmtPoints(total.call_total)}
            hint={t('keys.stat_calls_hint', { ok: fmtPoints(total.call_succeeded), fail: fmtPoints(total.call_failed) })}
          />
          <StatTile label={t('keys.stat_consumed')} value={fmtPoints(total.consumed_points)} hint={t('keys.stat_consumed_hint')} />
          <StatTile label={t('keys.stat_refunded')} value={fmtPoints(total.refunded_points)} hint={t('keys.stat_refunded_hint')} />
          <StatTile
            label={t('keys.stat_last_called')}
            value={total.last_called_at ? fmtRelative(total.last_called_at) : '—'}
            hint={t('keys.stat_last_called_hint')}
          />
        </div>
      </section>

      <div className="card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="data-table min-w-[920px]">
            <thead>
              <tr>
                <th>{t('keys.th_name')}</th>
                <th>{t('keys.th_key')}</th>
                <th>{t('keys.th_scope')}</th>
                <th>{t('keys.th_rpm')}</th>
                <th className="text-right">{t('keys.th_calls')}</th>
                <th className="text-right">{t('keys.th_consumed')}</th>
                <th className="text-right">{t('keys.th_refunded')}</th>
                <th>{t('keys.th_last_used')}</th>
                <th>{t('keys.th_actions')}</th>
              </tr>
            </thead>
            <tbody>
              {listQ.isLoading && (
                <tr>
                  <td colSpan={9} className="text-center text-text-tertiary text-small py-10">{t('common.loading')}</td>
                </tr>
              )}
              {!listQ.isLoading && keys.length === 0 && (
                <tr>
                  <td colSpan={9}>
                    <div className="empty-state">
                      <span className="empty-state-icon">
                        <KeyRound size={22} />
                      </span>
                      <p className="empty-state-title">{t('keys.empty_title')}</p>
                      <p className="empty-state-desc">{t('keys.empty_desc')}</p>
                    </div>
                  </td>
                </tr>
              )}
              {keys.map((k) => {
                const s = statByKey.get(k.id) ?? EMPTY_STAT;
                return (
                  <tr key={k.id}>
                    <td className="font-medium text-text-primary">{k.name}</td>
                    <td className="font-mono text-small text-text-secondary">{k.mask}</td>
                    <td className="text-text-secondary">{k.scope || t('keys.scope_all_label')}</td>
                    <td className="text-text-secondary">{k.rpm_limit || t('keys.rpm_unlimited')}</td>
                    <td className="text-right tabular-nums">
                      <span className="font-medium text-text-primary">{fmtPoints(s.call_total)}</span>
                      {s.call_failed > 0 && (
                        <span className="ml-1 text-tiny text-danger">↓{fmtPoints(s.call_failed)}</span>
                      )}
                    </td>
                    <td className="text-right tabular-nums font-medium text-text-primary">
                      {fmtPoints(s.consumed_points)}
                    </td>
                    <td className="text-right tabular-nums text-text-tertiary">
                      {s.refunded_points > 0 ? fmtPoints(s.refunded_points) : '—'}
                    </td>
                    <td className="text-text-tertiary text-small">{fmtRelative(s.last_called_at || k.last_used_at)}</td>
                    <td>
                      <div className="flex justify-end gap-1">
                        <button
                          title={k.status === STATUS_ENABLED ? t('keys.tip_disable') : t('keys.tip_enable')}
                          className={clsx(
                            'btn btn-ghost btn-icon btn-sm',
                            k.status === STATUS_ENABLED ? '' : 'text-warning',
                          )}
                          onClick={() => toggleMut.mutate({ id: k.id, enable: k.status !== STATUS_ENABLED })}
                        >
                          <Power size={16} />
                        </button>
                        <button
                          title={t('keys.tip_delete')}
                          className="btn btn-danger-ghost btn-icon btn-sm"
                          onClick={async () => {
                            const ok = await confirm({
                              title: t('keys.del_dialog_title'),
                              description: (
                                <>
                                  {t('keys.del_dialog_desc_prefix')} <span className="font-mono text-text-primary">{k.name}</span>{t('keys.del_dialog_desc_suffix')}
                                </>
                              ),
                              tone: 'danger',
                              confirmLabel: t('keys.del_dialog_confirm'),
                            });
                            if (ok) removeMut.mutate(k.id);
                          }}
                        >
                          <Trash2 size={16} />
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>

      {showCreate && (
        <CreateKeyDialog
          onClose={() => setShowCreate(false)}
          onSubmit={(body) => createMut.mutate(body)}
          submitting={createMut.isPending}
        />
      )}
      {confirmDialog}
    </div>
  );
}

function StatTile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-md border border-border bg-surface-2/40 px-3 py-2.5">
      <div className="text-tiny text-text-tertiary">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums text-text-primary">{value}</div>
      {hint && <div className="mt-0.5 text-tiny text-text-tertiary">{hint}</div>}
    </div>
  );
}

interface CreateKeyDialogProps {
  onClose: () => void;
  onSubmit: (body: {
    name: string;
    scope?: string;
    rpm_limit?: number;
    daily_quota?: number;
    expire_days?: number;
  }) => void;
  submitting: boolean;
}

function CreateKeyDialog({ onClose, onSubmit, submitting }: CreateKeyDialogProps) {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [scope, setScope] = useState(SCOPE_OPTIONS[0]!.value);
  const [rpm, setRpm] = useState(60);
  const [expireDays, setExpireDays] = useState(0);

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-4 backdrop-blur-sm">
      <div className="dialog-surface w-full max-w-md p-6 klein-fade-in">
        <header className="flex items-center justify-between mb-5">
          <h2 className="text-h3 text-text-primary">{t('keys.create_dialog_title')}</h2>
          <button className="btn btn-ghost btn-icon btn-sm" onClick={onClose} aria-label={t('common.close')}>
            <X size={16} />
          </button>
        </header>

        <div className="space-y-4">
          <div className="field">
            <label className="field-label">{t('keys.create_name_label')}</label>
            <input
              className="input"
              placeholder={t('keys.create_name_placeholder')}
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={64}
            />
          </div>

          <div className="field">
            <label className="field-label">{t('keys.create_scope_label')}</label>
            <select className="select" value={scope} onChange={(e) => setScope(e.target.value)}>
              {SCOPE_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.labelKey)}
                </option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="field">
              <label className="field-label">{t('keys.create_rpm_label')}</label>
              <input
                type="number"
                className="input"
                value={rpm}
                min={0}
                max={10000}
                onChange={(e) => setRpm(Number(e.target.value) || 0)}
              />
            </div>
            <div className="field">
              <label className="field-label">{t('keys.create_expire_label')}</label>
              <input
                type="number"
                className="input"
                value={expireDays}
                min={0}
                max={3650}
                onChange={(e) => setExpireDays(Number(e.target.value) || 0)}
              />
            </div>
          </div>
        </div>

        <div className="mt-6 flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>{t('common.cancel')}</button>
          <button
            className="btn btn-primary btn-md"
            disabled={!name.trim() || submitting}
            onClick={() =>
              onSubmit({
                name: name.trim(),
                scope,
                rpm_limit: rpm,
                expire_days: expireDays,
              })
            }
          >
            {submitting ? t('keys.create_creating') : t('keys.create_btn_text')}
          </button>
        </div>
      </div>
    </div>
  );
}

function CreatedKeyCard({ data, onClose }: { data: APIKeyCreated; onClose: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <section className="card-tinted card-section mb-5 klein-fade-in">
      <header className="flex items-center justify-between mb-3">
        <h3 className="text-h4 text-text-primary">{t('keys.created_card_title', { name: data.name })}</h3>
        <button className="btn btn-ghost btn-icon btn-sm" onClick={onClose} aria-label={t('common.close')}>
          <X size={16} />
        </button>
      </header>
      <p className="text-small text-text-secondary mb-3">
        {t('keys.created_card_desc')}
      </p>
      <div className="flex flex-col sm:flex-row items-stretch gap-2">
        <code className="flex-1 rounded-sm bg-surface-1 border border-border px-4 py-3 font-mono text-body break-all">
          {data.plain}
        </code>
        <button
          className="btn btn-primary btn-lg sm:min-w-[120px]"
          onClick={() => {
            navigator.clipboard.writeText(data.plain).then(() => {
              setCopied(true);
              toast.success(t('keys.copied_clipboard'));
              setTimeout(() => setCopied(false), 2000);
            });
          }}
        >
          {copied ? <Check size={16} /> : <Copy size={16} />}
          {copied ? t('keys.copied_btn') : t('keys.copy_btn')}
        </button>
      </div>
    </section>
  );
}
