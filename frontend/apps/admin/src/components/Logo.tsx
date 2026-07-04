import clsx from 'clsx';
import { Sparkles } from 'lucide-react';

const BRAND_NAME = 'Xian Ai';

interface LogoProps {
  size?: 'sm' | 'md' | 'lg';
  /** 仅图标，不渲染文字 */
  iconOnly?: boolean;
  /** 头部独占文案（例如「管理后台」） */
  suffix?: string;
  className?: string;
}

const SIZE: Record<
  NonNullable<LogoProps['size']>,
  { box: number; icon: number; text: string }
> = {
  sm: { box: 28, icon: 16, text: 'text-small' },
  md: { box: 34, icon: 20, text: 'text-h4' },
  lg: { box: 44, icon: 24, text: 'text-h3' },
};

export function Logo({ size = 'md', iconOnly = false, suffix, className }: LogoProps) {
  const cfg = SIZE[size];
  return (
    <div className={clsx('flex items-center gap-2 select-none min-w-0', className)}>
      <span
        aria-label={BRAND_NAME}
        title={BRAND_NAME}
        style={{ height: cfg.box, width: cfg.box }}
        className="grid shrink-0 place-items-center rounded-lg bg-klein-gradient text-white shadow-glow-soft"
      >
        <Sparkles size={cfg.icon} strokeWidth={2.2} />
      </span>
      {!iconOnly && (
        <span className={clsx(cfg.text, 'font-semibold tracking-tight text-text-primary leading-none')}>
          {BRAND_NAME}
          {suffix && (
            <span className="ml-2 align-middle text-tiny font-medium text-text-tertiary">{suffix}</span>
          )}
        </span>
      )}
    </div>
  );
}
