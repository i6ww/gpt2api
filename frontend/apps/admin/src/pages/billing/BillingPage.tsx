import { useQuery } from '@tanstack/react-query';
import { RefreshCw, Search, Wallet } from 'lucide-react';
import { useEffect, useState } from 'react';

import { billingApi } from '../../lib/services';
import type { AdminWalletLogItem } from '../../lib/types';
import { fmtNumber, fmtPoints, fmtTime } from '../../lib/format';
import {
  PageHeader,
  PageShell,
  Pager,
  Section,
  Stat as StatCard,
  StatRow,
  Toolbar,
  ToolbarSpacer,
} from '../../components/layout/PageShell';
import { usePageSize } from '../../stores/uiPrefs';

const BIZ_OPTIONS = [
  { value: '', label: '全部业务' },
  { value: 'recharge', label: '充值' },
  { value: 'consume', label: '消费' },
  { value: 'refund', label: '退款' },
  { value: 'cdk', label: '兑换码' },
  { value: 'promo', label: '优惠码' },
  { value: 'invite_reward', label: '邀请奖励' },
  { value: 'gift', label: '赠送' },
];

export default function BillingPage() {
  const [keyword, setKeyword] = useState('');
  const [userID, setUserID] = useState('');
  const [bizType, setBizType] = useState('');
  const [direction, setDirection] = useState<'' | '1' | '-1'>('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useQuery({
    queryKey: ['admin', 'billing', 'wallet-logs', keyword, userID, bizType, direction, page],
    queryFn: () => billingApi.walletLogs({
      keyword: keyword.trim() || undefined,
      user_id: Number(userID) || undefined,
      biz_type: bizType || undefined,
      // direction 必须是 undefined 而不是 ''，否则会序列化成 ?direction= 让
      // 后端 *int + oneof 校验 400，整张表变空（共 0 条）。
      direction: direction ? (Number(direction) as 1 | -1) : undefined,
      page,
      page_size: pageSize,
    }),
  });
  // 顶部 stat 卡片走单独的 summary 接口，不受筛选/分页影响——之前是只在当前页 rows
  // 上累加，导致管理员一眼以为「就只赚了这几条的钱」。
  const summaryQ = useQuery({
    queryKey: ['admin', 'billing', 'wallet-logs', 'summary'],
    queryFn: () => billingApi.walletSummary(),
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
  });

  const rows = query.data?.list ?? [];
  const total = query.data?.total ?? 0;
  const summary = summaryQ.data;

  const refreshAll = () => { query.refetch(); summaryQ.refetch(); };

  return (
    <PageShell>
      <PageHeader
        icon={<Wallet size={16} />}
        title="充值消费记录"
        right={
          <button className="btn btn-outline btn-sm" onClick={refreshAll} disabled={query.isFetching || summaryQ.isFetching}>
            <RefreshCw size={14} className={(query.isFetching || summaryQ.isFetching) ? 'animate-spin' : ''} /> 刷新
          </button>
        }
      />

      <StatRow cols={4}>
        <StatCard
          label="今日充值"
          value={fmtPoints(summary?.recharge_today ?? 0)}
          hint={`累计 ${fmtPoints(summary?.recharge_total ?? 0)}`}
          tone="text-success"
        />
        <StatCard
          label="今日消费"
          value={fmtPoints(summary?.consume_today ?? 0)}
          hint={`累计 ${fmtPoints(summary?.consume_total ?? 0)}`}
          tone="text-danger"
        />
        <StatCard
          label="今日退款"
          value={fmtPoints(summary?.refund_today ?? 0)}
          hint={`累计 ${fmtPoints(summary?.refund_total ?? 0)}`}
          tone="text-warn"
        />
        <StatCard
          label="今日净流入"
          value={fmtPoints(summary?.net_today ?? 0)}
          hint={`累计 ${fmtPoints(summary?.net_total ?? 0)} · 流水 ${fmtNumber(summary?.records_total ?? 0)}`}
        />
      </StatRow>

      <Toolbar>
        <div className="relative min-w-[260px] flex-1">
          <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
          <input
            className="input input-sm pl-7"
            value={keyword}
            onChange={(e) => { setKeyword(e.target.value); setPage(1); }}
            placeholder="搜索流水 ID / 用户 / 业务 ID / 备注"
          />
        </div>
        <input
          className="input input-sm w-28"
          value={userID}
          onChange={(e) => { setUserID(e.target.value); setPage(1); }}
          placeholder="用户ID"
        />
        <select
          className="select select-sm"
          value={bizType}
          onChange={(e) => { setBizType(e.target.value); setPage(1); }}
        >
          {BIZ_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
        <select
          className="select select-sm"
          value={direction}
          onChange={(e) => { setDirection(e.target.value as typeof direction); setPage(1); }}
        >
          <option value="">全部方向</option>
          <option value="1">收入</option>
          <option value="-1">支出</option>
        </select>
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary whitespace-nowrap">共 {fmtNumber(total)} 条</span>
      </Toolbar>

      <div className="space-y-3 md:hidden">
        {query.isLoading && (
          <div className="rounded-xl border border-border bg-surface-1 px-3 py-10 text-center text-text-tertiary">加载中...</div>
        )}
        {!query.isLoading && rows.length === 0 && (
          <div className="rounded-xl border border-border bg-surface-1 px-3 py-10 text-center text-text-tertiary">暂无记录</div>
        )}
        {rows.map((row) => {
          const isIncome = row.direction > 0;
          return (
            <div key={row.id} className="rounded-xl border border-border bg-surface-1 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate font-medium text-text-primary">{row.user_label || `用户 ${row.user_id}`}</div>
                  <div className="mt-1 text-tiny text-text-tertiary">{fmtTime(row.created_at)} · {bizLabel(row.biz_type)}</div>
                </div>
                <span className={isIncome ? 'badge badge-success' : 'badge badge-danger'}>
                  {isIncome ? '收入' : '支出'}
                </span>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 text-tiny">
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">变动</div>
                  <div className={isIncome ? 'text-success font-semibold' : 'text-danger font-semibold'}>
                    {isIncome ? '+' : '-'}{fmtPoints(Math.abs(row.points))}
                  </div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">前后</div>
                  <div className="text-text-secondary">{fmtPoints(row.points_before)} → {fmtPoints(row.points_after)}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5 col-span-2">
                  <div className="text-text-tertiary">备注</div>
                  <div className="truncate text-text-secondary">{row.remark || '-'}</div>
                </div>
              </div>
            </div>
          );
        })}
      </div>

      <Section bodyClass="p-0">
      <div className="hidden table-wrap md:block">
        <table className="data-table min-w-[1120px]">
          <thead>
            <tr>
              <th>时间</th>
              <th>用户</th>
              <th>业务</th>
              <th>业务ID</th>
              <th>方向</th>
              <th>变动积分</th>
              <th>变动前</th>
              <th>变动后</th>
              <th>备注</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => <LogRow key={row.id} row={row} />)}
            {!query.isLoading && rows.length === 0 && (
              <tr><td colSpan={9} className="py-10 text-center text-text-tertiary">暂无记录</td></tr>
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
    </PageShell>
  );
}

