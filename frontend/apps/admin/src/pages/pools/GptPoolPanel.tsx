import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  Crown,
  Download,
  Eye,
  EyeOff,
  Inbox,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
} from 'lucide-react';
import { type ChangeEvent, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError } from '../../lib/api';
import { poolGptApi, registerTaskApi, type GptPoolPlanFilter } from '../../lib/services';
import type {
  GptPlanType,
  GptPoolBatchRefreshBody,
  GptPoolDetail,
  GptPoolItem,
  GptPoolPurgeBody,
  GptPoolStatus,
  GptPoolUpdateBody,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, SplitMenu, fmtMs } from './_shared';
import UpgradePlusDrawer from './UpgradePlusDrawer';
import {
  Pager,
  Section,
  Stat,
  StatRow,
  Toolbar,
  ToolbarSpacer,
} from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const STATUS_OPTIONS: { value: '' | GptPoolStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'valid', label: '可用' },
  { value: 'invalid', label: '失效' },
  { value: 'cooldown', label: '冷却' },
  { value: 'disabled', label: '禁用' },
];

// PLAN_OPTIONS 档位筛选下拉：
//   - 第一组（首项）：全部档位
//   - 第二组（聚合快查）：所有未订阅 = Free + 未探测，便于一键圈出"待升 Plus"号
//   - 第三组（按官方档位精确筛）：Free / Plus / Pro / Team / Enterprise / 未探测
//
// '__unsubscribed' 是后端 dto 约定的特殊值；后端会展开成 plan_type IN
// (NULL, '', 'free', 'unknown')。
const PLAN_OPTIONS: { value: '' | GptPoolPlanFilter; label: string; group?: string }[] = [
  { value: '', label: '全部档位' },
  { value: '__unsubscribed', label: '所有未订阅 (Free + 未探测)', group: '快查' },
  { value: 'free', label: 'Free（普号）', group: '档位' },
  { value: 'plus', label: 'Plus', group: '档位' },
  { value: 'pro', label: 'Pro', group: '档位' },
  { value: 'team', label: 'Team', group: '档位' },
  { value: 'enterprise', label: 'Enterprise', group: '档位' },
  { value: 'unknown', label: '未探测', group: '档位' },
];

