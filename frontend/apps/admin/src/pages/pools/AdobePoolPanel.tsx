import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Download, Inbox, Pencil, RefreshCw, Trash2, Upload } from 'lucide-react';
import { type ChangeEvent, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { poolAdobeApi } from '../../lib/services';
import type {
  AdobeEntitlementState,
  AdobePoolEntitlements,
  AdobePoolItem,
  AdobePoolStatus,
  AdobePoolUpdateBody,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

import { Badge, FlagPill, ImportDialogShell, SplitMenu, fmtMs } from './_shared';
import { Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';

type AdobePoolStatusFilter = '' | AdobePoolStatus | 'quota_recovery';

const STATUS_OPTIONS: { value: AdobePoolStatusFilter; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'valid', label: '可用' },
  { value: 'invalid', label: '失效' },
  { value: 'cooldown', label: '冷却' },
  { value: 'disabled', label: '禁用' },
  { value: 'quota_recovery', label: '额度回收中' },
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

// expiryRelative 把 expires_at（毫秒时间戳）格式化成"距过期 X 小时"。
//
// 后台 < 12h 阈值会自动 silent refresh，所以这里只是给运维一个直观感受。
function expiryRelative(expiresAt?: number): string {
  if (!expiresAt) return '未知';
  const diffMs = expiresAt - Date.now();
  if (diffMs <= 0) return '已过期';
  const hours = diffMs / 3600_000;
  if (hours < 1) return `${Math.round(diffMs / 60_000)} 分钟后`;
  if (hours < 24) return `${hours.toFixed(1)} 小时后`;
  return `${(hours / 24).toFixed(1)} 天后`;
}

// tierBadge 渲染单档位（1K/2K/4K）的权益状态徽章。
//
// 颜色含义：
//   - 灰（unknown）：从未撞过 not_entitled，也从未成功跑过此档位 → 默认乐观，会被任务选中试一次
//   - 红（blocked）：最近 7 天内被 NotEntitledError 标记过，调度自动跳过；hover 显示剩余 TTL
//   - 绿（ok）   ：最近 7 天内成功跑通此档位 → 确认账号有效，hover 显示「验证于 X 天前」
//
// 三态都带 7 天 TTL：blocked 过期后允许重新探测（运营可能升级了账号），
// ok 过期后徽章会退回 unknown（生产环境会被任务实际跑成功后立刻刷绿）。
function tierBadge(label: string, state: AdobeEntitlementState | undefined, checkedAt?: number) {
  const dayMs = 24 * 3600 * 1000;
  const ttlMs = 7 * dayMs;
  let tone = 'bg-surface-2 text-text-tertiary border border-border';
  let title = `${label}: 未测试，下次 ${label} 任务会被选中试一次`;
  switch (state) {
    case 'blocked': {
      tone = 'bg-danger-soft text-danger';
      if (checkedAt) {
        const remainMs = checkedAt + ttlMs - Date.now();
        const days = Math.max(0, Math.round(remainMs / dayMs));
        title = `${label}: 已确认无权益，${days} 天后自动重新探测`;
      } else {
        title = `${label}: 已确认无权益（无时间戳，下次会重新探测）`;
      }
      break;
    }
    case 'ok': {
      tone = 'bg-success-soft text-success';
      if (checkedAt) {
        const ageMs = Date.now() - checkedAt;
        if (ageMs < 60 * 60 * 1000) {
          const mins = Math.max(1, Math.round(ageMs / (60 * 1000)));
          title = `${label}: 已确认开通（${mins} 分钟前刚跑通）`;
        } else if (ageMs < dayMs) {
          const hours = Math.max(1, Math.round(ageMs / (60 * 60 * 1000)));
          title = `${label}: 已确认开通（${hours} 小时前刚跑通）`;
        } else {
          const days = Math.max(1, Math.round(ageMs / dayMs));
          title = `${label}: 已确认开通（${days} 天前验证过）`;
        }
      } else {
        title = `${label}: 已确认开通`;
      }
      break;
    }
  }
  return (
    <span
      title={title}
      className={`inline-flex items-center rounded px-1.5 py-0.5 text-tiny font-medium ${tone}`}
    >
      {label}
    </span>
  );
}

// entitlementCell 渲染整行的"4K 权益"列：把 1K/2K/4K 三个徽章并排显示。
// 后台运营靠这一眼判断"这个号能跑 4K 吗"，决定是否要补 Premium 号。
function entitlementCell(ent: AdobePoolEntitlements | null | undefined) {
  return (
    <div className="flex flex-wrap gap-1">
      {tierBadge('1K', ent?.image_1k, ent?.image_1k_checked_at)}
      {tierBadge('2K', ent?.image_2k, ent?.image_2k_checked_at)}
      {tierBadge('4K', ent?.image_4k, ent?.image_4k_checked_at)}
    </div>
  );
}

// expiryTone 根据距过期时间给文字着色：< 1h 红 / < 12h 黄 / 其他灰。
function expiryTone(expiresAt?: number): string {
  if (!expiresAt) return 'text-text-tertiary';
  const diffMs = expiresAt - Date.now();
  if (diffMs <= 0) return 'text-danger';
  if (diffMs < 3600_000) return 'text-danger';
  if (diffMs < 12 * 3600_000) return 'text-warn';
  return 'text-text-tertiary';
}


export default function AdobePoolPanel() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<AdobePoolStatusFilter>('');
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [openImport, setOpenImport] = useState(false);
  const [editing, setEditing] = useState<AdobePoolItem | null>(null);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);

  const query = useMemo(
    () => ({
      keyword: keyword || undefined,
      status: status || undefined,
      page,
      page_size: pageSize,
    }),
    [keyword, page, status],
  );

  const list = useQuery({
    queryKey: ['admin', 'pool-adobe', 'list', query],
    queryFn: () => poolAdobeApi.list(query),
  });
  const stats = useQuery({
    queryKey: ['admin', 'pool-adobe', 'stats'],
    queryFn: () => poolAdobeApi.stats(),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin', 'pool-adobe'] });

  const removeOne = useMutation({
    mutationFn: (id: number) => poolAdobeApi.remove(id),
    onSuccess: () => {
      refresh();
      toast.success('账号已删除');
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const batchDelete = useMutation({
    mutationFn: (ids: number[]) => poolAdobeApi.batchDelete(ids),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已删除 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const refreshOne = useMutation({
    mutationFn: ({ id, onlyCredits }: { id: number; onlyCredits?: boolean }) =>
      poolAdobeApi.refresh(id, !!onlyCredits),
    onSuccess: (r) => {
      refresh();
      toast.success(`已刷新 #${r.id}：积分 ${r.credits.toFixed(2)}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '刷新失败'),
  });
  const refreshAll = useMutation({
    mutationFn: () => poolAdobeApi.refreshAll(),
    onSuccess: (r) => {
      refresh();
      toast.success(`扫描完成：成功 ${r.ok}，失败 ${r.fail}`);
    },
    onError: (e: ApiError) => toast.error(e.message || '扫描失败'),
  });
  const batchRefresh = useMutation({
    mutationFn: (body: { scope: 'all' | 'zero_credits' | 'abnormal' | 'expiring' | 'quota_recovery'; only_credits?: boolean }) =>
      poolAdobeApi.batchRefresh(body),
    onSuccess: (r) => {
      refresh();
      toast.success(`批量刷新完成：${r.ok}/${r.total} 成功，${r.fail} 失败`);
    },
    onError: (e: ApiError) => toast.error(e.message || '批量刷新失败'),
  });
  const purge = useMutation({
    mutationFn: (body: { all?: boolean; status?: string; zero_credits?: boolean; token_expired?: boolean; quota_recovery_days?: number }) =>
      poolAdobeApi.purge(body),
    onSuccess: (r) => {
      refresh();
      setSelected(new Set());
      toast.success(`已清理 ${r.affected} 条`);
    },
    onError: (e: ApiError) => toast.error(e.message || '清理失败'),
  });

  // 通用确认 + 批量删除入口
  async function purgeWithConfirm(
    title: string,
    description: string,
    body: Parameters<typeof purge.mutate>[0],
  ) {
    const ok = await confirm({
      title,
      description,
      tone: 'danger',
      confirmLabel: '清理',
    });
    if (ok) purge.mutate(body);
  }

  const updateOne = useMutation({
    mutationFn: ({ id, body }: { id: number; body: AdobePoolUpdateBody }) =>
      poolAdobeApi.update(id, body),
    onSuccess: () => {
      refresh();
      setEditing(null);
      toast.success('已保存');
    },
    onError: (e: ApiError) => toast.error(e.message || '保存失败'),
  });

  // 把字符串保存为指定 mime 的文件，触发浏览器下载（无 BOM）
  function saveAsFile(text: string, filename: string, mime = 'application/json;charset=utf-8') {
    const blob = new Blob([text], { type: mime });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  const exportAll = useMutation({
    mutationFn: (scope: 'all' | 'valid' | 'invalid') => poolAdobeApi.exportText(scope),
    onSuccess: (r, scope) => {
      const ts = new Date().toISOString().replace(/[:T]/g, '-').slice(0, 19);
      saveAsFile(r.text, `adobe-${scope}-${ts}.json`);
      toast.success(`已导出 ${r.count} 条（含密码 / token / cookie）`);
    },
    onError: (e: ApiError) => toast.error(e.message || '导出失败'),
  });

  // 当前页直接基于已加载 items 拼 JSON，不打后端
  // 注意：list 接口不下发 password / access_token / cookie 明文，
  // 所以"导出本页"只含元数据（email / 状态 / 积分 / 凭证 flags / 时间戳等）。
  // 需要完整凭证请使用"导出全部 / 有效 / 失效"。
  function exportCurrentPage() {
    if (items.length === 0) {
      toast.error('当前页没有数据');
      return;
    }
    const text = JSON.stringify(items, null, 2);
    const ts = new Date().toISOString().replace(/[:T]/g, '-').slice(0, 19);
    saveAsFile(text, `adobe-page-${page}-${ts}.json`);
    toast.success(`已导出本页 ${items.length} 条（不含凭证明文）`);
  }

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
            setStatus(e.target.value as AdobePoolStatusFilter);
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
              label: '刷新 0 积分账号',
              description: '仅 credits=0 的账号，仅拉积分（不换 token）',
              onClick: () => batchRefresh.mutate({ scope: 'zero_credits', only_credits: true }),
            },
            {
              label: '刷新额度回收账号',
              description: 'taste/quota exhausted 冷却号，仅拉积分；恢复后自动拉回可用',
              onClick: () => batchRefresh.mutate({ scope: 'quota_recovery', only_credits: true }),
            },
            {
              label: '刷新异常账号 Token',
              description: 'invalid / cooldown 的账号，silent refresh',
              onClick: () => batchRefresh.mutate({ scope: 'abnormal', only_credits: false }),
            },
            {
              label: '刷新全部账号 Token',
              description: '所有账号一起 silent refresh（耗时）',
              onClick: () => batchRefresh.mutate({ scope: 'all', only_credits: false }),
            },
            {
              label: '刷新异常账号 Token + 积分',
              description: '与「异常 Token」等价，会顺带拉积分',
              onClick: () => batchRefresh.mutate({ scope: 'abnormal', only_credits: false }),
            },
            {
              label: '刷新全部账号 Token + 积分',
              description: '所有账号完整刷新（最慢，最完整）',
              onClick: () => batchRefresh.mutate({ scope: 'all', only_credits: false }),
            },
            {
              label: '仅扫描即将过期（< 12h）',
              description: '与后台调度器一致的 silent refresh 策略',
              onClick: () => refreshAll.mutate(),
            },
          ]}
        />
        <button className="btn btn-primary btn-sm" onClick={() => setOpenImport(true)}>
          <Upload size={14} /> 导入
        </button>
        <SplitMenu
          label="导出"
          icon={<Download size={14} />}
          busy={exportAll.isPending}
          items={[
            {
              label: '导出本页',
              description: `当前页 ${items.length} 条 JSON（不含凭证明文）`,
              onClick: () => exportCurrentPage(),
            },
            {
              label: '导出全部',
              description: 'JSON Array，含密码 / access_token / cookie',
              onClick: () => exportAll.mutate('all'),
            },
            {
              label: '导出有效账号',
              description: 'status = valid（JSON，含凭证）',
              onClick: () => exportAll.mutate('valid'),
            },
            {
              label: '导出失效账号',
              description: 'status = invalid（JSON，含凭证）',
              onClick: () => exportAll.mutate('invalid'),
            },
          ]}
        />
        <SplitMenu
          label={`删除${selected.size ? ` (${selected.size})` : ''}`}
          icon={<Trash2 size={14} />}
          tone="danger"
          busy={batchDelete.isPending || purge.isPending}
          items={[
            {
              label: `删除选中 (${selected.size})`,
              description: '仅勾选的账号',
              tone: 'danger',
              onClick: async () => {
                if (selected.size === 0) {
                  toast.error('请先勾选要删除的账号');
                  return;
                }
                const ok = await confirm({
                  title: '批量删除 Banana 账号',
                  description: `将永久删除选中的 ${selected.size} 条 Banana 账号记录（含凭证）。`,
                  tone: 'danger',
                  confirmLabel: '删除',
                });
                if (ok) batchDelete.mutate(Array.from(selected));
              },
            },
            {
              label: '删除失效账号',
              description: 'status = invalid',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除失效账号',
                  '将永久删除所有 status=invalid 的账号（含凭证），不可恢复。',
                  { status: 'invalid' },
                ),
            },
            {
              label: '删除 0 积分账号',
              description: 'credits ≤ 0',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除 0 积分账号',
                  '将永久删除所有 credits ≤ 0 的账号（含凭证），不可恢复。',
                  { zero_credits: true },
                ),
            },
            {
              label: '删除 7 天仍未恢复额度',
              description: '额度回收中且 7 天未恢复',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除 7 天仍未恢复额度账号',
                  '将永久删除所有处于额度回收中、且 7 天仍未恢复额度的账号（含凭证），不可恢复。',
                  { quota_recovery_days: 7 },
                ),
            },
            {
              label: '删除 Token 已失效账号',
              description: 'expires_at 为空或 < 当前时间',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除 Token 已失效账号',
                  '将永久删除所有 expires_at 为空或已过期的账号（含凭证），不可恢复。',
                  { token_expired: true },
                ),
            },
            {
              label: '删除全部账号',
              description: '清空所有 Banana 账号',
              tone: 'danger',
              onClick: () =>
                purgeWithConfirm(
                  '删除全部 Banana 账号',
                  '将永久删除全部 Banana 账号记录（含凭证），不可恢复。请谨慎操作。',
                  { all: true },
                ),
            },
          ]}
        />
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
                <th className="px-3 py-2 text-left" title="该号在各档位上的权益学习状态。红=已确认无（自动跳过），灰=未测/默认乐观。7 天后自动重新探测。">权益</th>
                <th className="px-3 py-2 text-left">Token 失效</th>
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
                    <input
                      type="checkbox"
                      checked={selected.has(it.id)}
                      onChange={() => onToggleOne(it.id)}
                    />
                  </td>
                  <td className="px-3 py-1.5 font-mono text-text-primary">
                    <div>{it.email}</div>
                    {it.display_name && (
                      <div className="text-tiny text-text-tertiary">{it.display_name}</div>
                    )}
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge {...statusBadge(it.status)} />
                  </td>
                  <td className="px-3 py-1.5 text-text-secondary">{it.credits.toFixed(2)}</td>
                  <td className="px-3 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      <FlagPill ok={it.has_password} label="pwd" />
                      <FlagPill ok={it.has_access_token} label="token" />
                      <FlagPill ok={it.has_cookie} label="cookie" />
                    </div>
                  </td>
                  <td className="px-3 py-1.5">{entitlementCell(it.entitlements)}</td>
                  <td className="px-3 py-1.5 text-text-tertiary">
                    <div>{fmtMs(it.expires_at)}</div>
                    <div className={`text-tiny ${expiryTone(it.expires_at)}`}>
                      {expiryRelative(it.expires_at)}
                    </div>
                  </td>
                  <td className="px-3 py-1.5 text-text-tertiary">{fmtMs(it.last_used_at)}</td>
                  <td className="px-3 py-1.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <button
                        className="btn btn-ghost btn-xs"
                        title="编辑账号字段（凭证 / 状态 / 积分 / 备注）"
                        onClick={() => setEditing(it)}
                      >
                        <Pencil size={12} /> 编辑
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        disabled={refreshOne.isPending}
                        title="silent refresh 换 token + 拉积分"
                        onClick={() => refreshOne.mutate({ id: it.id })}
                      >
                        <RefreshCw size={12} /> 刷新
                      </button>
                      <button
                        className="btn btn-ghost btn-xs text-danger"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除 Banana 账号',
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

      {openImport && (
        <AdobeImportDialog onClose={() => setOpenImport(false)} onDone={() => refresh()} />
      )}
      {editing && (
        <AdobeEditDialog
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

function AdobeImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [text, setText] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState(false);
  // lastResult: 把最近一次导入结果留在面板上展示，不只 toast 一闪而过 —— 这样
  // imported=0 + errors=[…] 这种"成功响应但其实啥都没入库"的情况，用户一眼看到。
  const [lastResult, setLastResult] = useState<{ imported: number; skipped: number; errors?: string[] } | null>(null);
  const importMut = useMutation({
    mutationFn: () => poolAdobeApi.import({ text, source: 'import' }),
    onSuccess: (r) => {
      setLastResult({ imported: r.imported, skipped: r.skipped, errors: r.errors });
      if (r.imported > 0) {
        toast.success(`导入完成：成功 ${r.imported}，跳过 ${r.skipped}`);
        onDone();
        onClose();
      } else {
        // 一行都没入库 → 不弹"成功"，让面板里的警告条说话。
        toast.error(`未入库任何记录（跳过 ${r.skipped} 条），请检查下方错误明细`);
      }
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  // 读文件 → 塞 textarea。供「选择文件」按钮 + 拖拽 共用。
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
    e.target.value = ''; // 同一文件二次选择也要触发 onChange
    if (file) await ingestFile(file);
  };

  // 拖拽事件：onDragEnter/Over/Leave/Drop 都要 preventDefault，不然浏览器
  // 会把文件直接当成 location.href 打开（典型行为：把 json 文件展示成纯文本）。
  const onDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (!dragOver) setDragOver(true);
  };
  const onDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  };
  const onDrop = async (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
    const files = e.dataTransfer?.files;
    if (!files || files.length === 0) return;
    const first = files.item(0);
    if (!first) return;
    // 多文件拖入：只取第一个，避免静默覆盖意图
    if (files.length > 1) {
      toast.error(`一次只能导入一个文件，已忽略其余 ${files.length - 1} 个`);
    }
    await ingestFile(first);
  };

  return (
    <ImportDialogShell
      title="批量导入 ADOBE 账号"
      description={
        <span>
          支持以下 <strong>3 种文件格式</strong>（点「选择文件」上传 .json/.txt，或粘贴文本）：
          <br />
          ① <code className="font-mono">{'{"items": [{"cookie":"...","name":"...","email":"...","password":"..."}, ...]}'}</code>
          （含 email + password + cookie 的完整格式，例：<code className="font-mono">adobe_items_*.json</code>）；
          <br />
          ② <code className="font-mono">{'[{"cookie":"...","name":"邮箱"}, ...]'}</code>
          （JSON Array，例：<code className="font-mono">cookies (NN).json</code>）；
          <br />
          ③ 每行一个 <code className="font-mono">{'{"cookie":"..."}'}</code>
          （JSONL 纯 cookie，无邮箱会按 cookie 摘要派生占位邮箱，后台刷新自动补上真实信息，例：
          <code className="font-mono">100个.txt</code>）。
          <br />
          所有格式以 email 为 upsert 主键，已存在则更新。
        </span>
      }
      onClose={onClose}
      busy={importMut.isPending}
      onConfirm={() => importMut.mutate()}
    >
      <div className="mb-2 flex items-center gap-2">
        <label className="btn btn-outline btn-sm cursor-pointer">
          <Upload size={14} /> 选择文件
          <input
            type="file"
            accept=".json,.txt,application/json,text/plain"
            className="hidden"
            onChange={onPickFile}
          />
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
      <div
        className={`relative rounded-md border-2 border-dashed transition-colors ${
          dragOver ? 'border-primary bg-primary/5' : 'border-border bg-transparent'
        }`}
        onDragEnter={onDragOver}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        {dragOver && (
          <div className="pointer-events-none absolute inset-0 z-10 grid place-items-center rounded-md bg-primary/10 text-primary">
            <div className="flex flex-col items-center gap-1">
              <Upload size={32} />
              <span className="text-small font-medium">松开鼠标导入文件</span>
            </div>
          </div>
        )}
        <textarea
          className="input min-h-[260px] w-full border-0 bg-transparent font-mono focus:outline-none"
          placeholder={`# 拖拽文件到这里、点上方「选择文件」、或直接粘贴文本。
# 支持 3 种格式：

# 格式 ①  adobe_items_*.json（含完整账号 email+password+cookie）
{
  "items": [
    { "cookie": "ims_sid=...; aux_sid=...; relay=...", "name": "alice@example.com", "email": "alice@example.com", "password": "..." }
  ]
}

# 格式 ②  cookies (NN).json（JSON Array，含 cookie + name 邮箱）
[
  { "cookie": "ims_sid=...; aux_sid=...; relay=...", "name": "alice@example.com" }
]

# 格式 ③  100个.txt（JSONL，每行一个纯 cookie 对象，无邮箱）
{"cookie":"ftrset=...; ims_sid=...; aux_sid=..."}
{"cookie":"ftrset=...; ims_sid=...; aux_sid=..."}`}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            if (fileName) setFileName(null); // 手动改过后清掉文件名提示
          }}
        />
      </div>
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
                {lastResult.errors.length > 50 && (
                  <li className="text-text-tertiary">…（仅显示前 50 条）</li>
                )}
              </ul>
            </details>
          )}
        </div>
      )}
    </ImportDialogShell>
  );
}

// AdobeEditDialog 单条账号编辑对话框。
//
// 设计要点：
//   - 凭证字段（password / access_token / cookie）后端不下发明文，对话框中初始值为空。
//   - 留空 → 不变；填了 → 覆盖。
//   - status / credits / refresh_enabled / display_name / notes 直接用 list 数据回填。
//
// 提交时只发 dirty 的字段，避免不必要的写。
function AdobeEditDialog({
  item,
  busy,
  onClose,
  onSubmit,
}: {
  item: AdobePoolItem;
  busy?: boolean;
  onClose: () => void;
  onSubmit: (body: AdobePoolUpdateBody) => void;
}) {
  const [displayName, setDisplayName] = useState(item.display_name ?? '');
  const [adobeUserID, setAdobeUserID] = useState(item.adobe_user_id ?? '');
  const [password, setPassword] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [cookie, setCookie] = useState('');
  const [status, setStatus] = useState<AdobePoolStatus>(
    (item.status as AdobePoolStatus) ?? 'valid',
  );
  const [credits, setCredits] = useState<string>(String(item.credits ?? 0));
  const [refreshEnabled, setRefreshEnabled] = useState<0 | 1>(1);
  const [notes, setNotes] = useState('');

  function submit() {
    const body: AdobePoolUpdateBody = {};
    if (displayName !== (item.display_name ?? '')) body.display_name = displayName;
    if (adobeUserID !== (item.adobe_user_id ?? '')) body.adobe_user_id = adobeUserID;
    if (password) body.password = password;
    if (accessToken) body.access_token = accessToken;
    if (cookie) body.cookie = cookie;
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
          <h3 className="text-h4 text-text-primary">编辑 Banana 账号</h3>
          <p className="mt-1 text-small text-text-tertiary">
            <span className="font-mono">{item.email}</span> · 凭证字段留空不变，填入则覆盖
          </p>
        </header>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <div className="text-tiny text-text-tertiary">Display Name</div>
            <input
              className="input input-sm w-full"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">Banana User ID</div>
            <input
              className="input input-sm w-full font-mono"
              value={adobeUserID}
              onChange={(e) => setAdobeUserID(e.target.value)}
            />
          </label>
          <label className="block">
            <div className="text-tiny text-text-tertiary">状态</div>
            <select
              className="select select-sm w-full"
              value={status}
              onChange={(e) => setStatus(e.target.value as AdobePoolStatus)}
            >
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
              <option value={0}>关闭（不被调度器自动刷新）</option>
            </select>
          </label>
          <label className="block sm:col-span-2">
            <div className="text-tiny text-text-tertiary">Password（留空不变）</div>
            <input
              className="input input-sm w-full font-mono"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
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
            <div className="text-tiny text-text-tertiary">Cookie（留空不变）</div>
            <textarea
              className="input min-h-[60px] w-full font-mono text-tiny"
              value={cookie}
              onChange={(e) => setCookie(e.target.value)}
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
