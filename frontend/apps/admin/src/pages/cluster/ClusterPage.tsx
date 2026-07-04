import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  Copy,
  KeyRound,
  Pencil,
  Plus,
  Power,
  RefreshCw,
  ShieldOff,
  Trash2,
  Wrench,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';

import { ApiError } from '../../lib/api';
import { clusterApi } from '../../lib/services';
import type {
  ClusterNodeItem,
  ClusterNodeStatus,
  ClusterNodeUpsertBody,
  ClusterNodeUpsertResp,
} from '../../lib/services';
import { toast } from '../../stores/toast';
import { useConfirm } from '../../components/ConfirmDialog';
import { PageHeader, PageShell, Section, Toolbar } from '../../components/layout/PageShell';

const STATUS_COLORS: Record<number, string> = {
  0: 'badge',
  1: 'badge badge-success',
  2: 'badge',
  3: 'badge badge-warning',
  9: 'badge badge-danger',
};

const PROVIDER_CHOICES = ['gpt', 'grok', 'adobe', 'pic2api'];

export default function ClusterPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [openDlg, setOpenDlg] = useState<
    | { mode: 'create' }
    | { mode: 'edit'; row: ClusterNodeItem }
    | null
  >(null);
  const [bootstrapResp, setBootstrapResp] = useState<{
    nodeID: string;
    token: string;
    controlURL?: string;
  } | null>(null);

  const list = useQuery({
    queryKey: ['admin', 'cluster', 'nodes', keyword],
    queryFn: () => clusterApi.list({ keyword: keyword || undefined, page: 1, page_size: 100 }),
    refetchInterval: 5000, // 节点心跳每 ~10s 一次，前端 5s 刷新，体感最迟 5s 状态可见
  });

  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'cluster'] });

  const setStatus = useMutation({
    mutationFn: ({ id, status }: { id: string; status: ClusterNodeStatus }) =>
      clusterApi.setStatus(id, status),
    onSuccess: () => {
      refresh();
      toast.success('状态已更新');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const revoke = useMutation({
    mutationFn: (id: string) => clusterApi.revoke(id),
    onSuccess: () => {
      refresh();
      toast.success('节点已吊销');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const remove = useMutation({
    mutationFn: (id: string) => clusterApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('节点已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const reissue = useMutation({
    mutationFn: (id: string) => clusterApi.reissueBootstrap(id),
    onSuccess: (r) =>
      setBootstrapResp({ nodeID: r.node_id, token: r.bootstrap_token, controlURL: r.control_url }),
    onError: (e: ApiError) => toast.error(e.message),
  });

  const items = list.data?.list ?? [];

  return (
    <PageShell>
      <PageHeader
        icon={<Activity size={16} />}
        title="集群节点"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={refresh}>
              <RefreshCw size={14} /> 刷新
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => setOpenDlg({ mode: 'create' })}>
              <Plus size={14} /> 添加节点
            </button>
          </>
        }
      />

      <Toolbar>
        <input
          className="input input-sm flex-1 min-w-[220px]"
          placeholder="搜索节点 ID / 名称"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
        />
        <span className="ml-auto text-tiny text-text-tertiary">
          共 {list.data?.total ?? 0} 个节点 · 列表每 5s 自动刷新
        </span>
      </Toolbar>

      <Section bodyClass="p-0">
        <div className="overflow-x-auto">
          <table className="data-table min-w-[1100px]">
            <thead>
              <tr>
                <th>节点</th>
                <th>角色</th>
                <th>状态</th>
                <th>Provider 范围</th>
                <th className="text-right">权重 / 并发</th>
                <th className="text-right">心跳</th>
                <th className="text-right" title="反向 /healthz 探活的连续失败次数；3 次会被踢到 Maintenance">
                  探活
                </th>
                <th>公网入口</th>
                <th>版本 / IP</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={9} className="py-10 text-center text-small text-text-tertiary">
                    加载中…
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={9}>
                    <div className="empty-state">
                      <p>还没有添加任何节点。</p>
                      <p className="text-tiny text-text-tertiary">
                        点击「添加节点」分配 bootstrap token，再到 agent 机器上启动它即可。
                      </p>
                    </div>
                  </td>
                </tr>
              )}
              {items.map((row) => (
                <NodeRow
                  key={row.node_id}
                  row={row}
                  onEdit={() => setOpenDlg({ mode: 'edit', row })}
                  onSetStatus={(status) => setStatus.mutate({ id: row.node_id, status })}
                  onReissue={() => reissue.mutate(row.node_id)}
                  onRevoke={async () => {
                    const ok = await confirm({
                      title: '吊销节点 secret',
                      description: (
                        <>
                          吊销后该节点 <code className="font-mono">{row.node_id}</code> 立即失联，
                          需要重新走 bootstrap 流程。该操作可逆（重发 token 后重新握手）。
                        </>
                      ),
                      tone: 'danger',
                      confirmLabel: '吊销',
                    });
                    if (ok) revoke.mutate(row.node_id);
                  }}
                  onRemove={async () => {
                    const ok = await confirm({
                      title: '删除节点',
                      description: (
                        <>
                          删除节点 <code className="font-mono">{row.node_id}</code> 并清空它持有的所有
                          download_locator。已存在该节点上的资源后续将无法被路由到。
                          建议先吊销，再删除。
                        </>
                      ),
                      tone: 'danger',
                      confirmLabel: '永久删除',
                    });
                    if (ok) remove.mutate(row.node_id);
                  }}
                />
              ))}
            </tbody>
          </table>
        </div>
      </Section>

      {openDlg && (
        <NodeDialog
          mode={openDlg.mode}
          initial={openDlg.mode === 'edit' ? openDlg.row : undefined}
          onClose={() => setOpenDlg(null)}
          onSaved={(resp) => {
            setOpenDlg(null);
            refresh();
            if (resp.bootstrap_token) {
              setBootstrapResp({
                nodeID: resp.node.node_id,
                token: resp.bootstrap_token,
                controlURL: resp.control_url,
              });
            } else {
              toast.success('节点已保存');
            }
          }}
        />
      )}

      {bootstrapResp && (
        <BootstrapDialog
          nodeID={bootstrapResp.nodeID}
          token={bootstrapResp.token}
          controlURL={bootstrapResp.controlURL}
          onClose={() => setBootstrapResp(null)}
        />
      )}
      {confirmDialog}
    </PageShell>
  );
}

function NodeRow({
  row,
  onEdit,
  onSetStatus,
  onReissue,
  onRevoke,
  onRemove,
}: {
  row: ClusterNodeItem;
  onEdit: () => void;
  onSetStatus: (s: ClusterNodeStatus) => void;
  onReissue: () => void;
  onRevoke: () => void;
  onRemove: () => void;
}) {
  const isControl = row.role === 'control';
  return (
    <tr>
      <td>
        <div className="flex flex-col">
          <span className="font-mono text-small text-text-primary">{row.node_id}</span>
          {row.display_name && <span className="text-tiny text-text-tertiary">{row.display_name}</span>}
        </div>
      </td>
      <td>
        <span className="badge">{row.role}</span>
      </td>
      <td>
        <span className={STATUS_COLORS[row.status] ?? 'badge'}>{row.status_label}</span>
      </td>
      <td>
        <div className="flex flex-wrap gap-1">
          {row.provider_scope.map((p) => (
            <span key={p} className="badge text-tiny">
              {p}
            </span>
          ))}
          {row.download_only && <span className="badge badge-info text-tiny">仅下载</span>}
        </div>
      </td>
      <td className="text-right">
        <span className="font-mono text-small">
          {row.weight} / {row.last_inflight}/{row.max_concurrency}
        </span>
      </td>
      <td className="text-right">
        {row.last_heartbeat_at ? (
          <div className="flex flex-col items-end">
            <span className="text-small text-text-secondary">
              {new Date(row.last_heartbeat_at).toLocaleString('zh-CN', { hour12: false })}
            </span>
            {typeof row.heartbeat_age_sec === 'number' && (
              <span className="text-tiny text-text-tertiary">{row.heartbeat_age_sec}s 前</span>
            )}
          </div>
        ) : (
          <span className="text-tiny text-text-tertiary">—</span>
        )}
      </td>
      <td className="text-right">
        <PingFailCell streak={row.ping_fail_streak ?? 0} isControl={isControl} />
      </td>
      <td className="text-small text-text-secondary">{row.public_host || '—'}</td>
      <td className="text-tiny text-text-tertiary">
        <div>{row.version || '—'}</div>
        <div className="font-mono">{row.last_ip || '—'}</div>
      </td>
      <td>
        <div className="flex flex-wrap gap-1">
          <button className="btn btn-ghost btn-sm" onClick={onEdit} title="编辑元信息">
            <Pencil size={13} />
          </button>
          {!isControl && row.status === 1 && (
            <button
              className="btn btn-ghost btn-sm"
              onClick={() => onSetStatus(2)}
              title="禁用调度"
            >
              <Power size={13} />
            </button>
          )}
          {!isControl && row.status === 2 && (
            <button
              className="btn btn-ghost btn-sm"
              onClick={() => onSetStatus(1)}
              title="重新启用"
            >
              <Power size={13} className="text-success" />
            </button>
          )}
          {!isControl && row.status !== 9 && (
            <button
              className="btn btn-ghost btn-sm"
              onClick={() => onSetStatus(3)}
              title="进入维护（不再领新任务）"
            >
              <Wrench size={13} />
            </button>
          )}
          {!isControl && (
            <button
              className="btn btn-ghost btn-sm"
              onClick={onReissue}
              title="重发 bootstrap token"
            >
              <KeyRound size={13} />
            </button>
          )}
          {!isControl && row.status !== 9 && (
            <button
              className="btn btn-ghost btn-sm"
              onClick={onRevoke}
              title="吊销 secret"
            >
              <ShieldOff size={13} className="text-danger" />
            </button>
          )}
          {!isControl && (
            <button className="btn btn-ghost btn-sm" onClick={onRemove} title="删除节点">
              <Trash2 size={13} className="text-danger" />
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

/**
 * PingFailCell —— 反向探活连续失败计数的单元格展示。
 *
 * 视觉规则：
 *   - control-main 没有公网入口可以 ping，固定显示「—」+ 提示「主控内置」；
 *   - 失败 0：灰色「正常」；
 *   - 失败 1-2：amber 警示，说明已开始抖动；
 *   - 失败 ≥3：理论上节点已被踢到 Maintenance 并清零，但 ListNodes 与 ping 不同步时
 *     仍可能看到 3+，此时同样用红色提示，避免运维误判节点"正常"。
 */
function PingFailCell({ streak, isControl }: { streak: number; isControl: boolean }) {
  if (isControl) {
    return (
      <span className="text-tiny text-text-tertiary" title="主控自带 lease 通道，不走 /healthz 反向探活">
        —
      </span>
    );
  }
  if (streak <= 0) {
    return <span className="text-tiny text-text-tertiary">正常</span>;
  }
  const cls = streak >= 3 ? 'text-danger' : 'text-amber-600';
  return (
    <span className={'text-tiny font-medium ' + cls} title={`连续 ${streak} 次 /healthz 失败；3 次会被自动踢到 Maintenance`}>
      {streak} 次
    </span>
  );
}

function NodeDialog({
  mode,
  initial,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  initial?: ClusterNodeItem;
  onClose: () => void;
  onSaved: (r: ClusterNodeUpsertResp) => void;
}) {
  const [nodeID, setNodeID] = useState(initial?.node_id ?? '');
  const [displayName, setDisplayName] = useState(initial?.display_name ?? '');
  const [publicHost, setPublicHost] = useState(initial?.public_host ?? '');
  const [internalHost, setInternalHost] = useState(initial?.internal_host ?? '');
  const [scope, setScope] = useState<string[]>(
    initial?.provider_scope?.length ? initial.provider_scope : ['gpt', 'grok', 'adobe', 'pic2api'],
  );
  const [weight, setWeight] = useState(initial?.weight ?? 100);
  const [maxConc, setMaxConc] = useState(initial?.max_concurrency ?? 16);
  const [downloadOnly, setDownloadOnly] = useState(!!initial?.download_only);
  const [allowedIPs, setAllowedIPs] = useState(initial?.allowed_ips ?? '');

  const submit = useMutation({
    mutationFn: (body: ClusterNodeUpsertBody) => clusterApi.upsert(body),
    onSuccess: onSaved,
    onError: (e: ApiError) => toast.error(e.message),
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!nodeID.trim()) {
      toast.error('节点 ID 必填');
      return;
    }
    submit.mutate({
      node_id: nodeID.trim(),
      display_name: displayName.trim(),
      public_host: publicHost.trim(),
      internal_host: internalHost.trim() || undefined,
      provider_scope: scope,
      weight,
      max_concurrency: maxConc,
      download_only: downloadOnly,
      allowed_ips: allowedIPs.trim() || undefined,
    });
  };

  const toggleProvider = (p: string) =>
    setScope((prev) => (prev.includes(p) ? prev.filter((x) => x !== p) : [...prev, p]));

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3 sm:px-4">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <form className="dialog-surface relative w-full max-w-lg p-4 sm:p-6" onSubmit={onSubmit}>
        <div className="mb-5 flex items-center justify-between">
          <div>
            <h2 className="text-h3 text-text-primary">
              {mode === 'create' ? '添加节点' : '编辑节点'}
            </h2>
            <p className="mt-1 text-small text-text-tertiary">
              新节点会立即签发 bootstrap token —— <strong className="text-text-primary">仅显示一次</strong>，请立即复制粘贴到 agent 机器的 <code>KLEIN_NODE_TOKEN</code> 环境变量。
            </p>
          </div>
          <button className="btn btn-ghost btn-icon btn-sm" type="button" onClick={onClose}>
            <X size={18} />
          </button>
        </div>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="field sm:col-span-2">
            <span className="field-label">
              节点 ID <span className="text-danger">*</span>
            </span>
            <input
              className="input"
              value={nodeID}
              onChange={(e) => setNodeID(e.target.value)}
              placeholder="例：hk-edge-01"
              disabled={mode === 'edit'}
              maxLength={40}
            />
          </label>
          <label className="field sm:col-span-2">
            <span className="field-label">显示名称</span>
            <input
              className="input"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="例：香港 1 号边缘节点"
            />
          </label>
          <label className="field sm:col-span-2">
            <span className="field-label">公网入口 (https://...)</span>
            <input
              className="input"
              value={publicHost}
              onChange={(e) => setPublicHost(e.target.value)}
              placeholder="https://hk01.cdn.klein.example"
            />
            <span className="field-hint">用户最终下载链接的 base，需要 TLS。</span>
          </label>
          <label className="field sm:col-span-2">
            <span className="field-label">内网地址（可选）</span>
            <input
              className="input"
              value={internalHost}
              onChange={(e) => setInternalHost(e.target.value)}
              placeholder="http://10.0.0.5:18080"
            />
          </label>
          <div className="field sm:col-span-2">
            <span className="field-label">Provider 范围</span>
            <div className="flex flex-wrap gap-2">
              {PROVIDER_CHOICES.map((p) => (
                <label
                  key={p}
                  className={
                    'inline-flex cursor-pointer items-center gap-1.5 rounded-full border px-3 py-1 text-small ' +
                    (scope.includes(p)
                      ? 'border-primary bg-primary-soft text-primary'
                      : 'border-border text-text-secondary')
                  }
                >
                  <input
                    type="checkbox"
                    className="hidden"
                    checked={scope.includes(p)}
                    onChange={() => toggleProvider(p)}
                  />
                  {p}
                </label>
              ))}
            </div>
          </div>
          <label className="field">
            <span className="field-label">权重</span>
            <input
              type="number"
              className="input"
              value={weight}
              min={1}
              max={1000}
              onChange={(e) => setWeight(parseInt(e.target.value || '0', 10))}
            />
          </label>
          <label className="field">
            <span className="field-label">并发上限</span>
            <input
              type="number"
              className="input"
              value={maxConc}
              min={1}
              max={256}
              onChange={(e) => setMaxConc(parseInt(e.target.value || '0', 10))}
            />
          </label>
          <label className="field sm:col-span-2 flex-row items-center gap-2">
            <input
              type="checkbox"
              className="rounded border-border"
              checked={downloadOnly}
              onChange={(e) => setDownloadOnly(e.target.checked)}
            />
            <span className="text-small text-text-secondary">仅作为下载边缘（不参与 lease）</span>
          </label>
          <label className="field sm:col-span-2">
            <span className="field-label">允许出口 IP（CIDR，逗号分隔，可选）</span>
            <input
              className="input"
              value={allowedIPs}
              onChange={(e) => setAllowedIPs(e.target.value)}
              placeholder="203.0.113.10/32,2001:db8::/32"
            />
          </label>
        </div>
        <div className="mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <button className="btn btn-outline btn-md" type="button" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary btn-md" type="submit" disabled={submit.isPending}>
            {submit.isPending ? '保存中...' : mode === 'create' ? '保存并签发 token' : '保存'}
          </button>
        </div>
      </form>
    </div>
  );
}

function BootstrapDialog({
  nodeID,
  token,
  controlURL,
  onClose,
}: {
  nodeID: string;
  token: string;
  controlURL?: string;
  onClose: () => void;
}) {
  const env = useMemo(() => {
    const lines = [
      `KLEIN_NODE_ID=${nodeID}`,
      `KLEIN_NODE_TOKEN=${token}`,
    ];
    if (controlURL) lines.push(`KLEIN_CONTROL_URL=${controlURL}`);
    return lines.join('\n');
  }, [nodeID, token, controlURL]);

  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3 sm:px-4">
      <div className="absolute inset-0" />
      <div className="dialog-surface relative w-full max-w-lg p-4 sm:p-6">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-h3 text-text-primary">节点 bootstrap token</h2>
          <button className="btn btn-ghost btn-icon btn-sm" type="button" onClick={onClose}>
            <X size={18} />
          </button>
        </div>
        <p className="text-small text-text-tertiary">
          这是节点 <code className="font-mono text-text-primary">{nodeID}</code> 的 bootstrap token，
          仅展示<strong className="text-danger"> 一次</strong>。把下面的环境变量原样写到 agent 机器的{' '}
          <code className="font-mono">.env.agent</code>，然后启动 agent 即可完成首次握手。
        </p>
        <div className="mt-4 rounded-lg bg-surface-2 p-3">
          <pre className="overflow-x-auto text-tiny text-text-primary">{env}</pre>
        </div>
        <div className="mt-3 flex justify-end gap-2">
          <button
            className="btn btn-outline btn-sm"
            onClick={() => {
              void navigator.clipboard.writeText(env).then(() => setCopied(true));
            }}
          >
            <Copy size={14} /> {copied ? '已复制' : '复制全部'}
          </button>
          <button className="btn btn-primary btn-sm" onClick={onClose}>
            我已保存
          </button>
        </div>
      </div>
    </div>
  );
}
