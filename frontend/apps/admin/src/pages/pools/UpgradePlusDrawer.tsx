// UpgradePlusDrawer
//
// GPT 号池右侧滑出抽屉，承载 GoPay 开 Plus 升级任务的「进度概览 + 历史 +
// 单任务实时日志」。所有任务都走 register_task 框架，provider=upgrade_plus。
//
// 设计要点
//   - 抽屉打开时，每 3s 轮询任务列表（仅 upgrade_plus），任务列表有 in-flight
//     时才会一直 active。
//   - 选中某个任务后展开实时日志，每 2s 轮询 register_task_logs?task_id=X，最多
//     拉 100 条；任务终态后停止轮询。
//   - 派发本身在父面板（GptPoolPanel）发起，本组件只负责"展示"。
//   - 顶部 4 个 Stat 直接消费列表数据 client side 聚合（避免再调 stats 接口，
//     stats 没有按 provider 过滤但我们已经按 provider 拉了）。
//
// 操作能力
//   - 取消任务（pending/running） → POST /register-tasks/:id/cancel
//   - 删除任务（终态）             → DELETE /register-tasks/:id
//   - 一键清理终态任务             → DELETE /register-tasks?provider=upgrade_plus
//
// 依赖
//   - registerTaskApi.list / get / cancel / remove / purge / logs
//   - 父组件传入 onClose；其余资料从 react-query 自取。

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  AlertCircle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock,
  Loader2,
  Trash2,
  X,
  XCircle,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { registerTaskApi } from '../../lib/services';
import type {
  RegisterTaskItem,
  RegisterTaskLogEntry,
  RegisterTaskStatus,
} from '../../lib/types';
import { toast } from '../../stores/toast';

import { Badge, fmtMs } from './_shared';

// 抽屉只关心这 5 种 status；任何其它字符串归类为 "pending"。
const TASK_TABS: { key: 'active' | 'all' | 'success' | 'failed'; label: string }[] = [
  { key: 'active', label: '进行中' },
  { key: 'all', label: '全部历史' },
  { key: 'success', label: '已成功' },
  { key: 'failed', label: '失败/取消' },
];

function statusBadge(s: string): { label: string; tone: string; icon?: JSX.Element } {
  switch (s) {
    case 'pending':
      return { label: '排队', tone: 'bg-surface-2 text-text-tertiary', icon: <Clock size={11} /> };
    case 'running':
      return { label: '运行中', tone: 'bg-info-soft text-info', icon: <Loader2 size={11} className="animate-spin" /> };
    case 'success':
      return { label: '成功', tone: 'bg-success-soft text-success', icon: <CheckCircle2 size={11} /> };
    case 'failed':
      return { label: '失败', tone: 'bg-danger-soft text-danger', icon: <XCircle size={11} /> };
    case 'cancelled':
      return { label: '已取消', tone: 'bg-warn-soft text-warn', icon: <AlertCircle size={11} /> };
    default:
      return { label: s, tone: 'bg-surface-2 text-text-secondary' };
  }
}

function levelTone(level: string): string {
  if (level === 'error') return 'text-danger';
  if (level === 'warn') return 'text-warn';
  return 'text-text-secondary';
}

// upgrade_plus 任务的 payload 里我们约定塞这两个字段，便于 UI 展示。
function taskLabel(t: RegisterTaskItem): string {
  const p = (t.payload ?? {}) as Record<string, any>;
  if (typeof p.gpt_email === 'string' && p.gpt_email) return p.gpt_email;
  if (typeof p.pool_gpt_id === 'number') return `pool_gpt #${p.pool_gpt_id}`;
  if (t.email) return t.email;
  return `task #${t.id}`;
}

function isTerminal(s: string): boolean {
  return s === 'success' || s === 'failed' || s === 'cancelled';
}

interface Props {
  open: boolean;
  onClose: () => void;
}

