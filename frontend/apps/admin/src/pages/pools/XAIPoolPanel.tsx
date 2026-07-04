import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { type ChangeEvent, useEffect, useMemo, useState } from 'react';
import { Inbox, Pencil, RefreshCw, Trash2, Upload } from 'lucide-react';

import { ApiError } from '../../lib/api';
import { poolXaiApi } from '../../lib/services';
import type { XaiPoolItem, XaiPoolStatus, XaiPoolUpdateBody } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, SplitMenu, fmtMs } from './_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

type StatusFilter = '' | XaiPoolStatus;

const STATUS_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'valid', label: '可用' },
  { value: 'invalid', label: '失效' },
  { value: 'cooldown', label: '冷却' },
  { value: 'disabled', label: '禁用' },
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

function expiryRelative(expiresAt?: number): string {
  if (!expiresAt) return '永不/未知';
  const diffMs = expiresAt - Date.now();
  if (diffMs <= 0) return '已过期';
  const mins = diffMs / 60_000;
  if (mins < 60) return `${Math.round(mins)} 分钟后`;
  const hours = mins / 60;
  if (hours < 24) return `${hours.toFixed(1)} 小时后`;
  return `${(hours / 24).toFixed(1)} 天后`;
}

export default function XAIPoolPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<StatusFilter>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [editing, setEditing] = useState<XaiPoolItem | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, page, status, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'pool-xai', 'list', query],
    queryFn: () => poolXaiApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'pool-xai', 'stats'],
    queryFn: () => poolXaiApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'pool-xai'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => poolXaiApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('账号已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => poolXaiApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const refreshOne = useMutation({
    mutationFn: (id: number) => poolXaiApi.refresh(id),
    onSuccess: (r) => {
      refresh();
      const exp = r.expires_at ? `，新有效期 ${fmtMs(r.expires_at)}` : '';
      toast.success(`已刷新 #${r.id}：${r.status}${exp}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '刷新失败'),
  });
  const batchRefresh = useMutation({
    mutationFn: (scope: 'all' | 'expiring' | 'abnormal') => poolXaiApi.batchRefresh(scope),
    onSuccess: (r) => {
      refresh();
      toast.success(`批量续期完成：成功 ${r.ok}，失败 ${r.fail}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量续期失败'),
  });
  const refreshBilling = useMutation({
    mutationFn: (id: number) => poolXaiApi.refreshBilling(id),
    onSuccess: (r) => {
      refresh();
      toast.success(`额度已更新：剩余 $${(r.remaining_usd ?? 0).toFixed(2)} / $${(r.limit_usd ?? 0).toFixed(2)}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '查额度失败'),
  });
  const refreshBillingAll = useMutation({
    mutationFn: () => poolXaiApi.refreshBillingAll(),
    onSuccess: (r) => {
      refresh();
      toast.success(`额度刷新完成：成功 ${r.ok}，失败 ${r.fail}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量查额度失败'),
  });
  const updateOne = useMutation({
    mutationFn: ({ id, body }: { id: number; body: XaiPoolUpdateBody }) => poolXaiApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message || '保存失败'),
  });

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
      <div className="rounded-md border border-border bg-surface-1 px-3 py-2 text-tiny text-text-tertiary">
        官方 xAI API（OAuth 账号）。对外只暴露 <code className="kbd mx-1">xai/grok-imagine-video</code> 一个模型：
        不传图走文生视频，<strong>传图自动切到 grok-imagine-video-1.5 图生视频</strong>，同价不分档；
        支持 6 / 10 / 15 秒。上游限速 <strong>1 RPS/账号</strong>（随消费自动升档，档位见“档位/类型”列），
        单号并发 3，多账号线性扩容。Token 由后台调度器在过期前自动续期。
        额度通过 <code className="kbd mx-1">cli-chat-proxy.grok.com/v1/billing</code> 自动查询（用 access_token，无需 Management Key），点“刷新额度”更新。
      </div>

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
          placeholder="搜索 email / subject"
          onChange={(e) => {
            setKeyword(e.target.value);
            setPage(1);
          }}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as StatusFilter);
            setPage(1);
          }}
        >
          {STATUS_OPTIONS.map((o) => (
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
          label="续期"
          icon={<RefreshCw size={14} />}
          busy={batchRefresh.isPending}
          items={[
            {
              label: '续期即将过期（< 15min）',
              description: '与后台调度器一致的续期策略',
              onClick: () => batchRefresh.mutate('expiring'),
            },
            {
              label: '续期异常账号',
              description: 'invalid / cooldown 的账号重新续期',
              onClick: () => batchRefresh.mutate('abnormal'),
            },
            {
              label: '续期全部账号',
              description: '所有账号一起换 token（耗时）',
              onClick: () => batchRefresh.mutate('all'),
            },
          ]}
        />
        <button
          className="btn btn-outline btn-sm"
          disabled={refreshBillingAll.isPending}
          title="查询所有可用账号的额度"
          onClick={() => refreshBillingAll.mutate()}
        >
          <RefreshCw size={14} /> 刷新额度
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} /> 导入
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={selected.size === 0 || batchDelete.isPending}
          onClick={async () => {
            if (selected.size === 0) return;
            const ok = await confirm({
              title: '批量删除 xAI 账号',
              description: `将永久删除选中的 ${selected.size} 条官方 GROK 账号（含凭证）。`,
              tone: 'danger',
              confirmLabel: '删除',
            });
            if (ok) batchDelete.mutate(Array.from(selected));
          }}
        >
          <Trash2 size={14} /> 删除{selected.size ? ` (${selected.size})` : ''}
        </button>
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
                <th className="px-3 py-2 text-left">档位/类型</th>
                <th className="px-3 py-2 text-left">额度(US$)</th>
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-left">凭证</th>
                <th className="px-3 py-2 text-left">自动续期</th>
                <th className="px-3 py-2 text-left">Token 失效</th>
                <th className="px-3 py-2 text-left">最近续期</th>
                <th className="px-3 py-2 text-left">用量</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={11} className="px-3 py-6 text-center text-text-tertiary">
                    加载中…
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={11} className="px-3 py-8 text-center text-text-tertiary">
                    <Inbox size={20} className="mx-auto mb-1" />
                    暂无账号
                  </td>
                </tr>
              )}
              {items.map((it) => (
                <tr key={it.id} className="border-b border-border last:border-0 hover:bg-surface-2/60">
                  <td className="px-3 py-1.5">
                    <input type="checkbox" checked={selected.has(it.id)} onChange={() => onToggleOne(it.id)} />
                  </td>
                  <td className="px-3 py-1.5 font-mono text-text-primary">
                    <div>{it.email}</div>
                    {it.subject && <div className="text-tiny text-text-tertiary">{it.subject}</div>}
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">{it.account_type || '—'}</td>
                  <td className="px-3 py-1.5 text-text-secondary">
                    <BalanceCell item={it} />
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge {...statusBadge(it.status)} />
                    {!!it.failure_count && (
                      <div className="mt-0.5 text-tiny text-danger">失败 {it.failure_count}</div>
                    )}
                  </td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      <FlagPill ok={it.has_access_token} label="access" />
                      <FlagPill ok={it.has_refresh_token} label="refresh" />
                    </div>
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge
                      label={it.refresh_enabled ? '开启' : '关闭'}
                      tone={it.refresh_enabled ? 'bg-success-soft text-success' : 'bg-surface-2 text-text-tertiary'}
                    />
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>{it.expires_at ? fmtMs(it.expires_at) : '—'}</div>
                    <div className="text-tiny">{expiryRelative(it.expires_at)}</div>
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>{fmtMs(it.last_refresh_at)}</div>
                    {it.last_refresh_result && (
                      <div className="text-tiny">{it.last_refresh_result}</div>
                    )}
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>成功 {it.success_count ?? 0}</div>
                    <div className="text-tiny">{fmtMs(it.last_used_at)}</div>
                  </td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button className="btn btn-ghost btn-xs" title="编辑账号" onClick={() => setEditing(it)}>
                        <Pencil size={12} /> 编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        disabled={refreshOne.isPending}
                        title="用 refresh_token 续期换新 access_token"
                        onClick={() => refreshOne.mutate(it.id)}
                      >
                        <RefreshCw size={12} /> 刷新
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        disabled={refreshBilling.isPending}
                        title="查询该账号额度"
                        onClick={() => refreshBilling.mutate(it.id)}
                      >
                        <RefreshCw size={12} /> 额度
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除 xAI 账号',
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

      {openImport && <XaiImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />}
      {editing && (
        <XaiEditDialog
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

function BalanceCell({ item }: { item: XaiPoolItem }) {
  const b = item.billing;
  if (b) {
    const danger = b.remaining_usd <= 0;
    const warn = !danger && b.used_pct >= 90;
    return (
      <div className="leading-tight">
        <div className={danger ? 'text-danger' : warn ? 'text-warn' : 'text-text-primary'}>
          剩余 ${b.remaining_usd.toFixed(2)} / ${b.limit_usd.toFixed(2)}
        </div>
        <div className="text-tiny text-text-tertiary">
          已用 {b.used_pct}% · 封顶 ${b.cap_usd.toFixed(0)}
        </div>
        {b.period_end && (
          <div className="text-tiny text-text-tertiary">{b.period_end.slice(0, 10)} 重置</div>
        )}
      </div>
    );
  }
  if (item.balance_note) return <span>{item.balance_note}</span>;
  return <span className="text-text-tertiary">—</span>;
}

function XaiImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [lastResult, setLastResult] = useState<{ imported: number; skipped: number; errors?: string[] } | null>(null);
  const importMut = useMutation({
    mutationFn: () => poolXaiApi.import({ text }),
    onSuccess: (r) => {
      setLastResult({ imported: r.imported, skipped: r.skipped, errors: r.errors });
      if (r.imported > 0) {
        toast.success(`导入完成：成功 ${r.imported}，跳过 ${r.skipped}`);
        onDone();
        onClose();
      } else {
        toast.error(`未入库任何记录（跳过 ${r.skipped} 条），请检查下方错误明细`);
      }
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const ingestFile = async (file: File) => {
    if (file.size > 20 * 1024 * 1024) {
      toast.error('文件太大（>20MB）');
      return;
    }
    try {
      const content = await file.text();
      setText(content);
      setFileName(file.name);
      setLastResult(null);
      toast.success(`已读入 ${file.name}（${content.length.toLocaleString()} 字符）`);
    } catch (err) {
      toast.error(`读取文件失败：${(err as Error).message}`);
    }
  };

  const onPickFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (file) await ingestFile(file);
  };

  return (
    <ImportDialogShell
      title="导入官方 GROK (xAI) 账号"
      description={
        <span>
          凭证来自 <code className="font-mono">cmd/xailogin</code> 的 OAuth 登录产物。支持以下格式：
          <br />
          ① <strong>JSON 数组</strong>：<code className="font-mono">{'[{"access_token":"...","refresh_token":"...","id_token":"...","email":"..."}]'}</code>（推荐，多账号）；
          <br />
          ② <strong>JSONL</strong>：每行一个 <code className="font-mono">{'{"access_token":"...","refresh_token":"..."}'}</code>；
          <br />
          ③ <strong>分隔文本</strong>：<code className="font-mono">email----access_token----refresh_token</code>（<code className="font-mono">----</code> 或 <code className="font-mono">|</code> 分隔）；
          <br />
          ④ 直接粘贴单个 <code className="font-mono">access_token</code>（<code className="font-mono">eyJ…</code> 开头的 JWT）。
          <br />
          缺 email 时会从 id_token 解析；以 email 为 upsert 主键，导入后调度器会自动续期。
        </span>
      }
      onClose={onClose}
      busy={importMut.isPending}
      onConfirm={() => importMut.mutate()}
    >
      <div className="mb-2 flex items-center gap-2">
        <label className="btn btn-outline btn-sm cursor-pointer">
          <Upload size={14} /> 选择文件
          <input type="file" accept=".json,.txt,application/json,text/plain" className="hidden" onChange={onPickFile} />
        </label>
        {fileName && (
          <span className="text-tiny text-text-tertiary truncate" title={fileName}>
            已选：{fileName}（{text.length.toLocaleString()} 字符）
          </span>
        )}
        {text && (
          <button
            type="button"
            className="btn btn-ghost btn-xs ml-auto"
            onClick={() => {
              setText('');
              setFileName(null);
              setLastResult(null);
            }}
          >
            清空
          </button>
        )}
      </div>
      <textarea
        className="input min-h-[260px] w-full font-mono"
        placeholder={`# 粘贴 xailogin 登录 JSON（建议用 [ ] 包成数组），例：
[
  {
    "access_token": "eyJ...",
    "refresh_token": "...",
    "id_token": "eyJ...",
    "email": "you@example.com"
  }
]`}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          if (fileName) setFileName(null);
        }}
      />
      {lastResult && (
        <div
          className={`mt-3 rounded-lg border p-3 text-small ${
            lastResult.imported > 0
              ? 'border-success/30 bg-success/5 text-success'
              : 'border-danger/30 bg-danger/5 text-danger'
          }`}
        >
          <div className="font-medium">
            {lastResult.imported > 0 ? '导入完成' : '未入库任何记录'} · 成功 {lastResult.imported}，跳过 {lastResult.skipped}
          </div>
          {lastResult.errors && lastResult.errors.length > 0 && (
            <details className="mt-2" open={lastResult.imported === 0}>
              <summary className="cursor-pointer text-tiny text-text-tertiary">
                查看错误明细（共 {lastResult.errors.length} 条）
              </summary>
              <ul className="mt-2 max-h-32 list-disc overflow-auto pl-5 text-tiny text-text-secondary">
                {lastResult.errors.slice(0, 50).map((err, i) => (
                  <li key={i} className="font-mono">{err}</li>
                ))}
              </ul>
            </details>
          )}
        </div>
      )}
    </ImportDialogShell>
  );
}

function XaiEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: XaiPoolItem;
  busy?: boolean;
  onClose: () => void;
  onSubmit: (body: XaiPoolUpdateBody) => void;
}) {
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [accountType, setAccountType] = useState(item.account_type ?? '');
  const [status, setStatus] = useState<XaiPoolStatus>((item.status as XaiPoolStatus) ?? 'valid');
  const [refreshEnabled, setRefreshEnabled] = useState<boolean>(item.refresh_enabled);
  const [notes, setNotes] = useState('');

  function submit() {
    const body: XaiPoolUpdateBody = {};
    if (accessToken) body.access_token = accessToken;
    if (refreshToken) body.refresh_token = refreshToken;
    if (accountType !== (item.account_type ?? '')) body.account_type = accountType;
    if (status !== item.status) body.status = status;
    if (refreshEnabled !== item.refresh_enabled) body.refresh_enabled = refreshEnabled;
    if (notes) body.notes = notes;
    onSubmit(body);
  }

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">编辑 xAI 账号</h3>
          <p className="mt-1 text-small text-text-tertiary">
            <span className="font-mono">{item.email}</span> · 凭证字段留空不变，填入则覆盖
          </p>
        </header>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <div className="text-tiny text-text-tertiary">账号类型</div>
            <input
              className="input input-sm w-full"
              value={accountType}
              onChange={(e) => setAccountType(e.target.value)}
              placeholder="super_grok / unknown …"
            />
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">状态</div>
            <select className="select select-sm w-full" value={status} onChange={(e) => setStatus(e.target.value as XaiPoolStatus)}>
              <option value="valid">可用</option>
              <option value="invalid">失效</option>
              <option value="cooldown">冷却</option>
              <option value="disabled">禁用</option>
            </select>
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">自动续期</div>
            <select
              className="select select-sm w-full"
              value={refreshEnabled ? 1 : 0}
              onChange={(e) => setRefreshEnabled(Number(e.target.value) === 1)}
            >
              <option value={1}>开启</option>
              <option value={0}>关闭（不被调度器自动续期）</option>
            </select>
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Access Token（留空不变）</div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              value={accessToken}
              onChange={(e) => setAccessToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Refresh Token（留空不变）</div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              value={refreshToken}
              onChange={(e) => setRefreshToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">备注</div>
            <input
              className="input input-sm w-full"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder={'保留原备注（这里填值会覆盖）'}
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
