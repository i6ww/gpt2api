import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { z } from 'zod';
import { zodResolver } from '@hookform/resolvers/zod';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';

import { applyThemeMode, type ThemeMode } from '@kleinai/theme';

import { ApiError } from '../../lib/api';
import { fmtPoints, fmtTime } from '../../lib/format';
import { authApi } from '../../lib/services';
import { useAuthStore } from '../../stores/auth';
import { toast } from '../../stores/toast';

type PwdForm = {
  old_password: string;
  new_password: string;
  confirm: string;
};

export default function SettingsPage() {
  const { t } = useTranslation();
  const me = useAuthStore((s) => s.me);
  const [mode, setLocalMode] = useState<ThemeMode>(
    (localStorage.getItem('klein:theme') as ThemeMode | null) ?? 'light',
  );

  const setTheme = (m: ThemeMode) => {
    applyThemeMode(m);
    localStorage.setItem('klein:theme', m);
    setLocalMode(m);
  };

  // 密码 schema 跟随 i18n 重建。三字段全空 = 不修改密码 = 整页保存不应被拦下。
  const pwdSchema = z
    .object({
      old_password: z.string().optional().default(''),
      new_password: z.string().optional().default(''),
      confirm: z.string().optional().default(''),
    })
    .superRefine((d, ctx) => {
      const old = (d.old_password ?? '').trim();
      const nw = (d.new_password ?? '').trim();
      const cf = (d.confirm ?? '').trim();
      if (!old && !nw && !cf) return;
      if (old.length < 6) {
        ctx.addIssue({ code: 'custom', path: ['old_password'], message: t('settings.old_password_min') });
      }
      if (nw.length < 8) {
        ctx.addIssue({ code: 'custom', path: ['new_password'], message: t('settings.new_password_min') });
      } else if (!/[A-Za-z]/.test(nw) || !/[0-9]/.test(nw)) {
        ctx.addIssue({ code: 'custom', path: ['new_password'], message: t('settings.new_password_letter_digit') });
      }
      if (nw !== cf) {
        ctx.addIssue({ code: 'custom', path: ['confirm'], message: t('settings.passwords_mismatch') });
      }
    });

  const {
    register,
    handleSubmit,
    reset,
    watch,
    formState: { errors },
  } = useForm<PwdForm>({
    resolver: zodResolver(pwdSchema),
    defaultValues: { old_password: '', new_password: '', confirm: '' },
  });

  const pwdWatched = watch();
  const pwdDirty =
    (pwdWatched.old_password ?? '').length > 0 ||
    (pwdWatched.new_password ?? '').length > 0 ||
    (pwdWatched.confirm ?? '').length > 0;

  const pwdMut = useMutation({
    mutationFn: (body: { old_password: string; new_password: string }) =>
      authApi.changePassword(body),
    onSuccess: () => {
      toast.success(t('settings.password_updated'));
      reset();
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('settings.password_update_failed')),
  });

  const themeOptions: {
    value: ThemeMode;
    label: string;
    desc: string;
  }[] = [
    { value: 'light', label: t('settings.theme_light'), desc: t('settings.theme_light_desc') },
    { value: 'dark', label: t('settings.theme_dark'), desc: t('settings.theme_dark_desc') },
    { value: 'system', label: t('settings.theme_system'), desc: t('settings.theme_system_desc') },
  ];

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <h1 className="page-title">{t('settings.title')}</h1>
          <p className="page-subtitle">{t('settings.subtitle')}</p>
        </div>
      </header>

      <section className="card card-section mb-4">
        <h3 className="section-title mb-4">{t('settings.profile')}</h3>
        <div className="grid sm:grid-cols-2 gap-3">
          <Field label={t('settings.uid')} value={me?.uid?.toString() ?? '—'} />
          <Field label={t('settings.uuid')} value={me?.uuid ?? '—'} mono />
          <Field label={t('settings.account')} value={me?.username || me?.email || me?.phone || '—'} />
          <Field label={t('settings.invite_code')} value={me?.invite_code ?? '—'} mono />
          <Field label={t('settings.plan')} value={me?.plan_code?.toUpperCase() ?? 'FREE'} />
          <Field label={t('settings.available_points')} value={fmtPoints(me?.points ?? 0)} />
          <Field label={t('settings.frozen_points')} value={fmtPoints(me?.frozen_points ?? 0)} />
          <Field label={t('settings.registered_at')} value={fmtTime(me?.created_at ?? 0)} />
        </div>
      </section>

      <section className="card card-section mb-4">
        <h3 className="section-title mb-4">{t('settings.theme_title')}</h3>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          {themeOptions.map((o) => {
            const active = mode === o.value;
            return (
              <button
                key={o.value}
                className={clsx(
                  'relative rounded-md border px-4 py-3 text-left transition',
                  active
                    ? 'border-klein-500 bg-klein-gradient-soft text-text-primary shadow-1'
                    : 'border-border bg-surface-1 hover:border-border-strong',
                )}
                onClick={() => setTheme(o.value)}
              >
                <p className="font-semibold text-body">{o.label}</p>
                <p className="text-small text-text-tertiary mt-0.5">{o.desc}</p>
              </button>
            );
          })}
        </div>
      </section>

      <section className="card card-section">
        <div className="mb-4 flex items-baseline gap-3">
          <h3 className="section-title">{t('settings.password_title')}</h3>
          <span className="text-tiny text-text-tertiary">{t('settings.password_optional_hint')}</span>
        </div>
        <form
          className="grid sm:grid-cols-2 gap-3"
          onSubmit={handleSubmit((d) => {
            const old = (d.old_password ?? '').trim();
            const nw = (d.new_password ?? '').trim();
            if (!old && !nw) {
              toast.success(t('settings.password_unchanged'));
              return;
            }
            pwdMut.mutate({ old_password: old, new_password: nw });
          })}
          noValidate
        >
          <div className="field sm:col-span-2">
            <label className="field-label">{t('settings.old_password')}</label>
            <input
              type="password"
              className={clsx('input', errors.old_password && 'input-error')}
              autoComplete="current-password"
              {...register('old_password')}
            />
            {errors.old_password && <p className="field-error">{errors.old_password.message}</p>}
          </div>
          <div className="field">
            <label className="field-label">{t('settings.new_password')}</label>
            <input
              type="password"
              className={clsx('input', errors.new_password && 'input-error')}
              autoComplete="new-password"
              {...register('new_password')}
            />
            {errors.new_password && <p className="field-error">{errors.new_password.message}</p>}
          </div>
          <div className="field">
            <label className="field-label">{t('settings.confirm_password')}</label>
            <input
              type="password"
              className={clsx('input', errors.confirm && 'input-error')}
              autoComplete="new-password"
              {...register('confirm')}
            />
            {errors.confirm && <p className="field-error">{errors.confirm.message}</p>}
          </div>
          <div className="sm:col-span-2 flex items-center justify-end gap-3">
            {!pwdDirty && (
              <span className="text-small text-text-tertiary">
                {t('settings.password_dirty_hint')}
              </span>
            )}
            <button
              type="submit"
              className="btn btn-primary btn-lg"
              disabled={pwdMut.isPending || !pwdDirty}
            >
              {pwdMut.isPending ? t('settings.password_updating') : t('settings.password_update_btn')}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="rounded-md bg-surface-2 border border-border px-4 py-3">
      <p className="text-tiny text-text-tertiary uppercase tracking-wider">{label}</p>
      <p className={clsx('mt-1 text-text-primary break-all', mono ? 'font-mono text-small' : 'text-body font-medium')}>
        {value}
      </p>
    </div>
  );
}