export default function UpgradePlusDrawer({ open, onClose }: Props) {
  const qc = useQueryClient();
  const [tab, setTab] = useState<(typeof TASK_TABS)[number]['key']>('active');
  const [selectedID, setSelectedID] = useState<number | null>(null);

  // ESC 关闭。
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  // 列表 query：只过 upgrade_plus，page_size 200 一次拉够，前端再按 tab 切。
  // 进行中任务存在时，自动每 3s 轮询；全完成时停轮询。
  const list = useQuery({
    queryKey: ['admin', 'register-tasks', 'upgrade_plus'],
    queryFn: () =>
      registerTaskApi.list({
        provider: 'upgrade_plus',
        page: 1,
        page_size: 200,
      }),
    enabled: open,
    refetchInterval: (query) => {
      const items = (query.state.data?.list ?? []) as RegisterTaskItem[];
      const hasInflight = items.some(
        (it) => it.status === 'pending' || it.status === 'running',
      );
      return hasInflight ? 3000 : false;
    },
  });

  const items = useMemo(() => list.data?.list ?? [], [list.data]);

  const counts = useMemo(() => {
    const c: Record<RegisterTaskStatus, number> = {
      pending: 0,
      running: 0,
      success: 0,
      failed: 0,
      cancelled: 0,
    };
    items.forEach((it) => {
      if (it.status in c) c[it.status as RegisterTaskStatus]++;
    });
    return c;
  }, [items]);

  const filtered = useMemo(() => {
    if (tab === 'active') {
      return items.filter((it) => it.status === 'pending' || it.status === 'running');
    }
    if (tab === 'success') return items.filter((it) => it.status === 'success');
    if (tab === 'failed') return items.filter((it) => it.status === 'failed' || it.status === 'cancelled');
    return items;
  }, [items, tab]);

  // 选中的 task 详情（fresh fetch + 实时日志）。
  const selected = useMemo(
    () => (selectedID ? items.find((it) => it.id === selectedID) ?? null : null),
    [items, selectedID],
  );

  const cancelMu = useMutation({
    mutationFn: (id: number) => registerTaskApi.cancel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });
      toast.success('已请求取消');
    },
    onError: (e: ApiError) => toast.error(e.message || '取消失败'),
  });
  const removeMu = useMutation({
    mutationFn: (id: number) => registerTaskApi.remove(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });
      setSelectedID(null);
      toast.success('已删除任务');
    },
    onError: (e: ApiError) => toast.error(e.message || '删除失败'),
  });
  const purgeMu = useMutation({
    mutationFn: () => registerTaskApi.purge('upgrade_plus'),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ['admin', 'register-tasks'] });
      setSelectedID(null);
      toast.success(`已清理 ${r.deleted} 条历史任务`);
    },
    onError: (e: ApiError) => toast.error(e.message || '清理失败'),
  });

  return (
    <>
      {/* 遮罩 */}
      <div
        className={`fixed inset-0 z-40 bg-black/30 backdrop-blur-[1px] transition-opacity ${
          open ? 'pointer-events-auto opacity-100' : 'pointer-events-none opacity-0'
        }`}
        onClick={onClose}
      />
      {/* 抽屉本体：右侧滑出 */}
      <aside
        className={`fixed inset-y-0 right-0 z-50 flex w-full max-w-[560px] flex-col bg-surface-1 shadow-2xl transition-transform duration-200 ease-out ${
          open ? 'translate-x-0' : 'translate-x-full'
        }`}
        role="dialog"
        aria-label="Plus 升级任务抽屉"
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-border-subtle px-4 py-3">
          <div>
            <h3 className="text-h4 text-text-primary">Plus 升级任务</h3>
            <p className="mt-0.5 text-tiny text-text-tertiary">
              GoPay 15 步流；进行中自动 3s 轮询；点任务看实时日志
            </p>
          </div>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={onClose}
            aria-label="关闭"
          >
            <X size={16} />
          </button>
        </div>

        {/* 4 个 Stat */}
        <div className="grid grid-cols-4 gap-2 border-b border-border-subtle px-4 py-3 text-center">
          <Stat tone="text-info" label="进行中" value={counts.pending + counts.running} />
          <Stat tone="text-success" label="成功" value={counts.success} />
          <Stat tone="text-danger" label="失败" value={counts.failed} />
          <Stat tone="text-warn" label="取消" value={counts.cancelled} />
        </div>

        {/* Tabs */}
        <div className="flex items-center justify-between border-b border-border-subtle px-2 py-2">
          <div className="flex items-center gap-1">
            {TASK_TABS.map((t) => {
              const active = tab === t.key;
              return (
                <button
                  key={t.key}
                  type="button"
                  className={`rounded-md px-3 py-1 text-small ${
                    active
                      ? 'bg-info-soft text-info'
                      : 'text-text-tertiary hover:bg-surface-2 hover:text-text-secondary'
                  }`}
                  onClick={() => setTab(t.key)}
                >
                  {t.label}
                </button>
              );
            })}
          </div>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            disabled={purgeMu.isPending || items.every((it) => !isTerminal(it.status))}
            onClick={() => purgeMu.mutate()}
            title="清理所有终态（success/failed/cancelled）的 upgrade_plus 任务"
          >
            <Trash2 size={13} /> 清理终态
          </button>
        </div>

        {/* 任务列表 */}
        <div className="flex-1 overflow-y-auto">
          {list.isLoading && (
            <div className="flex items-center justify-center py-10 text-text-tertiary">
              <Loader2 size={18} className="animate-spin" />
            </div>
          )}
          {!list.isLoading && filtered.length === 0 && (
            <div className="flex flex-col items-center justify-center gap-2 py-10 text-text-tertiary">
              <AlertCircle size={20} />
              <div className="text-small">当前 tab 暂无任务</div>
              {tab === 'active' && (
                <div className="text-tiny">从工具栏「订阅升级」下拉派发任务即可看到进度</div>
              )}
            </div>
          )}
          <ul className="divide-y divide-border-subtle">
            {filtered.map((it) => (
              <TaskRow
                key={it.id}
                task={it}
                expanded={selectedID === it.id}
                onToggle={() => setSelectedID(selectedID === it.id ? null : it.id)}
                onCancel={() => cancelMu.mutate(it.id)}
                onRemove={() => removeMu.mutate(it.id)}
                cancelBusy={cancelMu.isPending}
                removeBusy={removeMu.isPending}
              />
            ))}
          </ul>
        </div>

        {/* footer 提示 */}
        {selected && (
          <div className="border-t border-border-subtle bg-surface-2 px-4 py-2 text-tiny text-text-tertiary">
            选中任务 #{selected.id} · {taskLabel(selected)} · status={selected.status} ·
            progress={selected.progress}%
          </div>
        )}
      </aside>
    </>
  );
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: string }) {
  return (
    <div>
      <div className={`text-h4 tabular-nums ${tone || 'text-text-primary'}`}>{value}</div>
      <div className="text-tiny text-text-tertiary">{label}</div>
    </div>
  );
}

