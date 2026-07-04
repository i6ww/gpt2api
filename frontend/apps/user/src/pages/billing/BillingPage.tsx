import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Copy, Gift, Mail, Sparkles, Wallet, X } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ApiError } from '../../lib/api';
import { bizLabelKey, fmtPoints, fmtTime, pointsClass } from '../../lib/format';
import { billingApi } from '../../lib/services';
import type { RechargeProduct } from '../../lib/types';
import { useAuthStore } from '../../stores/auth';
import { toast } from '../../stores/toast';

export default function BillingPage() {
  const { t } = useTranslation();
  const me = useAuthStore((s) => s.me);
  const refreshMe = useAuthStore((s) => s.refreshMe);
  const qc = useQueryClient();

  const [page, setPage] = useState(1);
  const logsQ = useQuery({
    queryKey: ['billing.logs', page],
    queryFn: () => billingApi.logs(page, 10),
  });

  // 充值套餐 + 客服信息：未登录也能拉，60s 刷一次，避免运营改套餐后用户长时间看到旧价。
  const productsQ = useQuery({
    queryKey: ['billing.recharge_products'],
    queryFn: () => billingApi.rechargeProducts(),
    staleTime: 60_000,
  });

  // 当前被点开「购买」的套餐 → 弹 Modal 显示客服邮箱。null = 关闭。
  const [purchasePkg, setPurchasePkg] = useState<RechargeProduct | null>(null);

  const [code, setCode] = useState('');
  const redeemMut = useMutation({
    mutationFn: () => billingApi.redeemCDK(code.trim()),
    onSuccess: async (resp) => {
      toast.success(`${t('billing.cdk_success_prefix')}${fmtPoints(resp.points)}${t('billing.cdk_success_suffix')}`);
      setCode('');
      await refreshMe();
      await qc.invalidateQueries({ queryKey: ['billing.logs'] });
      setPage(1);
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('billing.cdk_fail')),
  });

  const stats = [
    { label: t('billing.stat_available'), value: fmtPoints(me?.points ?? 0), accent: true },
    { label: t('billing.stat_frozen'), value: fmtPoints(me?.frozen_points ?? 0) },
    { label: t('billing.stat_plan'), value: me?.plan_code?.toUpperCase() ?? 'FREE' },
    { label: t('billing.stat_invite_code'), value: me?.invite_code ?? '—' },
  ];

  const logs = logsQ.data?.list ?? [];
  const total = logsQ.data?.total ?? 0;
  const pageSize = logsQ.data?.page_size ?? 10;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <h1 className="page-title">{t('billing.title')}</h1>
          <p className="page-subtitle">{t('billing.subtitle')}</p>
        </div>
      </header>

      <div className="stat-grid mb-6">
        {stats.map((s) => (
          <div key={s.label} className={`stat-tile ${s.accent ? 'stat-tile-accent' : ''}`}>
            <p className="stat-label">{s.label}</p>
            <p className="stat-value">{s.value}</p>
          </div>
        ))}
      </div>

      <section className="grid gap-4 mb-6 lg:grid-cols-2">
        <div className="card card-section">
          <header className="section-header mb-3">
            <span className="section-title">
              <Gift size={18} className="text-klein-500" />
              {t('billing.cdk_title')}
            </span>
          </header>
          <p className="text-small text-text-secondary mb-4 leading-loose">
            {t('billing.cdk_desc')}
          </p>
          <div className="flex flex-col sm:flex-row gap-2">
            <input
              className="input"
              placeholder={t('billing.cdk_placeholder')}
              value={code}
              onChange={(e) => setCode(e.target.value.toUpperCase())}
              maxLength={32}
            />
            <button
              className="btn btn-primary btn-lg whitespace-nowrap"
              disabled={code.trim().length < 4 || redeemMut.isPending}
              onClick={() => redeemMut.mutate()}
              type="button"
            >
              {redeemMut.isPending ? t('billing.cdk_redeeming') : t('billing.cdk_redeem_btn')}
            </button>
          </div>
        </div>

        <div className="card-tinted card-section">
          <header className="section-header mb-3">
            <span className="section-title">
              <Sparkles size={18} className="text-klein-500" />
              {t('billing.packages_title')}
            </span>
            {productsQ.data?.online_payment_enabled ? null : (
              <span className="badge badge-warning">{t('billing.packages_offline_payment_badge')}</span>
            )}
          </header>
          <p className="text-small text-text-secondary mb-4 leading-loose">
            {t('billing.packages_desc')}
          </p>
          {productsQ.isLoading ? (
            <p className="text-small text-text-tertiary">{t('common.loading')}</p>
          ) : productsQ.data && productsQ.data.products.length > 0 ? (
            <div className="grid gap-2 sm:grid-cols-2">
              {productsQ.data.products.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setPurchasePkg(p)}
                  className="group relative flex flex-col gap-1 rounded-[14px] border border-border bg-surface-1 px-4 py-3 text-left transition hover:border-klein-300 hover:shadow-1"
                >
                  {p.badge ? (
                    <span className="absolute -top-2 right-3 rounded-full bg-klein px-2 py-0.5 text-tiny font-medium text-text-on-klein">
                      {p.badge}
                    </span>
                  ) : null}
                  <span className="text-small font-medium text-text-primary">{p.name}</span>
                  <div className="flex items-baseline gap-1">
                    <span className="text-[20px] font-semibold text-klein-600 tabular-nums">
                      ¥{p.amount}
                    </span>
                    <span className="text-tiny text-text-tertiary">/ {t('billing.package_unit')}</span>
                  </div>
                  <div className="text-tiny text-text-tertiary">
                    {fmtPoints(p.points)} {t('common.points')}
                    {p.bonus_points > 0 ? (
                      <span className="ml-1 text-success">
                        + {fmtPoints(p.bonus_points)} {t('billing.package_bonus_suffix')}
                      </span>
                    ) : null}
                  </div>
                </button>
              ))}
            </div>
          ) : (
            <p className="text-small text-text-tertiary">{t('billing.packages_empty')}</p>
          )}
          <p className="mt-3 text-small text-text-tertiary leading-loose">
            {t('billing.frozen_explainer')}
          </p>
        </div>
      </section>

      {purchasePkg ? (
        <PurchaseDialog
          product={purchasePkg}
          contactEmail={productsQ.data?.contact.email ?? ''}
          contactNotice={productsQ.data?.contact.notice ?? ''}
          uid={me?.uid}
          onClose={() => setPurchasePkg(null)}
        />
      ) : null}

      <section className="card overflow-hidden">
        <div className="px-5 py-3.5 border-b border-border flex items-center justify-between">
          <span className="section-title">
            <Wallet size={16} className="text-text-tertiary" />
            {t('billing.logs_title')}
          </span>
          <span className="text-small text-text-tertiary">{t('common.total_count', { n: total })}</span>
        </div>
        <div className="divide-y divide-border">
          {logsQ.isLoading && (
            <p className="px-5 py-10 text-center text-text-tertiary text-small">{t('common.loading')}</p>
          )}
          {!logsQ.isLoading && logs.length === 0 && (
            <div className="empty-state">
              <span className="empty-state-icon">
                <Wallet size={22} />
              </span>
              <p className="empty-state-title">{t('billing.logs_empty_title')}</p>
              <p className="empty-state-desc">{t('billing.logs_empty_desc')}</p>
            </div>
          )}
          {logs.map((l) => (
            <div key={l.id} className="list-row">
              <div className="min-w-0">
                <p className="font-medium text-text-primary truncate">
                  {(() => {
                    // bizLabelKey 命中 → t(key)；没命中 → 原 biz_type 字符串。
                    const k = bizLabelKey(l.biz_type);
                    return k ? t(k) : l.biz_type;
                  })()}
                  {l.remark ? ` · ${l.remark}` : ''}
                </p>
                <p className="text-small text-text-tertiary mt-0.5">{fmtTime(l.created_at)}</p>
              </div>
              <p className={`font-bold whitespace-nowrap ${pointsClass(l.direction)}`}>
                {l.direction > 0 ? '+' : '-'} {fmtPoints(Math.abs(l.points))} {t('common.points')}
              </p>
            </div>
          ))}
        </div>
        <div className="flex items-center justify-between gap-3 border-t border-border px-5 py-4 text-sm">
          <span className="text-text-tertiary">
            {t('common.page_x_of_y', { page, total: totalPages })}, {t('common.total_count', { n: total })}
          </span>
          <div className="flex items-center gap-2">
            <button
              className="btn btn-outline btn-md"
              disabled={page <= 1 || logsQ.isFetching}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              type="button"
            >
              {t('common.prev_page')}
            </button>
            <button
              className="btn btn-outline btn-md"
              disabled={page >= totalPages || logsQ.isFetching}
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              type="button"
            >
              {t('common.next_page')}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}

