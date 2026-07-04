import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  AlertCircle,
  CheckCircle2,
  ChevronLeft,
  Download,
  PauseCircle,
  Plus,
  PlusCircle,
  RefreshCw,
  Search,
  Ticket,
  XCircle,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { cdkApi } from '../../lib/services';
import type {
  AdminCDKBatchItem,
  AdminCDKCodeItem,
  CDKCreateBatchBody,
  CDKCreateBatchResp,
} from '../../lib/types';
import { fmtNumber, fmtPoints, fmtTime } from '../../lib/format';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section, Stat, StatRow } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

type Tab = 'list' | 'create';

export default function CDKPage() {
  const [tab, setTab] = useState<Tab>('list');
  const [detail, setDetail] = useState<AdminCDKBatchItem | null>(null);

  return (
    <PageShell>
      <PageHeader
        icon={<Ticket size={16} />}
        title="兑换码"
        right={
          <div className="inline-flex rounded-md border border-border bg-surface-2 p-0.5">
            <TabBtn active={tab === 'list'} onClick={() => { setTab('list'); setDetail(null); }}>
              批次列表
            </TabBtn>
            <TabBtn active={tab === 'create'} onClick={() => { setTab('create'); setDetail(null); }}>
              批量生成
            </TabBtn>
          </div>
        }
      />

      {tab === 'create' && (
        <CreatePanel
          onSuccess={() => {
            // 生成后自动跳到列表标签
            setTab('list');
          }}
        />
      )}

      {tab === 'list' && !detail && (
        <BatchListPanel onPick={(b) => setDetail(b)} />
      )}

      {tab === 'list' && detail && (
        <BatchDetailPanel batch={detail} onBack={() => setDetail(null)} />
      )}
    </PageShell>
  );
}

// ============================================================
// 标签按钮
// ============================================================

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        'rounded px-3 py-1 text-small transition-colors ' +
        (active
          ? 'bg-surface-1 font-semibold text-text-primary shadow-sm'
          : 'text-text-tertiary hover:text-text-secondary')
      }
    >
      {children}
    </button>
  );
}

// ============================================================
// 批次列表面板
// ============================================================

