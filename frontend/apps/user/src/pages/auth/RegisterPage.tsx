import { Link, useNavigate } from 'react-router-dom';
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

type FormValues = {
  account: string;
  password: string;
  confirm: string;
  invite_code: string;
  agree: true;
};

export default function RegisterPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const setToken = useAuthStore((s) => s.setToken);
  const refreshMe = useAuthStore((s) => s.refreshMe);

  // schema 跟着 i18n 重建，切换语言时校验报错也跟着切换。
  const schema = z
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
      // 注册必须主动勾选「同意服务条款与隐私政策」，规避法律风险。
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
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      account: '',
      password: '',
      confirm: '',
      invite_code: '',
      agree: false as unknown as true,
    },
  });

  const onSubmit = async (values: FormValues) => {
    try {
      const resp = await authApi.register({
        account: values.account,
        password: values.password,
        invite_code: values.invite_code || undefined,
      });
      setToken(resp.token);
      await refreshMe();
      toast.success(t('auth.register_success'));
      navigate('/create/image', { replace: true });
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : t('auth.register_failed');
      toast.error(msg);
    }
  };

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h1 className="text-h1 text-text-primary">{t('auth.create_account')}</h1>
        <p className="text-body text-text-secondary">{t('auth.register_hint')}</p>
      </header>

      <form className="space-y-4" onSubmit={handleSubmit(onSubmit)} noValidate>
        <div className="field">
          <label className="field-label">{t('auth.account')}</label>
          <input
            className={clsx('input', errors.account && 'input-error')}
            placeholder={t('auth.account_placeholder')}
            autoComplete="username"
            {...register('account')}
          />
          {errors.account && <p className="field-error">{errors.account.message}</p>}
        </div>

        <div className="field">
          <label className="field-label">{t('auth.password')}</label>
          <input
            className={clsx('input', errors.password && 'input-error')}
            type="password"
            placeholder={t('auth.password_strong_hint')}
            autoComplete="new-password"
            {...register('password')}
          />
          {errors.password && <p className="field-error">{errors.password.message}</p>}
        </div>

        <div className="field">
          <label className="field-label">{t('auth.confirm_password')}</label>
          <input
            className={clsx('input', errors.confirm && 'input-error')}
            type="password"
            placeholder={t('auth.confirm_password_placeholder')}
            autoComplete="new-password"
            {...register('confirm')}
          />
          {errors.confirm && <p className="field-error">{errors.confirm.message}</p>}
        </div>

        <div className="field">
          <label className="field-label">{t('auth.invite_code')}</label>
          <input
            className="input"
            placeholder={t('auth.invite_code_placeholder')}
            {...register('invite_code')}
          />
          <p className="field-hint">{t('auth.invite_code_hint')}</p>
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

        <button className="btn btn-primary btn-lg btn-block" type="submit" disabled={isSubmitting}>
          {isSubmitting ? t('auth.registering') : t('auth.register_btn')}
        </button>
      </form>

      <p className="text-small text-text-secondary text-center">
        {t('auth.has_account')}
        <Link to="/login" className="text-klein-500 hover:underline ml-1">{t('auth.login_now')}</Link>
      </p>
    </div>
  );
}
