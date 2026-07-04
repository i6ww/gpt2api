import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  Copy,
  Download,
  ImageIcon,
  Images,
  Loader2,
  MoreHorizontal,
  Music,
  Play,
  Trash2,
  Video as VideoIcon,
  X,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { fmtPoints, fmtRelative } from '../../lib/format';
import { loadToken } from '../../lib/api';
import { genApi } from '../../lib/services';
import { toast } from '../../stores/toast';
import type { GenerationTask, TaskStatus } from '../../lib/types';

/**
 * 把后端返回的 cached 资源路径补成完整 URL，便于复制后能直接在浏览器 / Markdown 里打开。
 * 兼容三种入参：
 *   - 已经是绝对 URL ("https://..." 或 "http://...")：原样返回
 *   - 相对路径 ("/api/v1/gen/cached/...")：拼上当前 origin
 *   - data:/blob: URL：原样返回
 */
function absolutizeAssetUrl(src: string): string {
  const s = String(src || '').trim();
  if (!s) return s;
  if (/^(https?:|data:|blob:)/i.test(s)) return s;
  if (typeof window !== 'undefined' && window.location?.origin) {
    return window.location.origin.replace(/\/$/, '') + (s.startsWith('/') ? s : '/' + s);
  }
  return s;
}

const PAGE_SIZE = 20;

// status / filter / delete-scope 标签的 i18n key（实际渲染时 t(key)）。
// 标签不能再用 module-level const，否则切换语言后不会刷新。改成函数式。
const STATUS_KEY: Record<TaskStatus, string> = {
  0: 'history.status_pending',
  1: 'history.status_running',
  2: 'history.status_succeeded',
  3: 'history.status_failed',
  4: 'history.status_refunded',
  5: 'history.status_canceled',
};

const STATUS_BADGE: Record<TaskStatus, string> = {
  0: 'badge',
  1: 'badge badge-klein',
  2: 'badge badge-success',
  3: 'badge badge-danger',
  4: 'badge badge-warning',
  5: 'badge',
};

type Filter = 'all' | 'image' | 'video' | 'music';
type DeleteScope = 'failed' | 'before_3d' | 'before_7d' | 'all';

const FILTERS: Array<{ value: Filter; labelKey: string }> = [
  { value: 'all', labelKey: 'history.filter_all' },
  { value: 'image', labelKey: 'history.filter_image' },
  { value: 'video', labelKey: 'history.filter_video' },
  { value: 'music', labelKey: 'history.filter_music' },
];

const DELETE_ACTIONS: Array<{ scope: DeleteScope; labelKey: string; hintKey: string }> = [
  { scope: 'failed', labelKey: 'history.del_failed', hintKey: 'history.del_failed_hint' },
  { scope: 'before_3d', labelKey: 'history.del_before_3d', hintKey: 'history.del_before_3d_hint' },
  { scope: 'before_7d', labelKey: 'history.del_before_7d', hintKey: 'history.del_before_7d_hint' },
  { scope: 'all', labelKey: 'history.del_all', hintKey: 'history.del_all_hint' },
];

