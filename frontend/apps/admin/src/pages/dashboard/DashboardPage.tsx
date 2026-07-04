import { useQuery } from '@tanstack/react-query';
import {
  Activity,
  BarChart3,
  Image,
  KeyRound,
  LayoutDashboard,
  RefreshCw,
  Sparkles,
  Video,
} from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';

import { dashboardApi } from '../../lib/services';
import type {
  DashboardProviderRow,
  DashboardRecentTask,
  DashboardTrendPoint,
} from '../../lib/types';
import { fmtNumber, fmtPoints, fmtTime } from '../../lib/format';
import { PageHeader, PageShell, Section, Stat, StatRow } from '../../components/layout/PageShell';

export default function DashboardPage() {
  const { data, isFetching, refetch } = useQuery({
    queryKey: ['admin', 'dashboard', 'overview'],
    queryFn: () => dashboardApi.overview(),
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
  });

  const providers = data?.account_providers ?? [];
  const totalAccounts = providers.reduce((s, r) => s + r.total, 0);
  const availableAccounts = providers.reduce((s, r) => s + r.available, 0);
  const quotaRemaining = providers.reduce((s, r) => s + r.quota_remaining, 0);
  const quotaTotal = providers.reduce((s, r) => s + r.quota_total, 0);
  const recent = data?.recent_generations ?? [];

  return (
    <PageShell>
      <PageHeader
        icon={<LayoutDashboard size={16} />}
        title="运营仪表盘"
        right={
          <button className="btn btn-outline btn-sm" onClick={() => refetch()} disabled={isFetching}>
            <RefreshCw size={14} className={isFetching ? 'animate-spin' : ''} /> 刷新
          </button>
        }
      />

      <StatRow cols={6}>
        <Stat
          label="今日生成"
          value={fmtNumber(data?.generated_today)}
          hint={`累计 ${fmtNumber(data?.generated_total)}`}
          tone="text-klein-500"
        />
        <Stat label="今日图片" value={fmtNumber(data?.image_today)} hint={`累计 ${fmtNumber(data?.image_total)}`} />
        <Stat label="今日视频" value={fmtNumber(data?.video_today)} hint={`累计 ${fmtNumber(data?.video_total)}`} />
        <Stat label="今日 Token" value={compact(data?.text_tokens_today)} hint={`累计 ${compact(data?.text_tokens_total)}`} />
        <Stat
          label="成功率"
          value={percent(data?.success_rate_today)}
          hint={`今日活跃 ${fmtNumber(data?.active_users_today)}`}
          tone="text-success"
        />
        <Stat
          label="今日消耗"
          value={fmtPoints(data?.wallet_spend_today)}
          hint={`累计 ${fmtPoints(data?.wallet_spend_total)}`}
          tone="text-warn"
        />
      </StatRow>

      <StatRow cols={4}>
        <Stat
          label="用户总数"
          value={fmtNumber(data?.users_total)}
          hint={`今日新增 ${fmtNumber(data?.users_today)}`}
        />
        <Stat
          label="账号可用"
          value={`${fmtNumber(availableAccounts)} / ${fmtNumber(totalAccounts)}`}
          hint={availabilityHint(availableAccounts, totalAccounts)}
        />
        <Stat
          label="剩余额度"
          value={fmtNumber(quotaRemaining)}
          hint={quotaHint(quotaRemaining, quotaTotal)}
        />
        <Stat
          label="任务积分"
          value={fmtPoints(data?.cost_points_today)}
          hint={`累计 ${fmtPoints(data?.cost_points_total)}`}
        />
      </StatRow>

      <Section
        title={
          <SectionHeading
            icon={<BarChart3 size={14} />}
            title="近 7 天趋势"
            hint="生成量 / 消耗积分"
          />
        }
        right={<span className="text-tiny text-text-tertiary">每分钟自动刷新</span>}
      >
        <TrendChart points={data?.trend ?? []} />
      </Section>

      <div className="grid gap-3 xl:grid-cols-2">
        <Section
          title={<SectionHeading icon={<KeyRound size={14} />} title="账号池与额度" />}
          right={<span className="text-tiny text-text-tertiary">{providers.length} 个供应商</span>}
        >
          <div className="space-y-2">
            {providers.map((row) => (
              <ProviderPanel key={row.provider} row={row} />
            ))}
            {providers.length === 0 && (
              <div className="py-6 text-center text-small text-text-tertiary">暂无账号池数据</div>
            )}
          </div>
        </Section>

        <Section
          title={<SectionHeading icon={<Activity size={14} />} title="最近生成" />}
          right={<span className="text-tiny text-text-tertiary">最新 {recent.length} 条</span>}
          bodyClass="p-0"
        >
          <ul className="divide-y divide-border">
            {recent.map((row) => (
              <RecentTaskRow key={row.task_id} row={row} />
            ))}
            {recent.length === 0 && (
              <li className="py-6 text-center text-small text-text-tertiary">暂无生成记录</li>
            )}
          </ul>
        </Section>
      </div>
    </PageShell>
  );
}

