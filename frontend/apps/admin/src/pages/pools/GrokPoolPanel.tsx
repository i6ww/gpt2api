import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, Inbox, Pencil, RefreshCw, Trash2, X } from 'lucide-react';
import { type ChangeEvent, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError } from '../../lib/api';
import { poolGrokApi } from '../../lib/services';
import type {
  GrokAccountType,
  GrokSubscriptionStatus,
  GrokPoolBatchRefreshJob,
  GrokPoolItem,
  GrokPoolPurgeBody,
  GrokPoolRefreshScope,
  GrokPoolUpdateBody,
  GrokTrialStatus,
} from '../../lib/types';

// IdemConflict 后端冲突错误码（重复请求）。
// pools/grok/batch-refresh 用这个 code 表示 "已有任务在跑"，前端 catch
// 后直接接管轮询，而不是当成错误抛 toast。
const ERR_CODE_CONFLICT = 409102;

const BATCH_SCOPE_LABEL: Record<GrokPoolRefreshScope, string> = {
  all: '全部账号',
  abnormal: '异常账号',
  zero_credits: '零额度账号',
  expiring: '额度即将刷新',
  unknown_type: '未识别类型',
};
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, SplitMenu, fmtMs } from './_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const TRIAL_OPTIONS: { value: '' | GrokTrialStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '待开通' },
  { value: 'activating', label: '开通中' },
  { value: 'active', label: '已开通' },
  { value: 'failed', label: '失败' },
  { value: 'expired', label: '已过期' },
];

const ACCOUNT_TYPE_OPTIONS: { value: GrokAccountType; label: string }[] = [
  { value: '', label: '未识别' },
  { value: 'free', label: 'Free' },
  { value: 'super_grok_lite', label: 'Lite' },
  { value: 'super_grok', label: 'SuperGrok' },
  { value: 'super_grok_heavy', label: 'Heavy' },
  { value: 'team', label: 'Team' },
  { value: 'unknown', label: '未知' },
];

// ACCOUNT_TYPE_FILTER_OPTIONS 列表筛选用的下拉选项。
//
// 与 ACCOUNT_TYPE_OPTIONS（编辑对话框用）的区别：
//   - 这里第一项 value='' 表示 "不限"（不带 account_type query），
//     而编辑对话框里 value='' 表示账号字段本身的 "未识别"。
//   - Heavy 这里用更短的 "Heavy" 标签（用户视角已经在 SuperGrok 上下文里）。
const ACCOUNT_TYPE_FILTER_OPTIONS: { value: GrokAccountType; label: string }[] = [
  { value: '', label: '全部类型' },
  { value: 'free', label: 'Free' },
  { value: 'super_grok_lite', label: 'Lite' },
  { value: 'super_grok', label: 'SuperGrok' },
  { value: 'super_grok_heavy', label: 'Heavy' },
  { value: 'team', label: 'Team' },
  { value: 'unknown', label: '未识别' },
];

// SUB_STATUS_FILTER_OPTIONS 按订阅生命周期筛选。
//
// "试用中 (trialing)"和"正式 (active)"分开筛是运营高频场景：
// 试用号过期就该转付费才有价值，所以经常需要单独查一批试用号催转化。
const SUB_STATUS_FILTER_OPTIONS: { value: GrokSubscriptionStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '正式' },
  { value: 'trialing', label: '试用中' },
  { value: 'past_due', label: '欠费' },
  { value: 'canceled', label: '已取消' },
  { value: 'inactive', label: '失效' },
];

