import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  CheckCircle2,
  Clock,
  Download,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  Trash2,
  XCircle,
} from 'lucide-react';
import { type FormEvent, type ReactNode, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError } from '../../lib/api';
import { fmtRelative, fmtTime } from '../../lib/format';
import { proxiesApi } from '../../lib/services';
import type { ProxyCreateBody, ProxyItem, ProxyUpdateBody } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import {
  PageHeader,
  PageShell,
  Pager,
  Section,
  Toolbar,
  ToolbarSpacer,
} from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';
import { Globe2 } from 'lucide-react';

const PROTOS: ProxyCreateBody['protocol'][] = ['http', 'https', 'socks5', 'socks5h'];

function checkLabel(s?: number): { label: string; cls: string; icon: typeof CheckCircle2 } {
  switch (s) {
    case 1:
      return { label: 'OK', cls: 'text-success', icon: CheckCircle2 };
    case 2:
      return { label: 'FAIL', cls: 'text-danger', icon: XCircle };
    default:
      return { label: '未测', cls: 'text-text-tertiary', icon: Clock };
  }
}

export default function ProxiesPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();

  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openDlg, setOpenDlg] = useState<{ mode: 'create' } | { mode: 'edit'; row: ProxyItem } | null>(null);
  const [openImport, setOpenImport] = useState(false);
  const headerCbRef = useRef<HTMLInputElement | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: statusFilter === 'all' ? undefined : statusFilter === 'enabled' ? (1 as const) : (0 as const),
      page,
      page_size: pageSize,
    }),
    [keyword, page, pageSize, statusFilter],
  );

  const list = useQuery({
    queryKey: ['admin', 'proxies', 'list', query],
    queryFn: () => proxiesApi.list(query),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'proxies'] });

  const toggle = useMutation({
    mutationFn: ({ id, status }: { id: number; status: 0 | 1 }) => proxiesApi.update(id, { status }),
    onSuccess: () => {
      refresh();
      toast.success('代理已更新');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const remove = useMutation({
    mutationFn: (id: number) => proxiesApi.remove(id),
    onSuccess: () => {
      refresh();
      setSelected((prev) => {
        const next = new Set(prev);
        if (remove.variables) next.delete(remove.variables);
        return next;
      });
      toast.success('代理已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => proxiesApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.deleted} 条代理`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const testMut = useMutation({
    mutationFn: (id: number) => proxiesApi.test(id),
    onSuccess: (r) => {
      refresh();
      if (r.ok) toast.success(`代理可达，延迟 ${r.latency_ms}ms`);
      else toast.error(`代理不可用：${r.error || '未知错误'}`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const batchTestMut = useMutation({
    mutationFn: (ids: number[]) => proxiesApi.batchTest(ids),
    onSuccess: (r) => {
      refresh();
      toast.success(`批量测试完成，成功 ${r.success} 条，失败 ${r.failed} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const total = list.data?.total ?? 0;
  const items: ProxyItem[] = list.data?.list ?? [];
  const pageIds = items.map((p) => p.id);
  const pageAllSelected = pageIds.length > 0 && pageIds.every((id) => selected.has(id));

  useEffect(() => {
    setSelected(new Set());
  }, [keyword, statusFilter, page]);

  useEffect(() => {
    const el = headerCbRef.current;
    if (!el) return;
    const some = pageIds.some((id) => selected.has(id));
    el.indeterminate = some && !pageAllSelected;
  }, [pageAllSelected, pageIds, selected]);

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const toggleSelectPage = () => {
    const allOn = pageIds.length > 0 && pageIds.every((id) => selected.has(id));
    setSelected((prev) => {
      const next = new Set(prev);
      if (allOn) pageIds.forEach((id) => next.delete(id));
      else pageIds.forEach((id) => next.add(id));
      return next;
    });
  };

  return (
    <PageShell>
      <PageHeader
        icon={<Globe2 size={16} />}
        title="代理管理"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={refresh}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-outline btn-sm" onClick={() => setOpenImport(true)}>
              <Download size={14} /> 导入
            </button>
            <button
              className="btn btn-outline btn-sm"
              disabled={selected.size === 0 || batchTestMut.isPending}
              onClick={() => {
                if (selected.size === 0) return;
                batchTestMut.mutate([...selected]);
              }}
            >
              <Activity size={14} /> 测试 ({selected.size})
            </button>
            <button
              className="btn btn-outline btn-sm text-danger"
              disabled={selected.size === 0 || batchDelete.isPending}
              onClick={async () => {
                if (selected.size === 0) return;
                const ok = await confirm({
                  title: '批量删除代理',
                  description: `将永久删除选中的 ${selected.size} 条代理。引用这些代理的账号 / 注册任务将失去代理来源，请确认继续。`,
                  tone: 'danger',
                  confirmLabel: '删除',
                });
                if (ok) batchDelete.mutate([...selected]);
              }}
            >
              <Trash2 size={14} /> 删除 ({selected.size})
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setOpenDlg({ mode: 'create' })}>
              <Plus size={14} /> 新增
            </button>
          </>
        }
      />

      <Toolbar>
        <select
          className="select select-sm"
          value={statusFilter}
          onChange={(e) => {
            setStatusFilter(e.target.value as 'all' | 'enabled' | 'disabled');
            setPage(1);
          }}
        >
          <option value="all">全部状态</option>
          <option value="enabled">启用</option>
          <option value="disabled">禁用</option>
        </select>
        <input
          className="input input-sm flex-1 min-w-[220px]"
          placeholder="搜索名称 / 备注 / 主机"
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value);
            setPage(1);
          }}
        />
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary">共 {total} 条</span>
      </Toolbar>

      <div className="space-y-3 md:hidden">
        {items.map((p) => {
          const enabled = p.status === 1;
          const t = checkLabel(p.last_check_ok);
          return (
            <div key={p.id} className="rounded-xl border border-border bg-surface-1 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-body font-semibold text-text-primary">{p.name}</div>
                  <div className="mt-1 text-tiny text-text-tertiary">{p.protocol} · {p.host}:{p.port}</div>
                </div>
                {enabled ? <span className="badge badge-success">启用</span> : <span className="badge">禁用</span>}
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 text-tiny">
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">认证</div>
                  <div className="text-text-secondary">{p.username ? p.username : '无'}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">测试</div>
                  <div className={t.cls}>{t.label}{p.last_check_ms ? ` / ${p.last_check_ms}ms` : ''}</div>
                </div>
              </div>
              <div className="mt-3 flex flex-wrap gap-2">
                <button className="btn btn-ghost btn-sm" onClick={() => testMut.mutate(p.id)}>
                  <Activity size={14} /> 测试
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setOpenDlg({ mode: 'edit', row: p })}>
                  <Pencil size={14} /> 编辑
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => toggle.mutate({ id: p.id, status: enabled ? 0 : 1 })}>
                  <Power size={14} /> {enabled ? '停用' : '启用'}
                </button>
                <button
                  className="btn btn-danger-ghost btn-sm"
                  onClick={async () => {
                    const ok = await confirm({
                      title: '删除代理',
                      description: (
                        <>
                          确认删除代理 <span className="font-mono text-text-primary">{p.name}</span>？引用此代理的账号 / 注册任务会失去代理来源。
                        </>
                      ),
                      tone: 'danger',
                      confirmLabel: '删除',
                    });
                    if (ok) remove.mutate(p.id);
                  }}
                >
                  <Trash2 size={14} /> 删除
                </button>
              </div>
            </div>
          );
        })}
      </div>

      <Section bodyClass="p-0">
      <div className="hidden overflow-x-auto md:block">
        <table className="data-table min-w-[1120px]">
          <thead>
            <tr>
              <th className="w-10">
                <input
                  ref={headerCbRef}
                  type="checkbox"
                  className="rounded border-border"
                  checked={pageAllSelected}
                  onChange={toggleSelectPage}
                  disabled={list.isLoading || items.length === 0}
                  title="全选当前页"
                />
              </th>
              <th>名称</th>
              <th>协议</th>
              <th>地址</th>
              <th>认证</th>
              <th>状态</th>
              <th>最近探测</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {list.isLoading && (
              <tr>
                <td colSpan={8} className="py-10 text-center text-small text-text-tertiary">
                  加载中…
                </td>
              </tr>
            )}
            {!list.isLoading && items.length === 0 && (
              <tr>
                <td colSpan={8}>
                  <div className="empty-state">
                    <p className="empty-state-title">暂无代理</p>
                    <p className="empty-state-desc">点击右上角“新增代理”或“批量导入”开始配置。</p>
                  </div>
                </td>
              </tr>
            )}
            {items.map((p) => {
              const enabled = p.status === 1;
              const t = checkLabel(p.last_check_ok);
              const Icon = t.icon;
              return (
                <tr key={p.id}>
                  <td>
                    <input
                      type="checkbox"
                      className="rounded border-border"
                      checked={selected.has(p.id)}
                      onChange={() => toggleSelect(p.id)}
                    />
                  </td>
                  <td className="font-medium text-text-primary">
                    {p.name}
                    {p.remark && <span className="mt-0.5 block text-small text-text-tertiary">{p.remark}</span>}
                  </td>
                  <td className="font-semibold uppercase text-klein-500">{p.protocol}</td>
                  <td className="font-mono text-small text-text-secondary">
                    {p.host}:{p.port}
                  </td>
                  <td className="text-small">
                    {p.username ? (
                      <span>
                        {p.username}
                        {p.has_password && <span className="text-text-tertiary"> · ******</span>}
                      </span>
                    ) : (
                      <span className="text-text-tertiary">无</span>
                    )}
                  </td>
                  <td>{enabled ? <span className="badge badge-success">启用</span> : <span className="badge">禁用</span>}</td>
                  <td className="text-small">
                    <div className={`inline-flex items-center gap-1 ${t.cls}`}>
                      <Icon size={12} />
                      <span>
                        {t.label}
                        {p.last_check_ms ? ` · ${p.last_check_ms}ms` : ''}
                      </span>
                    </div>
                    {p.last_check_at && (
                      <span className="mt-0.5 block text-tiny text-text-tertiary" title={fmtTime(p.last_check_at)}>
                        {fmtRelative(p.last_check_at)}
                      </span>
                    )}
                    {p.last_error && (
                      <span className="mt-0.5 block max-w-[220px] truncate text-tiny text-danger" title={p.last_error}>
                        {p.last_error}
                      </span>
                    )}
                  </td>
                  <td>
                    <div className="inline-flex gap-1">
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title="测试连通性"
                        onClick={() => testMut.mutate(p.id)}
                        disabled={testMut.isPending && testMut.variables === p.id}
                      >
                        <Activity
                          size={14}
                          className={
                            testMut.isPending && testMut.variables === p.id
                              ? 'animate-pulse text-klein-500'
                              : 'text-text-secondary'
                          }
                        />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title="编辑"
                        onClick={() => setOpenDlg({ mode: 'edit', row: p })}
                      >
                        <Pencil size={14} />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title={enabled ? '禁用' : '启用'}
                        onClick={() => toggle.mutate({ id: p.id, status: enabled ? 0 : 1 })}
                      >
                        <Power size={14} className={enabled ? 'text-success' : 'text-text-tertiary'} />
                      </button>
                      <button
                        className="btn btn-danger-ghost btn-icon btn-sm"
                        title="删除"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除代理',
                            description: (
                              <>
                                确认删除代理 <span className="font-mono text-text-primary">{p.name}</span>？引用此代理的账号 / 注册任务会失去代理来源。
                              </>
                            ),
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) remove.mutate(p.id);
                        }}
                      >
                        <Trash2 size={14} />
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

      {openDlg && (
        <ProxyDialog
          mode={openDlg.mode}
          row={openDlg.mode === 'edit' ? openDlg.row : undefined}
          onClose={() => setOpenDlg(null)}
          onSuccess={() => {
            setOpenDlg(null);
            refresh();
          }}
        />
      )}

      {openImport && (
        <ProxyImportDialog
          onClose={() => setOpenImport(false)}
          onSuccess={() => {
            setOpenImport(false);
            refresh();
          }}
        />
      )}
      {confirmDialog}
    </PageShell>
  );
}

function ProxyDialog({
  mode,
  row,
  onClose,
  onSuccess,
}: {
  mode: 'create' | 'edit';
  row?: ProxyItem;
  onClose: () => void;
  onSuccess: () => void;
}) {
  const [body, setBody] = useState<ProxyCreateBody>(() =>
    row
      ? {
          name: row.name,
          protocol: (row.protocol as ProxyCreateBody['protocol']) || 'http',
          host: row.host,
          port: row.port,
          username: row.username || '',
          password: '',
          remark: row.remark || '',
        }
      : { name: '', protocol: 'http', host: '', port: 7890, username: '', password: '', remark: '' },
  );

  const create = useMutation({
    mutationFn: (b: ProxyCreateBody) => proxiesApi.create(b),
    onSuccess: () => {
      toast.success('代理已添加');
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const update = useMutation({
    mutationFn: (b: ProxyUpdateBody) => proxiesApi.update(row!.id, b),
    onSuccess: () => {
      toast.success('代理已更新');
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!body.name.trim() || !body.host.trim() || !body.port) {
      toast.error('请填写名称、主机和端口');
      return;
    }

    const payload: ProxyCreateBody = {
      ...body,
      name: body.name.trim(),
      host: body.host.trim(),
      username: body.username?.trim() || undefined,
      password: body.password || undefined,
      remark: body.remark?.trim() || undefined,
    };

    if (mode === 'create') {
      create.mutate(payload);
      return;
    }

    const patch: ProxyUpdateBody = {
      name: payload.name,
      protocol: payload.protocol,
      host: payload.host,
      port: payload.port,
      username: payload.username,
      remark: payload.remark,
    };
    if (body.password) patch.password = body.password;
    update.mutate(patch);
  };

  const submitting = create.isPending || update.isPending;

  return (
    <Modal title={mode === 'create' ? '新增代理' : '编辑代理'} onClose={onClose}>
      <form className="space-y-3" onSubmit={submit}>
        <div className="grid grid-cols-2 gap-3">
          <Field label="名称">
            <input
              className="input"
              placeholder="例如：HK-Cloudflare-1"
              value={body.name}
              onChange={(e) => setBody((s) => ({ ...s, name: e.target.value }))}
            />
          </Field>
          <Field label="协议">
            <select
              className="select"
              value={body.protocol}
              onChange={(e) => setBody((s) => ({ ...s, protocol: e.target.value as ProxyCreateBody['protocol'] }))}
            >
              {PROTOS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <div className="grid grid-cols-3 gap-3">
          <Field label="主机 (host)" className="col-span-2">
            <input
              className="input"
              placeholder="proxy.example.com"
              value={body.host}
              onChange={(e) => setBody((s) => ({ ...s, host: e.target.value }))}
            />
          </Field>
          <Field label="端口">
            <input
              type="number"
              className="input"
              min={1}
              max={65535}
              value={body.port || ''}
              onChange={(e) => setBody((s) => ({ ...s, port: Number(e.target.value) || 0 }))}
            />
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label="用户名（可选）">
            <input
              className="input"
              value={body.username || ''}
              onChange={(e) => setBody((s) => ({ ...s, username: e.target.value }))}
            />
          </Field>
          <Field label={mode === 'edit' ? '密码（留空表示不修改）' : '密码（可选）'}>
            <input
              type="password"
              className="input"
              value={body.password || ''}
              onChange={(e) => setBody((s) => ({ ...s, password: e.target.value }))}
            />
          </Field>
        </div>

        <Field label="备注">
          <input
            className="input"
            value={body.remark || ''}
            onChange={(e) => setBody((s) => ({ ...s, remark: e.target.value }))}
          />
        </Field>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={submitting}>
            {submitting ? '提交中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function ProxyImportDialog({
  onClose,
  onSuccess,
}: {
  onClose: () => void;
  onSuccess: () => void;
}) {
  const [text, setText] = useState('');
  const [remark, setRemark] = useState('');

  const importMut = useMutation({
    mutationFn: () => proxiesApi.import({ text, remark: remark.trim() || undefined }),
    onSuccess: (r) => {
      toast.success(`已导入 ${r.imported} 条，跳过 ${r.skipped} 条`);
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!text.trim()) {
      toast.error('请粘贴代理链接');
      return;
    }
    importMut.mutate();
  };

  return (
    <Modal title="批量导入代理" onClose={onClose}>
      <form className="space-y-3" onSubmit={submit}>
        <label className="field">
          <span className="field-label">代理链接</span>
          <textarea
            className="textarea min-h-[220px] font-mono text-small"
            placeholder={'每行一条，例如：\nsocks5://user:pass@127.0.0.1:1080'}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <span className="field-hint">支持 `http://`、`https://`、`socks5://`、`socks5h://` 多行导入。</span>
        </label>

        <label className="field">
          <span className="field-label">统一备注</span>
          <input
            className="input"
            placeholder="可选，导入的代理都会附带这个备注"
            value={remark}
            onChange={(e) => setRemark(e.target.value)}
          />
        </label>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={importMut.isPending}>
            {importMut.isPending ? '导入中…' : '开始导入'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-[80] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in w-full max-w-xl">
        <header className="flex h-12 items-center justify-between border-b border-border px-5">
          <h3 className="font-semibold text-text-primary">{title}</h3>
          <button className="btn btn-ghost btn-icon btn-sm" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </header>
        <div className="max-h-[70vh] overflow-y-auto p-5">{children}</div>
      </div>
    </div>
  );
}

function Field({
  label,
  hint,
  className,
  children,
}: {
  label: string;
  hint?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <label className={`field ${className || ''}`}>
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}
