// 格式化工具。所有点数后端均按 *100 存储。

import i18n from '../i18n';

/** 当前 i18n 语言是否为中文（包括 zh / zh-CN / zh-TW 等）。 */
function isZh(): boolean {
  return (i18n.language || 'zh').toLowerCase().startsWith('zh');
}

/** 数字格式：跟随语言切 locale，避免英文下也显示中文千位分隔符。 */
function numberFmt(): Intl.NumberFormat {
  return new Intl.NumberFormat(isZh() ? 'zh-CN' : 'en-US', {
    minimumFractionDigits: 0,
    maximumFractionDigits: 2,
  });
}

/** 后端 points（*100） → 展示数值 */
export function fmtPoints(p: number | undefined | null): string {
  if (p == null) return '0';
  const v = p / 100;
  return numberFmt().format(v);
}

/** 后端 unix 秒 → 本地化时间字符串 */
export function fmtTime(ts: number | undefined | null): string {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleString(isZh() ? 'zh-CN' : 'en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/** 后端 unix 秒 → 相对时间（如「4 分钟前」/「4 minutes ago」）。
 *  跟随 i18n 语言切换；超过 7 天回退到 fmtTime 的本地化时间串。
 */
export function fmtRelative(ts: number | undefined | null): string {
  if (!ts) return '—';
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return i18n.t('format.just_now');
  if (diff < 3600) return i18n.t('format.minutes_ago', { n: Math.floor(diff / 60) });
  if (diff < 86400) return i18n.t('format.hours_ago', { n: Math.floor(diff / 3600) });
  if (diff < 86400 * 7) return i18n.t('format.days_ago', { n: Math.floor(diff / 86400) });
  return fmtTime(ts);
}

// 业务类型 → i18n key 映射。调用方拿到 key 后用 t() 翻译，老 fmtBiz 已废弃。
// 没命中映射时返回 ""，调用方应该 fallback 到原始 biz_type 字符串。
const BIZ_LABEL_KEY: Record<string, string> = {
  recharge: 'billing.biz_recharge',
  cdk: 'billing.biz_cdk',
  promo: 'billing.biz_promo',
  invite: 'billing.biz_invite',
  refund: 'billing.biz_refund',
  consume: 'billing.biz_consume',
  freeze: 'billing.biz_freeze',
  unfreeze: 'billing.biz_unfreeze',
  grant: 'billing.biz_grant',
};

/** 返回 biz_type 对应的 i18n key；没命中返回 ""。 */
export function bizLabelKey(biz: string): string {
  return BIZ_LABEL_KEY[biz] ?? '';
}

export function pointsClass(direction: number): string {
  return direction > 0 ? 'text-success' : 'text-danger';
}
