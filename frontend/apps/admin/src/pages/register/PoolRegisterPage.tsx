import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  ChevronRight,
  Loader2,
  Plus,
  RefreshCw,
  Sparkles,
  StopCircle,
  Trash2,
  UserPlus,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import clsx from 'clsx';

import { ApiError } from '../../lib/api';
import { registerTaskApi } from '../../lib/services';
import type {
  RegisterTaskItem,
  RegisterTaskLogEntry,
  RegisterTaskLogLevel,
  RegisterTaskProvider,
  RegisterTaskStatus,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { fmtMs } from '../pools/_shared';
import {
  PageHeader,
  PageShell,
  Pager,
  Section,
  Stat,
  StatRow,
} from '../../components/layout/PageShell';
import { ConfirmDialog } from '../../components/ConfirmDialog';

type ViewMode = RegisterTaskProvider | 'logs';

const TABS: { id: ViewMode; label: string; hint: string; icon: typeof Sparkles }[] = [
  { id: 'adobe', label: 'BANANA 注册', hint: '调用 Banana 注册接口 + 邮箱验证', icon: Sparkles },
  { id: 'grok', label: 'GROK 注册', hint: '注册 X 账号 + 开通 SuperGrok 试用', icon: Sparkles },
  { id: 'gpt', label: 'GPT 注册', hint: '注册 ChatGPT 账号 + 拿 OAuth refresh_token', icon: Sparkles },
  { id: 'logs', label: '实时日志', hint: '聚合三家所有任务的步骤日志，自动刷新', icon: Activity },
];

const STATUS_OPTIONS: { value: '' | RegisterTaskStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '排队中' },
  { value: 'running', label: '运行中' },
  { value: 'success', label: '成功' },
  { value: 'failed', label: '失败' },
  { value: 'cancelled', label: '已取消' },
];

function statusBadge(s: string) {
  switch (s) {
    case 'pending':
      return { label: '排队中', tone: 'bg-info-soft text-klein-500' };
    case 'running':
      return { label: '运行中', tone: 'bg-warn-soft text-warn' };
    case 'success':
      return { label: '成功', tone: 'bg-success-soft text-success' };
    case 'failed':
      return { label: '失败', tone: 'bg-danger-soft text-danger' };
    case 'cancelled':
      return { label: '已取消', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: s, tone: 'bg-surface-2 text-text-secondary' };
  }
}

interface FormState {
  count: number;
  first_name: string;
  last_name: string;
  password: string;
  notes: string;
  trial: boolean;
  country: string;
  proxy_id: string;
}

const DEFAULT_FORM: FormState = {
  count: 1,
  first_name: '',
  last_name: '',
  password: '',
  notes: '',
  trial: true,
  country: '',
  proxy_id: '',
};