function BatchListPanel({ onPick }: { onPick: (b: AdminCDKBatchItem) => void }) {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | '0' | '1'>('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useQuery({
    queryKey: ['admin', 'cdk', 'batches', keyword, status, page, pageSize],
    queryFn: () =>
      cdkApi.listBatches({
        keyword: keyword.trim() || undefined,
        status: status === '' ? '' : (Number(status) as 0 | 1),
        page,
        page_size: pageSize,
      }),
  });

  const rows = query.data?.list ?? [];
  const total = query.data?.total ?? 0;

  const totals = useMemo(() => {
    return {
      batches: total,
      totalQty: rows.reduce((s, r) => s + r.total_qty, 0),
      usedQty: rows.reduce((s, r) => s + r.used_qty, 0),
      remaining: rows.reduce((s, r) => s + r.remaining_qty, 0),
    };
  }, [rows, total]);

  const toggle = useMutation({
    mutationFn: (row: AdminCDKBatchItem) => cdkApi.toggleBatch(row.id, row.status === 1 ? 0 : 1),
    onSuccess: () => {
      toast.success('批次状态已更新');
      qc.invalidateQueries({ queryKey: ['admin', 'cdk'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  return (
    <>
      <StatRow cols={4}>
        <Stat label="批次总数（当前筛选）" value={totals.batches} />
        <Stat label="本页累计码数" value={fmtNumber(totals.totalQty)} />
        <Stat label="本页已用" value={fmtNumber(totals.usedQty)} tone="text-warn" />
        <Stat label="本页剩余" value={fmtNumber(totals.remaining)} tone="text-success" />
      </StatRow>

      <Section
        title="批次列表"
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
            <div className="relative w-60">
              <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
              <input
                className="input input-sm pl-7"
                value={keyword}
                onChange={(e) => {
                  setKeyword(e.target.value);
                  setPage(1);
                }}
                placeholder="按批次号 / 名称 / ID 搜索"
              />
            </div>
            <button
              className="btn btn-outline btn-sm"
              onClick={() => query.refetch()}
              disabled={query.isFetching}
            >
              <RefreshCw size={14} className={query.isFetching ? 'animate-spin' : ''} /> 刷新
            </button>
          </>
        }
        bodyClass="p-0"
      >
        <div className="overflow-x-auto">
          <table className="min-w-full text-small">
            <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
              <tr>
                <th className="px-3 py-2 text-left">批次</th>
                <th className="px-3 py-2 text-left">单码点数</th>
                <th className="px-3 py-2 text-left">数量</th>
                <th className="px-3 py-2 text-left">已用 / 已吊销 / 剩余</th>
                <th className="px-3 py-2 text-left">每用户</th>
                <th className="px-3 py-2 text-left">过期</th>
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-left">创建时间</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id} className="border-b border-border last:border-0 hover:bg-surface-2/60">
                  <td className="px-3 py-1.5">
                    <button
                      className="text-left"
                      type="button"
                      onClick={() => onPick(row)}
                      title="查看单码"
                    >
                      <div className="font-mono font-semibold text-klein-500 hover:underline">{row.batch_no}</div>
                      <div className="text-tiny text-text-tertiary">{row.name}</div>
                    </button>
                  </td>
                  <td className="px-3 py-1.5 font-semibold tabular-nums text-text-primary">
                    {fmtPoints(row.reward_points)}
                  </td>
                  <td className="px-3 py-1.5 tabular-nums text-text-secondary">{fmtNumber(row.total_qty)}</td>
                  <td className="px-3 py-1.5 tabular-nums">
                    <span className="text-warn">{fmtNumber(row.used_qty)}</span>
                    <span className="text-text-tertiary"> / </span>
                    <span className="text-text-tertiary">{fmtNumber(row.revoked_qty)}</span>
                    <span className="text-text-tertiary"> / </span>
                    <span className="text-success">{fmtNumber(row.remaining_qty)}</span>
                  </td>
                  <td className="px-3 py-1.5 tabular-nums text-text-secondary">
                    {row.per_user_limit > 0 ? `${row.per_user_limit} 次` : '不限'}
                  </td>
                  <td className="whitespace-nowrap px-3 py-1.5 text-tiny text-text-tertiary">
                    {row.expire_at > 0 ? fmtTime(row.expire_at) : '永久'}
                  </td>
                  <td className="px-3 py-1.5">
                    <button
                      className={
                        'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                        (row.status === 1
                          ? 'bg-success-soft text-success'
                          : 'bg-surface-2 text-text-tertiary')
                      }
                      onClick={async () => {
                        if (row.status === 1) {
                          const ok = await confirm({
                            title: '停用批次',
                            description: (
                              <>
                                停用后批次 <span className="font-mono">{row.batch_no}</span> 内未使用的码将无法被兑换。
                                已使用记录保留。
                              </>
                            ),
                            tone: 'warning',
                          });
                          if (!ok) return;
                        }
                        toggle.mutate(row);
                      }}
                    >
                      {row.status === 1 ? (
                        <>启用</>
                      ) : (
                        <><PauseCircle size={12} /> 停用</>
                      )}
                    </button>
                  </td>
                  <td className="whitespace-nowrap px-3 py-1.5 text-tiny text-text-tertiary">
                    {fmtTime(row.created_at)}
                  </td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex justify-end gap-1">
                      <button
                        className="btn btn-ghost btn-xs"
                        onClick={() => onPick(row)}
                        title="查看单码"
                      >
                        详情
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-xs"
                        title="导出 CSV"
                        onClick={async () => {
                          try {
                            await cdkApi.exportBatch(row.id, row.batch_no);
                            toast.success('已开始下载 CSV');
                          } catch (e) {
                            toast.error(e instanceof Error ? e.message : '下载失败');
                          }
                        }}
                      >
                        <Download size={13} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {!query.isLoading && rows.length === 0 && (
                <tr>
                  <td colSpan={9} className="py-10 text-center text-text-tertiary">
                    暂无批次
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
      {confirmDialog}
    </>
  );
}

// ============================================================
// 批次详情（单码列表 + 追加 / 吊销）
// ============================================================

function BatchDetailPanel({ batch: initialBatch, onBack }: { batch: AdminCDKBatchItem; onBack: () => void }) {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | '0' | '1' | '2'>('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [appendQty, setAppendQty] = useState<number>(100);
  useEffect(() => setPage(1), [pageSize, status]);

  // 详情查询：实时刷新出 batch 的 used / revoked / remaining
  const batchQ = useQuery({
    queryKey: ['admin', 'cdk', 'batch', initialBatch.id],
    queryFn: () => cdkApi.getBatch(initialBatch.id),
    initialData: initialBatch,
  });
  const batch = batchQ.data ?? initialBatch;

  const codesQ = useQuery({
    queryKey: ['admin', 'cdk', 'codes', batch.id, status, keyword, page, pageSize],
    queryFn: () =>
      cdkApi.listCodes(batch.id, {
        status: status === '' ? '' : (Number(status) as 0 | 1 | 2),
        keyword: keyword.trim() || undefined,
        page,
        page_size: pageSize,
      }),
  });

  const codes = codesQ.data?.list ?? [];
  const totalCodes = codesQ.data?.total ?? 0;

  const append = useMutation({
    mutationFn: (qty: number) => cdkApi.appendBatch(batch.id, qty),
    onSuccess: (resp) => {
      toast.success(`已追加生成 ${resp.appended} 张（批次总量 ${resp.total_qty}）`);
      qc.invalidateQueries({ queryKey: ['admin', 'cdk'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  const revoke = useMutation({
    mutationFn: (id: number) => cdkApi.revokeCode(id),
    onSuccess: () => {
      toast.success('单码已吊销');
      qc.invalidateQueries({ queryKey: ['admin', 'cdk'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  return (
    <>
      <Section
        title={
          <div className="flex items-center gap-2">
            <button className="btn btn-ghost btn-xs" onClick={onBack}>
              <ChevronLeft size={14} /> 返回列表
            </button>
            <span className="text-text-secondary">/</span>
            <span className="font-mono text-text-primary">{batch.batch_no}</span>
            <span className="text-text-tertiary">·</span>
            <span>{batch.name}</span>
          </div>
        }
        right={
          <button
            className="btn btn-outline btn-sm"
            onClick={async () => {
              try {
                await cdkApi.exportBatch(batch.id, batch.batch_no);
                toast.success('已开始下载 CSV');
              } catch (e) {
                toast.error(e instanceof Error ? e.message : '下载失败');
              }
            }}
          >
            <Download size={14} /> 导出 CSV
          </button>
        }
      >
        <StatRow cols={4}>
          <Stat label="单码点数" value={fmtPoints(batch.reward_points) + ' 点'} />
          <Stat label="总数量" value={fmtNumber(batch.total_qty)} />
          <Stat label="已使用 / 已吊销" value={`${fmtNumber(batch.used_qty)} / ${fmtNumber(batch.revoked_qty)}`} tone="text-warn" />
          <Stat label="剩余可用" value={fmtNumber(batch.remaining_qty)} tone="text-success" />
        </StatRow>

        <div className="mt-3 flex flex-wrap items-center gap-2 rounded-md border border-info-soft bg-klein-gradient-soft px-3 py-2">
          <PlusCircle size={14} className="text-klein-500" />
          <span className="text-small text-text-secondary">追加生成</span>
          <input
            type="number"
            min={1}
            max={100000}
            className="input input-sm w-24 tabular-nums"
            value={appendQty}
            onChange={(e) => setAppendQty(Math.max(1, Number(e.target.value) || 0))}
          />
          <span className="text-small text-text-secondary">张到此批次</span>
          <button
            className="btn btn-primary btn-sm"
            disabled={append.isPending || appendQty <= 0}
            onClick={async () => {
              const ok = await confirm({
                title: '追加生成单码',
                description: (
                  <>
                    将向批次 <span className="font-mono text-text-primary">{batch.batch_no}</span> 追加
                    <strong className="mx-1 text-text-primary">{fmtNumber(appendQty)}</strong>
                    张未使用的兑换码。所有新码使用同样的点数 / 限领次数 / 过期时间。
                  </>
                ),
                confirmLabel: '追加',
              });
              if (!ok) return;
              append.mutate(appendQty);
            }}
          >
            {append.isPending ? '追加中…' : '追加'}
          </button>
        </div>
      </Section>

      <Section
        title="单码列表"
        right={
          <>
            <select
              className="select select-sm"
              value={status}
              onChange={(e) => setStatus(e.target.value as typeof status)}
            >
              <option value="">全部状态</option>
              <option value="0">未使用</option>
              <option value="1">已使用</option>
              <option value="2">已吊销</option>
            </select>
            <div className="relative w-56">
              <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
              <input
                className="input input-sm pl-7 font-mono"
                value={keyword}
                onChange={(e) => {
                  setKeyword(e.target.value.toUpperCase());
                  setPage(1);
                }}
                placeholder="码片段（含大小写）"
              />
            </div>
            <button
              className="btn btn-outline btn-sm"
              onClick={() => codesQ.refetch()}
              disabled={codesQ.isFetching}
            >
              <RefreshCw size={14} className={codesQ.isFetching ? 'animate-spin' : ''} />
            </button>
          </>
        }
        bodyClass="p-0"
      >
        <div className="overflow-x-auto">
          <table className="min-w-full text-small">
            <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
              <tr>
                <th className="px-3 py-2 text-left">兑换码</th>
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-left">使用者</th>
                <th className="px-3 py-2 text-left">使用时间</th>
                <th className="px-3 py-2 text-left">创建时间</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {codes.map((c) => (
                <CodeRow key={c.id} code={c} onRevoke={async () => {
                  const ok = await confirm({
                    title: '吊销兑换码',
                    description: (
                      <>
                        确认吊销 <span className="font-mono text-text-primary">{c.code}</span>？吊销后该码无法被任何用户兑换。
                        已使用的码不能吊销。
                      </>
                    ),
                    tone: 'danger',
                    confirmLabel: '吊销',
                  });
                  if (!ok) return;
                  revoke.mutate(c.id);
                }} />
              ))}
              {!codesQ.isLoading && codes.length === 0 && (
                <tr>
                  <td colSpan={6} className="py-10 text-center text-text-tertiary">
                    没有符合条件的兑换码
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <Pager
          total={totalCodes}
          page={page}
          pageSize={pageSize}
          onChange={setPage}
          onPageSizeChange={setPageSize}
          sizeOptions={sizeOptions}
        />
      </Section>
      {confirmDialog}
    </>
  );
}

function CodeRow({ code, onRevoke }: { code: AdminCDKCodeItem; onRevoke: () => void }) {
  return (
    <tr className="border-b border-border last:border-0 hover:bg-surface-2/60">
      <td className="px-3 py-1.5 font-mono font-semibold text-text-primary">{code.code}</td>
      <td className="px-3 py-1.5">{statusChip(code.status)}</td>
      <td className="px-3 py-1.5 tabular-nums text-text-secondary">
        {code.used_by ? `用户 #${code.used_by}` : '—'}
      </td>
      <td className="whitespace-nowrap px-3 py-1.5 text-tiny text-text-tertiary">
        {code.used_at ? fmtTime(code.used_at) : '—'}
      </td>
      <td className="whitespace-nowrap px-3 py-1.5 text-tiny text-text-tertiary">
        {fmtTime(code.created_at)}
      </td>
      <td className="px-3 py-1.5 text-right">
        {code.status === 0 ? (
          <button className="btn btn-ghost btn-xs text-danger" onClick={onRevoke}>
            <XCircle size={13} /> 吊销
          </button>
        ) : (
          <span className="text-tiny text-text-tertiary">—</span>
        )}
      </td>
    </tr>
  );
}

function statusChip(v: number) {
  if (v === 1) return <span className="inline-flex items-center rounded bg-warn-soft px-1.5 py-0.5 text-tiny text-warn">已使用</span>;
  if (v === 2) return <span className="inline-flex items-center rounded bg-danger-soft px-1.5 py-0.5 text-tiny text-danger">已吊销</span>;
  return <span className="inline-flex items-center rounded bg-success-soft px-1.5 py-0.5 text-tiny text-success">未使用</span>;
}

// ============================================================
// 批量生成面板（保留原有 UI）
// ============================================================

function CreatePanel({ onSuccess }: { onSuccess: () => void }) {
  const qc = useQueryClient();
  const [body, setBody] = useState<CDKCreateBatchBody>({
    batch_no: '',
    name: '',
    points: 1000,
    qty: 100,
    per_user_limit: 1,
    expire_at: 0,
  });
  const [last, setLast] = useState<CDKCreateBatchResp | null>(null);

  const m = useMutation({
    mutationFn: (b: CDKCreateBatchBody) => cdkApi.createBatch(b),
    onSuccess: (r) => {
      toast.success(`已生成批次 ${r.batch_no}（共 ${r.total_qty} 张）`);
      setLast(r);
      qc.invalidateQueries({ queryKey: ['admin', 'cdk'] });
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!body.batch_no.trim() || !body.name.trim()) {
      toast.error('请填写批次号和名称');
      return;
    }
    if (body.points <= 0 || body.qty <= 0) {
      toast.error('点数和数量必须 > 0');
      return;
    }
    m.mutate({
      ...body,
      batch_no: body.batch_no.trim(),
      name: body.name.trim(),
      per_user_limit: body.per_user_limit || 0,
      expire_at: body.expire_at || undefined,
    });
  };

  return (
    <>
      <Section title="批量生成">
        <form onSubmit={submit} className="grid w-full gap-3 lg:grid-cols-2">
          <Field label="批次号" hint="同批次唯一，如 SPRING2026-A">
            <input
              className="input input-sm font-mono"
              value={body.batch_no}
              onChange={(e) => setBody((s) => ({ ...s, batch_no: e.target.value }))}
              placeholder="SPRING2026-A"
            />
          </Field>

          <Field label="批次名称" hint="展示给运营 / 客服的友好名称">
            <input
              className="input input-sm"
              value={body.name}
              onChange={(e) => setBody((s) => ({ ...s, name: e.target.value }))}
              placeholder="春节活动 100 点"
            />
          </Field>

          <Field
            label="单码点数（×100 储存）"
            hint={`输入 1000 = 实际 10.00 点；当前等价：${fmtPoints(body.points)} 点`}
          >
            <input
              type="number"
              min={1}
              className="input input-sm tabular-nums"
              value={body.points}
              onChange={(e) =>
                setBody((s) => ({ ...s, points: Math.max(1, Number(e.target.value) || 0) }))
              }
            />
          </Field>

          <Field label="生成数量" hint="单批次最多 100,000 张">
            <input
              type="number"
              min={1}
              max={100_000}
              className="input input-sm tabular-nums"
              value={body.qty}
              onChange={(e) =>
                setBody((s) => ({ ...s, qty: Math.max(1, Number(e.target.value) || 0) }))
              }
            />
          </Field>

          <Field label="每用户限领次数" hint="0 表示不限制；建议 1（防羊毛）">
            <input
              type="number"
              min={0}
              className="input input-sm tabular-nums"
              value={body.per_user_limit ?? 0}
              onChange={(e) => setBody((s) => ({ ...s, per_user_limit: Number(e.target.value) || 0 }))}
            />
          </Field>

          <Field label="过期时间（可选）" hint="留空表示永久有效">
            <input
              type="datetime-local"
              className="input input-sm"
              onChange={(e) => {
                const v = e.target.value;
                if (!v) {
                  setBody((s) => ({ ...s, expire_at: 0 }));
                  return;
                }
                const t = Math.floor(new Date(v).getTime() / 1000);
                setBody((s) => ({ ...s, expire_at: t }));
              }}
            />
          </Field>

          <div className="flex flex-col items-stretch justify-between gap-3 rounded-md border border-info-soft bg-klein-gradient-soft px-3 py-2.5 md:flex-row md:items-center lg:col-span-2">
            <div className="flex flex-wrap items-center gap-1 text-small text-text-secondary">
              <AlertCircle size={14} className="text-klein-500" />
              <span>预计生成</span>
              <strong className="text-text-primary">{fmtNumber(body.qty)}</strong>
              <span>张，单码</span>
              <strong className="text-text-primary">{fmtPoints(body.points)} 点</strong>
              <span>，合计</span>
              <strong className="text-klein-500">{fmtPoints(body.points * body.qty)} 点</strong>
            </div>
            <button type="submit" className="btn btn-primary btn-sm md:shrink-0" disabled={m.isPending}>
              {m.isPending ? (
                '生成中…'
              ) : (
                <>
                  <Plus size={14} /> 生成批次
                </>
              )}
            </button>
          </div>
        </form>
      </Section>

      {last && (
        <Section title="最新生成结果">
          <div className="flex items-start gap-3 rounded-md border border-success-soft bg-success-soft/40 px-3 py-2.5">
            <CheckCircle2 className="mt-0.5 shrink-0 text-success" size={18} />
            <div className="min-w-0 space-y-0.5">
              <p className="text-small font-medium text-text-primary">批次已生成</p>
              <p className="text-tiny text-text-tertiary">
                ID #{last.id} · 批次号
                <code className="kbd mx-1">{last.batch_no}</code>
                · 共 {fmtNumber(last.total_qty)} 张
              </p>
            </div>
          </div>
        </Section>
      )}
    </>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}