function trialBadge(status: string) {
  switch (status) {
    case 'pending':
      return { label: '待开通', tone: 'bg-surface-2 text-text-secondary' };
    case 'activating':
      return { label: '开通中', tone: 'bg-info-soft text-info' };
    case 'active':
      return { label: '已开通', tone: 'bg-success-soft text-success' };
    case 'failed':
      return { label: '失败', tone: 'bg-danger-soft text-danger' };
    case 'expired':
      return { label: '已过期', tone: 'bg-warn-soft text-warn' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

// accountTypeLabel 表格 + 编辑对话框 + toast 共用的 tier 短标签。
//
// 注意：Heavy 直接显示成 "Heavy" 而不是 "SuperGrok Heavy"，避免列表里"已开通
// SuperGrok Heavy"被误读成"叠了两个级别"。Heavy 实际上 *就是 Heavy 档* —
// SuperGrok 是普通月订，Heavy 是更高的月订，两者互斥不叠加。
function accountTypeLabel(t?: string): string {
  switch (t) {
    case 'free':
      return 'Free';
    case 'super_grok_lite':
      return 'Lite';
    case 'super_grok':
      return 'SuperGrok';
    case 'super_grok_heavy':
      return 'Heavy';
    case 'team':
      return 'Team';
    case 'unknown':
      return '未知';
    case '':
    case undefined:
    case null:
      return '—';
    default:
      return t!;
  }
}

// SubscriptionStatusBadge 试用 / 欠费 / 已取消 等订阅生命周期状态的小徽章。
//
// active 状态不显示（避免每行都堆字符）；其他状态各有配色：
//   - trialing → 蓝色 "试用中"（用户处于三天免费试用期）
//   - past_due → 黄色 "欠费"
//   - canceled → 灰色 "已取消"（取消后仍在 period 内能用）
//
// 故意不显示 inactive：grok 的 /rest/subscriptions 在用户刚续费时经常返回
// status=INACTIVE + 上一周期 billingPeriodEnd 的 stale 快照。后端已经把
// expires_at 外推到了下一周期，运营只需看时间就够，再显示一个红色"失效"
// 徽章反而会误导（账号实际还能用）。真到期会被 trial_status=expired 兜底。
function SubscriptionStatusBadge({ status }: { status?: string }) {
  if (!status || status === 'active' || status === 'inactive') return null;
  const map: Record<string, { label: string; tone: string }> = {
    trialing: { label: '试用中', tone: 'bg-info/15 text-info' },
    trial_ended: { label: '试用结束', tone: 'bg-danger/15 text-danger' },
    past_due: { label: '欠费', tone: 'bg-warn/15 text-warn' },
    canceled: { label: '已取消', tone: 'bg-text-tertiary/15 text-text-tertiary' },
  };
  const cfg = map[status];
  if (!cfg) return null;
  return (
    <span className={`inline-block rounded px-1 py-px text-tiny ${cfg.tone}`}>{cfg.label}</span>
  );
}

// SubscriptionExpiryCell 列表里"续费 / 到期"列的渲染。
//
// 设计要点：
//   - cancel_at_period_end=false（默认）→ 自动续费扣费日，浅色「下次续费」+ 时间
//   - cancel_at_period_end=true          → 真到期日，红色「将到期」+ 时间
//   - expires_at 为空                    → "—" 占位（一般是 free / 未订阅）
//   - billing_interval 有值时副标签显示「月订/年订」
//
// 注意：grok 个人中心的 subscription endpoint 有同步延迟，已过期但还有 quota
// 的 stale 数据会在后端用 billing_interval 自动外推到下一周期 — 前端不用关心。
function SubscriptionExpiryCell({
  expiresAt,
  cancelAtPeriodEnd,
  billingInterval,
  subscriptionStatus,
}: {
  expiresAt?: number;
  cancelAtPeriodEnd?: boolean;
  billingInterval?: string;
  subscriptionStatus?: string;
}) {
  if (!expiresAt) {
    return <span className="text-text-tertiary/60">—</span>;
  }
  // 三种状态优先级：试用 > 取消 > 自动续费
  const isTrial =
    subscriptionStatus === 'trialing' || subscriptionStatus === 'trial_ended';
  const cancelMode = !!cancelAtPeriodEnd;
  const interval =
    billingInterval === 'monthly' ? '月订' : billingInterval === 'yearly' ? '年订' : '';
  let headerText: string;
  let headerTone: string;
  let valueTone: string;
  if (isTrial) {
    headerText = subscriptionStatus === 'trial_ended' ? '试用已结束' : '试用到期';
    headerTone = subscriptionStatus === 'trial_ended' ? 'text-danger' : 'text-info';
    valueTone = subscriptionStatus === 'trial_ended' ? 'text-danger' : 'text-info';
  } else if (cancelMode) {
    headerText = '将到期';
    headerTone = 'text-danger';
    valueTone = 'text-danger';
  } else {
    headerText = '下次续费';
    headerTone = 'text-text-tertiary/80';
    valueTone = 'text-text-primary';
  }
  return (
    <div className="leading-tight">
      <div className={`text-tiny ${headerTone}`}>{headerText}</div>
      <div className={valueTone}>{fmtMs(expiresAt)}</div>
      {interval && !isTrial && <div className="text-tiny text-text-tertiary/60">{interval}</div>}
      {isTrial && (
        <div className="text-tiny text-text-tertiary/60">{interval ? `${interval} · 试用` : '试用'}</div>
      )}
    </div>
  );
}

function accountTypeTone(t?: string): string {
  switch (t) {
    case 'super_grok':
    case 'super_grok_heavy':
      return 'text-success';
    case 'team':
      return 'text-info';
    case 'free':
      return 'text-text-secondary';
    default:
      return 'text-text-tertiary';
  }
}

export default function GrokPoolPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [trial, setTrial] = useState<'' | GrokTrialStatus>('');
  const [accountType, setAccountType] = useState<GrokAccountType>('');
  const [subStatus, setSubStatus] = useState<GrokSubscriptionStatus>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [editing, setEditing] = useState<GrokPoolItem | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      trial_status: trial || undefined,
      account_type: accountType || undefined,
      subscription_status: subStatus || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, page, trial, accountType, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'pool-grok', 'list', query],
    queryFn: () => poolGrokApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'pool-grok', 'stats'],
    queryFn: () => poolGrokApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'pool-grok'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => poolGrokApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('账号已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => poolGrokApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const expireOverdue = useMutation({
    mutationFn: () => poolGrokApi.expireOverdue(),
    onSuccess: (r) => {
      refresh();
      toast.success(`已将 ${r.affected} 条置为 expired`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const updateOne = useMutation({
    mutationFn: ({ id, body }: { id: number; body: GrokPoolUpdateBody }) =>
      poolGrokApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const refreshOne = useMutation({
    mutationFn: (id: number) => poolGrokApi.refresh(id),
    onSuccess: (r) => {
      refresh();
      toast.success(
        `已刷新 #${r.id}：${accountTypeLabel(r.account_type)} · 额度 ${r.credits.toFixed(0)}/${r.quota_total.toFixed(0)}`,
      );
    },
    onError: (e: ApiError) => toast.error(e.message || '刷新失败'),
  });
  // ----- 批量刷新：fire-and-poll -----
  //
  // 后端把 batch-refresh 改成了异步：handler 立即返回任务快照，前端按
  // 3s 间隔轮询 /batch-refresh/status 看进度，跑完弹结果 toast 并停止轮询。
  // 这样 10000 个号也不会被 nginx 90s 超时砍掉，单个号 hang 也不会让别的
  // 号跟着卡。
  const lastJobIdRef = useRef<string | null>(null);
  const reportedJobIdRef = useRef<string | null>(null);

  const batchStatus = useQuery({
    queryKey: ['admin', 'pool-grok', 'batch-status'],
    queryFn: () => poolGrokApi.batchRefreshStatus(),
    refetchInterval: (q) => {
      // 任务跑完后立即停止轮询；进入页面看到历史完成态时也不会一直 poll
      const s = q.state.data?.status;
      return s === 'running' ? 3000 : false;
    },
    // 进入页面时拉一次，看后台是否有未跑完的旧任务（页面刷新会丢前端状态）
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
  });

  const jobRunning = batchStatus.data?.status === 'running';

  useEffect(() => {
    const j = batchStatus.data;
    if (!j || j.status === 'idle' || !j.job_id) return;
    if (j.status === 'running') {
      lastJobIdRef.current = j.job_id;
      return;
    }
    // 终结态（completed / cancelled / failed）只报告一次
    if (reportedJobIdRef.current === j.job_id) return;
    reportedJobIdRef.current = j.job_id;
    refresh();
    const head = formatJobDoneHeading(j);
    if (j.status === 'completed' && (j.fail ?? 0) === 0) {
      toast.success(head);
    } else {
      toast.error(formatJobDoneDetail(j, head));
    }
  }, [batchStatus.data]);

  const batchRefresh = useMutation({
    mutationFn: (scope: GrokPoolRefreshScope) =>
      poolGrokApi.batchRefresh({ scope }).then((j) => ({ scope, job: j })),
    onSuccess: ({ scope, job }) => {
      lastJobIdRef.current = job.job_id ?? null;
      // 立刻刷一次 status 进入轮询循环
      qc.setQueryData<GrokPoolBatchRefreshJob>(
        ['admin', 'pool-grok', 'batch-status'],
        job,
      );
      qc.invalidateQueries({ queryKey: ['admin', 'pool-grok', 'batch-status'] });
      toast.success(
        `已启动后台扫描（${BATCH_SCOPE_LABEL[scope]}）。可关闭对话框继续操作，进度会在工具栏实时显示。`,
      );
    },
    onError: (e: ApiError) => {
      if (e.code === ERR_CODE_CONFLICT) {
        // 已有任务在跑 — 接管轮询即可
        qc.invalidateQueries({ queryKey: ['admin', 'pool-grok', 'batch-status'] });
        toast.error('已有批量刷新任务在跑，正在接入查看进度。');
        return;
      }
      toast.error(e.message || '扫描启动失败');
    },
  });

  const batchCancel = useMutation({
    mutationFn: () => poolGrokApi.batchRefreshCancel(),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ['admin', 'pool-grok', 'batch-status'] });
      if (r.cancelled) toast.success('已请求取消，已在跑的账号会自然走完');
      else toast.error('没有正在运行的任务');
    },
    onError: (e: ApiError) => toast.error(e.message || '取消失败'),
  });
  const purge = useMutation({
    mutationFn: (body: GrokPoolPurgeBody) => poolGrokApi.purge(body),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已清理 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message || '清理失败'),
  });

  async function purgeWithConfirm(
    title: string,
    description: string,
    body: GrokPoolPurgeBody,
  ) {
    const ok = await confirm({
      title,
      description,
      tone: 'danger',
      confirmLabel: '清理',
    });
    if (ok) purge.mutate(body);
  }

  const items = list.data?.list ?? [];
  const total = list.data?.total ?? 0;
  const allSelected = items.length > 0 && items.every((it) => selected.has(it.id));

  const onToggleAll = (e: ChangeEvent<HTMLInputElement>) => {
    if (e.target.checked) setSelected(new Set(items.map((it) => it.id)));
    else setSelected(new Set());
  };
  const onToggleOne = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <div className="space-y-3">
      <StatRow>
        <Stat label="总数" value={stats.data?.total ?? 0} />
        <Stat label="待开通" value={stats.data?.pending ?? 0} tone="text-text-secondary" />
        <Stat label="开通中" value={stats.data?.activating ?? 0} tone="text-info" />
        <Stat label="已开通" value={stats.data?.active ?? 0} tone="text-success" />
        <Stat label="失败" value={stats.data?.failed ?? 0} tone="text-danger" />
        <Stat label="已过期" value={stats.data?.expired ?? 0} tone="text-warn" />
      </StatRow>

      {batchStatus.data && batchStatus.data.status !== 'idle' && (
        <BatchProgress
          job={batchStatus.data}
          scopeLabel={
            (batchStatus.data.scope && BATCH_SCOPE_LABEL[batchStatus.data.scope]) ?? '账号'
          }
          onCancel={() => batchCancel.mutate()}
          cancelling={batchCancel.isPending}
        />
      )}

      <Toolbar>
        <input
          className="input input-sm w-56"
          value={keyword}
          placeholder="搜索 email"
          onChange={(e) => {
            setKeyword(e.target.value);
            setPage(1);
          }}
        />
        <select
          className="select select-sm"
          value={trial}
          onChange={(e) => {
            setTrial(e.target.value as GrokTrialStatus | '');
            setPage(1);
          }}
        >
          {TRIAL_OPTIONS.map((o) => (
            <option key={o.value || 'all'} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          className="select select-sm"
          value={accountType}
          title="按订阅 tier 筛选（来自 /rest/subscriptions + stripe productId）"
          onChange={(e) => {
            setAccountType(e.target.value as GrokAccountType);
            setPage(1);
          }}
        >
          {ACCOUNT_TYPE_FILTER_OPTIONS.map((o) => (
            <option key={o.value || 'all'} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          className="select select-sm"
          value={subStatus}
          title="按订阅生命周期筛选：试用中 / 正式 / 欠费 / 已取消 / 失效"
          onChange={(e) => {
            setSubStatus(e.target.value as GrokSubscriptionStatus);
            setPage(1);
          }}
        >
          {SUB_STATUS_FILTER_OPTIONS.map((o) => (
            <option key={o.value || 'all'} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <ToolbarSpacer />
        <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
          <RefreshCw size={14} /> 列表刷新
        </button>
        <SplitMenu
          label={jobRunning ? '扫描中…' : '扫描续期'}
          icon={<RefreshCw size={14} className={jobRunning ? 'animate-spin' : ''} />}
          busy={batchRefresh.isPending || jobRunning}
          items={[
            {
              label: '检测全部账号',
              description: '全部账号 / rate-limits 探测类型 + 额度',
              onClick: () => batchRefresh.mutate('all'),
            },
            {
              label: '检测异常账号',
              description: 'failed / expired / 失败计数 > 0',
              onClick: () => batchRefresh.mutate('abnormal'),
            },
            {
              label: '检测 0 额度账号',
              description: 'credits ≤ 0 + 状态 active/pending',
              onClick: () => batchRefresh.mutate('zero_credits'),
            },
            {
              label: '检测未识别类型账号',
              description: 'account_type 为空（一般是新导入的）',
              onClick: () => batchRefresh.mutate('unknown_type'),
            },
            {
              label: '检测额度即将刷新(<12h)',
              description: 'trial_expires_at（quota 窗口）剩余 < 12h 的 active 账号',
              onClick: () => batchRefresh.mutate('expiring'),
            },
            {
              label: '标记 quota 窗口已结束',
              description: 'trial_expires_at < now 的 active → expired（不打外网，仅本地标记，待下次扫描更新）',
              onClick: () => expireOverdue.mutate(),
            },
          ]}
        />
        <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
          <Download size={14} /> 导入
        </button>
        <SplitMenu
          label={`删除${selected.size ? ` (${selected.size})` : ''}`}
          icon={<Trash2 size={14} />}
          tone="danger"
          busy={batchDelete.isPending || purge.isPending}
          items={[
            {
              label: `删除选中 (${selected.size})`,
              description: '仅勾选的账号',
              tone: 'danger',
              onClick: async () => {
                if (selected.size === 0) {
                  toast.error('请先勾选要删除的账号');
                  return;
                }
                const ok = await confirm({
                  title: '批量删除 Grok 账号',
                  description: `将永久删除选中的 ${selected.size} 条 Grok 账号记录（含凭证）。`,
                  tone: 'danger',
                  confirmLabel: '删除',
                });
                if (ok) batchDelete.mutate(Array.from(selected));
              },
            },
            {
              label: '删除失效账号',
              description: 'trial_status = failed',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除失效账号',
                  '将永久删除所有 trial_status=failed 的账号（含凭证），不可恢复。',
                  { status: 'failed' },
                ),
            },
            {
              label: '删除异常账号',
              description: 'failed + expired',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除异常账号',
                  '将永久删除所有 failed / expired 状态的账号（含凭证），不可恢复。',
                  { abnormal: true },
                ),
            },
            {
              label: '删除 0 额度账号',
              description: 'credits ≤ 0',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除 0 额度账号',
                  '将永久删除所有 credits ≤ 0 的账号（含凭证），不可恢复。',
                  { zero_credits: true },
                ),
            },
            {
              label: '删除全部账号',
              description: '清空所有 Grok 账号',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除全部 Grok 账号',
                  '将永久删除全部 Grok 账号记录（含凭证），不可恢复。请谨慎操作。',
                  { all: true },
                ),
            },
          ]}
        />
      </Toolbar>

      <Section bodyClass="p-0">
        <div className="overflow-x-auto">
          <table className="min-w-full text-small">
            <thead>
              <tr className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
                <th className="w-10 px-3 py-2 text-left">
                  <input type="checkbox" checked={allSelected} onChange={onToggleAll} />
                </th>
                <th className="px-3 py-2 text-left">Email</th>
                <th className="px-3 py-2 text-left">凭证</th>
                <th className="px-3 py-2 text-left">使用状态</th>
                <th
                  className="px-3 py-2 text-left"
                  title="来自 grok /rest/subscriptions：
• 默认 = 自动续费扣费日（过了会自动延一周期）
• 红色「将到期」= 用户已退订，过期后失效
• 空 = 未拉到订阅信号（free / 未订阅）

由于 grok 个人中心数据有同步延迟，过期 + 仍有 quota 的 stale 数据会按 billing_interval 自动外推到下一周期。"
                >
                  续费 / 到期
                </th>
                <th
                  className="px-3 py-2 text-left"
                  title="grok /rest/rate-limits 返回的 windowSizeSeconds 推算的下一次额度刷新时间（一般是 1-4h），不是订阅到期。"
                >
                  额度刷新于
                </th>
                <th className="px-3 py-2 text-right">额度</th>
                <th className="px-3 py-2 text-left">最近检测</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={9} className="px-3 py-6 text-center text-text-tertiary">
                    加载中…
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={9} className="px-3 py-8 text-center text-text-tertiary">
                    <Inbox size={20} className="mx-auto mb-1" />
                    暂无账号
                  </td>
                </tr>
              )}
              {items.map((it) => (
                <tr key={it.id} className="border-b border-border last:border-0 hover:bg-surface-2/60">
                  <td className="px-3 py-1.5">
                    <input
                      type="checkbox"
                      checked={selected.has(it.id)}
                      onChange={() => onToggleOne(it.id)}
                    />
                  </td>
                  <td className="px-3 py-1.5 font-mono text-text-primary">{it.email}</td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      <FlagPill ok={it.has_password} label="pwd" />
                      <FlagPill ok={it.has_sso} label="sso" />
                      <FlagPill ok={it.has_sso_rw} label="sso-rw" />
                    </div>
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge {...trialBadge(it.trial_status)} />
                    <div className="mt-0.5 flex items-center gap-1">
                      <span
                        className={`text-tiny ${accountTypeTone(it.account_type)}`}
                        title="账号订阅类型（来自 /rest/subscriptions.tier 与 stripe productId 综合识别）"
                      >
                        {accountTypeLabel(it.account_type)}
                      </span>
                      <SubscriptionStatusBadge status={it.subscription_status} />
                    </div>
                    {it.trial_error && (
                      <div
                        className="mt-0.5 line-clamp-1 max-w-[240px] text-tiny text-text-tertiary"
                        title={it.trial_error}
                      >
                        {it.trial_error}
                      </div>
                    )}
                  </td>
                  <td className="px-3 py-1.5">
                    <SubscriptionExpiryCell
                      expiresAt={it.expires_at}
                      cancelAtPeriodEnd={it.cancel_at_period_end}
                      billingInterval={it.billing_interval}
                      subscriptionStatus={it.subscription_status}
                    />
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">{fmtMs(it.trial_expires_at)}</td>
                  <td className="px-3 py-1.5 text-right font-mono text-text-primary">
                    {(it.credits ?? 0).toFixed(0)}
                    {it.quota_total > 0 && (
                      <span className="text-tiny text-text-tertiary"> /{it.quota_total.toFixed(0)}</span>
                    )}
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>{it.last_checked_at ? fmtMs(it.last_checked_at) : '—'}</div>
                    {it.failure_count && it.failure_count > 0 ? (
                      <div className="text-tiny text-warn">连续失败 {it.failure_count} 次</div>
                    ) : null}
                  </td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button
                        className="btn btn-ghost btn-xs"
                        title="编辑账号字段（凭证 / 状态 / 类型 / 额度）"
                        onClick={() => setEditing(it)}
                      >
                        <Pencil size={12} /> 编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        disabled={refreshOne.isPending}
                        title="rate-limits 探测：账号类型 + 当前额度"
                        onClick={() => refreshOne.mutate(it.id)}
                      >
                        <RefreshCw size={12} /> 刷新
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除 Grok 账号',
                            description: (
                              <>
                                确认删除 <span className="font-mono text-text-primary">{it.email}</span>？删除后凭证不可恢复。
                              </>
                            ),
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) removeOne.mutate(it.id);
                        }}
                      >
                        <Trash2 size={12} /> 删除
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

      {openImport && (
        <GrokImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />
      )}
      {editing && (
        <GrokEditDialog
          item={editing}
          busy={updateOne.isPending}
          onClose={() => setEditing(null)}
          onSubmit={(body) => updateOne.mutate({ id: editing.id, body })}
        />
      )}
      {confirmDialog}
    </div>
  );
}

function GrokImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const importMut = useMutation({
    mutationFn: () => poolGrokApi.import({ text }),
    onSuccess: (r) => {
      toast.success(`导入完成：成功 ${r.imported}，跳过 ${r.skipped}`);
      if (r.errors?.length) toast.error(r.errors.slice(0, 3).join('；'));
      onDone();
      onClose();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <ImportDialogShell
      title="批量导入 GROK 账号"
      description={
        <span>
          一行一条，自动识别 4 种格式：① 整段 JSON Array；② 每行 JSON 对象；
          ③ <code className="font-mono">email----password----sso[----sso_rw]</code>；
          ④ 裸 JWT token（<code className="font-mono">eyJ...</code>，会自动派生占位 email）。
          以 email 作为唯一键 upsert。
        </span>
      }
      onClose={onClose}
      busy={importMut.isPending}
      onConfirm={() => importMut.mutate()}
    >
      <textarea
        className="input min-h-[260px] font-mono text-tiny"
        placeholder={`# 简形（含 sso）
abc@example.com----secret----eyJ0eXAi...

# 裸 JWT（自动派生 email）
eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJzZXNzaW9uX2lkIjoi...

# JSON 对象
{"email":"abc@example.com","password":"...","sso":"...","trial_status":"pending"}`}
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
    </ImportDialogShell>
  );
}

// GrokEditDialog 单条 Grok 账号编辑对话框。
function GrokEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: GrokPoolItem;
  busy?: boolean;
  onClose: () => void;
  onSubmit: (body: GrokPoolUpdateBody) => void;
}) {
  const [trialStatus, setTrialStatus] = useState<GrokTrialStatus>(
    (item.trial_status as GrokTrialStatus) ?? 'pending',
  );
  const [accountType, setAccountType] = useState<GrokAccountType>(
    (item.account_type as GrokAccountType) ?? '',
  );
  const [credits, setCredits] = useState<string>(String(item.credits ?? 0));
  const [trialExpiresLocal, setTrialExpiresLocal] = useState<string>(
    item.trial_expires_at ? toLocalInput(item.trial_expires_at) : '',
  );
  const [password, setPassword] = useState('');
  const [sso, setSso] = useState('');
  const [ssoRw, setSsoRw] = useState('');
  const [paymentUrl, setPaymentUrl] = useState(item.payment_url ?? '');
  const [notes, setNotes] = useState(item.notes ?? '');

  function submit() {
    const body: GrokPoolUpdateBody = {};
    if (trialStatus !== item.trial_status) body.trial_status = trialStatus;
    if (accountType !== (item.account_type ?? '')) body.account_type = accountType;
    const c = Number(credits);
    if (!Number.isNaN(c) && c !== item.credits) body.credits = c;
    if (trialExpiresLocal) {
      const ts = new Date(trialExpiresLocal).getTime();
      if (!Number.isNaN(ts) && ts !== (item.trial_expires_at ?? 0)) {
        body.trial_expires_at = ts;
      }
    } else if (item.trial_expires_at) {
      body.trial_expires_at = 0;
    }
    if (password) body.password = password;
    if (sso) body.sso = sso;
    if (ssoRw) body.sso_rw = ssoRw;
    if (paymentUrl !== (item.payment_url ?? '')) body.payment_url = paymentUrl;
    if (notes !== (item.notes ?? '')) body.notes = notes;
    onSubmit(body);
  }

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">编辑 Grok 账号</h3>
          <p className="mt-1 text-small text-text-tertiary">
            <span className="font-mono">{item.email}</span> · 凭证字段留空不变，填入则覆盖
          </p>
        </header>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <div className="text-tiny text-text-tertiary">使用状态</div>
            <select
              className="select select-sm w-full"
              value={trialStatus}
              onChange={(e) => setTrialStatus(e.target.value as GrokTrialStatus)}
            >
              <option value="pending">待开通</option>
              <option value="activating">开通中</option>
              <option value="active">已开通</option>
              <option value="failed">失败</option>
              <option value="expired">已过期</option>
            </select>
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">账号类型</div>
            <select
              className="select select-sm w-full"
              value={accountType}
              onChange={(e) => setAccountType(e.target.value as GrokAccountType)}
            >
              {ACCOUNT_TYPE_OPTIONS.map((o) => (
                <option key={o.value || '__empty'} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">额度</div>
            <input
              className="input input-sm w-full"
              type="number"
              min={0}
              step="0.01"
              value={credits}
              onChange={(e) => setCredits(e.target.value)}
            />
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">试用到期（留空清除）</div>
            <input
              className="input input-sm w-full"
              type="datetime-local"
              value={trialExpiresLocal}
              onChange={(e) => setTrialExpiresLocal(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              Password（留空不变）
              {item.has_password ? (
                <span className="ml-1 text-info">· 已存有密码</span>
              ) : (
                <span className="ml-1 text-text-tertiary/60">· 未设置</span>
              )}
            </div>
            <input
              className="input input-sm w-full font-mono"
              type="password"
              autoComplete="new-password"
              placeholder={item.has_password ? '已加密保存，输入新值即可覆盖' : '未设置'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              SSO Token（留空不变）
              {item.has_sso ? (
                <span className="ml-1 text-info">· 已存有 SSO（出于安全不回显明文）</span>
              ) : (
                <span className="ml-1 text-text-tertiary/60">· 未设置</span>
              )}
            </div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              placeholder={
                item.has_sso
                  ? '已加密保存，输入新 JWT 即可覆盖；留空表示保留原值'
                  : '尚未设置，粘贴 eyJ... 形式的 JWT'
              }
              value={sso}
              onChange={(e) => setSso(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              SSO-RW Token（留空不变）
              {item.has_sso_rw ? (
                <span className="ml-1 text-info">· 已存有 SSO-RW</span>
              ) : (
                <span className="ml-1 text-text-tertiary/60">· 未设置</span>
              )}
            </div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              placeholder={
                item.has_sso_rw
                  ? '已加密保存，输入新值即可覆盖；留空表示保留原值'
                  : '可选，若有则粘贴 sso-rw 同样格式 JWT'
              }
              value={ssoRw}
              onChange={(e) => setSsoRw(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">支付链接</div>
            <input
              className="input input-sm w-full font-mono"
              value={paymentUrl}
              onChange={(e) => setPaymentUrl(e.target.value)}
              placeholder="https://x.com/i/account/subscriptions"
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">备注</div>
            <input
              className="input input-sm w-full"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
            />
          </label>
        </div>
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary btn-md" disabled={busy} onClick={submit}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </div>
    </div>
  );
}

// toLocalInput 把毫秒时间戳转成 <input type="datetime-local"> 接受的 "YYYY-MM-DDTHH:mm" 字符串。
function toLocalInput(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}

// BatchProgress 后台批量刷新任务的进度条 + 实时数字 + 取消按钮。
//
// 只在 status !== 'idle' 时渲染。3 种终态（completed/cancelled/failed）也
// 会显示一会儿（直到下一次手动启动新任务把它顶掉），方便用户回看结果。
function BatchProgress({
  job,
  scopeLabel,
  onCancel,
  cancelling,
}: {
  job: GrokPoolBatchRefreshJob;
  scopeLabel: string;
  onCancel: () => void;
  cancelling: boolean;
}) {
  const running = job.status === 'running';
  const scanned = job.scanned ?? 0;
  const ok = job.ok ?? 0;
  const fail = job.fail ?? 0;
  const elapsedSec = Math.max(0, Math.floor((job.elapsed_ms ?? 0) / 1000));
  const ratePerSec = elapsedSec > 0 ? scanned / elapsedSec : 0;

  const toneByStatus =
    job.status === 'completed'
      ? 'border-success/40 bg-success-soft'
      : job.status === 'cancelled'
        ? 'border-warn/40 bg-warn-soft'
        : job.status === 'failed'
          ? 'border-danger/40 bg-danger-soft'
          : 'border-info/40 bg-info-soft';

  return (
    <div
      className={`flex flex-col gap-2 rounded-lg border px-3 py-2 ${toneByStatus}`}
      role="status"
      aria-live="polite"
    >
      <div className="flex flex-wrap items-center gap-3 text-small">
        <div className="flex items-center gap-2">
          {running && <RefreshCw size={14} className="animate-spin text-info" />}
          <span className="font-medium text-text-primary">
            {running
              ? `扫描中：${scopeLabel}`
              : job.status === 'completed'
                ? `扫描完成：${scopeLabel}`
                : job.status === 'cancelled'
                  ? `已取消：${scopeLabel}`
                  : `扫描失败：${scopeLabel}`}
          </span>
        </div>
        <div className="text-text-secondary">
          已处理 <span className="font-mono text-text-primary">{scanned.toLocaleString()}</span>
          {' · '}
          <span className="text-success">成功 {ok.toLocaleString()}</span>
          {' · '}
          <span className={fail > 0 ? 'text-danger' : 'text-text-tertiary'}>
            失败 {fail.toLocaleString()}
          </span>
        </div>
        <div className="text-tiny text-text-tertiary">
          用时 {elapsedSec}s{running && ratePerSec > 0 && ` · 速率 ${ratePerSec.toFixed(1)}/s`}
        </div>
        {job.last_error && (
          <div
            className="line-clamp-1 max-w-[300px] text-tiny text-text-tertiary"
            title={job.last_error}
          >
            最后错误：{job.last_error}
          </div>
        )}
        <div className="ml-auto">
          {running ? (
            <button
              className="btn btn-outline btn-xs"
              disabled={cancelling}
              onClick={onCancel}
              title="标记任务取消；正在跑的账号会自然走完或撞超时"
            >
              <X size={12} /> {cancelling ? '取消中…' : '取消'}
            </button>
          ) : null}
        </div>
      </div>
      {!running && job.errors && job.errors.length > 0 && (
        <ul className="ml-1 list-disc space-y-0.5 pl-4 text-tiny text-text-tertiary">
          {job.errors.slice(0, 3).map((e, i) => (
            <li key={i} className="truncate" title={e.message}>
              {e.message} <span className="text-text-tertiary/70">×{e.count}</span>
            </li>
          ))}
          {job.errors.length > 3 && (
            <li className="text-text-tertiary/70">…还有 {job.errors.length - 3} 类错误</li>
          )}
        </ul>
      )}
    </div>
  );
}

// formatJobDoneHeading 终态任务的 toast 标题（单行）。
function formatJobDoneHeading(j: GrokPoolBatchRefreshJob): string {
  const scope = (j.scope && BATCH_SCOPE_LABEL[j.scope]) ?? '账号';
  const scanned = (j.scanned ?? 0).toLocaleString();
  const ok = (j.ok ?? 0).toLocaleString();
  const fail = (j.fail ?? 0).toLocaleString();
  switch (j.status) {
    case 'completed':
      return `扫描完成（${scope}）：${scanned} 个账号，成功 ${ok}，失败 ${fail}`;
    case 'cancelled':
      return `已取消（${scope}）：已处理 ${scanned}，成功 ${ok}，失败 ${fail}`;
    case 'failed':
      return `扫描失败（${scope}）：${j.last_error ?? '未知错误'}（已处理 ${scanned}）`;
    default:
      return `扫描结束（${scope}）`;
  }
}

// formatJobDoneDetail 失败 / 取消时的多行明细（包含错误样本）。
function formatJobDoneDetail(j: GrokPoolBatchRefreshJob, head: string): string {
  if (!j.errors || j.errors.length === 0) return head;
  const detail = j.errors
    .slice(0, 3)
    .map((e) => `· ${e.message} ×${e.count}`)
    .join('\n');
  const more = j.errors.length > 3 ? `\n…还有 ${j.errors.length - 3} 类错误` : '';
  return `${head}\n${detail}${more}`;
}
