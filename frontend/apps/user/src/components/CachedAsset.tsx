/**
 * CachedAsset.tsx
 *
 * 包装 `<img>` / `<video>` —— 用来访问主控的 `/api/v1/gen/cached/...`：
 *   1) 第一次直接用原 src。后端会 302 到某个边缘节点（cluster.enabled=true 时）。
 *   2) 浏览器加载失败（onError）→ 触发降级流程：
 *        a. 解析失败的 src，把目标位置的 node_id 上报给主控（POST /gen/cached/tainted）；
 *        b. 重新用 `?nocluster=1` 直接走主控本地，恢复显示。
 *
 * 这能让一台边缘节点宕机时，普通用户的浏览仍能自我修复，并且让主控自动知道
 * 「该节点上这条资源不行」，下次别再 302 过去。
 *
 * 注：浏览器对 302 是透明的，img.src 不会在 onError 里告诉我们 Location。所以
 * 我们用一次 `fetch(..., { redirect: 'manual' })` 把真正的 edge URL 拿回来再上报。
 * 这次 fetch 在背景静默执行，不影响重新加载流程。
 */
import { useCallback, useMemo, useState } from 'react';

import { genApi } from '../lib/services';

type CachedAssetCommonProps = {
  /** /api/v1/gen/cached/... 原始 URL（含或不含主机均可）。 */
  src: string;
  /** asset_kind；不填会按路径里的 _thumb 自动判断。 */
  kind?: 'gen' | 'thumb' | 'asset';
  /** 用户态显式失败回调（重试后仍挂掉再触发）。 */
  onFinalError?: () => void;
};

export interface CachedImgProps
  extends Omit<React.ImgHTMLAttributes<HTMLImageElement>, 'src' | 'onError'>,
    CachedAssetCommonProps {}

export interface CachedVideoProps
  extends Omit<React.VideoHTMLAttributes<HTMLVideoElement>, 'src' | 'onError'>,
    CachedAssetCommonProps {}

/** 把任意 src 规整为相对 cached path：`generated/2026/05/14/<task>_<seq>.jpg`。 */
function toRelKey(src: string): string | null {
  try {
    const u = new URL(src, window.location.origin);
    const idx = u.pathname.indexOf('/gen/cached/');
    if (idx < 0) return null;
    return u.pathname.slice(idx + '/gen/cached/'.length).replace(/^\/+/, '');
  } catch {
    return null;
  }
}

function appendNoCluster(src: string): string {
  try {
    const u = new URL(src, window.location.origin);
    u.searchParams.set('nocluster', '1');
    return u.toString();
  } catch {
    return src + (src.includes('?') ? '&' : '?') + 'nocluster=1';
  }
}

/**
 * probeEdgeNode 静默 HEAD 探测 302 目标，提取 node_id。
 *
 * 路径规则与 service.BuildDownloadURL 对齐：
 *   `<scheme>://<edge_host>/d/<node_id>.<encPayload>.<sig>`
 *
 * 失败 / 不是 302 / 解析不出 node_id → 返回 null，调用方继续走 fallback 但不上报。
 */
async function probeEdgeNode(src: string): Promise<string | null> {
  try {
    // redirect: 'manual' 让 fetch 拿到 0-status 的 opaqueredirect Response；
    // 我们不关心 body，只关心 Location，因此用 GET 而不是 HEAD（部分 CDN HEAD 不带 Location）。
    const resp = await fetch(src, { method: 'GET', redirect: 'manual', credentials: 'same-origin' });
    // opaqueredirect 的 type 检测；resp.url 在 manual 模式下通常为空。
    if (resp.type !== 'opaqueredirect') {
      return null;
    }
    // 拿不到 Location header（CORS 限制）→ 退而求其次用第二次 follow 请求。
    const followed = await fetch(src, { method: 'GET', redirect: 'follow', credentials: 'same-origin' });
    if (!followed.url) return null;
    const u = new URL(followed.url);
    const m = u.pathname.match(/^\/d\/([a-zA-Z0-9_\-]+)\./);
    return m && m[1] ? m[1] : null;
  } catch {
    return null;
  }
}

function inferKind(src: string): 'gen' | 'thumb' | 'asset' {
  return /_thumb\./.test(src) ? 'thumb' : 'gen';
}

/**
 * useCachedSrc —— 两段式 src 状态机：
 *   - 初始：使用 props.src
 *   - 第一次出错：标记 failed，把 src 切到 nocluster 旁路；同时背景上报 tainted
 *   - 第二次出错：调用 onFinalError，停止重试
 */
function useCachedSrc({ src, kind, onFinalError }: CachedAssetCommonProps) {
  const [attempt, setAttempt] = useState<0 | 1 | 2>(0);
  const effective = useMemo(() => (attempt === 0 ? src : appendNoCluster(src)), [src, attempt]);

  const handleError = useCallback(async () => {
    if (attempt === 0) {
      setAttempt(1);
      // 背景上报：node_id 拿不到时也不阻塞重试。
      const relKey = toRelKey(src);
      if (relKey) {
        try {
          const nodeID = await probeEdgeNode(src);
          if (nodeID) {
            await genApi.reportTainted({
              asset_kind: kind ?? inferKind(src),
              asset_key: relKey,
              node_id: nodeID,
              reason: 'client_load_error',
            });
          }
        } catch {
          // 上报失败不影响用户态显示
        }
      }
      return;
    }
    if (attempt === 1) {
      setAttempt(2);
      onFinalError?.();
    }
  }, [attempt, src, kind, onFinalError]);

  return { src: effective, onError: handleError, failed: attempt >= 2 };
}

/** CachedImg —— 替代普通 `<img src="/api/v1/gen/cached/...">`，自带降级 + tainted 上报。 */
export function CachedImg({ src, kind, onFinalError, ...rest }: CachedImgProps) {
  const { src: effective, onError } = useCachedSrc({ src, kind, onFinalError });
  return <img {...rest} src={effective} onError={onError} />;
}

/** CachedVideo —— 同上，替代 `<video src="/api/v1/gen/cached/...">`。 */
export function CachedVideo({ src, kind, onFinalError, ...rest }: CachedVideoProps) {
  const { src: effective, onError } = useCachedSrc({ src, kind, onFinalError });
  return <video {...rest} src={effective} onError={onError} />;
}

/**
 * 兼容入口：旧组件经常持有完整 URL 串，这里给它一个工厂方便就地替换。
 * 用法：`<img src={fallbackCachedSrc(url)} onError={...}/>` 不上报，但仍尝试旁路。
 */
export function fallbackCachedSrc(src: string): string {
  return appendNoCluster(src);
}