export default function HistoryPage() {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<Filter>('all');
  const [page, setPage] = useState(1);
  const [menuOpen, setMenuOpen] = useState(false);
  const [busyScope, setBusyScope] = useState<DeleteScope | null>(null);
  const [confirmScope, setConfirmScope] = useState<DeleteScope | null>(null);
  const [preview, setPreview] = useState<HistoryPreview | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  const q = useQuery({
    queryKey: ['gen.history', filter, page],
    queryFn: () =>
      genApi.history({ kind: filter === 'all' ? undefined : filter, page, page_size: PAGE_SIZE }),
  });

  const items = q.data?.list ?? [];
  const total = q.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  useEffect(() => {
    const onDocClick = (ev: MouseEvent) => {
      if (!menuRef.current) return;
      if (!menuRef.current.contains(ev.target as Node)) setMenuOpen(false);
    };
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  }, []);

  const handleDelete = async (scope: DeleteScope) => {
    setBusyScope(scope);
    try {
      await genApi.deleteHistory(scope);
      setPage(1);
      await q.refetch();
    } finally {
      setBusyScope(null);
      setMenuOpen(false);
    }
  };

  return (
    <div className="page">
      <header className="page-header items-start gap-4">
        <div className="space-y-2">
          <h1 className="page-title">{t('history.title')}</h1>
          <p className="page-subtitle">{t('history.subtitle')}</p>
        </div>

        <div className="flex items-center gap-3">
          <div className="tabs">
            {FILTERS.map((f) => (
              <button
                key={f.value}
                type="button"
                className="tab"
                aria-selected={filter === f.value}
                onClick={() => {
                  setFilter(f.value);
                  setPage(1);
                }}
              >
                {t(f.labelKey)}
              </button>
            ))}
          </div>

          <div className="relative" ref={menuRef}>
            <button
              type="button"
              className="btn btn-outline btn-md gap-2"
              onClick={() => setMenuOpen((v) => !v)}
            >
              <MoreHorizontal size={16} />
              {t('history.manage_btn')}
            </button>
            {menuOpen && (
              <div className="absolute right-0 top-[calc(100%+8px)] z-30 w-48 rounded-xl border border-border bg-surface-1 p-2 shadow-lg">
                {DELETE_ACTIONS.map((item) => (
                  <button
                    key={item.scope}
                    type="button"
                    className={clsx(
                      'flex w-full items-start gap-3 rounded-lg px-3 py-2 text-left text-sm transition hover:bg-surface-2',
                      busyScope === item.scope && 'opacity-60 pointer-events-none',
                    )}
                    onClick={() => setConfirmScope(item.scope)}
                  >
                    <Trash2 size={16} className="mt-0.5 text-text-tertiary" />
                    <span className="flex-1">
                      <span className="block text-text-primary">{t(item.labelKey)}</span>
                      <span className="block text-xs text-text-tertiary">{t(item.hintKey)}</span>
                    </span>
                    {busyScope === item.scope && <Loader2 size={14} className="animate-spin" />}
                  </button>
                ))}
              </div>
            )}
          </div>

          <button
            type="button"
            className="btn btn-outline btn-md"
            onClick={() => q.refetch()}
            disabled={q.isFetching}
          >
            <Loader2 size={16} className={clsx(q.isFetching && 'animate-spin')} />
            {t('history.refresh_btn')}
          </button>
        </div>
      </header>

      <section className="mb-4 flex items-center justify-between text-sm text-text-tertiary">
        <span>{t('history.total_records', { n: total })}</span>
        <span>{t('common.page_x_of_y', { page, total: totalPages })}</span>
      </section>

      {q.isLoading && (
        <div className="grid place-items-center py-20 text-text-tertiary">
          <Loader2 className="animate-spin" size={28} />
        </div>
      )}

      {!q.isLoading && q.error && (
        <div className="card">
          <div className="empty-state">
            <span className="empty-state-icon">
              <Trash2 size={22} />
            </span>
            <p className="empty-state-title">{t('history.load_failed_title')}</p>
            <p className="empty-state-desc">{t('history.load_failed_desc')}</p>
          </div>
        </div>
      )}

      {!q.isLoading && !q.error && items.length === 0 && (
        <div className="card">
          <div className="empty-state">
            <span className="empty-state-icon">
              <Images size={22} />
            </span>
            <p className="empty-state-title">{t('history.empty_title')}</p>
            <p className="empty-state-desc">{t('history.empty_desc')}</p>
          </div>
        </div>
      )}

      {!q.isLoading && items.length > 0 && (
        <>
          <div className="grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(min(240px,100%),1fr))]">
            {items.map((task) => (
              <TaskCard key={task.task_id} t={task} onPreview={() => setPreview(createPreview(task))} />
            ))}
          </div>

          <div className="mt-6 flex items-center justify-center gap-3">
            <button
              className="btn btn-outline btn-md"
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page <= 1 || q.isFetching}
            >
              {t('common.prev_page')}
            </button>
            <button
              className="btn btn-outline btn-md"
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page >= totalPages || q.isFetching}
            >
              {t('common.next_page')}
            </button>
          </div>
        </>
      )}

      {preview && <PreviewModal preview={preview} onClose={() => setPreview(null)} />}
      {confirmScope && (
        <DeleteConfirmDialog
          scope={confirmScope}
          loading={busyScope === confirmScope}
          onClose={() => setConfirmScope(null)}
          onConfirm={async () => {
            const scope = confirmScope;
            if (!scope) return;
            setConfirmScope(null);
            await handleDelete(scope);
          }}
        />
      )}
    </div>
  );
}

