import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ChevronDown,
  ChevronRight,
  Copy,
  Eye,
  ImageIcon,
  Link as LinkIcon,
  MessageSquare,
  RefreshCw,
  Search,
  Trash2,
  Video,
  X,
} from 'lucide-react';
import { Fragment, useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { fmtPoints, fmtTime } from '../../lib/format';
import { logsApi } from '../../lib/services';
import type { AdminGenerationLogItem, AdminGenerationUpstreamLogItem } from '../../lib/types';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';
import { PageHeader, PageShell, Pager, Section, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { FileText } from 'lucide-react';

function statusInfo(s: number): { label: string; cls: string } {
  switch (s) {
    case 0:
      return { label: '待处理', cls: 'badge badge-outline' };
    case 1:
      return { label: '生成中', cls: 'badge badge-warning' };
    case 2:
      return { label: '成功', cls: 'badge badge-success' };
    case 3:
      return { label: '失败', cls: 'badge badge-danger' };
    case 4:
      return { label: '已退款', cls: 'badge badge-warning' };
    default:
      return { label: String(s), cls: 'badge badge-outline' };
  }
}

function kindInfo(kind: string) {
  if (kind === 'video') return { label: '视频', icon: Video };
  if (kind === 'chat' || kind === 'text') return { label: '文字', icon: MessageSquare };
  return { label: '图片', icon: ImageIcon };
}

function fmtDuration(ms?: number): string {
  if (!ms || ms <= 0) return '-';
  return `${Math.max(1, Math.round(ms / 1000))}s`;
}

function formatGenerationSpec(row: AdminGenerationLogItem): string {
  return [row.resolution, row.aspect_ratio].filter(Boolean).join(' · ') || '-';
}

function Preview({ row }: { row: AdminGenerationLogItem }) {
  // 列表小图始终用 preview_url（视频是首帧 jpg、图片是图本身），
  // 点开「查看」用 asset_url 才是真正的主资源（视频 = mp4、图片 = png/jpg）。
  const thumb = row.preview_url;
  const main = row.asset_url || row.preview_url;
  if (!thumb && !main) return <span className="text-text-tertiary">-</span>;

  const copyLink = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (!main) return;
    try {
      // 复制绝对 URL（拼上当前 origin），方便外部直接打开。
      const abs = main.startsWith('http') ? main : `${window.location.origin}${main}`;
      await navigator.clipboard.writeText(abs);
      toast.success(row.kind === 'video' ? '视频链接已复制' : '图片链接已复制');
    } catch {
      toast.error('复制失败，请手动复制');
    }
  };

  if (row.kind === 'video') {
    // 视频不再带缩略图占位，直接「视频」按钮 + 「复制链接」按钮，避免列表里那张莫名其妙的小图。
    return (
      <div className="inline-flex items-center gap-1">
        <a className="btn btn-ghost btn-sm" href={main} target="_blank" rel="noreferrer" title="在新标签页打开 mp4">
          <Video size={14} /> 视频
        </a>
        {main && (
          <button className="btn btn-ghost btn-icon btn-sm" onClick={copyLink} title="复制视频链接">
            <Copy size={13} />
          </button>
        )}
      </div>
    );
  }
  // 图片：缩略图 + 浮层复制按钮。
  return (
    <div className="group relative inline-block">
      <a
        href={main}
        target="_blank"
        rel="noreferrer"
        className="block h-10 w-10 overflow-hidden rounded-md border border-border bg-surface-2"
        title="点击查看原图"
      >
        <img src={thumb} alt="" className="h-full w-full object-cover" />
      </a>
      {main && (
        <button
          className="absolute -right-1 -top-1 hidden h-5 w-5 place-items-center rounded-full border border-border bg-surface-1 text-text-secondary shadow-1 hover:text-text-primary group-hover:grid"
          onClick={copyLink}
          title="复制图片链接"
        >
          <LinkIcon size={11} />
        </button>
      )}
    </div>
  );
}

export default function LogsPage() {
  const qc = useQueryClient();
  const [keyword, setKeyword] = useState('');
  const [debouncedKeyword, setDebouncedKeyword] = useState('');
  const [kind, setKind] = useState<'all' | 'image' | 'video' | 'chat'>('all');
  const [status, setStatus] = useState<'all' | '0' | '1' | '2' | '3' | '4'>('all');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  useEffect(() => setPage(1), [pageSize]);
  const [purgeDays, setPurgeDays] = useState('30');
  // purgeOnlyFailed: 仅删失败 / 已退款（status 3/4）的记录，避免一刀切误删成功记录。
  const [purgeOnlyFailed, setPurgeOnlyFailed] = useState(false);
  const [stuckMinutes, setStuckMinutes] = useState('10');
  // confirmPurge: 普通"N 天前"删除的确认弹窗
  // confirmPurgeAllFailed: "一键删全部失败任务"的确认弹窗（不限时间）
  const [confirmPurge, setConfirmPurge] = useState(false);
  const [confirmPurgeAllFailed, setConfirmPurgeAllFailed] = useState(false);
  const [confirmCleanupStuck, setConfirmCleanupStuck] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [upstreamTask, setUpstreamTask] = useState<AdminGenerationLogItem | null>(null);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setDebouncedKeyword(keyword);
    }, 350);
    return () => window.clearTimeout(timer);
  }, [keyword]);

  const query = useMemo(
    () => ({
      keyword: debouncedKeyword.trim() || undefined,
      kind: kind === 'all' ? undefined : kind,
      status: status === 'all' ? undefined : (Number(status) as 0 | 1 | 2 | 3 | 4),
      page,
      page_size: pageSize,
    }),
    [debouncedKeyword, kind, status, page, pageSize],
  );

  const list = useQuery({
    queryKey: ['admin', 'logs', 'generations', query],
    queryFn: () => logsApi.generations(query),
  });

  const items = list.data?.list ?? [];
  const total = list.data?.total ?? 0;
  const purgeDayNum = Math.max(1, Math.floor(Number(purgeDays) || 0));
  const stuckMinuteNum = Math.max(1, Math.floor(Number(stuckMinutes) || 0));

  const purge = useMutation({
    mutationFn: () => logsApi.purgeGenerations(purgeDayNum, purgeOnlyFailed ? 3 : undefined),
    onSuccess: (r) => {
      const scope = purgeOnlyFailed ? '失败' : '全部';
      toast.success(`已删除 ${purgeDayNum} 天前的 ${r.deleted} 条${scope}记录`);
      setConfirmPurge(false);
      setPage(1);
      qc.invalidateQueries({ queryKey: ['admin', 'logs'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  // 一键删除全部失败任务（不限时间，status=3）。后端 days=0 + status=3 走"不限时间只删失败"路径。
  const purgeAllFailed = useMutation({
    mutationFn: () => logsApi.purgeGenerations(0, 3),
    onSuccess: (r) => {
      toast.success(`已删除全部失败记录共 ${r.deleted} 条`);
      setConfirmPurgeAllFailed(false);
      setPage(1);
      qc.invalidateQueries({ queryKey: ['admin', 'logs'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const cleanupStuck = useMutation({
    mutationFn: () => logsApi.cleanupStuckGenerations(stuckMinuteNum),
    onSuccess: (r) => {
      toast.success(`已结束并退款 ${r.cleaned} 个卡住任务`);
      setConfirmCleanupStuck(false);
      qc.invalidateQueries({ queryKey: ['admin', 'logs'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  return (
    <PageShell>
      <PageHeader
        icon={<FileText size={16} />}
        title="请求日志"
        right={
          <>
            <div className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-surface-1 px-2">
              <span className="text-tiny text-text-tertiary">删除</span>
              <input
                className="input input-sm h-6 w-12 rounded px-1 text-center"
                value={purgeDays}
                inputMode="numeric"
                onChange={(e) => setPurgeDays(e.target.value.replace(/\D/g, '').slice(0, 4))}
                aria-label="删除几天前"
              />
              <span className="text-tiny text-text-tertiary">天前</span>
              <label className="ml-1 inline-flex items-center gap-1 text-tiny text-text-secondary cursor-pointer" title="只删除失败任务（status=3），保留成功记录">
                <input
                  type="checkbox"
                  className="checkbox checkbox-xs"
                  checked={purgeOnlyFailed}
                  onChange={(e) => setPurgeOnlyFailed(e.target.checked)}
                />
                只删失败
              </label>
              <button className="btn btn-danger btn-xs" disabled={purge.isPending || purgeDayNum <= 0} onClick={() => setConfirmPurge(true)}>
                <Trash2 size={12} /> 删除
              </button>
            </div>
            <button
              className="btn btn-danger btn-sm"
              disabled={purgeAllFailed.isPending}
              onClick={() => setConfirmPurgeAllFailed(true)}
              title="不限时间，删除所有 status=3 失败任务"
            >
              <Trash2 size={14} /> 一键清空失败
            </button>
            <div className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-surface-1 px-2">
              <span className="text-tiny text-text-tertiary">卡住超过</span>
              <input
                className="input input-sm h-6 w-12 rounded px-1 text-center"
                value={stuckMinutes}
                inputMode="numeric"
                onChange={(e) => setStuckMinutes(e.target.value.replace(/\D/g, '').slice(0, 4))}
                aria-label="卡住分钟数"
              />
              <span className="text-tiny text-text-tertiary">分钟</span>
              <button
                className="btn btn-warning btn-xs"
                disabled={cleanupStuck.isPending || stuckMinuteNum <= 0}
                onClick={() => setConfirmCleanupStuck(true)}
                title="将超过指定分钟数仍在生成中的任务置为失败并自动退款"
              >
                清理卡住
              </button>
            </div>
            <button className="btn btn-outline btn-sm" onClick={() => qc.invalidateQueries({ queryKey: ['admin', 'logs'] })}>
              <RefreshCw size={14} /> 刷新
            </button>
          </>
        }
      />

      <Toolbar>
        <div className="relative min-w-[280px] flex-1">
          <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-tertiary" />
          <input
            className="input input-sm pl-7"
            placeholder="搜索用户 / Key / 模型 / 提示词 / task_id"
            value={keyword}
            onChange={(e) => {
              setKeyword(e.target.value);
              setPage(1);
            }}
          />
        </div>
        <select
          className="select select-sm"
          value={kind}
          onChange={(e) => {
            setKind(e.target.value as typeof kind);
            setPage(1);
          }}
        >
          <option value="all">全部类型</option>
          <option value="chat">文字</option>
          <option value="image">图片</option>
          <option value="video">视频</option>
        </select>
        <select
          className="select select-sm"
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as typeof status);
            setPage(1);
          }}
        >
          <option value="all">全部状态</option>
          <option value="0">待处理</option>
          <option value="1">生成中</option>
          <option value="2">成功</option>
          <option value="3">失败</option>
          <option value="4">已退款</option>
        </select>
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary whitespace-nowrap">共 {total} 条</span>
      </Toolbar>

      <div className="space-y-3 md:hidden">
        {items.map((row) => {
          const st = statusInfo(row.status);
          const ki = kindInfo(row.kind);
          const KindIcon = ki.icon;
          return (
            <div key={row.task_id} className="rounded-xl border border-border bg-surface-1 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <KindIcon size={14} className="text-text-tertiary" />
                    <span className="font-medium text-text-primary">{row.model_code}</span>
                    <span className={st.cls}>{st.label}</span>
                  </div>
                  <div className="mt-1 text-tiny text-text-tertiary">{fmtTime(row.created_at)} · {row.user_label}</div>
                </div>
                <button className="btn btn-ghost btn-sm" onClick={() => setUpstreamTask(row)}>
                  <Eye size={14} /> 上游
                </button>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-2 text-tiny">
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">耗时</div>
                  <div className="text-text-secondary">{fmtDuration(row.duration_ms)}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">费用</div>
                  <div className="text-text-secondary">{fmtPoints(row.cost_points)}</div>
                </div>
                <div className="rounded-lg bg-surface-2 px-2 py-1.5">
                  <div className="text-text-tertiary">规格</div>
                  <div className="text-text-secondary">{formatGenerationSpec(row)}</div>
                </div>
              </div>
              <div className="mt-3 flex items-center gap-2">
                <Preview row={row} />
                <button className="btn btn-ghost btn-sm" onClick={() => setExpanded(expanded === row.task_id ? null : row.task_id)}>
                  {expanded === row.task_id ? '收起详情' : '展开详情'}
                </button>
              </div>
              {expanded === row.task_id && (
                <div className="mt-3 space-y-3">
                  <DetailBlock title="提示词" value={row.prompt || '-'} />
                  <DetailBlock title="错误信息" value={row.error || '-'} danger={Boolean(row.error)} />
                </div>
              )}
            </div>
          );
        })}
      </div>

      <Section bodyClass="p-0">
      <div className="hidden table-wrap overflow-hidden md:block">
        <table className="data-table table-fixed text-small">
          <thead>
            <tr>
              <th className="w-[42px]" />
              <th className="w-[156px]">时间</th>
              <th className="w-[160px]">用户</th>
              <th className="w-[150px]">Key</th>
              <th className="w-[170px]">模型</th>
              <th className="w-[92px]">状态</th>
              <th className="w-[86px]">耗时</th>
              <th className="w-[86px]">费用</th>
              <th className="w-[92px]">规格</th>
              <th className="w-[88px]">预览</th>
              <th className="w-[118px]">上游</th>
            </tr>
          </thead>
          <tbody>
            {list.isLoading && (
              <tr>
                <td colSpan={11} className="py-10 text-center text-text-tertiary">加载中...</td>
              </tr>
            )}
            {!list.isLoading && items.length === 0 && (
              <tr>
                <td colSpan={11} className="py-10 text-center text-text-tertiary">暂无生成记录</td>
              </tr>
            )}
            {items.map((row) => {
              const st = statusInfo(row.status);
              const ki = kindInfo(row.kind);
              const KindIcon = ki.icon;
              const isOpen = expanded === row.task_id;
              return (
                <Fragment key={row.task_id}>
                  <tr className="align-middle">
                    <td>
                      <button
                        className="btn btn-ghost btn-icon btn-sm"
                        title={isOpen ? '收起详情' : '展开详情'}
                        onClick={() => setExpanded(isOpen ? null : row.task_id)}
                      >
                        {isOpen ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                      </button>
                    </td>
                    <td className="whitespace-nowrap">
                      <div>{fmtTime(row.created_at)}</div>
                      <div className="truncate text-tiny text-text-tertiary">{row.task_id}</div>
                    </td>
                    <td>
                      <div className="truncate">{row.user_label}</div>
                      <div className="text-tiny text-text-tertiary">UID {row.user_id}</div>
                    </td>
                    <td className="truncate" title={row.key_label || '-'}>
                      {row.key_label || '-'}
                    </td>
                    <td>
                      <div className="flex min-w-0 items-center gap-1.5">
                        <KindIcon size={14} className="shrink-0 text-text-tertiary" />
                        <span className="truncate" title={row.model_code}>{row.model_code}</span>
                      </div>
                    </td>
                    <td><span className={st.cls}>{st.label}</span></td>
                    <td>{fmtDuration(row.duration_ms)}</td>
                    <td>{fmtPoints(row.cost_points)}</td>
                    <td className="whitespace-nowrap text-tiny text-text-secondary">{formatGenerationSpec(row)}</td>
                    <td><Preview row={row} /></td>
                    <td>
                      <button className="btn btn-ghost btn-sm" onClick={() => setUpstreamTask(row)}>
                        <Eye size={14} /> 日志
                      </button>
                    </td>
                  </tr>
                  {isOpen && (
                    <tr>
                      <td colSpan={11} className="bg-surface-2/60 p-0">
                        <div className="grid gap-3 p-4 lg:grid-cols-[1fr_1fr]">
                          <DetailBlock title="规格" value={formatGenerationSpec(row)} />
                          <DetailBlock title="提示词" value={row.prompt || '-'} />
                          <DetailBlock title="错误信息" value={row.error || '-'} danger={Boolean(row.error)} />
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
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

      {upstreamTask && <UpstreamDialog task={upstreamTask} onClose={() => setUpstreamTask(null)} />}
      {confirmPurge && (
        <ConfirmDialog
          days={purgeDayNum}
          onlyFailed={purgeOnlyFailed}
          loading={purge.isPending}
          onClose={() => setConfirmPurge(false)}
          onConfirm={() => purge.mutate()}
        />
      )}
      {confirmPurgeAllFailed && (
        <ConfirmAllFailedDialog
          loading={purgeAllFailed.isPending}
          onClose={() => setConfirmPurgeAllFailed(false)}
          onConfirm={() => purgeAllFailed.mutate()}
        />
      )}
      {confirmCleanupStuck && (
        <ConfirmCleanupStuckDialog
          minutes={stuckMinuteNum}
          loading={cleanupStuck.isPending}
          onClose={() => setConfirmCleanupStuck(false)}
          onConfirm={() => cleanupStuck.mutate()}
        />
      )}
    </PageShell>
  );
}

function DetailBlock({ title, value, danger }: { title: string; value: string; danger?: boolean }) {
  return (
    <div className="rounded-xl border border-border bg-surface-1 p-3">
      <div className="mb-2 text-tiny text-text-tertiary">{title}</div>
      <div className={`max-h-36 overflow-auto whitespace-pre-wrap break-words text-small leading-relaxed ${danger ? 'text-danger' : 'text-text-secondary'}`}>
        {value}
      </div>
    </div>
  );
}

function UpstreamDialog({ task, onClose }: { task: AdminGenerationLogItem; onClose: () => void }) {
  const q = useQuery({
    queryKey: ['admin', 'logs', 'generations', task.task_id, 'upstream'],
    queryFn: () => logsApi.generationUpstream(task.task_id),
  });
  const rows = q.data ?? [];
  return (
    <div className="fixed inset-0 z-[80] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in max-h-[86vh] w-full max-w-5xl overflow-hidden">
        <header className="modal-header">
          <div>
            <h2 className="text-h4">上游日志</h2>
            <p className="text-small text-text-tertiary">{task.task_id} · {task.model_code}</p>
          </div>
          <button className="btn btn-ghost btn-sm" onClick={onClose}><X size={16} /></button>
        </header>
        <div className="modal-body max-h-[70vh] space-y-3 overflow-auto">
          {q.isLoading && <div className="py-10 text-center text-text-tertiary">加载中...</div>}
          {!q.isLoading && rows.length === 0 && <div className="py-10 text-center text-text-tertiary">暂无上游日志，新任务会自动记录。</div>}
          {rows.map((row) => <UpstreamRow key={row.id} row={row} />)}
        </div>
      </div>
    </div>
  );
}

function UpstreamRow({ row }: { row: AdminGenerationUpstreamLogItem }) {
  return (
    <section className="rounded-xl border border-border bg-surface-1 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="badge badge-outline">{row.stage}</span>
          {row.method && <span className="text-small text-text-tertiary">{row.method}</span>}
          {row.status_code > 0 && <span className="text-small text-text-tertiary">HTTP {row.status_code}</span>}
          {row.duration_ms > 0 && <span className="text-small text-text-tertiary">{fmtDuration(row.duration_ms)}</span>}
        </div>
        <span className="text-tiny text-text-tertiary">{fmtTime(row.created_at)}</span>
      </div>
      {row.url && <div className="mt-2 break-all text-tiny text-text-tertiary">{row.url}</div>}
      <LogBlock title="请求" value={row.request_excerpt} />
      <LogBlock title="响应" value={row.response_excerpt} />
      <LogBlock title="错误" value={row.error} danger />
      <LogBlock title="附加信息" value={prettyMeta(row.meta)} />
    </section>
  );
}

function LogBlock({ title, value, danger }: { title: string; value?: string; danger?: boolean }) {
  if (!value) return null;
  return (
    <div className="mt-3">
      <div className="mb-1 text-tiny text-text-tertiary">{title}</div>
      <pre className={`max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-surface-2 p-3 text-tiny ${danger ? 'text-danger' : 'text-text-secondary'}`}>
        {value}
      </pre>
    </div>
  );
}

function ConfirmAllFailedDialog({ loading, onClose, onConfirm }: { loading: boolean; onClose: () => void; onConfirm: () => void }) {
  return (
    <div className="fixed inset-0 z-[90] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in w-full max-w-md p-6">
        <div className="mb-4 flex items-start gap-3">
          <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-danger/10 text-danger">
            <Trash2 size={18} />
          </div>
          <div>
            <h2 className="text-h4">一键清空所有失败任务</h2>
            <p className="mt-1 text-small text-text-secondary">
              将删除 <span className="font-medium text-danger">所有</span> 状态为「失败」的请求日志（不限时间），该操作不可恢复。
            </p>
            <p className="mt-2 text-tiny text-text-tertiary">
              仅影响 status=失败 的记录，成功 / 已退款 / 进行中的不受影响。
            </p>
          </div>
        </div>
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" disabled={loading} onClick={onClose}>取消</button>
          <button className="btn btn-danger btn-md" disabled={loading} onClick={onConfirm}>
            {loading ? '删除中...' : '确认清空'}
          </button>
        </div>
      </div>
    </div>
  );
}

function ConfirmCleanupStuckDialog({ minutes, loading, onClose, onConfirm }: { minutes: number; loading: boolean; onClose: () => void; onConfirm: () => void }) {
  return (
    <div className="fixed inset-0 z-[90] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in w-full max-w-md p-6">
        <div className="mb-4 flex items-start gap-3">
          <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-warning/10 text-warning">
            <RefreshCw size={18} />
          </div>
          <div>
            <h2 className="text-h4">清理卡住任务</h2>
            <p className="mt-1 text-small text-text-secondary">
              将超过 {minutes} 分钟仍处于「生成中」的任务置为失败，并按现有失败流程自动退款。
            </p>
            <p className="mt-2 text-tiny text-text-tertiary">
              正在更新的正常任务不会被处理；建议只在重启、发布或 worker 异常后使用。
            </p>
          </div>
        </div>
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" disabled={loading} onClick={onClose}>取消</button>
          <button className="btn btn-warning btn-md" disabled={loading} onClick={onConfirm}>
            {loading ? '清理中...' : '确认清理'}
          </button>
        </div>
      </div>
    </div>
  );
}

function ConfirmDialog({ days, onlyFailed, loading, onClose, onConfirm }: { days: number; onlyFailed?: boolean; loading: boolean; onClose: () => void; onConfirm: () => void }) {
  return (
    <div className="fixed inset-0 z-[90] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in w-full max-w-md p-6">
        <div className="mb-4 flex items-start gap-3">
          <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-danger/10 text-danger">
            <Trash2 size={18} />
          </div>
          <div>
            <h2 className="text-h4">删除请求日志</h2>
            <p className="mt-1 text-small text-text-secondary">
              确定删除 {days} 天前的{onlyFailed ? '「失败」状态' : '所有'}请求日志吗？该操作不可恢复。
            </p>
          </div>
        </div>
        <div className="flex justify-end gap-2">
          <button className="btn btn-outline btn-md" disabled={loading} onClick={onClose}>取消</button>
          <button className="btn btn-danger btn-md" disabled={loading} onClick={onConfirm}>
            {loading ? '删除中...' : '确认删除'}
          </button>
        </div>
      </div>
    </div>
  );
}

function prettyMeta(v?: string) {
  if (!v) return '';
  try {
    return JSON.stringify(JSON.parse(v), null, 2);
  } catch {
    return v;
  }
}
