import { useEffect, useMemo, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { AlertCircle, AlertTriangle, ArrowUpRight, Bell, CheckCircle2, X } from 'lucide-react';
import clsx from 'clsx';

import { announcementsApi } from '../lib/services';
import type { Announcement } from '../lib/types';

/* ============================================================================
 * AnnouncementBanner
 *
 * 用户端顶部公告条：
 *   1. 单条公告：静止显示，整条可点击 → 弹窗看完整内容。
 *   2. 多条公告：横幅高 36px = 单行；内部 track 把所有条纵向首尾排列，
 *      每 4 秒 translateY 上移 1 行，多条「上下滚动」无缝循环。
 *   3. 配色按当前显示条的 level 走（info=灰、success=绿、warning=橙、danger=红）。
 *   4. 点击横幅任意位置（除右上 X）→ 弹出 Dialog，展示全部条目的标题 / 正文 / 链接。
 *   5. 右上 X：把当前可见的所有 id 写到 localStorage.dismissed，本机不再出现该批；
 *      运营发新公告（id 不在 dismissed 集合）会重新出现。
 *   6. 每 60 秒拉一次新公告 + window focus 强刷，1 分钟内全网生效。
 *
 * 切换实现：
 *   - track 高度 = items.length × ROW_HEIGHT；外层 wrapper height = ROW_HEIGHT + overflow hidden
 *   - 每 4s setActiveIdx((i)=>(i+1)%n)，track 用 transform: translateY(-idx*ROW)
 *   - 走到末尾再回 0 时禁用 transition，瞬间归位避免「倒退」视觉
 * ========================================================================= */

const DISMISS_KEY = 'klein.dismissed_announcements';
const ROTATE_INTERVAL_MS = 4000;
const ROW_HEIGHT = 36; // px，与外层 h-9 (36px) 对齐

function loadDismissed(): Set<number> {
  try {
    const raw = localStorage.getItem(DISMISS_KEY);
    if (!raw) return new Set();
    const arr = JSON.parse(raw);
    return new Set(Array.isArray(arr) ? arr.filter((x) => typeof x === 'number') : []);
  } catch {
    return new Set();
  }
}

function saveDismissed(ids: Set<number>) {
  try {
    localStorage.setItem(DISMISS_KEY, JSON.stringify([...ids]));
  } catch {
    /* ignore */
  }
}

type LevelTheme = { wrap: string; icon: typeof Bell; chip: string };

function levelTheme(level: Announcement['level']): LevelTheme {
  switch (level) {
    case 'success':
      return {
        wrap: 'border-success/30 bg-success/8 text-success',
        icon: CheckCircle2,
        chip: 'bg-success/15 text-success',
      };
    case 'warning':
      return {
        wrap: 'border-warning/30 bg-warning/10 text-warning',
        icon: AlertTriangle,
        chip: 'bg-warning/15 text-warning',
      };
    case 'danger':
      return {
        wrap: 'border-danger/30 bg-danger/10 text-danger',
        icon: AlertCircle,
        chip: 'bg-danger/15 text-danger',
      };
    case 'info':
    default:
      return {
        wrap: 'border-border bg-surface-2 text-text-primary',
        icon: Bell,
        chip: 'bg-surface-3 text-text-secondary',
      };
  }
}

export function AnnouncementBanner() {
  const { t } = useTranslation();
  const [dismissed, setDismissed] = useState<Set<number>>(() => loadDismissed());
  const [activeIdx, setActiveIdx] = useState(0);
  const [modalOpen, setModalOpen] = useState(false);
  // 切换到首条时禁用动画（瞬间归位），避免循环跳回时出现反向回滚动画。
  const [noTransition, setNoTransition] = useState(false);

  const { data } = useQuery({
    queryKey: ['public.announcements'],
    queryFn: () => announcementsApi.list(),
    staleTime: 60_000,
    refetchInterval: 60_000,
    refetchOnWindowFocus: true,
  });

  // 过滤已 dismiss + 按 pinned 优先 / sort_order 升序 / id 降序排序。
  const visible = useMemo(() => {
    const rows = (data ?? []).filter((a) => !dismissed.has(a.id));
    return rows.slice().sort((a, b) => {
      if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
      if (a.sort_order !== b.sort_order) return a.sort_order - b.sort_order;
      return b.id - a.id;
    });
  }, [data, dismissed]);

  // 列表变短时把 activeIdx 拉回 0，避免越界。
  useEffect(() => {
    if (activeIdx >= visible.length) setActiveIdx(0);
  }, [visible.length, activeIdx]);

  // 自动滚动：≥2 条且弹窗未打开时启动；弹窗里有完整列表，那时暂停轮播。
  useEffect(() => {
    if (visible.length <= 1 || modalOpen) return;
    const t = window.setInterval(() => {
      setActiveIdx((i) => (i + 1) % visible.length);
    }, ROTATE_INTERVAL_MS);
    return () => window.clearInterval(t);
  }, [visible.length, modalOpen]);

  // activeIdx 回到 0 且不是初始状态时（即从末条循环回首条），先关掉 transition 瞬移，
  // 一帧后再恢复 transition；避免 translateY(-(n-1)*36) → translateY(0) 反向滚回的尴尬。
  const prevIdx = useRef(0);
  useEffect(() => {
    const prev = prevIdx.current;
    prevIdx.current = activeIdx;
    if (activeIdx === 0 && prev > 0) {
      setNoTransition(true);
      const t = window.setTimeout(() => setNoTransition(false), 30);
      return () => window.clearTimeout(t);
    }
  }, [activeIdx]);

  if (visible.length === 0) return null;

  const cur = visible[activeIdx] ?? visible[0]!;
  const theme = levelTheme(cur.level);
  const Icon = theme.icon;

  const dismissAll = (e: React.MouseEvent) => {
    e.stopPropagation();
    const next = new Set(dismissed);
    for (const a of visible) next.add(a.id);
    saveDismissed(next);
    setDismissed(next);
  };

  return (
    <>
      <div
        className={clsx(
          'announcement-banner relative flex h-9 items-center gap-2 overflow-hidden border-b px-3 text-small leading-none cursor-pointer transition-colors',
          theme.wrap,
        )}
        role="button"
        tabIndex={0}
        aria-label={t('announcement.aria_label', { title: cur.title, count: visible.length })}
        onClick={() => setModalOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            setModalOpen(true);
          }
        }}
      >
        <Icon size={14} className="shrink-0 opacity-90" />

        {/* ticker viewport：高度固定为单行高度，overflow:hidden；内部 track 纵向上推。 */}
        <div className="relative min-w-0 flex-1 overflow-hidden" style={{ height: ROW_HEIGHT }}>
          <div
            className="absolute inset-x-0 top-0"
            style={{
              transform: `translateY(-${activeIdx * ROW_HEIGHT}px)`,
              transition: noTransition ? 'none' : 'transform 500ms ease',
            }}
          >
            {visible.map((a) => (
              <div
                key={a.id}
                className="flex items-center truncate"
                style={{ height: ROW_HEIGHT }}
                title={a.title}
              >
                {a.pinned && (
                  <span className={clsx('mr-1.5 inline-block rounded px-1 py-px text-[10px]', levelTheme(a.level).chip)}>
                    {t('announcement.pinned')}
                  </span>
                )}
                <span className="font-medium">{a.title}</span>
                {a.content && a.content !== a.title && (
                  <span className="ml-2 truncate opacity-75">— {a.content}</span>
                )}
              </div>
            ))}
          </div>
        </div>

        {visible.length > 1 && (
          <span className="hidden shrink-0 text-tiny opacity-60 sm:inline">
            {activeIdx + 1}/{visible.length}
          </span>
        )}
        <span className="shrink-0 text-tiny opacity-60 hidden md:inline">{t('announcement.click_to_view')}</span>
        <button
          type="button"
          className="-mr-1 grid h-7 w-7 shrink-0 place-items-center rounded-full opacity-60 transition hover:bg-black/5 hover:opacity-100 dark:hover:bg-white/10"
          title={t('announcement.close')}
          aria-label={t('announcement.close')}
          onClick={dismissAll}
        >
          <X size={14} />
        </button>
      </div>

      {modalOpen && (
        <AnnouncementDialog items={visible} onClose={() => setModalOpen(false)} />
      )}
    </>
  );
}