function LogRow({ row }: { row: AdminWalletLogItem }) {
  const isIncome = row.direction > 0;
  return (
    <tr>
      <td className="whitespace-nowrap">{fmtTime(row.created_at)}</td>
      <td>
        <div className="font-medium text-text-primary">{row.user_label || `用户 ${row.user_id}`}</div>
        <div className="text-tiny text-text-tertiary">ID {row.user_id}</div>
      </td>
      <td><span className="badge badge-outline">{bizLabel(row.biz_type)}</span></td>
      <td className="font-mono text-small max-w-[220px] truncate" title={row.biz_id}>{row.biz_id}</td>
      <td><span className={isIncome ? 'badge badge-success' : 'badge badge-danger'}>{isIncome ? '收入' : '支出'}</span></td>
      <td className={isIncome ? 'text-success font-semibold tabular-nums' : 'text-danger font-semibold tabular-nums'}>
        {isIncome ? '+' : '-'}{fmtPoints(Math.abs(row.points))}
      </td>
      <td className="tabular-nums">{fmtPoints(row.points_before)}</td>
      <td className="tabular-nums">{fmtPoints(row.points_after)}</td>
      <td className="max-w-[240px] truncate" title={row.remark}>{row.remark || '-'}</td>
    </tr>
  );
}

function bizLabel(v: string) {
  return BIZ_OPTIONS.find((o) => o.value === v)?.label || v || '-';
}
