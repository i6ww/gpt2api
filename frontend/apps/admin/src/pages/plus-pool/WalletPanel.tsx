import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Eye, EyeOff, Pencil, Plus, RefreshCw, Trash2, Upload, Wallet, X } from 'lucide-react';
import { type FormEvent, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { cloudPhoneApi, gopayWalletApi } from '../../lib/services';
import type {
  GopayBindingItem,
  GopayBindingStatus,
  GopayWalletCreateBody,
  GopayWalletItem,
  GopayWalletStatus,
  GopayWalletUpdateBody,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, ImportDialogShell, fmtMs } from '../pools/_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

const WALLET_STATUS_OPTIONS: { value: '' | GopayWalletStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'available', label: '可用' },
  { value: 'leased', label: '使用中' },
  { value: 'cooldown', label: '冷却' },
  { value: 'exhausted', label: '已满额' },
  { value: 'banned', label: '封禁' },
  { value: 'disabled', label: '停用' },
];

function walletStatusBadge(status: string) {
  switch (status) {
    case 'available':
      return { label: '可用', tone: 'bg-success-soft text-success' };
    case 'leased':
      return { label: '使用中', tone: 'bg-info-soft text-klein-500' };
    case 'cooldown':
      return { label: '冷却', tone: 'bg-warn-soft text-warn' };
    case 'exhausted':
      return { label: '已满额', tone: 'bg-warn-soft text-warn' };
    case 'banned':
      return { label: '封禁', tone: 'bg-danger-soft text-danger' };
    case 'disabled':
      return { label: '停用', tone: 'bg-surface-2 text-text-tertiary' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

const SUB_TABS = [
  { id: 'wallets' as const, label: '钱包池' },
  { id: 'bindings' as const, label: 'Plus 绑定记录' },
];

export default function WalletPanel() {
  const [sub, setSub] = useState<'wallets' | 'bindings'>('wallets');

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        {SUB_TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            className={`btn btn-sm ${sub === t.id ? 'btn-primary' : 'btn-outline'}`}
            onClick={() => setSub(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {sub === 'wallets' ? <WalletList /> : <BindingList />}
    </div>
  );
}

// ──────────────────────────────────────────────
// 钱包池
// ──────────────────────────────────────────────

function WalletList() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<'' | GopayWalletStatus>('');
  const [cloudPhoneID, setCloudPhoneID] = useState('');
  const [hasAvailableOn, setHasAvailableOn] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [editing, setEditing] = useState<GopayWalletItem | null>(null);
  const [creating, setCreating] = useState(false);
  const [openImport, setOpenImport] = useState(false);
  useEffect(() => setPage(1), [pageSize, status, hasAvailableOn]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      cloud_phone_id: cloudPhoneID || undefined,
      has_available_on: hasAvailableOn || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, status, cloudPhoneID, hasAvailableOn, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'gopay-wallet', 'list', query],
    queryFn: () => gopayWalletApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'gopay-wallet', 'stats'],
    queryFn: () => gopayWalletApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'gopay-wallet'] });

  const create = useMutation({
    mutationFn: (body: GopayWalletCreateBody) => gopayWalletApi.create(body),
    onSuccess: () => {
      refresh();
      setCreating(false);
      toast.success('钱包已添加');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: GopayWalletUpdateBody }) =>
      gopayWalletApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const removeOne = useMutation({
    mutationFn: (id: number) => gopayWalletApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => gopayWalletApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 个`);
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
      toast.info('请先勾选要删除的钱包');
      return;
    }
    const ok = await confirm({
      title: `确认删除 ${selected.size} 个钱包？`,
      description: '该操作只删本地池记录，不会影响已开通的 Plus 订阅。',
      tone: 'danger',
      confirmLabel: '删除',
    });
    if (ok) batchDelete.mutate(Array.from(selected));
  }

  return (
    <div className="space-y-3">
      <StatRow cols={5}>
        <Stat label="总计" value={stats.data?.total ?? 0} />
        <Stat label="可用" value={stats.data?.available ?? 0} tone="text-success" />
        <Stat label="使用中" value={stats.data?.leased ?? 0} tone="text-klein-500" />
        <Stat label="冷却/满额" value={(stats.data?.cooldown ?? 0) + (stats.data?.exhausted ?? 0)} tone="text-warn" />
        <Stat label="封禁/停用" value={(stats.data?.banned ?? 0) + (stats.data?.disabled ?? 0)} tone="text-danger" />
      </StatRow>

      <Toolbar>
        <input
          type="text"
          className="input input-sm w-44"
          placeholder="搜索手机号 / 备注"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && setPage(1)}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => setStatus(e.target.value as '' | GopayWalletStatus)}
        >
          {WALLET_STATUS_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <input
          type="text"
          className="input input-sm w-36"
          placeholder="按云手机 ID 过滤"
          value={cloudPhoneID}
          onChange={(e) => setCloudPhoneID(e.target.value)}
        />
        <label className="inline-flex items-center gap-1.5 text-tiny text-text-secondary">
          <input
            type="checkbox"
            checked={hasAvailableOn}
            onChange={(e) => setHasAvailableOn(e.target.checked)}
          />
          只看还能开 Plus 的
        </label>

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
          新增钱包
        </button>
      </Toolbar>

      <Section
        title={
          <span className="inline-flex items-center gap-1.5">
            <Wallet size={14} className="text-klein-500" />
            GoPay 钱包池
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
                <th>手机号</th>
                <th>状态</th>
                <th>已开 Plus</th>
                <th>成功 / 失败</th>
                <th>云手机</th>
                <th>上次使用</th>
                <th className="w-32 text-right">操作</th>
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
                    暂无钱包，请先添加 GoPay 印尼手机号 + PIN
                  </td>
                </tr>
              )}
              {items.map((it) => {
                const sb = walletStatusBadge(it.status);
                return (
                  <tr key={it.id}>
                    <td>
                      <input
                        type="checkbox"
                        checked={selected.has(it.id)}
                        onChange={() => toggleOne(it.id)}
                      />
                    </td>
                    <td className="font-mono text-tiny">
                      {it.phone_number ? (
                        <>+{it.country_code} {it.phone_masked || it.phone_number}</>
                      ) : (
                        <span className="text-warn">手机号未设置</span>
                      )}
                    </td>
                    <td>
                      <Badge label={sb.label} tone={sb.tone} />
                      {it.cooldown_until && (
                        <div className="mt-0.5 text-tiny text-text-tertiary">
                          冷却至 {fmtMs(it.cooldown_until * 1000)}
                        </div>
                      )}
                    </td>
                    <td className="tabular-nums">{it.active_plus_count}</td>
                    <td className="tabular-nums text-tiny text-text-tertiary">
                      {it.total_success} / {it.total_failed}
                    </td>
                    <td className="font-mono text-tiny">
                      {it.cloud_phone_name ? (
                        <>
                          {it.cloud_phone_name}
                          <div className="text-text-tertiary">{it.cloud_phone_id}</div>
                        </>
                      ) : (
                        it.cloud_phone_id
                      )}
                    </td>
                    <td className="text-tiny text-text-tertiary">{fmtMs(it.last_used_at && it.last_used_at * 1000)}</td>
                    <td className="text-right">
                      <button className="btn btn-ghost btn-xs" onClick={() => setEditing(it)}>
                        <Pencil size={12} />
                        编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: `删除钱包 #${it.id}？`,
                            description: '不影响已开通的 Plus 订阅。',
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
        <WalletEditDialog
          item={editing}
          busy={create.isPending || update.isPending}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
          onSubmit={(body) => {
            if (editing) update.mutate({ id: editing.id, body });
            else create.mutate(body as GopayWalletCreateBody);
          }}
        />
      )}

      {openImport && (
        <WalletImportDialog
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

// ─── 钱包编辑/新增弹窗 ───

function WalletEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: GopayWalletItem | null;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: GopayWalletCreateBody | GopayWalletUpdateBody) => void;
}) {
  const isEdit = !!item;
  const [pin, setPin] = useState('');
  const [pinReveal, setPinReveal] = useState(false);
  const [cloudPhoneID, setCloudPhoneID] = useState(item?.cloud_phone_id || '');
  const [status, setStatus] = useState<GopayWalletStatus>((item?.status as GopayWalletStatus) || 'available');
  const [activePlusCount, setActivePlusCount] = useState(item?.active_plus_count ?? 0);
  const [remark, setRemark] = useState(item?.remark || '');

  // 编辑时点"显示"再去拉明文 PIN，避免列表请求带敏感数据
  const fetchPin = useMutation({
    mutationFn: () => gopayWalletApi.secrets(item!.id),
    onSuccess: (r) => {
      setPin(r.pin);
      setPinReveal(true);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  // 顺手把云手机选项拉一下；展示 "name +国家码手机号 (id)" 便于运维识别
  const cloudPhones = useQuery({
    queryKey: ['admin', 'cloud-phone', 'list-for-wallet'],
    queryFn: () => cloudPhoneApi.list({ page: 1, page_size: 1000 }),
  });
  const selectedPhone = useMemo(
    () => (cloudPhones.data?.list || []).find((p) => p.id === cloudPhoneID),
    [cloudPhones.data, cloudPhoneID],
  );

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (isEdit) {
      const body: GopayWalletUpdateBody = {
        pin: pin || undefined, // 留空=不动
        cloud_phone_id: cloudPhoneID || undefined,
        status,
        active_plus_count: activePlusCount,
        remark: remark || undefined,
      };
      onSubmit(body);
    } else {
      if (!pin.trim() || !cloudPhoneID.trim()) {
        toast.error('PIN 与云手机必填');
        return;
      }
      const body: GopayWalletCreateBody = {
        pin: pin.trim(),
        cloud_phone_id: cloudPhoneID.trim(),
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
          <h3 className="text-h4 text-text-primary">{isEdit ? '编辑钱包' : '新增 GoPay 钱包'}</h3>
          <p className="mt-1 text-tiny text-text-tertiary">
            一台云手机 = 一张 SIM = 一个 GoPay 钱包。手机号在「云手机」面板填写，
            这里只需 PIN 即可。
          </p>
        </header>

        <div className="grid grid-cols-3 gap-3">
          <label className="col-span-3 space-y-1">
            <span className="text-tiny text-text-secondary">关联云手机 *</span>
            <select
              className="select w-full font-mono text-small"
              value={cloudPhoneID}
              onChange={(e) => setCloudPhoneID(e.target.value)}
            >
              <option value="">— 请选择云手机 —</option>
              {(cloudPhones.data?.list || []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || p.id}
                  {p.phone_number ? ` · +${p.country_code} ${p.phone_masked || p.phone_number}` : ' · 未设手机号'}
                  {`  (${p.id})`}
                </option>
              ))}
            </select>
            {selectedPhone && !selectedPhone.phone_number && (
              <div className="mt-1 text-tiny text-warn">
                ⚠ 这台云手机还没填手机号，请先到「云手机」面板补全，否则 GoPay 流程跑不起来
              </div>
            )}
          </label>

          <label className="col-span-3 space-y-1">
            <span className="text-tiny text-text-secondary">
              PIN (6 位){!isEdit && ' *'}
              {isEdit && <span className="ml-2 text-text-tertiary">（留空=不动）</span>}
            </span>
            <div className="flex gap-2">
              <input
                className="input w-full font-mono tracking-widest"
                type={pinReveal ? 'text' : 'password'}
                value={pin}
                onChange={(e) => setPin(e.target.value)}
                placeholder={isEdit ? '点"显示"加载明文' : '••••••'}
                autoComplete="off"
              />
              {isEdit && (
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  disabled={fetchPin.isPending}
                  onClick={() => {
                    if (pinReveal) {
                      setPinReveal(false);
                      setPin('');
                    } else {
                      fetchPin.mutate();
                    }
                  }}
                >
                  {pinReveal ? <EyeOff size={14} /> : <Eye size={14} />}
                  {pinReveal ? '隐藏' : '显示'}
                </button>
              )}
            </div>
          </label>

          {isEdit && (
            <>
              <label className="space-y-1">
                <span className="text-tiny text-text-secondary">状态</span>
                <select
                  className="select w-full"
                  value={status}
                  onChange={(e) => setStatus(e.target.value as GopayWalletStatus)}
                >
                  <option value="available">可用</option>
                  <option value="leased">使用中</option>
                  <option value="cooldown">冷却</option>
                  <option value="exhausted">已满额</option>
                  <option value="banned">封禁</option>
                  <option value="disabled">停用</option>
                </select>
              </label>
              <label className="col-span-2 space-y-1">
                <span className="text-tiny text-text-secondary">已开 Plus 数</span>
                <input
                  type="number"
                  className="input w-full"
                  value={activePlusCount}
                  min={0}
                  onChange={(e) => setActivePlusCount(Number(e.target.value) || 0)}
                />
              </label>
            </>
          )}

          <label className="col-span-3 space-y-1">
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

// ─── 钱包批量导入弹窗 ───

function WalletImportDialog({
  onClose,
  onDone,
}: {
  onClose: () => void;
  onDone: () => void;
}) {
  const [text, setText] = useState('');

  const importMu = useMutation({
    mutationFn: () => gopayWalletApi.import({ text }),
    onSuccess: (r) => {
      toast.success(`新增 ${r.imported} / 跳过 ${r.skipped}`);
      if (r.errors && r.errors.length > 0) {
        toast.info(`部分失败：${r.errors.slice(0, 3).join('；')}`);
      }
      onDone();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <ImportDialogShell
      title="批量导入 GoPay 钱包"
      description={
        <span>
          一行一条，分隔符 <code>|</code>：
          <code>pin|cloud_phone_id[|remark]</code>
          <br />
          手机号信息保存在「云手机」面板，钱包只关联即可。
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
      <textarea
        className="textarea w-full font-mono text-tiny"
        rows={10}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={`123456|g_1111111111|主力钱包\n654321|g_2222222222`}
      />
    </ImportDialogShell>
  );
}

// ──────────────────────────────────────────────
// Plus 绑定记录
// ──────────────────────────────────────────────

const BINDING_STATUS_OPTIONS: { value: '' | GopayBindingStatus; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '生效中' },
  { value: 'cancelled', label: '已取消' },
  { value: 'expired', label: '已过期' },
  { value: 'refunded', label: '已退款' },
];

function bindingStatusBadge(status: string) {
  switch (status) {
    case 'active':
      return { label: '生效中', tone: 'bg-success-soft text-success' };
    case 'cancelled':
      return { label: '已取消', tone: 'bg-surface-2 text-text-tertiary' };
    case 'expired':
      return { label: '已过期', tone: 'bg-warn-soft text-warn' };
    case 'refunded':
      return { label: '已退款', tone: 'bg-danger-soft text-danger' };
    default:
      return { label: status, tone: 'bg-surface-2 text-text-secondary' };
  }
}

function BindingList() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [walletID, setWalletID] = useState('');
  const [accountID, setAccountID] = useState('');
  const [status, setStatus] = useState<'' | GopayBindingStatus>('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize, status, walletID, accountID]);

  const query = useMemo(
    () => ({
      wallet_id: walletID ? Number(walletID) : undefined,
      gpt_account_id: accountID ? Number(accountID) : undefined,
      status: status || undefined,
      page,
      page_size: pageSize,
    }),
    [walletID, accountID, status, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'gopay-binding', 'list', query],
    queryFn: () => gopayWalletApi.listBindings(query),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'gopay-binding'] });

  const cancel = useMutation({
    mutationFn: ({ id, note }: { id: number; note?: string }) => gopayWalletApi.cancelBinding(id, note),
    onSuccess: () => {
      refresh();
      qc.invalidateQueries({ queryKey: ['admin', 'gopay-wallet'] }); // active_plus_count 会变
      toast.success('订阅已取消');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const items = list.data?.list || [];
  const total = list.data?.total || 0;

  return (
    <div className="space-y-3">
      <Toolbar>
        <input
          type="number"
          className="input input-sm w-32"
          placeholder="按 wallet_id"
          value={walletID}
          onChange={(e) => setWalletID(e.target.value)}
        />
        <input
          type="number"
          className="input input-sm w-32"
          placeholder="按 account_id"
          value={accountID}
          onChange={(e) => setAccountID(e.target.value)}
        />
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => setStatus(e.target.value as '' | GopayBindingStatus)}
        >
          {BINDING_STATUS_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>

        <ToolbarSpacer />

        <button className="btn btn-outline btn-sm" onClick={() => refresh()}>
          <RefreshCw size={14} />
          刷新
        </button>
      </Toolbar>

      <Section title="Plus 绑定记录（每次成功开 Plus 都会写一行）">
        <div className="overflow-x-auto">
          <table className="data-table w-full text-small">
            <thead>
              <tr>
                <th>ID</th>
                <th>钱包 ID</th>
                <th>GPT 账号 ID</th>
                <th>状态</th>
                <th>金额 (IDR)</th>
                <th>开通时间</th>
                <th>到期时间</th>
                <th>cs_id</th>
                <th>charge_ref</th>
                <th className="w-24 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={10} className="py-6 text-center text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={10} className="py-6 text-center text-text-tertiary">
                    暂无绑定记录
                  </td>
                </tr>
              )}
              {items.map((it: GopayBindingItem) => {
                const sb = bindingStatusBadge(it.status);
                return (
                  <tr key={it.id}>
                    <td className="tabular-nums">{it.id}</td>
                    <td className="tabular-nums">{it.wallet_id}</td>
                    <td className="tabular-nums">{it.gpt_account_id}</td>
                    <td>
                      <Badge label={sb.label} tone={sb.tone} />
                    </td>
                    <td className="tabular-nums">{it.amount_idr.toLocaleString()}</td>
                    <td className="text-tiny text-text-tertiary">{fmtMs(it.charged_at * 1000)}</td>
                    <td className="text-tiny text-text-tertiary">{fmtMs(it.expires_at * 1000)}</td>
                    <td className="font-mono text-tiny">{it.cs_id ? it.cs_id.slice(0, 16) + '…' : '—'}</td>
                    <td className="font-mono text-tiny">{it.charge_ref || '—'}</td>
                    <td className="text-right">
                      {it.status === 'active' && (
                        <button
                          className="btn btn-ghost btn-xs text-danger"
                          onClick={async () => {
                            const ok = await confirm({
                              title: `取消绑定 #${it.id}？`,
                              description:
                                '⚠️ 注意：此操作仅在本系统标记绑定已取消、释放钱包配额；' +
                                '不会调用 OpenAI / Stripe 取消用户订阅。\n\n' +
                                '该 Plus 订阅会在 ' +
                                fmtMs(it.expires_at * 1000) +
                                ' 自动续费（约 21 万印尼盾 / ≈ 13 USD），' +
                                '如不想被扣款，请在续费日期前手动操作：\n' +
                                '  1. 用账号登录 chatgpt.com\n' +
                                '  2. Settings → Subscription → Manage my subscription\n' +
                                '  3. 在 Stripe 页面点 Cancel plan\n\n' +
                                '确认要在本系统标记为已取消并释放钱包配额吗？',
                              tone: 'danger',
                              confirmLabel: '释放钱包配额',
                            });
                            if (ok) cancel.mutate({ id: it.id });
                          }}
                        >
                          <X size={12} />
                          取消
                        </button>
                      )}
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

      {confirmDialog}
    </div>
  );
}