interface TaskRowProps {
  task: RegisterTaskItem;
  expanded: boolean;
  onToggle: () => void;
  onCancel: () => void;
  onRemove: () => void;
  cancelBusy: boolean;
  removeBusy: boolean;
}

function TaskRow({
  task,
  expanded,
  onToggle,
  onCancel,
  onRemove,
  cancelBusy,
  removeBusy,
}: TaskRowProps) {
  const sb = statusBadge(task.status);
  const inflight = task.status === 'pending' || task.status === 'running';

  return (
    <li className="px-3 py-2.5">
      <button
        type="button"
        className="flex w-full items-center gap-2 text-left"
        onClick={onToggle}
      >
        {expanded ? (
          <ChevronDown size={14} className="text-text-tertiary" />
        ) : (
          <ChevronRight size={14} className="text-text-tertiary" />
        )}
        <Badge label={`#${task.id}`} tone="bg-surface-2 text-text-secondary" />
        <span
          className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-tiny ${sb.tone}`}
        >
          {sb.icon}
          {sb.label}
        </span>
        <span className="flex-1 truncate text-small text-text-primary">{taskLabel(task)}</span>
        <span className="tabular-nums text-tiny text-text-tertiary">{task.progress}%</span>
      </button>
      {/* 进度条 */}
      <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-surface-2">
        <div
          className={`h-full transition-all ${
            task.status === 'failed' || task.status === 'cancelled'
              ? 'bg-danger'
              : task.status === 'success'
                ? 'bg-success'
                : 'bg-info'
          }`}
          style={{ width: `${Math.max(0, Math.min(100, task.progress))}%` }}
        />
      </div>
      {/* meta 第二行 */}
      <div className="mt-1 flex items-center gap-3 text-tiny text-text-tertiary">
        <span>step: {task.step || '—'}</span>
        <span>created: {fmtMs(task.created_at)}</span>
        {task.finished_at && <span>finish: {fmtMs(task.finished_at)}</span>}
      </div>
      {task.error && !expanded && (
        <div className="mt-1 truncate text-tiny text-danger" title={task.error}>
          err: {task.error}
        </div>
      )}

      {/* 展开：实时日志 + 操作按钮 */}
      {expanded && (
        <div className="mt-2 space-y-2 rounded-md bg-surface-2 p-2">
          {task.error && (
            <div className="rounded bg-danger-soft px-2 py-1 text-tiny text-danger">
              {task.error}
            </div>
          )}
          <TaskLogs taskID={task.id} active={inflight} />
          <div className="flex items-center justify-end gap-2 pt-1">
            {inflight && (
              <button
                type="button"
                className="btn btn-outline btn-sm"
                disabled={cancelBusy || task.cancel_requested}
                onClick={onCancel}
              >
                {task.cancel_requested ? '取消中…' : '取消任务'}
              </button>
            )}
            {!inflight && (
              <button
                type="button"
                className="btn btn-ghost btn-sm text-danger"
                disabled={removeBusy}
                onClick={onRemove}
              >
                <Trash2 size={13} /> 删除任务
              </button>
            )}
          </div>
        </div>
      )}
    </li>
  );
}

// TaskLogs 实时日志区。
//
//   - active=true（任务还在跑）：每 2s 轮询，limit=100。
//   - active=false（终态）   ：只拉一次，不再轮询。
function TaskLogs({ taskID, active }: { taskID: number; active: boolean }) {
  const q = useQuery({
    queryKey: ['admin', 'register-tasks', 'logs', taskID],
    queryFn: () => registerTaskApi.logs({ task_id: taskID, limit: 100 }),
    refetchInterval: active ? 2000 : false,
  });
  const logs = q.data?.list ?? [];
  if (q.isLoading) {
    return (
      <div className="flex items-center gap-2 px-1 py-2 text-tiny text-text-tertiary">
        <Loader2 size={12} className="animate-spin" /> 拉取日志…
      </div>
    );
  }
  if (logs.length === 0) {
    return <div className="px-1 py-2 text-tiny text-text-tertiary">暂无日志</div>;
  }
  return (
    <div className="max-h-72 overflow-y-auto rounded bg-surface-1 p-2 font-mono text-tiny leading-relaxed">
      {logs.map((l: RegisterTaskLogEntry) => (
        <div key={l.id} className="flex gap-2">
          <span className="shrink-0 text-text-tertiary">{fmtMs(l.created_at).slice(11)}</span>
          <span className={`shrink-0 uppercase ${levelTone(l.level)}`}>{l.level}</span>
          {l.step && <span className="shrink-0 text-text-tertiary">[{l.step}]</span>}
          <span className="break-all text-text-secondary">{l.message || '—'}</span>
        </div>
      ))}
    </div>
  );
}
