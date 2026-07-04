import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Edit3, Plus, RefreshCw, Search, Tag, Trash2, X } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';

import { ApiError } from '../../lib/api';
import { promoApi } from '../../lib/services';
import type { AdminPromoBody, AdminPromoItem } from '../../lib/types';
import { fmtNumber, fmtPoints, fmtTime } from '../../lib/format';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

interface FormState {
  id?: number;
  code: string;
  name: string;
  discount_type: 1 | 2 | 3;
  discount_val: number;
  min_amount: number;
  apply_to: string;
  total_qty: number;
  per_user_limit: number;
  start_at: string;
  end_at: string;
  status: 0 | 1;
}

const DEFAULT_FORM: FormState = {
  code: '',
  name: '',
  discount_type: 1,
  discount_val: 0,
  min_amount: 0,
  apply_to: 'all',
  total_qty: 0,
  per_user_limit: 1,
  start_at: '',
  end_at: '',
  status: 1,
};

export default function PromoPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | '0' | '1'>('');
  const [discountType, setDiscountType] = useState<'' | '1' | '2' | '3'>('');
  const [page, setPage] = useState(1);
  const [form, setForm] = useState<FormState | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useQuery({
    queryKey: ['admin', 'promo', keyword, status, discountType, page],
    queryFn: () =>
      promoApi.list({
        keyword: keyword.trim() || undefined,
        status: status === '' ? '' : (Number(status) as 0 | 1),
        discount_type: discountType === '' ? '' : (Number(discountType) as 1 | 2 | 3),
        page,
        page_size: pageSize,
      }),
  });

  const rows = query.data?.list ?? [];
  const total = query.data?.total ?? 0;

  const save = useMutation({
    mutationFn: (f: FormState) => {
      const body = formToBody(f);
      return f.id ? promoApi.update(f.id, body) : promoApi.create(body).then(() => undefined);
    },
    onSuccess: () => {
      toast.success('优惠码已保存');
      setForm(null);
      qc.invalidateQueries({ queryKey: ['admin', 'promo'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  const remove = useMutation({
    mutationFn: (id: number) => promoApi.remove(id),
    onSuccess: () => {
      toast.success('优惠码已删除');
      qc.invalidateQueries({ queryKey: ['admin', 'promo'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  const toggle = useMutation({
    mutationFn: (row: AdminPromoItem) => promoApi.update(row.id, { status: row.status === 1 ? 0 : 1 }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'promo'] }),
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  return (
    <PageShell>
      <PageHeader
        icon={<Tag size={16} />}
        title="优惠码"
        right={
          <>
            <button
              className="btn btn-outline btn-sm"
              onClick={() => query.refetch()}
              disabled={query.isFetching}
            >
              <RefreshCw size={14} className={query.isFetching ? 'animate-spin' : ''} /> 刷新
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setForm(DEFAULT_FORM)}>
              <Plus size={14} /> 新增
            </button>
          </>
        }
      />

      <Section
        title="优惠码列表"
        right={
          <>
            <select
              className="select select-sm"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as typeof status);
                setPage(1);
              }}
            >
              <option value="">全部状态</option>
              <option value="1">启用</option>
              <option value="0">停用</option>
            </select>
            <select
              className="select select-sm"
              value={discountType}
              onChange={(e) => {
                setDiscountType(e.target.value as typeof discountType);
                setPage(1);
              }}
            >
              <option value="">全部类型</option>
              <option value="1">满减</option>
              <option value="2">折扣</option>
              <option value="3">赠点</option>
            </select>
            <div className="relative w-60">
              <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
              <input
                className="input input-sm pl-7"
                value={keyword}
                onChange={(e) => {
                  setKeyword(e.target.value);
                  setPage(1);
                }}
                placeholder="搜索优惠码 / 名称 / ID"
              />
            </div>
          </>
        }
        bodyClass="p-0"
      >
        <div className="overflow-x-auto">
          <table className="min-w-full text-small">
            <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
              <tr>
                <th className="px-3 py-2 text-left">优惠码</th>
                <th className="px-3 py-2 text-left">类型</th>
                <th className="px-3 py-2 text-left">优惠值</th>
                <th className="px-3 py-2 text-left">门槛</th>
                <th className="px-3 py-2 text-left">范围</th>
                <th className="px-3 py-2 text-left">使用</th>
                <th className="px-3 py-2 text-left">每用户</th>
                <th className="px-3 py-2 text-left">有效期</th>
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id} className="border-b border-border last:border-0 hover:bg-surface-2/60">
                  <td className="px-3 py-1.5">
                    <div className="font-mono font-semibold text-text-primary">{row.code}</div>
                    <div className="text-tiny text-text-tertiary">{row.name}</div>
                  </td>
                  <td className="px-3 py-1.5">
                    <span className="inline-flex items-center rounded border border-border bg-surface-2 px-1.5 py-0.5 text-tiny text-text-secondary">
                      {discountLabel(row.discount_type)}
                    </span>
                  </td>
                  <td className="px-3 py-1.5 font-semibold tabular-nums text-text-primary">
                    {discountValue(row)}
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">
                    {row.min_amount > 0 ? `${fmtNumber(row.min_amount / 100)} 元` : '无门槛'}
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">{row.apply_to || 'all'}</td>
                  <td className="px-3 py-1.5 tabular-nums text-text-secondary">
                    {fmtNumber(row.used_qty)} / {row.total_qty > 0 ? fmtNumber(row.total_qty) : '不限'}
                  </td>
                  <td className="px-3 py-1.5 tabular-nums text-text-secondary">
                    {row.per_user_limit > 0 ? `${row.per_user_limit} 次` : '不限'}
                  </td>
                  <td className="whitespace-nowrap px-3 py-1.5 text-tiny text-text-tertiary">
                    {fmtTime(row.start_at)} → {fmtTime(row.end_at)}
                  </td>
                  <td className="px-3 py-1.5">
                    <button
                      className={
                        'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                        (row.status === 1 ? 'bg-success-soft text-success' : 'bg-surface-2 text-text-tertiary')
                      }
                      onClick={() => toggle.mutate(row)}
                    >
                      {row.status === 1 ? '启用' : '停用'}
                    </button>
                  </td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex justify-end gap-1">
                      <button
                        className="btn btn-ghost btn-icon btn-xs"
                        onClick={() => setForm(rowToForm(row))}
                        title="编辑"
                      >
                        <Edit3 size={13} />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除优惠码',
                            description: (
                              <>
                                确认删除优惠码 <span className="font-mono text-text-primary">{row.code}</span>？删除后已使用记录保留，但无法再被领取。
                              </>
                            ),
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) remove.mutate(row.id);
                        }}
                        title="删除"
                      >
                        <Trash2 size={13} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {!query.isLoading && rows.length === 0 && (
                <tr>
                  <td colSpan={10} className="py-10 text-center text-text-tertiary">
                    暂无优惠码
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        <Pager
          total={total}
          page={page}
          pageSize={pageSize}
          onChange={setPage}
          onPageSizeChange={setPageSize}
          sizeOptions={sizeOptions}
        />
      </Section>

      {form && (
        <PromoDialog
          form={form}
          setForm={setForm}
          saving={save.isPending}
          onClose={() => setForm(null)}
          onSave={() => save.mutate(form)}
        />
      )}
      {confirmDialog}
    </PageShell>
  );
}

function PromoDialog({
  form,
  setForm,
  saving,
  onClose,
  onSave,
}: {
  form: FormState;
  setForm: (f: FormState | null) => void;
  saving: boolean;
  onClose: () => void;
  onSave: () => void;
}) {
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setForm({ ...form, [k]: v });
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay p-4">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-3xl overflow-hidden rounded-md border border-border bg-surface-1">
        <header className="flex items-center justify-between border-b border-border px-4 py-3">
          <div>
            <h2 className="text-h5 font-semibold text-text-primary">{form.id ? '编辑优惠码' : '新增优惠码'}</h2>
            <p className="text-tiny text-text-tertiary">金额单位为元，赠点单位为点</p>
          </div>
          <button className="btn btn-ghost btn-icon btn-sm" type="button" onClick={onClose} aria-label="关闭">
            <X size={16} />
          </button>
        </header>
        <div className="grid gap-3 p-4 md:grid-cols-2">
          <Field label="优惠码">
            <input
              className="input input-sm font-mono"
              value={form.code}
              onChange={(e) => set('code', e.target.value.toUpperCase())}
              placeholder="SPRING2026"
            />
          </Field>
          <Field label="名称">
            <input
              className="input input-sm"
              value={form.name}
              onChange={(e) => set('name', e.target.value)}
              placeholder="春季活动"
            />
          </Field>
          <Field label="类型">
            <select
              className="select select-sm"
              value={form.discount_type}
              onChange={(e) => set('discount_type', Number(e.target.value) as 1 | 2 | 3)}
            >
              <option value={1}>满减</option>
              <option value={2}>折扣</option>
              <option value={3}>赠点</option>
            </select>
          </Field>
          <Field
            label={
              form.discount_type === 2
                ? '折扣百分比'
                : form.discount_type === 3
                ? '赠送积分（点）'
                : '减免金额（元）'
            }
          >
            <input
              className="input input-sm tabular-nums"
              type="number"
              min={0}
              value={form.discount_val}
              onChange={(e) => set('discount_val', Number(e.target.value) || 0)}
            />
          </Field>
          <Field label="最低消费（元）">
            <input
              className="input input-sm tabular-nums"
              type="number"
              min={0}
              value={form.min_amount}
              onChange={(e) => set('min_amount', Number(e.target.value) || 0)}
            />
          </Field>
          <Field label="适用范围">
            <input
              className="input input-sm"
              value={form.apply_to}
              onChange={(e) => set('apply_to', e.target.value)}
              placeholder="all / p100 / image"
            />
          </Field>
          <Field label="总数量（0=不限）">
            <input
              className="input input-sm tabular-nums"
              type="number"
              min={0}
              value={form.total_qty}
              onChange={(e) => set('total_qty', Number(e.target.value) || 0)}
            />
          </Field>
          <Field label="每用户限用（0=不限）">
            <input
              className="input input-sm tabular-nums"
              type="number"
              min={0}
              value={form.per_user_limit}
              onChange={(e) => set('per_user_limit', Number(e.target.value) || 0)}
            />
          </Field>
          <Field label="开始时间">
            <input
              className="input input-sm"
              type="datetime-local"
              value={form.start_at}
              onChange={(e) => set('start_at', e.target.value)}
            />
          </Field>
          <Field label="结束时间">
            <input
              className="input input-sm"
              type="datetime-local"
              value={form.end_at}
              onChange={(e) => set('end_at', e.target.value)}
            />
          </Field>
          <Field label="状态">
            <select
              className="select select-sm"
              value={form.status}
              onChange={(e) => set('status', Number(e.target.value) as 0 | 1)}
            >
              <option value={1}>启用</option>
              <option value={0}>停用</option>
            </select>
          </Field>
        </div>
        <div className="flex justify-end gap-2 border-t border-border bg-surface-2/40 px-4 py-3">
          <button className="btn btn-outline btn-sm" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary btn-sm" disabled={saving} onClick={onSave}>
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
    </label>
  );
}

function rowToForm(row: AdminPromoItem): FormState {
  return {
    id: row.id,
    code: row.code,
    name: row.name,
    discount_type: row.discount_type as 1 | 2 | 3,
    discount_val: displayDiscount(row),
    min_amount: row.min_amount / 100,
    apply_to: row.apply_to,
    total_qty: row.total_qty,
    per_user_limit: row.per_user_limit,
    start_at: toLocalInput(row.start_at),
    end_at: toLocalInput(row.end_at),
    status: row.status as 0 | 1,
  };
}

function formToBody(f: FormState): AdminPromoBody {
  return {
    code: f.code.trim(),
    name: f.name.trim(),
    discount_type: f.discount_type,
    discount_val: storedDiscount(f),
    min_amount: Math.round((Number(f.min_amount) || 0) * 100),
    apply_to: f.apply_to.trim() || 'all',
    total_qty: Number(f.total_qty) || 0,
    per_user_limit: Number(f.per_user_limit) || 0,
    start_at: fromLocalInput(f.start_at),
    end_at: fromLocalInput(f.end_at),
    status: f.status,
  };
}

function displayDiscount(row: AdminPromoItem) {
  if (row.discount_type === 1 || row.discount_type === 3) return row.discount_val / 100;
  return row.discount_val;
}

function storedDiscount(f: FormState) {
  if (f.discount_type === 1 || f.discount_type === 3) return Math.round((Number(f.discount_val) || 0) * 100);
  return Math.round(Number(f.discount_val) || 0);
}

function discountValue(row: AdminPromoItem) {
  if (row.discount_type === 1) return `${fmtNumber(row.discount_val / 100)} 元`;
  if (row.discount_type === 2) return `${row.discount_val}%`;
  return `${fmtPoints(row.discount_val)} 点`;
}

function discountLabel(v: number) {
  if (v === 1) return '满减';
  if (v === 2) return '折扣';
  if (v === 3) return '赠点';
  return String(v);
}

function toLocalInput(ts?: number) {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(v: string) {
  if (!v) return 0;
  return Math.floor(new Date(v).getTime() / 1000);
}