export default function PoolRegisterPage() {
  const [tab, setTab] = useState<ViewMode>('adobe');
  const [status, setStatus] = useState<'' | RegisterTaskStatus>('');
  const [keyword, setKeyword] = useState('');
  const [page, setPage] = useState(1);
  const [form, setForm] = useState<FormState>(DEFAULT_FORM);
  const qc = useQueryClient();
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  // 当 tab 是 logs 时 currentProvider 为 undefined，stats 拉所有 provider 总和
  const isLogsView = tab === 'logs';
  const currentProvider = isLogsView ? undefined : (tab as RegisterTaskProvider);

  const query = useMemo(
    () => ({
      provider: currentProvider,
      status: status || undefined,
      keyword: keyword || undefined,
      page,
      page_size: pageSize,
    }),
    [currentProvider, status, keyword, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'register-tasks', query],
    queryFn: () => registerTaskApi.list(query),
    refetchInterval: 5000,
    enabled: !isLogsView,
  });

  const stats = useQuery({
    queryKey: ['admin', 'register-tasks', 'stats', currentProvider ?? 'all'],
    queryFn: () => registerTaskApi.stats(currentProvider),
    refetchInterval: 5000,
  });

  const createMut = useMutation({
    mutationFn: (body: ReturnType<typeof buildPayload>) => registerTaskApi.create(body),
    onSuccess: (res) => {
      toast.success(`已创建 ${res.created} 个注册任务，将在后台依次执行`);
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });
      setForm((s) => ({ ...DEFAULT_FORM, password: s.password }));
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : '提交失败'),
  });

  const cancelMut = useMutation({
    mutationFn: (id: number) => registerTaskApi.cancel(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] }),
    onError: (err) => toast.error(err instanceof ApiError ? err.message : '取消失败'),
  });

  const removeMut = useMutation({
    mutationFn: (id: number) => registerTaskApi.remove(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] }),
    onError: (err) => toast.error(err instanceof ApiError ? err.message : '删除失败'),
  });

  const purgeMut = useMutation({
    mutationFn: (provider?: RegisterTaskProvider) => registerTaskApi.purge(provider),
    onSuccess: (r) => {
      toast.success(`已清空 ${r.deleted} 条已结束任务`);
      setPurgeOpen(false);
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : '清空失败'),
  });
  const [purgeOpen, setPurgeOpen] = useState(false);

  function buildPayload(provider: RegisterTaskProvider) {
    const payload: Record<string, any> = {};
    if (form.first_name.trim()) payload.first_name = form.first_name.trim();
    if (form.last_name.trim()) payload.last_name = form.last_name.trim();
    if (form.password.trim()) payload.password = form.password.trim();
    if (form.notes.trim()) payload.notes = form.notes.trim();
    if (form.proxy_id.trim()) payload.proxy_id = Number(form.proxy_id) || undefined;
    if (provider === 'grok') payload.trial = form.trial;
    if (provider === 'gpt' && form.country.trim()) payload.country = form.country.trim();
    return {
      provider,
      count: Math.max(1, Math.min(5000, Number(form.count) || 1)),
      payload,
    };
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!currentProvider) return;
    createMut.mutate(buildPayload(currentProvider));
  }

  const items: RegisterTaskItem[] = list.data?.list ?? [];
  const total = list.data?.total ?? 0;
  const s = stats.data ?? { total: 0, pending: 0, running: 0, success: 0, failed: 0, cancelled: 0 };

  return (
    <PageShell>
      <PageHeader icon={<UserPlus size={16} />} title="号池注册" />

      <nav className="flex flex-wrap gap-1.5">
        {TABS.map((t) => {
          const Icon = t.icon;
          return (
            <button
              key={t.id}
              type="button"
              className={clsx(
                'inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-small transition',
                tab === t.id
                  ? 'border-transparent bg-klein-gradient text-text-on-klein shadow-glow-soft'
                  : 'border-border bg-surface-1 text-text-secondary hover:bg-surface-2',
              )}
              onClick={() => {
                setTab(t.id);
                setPage(1);
              }}
              title={t.hint}
            >
              <Icon size={13} className={clsx(tab === t.id ? 'text-text-on-klein' : 'text-text-tertiary')} />
              {t.label}
            </button>
          );
        })}
      </nav>

      <StatRow>
        <Stat label="总任务" value={s.total} />
        <Stat label="排队" value={s.pending} tone="text-klein-500" />
        <Stat label="运行中" value={s.running} tone="text-warn" />
        <Stat label="成功" value={s.success} tone="text-success" />
        <Stat label="失败" value={s.failed} tone="text-danger" />
        <Stat label="已取消" value={s.cancelled} tone="text-text-tertiary" />
      </StatRow>

      {isLogsView && <LogsView />}

      {!isLogsView && (
      <>
      <Section title={`提交 ${TABS.find((t) => t.id === tab)?.label} 任务`}>
        <form className="grid gap-2.5 md:grid-cols-3 lg:grid-cols-4" onSubmit={handleSubmit}>
          <CompactField label="数量（1-5000）">
            <input
              type="number"
              min={1}
              max={5000}
              className="input input-sm"
              value={form.count}
              onChange={(e) => setForm((s) => ({ ...s, count: Number(e.target.value) || 1 }))}
              title="单次最多 5000；并发由系统配置 - 号池注册并发数 决定"
            />
          </CompactField>
          <CompactField label="名">
            <input
              className="input input-sm"
              value={form.first_name}
              placeholder="随机"
              onChange={(e) => setForm((s) => ({ ...s, first_name: e.target.value }))}
            />
          </CompactField>
          <CompactField label="姓">
            <input
              className="input input-sm"
              value={form.last_name}
              placeholder="随机"
              onChange={(e) => setForm((s) => ({ ...s, last_name: e.target.value }))}
            />
          </CompactField>
          <CompactField label="密码">
            <input
              className="input input-sm"
              type="password"
              value={form.password}
              placeholder="留空随机"
              onChange={(e) => setForm((s) => ({ ...s, password: e.target.value }))}
            />
          </CompactField>
          <CompactField label="代理 ID">
            <input
              className="input input-sm"
              value={form.proxy_id}
              placeholder="全局"
              onChange={(e) => setForm((s) => ({ ...s, proxy_id: e.target.value.replace(/[^0-9]/g, '') }))}
            />
          </CompactField>
          {tab === 'gpt' && (
            <CompactField label="国家">
              <input
                className="input input-sm"
                value={form.country}
                placeholder="US / SG"
                onChange={(e) => setForm((s) => ({ ...s, country: e.target.value }))}
              />
            </CompactField>
          )}
          {tab === 'grok' && (
            <CompactField label="SuperGrok 试用">
              <button
                type="button"
                role="switch"
                aria-checked={form.trial}
                onClick={() => setForm((s) => ({ ...s, trial: !s.trial }))}
                className={clsx(
                  'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition',
                  form.trial ? 'bg-klein-500' : 'bg-surface-3',
                )}
              >
                <span
                  className={clsx(
                    'inline-block h-5 w-5 rounded-full bg-white shadow transition',
                    form.trial ? 'translate-x-5' : 'translate-x-0.5',
                  )}
                />
              </button>
            </CompactField>
          )}
          <CompactField label="备注">
            <input
              className="input input-sm"
              value={form.notes}
              onChange={(e) => setForm((s) => ({ ...s, notes: e.target.value }))}
            />
          </CompactField>
          <div className="col-span-full flex items-center justify-end gap-2">
            <button
              type="button"
              className="btn btn-outline btn-sm"
              onClick={() => setForm({ ...DEFAULT_FORM })}
            >
              重置
            </button>
            <button type="submit" className="btn btn-primary btn-sm" disabled={createMut.isPending}>
              {createMut.isPending ? (
                <>
                  <Loader2 size={14} className="animate-spin" /> 提交中
                </>
              ) : (
                <>
                  <Plus size={14} /> 创建任务
                </>
              )}
            </button>
          </div>
        </form>
      </Section>

      <Section
        title="任务列表"
        right={
          <>
            <select
              className="select select-sm"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as RegisterTaskStatus | '');
                setPage(1);
              }}
            >
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
            <input
              className="input input-sm w-48"
              placeholder="搜索邮箱"
              value={keyword}
              onChange={(e) => {
                setKeyword(e.target.value);
                setPage(1);
              }}
            />
            <button className="btn btn-outline btn-sm" type="button" onClick={() => list.refetch()}>
              <RefreshCw size={14} className={list.isFetching ? 'animate-spin' : ''} /> 刷新
            </button>
            <button
              className="btn btn-outline btn-sm text-danger hover:bg-danger-soft"
              type="button"
              onClick={() => setPurgeOpen(true)}
              disabled={purgeMut.isPending}
              title="清空当前 provider 的已结束任务（success / failed / cancelled）"
            >
              <Trash2 size={14} /> 清空已结束
            </button>
          </>
        }
        bodyClass="p-0"
      >
        <div className="overflow-x-auto">
          <table className="min-w-full text-small">
            <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
              <tr>
                <th className="px-3 py-2 text-left">任务</th>
                <th className="px-3 py-2 text-left">邮箱</th>
                <th className="px-3 py-2 text-left">状态 / 阶段</th>
                <th className="px-3 py-2 text-left">进度</th>
                <th className="px-3 py-2 text-left">创建</th>
                <th className="px-3 py-2 text-left">耗时</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 && !list.isLoading && (
                <tr>
                  <td colSpan={7} className="py-10 text-center text-text-tertiary">
                    暂无任务
                  </td>
                </tr>
              )}
              {items.map((t) => (
                <TaskRow
                  key={t.id}
                  task={t}
                  onCancel={() => cancelMut.mutate(t.id)}
                  onDelete={() => removeMut.mutate(t.id)}
                  cancelBusy={cancelMut.isPending}
                  deleteBusy={removeMut.isPending}
                />
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
      </>
      )}

      <ConfirmDialog
        open={purgeOpen}
        title={`清空已结束任务${currentProvider ? `（${providerLabel(currentProvider)}）` : ''}`}
        description={
          `将删除${currentProvider ? `当前 ${providerLabel(currentProvider)} 下` : ''}所有 “成功 / 失败 / 已取消” 的任务记录；` +
          '运行中和排队中的任务会保留。删除后无法恢复（账号本身不会受影响）。'
        }
        confirmLabel="确认清空"
        cancelLabel="取消"
        tone="danger"
        loading={purgeMut.isPending}
        onCancel={() => setPurgeOpen(false)}
        onConfirm={() => purgeMut.mutate(currentProvider)}
      />
    </PageShell>
  );
}