function SectionHeading({ icon, title, hint }: { icon: ReactNode; title: string; hint?: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="grid h-5 w-5 place-items-center rounded bg-info-soft text-klein-500">{icon}</span>
      <span className="font-semibold text-text-primary">{title}</span>
      {hint && <span className="hidden text-tiny text-text-tertiary md:inline">— {hint}</span>}
    </div>
  );
}

function TrendChart({ points }: { points: DashboardTrendPoint[] }) {
  const rows =
    points.length > 0
      ? points
      : Array.from({ length: 7 }, (_, i) => ({ date: `D${i + 1}`, generated: 0, cost_points: 0 }));
  const totalGen = rows.reduce((s, p) => s + p.generated, 0);
  const totalCost = rows.reduce((s, p) => s + p.cost_points, 0);
  const isEmpty = totalGen === 0 && totalCost === 0;
  const maxGenerated = Math.max(1, ...rows.map((p) => p.generated));
  const maxCost = Math.max(1, ...rows.map((p) => p.cost_points));

  const containerRef = useRef<HTMLDivElement | null>(null);
  // 用容器实际宽度作为 SVG viewBox 的宽度，避免 preserveAspectRatio="none"
  // 拉伸文字和圆点（之前日期就是这么被拉变形的）。
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    if (!containerRef.current) return;
    setWidth(containerRef.current.clientWidth);
  }, []);
  useEffect(() => {
    if (!containerRef.current || typeof ResizeObserver === 'undefined') return;
    const el = containerRef.current;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setWidth(Math.floor(e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const height = 160;
  const padX = 36;
  const padY = 22;
  // SSR / 初次渲染前 width=0，用 1 占位以避免负数；真正绘制等容器测量后。
  const safeW = width > 0 ? width : 1;
  const innerW = Math.max(1, safeW - padX * 2);
  const innerH = height - padY * 2;
  const step = innerW / Math.max(1, rows.length - 1);
  const y = (v: number, max: number) => height - padY - (v / max) * innerH;
  const linePath = (key: 'generated' | 'cost_points', max: number) =>
    rows.map((p, i) => `${padX + i * step},${y(p[key], max)}`).join(' ');

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-3 text-tiny text-text-tertiary">
        <span className="inline-flex items-center gap-1">
          <span className="h-2 w-2 rounded-full bg-klein-500" />
          生成量
          <span className="ml-1 font-medium tabular-nums text-text-secondary">{fmtNumber(totalGen)}</span>
        </span>
        <span className="inline-flex items-center gap-1">
          <span className="h-2 w-2 rounded-full bg-warn" />
          消耗积分
          <span className="ml-1 font-medium tabular-nums text-text-secondary">{fmtPoints(totalCost)}</span>
        </span>
      </div>
      <div ref={containerRef} className="relative w-full">
        {width > 0 && (
          <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block">
            <defs>
              <linearGradient id="gen-fill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="rgb(99 102 241 / 0.18)" />
                <stop offset="100%" stopColor="rgb(99 102 241 / 0)" />
              </linearGradient>
            </defs>
            {[0, 0.25, 0.5, 0.75, 1].map((r) => (
              <line
                key={r}
                x1={padX}
                x2={width - padX}
                y1={padY + r * innerH}
                y2={padY + r * innerH}
                stroke="rgb(148 163 184 / 0.15)"
                strokeWidth="1"
                strokeDasharray={r === 0 || r === 1 ? '' : '3 4'}
              />
            ))}
            {!isEmpty && (
              <>
                <polygon
                  points={`${padX},${height - padY} ${linePath('generated', maxGenerated)} ${width - padX},${height - padY}`}
                  fill="url(#gen-fill)"
                />
                <polyline
                  points={linePath('generated', maxGenerated)}
                  fill="none"
                  stroke="rgb(99 102 241)"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
                <polyline
                  points={linePath('cost_points', maxCost)}
                  fill="none"
                  stroke="rgb(245 158 11)"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeDasharray="4 3"
                />
              </>
            )}
            {rows.map((p, i) => {
              const cx = padX + i * step;
              return (
                <g key={p.date}>
                  {!isEmpty && (
                    <circle cx={cx} cy={y(p.generated, maxGenerated)} r="3" fill="rgb(99 102 241)" />
                  )}
                  <text x={cx} y={height - 6} textAnchor="middle" fontSize="11" fill="rgb(148 163 184)">
                    {formatDay(p.date)}
                  </text>
                </g>
              );
            })}
          </svg>
        )}
        {isEmpty && width > 0 && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
            <span className="rounded-md bg-surface-2/70 px-3 py-1 text-tiny text-text-tertiary">
              近 7 天暂无生成记录
            </span>
          </div>
        )}
      </div>
    </div>
  );
}

// PROVIDER_LABEL 把后端 provider id 映射成对用户/运营更友好的字面。
// 「adobe」其实是 Adobe Firefly 池，但实际产能就是 Nano Banana 三件套 + GPT-Image-2 的 2K/4K
// 兜底，对运营来说叫 Banana 更直观，所以仪表盘统一展示「Banana」。后端 provider id
// 仍然是 adobe，下游路由 / DB / 日志一律不动。
const PROVIDER_LABEL: Record<string, string> = {
  gpt: 'GPT',
  grok: 'Grok',
  adobe: 'Banana',
  flowmusic: 'Music',
};

function providerLabel(p: string): string {
  return PROVIDER_LABEL[p] || p.toUpperCase();
}

function ProviderPanel({ row }: { row: DashboardProviderRow }) {
  const availableRatio = row.total > 0 ? row.available / row.total : 0;
  const quotaKnown = row.quota_remaining > 0 || row.quota_total > 0;
  const quotaRatio = row.quota_total > 0 ? row.quota_remaining / row.quota_total : quotaKnown ? 1 : 0;
  const tone =
    availableRatio === 0
      ? 'bg-danger-soft text-danger'
      : availableRatio < 0.5
        ? 'bg-warn-soft text-warn'
        : 'bg-success-soft text-success';

  return (
    <div className="rounded-md border border-border bg-surface-2/40 px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-small font-semibold uppercase text-text-primary">{providerLabel(row.provider)}</span>
            <span className={'inline-flex rounded px-1.5 py-0.5 text-tiny ' + tone}>
              {fmtNumber(row.available)} / {fmtNumber(row.total)}
            </span>
          </div>
          <div className="mt-0.5 truncate text-tiny text-text-tertiary">
            OK {fmtNumber(row.test_ok)} · 熔断 {fmtNumber(row.broken)} · 成功 {fmtNumber(row.success_count)} ·
            错误 {fmtNumber(row.error_count)}
          </div>
        </div>
      </div>
      <div className="mt-2 grid gap-2 md:grid-cols-2">
        <Bar
          label="账号可用率"
          value={availableRatio}
          text={`${Math.round(availableRatio * 100)}%`}
          color="bg-klein-500"
        />
        <Bar
          label="额度剩余"
          value={quotaRatio}
          text={providerQuotaText(row)}
          color={quotaRatio < 0.3 ? 'bg-warn' : 'bg-success'}
        />
      </div>
    </div>
  );
}

function Bar({ label, value, text, color }: { label: string; value: number; text: string; color: string }) {
  const w = Math.max(0, Math.min(100, value * 100));
  return (
    <div>
      <div className="mb-1 flex items-center justify-between text-tiny text-text-tertiary">
        <span>{label}</span>
        <span className="tabular-nums text-text-secondary">{text}</span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3">
        <div className={'h-full rounded-full transition-all ' + color} style={{ width: `${w}%` }} />
      </div>
    </div>
  );
}

function RecentTaskRow({ row }: { row: DashboardRecentTask }) {
  const Icon = row.kind === 'video' ? Video : row.kind === 'image' ? Image : Sparkles;
  return (
    <li className="flex items-center gap-3 px-3 py-2 hover:bg-surface-2/60">
      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-md bg-info-soft text-klein-500">
        <Icon size={14} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-small">
          <span className="truncate font-medium text-text-primary">{row.user_label}</span>
          <span className="rounded border border-border bg-surface-2 px-1.5 py-0.5 text-tiny text-text-tertiary">
            {row.kind === 'video' ? '视频' : row.kind === 'image' ? '图片' : '文字'} ×{row.count}
          </span>
          <StatusTag status={row.status} />
        </div>
        <div className="mt-0.5 truncate font-mono text-tiny text-text-tertiary">
          {row.model_code} · #{row.task_id} · {fmtTime(row.created_at)}
        </div>
      </div>
      <div className="shrink-0 text-right text-small font-semibold tabular-nums text-text-primary">
        {fmtPoints(row.cost_points)}
        <span className="ml-0.5 text-tiny font-normal text-text-tertiary">点</span>
      </div>
    </li>
  );
}

const STATUS_TAGS: Record<number, { label: string; tone: string }> = {
  0: { label: '排队', tone: 'bg-surface-3 text-text-tertiary' },
  1: { label: '运行', tone: 'bg-info-soft text-klein-500' },
  2: { label: '成功', tone: 'bg-success-soft text-success' },
  3: { label: '失败', tone: 'bg-danger-soft text-danger' },
  4: { label: '退款', tone: 'bg-warn-soft text-warn' },
};

function StatusTag({ status }: { status: number }) {
  const t = STATUS_TAGS[status] ?? STATUS_TAGS[0]!;
  return <span className={'inline-flex rounded px-1.5 py-0.5 text-tiny ' + t.tone}>{t.label}</span>;
}

function availabilityHint(available: number, total: number) {
  if (total === 0) return '暂无账号';
  const ratio = available / total;
  if (ratio === 0) return '所有账号不可用';
  if (ratio < 0.5) return `可用率 ${Math.round(ratio * 100)}%（偏低）`;
  return `可用率 ${Math.round(ratio * 100)}%`;
}

function quotaHint(remaining: number, total: number) {
  if (remaining <= 0 && total <= 0) return '等待探测额度';
  if (total > remaining) return `已用 ${fmtNumber(Math.max(0, total - remaining))} / ${fmtNumber(total)}`;
  return '已统计各号池可见余额';
}

function providerQuotaText(row: DashboardProviderRow) {
  if (row.quota_remaining <= 0 && row.quota_total <= 0) return '未探测';
  if (row.provider === 'grok' && row.quota_total > 0) {
    return `${fmtNumber(row.quota_remaining)} / ${fmtNumber(row.quota_total)}`;
  }
  if (row.quota_total > row.quota_remaining) {
    return `${fmtNumber(row.quota_remaining)} / ${fmtNumber(row.quota_total)}`;
  }
  return `剩余 ${fmtNumber(row.quota_remaining)}`;
}

function percent(v?: number) {
  if (v == null) return '—';
  return `${Math.round(v * 100)}%`;
}

function compact(v?: number | null) {
  const n = Number(v || 0);
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 10_000) return `${(n / 1000).toFixed(0)}K`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return fmtNumber(n);
}

function formatDay(v: string) {
  if (!v.includes('-')) return v;
  return v.slice(5).replace('-', '/');
}
