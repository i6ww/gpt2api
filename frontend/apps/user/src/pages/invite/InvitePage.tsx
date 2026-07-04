import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { Copy, Share2, UserCheck, Users, Wallet } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { fmtPoints, fmtTime } from '../../lib/format';
import { inviteApi } from '../../lib/services';
import { useAuthStore } from '../../stores/auth';
import { toast } from '../../stores/toast';

export default function InvitePage() {
  const { t } = useTranslation();
  const me = useAuthStore((s) => s.me);

  const summaryQ = useQuery({
    queryKey: ['invite.summary'],
    queryFn: inviteApi.summary,
  });
  const summary = summaryQ.data;

  // 优先用 summary.invite_code（强制权威）；fallback 到 me.invite_code。
  const code = summary?.invite_code || me?.invite_code || '—';
  // 后端给出的 invite_link 是用 base_url 拼的，前端展示时优先用当前 origin，便于本地调试。
  const link =
    typeof window !== 'undefined'
      ? `${window.location.origin}/register?invite=${code}`
      : summary?.invite_link || `https://gpt2api.example/register?invite=${code}`;

  const ratePct = summary ? summary.commission_rate.toFixed(2).replace(/\.00$/, '') : '10';

  const [page, setPage] = useState(1);
  const PAGE_SIZE = 10;
  const listQ = useQuery({
    queryKey: ['invite.invitees', page],
    queryFn: () => inviteApi.invitees(page, PAGE_SIZE),
  });
  const invitees = listQ.data?.list ?? [];
  const total = listQ.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const copy = (text: string, label: string) => {
    navigator.clipboard.writeText(text).then(() => toast.success(`${label}${t('invite.copied_suffix')}`));
  };

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <h1 className="page-title">{t('invite.title')}</h1>
          <p className="page-subtitle">
            {t('invite.subtitle_prefix')} <span className="text-klein-500 font-semibold">{ratePct}%</span> {t('invite.subtitle_suffix')}
          </p>
        </div>
      </header>

      <section className="card-tinted card-section grid lg:grid-cols-[1fr_auto] gap-6 items-end mb-4">
        <div className="space-y-4 min-w-0">
          <div>
            <p className="text-overline mb-1">{t('invite.your_code')}</p>
            <p className="font-mono text-display gradient-text break-all leading-tight">{code}</p>
          </div>
          <div>
            <p className="text-overline mb-1">{t('invite.your_link')}</p>
            <code className="block rounded-md bg-surface-1 border border-border px-4 py-2.5 font-mono text-small break-all">
              {link}
            </code>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <button className="btn btn-primary btn-lg" onClick={() => copy(code, t('invite.copied_label_code'))} type="button">
            <Copy size={16} /> {t('invite.copy_code_btn')}
          </button>
          <button className="btn btn-outline btn-lg" onClick={() => copy(link, t('invite.copied_label_link'))} type="button">
            <Share2 size={16} /> {t('invite.copy_link_btn')}
          </button>
        </div>
      </section>

      <div className="stat-grid mb-6">
        <div className="stat-tile stat-tile-accent">
          <p className="stat-label">{t('invite.stats_invited')}</p>
          <p className="stat-value">{summary?.invitee_count ?? 0}</p>
        </div>
        <div className="stat-tile">
          <p className="stat-label">{t('invite.stats_reward_total')}</p>
          <p className="stat-value">{fmtPoints(summary?.total_reward_points ?? 0)}</p>
        </div>
        <div className="stat-tile">
          <p className="stat-label">{t('invite.stats_reward_count')}</p>
          <p className="stat-value">{summary?.reward_count ?? 0}</p>
        </div>
        <div className="stat-tile">
          <p className="stat-label">{t('invite.stats_rate')}</p>
          <p className="stat-value">{ratePct}%</p>
        </div>
      </div>

      <section className="card overflow-hidden mb-6">
        <div className="px-5 py-3.5 border-b border-border flex items-center justify-between">
          <span className="section-title">
            <UserCheck size={16} className="text-text-tertiary" />
            {t('invite.list_title')}
          </span>
          <span className="text-small text-text-tertiary">{t('invite.total_n_people', { n: total })}</span>
        </div>
        <div className="divide-y divide-border">
          {listQ.isLoading && (
            <p className="px-5 py-10 text-center text-text-tertiary text-small">{t('invite.loading')}</p>
          )}
          {!listQ.isLoading && invitees.length === 0 && (
            <div className="empty-state">
              <span className="empty-state-icon">
                <Users size={22} />
              </span>
              <p className="empty-state-title">{t('invite.empty_title')}</p>
              <p className="empty-state-desc">{t('invite.empty_desc')}</p>
            </div>
          )}
          {invitees.map((u) => (
            <div key={u.user_id} className="list-row">
              <div className="min-w-0 flex items-center gap-3">
                <span className="w-9 h-9 rounded-full bg-surface-1 border border-border flex items-center justify-center text-text-tertiary shrink-0">
                  <Users size={16} />
                </span>
                <div className="min-w-0">
                  <p className="font-medium text-text-primary truncate flex items-center gap-2">
                    {u.account}
                    {u.status !== 1 && (
                      <span className="badge badge-warning text-tiny">{t('invite.disabled_badge')}</span>
                    )}
                  </p>
                  <p className="text-small text-text-tertiary mt-0.5">
                    {t('invite.bound_at')} {fmtTime(u.bound_at)} · {t('invite.total_recharge')} {fmtPoints(u.total_recharge)} {t('common.points')}
                  </p>
                </div>
              </div>
              <div className="text-right whitespace-nowrap">
                <p className="font-bold text-success">+ {fmtPoints(u.reward_to_inviter)} {t('common.points')}</p>
                <p className="text-tiny text-text-tertiary mt-0.5">{t('invite.my_reward')}</p>
              </div>
            </div>
          ))}
        </div>
        <div className="flex items-center justify-between gap-3 border-t border-border px-5 py-4 text-sm">
          <span className="text-text-tertiary">
            {t('invite.page_x_y_n', { page, total: totalPages, n: total })}
          </span>
          <div className="flex items-center gap-2">
            <button
              className="btn btn-outline btn-md"
              disabled={page <= 1 || listQ.isFetching}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              type="button"
            >
              {t('common.prev_page')}
            </button>
            <button
              className="btn btn-outline btn-md"
              disabled={page >= totalPages || listQ.isFetching}
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              type="button"
            >
              {t('common.next_page')}
            </button>
          </div>
        </div>
      </section>

      <section className="card card-section">
        <h3 className="section-title mb-3">
          <Wallet size={16} className="text-text-tertiary" />
          {t('invite.rules_title')}
        </h3>
        <ul className="space-y-2 text-body text-text-secondary list-disc pl-5 leading-loose">
          <li>{t('invite.rule_1')}</li>
          <li>
            {t('invite.rule_2_prefix')}
            <span className="text-text-primary font-medium">{ratePct}%</span>{t('invite.rule_2_suffix')}
          </li>
          <li>{t('invite.rule_3')}</li>
          <li>{t('invite.rule_4')}</li>
        </ul>
      </section>
    </div>
  );
}
