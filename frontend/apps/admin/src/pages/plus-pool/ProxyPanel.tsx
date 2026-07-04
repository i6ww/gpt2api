import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Globe2, Pencil, Plus, RefreshCw, Trash2, Upload, Zap } from 'lucide-react';
import { type FormEvent, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { paymentProxyApi } from '../../lib/services';
import type {
  PaymentProxyCreateBody,
  PaymentProxyItem,
  PaymentProxyScheme,
  PaymentProxyStatus,
  PaymentProxyUpdateBody,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, ImportDialogShell, fmtMs } from '../pools/_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const STATUS_OPTIONS: { value: '' | PaymentProxyStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '可用' },
  { value: 'banned', label: '封禁' },
  { value: 'disabled', label: '停用' },
];

function statusBadge(status: string) {
  switch (status) {
    case 'active':
      return { label: '可用', tone: 'bg-success-soft text-success' };
    case 'banned':
      return { label: '封禁', tone: 'bg-danger-soft text-danger' };
    case 'disabled':
      return { label: '停用', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

export default function ProxyPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | PaymentProxyStatus>('');
  const [country, setCountry] = useState('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [editing, setEditing] = useState<PaymentProxyItem | null>(null);
  const [creating, setCreating] = useState(false);
  const [openImport, setOpenImport] = useState(false);
  useEffect(() => setPage(1), [pageSize, status, country]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      country: country || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, status, country, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'payment-proxy', 'list', query],
    queryFn: () => paymentProxyApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'payment-proxy', 'stats'],
    queryFn: () => paymentProxyApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'payment-proxy'] });

  const create = useMutation({
    mutationFn: (body: PaymentProxyCreateBody) => paymentProxyApi.create(body),
    onSuccess: () => {
      refresh();
      setCreating(false);
      toast.success('代理已添加');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: PaymentProxyUpdateBody }) =>
      paymentProxyApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const removeOne = useMutation({
    mutationFn: (id: number) => paymentProxyApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => paymentProxyApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 个`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const test = useMutation({
    mutationFn: (id: number) => paymentProxyApi.test(id),
    onSuccess: (r, id) => {
      refresh();
      if (r.ok) toast.success(`#${id} 通：${r.ip || '?'} (${r.country || '?'}) ${r.latency_ms}ms`);
      else toast.error(`#${id} 不通：${r.error || '未知错误'}`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const items = list.data?.list || [];
  const total = list.data?.total || 0;
  const allChecked = items.length > 0 && items.every((it) => selected.has(it.id));
  const indeterminate = !allChecked && items.some((it) => selected.has(it.id));

  function toggleAll() {
    const next = new Set(selected);
    if (allChecked) items.forEach((it) => next.delete(it.id));
    else items.forEach((it) => next.add(it.id));
    setSelected(next);
  }
  function toggleOne(id: number) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelected(next);
  }

  async function onBatchDelete() {
    if (selected.size === 0) {
      toast.info('请先勾选要删除的代理');
      return;
    }
    const ok = await confirm({
      title: `确认删除 ${selected.size} 个代理？`,
      description: '该操作不可撤销，删除后该代理不再参与 GoPay 任务调度。',
      tone: 'danger',
      confirmLabel: '删除',
    });
    if (ok) batchDelete.mutate(Array.from(selected));
  }

  return (
    <div className="space-y-3">
      <StatRow cols={4}>
        <Stat label="总计" value={stats.data?.total ?? 0} />
        <Stat label="可用" value={stats.data?.active ?? 0} tone="text-success" />
        <Stat label="封禁" value={stats.data?.banned ?? 0} tone="text-danger" />
        <Stat label="停用" value={stats.data?.disabled ?? 0} tone="text-text-tertiary" />
      </StatRow>

      <Toolbar>
        <input
          type="text"
          className="input input-sm w-44"
          placeholder="搜索 host / 名称"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && setPage(1)}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => setStatus(e.target.value as '' | PaymentProxyStatus)}
        >
          {STATUS_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <input
          type="text"
          className="input input-sm w-20"
          placeholder="国家"
          value={country}
          onChange={(e) => setCountry(e.target.value)}
        />

        <ToolbarSpacer />

        <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
          <RefreshCw size={14} />
          刷新
        </button>
        <button className="btn btn-outline btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} />
          批量导入
        </button>
        <button
          className="btn btn-outline btn-sm text-danger"
          disabled={selected.size === 0}
          onClick={onBatchDelete}
        >
          <Trash2 size={14} />
          删除选中 {selected.size > 0 && `(${selected.size})`}
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
          <Plus size={14} />
          新增代理
        </button>
      </Toolbar>

      <Section
        title={
          <span className="inline-flex items-center gap-1.5">
            <Globe2 size={14} className="text-klein-500" />
            印尼支付代理池
          </span>
        }
      >
        <div className="overflow-x-auto">
          <table className="data-table w-full text-small">
            <thead>
              <tr>
                <th className="w-8">
                  <input
                    type="checkbox"
                    checked={allChecked}
                    ref={(el) => {
                      if (el) el.indeterminate = indeterminate;
                    }}
                    onChange={toggleAll}
                  />
                </th>
                <th>名称</th>
                <th>地址</th>
                <th>国家</th>
                <th>状态</th>
                <th>使用 / 失败</th>
                <th>上次检测</th>
                <th className="w-40 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={8} className="py-6 text-center text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={8} className="py-6 text-center text-text-tertiary">
                    暂无代理。GoPay 阶段必须用印尼住宅 IP，否则风控很高。
                  </td>
                </tr>
              )}
              {items.map((it) => {
                const sb = statusBadge(it.status);
                const addr = it.api_url
                  ? `(动态 API) ${it.api_url.slice(0, 40)}${it.api_url.length > 40 ? '…' : ''}`
                  : `${it.scheme}://${it.username ? it.username + '@' : ''}${it.host}:${it.port}`;
                return (
                  <tr key={it.id}>
                    <td>
                      <input
                        type="checkbox"
                        checked={selected.has(it.id)}
                        onChange={() => toggleOne(it.id)}
                      />
                    </td>
                    <td>{it.name || <span className="text-text-tertiary">—</span>}</td>
                    <td className="font-mono text-tiny">{addr}</td>
                    <td className="text-tiny">{it.country}</td>
                    <td>
                      <Badge label={sb.label} tone={sb.tone} />
                    </td>
                    <td className="tabular-nums text-tiny text-text-tertiary">
                      {it.total_used} / {it.total_failed}
                    </td>
                    <td className="text-tiny text-text-tertiary">
                      {fmtMs(it.last_check_at && it.last_check_at * 1000)}
                      {it.last_check_ms > 0 && (
                        <span className="ml-1">{it.last_check_ms}ms</span>
                      )}
                    </td>
                    <td className="text-right">
                      <button
                        className="btn btn-ghost btn-xs"
                        onClick={() => test.mutate(it.id)}
                        disabled={test.isPending}
                      >
                        <Zap size={12} />
                        测试
                      </button>
                      <button className="btn btn-ghost btn-xs" onClick={() => setEditing(it)}>
                        <Pencil size={12} />
                        编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: `删除代理 #${it.id}？`,
                            description: '该操作不可撤销。',
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) removeOne.mutate(it.id);
                        }}
                      >
                        <Trash2 size={12} />
                        删除
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>

        <div className="mt-3">
          <Pager
            total={total}
            page={page}
            pageSize={pageSize}
            onChange={setPage}
            onPageSizeChange={setPageSize}
            sizeOptions={sizeOptions}
          />
        </div>
      </Section>

      {(creating || editing) && (
        <ProxyEditDialog
          item={editing}
          busy={create.isPending || update.isPending}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSubmit={(body) => {
            if (editing) update.mutate({ id: editing.id, body });
            else create.mutate(body as PaymentProxyCreateBody);
          }}
        />
      )}

      {openImport && (
        <ProxyImportDialog
          onClose={() => setOpenImport(false)}
          onDone={() => {
            setOpenImport(false);
            refresh();
          }}
        />
      )}

      {confirmDialog}
    </div>
  );
}

