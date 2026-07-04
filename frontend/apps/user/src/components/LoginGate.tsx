import { useEffect, useState } from 'react';
import { Controller, useForm } from 'react-hook-form';
import { z } from 'zod';
import { zodResolver } from '@hookform/resolvers/zod';
import { LogIn, UserPlus, X } from 'lucide-react';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';

import { ApiError } from '../lib/api';
import { authApi } from '../lib/services';
import { useAuthStore } from '../stores/auth';
import { useLoginGateStore } from '../stores/loginGate';
import { toast } from '../stores/toast';
import { LegalAgreement } from './LegalDocs';

// schema 现在在 LoginForm / RegisterForm 内部按 useTranslation 重建，
// 不再定义 module-level —— 切换语言后报错文案能即时跟随。
type LoginValues = {
  account: string;
  password: string;
  agree: true;
};

type RegisterValues = {
  account: string;
  password: string;
  confirm: string;
  invite_code?: string;
  agree: true;
};

export function LoginGate() {
  const { t } = useTranslation();
  const open = useLoginGateStore((s) => s.open);
  const hint = useLoginGateStore((s) => s.hint);
  const initialTab = useLoginGateStore((s) => s.initialTab);
  const closeGate = useLoginGateStore((s) => s.closeGate);
  const resolve = useLoginGateStore((s) => s.resolve);

  const [tab, setTab] = useState<'login' | 'register'>(initialTab);

  useEffect(() => {
    if (open) setTab(initialTab);
  }, [open, initialTab]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeGate();
    };
    document.addEventListener('keydown', onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prev;
    };
  }, [open, closeGate]);

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="登录或注册"
      className="fixed inset-0 z-[80] grid place-items-center px-4 py-10"
    >
      <button
        aria-label="关闭"
        type="button"
        className="absolute inset-0 bg-surface-overlay backdrop-blur-sm"
        onClick={closeGate}
      />

      <div className="relative w-full max-w-[440px] overflow-hidden rounded-[28px] border border-border bg-surface-1 p-6 shadow-4 klein-fade-in">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-[26px] font-medium leading-tight text-text-primary">
              {tab === 'login' ? t('auth.welcome_back') : t('auth.create_account')}
            </h2>
            <p className="mt-2 text-[15px] text-text-tertiary">{hint || t('auth.gate_default_hint')}</p>
          </div>
          <button
            type="button"
            aria-label={t('common.close')}
            className="grid h-9 w-9 flex-shrink-0 place-items-center rounded-full text-text-tertiary transition hover:bg-surface-2 hover:text-text-primary"
            onClick={closeGate}
          >
            <X size={20} />
          </button>
        </div>

        <div className="mt-6 grid grid-cols-2 rounded-full bg-surface-2 p-1">
          <button
            role="tab"
            aria-selected={tab === 'login'}
            type="button"
            onClick={() => setTab('login')}
            className={clsx(
              'inline-flex h-11 items-center justify-center gap-2 rounded-full text-[15px] transition',
              tab === 'login'
                ? 'bg-surface-1 font-medium text-text-primary shadow-1'
                : 'text-text-tertiary',
            )}
          >
            <LogIn size={16} /> {t('nav.login')}
          </button>
          <button
            role="tab"
            aria-selected={tab === 'register'}
            type="button"
            onClick={() => setTab('register')}
            className={clsx(
              'inline-flex h-11 items-center justify-center gap-2 rounded-full text-[15px] transition',
              tab === 'register'
                ? 'bg-surface-1 font-medium text-text-primary shadow-1'
                : 'text-text-tertiary',
            )}
          >
            <UserPlus size={16} /> {t('nav.register')}
          </button>
        </div>

        <div className="py-6">
          {tab === 'login' ? <LoginForm onDone={resolve} /> : <RegisterForm onDone={resolve} />}
        </div>
      </div>
    </div>
  );
}

function LoginForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const setToken = useAuthStore((s) => s.setToken);
  const refreshMe = useAuthStore((s) => s.refreshMe);

  const loginSchema = z.object({
    account: z.string().min(3, t('auth.account_min')),
    password: z.string().min(6, t('auth.password_min')),
    agree: z.literal(true, {
      errorMap: () => ({ message: t('auth.agree_required') }),
    }),
  });

  const {
    register,
    handleSubmit,
    control,
    formState: { errors, isSubmitting },
  } = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { account: '', password: '', agree: false as unknown as true },
  });

  const onSubmit = async (v: LoginValues) => {
    try {
      const resp = await authApi.login({ account: v.account, password: v.password });
      setToken(resp.token);
      await refreshMe();
      toast.success(t('auth.login_success'));
      onDone();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t('auth.login_failed'));
    }
  };

  return (
    <form className="space-y-3" onSubmit={handleSubmit(onSubmit)} noValidate>
      <div className="field">
        <input
          className={clsx('input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal', errors.account && 'input-error')}
          placeholder={t('auth.account_placeholder')}
          autoComplete="username"
          {...register('account')}
        />
        {errors.account && <p className="field-error">{errors.account.message}</p>}
      </div>
      <div className="field">
        <input
          className={clsx('input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal', errors.password && 'input-error')}
          type="password"
          placeholder={t('auth.password')}
          autoComplete="current-password"
          {...register('password')}
        />
        {errors.password && <p className="field-error">{errors.password.message}</p>}
      </div>
      <Controller
        control={control}
        name="agree"
        render={({ field }) => (
          <LegalAgreement
            checked={Boolean(field.value)}
            onChange={(v) => field.onChange(v)}
            error={errors.agree?.message}
          />
        )}
      />
      <button className="btn btn-primary btn-lg btn-block h-14 text-[17px]" type="submit" disabled={isSubmitting}>
        {isSubmitting ? t('auth.logging_in') : t('nav.login')}
      </button>
    </form>
  );
}

function RegisterForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const setToken = useAuthStore((s) => s.setToken);
  const refreshMe = useAuthStore((s) => s.refreshMe);

  const registerSchema = z
    .object({
      account: z.string().min(3, t('auth.account_min')).max(64, t('auth.account_max')),
      password: z
        .string()
        .min(8, t('auth.password_min_8'))
        .max(64, t('auth.password_max'))
        .regex(/[A-Za-z]/, t('auth.password_needs_letter'))
        .regex(/[0-9]/, t('auth.password_needs_digit')),
      confirm: z.string(),
      invite_code: z.string().max(16).optional().or(z.literal('')),
      agree: z.literal(true, {
        errorMap: () => ({ message: t('auth.agree_required') }),
      }),
    })
    .refine((d) => d.password === d.confirm, {
      message: t('auth.passwords_mismatch'),
      path: ['confirm'],
    });

  const {
    register,
    handleSubmit,
    control,
    formState: { errors, isSubmitting },
  } = useForm<RegisterValues>({
    resolver: zodResolver(registerSchema),
    defaultValues: {
      account: '',
      password: '',
      confirm: '',
      invite_code: '',
      agree: false as unknown as true,
    },
  });

  const onSubmit = async (v: RegisterValues) => {
    try {
      const resp = await authApi.register({
        account: v.account,
        password: v.password,
        invite_code: v.invite_code || undefined,
      });
      setToken(resp.token);
      await refreshMe();
      toast.success(t('auth.register_success'));
      onDone();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t('auth.register_failed'));
    }
  };

  return (
    <form className="space-y-3" onSubmit={handleSubmit(onSubmit)} noValidate>
      <div className="field">
        <input
          className={clsx('input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal', errors.account && 'input-error')}
          placeholder={t('auth.account_placeholder')}
          autoComplete="username"
          {...register('account')}
        />
        {errors.account && <p className="field-error">{errors.account.message}</p>}
      </div>
      <div className="field">
        <input
          className={clsx('input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal', errors.password && 'input-error')}
          type="password"
          placeholder={t('auth.password_strong_hint')}
          autoComplete="new-password"
          {...register('password')}
        />
        {errors.password && <p className="field-error">{errors.password.message}</p>}
      </div>
      <div className="field">
        <input
          className={clsx('input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal', errors.confirm && 'input-error')}
          type="password"
          placeholder={t('auth.confirm_password_placeholder')}
          autoComplete="new-password"
          {...register('confirm')}
        />
        {errors.confirm && <p className="field-error">{errors.confirm.message}</p>}
      </div>
      <div className="field">
        <input
          className="input h-14 rounded-2xl text-[15px] font-normal placeholder:font-normal"
          placeholder={t('auth.invite_code')}
          {...register('invite_code')}
        />
      </div>
      <Controller
        control={control}
        name="agree"
        render={({ field }) => (
          <LegalAgreement
            checked={Boolean(field.value)}
            onChange={(v) => field.onChange(v)}
            error={errors.agree?.message}
          />
        )}
      />
      <button className="btn btn-primary btn-lg btn-block h-14 text-[17px]" type="submit" disabled={isSubmitting}>
        {isSubmitting ? t('auth.registering') : t('auth.register_btn')}
      </button>
    </form>
  );
}
