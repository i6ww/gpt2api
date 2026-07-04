import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Controller, useForm } from 'react-hook-form';
import { z } from 'zod';
import { zodResolver } from '@hookform/resolvers/zod';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';

import { ApiError } from '../../lib/api';
import { authApi } from '../../lib/services';
import { useAuthStore } from '../../stores/auth';
import { toast } from '../../stores/toast';
import { LegalAgreement } from '../../components/LegalDocs';

// schema 用函数延迟构造：i18n 在 LoginPage 内部用 hook 取，schema 必须在 hook 之后用 useMemo 拿。
// 之前是 module-level const，文案永远是 zh.json 加载时的语言，切换语言后报错文案不会更新。
type FormValues = {
  account: string;
  password: string;
  remember: boolean;
  agree: true;
};

export default function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const setToken = useAuthStore((s) => s.setToken);
  const refreshMe = useAuthStore((s) => s.refreshMe);

  // zod schema 与当前语言绑定：useTranslation 变化时会重建 schema 让校验信息也跟着切换。
  const schema = z.object({
    account: z.string().min(3, t('auth.account_min', '账号至少 3 位')),
    password: z.string().min(6, t('auth.password_min', '密码至少 6 位')),
    remember: z.boolean().default(true),
    // 必须勾选「同意服务条款与隐私政策」才能登录。
    agree: z.literal(true, {
      errorMap: () => ({ message: t('auth.agree_required') }),
    }),
  });

  const {
    register,
    handleSubmit,
    control,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { account: '', password: '', remember: true, agree: false as unknown as true },
  });

  const onSubmit = async (values: FormValues) => {
    try {
      const resp = await authApi.login({ account: values.account, password: values.password });
      setToken(resp.token);
      await refreshMe();
      toast.success(t('auth.login_success'));
      const from = (location.state as { from?: string } | null)?.from ?? '/create/image';
      navigate(from, { replace: true });
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : t('auth.login_failed');
      toast.error(msg);
    }
  };

  return (
    <div className="space-y-7">
      <header className="space-y-2">
        <h1 className="text-h1 text-text-primary">{t('auth.welcome_back')}</h1>
        <p className="text-body text-text-secondary">{t('auth.login_hint')}</p>
      </header>

      <form className="space-y-4" onSubmit={handleSubmit(onSubmit)} noValidate>
        <div className="field">
          <label htmlFor="account" className="field-label">{t('auth.account')}</label>
          <input
            id="account"
            placeholder={t('auth.account_placeholder')}
            autoComplete="username"
            inputMode="email"
            className={clsx('input', errors.account && 'input-error')}
            {...register('account')}
          />
          {errors.account && <p className="field-error">{errors.account.message}</p>}
        </div>

        <div className="field">
          <label htmlFor="password" className="field-label">{t('auth.password')}</label>
          <input
            id="password"
            type="password"
            placeholder={t('auth.password_placeholder')}
            autoComplete="current-password"
            className={clsx('input', errors.password && 'input-error')}
            {...register('password')}
          />
          {errors.password && <p className="field-error">{errors.password.message}</p>}
        </div>

        <div className="flex items-center justify-between text-small">
          <label className="flex items-center gap-2 text-text-secondary cursor-pointer select-none">
            <input type="checkbox" className="checkbox" {...register('remember')} />
            {t('auth.remember_me')}
          </label>
          <Link to="/forgot" className="text-klein-500 hover:underline">{t('auth.forgot_password')}</Link>
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

        <button type="submit" className="btn btn-primary btn-lg btn-block" disabled={isSubmitting}>
          {isSubmitting ? t('auth.logging_in') : t('auth.login_btn')}
        </button>

        <div className="relative my-2">
          <div className="absolute inset-0 grid place-items-center">
            <div className="h-px w-full bg-border" />
          </div>
          <div className="relative flex justify-center">
            <span className="bg-surface-bg px-3 text-tiny text-text-tertiary uppercase tracking-wider">{t('auth.or')}</span>
          </div>
        </div>

        <button type="button" className="btn btn-outline btn-lg btn-block" disabled>
          {t('auth.wechat_login')}
        </button>
      </form>

      <p className="text-small text-text-secondary text-center">
        {t('auth.no_account')}
        <Link to="/register" className="text-klein-500 hover:underline ml-1">{t('auth.register_now')}</Link>
      </p>
    </div>
  );
}