// ─── 编辑/新增 ───

function ProxyEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: PaymentProxyItem | null;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: PaymentProxyCreateBody | PaymentProxyUpdateBody) => void;
}) {
  const isEdit = !!item;
  const [name, setName] = useState(item?.name || '');
  const [scheme, setScheme] = useState<PaymentProxyScheme>((item?.scheme as PaymentProxyScheme) || 'http');
  const [host, setHost] = useState(item?.host || '');
  const [port, setPort] = useState<number>(item?.port || 0);
  const [username, setUsername] = useState(item?.username || '');
  const [password, setPassword] = useState('');
  const [apiURL, setApiURL] = useState(item?.api_url || '');
  const [country, setCountry] = useState(item?.country || 'ID');
  const [status, setStatus] = useState<PaymentProxyStatus>((item?.status as PaymentProxyStatus) || 'active');
  const [remark, setRemark] = useState(item?.remark || '');

  const dynamic = !!apiURL.trim();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!dynamic && (!host.trim() || !port)) {
      toast.error('请填写 host:port，或填写动态 api_url');
      return;
    }
    if (isEdit) {
      const body: PaymentProxyUpdateBody = {
        name: name || undefined,
        scheme,
        host: host || undefined,
        port: port || undefined,
        username: username || undefined,
        password: password || undefined,
        api_url: apiURL || undefined,
        country: country || undefined,
        status,
        remark: remark || undefined,
      };
      onSubmit(body);
    } else {
      const body: PaymentProxyCreateBody = {
        name: name || undefined,
        scheme,
        host: host || undefined,
        port: port || undefined,
        username: username || undefined,
        password: password || undefined,
        api_url: apiURL || undefined,
        country: country || 'ID',
        remark: remark || undefined,
      };
      onSubmit(body);
    }
  }

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <form
        className="dialog-surface relative w-full max-w-xl space-y-3 p-4 sm:p-6"
        onSubmit={handleSubmit}
      >
        <header>
          <h3 className="text-h4 text-text-primary">{isEdit ? '编辑代理' : '新增印尼支付代理'}</h3>
          <p className="mt-1 text-tiny text-text-tertiary">
            两种模式选一：① 静态 host:port (+ user/pass) ② 动态 api_url（每次取代理 GET 一次返回 host:port:user:pass）
          </p>
        </header>

        <div className="grid grid-cols-3 gap-3">
          <label className="col-span-2 space-y-1">
            <span className="text-tiny text-text-secondary">名称（备注）</span>
            <input className="input w-full" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">国家</span>
            <input className="input w-full" value={country} onChange={(e) => setCountry(e.target.value)} placeholder="ID" />
          </label>

          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">协议</span>
            <select
              className="select w-full"
              value={scheme}
              onChange={(e) => setScheme(e.target.value as PaymentProxyScheme)}
            >
              <option value="http">http</option>
              <option value="https">https</option>
              <option value="socks5">socks5</option>
              <option value="socks5h">socks5h</option>
            </select>
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">Host</span>
            <input
              className="input w-full font-mono text-tiny"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              disabled={dynamic}
            />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">Port</span>
            <input
              type="number"
              className="input w-full"
              value={port || ''}
              onChange={(e) => setPort(Number(e.target.value) || 0)}
              disabled={dynamic}
            />
          </label>
          <label className="space-y-1">
            <span className="text-tiny text-text-secondary">用户名</span>
            <input
              className="input w-full"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              disabled={dynamic}
            />
          </label>
          <label className="col-span-2 space-y-1">
            <span className="text-tiny text-text-secondary">
              密码{isEdit && <span className="ml-2 text-text-tertiary">（留空=不动）</span>}
            </span>
            <input
              type="password"
              className="input w-full"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="off"
              disabled={dynamic}
            />
          </label>

          <label className="col-span-3 space-y-1">
            <span className="text-tiny text-text-secondary">动态 API URL（可选；填了就走动态模式）</span>
            <input
              className="input w-full font-mono text-tiny"
              value={apiURL}
              onChange={(e) => setApiURL(e.target.value)}
              placeholder="https://provider.com/api/get?country=ID&format=line"
            />
          </label>

          {isEdit && (
            <label className="space-y-1">
              <span className="text-tiny text-text-secondary">状态</span>
              <select
                className="select w-full"
                value={status}
                onChange={(e) => setStatus(e.target.value as PaymentProxyStatus)}
              >
                <option value="active">可用</option>
                <option value="banned">封禁</option>
                <option value="disabled">停用</option>
              </select>
            </label>
          )}
          <label className={`${isEdit ? 'col-span-2' : 'col-span-3'} space-y-1`}>
            <span className="text-tiny text-text-secondary">备注</span>
            <input className="input w-full" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </label>
        </div>

        <div className="flex justify-end gap-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </div>
  );
}