function statusBadge(status: string) {
  switch (status) {
    case 'valid':
      return { label: '可用', tone: 'bg-success-soft text-success' };
    case 'invalid':
      return { label: '失效', tone: 'bg-danger-soft text-danger' };
    case 'cooldown':
      return { label: '冷却', tone: 'bg-warn-soft text-warn' };
    case 'disabled':
      return { label: '禁用', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

// planBadge OpenAI 订阅档位的色彩 chip。
//
// 'free' 灰、'plus' 蓝、'pro' 紫、'team' / 'enterprise' 绿；其余 'unknown' / 留空显示 '-'。
function planBadge(plan?: string) {
  switch (plan) {
    case 'plus':
      return { label: 'Plus', tone: 'bg-info-soft text-info' };
    case 'pro':
      return { label: 'Pro', tone: 'bg-purple-100 text-purple-700' };
    case 'team':
      return { label: 'Team', tone: 'bg-success-soft text-success' };
    case 'enterprise':
      return { label: 'Enterprise', tone: 'bg-success-soft text-success' };
    case 'free':
      return { label: 'Free', tone: 'bg-surface-2 text-text-secondary' };
    case 'unknown':
    case '':
    case undefined:
      return { label: '未探测', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: plan, tone: 'bg-surface-2 text-text-secondary' };
  }
}

// QuotaBar 把 0~100 已用百分比渲染成一段彩色进度条 + 数字。
//
// 阈值：< 70 绿色 / 70~89 黄色 / >=90 红色。
function QuotaBar({ used, label }: { used?: number; label: string }) {
  if (used == null) {
    return (
      <div className="flex items-center gap-1 text-tiny text-text-tertiary">
        <span className="w-10 truncate">{label}</span>
        <span>—</span>
      </div>
    );
  }
  const pct = Math.max(0, Math.min(100, used));
  let bar = 'bg-success';
  if (pct >= 90) bar = 'bg-danger';
  else if (pct >= 70) bar = 'bg-warn';
  return (
    <div className="flex items-center gap-1 text-tiny">
      <span className="w-10 shrink-0 truncate text-text-tertiary">{label}</span>
      <div className="relative h-1.5 w-16 overflow-hidden rounded-full bg-surface-2">
        <div className={`absolute inset-y-0 left-0 ${bar}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="w-10 shrink-0 text-right tabular-nums text-text-secondary">
        {pct.toFixed(0)}%
      </span>
    </div>
  );
}

// expiresHint 把 expires_at 渲染成 "12-31 14:30 (剩 3h)" 这种带相对剩余时间的字符串。
function expiresHint(ms?: number): { text: string; tone: string } {
  if (!ms) return { text: '—', tone: 'text-text-tertiary' };
  const remain = ms - Date.now();
  const abs = fmtMs(ms);
  if (remain <= 0) return { text: `${abs} (已过期)`, tone: 'text-danger' };
  const h = Math.floor(remain / 3_600_000);
  const m = Math.floor((remain % 3_600_000) / 60_000);
  const rel = h >= 24 ? `${Math.floor(h / 24)}d` : h > 0 ? `${h}h${m}m` : `${m}m`;
  const tone = h < 12 ? 'text-warn' : 'text-text-secondary';
  return { text: `${abs} · 剩 ${rel}`, tone };
}

export default function GptPoolPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | GptPoolStatus>('');
  const [planFilter, setPlanFilter] = useState<'' | GptPoolPlanFilter>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [editingItem, setEditingItem] = useState<GptPoolItem | null>(null);
  const [refreshingID, setRefreshingID] = useState<number | null>(null);
  const [showUpgradeDrawer, setShowUpgradeDrawer] = useState(false);
  // 派发期间显示一个 inflight 计数（用于防止重复点击 + 顶部按钮 spinner）
  const [dispatchInflight, setDispatchInflight] = useState(false);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      plan_type: planFilter || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, page, pageSize, status, planFilter],
  );

  const list = useQuery({
    queryKey: ['admin', 'pool-gpt', 'list', query],
    queryFn: () => poolGptApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'pool-gpt', 'stats'],
    queryFn: () => poolGptApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'pool-gpt'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => poolGptApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('账号已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => poolGptApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const updateOne = useMutation({
    mutationFn: ({ id, body }: { id: number; body: GptPoolUpdateBody }) =>
      poolGptApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditingItem(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message || '保存失败'),
  });

  // 单条刷新：refresh AT/RT + 拿 plan/quota。失败时只 toast，不弹 confirm。
  const refreshOne = useMutation({
    mutationFn: ({ id, onlyQuota }: { id: number; onlyQuota?: boolean }) =>
      poolGptApi.refresh(id, !!onlyQuota),
    onMutate: ({ id }) => setRefreshingID(id),
    onSuccess: (det) => {
      refresh();
      const plan = det?.plan_type ? `(${det.plan_type})` : '';
      toast.success(`刷新成功 ${plan}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '刷新失败'),
    onSettled: () => setRefreshingID(null),
  });

  const batchRefresh = useMutation({
    mutationFn: (body: GptPoolBatchRefreshBody) => poolGptApi.batchRefresh(body),
    onSuccess: (r) => {
      refresh();
      toast.success(`扫描完成：${r.ok}/${r.total} 成功，${r.fail} 失败`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量刷新失败'),
  });

  const purge = useMutation({
    mutationFn: (body: GptPoolPurgeBody) => poolGptApi.purge(body),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量删除失败'),
  });

  // doExport 触发浏览器下载。format 决定文件后缀，scope 决定文件名前缀。
  // 失败时 toast；成功时 toast 显示导出条数。
  const doExport = async (
    scope: 'all' | 'valid' | 'invalid' | 'selected',
    format: 'internal' | 'crs' | 'codex' | 'account_password',
  ) => {
    if (scope === 'selected' && selected.size === 0) {
      toast.error('请先勾选要导出的账号');
      return;
    }
    try {
      const ids = scope === 'selected' ? Array.from(selected) : undefined;
      const { text, count } = await poolGptApi.exportText(scope, format, ids);
      if (!text || count === 0) {
        toast.error('没有可导出的账号');
        return;
      }
      const ext = format === 'account_password' ? 'txt' : 'json';
      const mime =
        format === 'account_password'
          ? 'text/plain;charset=utf-8'
          : 'application/json;charset=utf-8';
      const ts = Math.floor(Date.now() / 1000);
      const filename = `gpt-${scope}-${format}-${ts}.${ext}`;
      const blob = new Blob([text], { type: mime });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      toast.success(`已导出 ${count} 条 → ${filename}`);
    } catch (e) {
      toast.error((e as ApiError).message || '导出失败');
    }
  };

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

  // 二次确认 + 调用 batchRefresh 的小工具。
  const confirmBatchRefresh = async (
    body: GptPoolBatchRefreshBody,
    title: string,
    description: string,
  ) => {
    const ok = await confirm({
      title,
      description,
      tone: 'primary',
      confirmLabel: '开始扫描',
    });
    if (ok) batchRefresh.mutate(body);
  };

  // dispatchUpgradePlus 真实派发入口，跑 P4 那条 GoPay 15 步链路。
  //
  // scope 取值
  //   - 'selected'    ：当前勾选的行；从 selected Set 拿 ID + 当前 page 拿 email
  //   - 'unsubscribed'：服务端按 plan_type=__unsubscribed 拉全量（最多 5000）
  //   - 'free_only'   ：服务端按 plan_type=free 拉全量（最多 5000）
  //
  // 实现策略：
  //   后端 register_task.create 一次只支持单个 payload，没有"批量按 IDs 派发"的
  //   端点（保持 dispatcher 与既有 adobe/grok/gpt 注册任务一致），所以前端循环
  //   N 次 POST。每次都把 pool_gpt_id + gpt_email 塞进 payload，dispatcher 拿
  //   pool_gpt_id 去 LoadCredentials；前端抽屉拿 gpt_email 做友好显示。
  //
  //   并发：浏览器端默认按串行 await（避免 admin 网关瞬时洪峰），单条 ~50ms，
  //   1000 条约 50s 可控。如需更快，后端再开 batch endpoint。
  const dispatchUpgradePlus = async (
    scope: 'selected' | 'unsubscribed' | 'free_only',
  ) => {
    if (dispatchInflight) {
      toast.info('已有派发任务在进行中，请稍候');
      return;
    }
    // Step1：解析候选号（id + email）
    let candidates: { id: number; email?: string }[] = [];
    if (scope === 'selected') {
      if (selected.size === 0) {
        toast.error('请先勾选要升级的账号');
        return;
      }
      const byID = new Map(items.map((it) => [it.id, it.email] as const));
      candidates = Array.from(selected).map((id) => ({ id, email: byID.get(id) }));
    } else {
      const planFilterValue: GptPoolPlanFilter = scope === 'free_only' ? 'free' : '__unsubscribed';
      try {
        // 拉一次大页 拿全部待升 ID。后端 page_size 上限 1000，实在更多需要后端开
        // 专门接口；当前足够覆盖单次操作。
        const resp = await poolGptApi.list({
          status: 'valid',
          plan_type: planFilterValue,
          page: 1,
          page_size: 1000,
        });
        candidates = (resp.list ?? []).map((it) => ({ id: it.id, email: it.email }));
      } catch (e) {
        toast.error((e as ApiError).message || '拉取候选号失败');
        return;
      }
      if (candidates.length === 0) {
        toast.info(scope === 'free_only' ? '暂无 Free 档号' : '暂无未订阅号');
        return;
      }
    }

    // Step2：二次确认（10 个以上必须确认）
    if (candidates.length >= 10) {
      const ok = await confirm({
        title: '批量开 Plus',
        description: `将派发 ${candidates.length} 个升级任务（GoPay 15 步流，每个号约耗时 60-120s，并占用 1 个钱包/手机/代理资源）。请确认资源池容量充足。`,
        tone: 'primary',
        confirmLabel: `开通 ${candidates.length} 个`,
      });
      if (!ok) return;
    }

    // Step3：循环派发
    setDispatchInflight(true);
    setShowUpgradeDrawer(true); // 立即弹抽屉，实时看进度
    let okCount = 0;
    let failCount = 0;
    for (const c of candidates) {
      try {
        await registerTaskApi.create({
          provider: 'upgrade_plus',
          count: 1,
          payload: { pool_gpt_id: c.id, gpt_email: c.email },
        });
        okCount++;
      } catch (e) {
        failCount++;
        // 单条失败不打断；最后汇总 toast
        // eslint-disable-next-line no-console
        console.warn('upgrade_plus dispatch failed', c, e);
      }
    }
    setDispatchInflight(false);
    qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });

    if (failCount === 0) {
      toast.success(`已派发 ${okCount} 个升级任务，进度看右侧抽屉`);
    } else {
      toast.error(`派发完成：${okCount} 成功 / ${failCount} 失败，详情看右侧抽屉`);
    }
  };

  const confirmPurge = async (
    body: GptPoolPurgeBody,
    description: string,
  ) => {
    const ok = await confirm({
      title: '批量删除 GPT 账号',
      description,
      tone: 'danger',
      confirmLabel: '删除',
    });
    if (ok) purge.mutate(body);
  };

  return (
    <div className="space-y-3">
      <StatRow cols={5}>
        <Stat label="总数" value={stats.data?.total ?? 0} />
        <Stat label="可用" value={stats.data?.valid ?? 0} tone="text-success" />
        <Stat label="失效" value={stats.data?.invalid ?? 0} tone="text-danger" />
        <Stat label="冷却" value={stats.data?.cooldown ?? 0} tone="text-warn" />
        <Stat label="禁用" value={stats.data?.disabled ?? 0} tone="text-text-tertiary" />
      </StatRow>

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
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as GptPoolStatus | '');
            setPage(1);
          }}
        >
          {STATUS_OPTIONS.map((o) => (
            <option key={o.value || 'all'} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          className="select select-sm"
          value={planFilter}
          onChange={(e) => {
            setPlanFilter(e.target.value as GptPoolPlanFilter | '');
            setPage(1);
          }}
          title="按 OpenAI 订阅档位筛选；「所有未订阅」= Free + 未探测"
        >
          <option value="">全部档位</option>
          <optgroup label="快查">
            {PLAN_OPTIONS.filter((o) => o.group === '快查').map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </optgroup>
          <optgroup label="按档位">
            {PLAN_OPTIONS.filter((o) => o.group === '档位').map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </optgroup>
        </select>
        <ToolbarSpacer />
        <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
          <RefreshCw size={14} /> 刷新列表
        </button>
        {/* 扫描续期：参考 Adobe，把 token / quota 5 个常用入口放到下拉。 */}
        <SplitMenu
          label="扫描续期"
          icon={<Activity size={14} />}
          busy={batchRefresh.isPending}
          items={[
            {
              label: '刷新即将过期 Token',
              description: '< 12h 过期的有效号 → 走 /oauth/token 换新 AT/RT',
              onClick: () =>
                confirmBatchRefresh(
                  { scope: 'expiring' },
                  '刷新即将过期 Token',
                  '会对所有 12 小时内即将过期的 GPT 账号执行 silent refresh，可能消耗少量代理流量。',
                ),
            },
            {
              label: '刷新异常账号',
              description: 'status != valid 的号 → 完整刷新 AT/RT + quota',
              onClick: () =>
                confirmBatchRefresh(
                  { scope: 'abnormal' },
                  '刷新异常账号',
                  '会对所有非 valid（失效 / 冷却 / 禁用）账号尝试 refresh，可恢复部分号。',
                ),
            },
            {
              label: '刷新全部账号 Token + 配额',
              description: '所有号 → 换 AT/RT + 拉 wham/usage（耗时较长）',
              onClick: () =>
                confirmBatchRefresh(
                  { scope: 'all' },
                  '刷新全部账号',
                  '会对账号池中全部 GPT 账号执行完整刷新，请确认有充足代理配额。',
                ),
            },
            {
              label: '只刷过期配额',
              description: 'last_quota_check_at > 30min → 只调 wham/usage',
              onClick: () =>
                confirmBatchRefresh(
                  { scope: 'quota_stale', only_quota: true },
                  '只刷过期配额',
                  '只调用 wham/usage 增量刷新 plan / 短长窗口百分比，不换 token，便宜得多。',
                ),
            },
            {
              label: '只刷全部账号配额',
              description: '所有号 → 只调 wham/usage（不换 token）',
              onClick: () =>
                confirmBatchRefresh(
                  { scope: 'all', only_quota: true },
                  '只刷全部账号配额',
                  '所有账号都重新探测 plan / 配额，但不换 token；适合"只想看一眼额度"。',
                ),
            },
          ]}
        />
        {/* 订阅升级：派发 register_task(provider=upgrade_plus)，跑 GoPay 15 步流。
            进度走右侧 UpgradePlusDrawer（点本工具栏「升级任务」按钮唤起）。 */}
        <SplitMenu
          label="订阅升级"
          icon={<Crown size={14} />}
          busy={dispatchInflight}
          items={[
            {
              label: `选中的号 → 开 Plus (${selected.size})`,
              description: '对当前勾选的账号批量派发升级任务（GoPay 15 步流）',
              disabled: selected.size === 0,
              onClick: () => dispatchUpgradePlus('selected'),
            },
            {
              label: '所有未订阅号 → 开 Plus',
              description: '服务端拉 plan_type ∈ (free, 未探测) 的全部号（最多 1000 条）',
              onClick: () => dispatchUpgradePlus('unsubscribed'),
            },
            {
              label: '只对 Free 档号开 Plus',
              description: '只升 plan_type=free（已探测过、确认是免费档）',
              onClick: () => dispatchUpgradePlus('free_only'),
            },
            {
              label: '取消订阅（保留功能位）',
              description: '请到「Plus 升级资源池 → 钱包池 → 绑定」中取消（暂未在此快捷入口）',
              disabled: true,
              onClick: () => undefined,
            },
          ]}
        />
        {/* 升级任务抽屉：随时看 upgrade_plus 历史/进行中任务，点开看实时日志 */}
        <button
          type="button"
          className="btn btn-outline btn-sm"
          onClick={() => setShowUpgradeDrawer(true)}
          title="查看 Plus 升级任务的历史和实时进度"
        >
          <Crown size={14} /> 升级任务
        </button>
        {/* 导出：4 种格式 × 4 种 scope。下拉里把"按格式分组"展开。 */}
        <SplitMenu
          label="导出"
          icon={<Download size={14} />}
          items={[
            // ---- 内部 JSON（导入完全互通） ----
            {
              label: `导出选中 · 内部 JSON (${selected.size})`,
              description: '只导出当前选中的行，含全部字段（明文）',
              disabled: selected.size === 0,
              onClick: () => doExport('selected', 'internal'),
            },
            {
              label: '导出全部 · 内部 JSON',
              description: '号池全部账号，扁平 JSON Array，可直接粘回导入',
              onClick: () => doExport('all', 'internal'),
            },
            {
              label: '导出有效 · 内部 JSON',
              description: '只导 status = valid',
              onClick: () => doExport('valid', 'internal'),
            },
            {
              label: '导出失效 · 内部 JSON',
              description: '只导 status = invalid（用于备份 / 排查）',
              onClick: () => doExport('invalid', 'internal'),
            },
            // ---- CRS（claude-relay-service / chatgpt-pool 格式）----
            {
              label: '导出全部 · CRS 格式',
              description: '{exported_at, accounts:[{credentials,extra,...}]}',
              onClick: () => doExport('all', 'crs'),
            },
            {
              label: '导出有效 · CRS 格式',
              description: '只导 valid，CRS 包装格式',
              onClick: () => doExport('valid', 'crs'),
            },
            // ---- Codex 单文件 token 风格（合并成 Array）----
            {
              label: '导出全部 · Codex Array',
              description: 'token_xxx_xxx_<unix>.json 单 object 风格合 Array',
              onClick: () => doExport('all', 'codex'),
            },
            {
              label: '导出有效 · Codex Array',
              description: '只导 valid，含 id_token / password',
              onClick: () => doExport('valid', 'codex'),
            },
            // ---- 账号密码 txt ----
            {
              label: '导出全部 · 账号:密码 (txt)',
              description: '一行一条 email:password，不含 token',
              onClick: () => doExport('all', 'account_password'),
            },
          ]}
        />
        {/* 导入：弹窗（粘 JSON / 文本） + 文件选择按钮 */}
        <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} /> 导入
        </button>
        {/* 删除：4 个常用入口。selected.size 仍走原批量删（精确选择）。 */}
        <SplitMenu
          label={selected.size > 0 ? `删除 (${selected.size})` : '删除'}
          icon={<Trash2 size={14} />}
          tone="danger"
          busy={batchDelete.isPending || purge.isPending}
          items={[
            {
              label: `删除选中 (${selected.size})`,
              description: '只删除当前选中的行',
              tone: 'danger',
              disabled: selected.size === 0,
              onClick: async () => {
                const ok = await confirm({
                  title: '批量删除 GPT 账号',
                  description: `将永久删除选中的 ${selected.size} 条 GPT 账号记录（含 access / refresh token），删除后该账号将不再被网关复用。`,
                  tone: 'danger',
                  confirmLabel: '删除',
                });
                if (ok) batchDelete.mutate(Array.from(selected));
              },
            },
            {
              label: '删除失效账号',
              description: 'status = invalid 的全部账号',
              tone: 'danger',
              onClick: () =>
                confirmPurge(
                  { scope: 'invalid' },
                  '删除所有 status=invalid 的 GPT 账号（被 OpenAI 拒收 / refresh 401 等）。',
                ),
            },
            {
              label: '删除 Token 已过期',
              description: 'expires_at <= now 的账号',
              tone: 'danger',
              onClick: () =>
                confirmPurge(
                  { scope: 'token_expired' },
                  '删除所有 access_token 已过期的账号（如果没有 refresh_token，等于死号）。',
                ),
            },
            {
              label: '删除满额账号',
              description: '短窗口已用 = 100% 的账号',
              tone: 'danger',
              onClick: () =>
                confirmPurge(
                  { scope: 'quota_exceeded' },
                  '删除所有短窗口（5h）已用 100% 的账号；通常意味着被滥用或配额耗尽。',
                ),
            },
            {
              label: '删除无 Refresh Token',
              description: '没 refresh_token 的死号（手动导入残缺）',
              tone: 'danger',
              onClick: () =>
                confirmPurge(
                  { scope: 'no_refresh' },
                  '删除所有缺失 refresh_token 的账号——这些号过期就废了，没法续命。',
                ),
            },
            {
              label: '删除全部',
              description: '⚠ 整库清空',
              tone: 'danger',
              onClick: () =>
                confirmPurge(
                  { scope: 'all' },
                  '⚠ 此操作会删除号池中所有 GPT 账号。此操作不可恢复，请务必确认。',
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
                <th className="px-3 py-2 text-left">状态 / 套餐</th>
                <th className="px-3 py-2 text-left">凭证</th>
                <th className="px-3 py-2 text-left">配额（已用）</th>
                <th className="px-3 py-2 text-left">Token 失效</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={7} className="px-3 py-6 text-center text-text-tertiary">
                    加载中…
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-3 py-8 text-center text-text-tertiary">
                    <Inbox size={20} className="mx-auto mb-1" />
                    暂无账号
                  </td>
                </tr>
              )}
              {items.map((it) => {
                const exp = expiresHint(it.expires_at);
                return (
                  <tr key={it.id} className="border-b border-border last:border-0 hover:bg-surface-2/60">
                    <td className="px-3 py-1.5">
                      <input
                        type="checkbox"
                        checked={selected.has(it.id)}
                        onChange={() => onToggleOne(it.id)}
                      />
                    </td>
                    <td className="px-3 py-1.5 font-mono text-text-primary">
                      <div>{it.email}</div>
                      {it.error_message && (
                        <div
                          className="mt-0.5 max-w-[260px] truncate text-tiny text-danger/80"
                          title={it.error_message}
                        >
                          {it.error_message}
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-1.5">
                      <div className="flex flex-col gap-1">
                        <Badge {...statusBadge(it.status)} />
                        <Badge {...planBadge(it.plan_type)} />
                      </div>
                    </td>
                    <td className="px-3 py-1.5">
                      <div className="flex flex-wrap gap-1">
                        <FlagPill ok={it.has_password} label="pwd" />
                        <FlagPill ok={it.has_access_token} label="AT" />
                        <FlagPill ok={it.has_refresh_token} label="RT" />
                        <FlagPill ok={!!it.has_id_token} label="IDT" />
                      </div>
                    </td>
                    <td className="px-3 py-1.5">
                      <div className="flex flex-col gap-0.5">
                        <QuotaBar used={it.quota_primary_used_percent} label="短(5h)" />
                        <QuotaBar used={it.quota_secondary_used_percent} label="长(7d)" />
                        {it.quota_code_review_used_percent != null && (
                          <QuotaBar
                            used={it.quota_code_review_used_percent}
                            label="评审"
                          />
                        )}
                        {it.last_quota_check_at && (
                          <div className="text-tiny text-text-tertiary/70">
                            探测于 {fmtMs(it.last_quota_check_at)}
                          </div>
                        )}
                      </div>
                    </td>
                    <td className={`px-3 py-1.5 ${exp.tone}`}>
                      <div>{exp.text}</div>
                      {it.last_refresh_at && (
                        <div className="text-tiny text-text-tertiary">
                          续期 {fmtMs(it.last_refresh_at)}
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-1.5 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <button
                          className="btn btn-ghost btn-xs"
                          title="立即刷新（换 AT/RT + 拉 wham/usage 拿 plan/quota）"
                          disabled={refreshingID === it.id}
                          onClick={() => refreshOne.mutate({ id: it.id })}
                        >
                          <RefreshCw
                            size={12}
                            className={refreshingID === it.id ? 'animate-spin' : ''}
                          />
                          刷新
                        </button>
                        <button
                          className="btn btn-ghost btn-xs"
                          title="编辑账号字段（凭证 / 状态 / 备注）"
                          onClick={() => setEditingItem(it)}
                        >
                          <Pencil size={12} /> 编辑
                        </button>
                        <button
                          className="btn btn-ghost btn-xs text-danger"
                          onClick={async () => {
                            const ok = await confirm({
                              title: '删除 GPT 账号',
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
                );
              })}
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
        <GptImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />
      )}
      {editingItem && (
        <GptEditDialog
          item={editingItem}
          busy={updateOne.isPending}
          onClose={() => setEditingItem(null)}
          onSubmit={(body) => updateOne.mutate({ id: editingItem.id, body })}
        />
      )}
      {confirmDialog}
      <UpgradePlusDrawer
        open={showUpgradeDrawer}
        onClose={() => setShowUpgradeDrawer(false)}
      />
    </div>
  );
}

// GptEditDialog 单条 GPT 账号编辑对话框。
//
// 与 Adobe 不同：默认 mount 时拉 detail 拿到明文 password / token 并填入文本框，
// 让运维能直接看到 RT/AT/IDT 内容（仍提供"显示/隐藏"切换避免肩膀偷窥）。
//
// 顶部新增"账号画像"只读区：plan_type / 短长窗口已用 / 上次探测时间。
//
// 提交时只把改动过的字段发回去（dirty diff）；留空 = 不变（避免误清）。
function GptEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: GptPoolItem;
  busy?: boolean;
  onClose: () => void;
  onSubmit: (body: GptPoolUpdateBody) => void;
}) {
  const detail = useQuery({
    queryKey: ['admin', 'pool-gpt', 'detail', item.id],
    queryFn: () => poolGptApi.detail(item.id),
  });

  // 默认明文显示，方便运维直接复制 RT/AT/密码；右上角"隐藏"按钮可切回 disc 掩码。
  const [showSecrets, setShowSecrets] = useState(true);
  const [status, setStatus] = useState<GptPoolStatus>(
    (item.status as GptPoolStatus) ?? 'valid',
  );
  const [oauthIssuer, setOauthIssuer] = useState(item.oauth_issuer ?? '');
  const [oauthClientID, setOauthClientID] = useState(item.oauth_client_id ?? '');
  const [password, setPassword] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [idToken, setIdToken] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [notes, setNotes] = useState(item.notes ?? '');
  const [hydrated, setHydrated] = useState(false);

  // detail 拿到后回填明文凭证字段（只回填一次，避免覆盖用户编辑中的内容）。
  useEffect(() => {
    if (!detail.data || hydrated) return;
    const d: GptPoolDetail = detail.data;
    setPassword(d.password ?? '');
    setAccessToken(d.access_token ?? '');
    setRefreshToken(d.refresh_token ?? '');
    setIdToken(d.id_token ?? '');
    setApiKey(d.api_key ?? '');
    setOauthIssuer(d.oauth_issuer ?? oauthIssuer);
    setOauthClientID(d.oauth_client_id ?? oauthClientID);
    setNotes(d.notes ?? notes);
    setHydrated(true);
  }, [detail.data, hydrated, oauthIssuer, oauthClientID, notes]);

  function submit() {
    const body: GptPoolUpdateBody = {};
    const d = detail.data;
    if (status !== item.status) body.status = status;
    if (oauthIssuer !== (d?.oauth_issuer ?? '')) body.oauth_issuer = oauthIssuer;
    if (oauthClientID !== (d?.oauth_client_id ?? '')) body.oauth_client_id = oauthClientID;
    if (notes !== (d?.notes ?? '')) body.notes = notes;
    if (password !== (d?.password ?? '')) body.password = password;
    if (accessToken !== (d?.access_token ?? '')) body.access_token = accessToken;
    if (refreshToken !== (d?.refresh_token ?? '')) body.refresh_token = refreshToken;
    if (idToken !== (d?.id_token ?? '')) body.id_token = idToken;
    if (apiKey !== (d?.api_key ?? '')) body.api_key = apiKey;
    if (Object.keys(body).length === 0) {
      toast.success('没有修改');
      onClose();
      return;
    }
    onSubmit(body);
  }

  const inputType = showSecrets ? 'text' : 'password';
  const credentialClass = 'input min-h-[60px] w-full font-mono text-tiny';
  const d = detail.data;

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header className="flex items-start justify-between gap-3">
          <div>
            <h3 className="text-h4 text-text-primary">编辑 GPT 账号</h3>
            <p className="mt-1 text-small text-text-tertiary">
              <span className="font-mono">{item.email}</span> · #{item.id}
            </p>
          </div>
          <button
            className="btn btn-ghost btn-xs"
            type="button"
            onClick={() => setShowSecrets((v) => !v)}
            title={showSecrets ? '隐藏凭证明文' : '显示凭证明文'}
          >
            {showSecrets ? <EyeOff size={14} /> : <Eye size={14} />}
            {showSecrets ? '隐藏' : '显示'}
          </button>
        </header>

        {/* 账号画像（只读）：plan / quota / 探测时间 */}
        <div className="rounded-md border border-border bg-surface-2/40 p-3">
          <div className="mb-1 text-tiny uppercase tracking-wide text-text-tertiary">
            账号画像 · 来源 wham/usage
          </div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <div>
              <div className="text-tiny text-text-tertiary">套餐</div>
              <Badge {...planBadge((d?.plan_type as GptPlanType) ?? item.plan_type)} />
            </div>
            <div>
              <div className="text-tiny text-text-tertiary">短窗口已用</div>
              <div className="text-small tabular-nums text-text-primary">
                {(d?.quota_primary_used_percent ?? item.quota_primary_used_percent) != null
                  ? `${(d?.quota_primary_used_percent ?? item.quota_primary_used_percent)?.toFixed(1)}%`
                  : '—'}
              </div>
            </div>
            <div>
              <div className="text-tiny text-text-tertiary">长窗口已用</div>
              <div className="text-small tabular-nums text-text-primary">
                {(d?.quota_secondary_used_percent ?? item.quota_secondary_used_percent) != null
                  ? `${(d?.quota_secondary_used_percent ?? item.quota_secondary_used_percent)?.toFixed(1)}%`
                  : '—'}
              </div>
            </div>
            <div>
              <div className="text-tiny text-text-tertiary">上次探测</div>
              <div className="text-small text-text-primary">
                {fmtMs(d?.last_quota_check_at ?? item.last_quota_check_at)}
              </div>
            </div>
          </div>
          {(d?.chatgpt_account_id ?? item.chatgpt_account_id) && (
            <div className="mt-2 truncate font-mono text-tiny text-text-tertiary">
              account_id：{d?.chatgpt_account_id ?? item.chatgpt_account_id}
            </div>
          )}
        </div>

        {detail.isLoading && (
          <div className="rounded-md bg-surface-2 px-3 py-2 text-tiny text-text-tertiary">
            正在加载凭证明文…
          </div>
        )}
        {detail.error && (
          <div className="rounded-md bg-danger-soft px-3 py-2 text-tiny text-danger">
            加载明文失败：{(detail.error as ApiError).message}
          </div>
        )}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <div className="text-tiny text-text-tertiary">状态</div>
            <select
              className="select select-sm w-full"
              value={status}
              onChange={(e) => setStatus(e.target.value as GptPoolStatus)}
            >
              <option value="valid">可用</option>
              <option value="invalid">失效</option>
              <option value="cooldown">冷却</option>
              <option value="disabled">禁用</option>
            </select>
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">OAuth Issuer</div>
            <input
              className="input input-sm w-full font-mono"
              value={oauthIssuer}
              onChange={(e) => setOauthIssuer(e.target.value)}
              placeholder="https://auth.openai.com"
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">OAuth Client ID</div>
            <input
              className="input input-sm w-full font-mono"
              value={oauthClientID}
              onChange={(e) => setOauthClientID(e.target.value)}
              placeholder="app_2SKx67Edpo... (platform) / app_EMoamEEZ73f0... (codex)"
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Password</div>
            <input
              className="input input-sm w-full font-mono"
              type={inputType}
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              Access Token{accessToken && ` · ${accessToken.length} 字符`}
            </div>
            <textarea
              className={credentialClass}
              style={!showSecrets ? ({ WebkitTextSecurity: 'disc' } as never) : undefined}
              value={accessToken}
              onChange={(e) => setAccessToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              Refresh Token{refreshToken && ` · ${refreshToken.length} 字符`}
            </div>
            <textarea
              className={credentialClass}
              style={!showSecrets ? ({ WebkitTextSecurity: 'disc' } as never) : undefined}
              value={refreshToken}
              onChange={(e) => setRefreshToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              ID Token{idToken && ` · ${idToken.length} 字符`}
            </div>
            <textarea
              className={credentialClass}
              style={!showSecrets ? ({ WebkitTextSecurity: 'disc' } as never) : undefined}
              value={idToken}
              onChange={(e) => setIdToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">
              API Key{apiKey && ` · ${apiKey.length} 字符`}
            </div>
            <input
              className="input input-sm w-full font-mono"
              type={inputType}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="sk-... (可选；platform AT 也可直接调 API)"
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
        <div className="flex items-center justify-between gap-2">
          <div className="text-tiny text-text-tertiary">
            注册时间 {fmtMs(item.registered_at)} · OAuth 过期 {fmtMs(item.expires_at)}
          </div>
          <div className="flex gap-2">
            <button className="btn btn-outline btn-md" onClick={onClose}>
              取消
            </button>
            <button
              className="btn btn-primary btn-md"
              disabled={busy || detail.isLoading}
              onClick={submit}
            >
              {busy ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function GptImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const fileRef = useRef<HTMLInputElement | null>(null);
  const importMut = useMutation({
    mutationFn: () => poolGptApi.import({ text, format: 'auto' }),
    onSuccess: (r) => {
      toast.success(`导入完成：成功 ${r.imported}，跳过 ${r.skipped}`);
      if (r.errors?.length) toast.error(r.errors.slice(0, 3).join('；'));
      onDone();
      onClose();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  // onPickFiles 支持一次选多个文件，把内容拼到 textarea。
  // 行级解析的兼容性：每个 JSON 文件用换行隔开放上去后，service 层
  // parseGptImportWholeJSON 会按 "整段 JSON" 优先尝试；如果整段不是合法
  // JSON（多文件拼一起就是这种情况），会逐行兜底。
  // 为了让多文件场景也走 "整段 JSON 优先"，我们按 "一个文件一次提交" 的
  // 方式串行 fire-and-forget 调 import 接口。
  const onPickFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    const all = Array.from(files);
    let totalOK = 0;
    let totalSkip = 0;
    const errs: string[] = [];
    for (const f of all) {
      try {
        const t = await f.text();
        if (!t.trim()) continue;
        const r = await poolGptApi.import({ text: t, format: 'auto' });
        totalOK += r.imported;
        totalSkip += r.skipped;
        if (r.errors?.length) errs.push(...r.errors);
      } catch (e) {
        errs.push(`${f.name}: ${(e as Error).message}`);
      }
    }
    toast.success(`已处理 ${all.length} 个文件：成功 ${totalOK}，跳过 ${totalSkip}`);
    if (errs.length) toast.error(errs.slice(0, 3).join('；'));
    onDone();
    onClose();
  };

  return (
    <ImportDialogShell
      title="批量导入 GPT 账号"
      description={
        <span>
          支持 <strong>5 种格式</strong>，自动识别：
          <br />
          1) <code className="font-mono">email:password[:refresh_token]</code> 一行一条；
          <br />
          2) 每行一个 JSON 对象；
          <br />
          3) 整段 JSON Array <code className="font-mono">[{`{...}, {...}`}]</code>；
          <br />
          4) CRS 风格 <code className="font-mono">{`{"accounts":[...]}`}</code>；
          <br />
          5) Codex 单文件 <code className="font-mono">{`{"id_token","access_token","refresh_token","email","type":"codex"}`}</code>。
          <br />
          多文件可一次选中，按文件分别上传。
        </span>
      }
      onClose={onClose}
      busy={importMut.isPending}
      onConfirm={() => importMut.mutate()}
    >
      <div className="flex items-center gap-2">
        <input
          ref={fileRef}
          type="file"
          multiple
          accept=".json,.txt,application/json,text/plain"
          className="hidden"
          onChange={(e) => onPickFiles(e.target.files)}
        />
        <button
          type="button"
          className="btn btn-outline btn-sm"
          onClick={() => fileRef.current?.click()}
        >
          <Upload size={14} /> 选择文件批量导入
        </button>
        <span className="text-xs text-text-tertiary">
          支持 .json / .txt 多选，一次性导入多个 CRS / codex 文件
        </span>
      </div>
      <textarea
        className="input min-h-[260px] font-mono"
        placeholder='abc@gmail.com:password123
{"email":"def@gmail.com","refresh_token":"...","oauth_client_id":"app_EMoamEEZ73f0CkXaXp7hrann"}
[{"email":"...","access_token":"...","refresh_token":"..."}]
{"exported_at":"...","accounts":[{"name":"...","credentials":{...}}]}'
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
    </ImportDialogShell>
  );
}
