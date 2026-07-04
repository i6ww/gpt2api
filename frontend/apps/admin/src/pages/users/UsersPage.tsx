import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Ban,
  CheckCircle2,
  KeyRound,
  MinusCircle,
  Pencil,
  Plus,
  PlusCircle,
  RefreshCw,
  Search,
  Users,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { fmtPoints, fmtRelative, fmtTime } from '../../lib/format';
import { usersApi } from '../../lib/services';
import type { AdminUserCreateBody, AdminUserItem, AdminUserUpdateBody } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';

type UserDialog =
  | { mode: 'create' }
  | { mode: 'edit'; row: AdminUserItem }
  | { mode: 'password'; row: AdminUserItem }
  | { mode: 'points'; row: AdminUserItem; action: 'recharge' | 'deduct' };

// 套餐选项与 backend/migrations/20260427130010_seed.sql 中的 `plan` 表保持一致。
// 如果运营自建了新套餐，用户可以在编辑表单里选「其他…」自由输入。
const PLAN_OPTIONS: { code: string; label: string }[] = [
  { code: 'free', label: 'Free 免费版' },
  { code: 'pro', label: 'Pro' },
  { code: 'max', label: 'Max' },
];

function toLocalDateTimeInput(unixSec?: number): string {
  if (!unixSec) return '';
  const d = new Date(unixSec * 1000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalDateTimeInput(v: string): number | null {
  if (!v) return null;
  const t = new Date(v).getTime();
  if (Number.isNaN(t)) return null;
  return Math.floor(t / 1000);
}

function toPointUnits(v: string): number {
  return Math.round((Number(v) || 0) * 100);
}

function userName(u: AdminUserItem): string {
  return u.username || u.email || u.phone || `用户 #${u.id}`;
}

export default function UsersPage() {
  const qc = useQueryClient();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [page, setPage] = useState(1);
  const [dlg, setDlg] = useState<UserDialog | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword.trim() || undefined,
      status: status === 'all' ? undefined : status === 'enabled' ? (1 as const) : (0 as const),
      page,
      page_size: pageSize,
    }),
    [keyword, page, pageSize, status],
  );

  const list = useQuery({
    queryKey: ['admin', 'users', query],
    queryFn: () => usersApi.list(query),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'users'] });
  const items = list.data?.list ?? [];
  const total = list.data?.total ?? 0;

  const toggle = useMutation({
    mutationFn: ({ id, next }: { id: number; next: 0 | 1 }) => usersApi.update(id, { status: next }),
    onSuccess: () => { refresh(); toast.success('已更新用户状态'); },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <PageShell>
      <PageHeader
        icon={<Users size={16} />}
        title="用户管理"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={refresh}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setDlg({ mode: 'create' })}>
              <Plus size={14} /> 新增
            </button>
          </>
        }
      />

      <Toolbar>
        <div className="relative min-w-[260px] flex-1">
          <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
          <input
            className="input input-sm pl-7"
            placeholder="搜索 ID / 邮箱 / 手机 / 用户名 / 邀请码"
            value={keyword}
            onChange={(e) => { setKeyword(e.target.value); setPage(1); }}
          />
        </div>
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => { setStatus(e.target.value as typeof status); setPage(1); }}
        >
          <option value="all">全部状态</option>
          <option value="enabled">正常</option>
          <option value="disabled">暂停</option>
        </select>
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary whitespace-nowrap">共 {total} 个用户</span>
      </Toolbar>

      <div className="space-y-3 md:hidden">
        {list.isLoading && (
          <div className="rounded-xl border border-border bg-surface-1 px-3 py-10 text-center text-text-tertiary">加载中...</div>
        )}
        {!list.isLoading && items.length === 0 && (
          <div className="rounded-xl border border-border bg-surface-1 px-3 py-10 text-center text-text-tertiary">暂无用户</div>
        )}
        {items.map((u) => {
          const enabled = u.status === 1;
          return (
            <div key={u.id} className="rounded-xl border border-border bg-surface-1 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-body font-semibold text-text-primary">{userName(u)}</div>
                  <div className="mt-1 text-tiny text-text-tertiary">
                    ID {u.id} · {u.email || u.phone || u.uuid}
                  </div>
                  <div className="mt-1 text-tiny text-text-tertiary">邀请码 {u.invite_code || '—'}</div>
                </div>
                <span className={enabled ? 'badge badge-success' : 'badge badge-warning'}>
                  {enabled ? '正常' : '暂停'}
                </span>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 text-tiny">
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">积分</div>
                  <div className="text-text-secondary">{fmtPoints(u.points)}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">方案</div>
                  <div className="text-text-secondary">{u.plan_code || 'free'}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">冻结 / 累充</div>
                  <div className="text-text-secondary">
                    {fmtPoints(u.frozen_points)} · {fmtPoints(u.total_recharge)}
                  </div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">登录</div>
                  <div className="truncate text-text-secondary">
                    {u.last_login_at ? fmtRelative(u.last_login_at) : '未登录'}
                  </div>
                </div>
              </div>
              <div className="mt-3 flex flex-wrap gap-2">
                <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'edit', row: u })}>
                  <Pencil size={14} /> 编辑
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'password', row: u })}>
                  <KeyRound size={14} /> 改密码
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'points', row: u, action: 'recharge' })}>
                  <PlusCircle size={14} /> 充值
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'points', row: u, action: 'deduct' })}>
                  <MinusCircle size={14} /> 扣除
                </button>
                <button
                  className={enabled ? 'btn btn-danger-ghost btn-sm' : 'btn btn-ghost btn-sm'}
                  disabled={toggle.isPending}
                  onClick={() => toggle.mutate({ id: u.id, next: enabled ? 0 : 1 })}
                >
                  {enabled ? <Ban size={14} /> : <CheckCircle2 size={14} />}
                  {enabled ? '停用' : '启用'}
                </button>
              </div>
            </div>
          );
        })}
      </div>

      <Section bodyClass="p-0">
      <div className="hidden table-wrap md:block">
        <table className="data-table">
          <thead>
            <tr>
              <th>用户</th>
              <th>状态</th>
              <th>积分</th>
              <th>套餐</th>
              <th>注册 / 登录</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.isLoading && (
              <tr><td colSpan={6} className="text-center text-text-tertiary py-10">加载中…</td></tr>
            )}
            {!list.isLoading && items.length === 0 && (
              <tr><td colSpan={6} className="text-center text-text-tertiary py-10">暂无用户</td></tr>
            )}
            {items.map((u) => (
              <tr key={u.id}>
                <td>
                  <div className="font-medium">{userName(u)}</div>
                  <div className="text-tiny text-text-tertiary mt-0.5">
                    ID {u.id} · {u.email || u.phone || u.uuid}
                  </div>
                  <div className="text-tiny text-text-tertiary mt-0.5">邀请码 {u.invite_code || '—'}</div>
                </td>
                <td>
                  <span className={u.status === 1 ? 'badge badge-success' : 'badge badge-warning'}>
                    {u.status === 1 ? '正常' : '暂停'}
                  </span>
                </td>
                <td>
                  <div className="font-semibold">{fmtPoints(u.points)}</div>
                  <div className="text-tiny text-text-tertiary">
                    冻结 {fmtPoints(u.frozen_points)} · 累充 {fmtPoints(u.total_recharge)}
                  </div>
                </td>
                <td>
                  <div>{u.plan_code || 'free'}</div>
                  <div className="text-tiny text-text-tertiary">{u.plan_expire_at ? fmtTime(u.plan_expire_at) : '长期'}</div>
                </td>
                <td>
                  <div className="text-small">{fmtTime(u.created_at)}</div>
                  <div className="text-tiny text-text-tertiary">
                    {u.last_login_at ? `${fmtRelative(u.last_login_at)} · ${u.last_login_ip || '未知 IP'}` : '未登录'}
                  </div>
                </td>
                <td>
                  <div className="inline-flex flex-wrap gap-1">
                    <button className="btn btn-ghost btn-sm" title="编辑用户资料 / 套餐" onClick={() => setDlg({ mode: 'edit', row: u })}>
                      <Pencil size={14} /> 编辑
                    </button>
                    <button className="btn btn-ghost btn-sm" title="重置该用户登录密码" onClick={() => setDlg({ mode: 'password', row: u })}>
                      <KeyRound size={14} /> 改密
                    </button>
                    <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'points', row: u, action: 'recharge' })}>
                      <PlusCircle size={14} /> 充值
                    </button>
                    <button className="btn btn-ghost btn-sm" onClick={() => setDlg({ mode: 'points', row: u, action: 'deduct' })}>
                      <MinusCircle size={14} /> 扣除
                    </button>
                    <button
                      className={u.status === 1 ? 'btn btn-danger-ghost btn-sm' : 'btn btn-ghost btn-sm'}
                      disabled={toggle.isPending}
                      onClick={() => toggle.mutate({ id: u.id, next: u.status === 1 ? 0 : 1 })}
                    >
                      {u.status === 1 ? <Ban size={14} /> : <CheckCircle2 size={14} />}
                      {u.status === 1 ? '暂停' : '启用'}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
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

      {dlg?.mode === 'create' && <UserFormDialog mode="create" onClose={() => setDlg(null)} onDone={refresh} />}
      {dlg?.mode === 'edit' && <UserFormDialog mode="edit" row={dlg.row} onClose={() => setDlg(null)} onDone={refresh} />}
      {dlg?.mode === 'password' && <PasswordDialog row={dlg.row} onClose={() => setDlg(null)} onDone={refresh} />}
      {dlg?.mode === 'points' && <PointsDialog row={dlg.row} action={dlg.action} onClose={() => setDlg(null)} onDone={refresh} />}
    </PageShell>
  );
}

