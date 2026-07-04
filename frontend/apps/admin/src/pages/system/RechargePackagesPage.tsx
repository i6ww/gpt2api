import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, Plus, RefreshCw, Save, Trash2, WalletCards } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { systemApi } from '../../lib/services';
import type { SystemSettings } from '../../lib/types';
import { toast } from '../../stores/toast';
import {
  PageHeader,
  PageShell,
  Section,
  Stat,
  StatRow,
} from '../../components/layout/PageShell';

interface RechargePackage {
  id: string;
  name: string;
  amount: number;
  points: number;
  bonus_points: number;
  enabled: boolean;
  sort_order: number;
  badge: string;
  remark: string;
}

const DEFAULT_ROWS: RechargePackage[] = [
  { id: 'p100', name: '100 点套餐', amount: 10, points: 100, bonus_points: 0, enabled: true, sort_order: 10, badge: '', remark: '' },
  { id: 'p500', name: '500 点套餐', amount: 45, points: 500, bonus_points: 50, enabled: true, sort_order: 20, badge: '推荐', remark: '' },
];

const asNum = (v: unknown, fallback: number) => {
  const n = Number(v);
  return Number.isFinite(n) ? n : fallback;
};
const asBool = (v: unknown, fallback = false) => (v == null ? fallback : Boolean(v));

function fromValue(v: unknown): RechargePackage[] {
  if (!Array.isArray(v)) return DEFAULT_ROWS;
  return v.map((item, idx) => {
    const row = item as Partial<RechargePackage>;
    return {
      id: String(row.id || `pkg_${idx + 1}`),
      name: String(row.name || ''),
      amount: asNum(row.amount, 0),
      points: asNum(row.points, 0) / 100,
      bonus_points: asNum(row.bonus_points, 0) / 100,
      enabled: asBool(row.enabled, true),
      sort_order: asNum(row.sort_order, (idx + 1) * 10),
      badge: String(row.badge || ''),
      remark: String(row.remark || ''),
    };
  });
}

function toPayload(
  rows: RechargePackage[],
  contactEmail: string,
  contactNotice: string,
): Partial<SystemSettings> {
  return {
    'recharge.packages': rows.map((row) => ({
      id: row.id.trim(),
      name: row.name.trim(),
      amount: Number(row.amount) || 0,
      points: Math.round((Number(row.points) || 0) * 100),
      bonus_points: Math.round((Number(row.bonus_points) || 0) * 100),
      enabled: row.enabled,
      sort_order: Number(row.sort_order) || 0,
      badge: row.badge.trim(),
      remark: row.remark.trim(),
    })),
    // 客服联系方式：在线支付未接通时，用户端 BillingPage 的"立即购买"会弹这个邮箱。
    'recharge.contact_email': contactEmail.trim(),
    'recharge.contact_notice': contactNotice.trim(),
  };
}