function TaskRow({
  task,
  onCancel,
  onDelete,
  cancelBusy,
  deleteBusy,
}: {
  task: RegisterTaskItem;
  onCancel: () => void;
  onDelete: () => void;
  cancelBusy: boolean;
  deleteBusy: boolean;
}) {
  const sb = statusBadge(task.status);
  const elapsed =
    task.finished_at && task.started_at
      ? Math.max(0, Math.round((task.finished_at - task.started_at) / 1000))
      : task.started_at
        ? Math.max(0, Math.round((Date.now() - task.started_at) / 1000))
        : 0;
  const ended = task.status === 'success' || task.status === 'failed' || task.status === 'cancelled';
  return (
    <tr className="border-b border-border last:border-0 hover:bg-surface-2/60">
      <td className="px-3 py-1.5 font-mono text-text-secondary">#{task.id}</td>
      <td className="px-3 py-1.5">
        {task.email ? <span className="font-mono">{task.email}</span> : <span className="text-text-tertiary">— 待领取</span>}
      </td>
      <td className="px-3 py-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <span
            className={`inline-flex items-center rounded px-1.5 py-0.5 text-tiny ${sb.tone}`}
            title={task.error || undefined}
          >
            {sb.label}
          </span>
          {task.step && (
            <span className="inline-flex items-center gap-1 rounded border border-border bg-surface-2 px-1.5 py-0.5 text-tiny text-text-tertiary">
              <ChevronRight size={10} />
              {stepLabel(task.step)}
            </span>
          )}
          {task.error && (
            <span
              className="inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-danger-soft text-[10px] font-semibold text-danger"
              title={task.error}
              aria-label="错误详情"
            >
              !
            </span>
          )}
        </div>
      </td>
      <td className="px-3 py-1.5">
        <div className="flex items-center gap-2">
          <div className="h-1.5 w-24 overflow-hidden rounded-full bg-surface-3">
            <div
              className={clsx(
                'h-full transition-all',
                task.status === 'failed'
                  ? 'bg-danger'
                  : task.status === 'success'
                    ? 'bg-success'
                    : 'bg-klein-gradient',
              )}
              style={{ width: `${Math.min(100, Math.max(0, task.progress))}%` }}
            />
          </div>
          <span className="font-mono text-tiny text-text-tertiary">{task.progress}%</span>
        </div>
      </td>
      <td className="px-3 py-1.5 text-text-secondary">{fmtMs(task.created_at)}</td>
      <td className="px-3 py-1.5 font-mono text-text-secondary">{elapsed > 0 ? `${elapsed}s` : '—'}</td>
      <td className="px-3 py-1.5 text-right">
        <div className="flex justify-end gap-1.5">
          {!ended && (
            <button
              type="button"
              className="btn btn-outline btn-xs"
              disabled={cancelBusy || task.cancel_requested}
              onClick={onCancel}
            >
              <StopCircle size={12} /> {task.cancel_requested ? '取消中' : '取消'}
            </button>
          )}
          {ended && (
            <button
              type="button"
              className="btn btn-outline btn-xs text-danger"
              disabled={deleteBusy}
              onClick={onDelete}
            >
              <Trash2 size={12} /> 删除
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

function CompactField({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-tiny text-text-tertiary">{label}</span>
      {children}
    </label>
  );
}

// ===================== 实时日志面板 =====================

const LEVEL_OPTIONS: { value: '' | RegisterTaskLogLevel; label: string }[] = [
  { value: '', label: '全部级别' },
  { value: 'info', label: '信息' },
  { value: 'warn', label: '警告' },
  { value: 'error', label: '错误' },
];

const PROVIDER_OPTIONS: { value: '' | RegisterTaskProvider; label: string }[] = [
  { value: '', label: '全部 Provider' },
  { value: 'adobe', label: 'BANANA' },
  { value: 'grok', label: 'GROK' },
  { value: 'gpt', label: 'GPT' },
];

// providerLabel 将后端 provider 字段映射为面向运维的显示名。
// adobe 始终显示为 BANANA（伪装），其他 provider 直接 uppercase。
function providerLabel(p?: RegisterTaskProvider | string): string {
  if (!p) return '-';
  if (p === 'adobe') return 'BANANA';
  return String(p).toUpperCase();
}

const LEVEL_LABEL: Record<RegisterTaskLogLevel, string> = {
  info: '信息',
  warn: '警告',
  error: '错误',
};

// 步骤标识 → 中文显示。未命中的步骤回退到原始字符串，方便后续新增。
const STEP_LABEL: Record<string, string> = {
  // 公共
  start: '开始执行',
  done: '完成',
  failed: '失败',
  cancelled: '已取消',
  preflight: '预检',
  pick_proxy: '挑选代理',
  acquire_mail: '领取邮箱',
  persist: '落库',
  // Adobe
  prewarm: '预热',
  create_account_init: '创建账号·初始化',
  captcha: '验证码求解',
  create_account_with_captcha: '提交账号',
  exchange_password: '换取密码凭证',
  send_mfa: '发送邮箱验证码',
  send_mfa_warn: '邮箱验证警告',
  ims_tokens: '获取 IMS Token',
  from_susi: '完成 SUSI 流程',
  // GPT
  authorize: 'OAuth 授权',
  authorize_continue: '授权·下一步',
  user_register: '用户注册',
  email_otp_send: '发送邮箱验证码',
  create_account: '创建账号',
  exchange_token: '换取 Token',
  // Grok
  bootstrap: '初始化',
  send_email_code: '发送邮箱验证码',
  verify_email_code: '校验邮箱验证码',
  submit_signup: '提交注册',
  follow_verify_url: '确认邮件链接',
  accept_tos: '接受协议',
  accept_tos_warn: '接受协议警告',
};

function stepLabel(step?: string): string {
  if (!step) return '';
  return STEP_LABEL[step] || step;
}

function LogsView() {
  const qc = useQueryClient();
  const [provider, setProvider] = useState<'' | RegisterTaskProvider>('');
  const [level, setLevel] = useState<'' | RegisterTaskLogLevel>('');
  const [taskID, setTaskID] = useState('');
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [purgeOpen, setPurgeOpen] = useState(false);

  const params = useMemo(
    () => ({
      provider: provider || undefined,
      level: level || undefined,
      task_id: taskID.trim() ? Number(taskID) || undefined : undefined,
      limit: 300,
    }),
    [provider, level, taskID],
  );

  const q = useQuery({
    queryKey: ['admin', 'register-tasks', 'logs', params],
    queryFn: () => registerTaskApi.logs(params),
    refetchInterval: autoRefresh ? 2000 : false,
  });

  const purge = useMutation({
    mutationFn: () =>
      registerTaskApi.logsPurge({
        provider: provider || undefined,
        level: level || undefined,
        task_id: taskID.trim() ? Number(taskID) || undefined : undefined,
      }),
    onSuccess: (r) => {
      toast.success(`已清空 ${r.deleted} 条日志`);
      setPurgeOpen(false);
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks', 'logs'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message || '清理失败'),
  });

  const logs: RegisterTaskLogEntry[] = q.data?.list ?? [];

  // 当存在过滤条件时，"清空"只清当前过滤范围；无过滤时则提示清空全部。
  const hasFilter = !!provider || !!level || !!taskID.trim();

  return (
    <Section
      title="实时日志"
      right={
        <>
          <select
            className="select select-sm"
            value={provider}
            onChange={(e) => setProvider(e.target.value as RegisterTaskProvider | '')}
          >
            {PROVIDER_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <select
            className="select select-sm"
            value={level}
            onChange={(e) => setLevel(e.target.value as RegisterTaskLogLevel | '')}
          >
            {LEVEL_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <input
            className="input input-sm w-32"
            placeholder="任务 ID"
            value={taskID}
            onChange={(e) => setTaskID(e.target.value.replace(/[^0-9]/g, ''))}
          />
          <label className="inline-flex cursor-pointer items-center gap-1 text-small text-text-tertiary">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            自动
          </label>
          <button
            className="btn btn-outline btn-sm"
            type="button"
            onClick={() => q.refetch()}
            title="手动刷新"
          >
            <RefreshCw size={14} className={q.isFetching ? 'animate-spin' : ''} />
          </button>
          <button
            className="btn btn-outline btn-sm text-danger hover:bg-danger-soft"
            type="button"
            onClick={() => setPurgeOpen(true)}
            disabled={purge.isPending}
            title={hasFilter ? '清空当前筛选范围内的日志' : '清空全部日志'}
          >
            <Trash2 size={14} /> {purge.isPending ? '清理中…' : hasFilter ? '清空筛选' : '清空全部'}
          </button>
        </>
      }
      bodyClass="p-0"
    >
      <div
        className="max-h-[680px] min-h-[260px] overflow-auto px-3 py-2 font-mono text-[12px] leading-[22px]"
        style={{ fontFamily: 'ui-monospace, SFMono-Regular, "JetBrains Mono", Menlo, Consolas, monospace' }}
      >
        {logs.length === 0 ? (
          <div className="py-12 text-center text-text-tertiary">暂无日志</div>
        ) : (
          logs.map((log) => <LogLine key={log.id} log={log} />)
        )}
      </div>

      <ConfirmDialog
        open={purgeOpen}
        title={hasFilter ? '清空筛选日志' : '清空全部日志'}
        description={
          hasFilter
            ? '将清除当前筛选条件命中的所有日志，此操作不可恢复。'
            : '将清除全部注册任务日志（不影响任务记录本身），此操作不可恢复。'
        }
        confirmLabel="确认清空"
        cancelLabel="取消"
        tone="danger"
        loading={purge.isPending}
        onCancel={() => setPurgeOpen(false)}
        onConfirm={() => purge.mutate()}
      />
    </Section>
  );
}

function LogLine({ log }: { log: RegisterTaskLogEntry }) {
  const date = new Date(log.created_at);
  const ts = `${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`;

  const provColor =
    log.provider === 'adobe'
      ? 'text-klein-500'
      : log.provider === 'grok'
        ? 'text-warn'
        : log.provider === 'gpt'
          ? 'text-success'
          : 'text-text-tertiary';

  const levelColor =
    log.level === 'error'
      ? 'text-danger'
      : log.level === 'warn'
        ? 'text-warn'
        : 'text-text-tertiary';

  const messageTone =
    log.level === 'error'
      ? 'text-danger'
      : log.level === 'warn'
        ? 'text-warn'
        : 'text-text-primary';

  const dot =
    log.level === 'error' ? 'bg-danger' : log.level === 'warn' ? 'bg-warn' : 'bg-surface-3';

  return (
    <div className="group flex items-baseline gap-2 rounded px-1 hover:bg-surface-2/60">
      <span className="shrink-0 tabular-nums text-text-tertiary">{ts}</span>
      <span className={`h-1.5 w-1.5 shrink-0 translate-y-px rounded-full ${dot}`} />
      <span className={`shrink-0 ${levelColor}`} style={{ minWidth: '2.6em' }}>
        {LEVEL_LABEL[log.level]}
      </span>
      <span className={`shrink-0 uppercase ${provColor}`} style={{ minWidth: '3.4em' }}>
        {providerLabel(log.provider)}
      </span>
      <span className="shrink-0 tabular-nums text-text-tertiary">#{log.task_id}</span>
      {log.step && (
        <span className="shrink-0 text-text-secondary">
          <span className="text-text-tertiary">›</span> {stepLabel(log.step)}
        </span>
      )}
      {typeof log.progress === 'number' && log.progress > 0 && (
        <span className="shrink-0 tabular-nums text-text-tertiary">{log.progress}%</span>
      )}
      {log.message && <span className={`break-all ${messageTone}`}>{log.message}</span>}
    </div>
  );
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : `${n}`;
}