function UserFormDialog(props: {
  mode: 'create' | 'edit';
  row?: AdminUserItem;
  onClose: () => void;
  onDone: () => void;
}) {
  const isCreate = props.mode === 'create';
  // plan_code 在内置列表中 → 使用 select；不在 → 切到「自定义」并填入文本框
  const initialPlanCode = props.row?.plan_code || 'free';
  const initialIsCustomPlan = !isCreate && !PLAN_OPTIONS.some((p) => p.code === initialPlanCode);

  const [body, setBody] = useState({
    account: props.row?.email || props.row?.phone || props.row?.username || '',
    password: '',
    username: props.row?.username || '',
    email: props.row?.email || '',
    phone: props.row?.phone || '',
    plan_code: initialPlanCode,
    plan_custom: initialIsCustomPlan ? initialPlanCode : '',
    plan_is_custom: initialIsCustomPlan,
    plan_expire_at_input: toLocalDateTimeInput(props.row?.plan_expire_at),
    plan_keep_lifetime: !props.row?.plan_expire_at,
    status: (props.row?.status === 0 ? 0 : 1) as 0 | 1,
    points: '',
  });

  const mut = useMutation({
    mutationFn: () => {
      if (isCreate) {
        const payload: AdminUserCreateBody = {
          account: body.account.trim(),
          password: body.password,
          username: body.username.trim() || undefined,
          points: toPointUnits(body.points),
          status: body.status,
        };
        return usersApi.create(payload);
      }
      // 套餐：自定义或从 select 选的
      const finalPlanCode = (body.plan_is_custom ? body.plan_custom : body.plan_code).trim() || 'free';
      // 到期时间：long-term 时显式传 null 清空，否则传 unix 秒
      const planExpireAt = body.plan_keep_lifetime
        ? null
        : fromLocalDateTimeInput(body.plan_expire_at_input);
      const payload: AdminUserUpdateBody = {
        email: body.email.trim() || null,
        phone: body.phone.trim() || null,
        username: body.username.trim() || null,
        plan_code: finalPlanCode,
        plan_expire_at: planExpireAt,
        status: body.status,
      };
      if (body.password.trim()) payload.password = body.password;
      return usersApi.update(props.row!.id, payload).then(() => ({ id: props.row!.id }));
    },
    onSuccess: () => {
      props.onDone();
      props.onClose();
      toast.success(isCreate ? '已新增用户' : '已保存用户');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <div className="modal-backdrop">
      <div className="modal-panel max-w-2xl">
        <header className="modal-header">
          <h2>
            {isCreate ? '新增用户' : `编辑用户 · ${userName(props.row!)}`}
          </h2>
          <button className="btn btn-ghost btn-sm" onClick={props.onClose}>关闭</button>
        </header>
        <div className="modal-body grid gap-4">
          <section className="grid gap-3">
            <div className="text-overline text-text-tertiary">账号资料</div>
            {isCreate ? (
              <label className="field">
                <span>账号</span>
                <input
                  className="input"
                  value={body.account}
                  onChange={(e) => setBody((s) => ({ ...s, account: e.target.value }))}
                  placeholder="邮箱 / 手机号 / 用户名"
                />
              </label>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <label className="field">
                  <span>邮箱</span>
                  <input
                    className="input"
                    value={body.email}
                    onChange={(e) => setBody((s) => ({ ...s, email: e.target.value }))}
                    placeholder="留空即清除"
                  />
                </label>
                <label className="field">
                  <span>手机号</span>
                  <input
                    className="input"
                    value={body.phone}
                    onChange={(e) => setBody((s) => ({ ...s, phone: e.target.value }))}
                    placeholder="留空即清除"
                  />
                </label>
              </div>
            )}
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <label className="field">
                <span>用户名</span>
                <input
                  className="input"
                  value={body.username}
                  onChange={(e) => setBody((s) => ({ ...s, username: e.target.value }))}
                />
              </label>
              <label className="field">
                <span>{isCreate ? '初始密码' : '登录密码（留空不修改）'}</span>
                <input
                  className="input"
                  type="password"
                  autoComplete="new-password"
                  value={body.password}
                  onChange={(e) => setBody((s) => ({ ...s, password: e.target.value }))}
                  placeholder={isCreate ? '至少 6 位' : '留空保留原密码'}
                />
              </label>
            </div>
          </section>

          <section className="grid gap-3">
            <div className="text-overline text-text-tertiary">套餐与状态</div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <label className="field">
                <span>套餐</span>
                <div className="flex gap-2">
                  <select
                    className="input flex-1"
                    value={body.plan_is_custom ? '__custom__' : body.plan_code}
                    onChange={(e) => {
                      const v = e.target.value;
                      if (v === '__custom__') {
                        setBody((s) => ({ ...s, plan_is_custom: true, plan_custom: s.plan_custom || s.plan_code }));
                      } else {
                        setBody((s) => ({ ...s, plan_is_custom: false, plan_code: v }));
                      }
                    }}
                  >
                    {PLAN_OPTIONS.map((p) => (
                      <option key={p.code} value={p.code}>{p.label} ({p.code})</option>
                    ))}
                    <option value="__custom__">其他…（自定义 plan_code）</option>
                  </select>
                </div>
              </label>
              <label className="field">
                <span>状态</span>
                <select
                  className="input"
                  value={body.status}
                  onChange={(e) => setBody((s) => ({ ...s, status: Number(e.target.value) as 0 | 1 }))}
                >
                  <option value={1}>正常</option>
                  <option value={0}>暂停</option>
                </select>
              </label>
            </div>
            {body.plan_is_custom && (
              <label className="field">
                <span>自定义套餐 code</span>
                <input
                  className="input"
                  value={body.plan_custom}
                  onChange={(e) => setBody((s) => ({ ...s, plan_custom: e.target.value }))}
                  placeholder="必须与 plan 表中已存在的 code 一致"
                />
              </label>
            )}
            {!isCreate && (
              <div className="grid grid-cols-1 md:grid-cols-[1fr_auto] gap-3 items-end">
                <label className="field">
                  <span>套餐到期时间</span>
                  <input
                    type="datetime-local"
                    className="input"
                    value={body.plan_expire_at_input}
                    disabled={body.plan_keep_lifetime}
                    onChange={(e) =>
                      setBody((s) => ({ ...s, plan_expire_at_input: e.target.value, plan_keep_lifetime: false }))
                    }
                  />
                </label>
                <label className="inline-flex items-center gap-2 text-small pb-2">
                  <input
                    type="checkbox"
                    checked={body.plan_keep_lifetime}
                    onChange={(e) => setBody((s) => ({ ...s, plan_keep_lifetime: e.target.checked }))}
                  />
                  <span>长期有效</span>
                </label>
              </div>
            )}
            {isCreate && (
              <label className="field">
                <span>初始积分</span>
                <input
                  className="input"
                  inputMode="decimal"
                  value={body.points}
                  onChange={(e) => setBody((s) => ({ ...s, points: e.target.value.replace(/[^\d.]/g, '') }))}
                  placeholder="例如 100"
                />
              </label>
            )}
          </section>
        </div>
        <footer className="modal-footer">
          <button className="btn btn-outline" onClick={props.onClose}>取消</button>
          <button className="btn btn-primary" disabled={mut.isPending} onClick={() => mut.mutate()}>
            {mut.isPending ? '保存中…' : '保存'}
          </button>
        </footer>
      </div>
    </div>
  );
}

function PasswordDialog(props: {
  row: AdminUserItem;
  onClose: () => void;
  onDone: () => void;
}) {
  const [pwd, setPwd] = useState('');
  const [pwd2, setPwd2] = useState('');
  const tooShort = pwd.length > 0 && pwd.length < 6;
  const mismatch = pwd2.length > 0 && pwd2 !== pwd;
  const canSubmit = pwd.length >= 6 && !mismatch;

  const mut = useMutation({
    mutationFn: () => usersApi.update(props.row.id, { password: pwd }),
    onSuccess: () => {
      props.onDone();
      props.onClose();
      toast.success(`已为 ${userName(props.row)} 重置登录密码`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <div className="modal-backdrop">
      <div className="modal-panel max-w-lg">
        <header className="modal-header">
          <h2>重置登录密码 · {userName(props.row)}</h2>
          <button className="btn btn-ghost btn-sm" onClick={props.onClose}>关闭</button>
        </header>
        <div className="modal-body grid gap-3">
          <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-small text-warning">
            重置后该用户原密码立即失效，请通过安全渠道告知新密码。
          </div>
          <label className="field">
            <span>新密码</span>
            <input
              className="input"
              type="password"
              autoComplete="new-password"
              value={pwd}
              onChange={(e) => setPwd(e.target.value)}
              placeholder="至少 6 位"
            />
            {tooShort && <span className="text-tiny text-danger mt-1">密码长度至少 6 位</span>}
          </label>
          <label className="field">
            <span>确认新密码</span>
            <input
              className="input"
              type="password"
              autoComplete="new-password"
              value={pwd2}
              onChange={(e) => setPwd2(e.target.value)}
            />
            {mismatch && <span className="text-tiny text-danger mt-1">两次输入的密码不一致</span>}
          </label>
        </div>
        <footer className="modal-footer">
          <button className="btn btn-outline" onClick={props.onClose}>取消</button>
          <button
            className="btn btn-primary"
            disabled={!canSubmit || mut.isPending}
            onClick={() => mut.mutate()}
          >
            {mut.isPending ? '提交中…' : '确认重置'}
          </button>
        </footer>
      </div>
    </div>
  );
}

function PointsDialog(props: {
  row: AdminUserItem;
  action: 'recharge' | 'deduct';
  onClose: () => void;
  onDone: () => void;
}) {
  const [points, setPoints] = useState('');
  const isRecharge = props.action === 'recharge';
  const [remark, setRemark] = useState(isRecharge ? '管理员充值' : '管理员扣除');
  const pointUnits = toPointUnits(points);
  const nextPoints = props.row.points + (isRecharge ? pointUnits : -pointUnits);
  const isValid = pointUnits > 0 && (isRecharge || nextPoints >= 0);

  const mut = useMutation({
    mutationFn: () => usersApi.adjustPoints(props.row.id, {
      action: props.action,
      points: pointUnits,
      remark: remark.trim() || (isRecharge ? '管理员充值' : '管理员扣除'),
    }),
    onSuccess: (r) => {
      props.onDone();
      props.onClose();
      toast.success(`${isRecharge ? '充值' : '扣除'}成功：${fmtPoints(r.points_before)} → ${fmtPoints(r.points_after)}`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  return (
    <div className="modal-backdrop">
      <div className="modal-panel max-w-lg">
        <header className="modal-header">
          <h2>{isRecharge ? '充值积分' : '扣除积分'}</h2>
          <button className="btn btn-ghost btn-sm" onClick={props.onClose}>关闭</button>
        </header>
        <div className="modal-body grid gap-3">
          <div className="rounded-lg border border-border bg-surface-2 p-3 text-small">
            <div className="text-text-secondary">{userName(props.row)}</div>
            <div className="mt-2 grid grid-cols-3 gap-2">
              <div>
                <div className="text-text-tertiary">当前可用</div>
                <div className="mt-1 text-lg">{fmtPoints(props.row.points)}</div>
              </div>
              <div>
                <div className="text-text-tertiary">{isRecharge ? '本次充值' : '本次扣除'}</div>
                <div className={isRecharge ? 'mt-1 text-lg text-success' : 'mt-1 text-lg text-danger'}>
                  {pointUnits > 0 ? `${isRecharge ? '+' : '-'}${fmtPoints(pointUnits)}` : '-'}
                </div>
              </div>
              <div>
                <div className="text-text-tertiary">调整后</div>
                <div className="mt-1 text-lg">{pointUnits > 0 ? fmtPoints(nextPoints) : '-'}</div>
              </div>
            </div>
          </div>
          <label className="field">
            <span>{isRecharge ? '充值积分' : '扣除积分'}</span>
            <input
              className="input"
              value={points}
              inputMode="decimal"
              onChange={(e) => setPoints(e.target.value.replace(/[^\d.]/g, ''))}
              placeholder="例如 100"
            />
          </label>
          <div className="flex flex-wrap gap-2">
            {['100', '500', '1000', '5000'].map((v) => (
              <button key={v} type="button" className="btn btn-outline btn-sm" onClick={() => setPoints(v)}>
                {v} 点
              </button>
            ))}
          </div>
          {!isRecharge && pointUnits > props.row.points && (
            <div className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-small text-danger">
              扣除金额不能超过用户当前可用积分。
            </div>
          )}
          <label className="field">
            <span>备注</span>
            <input className="input" value={remark} onChange={(e) => setRemark(e.target.value)} placeholder="显示在钱包流水中" />
          </label>
          <div className="text-tiny text-text-tertiary">
            充值会计入用户累计充值，扣除只记录人工扣除流水，不会减少累计充值。
          </div>
        </div>
        <footer className="modal-footer">
          <button className="btn btn-outline" onClick={props.onClose}>取消</button>
          <button className={isRecharge ? 'btn btn-primary' : 'btn btn-danger'} disabled={mut.isPending || !isValid} onClick={() => mut.mutate()}>
            {mut.isPending ? '处理中…' : isRecharge ? '确认充值' : '确认扣除'}
          </button>
        </footer>
      </div>
    </div>
  );
}
