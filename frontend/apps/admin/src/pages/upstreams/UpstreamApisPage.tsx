import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  CheckCircle2,
  Clock,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  Trash2,
  XCircle,
} from 'lucide-react';
import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { fmtRelative, fmtTime } from '../../lib/format';
import { accountsApi, proxiesApi } from '../../lib/services';
import type { AccountCreateBody, AccountItem, AccountUpdateBody, ProxyItem } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';
import { Globe2 } from 'lucide-react';

function normalizeBaseURL(value?: string): string | undefined {
  const v = (value || '').trim();
  if (!v) return undefined;
  if (/^https?:\/\//i.test(v)) return v;
  return `https://${v}`;
}

function getTestState(status?: number): { label: string; cls: string; icon: typeof CheckCircle2 } {
  switch (status) {
    case 1:
      return { label: 'OK', cls: 'text-success', icon: CheckCircle2 };
    case 2:
      return { label: 'FAIL', cls: 'text-danger', icon: XCircle };
    default:
      return { label: '未测试', cls: 'text-text-tertiary', icon: Clock };
  }
}

function renderModels(models?: string[]): string {
  if (!models || models.length === 0) return '未同步';
  if (models.length <= 3) return models.join(', ');
  return `${models.slice(0, 3).join(', ')} 等 ${models.length} 个`;
}

export default function UpstreamApisPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [provider, setProvider] = useState<'all' | 'gpt' | 'grok' | 'pic2api'>('all');
  const [keyword, setKeyword] = useState('');
  const [page, setPage] = useState(1);
  const [openDialog, setOpenDialog] = useState<{ mode: 'create' } | { mode: 'edit'; row: AccountItem } | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      provider: provider === 'all' ? undefined : provider,
      auth_type: 'api_key' as const,
      keyword: keyword.trim() || undefined,
      page: 1,
      page_size: 1000,
    }),
    [keyword, provider],
  );

  const listQuery = useQuery({
    queryKey: ['admin', 'upstreams', 'accounts', query],
    queryFn: () => accountsApi.list(query),
  });

  const proxiesQuery = useQuery({
    queryKey: ['admin', 'upstreams', 'proxies'],
    queryFn: () => proxiesApi.list({ page: 1, page_size: 1000 }),
  });

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['admin', 'upstreams'] });
    qc.invalidateQueries({ queryKey: ['admin', 'accounts'] });
    qc.invalidateQueries({ queryKey: ['admin', 'proxies'] });
  };

  const toggleMutation = useMutation({
    mutationFn: ({ id, status }: { id: number; status: 0 | 1 }) => accountsApi.update(id, { status }),
    onSuccess: () => {
      refresh();
      toast.success('上游状态已更新');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (id: number) => accountsApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('上游已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const testMutation = useMutation({
    mutationFn: (id: number) => accountsApi.test(id),
    onSuccess: (result) => {
      refresh();
      if (result.ok) {
        const modelsText = result.supported_models?.length ? `，已同步 ${result.supported_models.length} 个模型` : '';
        toast.success(`连通正常，耗时 ${result.latency_ms}ms${modelsText}`);
      } else {
        toast.error(`测试失败：${result.error || '未知错误'}`);
      }
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const syncModelsMutation = useMutation({
    mutationFn: (id: number) => accountsApi.syncModels(id),
    onSuccess: (result) => {
      refresh();
      toast.success(`已同步 ${result.supported_models?.length || 0} 个模型`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const upstreams = listQuery.data?.list ?? [];
  const total = upstreams.length;
  const lastPage = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(page, lastPage);
  const items = upstreams.slice((safePage - 1) * pageSize, safePage * pageSize);

  useEffect(() => {
    setPage(1);
  }, [keyword, provider]);

  useEffect(() => {
    if (page > lastPage) setPage(lastPage);
  }, [lastPage, page]);

  const proxyMap = useMemo(() => {
    const map = new Map<number, ProxyItem>();
    for (const item of proxiesQuery.data?.list ?? []) {
      map.set(item.id, item);
    }
    return map;
  }, [proxiesQuery.data?.list]);

  return (
    <PageShell>
      <PageHeader
        icon={<Globe2 size={16} />}
        title="上游 API 管理"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={refresh}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setOpenDialog({ mode: 'create' })}>
              <Plus size={14} /> 新增
            </button>
          </>
        }
      />

      <Toolbar>
        <select
          className="select select-sm"
          value={provider}
          onChange={(e) => setProvider(e.target.value as 'all' | 'gpt' | 'grok' | 'pic2api')}
        >
          <option value="all">全部 Provider</option>
          <option value="gpt">GPT</option>
          <option value="grok">GROK</option>
          <option value="pic2api">PIC2API</option>
        </select>
        <input
          className="input input-sm flex-1 min-w-[220px]"
          placeholder="搜索名称 / Base URL / 备注"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
        />
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary whitespace-nowrap">共 {total} 条</span>
      </Toolbar>

      <div className="space-y-3 md:hidden">
        {items.map((row) => {
          const enabled = row.status === 1;
          const testState = getTestState(row.last_test_status);
          return (
            <div key={row.id} className="rounded-xl border border-border bg-surface-1 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-body font-semibold text-text-primary">{row.name}</div>
                  <div className="mt-1 text-tiny text-text-tertiary">{row.provider} · {row.base_url || '默认'}</div>
                </div>
                {enabled ? <span className="badge badge-success">启用</span> : <span className="badge">禁用</span>}
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 text-tiny">
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">模型</div>
                  <div className="text-text-secondary">{row.supported_models?.length || 0} 个</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">测试</div>
                  <div className={testState.cls}>{testState.label}</div>
                </div>
              </div>
              <div className="mt-3 flex flex-wrap gap-2">
                <button className="btn btn-ghost btn-sm" onClick={() => testMutation.mutate(row.id)}>
                  <Activity size={14} /> 测试
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => syncModelsMutation.mutate(row.id)}>
                  <RefreshCw size={14} /> 同步
                </button>
                <button className="btn btn-ghost btn-sm" onClick={() => setOpenDialog({ mode: 'edit', row })}>
                  <Pencil size={14} /> 编辑
                </button>
                <button className="btn btn-danger-ghost btn-sm" onClick={() => removeMutation.mutate(row.id)}>
                  <Trash2 size={14} /> 删除
                </button>
              </div>
            </div>
          );
        })}
      </div>

      <Section bodyClass="p-0">
      <div className="hidden overflow-x-auto md:block">
        <table className="data-table min-w-[1280px]">
          <thead>
            <tr>
              <th>名称</th>
              <th>提供方</th>
              <th>Base URL</th>
              <th>API Key</th>
              <th>支持模型</th>
              <th>代理</th>
              <th>状态</th>
              <th>最近测试</th>
              <th>备注</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {listQuery.isLoading && (
              <tr>
                <td colSpan={10} className="py-10 text-center text-small text-text-tertiary">
                  加载中...
                </td>
              </tr>
            )}

            {!listQuery.isLoading && items.length === 0 && (
              <tr>
                <td colSpan={10}>
                  <div className="empty-state">
                    <p className="empty-state-title">暂无上游 API</p>
                    <p className="empty-state-desc">
                      新增一个 <code className="kbd mx-1">pic2api</code> 上游后，即可同步模型用于文生图、图生图和图片编辑。
                    </p>
                  </div>
                </td>
              </tr>
            )}

            {items.map((row) => {
              const enabled = row.status === 1;
              const testState = getTestState(row.last_test_status);
              const TestIcon = testState.icon;
              const boundProxy = row.proxy_id ? proxyMap.get(row.proxy_id) : null;
              const syncing = syncModelsMutation.isPending && syncModelsMutation.variables === row.id;
              const testing = testMutation.isPending && testMutation.variables === row.id;

              return (
                <tr key={row.id}>
                  <td className="font-medium text-text-primary">
                    {row.name}
                    <span className="mt-0.5 block text-small text-text-tertiary">
                      {row.default_model || '未设置默认模型'}
                    </span>
                  </td>
                  <td>
                    <span className="badge">{row.provider.toUpperCase()}</span>
                  </td>
                  <td className="font-mono text-small text-text-secondary">{row.base_url || '默认'}</td>
                  <td className="font-mono text-small text-text-secondary">{row.credential_mask || '已隐藏'}</td>
                  <td
                    className="max-w-[260px] text-small text-text-secondary"
                    title={row.supported_models?.join(', ') || '未同步'}
                  >
                    {renderModels(row.supported_models)}
                  </td>
                  <td className="text-small text-text-secondary">
                    {boundProxy ? `${boundProxy.name} (${boundProxy.host}:${boundProxy.port})` : '未绑定'}
                  </td>
                  <td>
                    {enabled ? <span className="badge badge-success">启用</span> : <span className="badge">禁用</span>}
                  </td>
                  <td className="text-small">
                    <div className={`inline-flex items-center gap-1 ${testState.cls}`}>
                      <TestIcon size={12} />
                      <span>
                        {testState.label}
                        {row.last_test_latency_ms ? ` / ${row.last_test_latency_ms}ms` : ''}
                      </span>
                    </div>
                    {row.last_test_at && (
                      <span className="mt-0.5 block text-tiny text-text-tertiary" title={fmtTime(row.last_test_at)}>
                        {fmtRelative(row.last_test_at)}
                      </span>
                    )}
                    {row.last_test_error && (
                      <span className="mt-0.5 block max-w-[220px] truncate text-tiny text-danger" title={row.last_test_error}>
                        {row.last_test_error}
                      </span>
                    )}
                  </td>
                  <td className="max-w-[180px] truncate text-small text-text-secondary" title={row.remark || ''}>
                    {row.remark || '-'}
                  </td>
                  <td>
                    <div className="inline-flex gap-1">
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title="测试连通并同步模型"
                        onClick={() => testMutation.mutate(row.id)}
                        disabled={testing}
                      >
                        <Activity
                          size={14}
                          className={testing ? 'animate-pulse text-klein-500' : 'text-text-secondary'}
                        />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title="同步模型"
                        onClick={() => syncModelsMutation.mutate(row.id)}
                        disabled={syncing}
                      >
                        <RefreshCw
                          size={14}
                          className={syncing ? 'animate-spin text-klein-500' : 'text-text-secondary'}
                        />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title="编辑"
                        onClick={() => setOpenDialog({ mode: 'edit', row })}
                      >
                        <Pencil size={14} />
                      </button>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title={enabled ? '禁用' : '启用'}
                        onClick={() => toggleMutation.mutate({ id: row.id, status: enabled ? 0 : 1 })}
                      >
                        <Power size={14} className={enabled ? 'text-success' : 'text-text-tertiary'} />
                      </button>
                      <button
                        className="btn btn-danger-ghost btn-icon btn-sm"
                        title="删除"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除上游 API',
                            description: (
                              <>
                                确认删除上游 <span className="font-mono text-text-primary">{row.name}</span>？删除后无法用于网关请求路由。
                              </>
                            ),
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) removeMutation.mutate(row.id);
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
        page={safePage}
        pageSize={pageSize}
        onChange={setPage}
        onPageSizeChange={setPageSize}
        sizeOptions={sizeOptions}
      />
      </Section>

      {openDialog && (
        <UpstreamDialog
          mode={openDialog.mode}
          row={openDialog.mode === 'edit' ? openDialog.row : undefined}
          proxies={proxiesQuery.data?.list ?? []}
          onClose={() => setOpenDialog(null)}
          onSuccess={() => {
            setOpenDialog(null);
            refresh();
          }}
        />
      )}
      {confirmDialog}
    </PageShell>
  );
}

function UpstreamDialog({
  mode,
  row,
  proxies,
  onClose,
  onSuccess,
}: {
  mode: 'create' | 'edit';
  row?: AccountItem;
  proxies: ProxyItem[];
  onClose: () => void;
  onSuccess: () => void;
}) {
  const [body, setBody] = useState<AccountCreateBody>(() => ({
    provider: row?.provider === 'grok' ? 'grok' : row?.provider === 'pic2api' ? 'pic2api' : 'gpt',
    name: row?.name || '',
    auth_type: 'api_key',
    credential: '',
    base_url: row?.base_url || '',
    proxy_id: row?.proxy_id,
    weight: row?.weight || 10,
    remark: row?.remark || '',
  }));

  const secretsQuery = useQuery({
    queryKey: ['admin', 'upstreams', row?.id, 'secrets'],
    queryFn: () => accountsApi.secrets(row!.id),
    enabled: mode === 'edit' && !!row,
    staleTime: 0,
    gcTime: 0,
  });

  useEffect(() => {
    if (mode !== 'edit' || !secretsQuery.data) return;
    setBody((prev) => ({
      ...prev,
      credential: secretsQuery.data?.credential || '',
    }));
  }, [mode, secretsQuery.data]);

  const createMutation = useMutation({
    mutationFn: async (payload: AccountCreateBody) => {
      const created = await accountsApi.create(payload);
      try {
        await accountsApi.syncModels(created.id);
      } catch {
        // best effort
      }
      return created;
    },
    onSuccess: () => {
      toast.success('上游已新增，并已尝试同步模型');
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const updateMutation = useMutation({
    mutationFn: async (payload: AccountUpdateBody) => {
      await accountsApi.update(row!.id, payload);
      try {
        await accountsApi.syncModels(row!.id);
      } catch {
        // best effort
      }
    },
    onSuccess: () => {
      toast.success('上游已更新，并已尝试同步模型');
      onSuccess();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!body.name?.trim()) {
      toast.error('请填写上游名称');
      return;
    }
    if (!body.credential?.trim()) {
      toast.error('请填写 API Key');
      return;
    }

    const payload: AccountCreateBody = {
      provider: body.provider,
      name: body.name.trim(),
      auth_type: 'api_key',
      credential: body.credential.trim(),
      base_url: normalizeBaseURL(body.base_url),
      proxy_id: body.proxy_id || undefined,
      weight: Number(body.weight) || 10,
      remark: body.remark?.trim() || undefined,
    };

    if (mode === 'create') {
      createMutation.mutate(payload);
      return;
    }

    const patch: AccountUpdateBody = {
      name: payload.name,
      credential: payload.credential,
      base_url: payload.base_url,
      proxy_id: payload.proxy_id,
      weight: payload.weight,
      remark: payload.remark,
    };
    updateMutation.mutate(patch);
  };

  const submitting = createMutation.isPending || updateMutation.isPending;

  return (
    <Modal title={mode === 'create' ? '新增上游 API' : '编辑上游 API'} onClose={onClose}>
      <form className="space-y-3" onSubmit={submit}>
        <div className="grid grid-cols-2 gap-3">
          <Field label="名称">
            <input
              className="input"
              placeholder="例如：Pic2API 图片上游"
              value={body.name}
              onChange={(e) => setBody((prev) => ({ ...prev, name: e.target.value }))}
            />
          </Field>
          <Field label="提供方">
            <select
              className="select"
              value={body.provider}
              onChange={(e) =>
                setBody((prev) => ({ ...prev, provider: e.target.value as 'gpt' | 'grok' | 'pic2api' }))
              }
              disabled={mode === 'edit'}
            >
              <option value="gpt">GPT / OpenAI 兼容</option>
              <option value="grok">Grok 兼容</option>
              <option value="pic2api">Pic2API 图片兼容</option>
            </select>
          </Field>
        </div>

        <Field label="Base URL" hint="留空表示使用该提供方默认地址。">
          <input
            className="input font-mono text-small"
            placeholder="https://pic2api.com"
            value={body.base_url || ''}
            onChange={(e) => setBody((prev) => ({ ...prev, base_url: e.target.value }))}
          />
        </Field>

        <Field
          label="API Key"
          hint={mode === 'edit' && secretsQuery.isLoading ? '正在读取当前密钥...' : '保存后会自动尝试同步该上游支持的模型。'}
        >
          <textarea
            className="textarea min-h-[96px] font-mono text-small"
            placeholder="sk-..."
            value={body.credential || ''}
            onChange={(e) => setBody((prev) => ({ ...prev, credential: e.target.value }))}
          />
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="代理">
            <select
              className="select"
              value={body.proxy_id ?? ''}
              onChange={(e) =>
                setBody((prev) => ({
                  ...prev,
                  proxy_id: e.target.value ? Number(e.target.value) : undefined,
                }))
              }
            >
              <option value="">未绑定</option>
              {proxies.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name} ({item.host}:{item.port})
                </option>
              ))}
            </select>
          </Field>
          <Field label="权重">
            <input
              type="number"
              min={1}
              className="input"
              value={body.weight ?? 10}
              onChange={(e) => setBody((prev) => ({ ...prev, weight: Number(e.target.value) || 10 }))}
            />
          </Field>
        </div>

        <Field label="备注">
          <input
            className="input"
            placeholder="可选备注"
            value={body.remark || ''}
            onChange={(e) => setBody((prev) => ({ ...prev, remark: e.target.value }))}
          />
        </Field>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={submitting}>
            {submitting ? '保存中...' : '保存'}
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
      <div className="dialog-surface klein-fade-in w-full max-w-2xl">
        <header className="flex h-12 items-center justify-between border-b border-border px-5">
          <h3 className="font-semibold text-text-primary">{title}</h3>
          <button className="btn btn-ghost btn-icon btn-sm" onClick={onClose} aria-label="关闭">
            x
          </button>
        </header>
        <div className="max-h-[80vh] overflow-y-auto p-5">{children}</div>
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