function TaskCard({ t, onPreview }: { t: GenerationTask; onPreview: () => void }) {
  const { t: tt } = useTranslation();
  const primary = t.results?.[0];
  const isVideo = t.kind === 'video';
  const isMusic = t.kind === 'music';
  // 音乐主链是音频，封面在 thumb_url；非音乐场景沿用原逻辑（thumb 优先，无 thumb 回退主链）。
  const cover = isMusic ? primary?.thumb_url || '' : primary?.thumb_url || primary?.url || '';
  const resolvedCover = useAuthedMediaUrl(cover);
  const error = t.status === 3 ? t.error?.trim() || tt('history.fail_default') : '';
  const spec = formatGenerationSpec(t.resolution || primary?.resolution, t.aspect_ratio || primary?.aspect_ratio);

  return (
    <article
      className="group overflow-hidden rounded-lg border border-border bg-surface-1 transition hover:-translate-y-0.5 hover:shadow-glow-soft"
      role="button"
      tabIndex={0}
      onClick={onPreview}
      onKeyDown={(ev) => {
        if (ev.key === 'Enter' || ev.key === ' ') onPreview();
      }}
    >
      <div className="relative aspect-square overflow-hidden bg-klein-gradient-soft" style={{ contain: 'paint' }}>
        {resolvedCover ? (
          isVideo || isMusic ? (
            <div className="relative h-full w-full">
              <img src={resolvedCover} alt="" className="h-full w-full object-cover" loading="lazy" />
              <div className="absolute inset-0 grid place-items-center bg-black/10 opacity-100 transition group-hover:bg-black/20">
                <span className="flex h-12 w-12 items-center justify-center rounded-full bg-black/70 text-white">
                  {isMusic ? <Music size={18} /> : <Play size={18} className="ml-0.5" fill="currentColor" />}
                </span>
              </div>
            </div>
          ) : (
            <img src={resolvedCover} alt="" className="h-full w-full object-cover" loading="lazy" />
          )
        ) : (
          <div className="flex h-full w-full items-center justify-center text-text-tertiary">
            {isVideo ? <VideoIcon size={28} /> : isMusic ? <Music size={28} /> : <ImageIcon size={28} />}
          </div>
        )}
        {t.status === 1 && (
          <div className="absolute inset-x-0 bottom-0 progress">
            <div className="progress-bar" style={{ width: `${t.progress}%` }} />
          </div>
        )}
        {t.status === 3 && (
          <div className="absolute inset-0 flex items-end bg-black/15 p-3">
            <span className="line-clamp-2 rounded-md bg-black/60 px-2 py-1 text-xs text-white">
              {error}
            </span>
          </div>
        )}
      </div>

      <div className="space-y-2 p-3">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm text-text-primary">{t.model}</span>
          <span className={clsx(STATUS_BADGE[t.status])}>{tt(STATUS_KEY[t.status])}</span>
        </div>
        {spec && <div className="text-xs text-text-secondary">{spec}</div>}
        <div className="flex items-center justify-between text-xs text-text-tertiary">
          <span>{fmtRelative(t.created_at)}</span>
          <span>{fmtPoints(t.cost_points)} {tt('common.points')}</span>
        </div>
      </div>
    </article>
  );
}