function AnnouncementDialog({ items, onClose }: { items: Announcement[]; onClose: () => void }) {
  const { t } = useTranslation();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/45 p-4 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      onMouseDown={onClose}
    >
      <div
        className="w-full max-w-2xl overflow-hidden rounded-[22px] border border-border bg-surface-1 shadow-3"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b border-border px-5 py-4">
          <div>
            <h2 className="text-[18px] font-medium text-text-primary">{t('announcement.modal_title')}</h2>
            <p className="mt-1 text-small text-text-tertiary">{t('announcement.total_count', { n: items.length })}</p>
          </div>
          <button
            type="button"
            className="grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2 hover:text-text-primary"
            onClick={onClose}
            aria-label={t('common.close')}
            title={t('common.close')}
          >
            <X size={16} />
          </button>
        </header>

        <div className="max-h-[70vh] space-y-3 overflow-auto p-5">
          {items.map((item) => {
            const theme = levelTheme(item.level);
            const Icon = theme.icon;
            const hasLink = item.link_url && item.link_url.trim() !== '';
            const linkText = (item.link_text && item.link_text.trim()) || t('announcement.view_detail');
            return (
              <article key={item.id} className="rounded-[16px] border border-border bg-surface-2 p-4">
                <div className="flex items-start gap-3">
                  <span className={clsx('mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-full', theme.chip)}>
                    <Icon size={15} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      {item.pinned && (
                        <span className={clsx('rounded px-1.5 py-0.5 text-[10px] font-medium', theme.chip)}>
                          {t('announcement.pinned')}
                        </span>
                      )}
                      <h3 className="text-[15px] font-medium leading-6 text-text-primary">{item.title}</h3>
                    </div>
                    {item.content && (
                      <p className="mt-2 whitespace-pre-wrap break-words text-small leading-7 text-text-secondary">
                        {item.content}
                      </p>
                    )}
                    {hasLink && (
                      <a
                        className="mt-3 inline-flex items-center gap-1 rounded-full border border-border px-3 py-1.5 text-small font-medium text-klein-600 hover:bg-surface-1"
                        href={item.link_url}
                        target={item.link_url!.startsWith('http') ? '_blank' : undefined}
                        rel="noreferrer"
                      >
                        {linkText}
                        <ArrowUpRight size={13} />
                      </a>
                    )}
                  </div>
                </div>
              </article>
            );
          })}
        </div>
      </div>
    </div>
  );
}