// ─── 批量导入 ───

function ProxyImportDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [text, setText] = useState('');
  const [country, setCountry] = useState('ID');
  const [remark, setRemark] = useState('');

  const importMu = useMutation({
    mutationFn: () => paymentProxyApi.import({ text, country, remark }),
    onSuccess: (r) => {
      toast.success(`新增 ${r.imported} / 跳过 ${r.skipped}`);
      onDone();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <ImportDialogShell
      title="批量导入支付代理"
      description={
        <span>
          一行一条，支持：<code>scheme://user:pass@host:port</code> 或 <code>host:port:user:pass</code>。导入后默认 scheme=http。
        </span>
      }
      busy={importMu.isPending}
      onClose={onClose}
      onConfirm={() => {
        if (!text.trim()) {
          toast.error('请粘贴要导入的内容');
          return;
        }
        importMu.mutate();
      }}
    >
      <div className="grid grid-cols-3 gap-2">
        <label className="space-y-1">
          <span className="text-tiny text-text-secondary">国家代码</span>
          <input className="input w-full" value={country} onChange={(e) => setCountry(e.target.value)} />
        </label>
        <label className="col-span-2 space-y-1">
          <span className="text-tiny text-text-secondary">备注</span>
          <input className="input w-full" value={remark} onChange={(e) => setRemark(e.target.value)} placeholder="例如「indo-premium-2026」" />
        </label>
      </div>
      <textarea
        className="textarea w-full font-mono text-tiny"
        rows={10}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={`http://user:pass@1.2.3.4:8080\nhttp://1.2.3.5:8080\n1.2.3.6:8080:user:pass`}
      />
    </ImportDialogShell>
  );
}