/**
 * PurchaseDialog —— 点击套餐后弹出，展示客服邮箱 + 一键复制 + 标准化邮件主题。
 * 当前没有在线支付通道，所以这里给出最直接的"线下购买"指引：
 *   - 客服邮箱（admin 在系统配置 recharge.contact_email 里填）
 *   - 邮件示例 subject / body 已预填好 UID + 套餐 ID + 价格，避免用户漏写信息
 *   - 一键 mailto: 链接，让用户在邮件客户端直接打开新邮件
 *   - 兜底：邮箱未配置时显示一段提示，让用户去公告 / 官网底部找联系方式
 */
function PurchaseDialog({
  product,
  contactEmail,
  contactNotice,
  uid,
  onClose,
}: {
  product: RechargeProduct;
  contactEmail: string;
  contactNotice: string;
  uid?: number;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const hasEmail = contactEmail.trim().length > 0;

  // 预填邮件正文：把 UID / 套餐 ID / 套餐名 / 金额都塞进去，运营收到不用再追问。
  const subject = t('billing.purchase_mail_subject', { id: product.id, name: product.name });
  const body = [
    t('billing.purchase_mail_greeting'),
    '',
    t('billing.purchase_mail_field_uid', { uid: uid ?? '-' }),
    t('billing.purchase_mail_field_pkg', { id: product.id, name: product.name }),
    t('billing.purchase_mail_field_amount', { amount: product.amount }),
    '',
    t('billing.purchase_mail_thanks'),
  ].join('\n');
  const mailto = hasEmail
    ? `mailto:${contactEmail}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`
    : '';

  const copyEmail = async () => {
    if (!hasEmail) return;
    try {
      await navigator.clipboard.writeText(contactEmail);
      toast.success(t('common.copied'));
    } catch {
      toast.error(t('billing.copy_email_failed'));
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/45 p-4 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      onMouseDown={onClose}
    >
      <div
        className="w-full max-w-md overflow-hidden rounded-[20px] border border-border bg-surface-1 shadow-3"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b border-border px-5 py-4">
          <div className="min-w-0">
            <h2 className="truncate text-[18px] font-medium text-text-primary">
              {t('billing.purchase_title')}
            </h2>
            <p className="mt-1 truncate text-small text-text-tertiary">
              {product.name} · ¥{product.amount} · {fmtPoints(product.points)} {t('common.points')}
              {product.bonus_points > 0
                ? ` + ${fmtPoints(product.bonus_points)} ${t('billing.package_bonus_suffix')}`
                : ''}
            </p>
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

        <div className="space-y-4 px-5 py-5">
          {hasEmail ? (
            <>
              <p className="text-small leading-7 text-text-secondary">
                {contactNotice || t('billing.purchase_default_notice')}
              </p>
              <div className="rounded-[12px] border border-border bg-surface-2 p-3">
                <p className="mb-1 text-tiny text-text-tertiary">
                  {t('billing.purchase_kf_email_label')}
                </p>
                <div className="flex items-center justify-between gap-2">
                  <a
                    href={`mailto:${contactEmail}`}
                    className="truncate font-mono text-small text-klein-600 hover:underline"
                  >
                    {contactEmail}
                  </a>
                  <button
                    type="button"
                    onClick={copyEmail}
                    className="btn btn-outline btn-sm gap-1"
                    title={t('common.copy')}
                  >
                    <Copy size={13} /> {t('common.copy')}
                  </button>
                </div>
              </div>
              <a href={mailto} className="btn btn-primary btn-lg w-full gap-2">
                <Mail size={16} /> {t('billing.purchase_open_mail_btn')}
              </a>
              <p className="text-tiny text-text-tertiary leading-5">
                {t('billing.purchase_mail_hint', { uid: uid ?? '-', id: product.id })}
              </p>
            </>
          ) : (
            <p className="rounded-[12px] border border-warning/30 bg-warning-soft p-3 text-small leading-7 text-warning">
              {t('billing.purchase_no_email')}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