function PreviewModal({ preview, onClose }: { preview: HistoryPreview; onClose: () => void }) {
  const { t } = useTranslation();
  const blobUrl = useAuthedMediaUrl(preview.src);
  const coverUrl = useAuthedMediaUrl(preview.cover);
  const [copying, setCopying] = useState(false);
  const [downloading, setDownloading] = useState(false);

  useEffect(() => {
    const onKeyDown = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  const handleCopy = async () => {
    setCopying(true);
    try {
      const full = absolutizeAssetUrl(preview.src);
      await navigator.clipboard.writeText(full);
      toast.success(t('common.copied'));
    } catch {
      toast.error(t('history.copy_failed'));
    } finally {
      setCopying(false);
    }
  };

  const handleDownload = async () => {
    setDownloading(true);
    try {
      const file = await fetchAuthedFile(preview.src);
      const url = URL.createObjectURL(file.blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = file.filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } finally {
      setDownloading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div
        className="w-full max-w-5xl overflow-hidden rounded-2xl border border-border bg-surface-1 shadow-2xl"
        onClick={(ev) => ev.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <div className="min-w-0">
            <p className="truncate text-sm text-text-primary">{preview.model}</p>
            <p className="text-xs text-text-tertiary">
              {fmtRelative(preview.created_at)} · {t(STATUS_KEY[preview.status])} · {fmtPoints(preview.cost_points)} {t('common.points')}
              {preview.spec ? ` · ${preview.spec}` : ''}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button className="btn btn-outline btn-sm gap-2" onClick={handleCopy} disabled={copying}>
              {copying ? <Loader2 size={14} className="animate-spin" /> : <Copy size={14} />}
              {t('history.copy_link')}
            </button>
            <button className="btn btn-outline btn-sm gap-2" onClick={handleDownload} disabled={downloading}>
              {downloading ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
              {t('history.download')}
            </button>
            <button className="btn btn-outline btn-sm" onClick={onClose}>
              <X size={14} />
            </button>
          </div>
        </div>

        <div className="bg-black/5 p-4">
          <div className="flex max-h-[75vh] min-h-[360px] items-center justify-center overflow-auto rounded-xl bg-surface-0">
            {preview.kind === 'video' ? (
              blobUrl ? (
                <video src={blobUrl} controls className="max-h-[75vh] w-full object-contain" />
              ) : (
                <div className="flex flex-col items-center gap-2 py-20 text-text-tertiary">
                  <Loader2 className="animate-spin" size={24} />
                  <span className="text-sm">{t('history.loading_video')}</span>
                </div>
              )
            ) : preview.kind === 'music' ? (
              <div className="flex w-full flex-col items-center gap-6 px-6 py-10">
                <div className="aspect-square w-full max-w-[320px] overflow-hidden rounded-2xl bg-surface-2 shadow-lg">
                  {coverUrl ? (
                    <img src={coverUrl} alt="" className="h-full w-full object-cover" />
                  ) : (
                    <div className="grid h-full w-full place-items-center text-text-tertiary">
                      <Music size={48} />
                    </div>
                  )}
                </div>
                {preview.title && <p className="text-base font-medium text-text-primary">{preview.title}</p>}
                {blobUrl ? (
                  <audio src={blobUrl} controls autoPlay className="w-full max-w-md" />
                ) : (
                  <div className="flex items-center gap-2 text-text-tertiary">
                    <Loader2 className="animate-spin" size={20} />
                    <span className="text-sm">{t('history.loading_audio')}</span>
                  </div>
                )}
              </div>
            ) : blobUrl ? (
              <img src={blobUrl} alt={preview.prompt || preview.model} className="max-h-[75vh] w-full object-contain" />
            ) : (
              <div className="flex flex-col items-center gap-2 py-20 text-text-tertiary">
                <Loader2 className="animate-spin" size={24} />
                <span className="text-sm">{t('history.loading_image')}</span>
              </div>
            )}
          </div>
        </div>

        <div className="border-t border-border px-4 py-3 text-sm text-text-tertiary">
          <p className="line-clamp-2">{preview.prompt || t('common.no_prompt')}</p>
        </div>
      </div>
    </div>
  );
}

function useAuthedMediaUrl(src?: string) {
  const [url, setUrl] = useState<string>('');

  useEffect(() => {
    if (!src) {
      setUrl('');
      return;
    }
    if (src.startsWith('data:')) {
      setUrl(src);
      return;
    }

    let alive = true;
    let objectUrl = '';

    (async () => {
      try {
        const token = loadToken();
        const headers: Record<string, string> = {};
        if (token?.access) headers.Authorization = `${token.type || 'Bearer'} ${token.access}`;
        let resp: Response;
        try {
          resp = await fetch(src, { headers, credentials: 'include' });
          if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        } catch (err) {
          // 主路径失败：可能是边缘节点宕机；切到 ?nocluster=1 直连主控本地，
          // 同时背景上报 tainted（拿到 resp.url 才知道 node_id；这里只能 best-effort）。
          reportClusterFailure(src).catch(() => undefined);
          const bypass = appendQuery(src, 'nocluster', '1');
          resp = await fetch(bypass, { headers, credentials: 'include' });
          if (!resp.ok) throw new Error(`HTTP ${resp.status} (fallback)`);
        }
        const blob = await resp.blob();
        if (!alive) return;
        objectUrl = URL.createObjectURL(blob);
        setUrl(objectUrl);
      } catch {
        if (alive) setUrl('');
      }
    })();

    return () => {
      alive = false;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [src]);

  return url;
}

/**
 * appendQuery 等价 URLSearchParams.set 的最小实现；
 * 不依赖 URL 解析，能容忍 src 是相对路径。
 */
function appendQuery(src: string, key: string, value: string): string {
  const sep = src.includes('?') ? '&' : '?';
  return `${src}${sep}${encodeURIComponent(key)}=${encodeURIComponent(value)}`;
}

/**
 * reportClusterFailure 用户态下载失败时背景上报。
 *
 * 流程：
 *   1) 同 origin 再发一次 GET，跟随重定向 → resp.url 暴露真正的 edge URL
 *   2) 从 edge URL 解析 `/d/<node_id>.<...>`
 *   3) 调 genApi.reportTainted(asset_kind / asset_key / node_id)
 *
 * 任何一步失败都静默吞掉，避免影响主流程；最差也只是控制面 GC 之前会再多 302 一次。
 */
async function reportClusterFailure(src: string): Promise<void> {
  let assetKey: string;
  try {
    const u = new URL(src, window.location.origin);
    const idx = u.pathname.indexOf('/gen/cached/');
    if (idx < 0) return;
    assetKey = u.pathname.slice(idx + '/gen/cached/'.length).replace(/^\/+/, '');
  } catch {
    return;
  }
  if (!assetKey) return;
  let nodeID = '';
  try {
    const followed = await fetch(src, { method: 'GET', redirect: 'follow', credentials: 'same-origin' });
    if (followed.url) {
      const finalU = new URL(followed.url);
      const m = finalU.pathname.match(/^\/d\/([a-zA-Z0-9_\-]+)\./);
      if (m && m[1]) nodeID = m[1];
    }
    // 完成后立刻丢弃 body，避免内存占用
    try {
      await followed.body?.cancel();
    } catch {
      // ignore
    }
  } catch {
    return;
  }
  if (!nodeID) return;
  try {
    await genApi.reportTainted({
      asset_kind: /_thumb\./.test(assetKey) ? 'thumb' : 'gen',
      asset_key: assetKey,
      node_id: nodeID,
      reason: 'history_authed_media_failed',
    });
  } catch {
    // 上报失败不影响 UX
  }
}

async function fetchAuthedFile(src: string) {
  const token = loadToken();
  const headers: Record<string, string> = {};
  if (token?.access) headers.Authorization = `${token.type || 'Bearer'} ${token.access}`;
  const resp = await fetch(src, { headers, credentials: 'include' });
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
  const blob = await resp.blob();
  return {
    blob,
    filename: guessFilename(src, resp.headers.get('content-type') || blob.type),
  };
}

function guessFilename(src: string, contentType: string) {
  const ext = guessExt(contentType, src);
  const cleanSrc = src.replace(/\?.*$/, '');
  const base = cleanSrc.replace(/^.*\//, '').replace(/[^a-zA-Z0-9._-]+/g, '_').slice(0, 80);
  return `${base || 'generation'}${ext}`;
}

function guessExt(contentType: string, src: string) {
  const lower = `${contentType} ${src}`.toLowerCase();
  if (lower.includes('video/mp4') || lower.includes('.mp4')) return '.mp4';
  if (lower.includes('video/webm') || lower.includes('.webm')) return '.webm';
  if (lower.includes('audio/mpeg') || lower.includes('audio/mp3') || lower.includes('.mp3')) return '.mp3';
  if (lower.includes('audio/mp4') || lower.includes('audio/x-m4a') || lower.includes('audio/aac') || lower.includes('.m4a')) return '.m4a';
  if (lower.includes('audio/wav') || lower.includes('.wav')) return '.wav';
  if (lower.includes('audio/ogg') || lower.includes('.ogg')) return '.ogg';
  if (lower.includes('image/png') || lower.includes('.png')) return '.png';
  if (lower.includes('image/jpeg') || lower.includes('image/jpg') || lower.includes('.jpg') || lower.includes('.jpeg')) return '.jpg';
  if (lower.includes('image/webp') || lower.includes('.webp')) return '.webp';
  return '';
}

function createPreview(t: GenerationTask): HistoryPreview {
  const first = t.results?.[0];
  const isMusic = t.kind === 'music';
  const meta = (first?.meta || {}) as Record<string, unknown>;
  return {
    kind: t.kind,
    status: t.status,
    model: t.model,
    prompt: t.prompt || '',
    cost_points: t.cost_points,
    created_at: t.created_at,
    error: t.error,
    // 音乐：主链(src)= 音频，封面(cover)= thumb_url；其它沿用原逻辑。
    src: isMusic ? first?.url || '' : first?.url || first?.thumb_url || '',
    cover: isMusic ? first?.thumb_url || '' : '',
    title: isMusic ? (typeof meta.title === 'string' ? meta.title : '') : '',
    spec: formatGenerationSpec(t.resolution || first?.resolution, t.aspect_ratio || first?.aspect_ratio),
  };
}

function formatGenerationSpec(resolution?: string, aspectRatio?: string): string {
  return [resolution, aspectRatio].filter(Boolean).join(' · ');
}

interface HistoryPreview {
  kind: 'image' | 'video' | 'chat' | 'music';
  status: TaskStatus;
  model: string;
  prompt: string;
  cost_points: number;
  created_at: number;
  error?: string;
  src: string;
  cover?: string;
  title?: string;
  spec?: string;
}

function DeleteConfirmDialog({
  scope,
  loading,
  onClose,
  onConfirm,
}: {
  scope: DeleteScope;
  loading: boolean;
  onClose: () => void;
  onConfirm: () => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const item = DELETE_ACTIONS.find((a) => a.scope === scope)!;
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/55 p-4" onClick={onClose}>
      <div
        className="dialog-surface w-full max-w-md overflow-hidden rounded-2xl border border-border bg-surface-1 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start gap-3 border-b border-border px-5 py-4">
          <div className="grid h-10 w-10 place-items-center rounded-full bg-danger-soft text-danger">
            <Trash2 size={18} />
          </div>
          <div className="min-w-0 flex-1">
            <h2 className="text-h4 text-text-primary">{t(item.labelKey)}</h2>
            <p className="mt-1 text-small text-text-tertiary">{t('history.del_confirm_desc')}</p>
          </div>
          <button type="button" className="btn btn-ghost btn-icon btn-sm" onClick={onClose}>
            <X size={16} />
          </button>
        </div>
        <div className="px-5 py-4">
          <p className="text-small text-text-secondary">{t(item.hintKey)}</p>
          <div className="mt-5 flex justify-end gap-2">
            <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
              {t('common.cancel')}
            </button>
            <button type="button" className="btn btn-danger btn-md gap-2" onClick={onConfirm} disabled={loading}>
              {loading ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
              {t('history.del_confirm_btn')}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