export default function RechargePackagesPage() {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ['admin', 'system', 'settings'], queryFn: () => systemApi.get() });
  const [rows, setRows] = useState<RechargePackage[]>(DEFAULT_ROWS);
  const [contactEmail, setContactEmail] = useState('');
  const [contactNotice, setContactNotice] = useState('');
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (settings.data) {
      setRows(fromValue(settings.data['recharge.packages']));
      setContactEmail(String(settings.data['recharge.contact_email'] ?? ''));
      setContactNotice(String(settings.data['recharge.contact_notice'] ?? ''));
      setDirty(false);
    }
  }, [settings.data]);

  const totals = useMemo(() => {
    const enabled = rows.filter((r) => r.enabled);
    return {
      total: rows.length,
      enabled: enabled.length,
      disabled: rows.length - enabled.length,
    };
  }, [rows]);

  const update = (idx: number, patch: Partial<RechargePackage>) => {
    setRows((old) => old.map((row, i) => (i === idx ? { ...row, ...patch } : row)));
    setDirty(true);
  };

  const addRow = () => {
    setRows((old) => [
      ...old,
      {
        id: `pkg_${Date.now()}`,
        name: '',
        amount: 0,
        points: 0,
        bonus_points: 0,
        enabled: true,
        sort_order: (old.length + 1) * 10,
        badge: '',
        remark: '',
      },
    ]);
    setDirty(true);
  };

  const cloneRow = (idx: number) => {
    const row = rows[idx];
    if (!row) return;
    setRows((old) => [
      ...old.slice(0, idx + 1),
      { ...row, id: `${row.id}_copy`, name: `${row.name} 副本`, sort_order: row.sort_order + 1 },
      ...old.slice(idx + 1),
    ]);
    setDirty(true);
  };

  const removeRow = (idx: number) => {
    setRows((old) => old.filter((_, i) => i !== idx));
    setDirty(true);
  };

  const save = useMutation({
    mutationFn: () => systemApi.update(toPayload(rows, contactEmail, contactNotice)),
    onSuccess: () => {
      toast.success('充值套餐已保存');
      setDirty(false);
      qc.invalidateQueries({ queryKey: ['admin', 'system'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  // 改 contact_email / notice 也要触发 dirty。
  const setContactEmailDirty = (v: string) => {
    setContactEmail(v);
    setDirty(true);
  };
  const setContactNoticeDirty = (v: string) => {
    setContactNotice(v);
    setDirty(true);
  };

  return (
    <PageShell>
      <PageHeader
        icon={<WalletCards size={16} />}
        title="充值套餐"
        right={
          <>
            <button
              className="btn btn-outline btn-sm"
              onClick={() => settings.refetch()}
              disabled={settings.isFetching}
            >
              <RefreshCw size={14} className={settings.isFetching ? 'animate-spin' : ''} /> 重载
            </button>
            <button className="btn btn-outline btn-sm" onClick={addRow}>
              <Plus size={14} /> 新增
            </button>
            <button
              className="btn btn-primary btn-sm"
              onClick={() => save.mutate()}
              disabled={!dirty || save.isPending}
            >
              <Save size={14} /> {save.isPending ? '保存中…' : dirty ? '保存' : '已最新'}
            </button>
          </>
        }
      />

      <StatRow cols={4}>
        <Stat label="套餐总数" value={totals.total} />
        <Stat label="已启用" value={totals.enabled} tone="text-success" />
        <Stat label="已停用" value={totals.disabled} tone="text-text-tertiary" />
        <Stat label="未保存" value={dirty ? '是' : '否'} tone={dirty ? 'text-warn' : 'text-text-tertiary'} />
      </StatRow>

      <Section title="套餐列表" bodyClass="p-0">
        {settings.isLoading ? (
          <div className="p-6 text-center text-text-tertiary">加载中…</div>
        ) : (
          <>
            <div className="space-y-2 p-3 md:hidden">
              {rows.map((row, idx) => (
                <div key={`${row.id}-${idx}`} className="rounded-md border border-border bg-surface-2 p-3">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="truncate text-small font-semibold text-text-primary">
                        {row.name || '未命名套餐'}
                      </div>
                      <div className="mt-0.5 font-mono text-tiny text-text-tertiary">{row.id}</div>
                    </div>
                    <span
                      className={
                        'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                        (row.enabled ? 'bg-success-soft text-success' : 'bg-surface-3 text-text-tertiary')
                      }
                    >
                      {row.enabled ? '启用' : '停用'}
                    </span>
                  </div>
                  <div className="mt-2 grid grid-cols-2 gap-1.5 text-tiny">
                    <Cell label="金额（元）" value={row.amount} />
                    <Cell label="基础积分" value={row.points} />
                    <Cell label="赠送积分" value={row.bonus_points} />
                    <Cell label="排序" value={row.sort_order} />
                    <Cell label="标签" value={row.badge || '-'} colSpan={2} />
                  </div>
                  <div className="mt-2 flex flex-wrap justify-end gap-1">
                    <button
                      className="btn btn-outline btn-xs"
                      onClick={() => update(idx, { enabled: !row.enabled })}
                    >
                      {row.enabled ? '停用' : '启用'}
                    </button>
                    <button className="btn btn-ghost btn-xs" onClick={() => cloneRow(idx)}>
                      <Copy size={12} /> 复制
                    </button>
                    <button className="btn btn-ghost btn-xs text-danger" onClick={() => removeRow(idx)}>
                      <Trash2 size={12} /> 删除
                    </button>
                  </div>
                </div>
              ))}
              {rows.length === 0 && (
                <div className="rounded-md border border-border bg-surface-2 p-6 text-center text-text-tertiary">
                  暂无套餐
                </div>
              )}
            </div>

            <div className="hidden overflow-x-auto md:block">
              <table className="min-w-full text-small">
                <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
                  <tr>
                    <th className="px-2 py-2 text-left">排序</th>
                    <th className="px-2 py-2 text-left">套餐 ID</th>
                    <th className="px-2 py-2 text-left">名称</th>
                    <th className="px-2 py-2 text-left">金额（元）</th>
                    <th className="px-2 py-2 text-left">基础（点）</th>
                    <th className="px-2 py-2 text-left">赠送（点）</th>
                    <th className="px-2 py-2 text-left">标签</th>
                    <th className="px-2 py-2 text-left">备注</th>
                    <th className="px-2 py-2 text-left">状态</th>
                    <th className="px-2 py-2 text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row, idx) => (
                    <tr key={`${row.id}-${idx}`} className="border-b border-border last:border-0">
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-16 tabular-nums"
                          type="number"
                          value={row.sort_order}
                          onChange={(e) => update(idx, { sort_order: Number(e.target.value) || 0 })}
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-32 font-mono"
                          value={row.id}
                          onChange={(e) => update(idx, { id: e.target.value })}
                          placeholder="p100"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-40"
                          value={row.name}
                          onChange={(e) => update(idx, { name: e.target.value })}
                          placeholder="100 点套餐"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-24 tabular-nums"
                          type="number"
                          min={0}
                          step="0.01"
                          value={row.amount}
                          onChange={(e) => update(idx, { amount: Number(e.target.value) || 0 })}
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-24 tabular-nums"
                          type="number"
                          min={0}
                          value={row.points}
                          onChange={(e) => update(idx, { points: Number(e.target.value) || 0 })}
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-24 tabular-nums"
                          type="number"
                          min={0}
                          value={row.bonus_points}
                          onChange={(e) => update(idx, { bonus_points: Number(e.target.value) || 0 })}
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-24"
                          value={row.badge}
                          onChange={(e) => update(idx, { badge: e.target.value })}
                          placeholder="推荐"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <input
                          className="input input-sm w-40"
                          value={row.remark}
                          onChange={(e) => update(idx, { remark: e.target.value })}
                          placeholder="内部备注"
                        />
                      </td>
                      <td className="px-2 py-1.5">
                        <button
                          className={
                            'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                            (row.enabled
                              ? 'bg-success-soft text-success'
                              : 'bg-surface-3 text-text-tertiary')
                          }
                          onClick={() => update(idx, { enabled: !row.enabled })}
                        >
                          {row.enabled ? '启用' : '停用'}
                        </button>
                      </td>
                      <td className="px-2 py-1.5 text-right">
                        <div className="flex justify-end gap-1">
                          <button
                            className="btn btn-ghost btn-icon btn-xs"
                            title="复制"
                            onClick={() => cloneRow(idx)}
                          >
                            <Copy size={13} />
                          </button>
                          <button
                            className="btn btn-ghost btn-icon btn-xs text-danger"
                            title="删除"
                            onClick={() => removeRow(idx)}
                          >
                            <Trash2 size={13} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                  {rows.length === 0 && (
                    <tr>
                      <td colSpan={10} className="py-8 text-center text-text-tertiary">
                        暂无套餐，点击右上角"新增"
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </>
        )}
      </Section>

      <Section title="客服联系方式（线下支付）">
        <div className="space-y-3">
          <p className="text-tiny leading-5 text-text-tertiary">
            未接入在线支付前，用户在 BillingPage 点击套餐 → 弹窗会展示这个邮箱，并预填邮件正文（含 UID + 套餐 ID）。
          </p>
          <label className="block">
            <div className="mb-1 text-tiny text-text-tertiary">客服邮箱（recharge.contact_email）</div>
            <input
              className="input input-md w-full sm:max-w-md"
              type="email"
              value={contactEmail}
              onChange={(e) => setContactEmailDirty(e.target.value)}
              placeholder="support@yourdomain.com"
            />
          </label>
          <label className="block">
            <div className="mb-1 text-tiny text-text-tertiary">
              给用户的购买说明（recharge.contact_notice，可空，用户端默认会展示一段标准提示）
            </div>
            <textarea
              className="input input-md w-full"
              rows={3}
              value={contactNotice}
              onChange={(e) => setContactNoticeDirty(e.target.value)}
              placeholder="如：付款后请将转账截图一并发到客服邮箱，工作时间 24h 内到账。"
            />
          </label>
          <p className="text-tiny text-text-tertiary">
            提示：邮箱为空时，用户端会显示「运营尚未配置客服邮箱」的兜底提示，不影响套餐列表展示。
          </p>
        </div>
      </Section>
    </PageShell>
  );
}

function Cell({ label, value, colSpan = 1 }: { label: string; value: number | string; colSpan?: number }) {
  return (
    <div
      className="rounded bg-surface-1 px-2 py-1"
      style={{ gridColumn: colSpan === 2 ? 'span 2 / span 2' : undefined }}
    >
      <div className="text-text-tertiary">{label}</div>
      <div className="mt-0.5 truncate font-medium text-text-secondary">{value}</div>
    </div>
  );
}
