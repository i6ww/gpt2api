import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { type ChangeEvent, useEffect, useMemo, useState } from 'react';
import { Inbox, Pencil, RefreshCw, Trash2, Upload } from 'lucide-react';

import { ApiError } from '../../lib/api';
import { poolGoogleApi } from '../../lib/services';
import type { GooglePoolItem, GooglePoolStatus, GooglePoolUpdateBody } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, SplitMenu, fmtMs } from './_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

type StatusFilter = '' | GooglePoolStatus;

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
  if (!expiresAt) return '未知';
  const diffMs = expiresAt - Date.now();
  if (diffMs <= 0) return '已过期';
  const hours = diffMs / 3600_000;
  if (hours < 1) return `${Math.round(diffMs / 60_000)} 分钟后`;
  if (hours < 24) return `${hours.toFixed(1)} 小时后`;
  return `${(hours / 24).toFixed(1)} 天后`;
}

export default function GooglePoolPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<StatusFilter>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [editing, setEditing] = useState<GooglePoolItem | null>(null);
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
    queryKey: ['admin', 'pool-google', 'list', query],
    queryFn: () => poolGoogleApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'pool-google', 'stats'],
    queryFn: () => poolGoogleApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'pool-google'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => poolGoogleApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('账号已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => poolGoogleApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const refreshOne = useMutation({
    mutationFn: ({ id, onlyCredits }: { id: number; onlyCredits?: boolean }) =>
      poolGoogleApi.refresh(id, !!onlyCredits),
    onSuccess: (r) => {
      refresh();
      toast.success(`已刷新 #${r.id}：积分 ${r.credits.toFixed(2)}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '刷新失败'),
  });
  const refreshAll = useMutation({
    mutationFn: () => poolGoogleApi.refreshAll(),
    onSuccess: (r) => {
      refresh();
      toast.success(`扫描完成：成功 ${r.ok}，失败 ${r.fail}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '扫描失败'),
  });
  const batchRefresh = useMutation({
    mutationFn: (body: { scope: 'all' | 'abnormal' | 'expiring'; only_credits?: boolean }) =>
      poolGoogleApi.batchRefresh(body),
    onSuccess: (r) => {
      refresh();
      toast.success(`批量刷新完成：${r.ok}/${r.total} 成功，${r.fail} 失败`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量刷新失败'),
  });
  const updateOne = useMutation({
    mutationFn: ({ id, body }: { id: number; body: GooglePoolUpdateBody }) =>
      poolGoogleApi.update(id, body),
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
          placeholder="搜索 email / display_name"
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
          label="扫描续期"
          icon={<RefreshCw size={14} />}
          busy={batchRefresh.isPending || refreshAll.isPending}
          items={[
            {
              label: '刷新异常账号',
              description: 'invalid / cooldown 的账号，重新续期 + 拉积分',
              onClick: () => batchRefresh.mutate({ scope: 'abnormal', only_credits: false }),
            },
            {
              label: '刷新全部账号',
              description: '所有账号一起续期（耗时）',
              onClick: () => batchRefresh.mutate({ scope: 'all', only_credits: false }),
            },
            {
              label: '仅扫描即将过期（< 12h）',
              description: '与后台调度器一致的续期策略',
              onClick: () => refreshAll.mutate(),
            },
          ]}
        />
        <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} /> 导入
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={selected.size === 0 || batchDelete.isPending}
          onClick={async () => {
            if (selected.size === 0) return;
            const ok = await confirm({
              title: '批量删除歌曲账号',
              description: `将永久删除选中的 ${selected.size} 条 FlowMusic 账号（含凭证）。`,
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
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-left">积分</th>
                <th className="px-3 py-2 text-left">凭证</th>
                <th className="px-3 py-2 text-left">Token 失效</th>
                <th className="px-3 py-2 text-left">最近续期</th>
                <th className="px-3 py-2 text-left">最近使用</th>
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
                    <input type="checkbox" checked={selected.has(it.id)} onChange={() => onToggleOne(it.id)} />
                  </td>
                  <td className="px-3 py-1.5 font-mono text-text-primary">
                    <div>{it.email}</div>
                    {it.display_name && <div className="text-tiny text-text-tertiary">{it.display_name}</div>}
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge {...statusBadge(it.status)} />
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">{it.credits.toFixed(2)}</td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      <FlagPill ok={it.has_credential} label="cred" />
                      <FlagPill ok={it.has_cookie} label="cookie" />
                    </div>
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>{fmtMs(it.expires_at)}</div>
                    <div className="text-tiny">{expiryRelative(it.expires_at)}</div>
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">{fmtMs(it.last_refresh_at)}</td>
                  <td className="px-3 py-1.5 text-text-tertiary">{fmtMs(it.last_used_at)}</td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button className="btn btn-ghost btn-xs" title="编辑账号" onClick={() => setEditing(it)}>
                        <Pencil size={12} /> 编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        disabled={refreshOne.isPending}
                        title="续期换 token + 拉积分"
                        onClick={() => refreshOne.mutate({ id: it.id })}
                      >
                        <RefreshCw size={12} /> 刷新
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除歌曲账号',
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

      {openImport && <GoogleImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />}
      {editing && (
        <GoogleEditDialog
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

function GoogleImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [lastResult, setLastResult] = useState<{ imported: number; skipped: number; errors?: string[] } | null>(null);
  const importMut = useMutation({
    mutationFn: () => poolGoogleApi.import({ text, source: 'import' }),
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
      title="批量导入歌曲（FlowMusic）账号"
      description={
        <span>
          支持以下 <strong>3 种格式</strong>（点「选择文件」上传 .json/.txt，或粘贴文本）：
          <br />
          ① 浏览器导出的 <strong>cookie JSON 数组</strong>（含 <code className="font-mono">sb-sb-auth-token.0/.1</code>）—— 单账号最省事；
          <br />
          ② <code className="font-mono">{'[{"cookies":"[…cookie数组字符串…]","email":"..."}, ...]'}</code>（多账号 JSON 数组）；
          <br />
          ③ 每行一个 <code className="font-mono">{'{"cookies":"...","email":"..."}'}</code>（JSONL，多账号）。
          <br />
          凭证以加密 bundle 入库，以 email 为 upsert 主键；导入后后台会自动续期补全信息。
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
        placeholder={`# 粘贴浏览器导出的 cookie JSON 数组（单账号），例：
[
  { "name": "sb-sb-auth-token.0", "value": "base64-..." },
  { "name": "sb-sb-auth-token.1", "value": "..." }
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

function GoogleEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: GooglePoolItem;
  busy?: boolean;
  onClose: () => void;
  onSubmit: (body: GooglePoolUpdateBody) => void;
}) {
  const [displayName, setDisplayName] = useState(item.display_name ?? '');
  const [cookies, setCookies] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [status, setStatus] = useState<GooglePoolStatus>((item.status as GooglePoolStatus) ?? 'valid');
  const [credits, setCredits] = useState<string>(String(item.credits ?? 0));
  const [refreshEnabled, setRefreshEnabled] = useState<0 | 1>(1);
  const [notes, setNotes] = useState('');

  function submit() {
    const body: GooglePoolUpdateBody = {};
    if (displayName !== (item.display_name ?? '')) body.display_name = displayName;
    if (cookies) body.cookies = cookies;
    if (accessToken) body.access_token = accessToken;
    if (refreshToken) body.refresh_token = refreshToken;
    if (status !== item.status) body.status = status;
    const c = Number(credits);
    if (!Number.isNaN(c) && c !== item.credits) body.credits = c;
    body.refresh_enabled = refreshEnabled;
    if (notes) body.notes = notes;
    onSubmit(body);
  }

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">编辑歌曲账号</h3>
          <p className="mt-1 text-small text-text-tertiary">
            <span className="font-mono">{item.email}</span> · 凭证字段留空不变，填入则覆盖
          </p>
        </header>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <div className="text-tiny text-text-tertiary">Display Name</div>
            <input className="input input-sm w-full" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">状态</div>
            <select className="select select-sm w-full" value={status} onChange={(e) => setStatus(e.target.value as GooglePoolStatus)}>
              <option value="valid">可用</option>
              <option value="invalid">失效</option>
              <option value="cooldown">冷却</option>
              <option value="disabled">禁用</option>
            </select>
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">积分</div>
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
            <div className="text-tiny text-text-tertiary">自动续期</div>
            <select
              className="select select-sm w-full"
              value={refreshEnabled}
              onChange={(e) => setRefreshEnabled(Number(e.target.value) as 0 | 1)}
            >
              <option value={1}>开启</option>
              <option value={0}>关闭（不被调度器自动续期）</option>
            </select>
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Cookies（JSON 数组 / cookie 头，留空不变）</div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              value={cookies}
              onChange={(e) => setCookies(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Access Token / Supabase JWT（留空不变）</div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              value={accessToken}
              onChange={(e) => setAccessToken(e.target.value)}
            />
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Refresh Token（留空不变）</div>
            <input
              className="input input-sm w-full font-mono"
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
