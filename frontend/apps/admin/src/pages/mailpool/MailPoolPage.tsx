import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, Cloud, Download, Inbox, Mail, RefreshCw, Trash2 } from 'lucide-react';
import { type ChangeEvent, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { mailPoolApi } from '../../lib/services';
import type { MailPoolMode, MailPoolStatus } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const STATUS_OPTIONS: { value: '' | MailPoolStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'available', label: '可用' },
  { value: 'in_use', label: '使用中' },
  { value: 'registered', label: '已注册' },
  { value: 'failed', label: '失败' },
  { value: 'disabled', label: '禁用' },
];

const MODE_OPTIONS: { value: '' | MailPoolMode; label: string }[] = [
  { value: '', label: '全部后端' },
  { value: 'outlook_graph', label: 'Outlook Graph' },
  { value: 'outlook_imap', label: 'Outlook IMAP' },
  { value: 'tempmail', label: '临时邮箱 API' },
  { value: 'cf', label: 'CF Worker' },
];

function statusBadge(status: string): { label: string; tone: string } {
  switch (status) {
    case 'available':
      return { label: '可用', tone: 'bg-success-soft text-success' };
    case 'in_use':
      return { label: '使用中', tone: 'bg-info-soft text-info' };
    case 'registered':
      return { label: '已注册', tone: 'bg-primary-soft text-primary' };
    case 'failed':
      return { label: '失败', tone: 'bg-danger-soft text-danger' };
    case 'disabled':
      return { label: '禁用', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

function fmtMs(ms?: number): string {
  if (!ms) return '—';
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return '—';
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export default function MailPoolPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | MailPoolStatus>('');
  const [mode, setMode] = useState<'' | MailPoolMode>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [openCFGen, setOpenCFGen] = useState(false);
  const [openTruncate, setOpenTruncate] = useState(false);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      mode: mode || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, mode, page, status],
  );

  const list = useQuery({
    queryKey: ['admin', 'mail-pool', 'list', query],
    queryFn: () => mailPoolApi.list(query),
  });

  const stats = useQuery({
    queryKey: ['admin', 'mail-pool', 'stats'],
    queryFn: () => mailPoolApi.stats(),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'mail-pool'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => mailPoolApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('邮箱已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => mailPoolApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条邮箱`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const reset = useMutation({
    mutationFn: (ids: number[]) => mailPoolApi.reset(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已重置 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const deleteFailed = useMutation({
    mutationFn: () => mailPoolApi.deleteByStatus('failed'),
    onSuccess: (r) => {
      refresh();
      toast.success(`已清理 ${r.affected} 条 failed 邮箱`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const items = list.data?.list ?? [];
  const total = list.data?.total ?? 0;
  const allSelected = items.length > 0 && items.every((it) => selected.has(it.id));

  const onToggleAll = (e: ChangeEvent<HTMLInputElement>) => {
    if (e.target.checked) {
      setSelected(new Set(items.map((it) => it.id)));
    } else {
      setSelected(new Set());
    }
  };
  const onToggleOne = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <PageShell>
      <PageHeader
        icon={<Mail size={16} />}
        title="共享邮箱池"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-outline btn-sm" onClick={() => setOpenCFGen(true)}>
              <Cloud size={14} /> CF 生成
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
              <Download size={14} /> 批量导入
            </button>
          </>
        }
      />

      <StatRow>
        <Stat label="总数" value={stats.data?.total ?? 0} />
        <Stat label="可用" value={stats.data?.available ?? 0} tone="text-success" />
        <Stat label="使用中" value={stats.data?.in_use ?? 0} tone="text-info" />
        <Stat label="已注册" value={stats.data?.registered ?? 0} tone="text-primary" />
        <Stat label="失败" value={stats.data?.failed ?? 0} tone="text-danger" />
        <Stat label="禁用" value={stats.data?.disabled ?? 0} tone="text-text-tertiary" />
      </StatRow>

      <Toolbar>
        <input
          className="input input-sm w-56"
          value={keyword}
          placeholder="搜索邮箱"
          onChange={(e) => {
            setKeyword(e.target.value);
            setPage(1);
          }}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as MailPoolStatus | '');
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
          value={mode}
          onChange={(e) => {
            setMode(e.target.value as MailPoolMode | '');
            setPage(1);
          }}
        >
          {MODE_OPTIONS.map((o) => (
            <option key={o.value || 'all'} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <ToolbarSpacer />
        <button
          className="btn btn-outline btn-sm"
          disabled={selected.size === 0 || reset.isPending}
          onClick={() => reset.mutate(Array.from(selected))}
        >
          重置 ({selected.size})
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={selected.size === 0 || batchDelete.isPending}
          onClick={async () => {
            const ok = await confirm({
              title: '批量删除邮箱',
              description: `将永久删除选中的 ${selected.size} 条邮箱记录，邮箱本身不会被回收。该操作不可恢复，确认继续？`,
              tone: 'danger',
              confirmLabel: '删除',
            });
            if (ok) batchDelete.mutate(Array.from(selected));
          }}
        >
          <Trash2 size={14} /> 删除 ({selected.size})
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={deleteFailed.isPending}
          onClick={async () => {
            const ok = await confirm({
              title: '清理 failed 邮箱',
              description: '将永久删除所有状态为「失败」的邮箱记录。此操作不可恢复，确认继续？',
              tone: 'danger',
              confirmLabel: '清理',
            });
            if (ok) deleteFailed.mutate();
          }}
        >
          清理 failed
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          onClick={() => setOpenTruncate(true)}
          title="按当前筛选条件清空，不选筛选则清空全部"
        >
          <AlertTriangle size={14} /> 清空
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
                <th className="px-3 py-2 text-left">后端</th>
                <th className="px-3 py-2 text-left">状态</th>
                <th className="px-3 py-2 text-left">失败</th>
                <th className="px-3 py-2 text-left">最近错误</th>
                <th className="px-3 py-2 text-left">导入时间</th>
                <th className="px-3 py-2 text-left">使用方</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td className="px-3 py-6 text-center text-text-tertiary" colSpan={9}>
                    加载中…
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td className="px-3 py-8 text-center text-text-tertiary" colSpan={9}>
                    <Inbox size={20} className="mx-auto mb-1 text-text-tertiary" />
                    暂无邮箱
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
                  <td className="px-3 py-1.5 text-text-secondary">{it.mode}</td>
                  <td className="px-3 py-1.5">
                    <Badge {...statusBadge(it.status)} />
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">{it.failure_count}</td>
                  <td className="px-3 py-1.5 text-text-tertiary" title={it.last_error}>
                    <span className="line-clamp-1 max-w-[240px]">{it.last_error || '—'}</span>
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">{fmtMs(it.imported_at)}</td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    {it.used_by_provider ? (
                      <span>
                        {it.used_by_provider}
                        {it.used_by_account_id ? `#${it.used_by_account_id}` : ''}
                      </span>
                    ) : (
                      '—'
                    )}
                  </td>
                  <td className="px-3 py-1.5 text-right">
                    <button
                      className="btn btn-ghost btn-xs text-danger"
                      onClick={async () => {
                        const ok = await confirm({
                          title: '删除邮箱',
                          description: (
                            <>
                              确认删除 <span className="font-mono text-text-primary">{it.email}</span>?
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
        <ImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />
      )}
      {openCFGen && (
        <CFGenerateDialog onClose={() => setOpenCFGen(false)} onDone={() => refresh()} />
      )}
      {openTruncate && (
        <TruncateDialog
          currentFilter={{ status, mode, keyword: keyword.trim() }}
          onClose={() => setOpenTruncate(false)}
          onDone={() => {
            setSelected(new Set());
            refresh();
          }}
        />
      )}
      {confirmDialog}
    </PageShell>
  );
}

function Badge({ label, tone }: { label: string; tone: string }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-tiny ${tone}`}>
      {label}
    </span>
  );
}

function ImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const [mode, setMode] = useState<MailPoolMode>('outlook_graph');
  const [separator, setSeparator] = useState('----');
  const importMut = useMutation({
    mutationFn: () => mailPoolApi.import({ text, mode, separator }),
    onSuccess: (r) => {
      toast.success(`导入完成：成功 ${r.imported}，跳过 ${r.skipped}`);
      onDone();
      onClose();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-2xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">批量导入邮箱</h3>
          <p className="mt-1 text-small text-text-tertiary">
            每行一条，按段数自动识别格式，AES-256-GCM 加密落盘；已存在的 email 会更新 password / client_id / refresh_token。
          </p>
          <ul className="mt-2 space-y-1 text-small text-text-tertiary">
            <li>
              <span className="badge badge-outline mr-1">4 段</span>
              <code className="font-mono">email{separator}password{separator}client_id{separator}refresh_token</code>
            </li>
            <li>
              <span className="badge badge-outline mr-1">7 段卡密</span>
              <code className="font-mono">email{separator}password{separator}读邮链接{separator}email{separator}password{separator}client_id{separator}refresh_token</code>
              <span className="ml-1">（仅取 1/2/6/7 段，其余忽略）</span>
            </li>
          </ul>
        </header>
        <div className="grid grid-cols-2 gap-3">
          <label className="field">
            <span className="field-label">收件后端</span>
            <select className="input" value={mode} onChange={(e) => setMode(e.target.value as MailPoolMode)}>
              <option value="outlook_graph">Outlook Graph (推荐)</option>
              <option value="outlook_imap">Outlook IMAP</option>
              <option value="tempmail">临时邮箱 API</option>
              <option value="cf">CF Worker</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">字段分隔符</span>
            <input className="input" value={separator} onChange={(e) => setSeparator(e.target.value)} />
          </label>
        </div>
        <label className="field">
          <span className="field-label">邮箱清单</span>
          <textarea
            className="input min-h-[260px] font-mono"
            placeholder={`# 4 段格式\nuser@outlook.com----password----xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx----M.C123_BAY.0.U.-...\n\n# 或 7 段卡密（自动识别）\nuser@outlook.com----password----https://xxx/m/abc/user%40outlook.com----user@outlook.com----password----xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx----M.C123_BAY.0.U.-...`}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </label>
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button
            className="btn btn-primary btn-md"
            disabled={!text.trim() || importMut.isPending}
            onClick={() => importMut.mutate()}
          >
            {importMut.isPending ? '导入中…' : '导入'}
          </button>
        </div>
      </div>
    </div>
  );
}

function CFGenerateDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [count, setCount] = useState(10);
  const [domain, setDomain] = useState('');
  const [enablePrefix, setEnablePrefix] = useState(true);
  const [nameLen, setNameLen] = useState(12);
  const [result, setResult] = useState<{
    generated: number;
    skipped: number;
    samples?: string[];
    errors?: string[];
  } | null>(null);

  const genMut = useMutation({
    mutationFn: () =>
      mailPoolApi.cfGenerate({
        count,
        domain: domain.trim() || undefined,
        enable_prefix: enablePrefix,
        name_len: nameLen,
      }),
    onSuccess: (r) => {
      setResult(r);
      toast.success(`生成完成：成功 ${r.generated}，失败 ${r.skipped}`);
      onDone();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-xl space-y-3 p-4 sm:p-6">
        <header>
          <h3 className="text-h4 text-text-primary">CF Worker 预生成临时邮箱（可选）</h3>
          <div className="mt-2 rounded-md border border-info/30 bg-info/5 p-3 text-small text-text-secondary">
            <strong className="text-info">前置条件：</strong>
            系统配置 → 邮箱配置 → <strong>默认收件后端</strong> 必须切到「CF Worker」，
            注册任务才会调 <code className="font-mono">/admin/new_address</code> 现签现用。
            <br />
            <span className="text-text-tertiary">
              当默认后端是 Outlook Graph / IMAP / Tempmail 时，注册任务直接从 mail_pool 拿对应类型的号，
              <strong>不会</strong> 触发 CF 即时签发；这里就退化成"应急储备"——CF 后端临时挂掉时从池里捞预生成的 cf 行。
            </span>
          </div>
          <p className="mt-2 text-small text-text-tertiary">
            调用「系统配置 → 邮箱配置 → CF Worker」中配置的 worker_domain / admin_password ，
            创建 N 个邮箱并入池（mode=cf，存 jwt 到 refresh_token）。
          </p>
        </header>
        <div className="grid grid-cols-2 gap-3">
          <label className="field">
            <span className="field-label">生成数量</span>
            <input
              className="input"
              type="number"
              min={1}
              max={200}
              value={count}
              onChange={(e) => setCount(Math.max(1, Math.min(200, Number(e.target.value) || 1)))}
            />
          </label>
          <label className="field">
            <span className="field-label">用户名长度</span>
            <input
              className="input"
              type="number"
              min={4}
              max={32}
              value={nameLen}
              onChange={(e) => setNameLen(Math.max(4, Math.min(32, Number(e.target.value) || 12)))}
            />
          </label>
          <label className="field col-span-2">
            <span className="field-label">域名（多个用逗号分隔；留空使用系统配置 email_domain）</span>
            <input
              className="input"
              placeholder="qq.qkmss.com, mail.qkmss.com, qq.jzqkwl.com"
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
            />
            <span className="mt-1 text-tiny text-text-tertiary">
              填多个时每条邮箱会随机挑一个域名。系统配置里也支持同样写法。
            </span>
          </label>
          <label className="flex items-center gap-2 col-span-2 text-small text-text-secondary">
            <input
              type="checkbox"
              checked={enablePrefix}
              onChange={(e) => setEnablePrefix(e.target.checked)}
            />
            启用 enablePrefix（worker 会自动加 tmp 前缀；推荐保留）
          </label>
        </div>

        {result && (
          <div className="space-y-1 rounded-md border border-border bg-surface-2 p-3 text-small">
            <div className="text-text-secondary">
              成功 <span className="text-success">{result.generated}</span>，跳过{' '}
              <span className="text-danger">{result.skipped}</span>
            </div>
            {result.samples && result.samples.length > 0 && (
              <div className="text-text-tertiary">
                示例：
                <span className="font-mono">{result.samples.join(', ')}</span>
              </div>
            )}
            {result.errors && result.errors.length > 0 && (
              <div className="text-danger">
                错误：
                <ul className="list-disc pl-5">
                  {result.errors.map((e, i) => (
                    <li key={i}>{e}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}

        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>
            关闭
          </button>
          <button
            className="btn btn-primary btn-md"
            disabled={genMut.isPending}
            onClick={() => genMut.mutate()}
          >
            {genMut.isPending ? '生成中…' : '开始生成'}
          </button>
        </div>
      </div>
    </div>
  );
}

function TruncateDialog({
  currentFilter,
  onClose,
  onDone,
}: {
  currentFilter: { status: '' | MailPoolStatus; mode: '' | MailPoolMode; keyword: string };
  onClose: () => void;
  onDone: () => void;
}) {
  const [scope, setScope] = useState<'all' | 'filtered'>(
    currentFilter.status || currentFilter.mode || currentFilter.keyword ? 'filtered' : 'all',
  );
  const [confirmText, setConfirmText] = useState('');
  const hasFilter = Boolean(
    currentFilter.status || currentFilter.mode || currentFilter.keyword,
  );

  const truncateMut = useMutation({
    mutationFn: () => {
      const body: { confirm: 'DELETE'; status?: string; mode?: string; keyword?: string } = {
        confirm: 'DELETE',
      };
      if (scope === 'filtered') {
        if (currentFilter.status) body.status = currentFilter.status;
        if (currentFilter.mode) body.mode = currentFilter.mode;
        if (currentFilter.keyword) body.keyword = currentFilter.keyword;
      }
      return mailPoolApi.truncate(body);
    },
    onSuccess: (r) => {
      toast.success(`已清空 ${r.affected} 条邮箱记录`);
      onDone();
      onClose();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const canSubmit = confirmText === 'DELETE' && !truncateMut.isPending;
  const statusLabel = STATUS_OPTIONS.find((o) => o.value === currentFilter.status)?.label;
  const modeLabel = MODE_OPTIONS.find((o) => o.value === currentFilter.mode)?.label;

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <div className="dialog-surface relative w-full max-w-lg space-y-4 p-4 sm:p-6">
        <header className="flex items-start gap-2">
          <AlertTriangle className="mt-0.5 shrink-0 text-danger" size={20} />
          <div>
            <h3 className="text-h4 text-text-primary">清空邮箱池</h3>
            <p className="mt-1 text-small text-text-tertiary">
              该操作执行<strong className="text-danger"> 软删除（标记 deleted_at）</strong>，已被
              注册任务在用的行不影响；操作不可在 UI 撤销。
            </p>
          </div>
        </header>

        <div className="space-y-2 rounded-md border border-border bg-surface-2 p-3 text-small">
          <label className="flex items-start gap-2 cursor-pointer">
            <input
              type="radio"
              className="mt-1"
              checked={scope === 'all'}
              onChange={() => setScope('all')}
            />
            <span>
              <strong className="text-text-primary">清空全部</strong>
              <div className="text-text-tertiary">删除全部未删除的邮箱（不可恢复）。</div>
            </span>
          </label>
          <label className="flex items-start gap-2 cursor-pointer">
            <input
              type="radio"
              className="mt-1"
              checked={scope === 'filtered'}
              onChange={() => setScope('filtered')}
              disabled={!hasFilter}
            />
            <span>
              <strong className="text-text-primary">按当前筛选条件清空</strong>
              <div className="text-text-tertiary">
                {hasFilter ? (
                  <span>
                    将匹配：
                    {statusLabel && <span className="mx-1 badge badge-outline">状态={statusLabel}</span>}
                    {modeLabel && <span className="mx-1 badge badge-outline">后端={modeLabel}</span>}
                    {currentFilter.keyword && (
                      <span className="mx-1 badge badge-outline">关键字={currentFilter.keyword}</span>
                    )}
                  </span>
                ) : (
                  <span className="text-text-tertiary">（顶部未设置任何筛选条件，请改用「清空全部」）</span>
                )}
              </div>
            </span>
          </label>
        </div>

        <label className="field">
          <span className="field-label">
            请输入 <code className="font-mono text-danger">DELETE</code> 确认执行
          </span>
          <input
            className="input font-mono"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder="DELETE"
            autoFocus
          />
        </label>

        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button
            className="btn btn-danger btn-md"
            disabled={!canSubmit}
            onClick={() => truncateMut.mutate()}
          >
            {truncateMut.isPending ? '清空中…' : '确认清空'}
          </button>
        </div>
      </div>
    </div>
  );
}
