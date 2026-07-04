import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import {
  BookOpen,
  Clock3,
  CreditCard,
  FileKey2,
  Gift,
  Home,
  Image,
  LogIn,
  LogOut,
  Menu,
  MessageCircle,
  Music,
  Settings,
  Video,
  X,
  type LucideIcon,
} from 'lucide-react';
import { useState } from 'react';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';

import { useAuthStore } from '../stores/auth';
import { useLoginGateStore } from '../stores/loginGate';
import { toast } from '../stores/toast';
import { AnnouncementBanner } from '../components/AnnouncementBanner';
import { LanguageSwitcher } from '../components/LanguageSwitcher';

interface NavItem {
  to: string;
  /** i18n key（如 `nav.create_image`），渲染时再用 t() 拿翻译。 */
  labelKey: string;
  icon: LucideIcon;
  authed?: boolean;
}

const NAV_ITEMS: NavItem[] = [
  { to: '/create/image', labelKey: 'nav.create_image', icon: Image },
  { to: '/create/text', labelKey: 'nav.create_text', icon: MessageCircle },
  { to: '/create/video', labelKey: 'nav.create_video', icon: Video },
  { to: '/create/music', labelKey: 'nav.create_music', icon: Music },
  { to: '/history', labelKey: 'nav.history', icon: Clock3, authed: true },
  { to: '/billing', labelKey: 'nav.billing', icon: CreditCard, authed: true },
  { to: '/keys', labelKey: 'nav.keys', icon: FileKey2, authed: true },
  { to: '/docs', labelKey: 'nav.docs', icon: BookOpen },
  { to: '/invite', labelKey: 'nav.invite', icon: Gift, authed: true },
  { to: '/settings', labelKey: 'nav.settings', icon: Settings, authed: true },
];

const APP_VERSION = 'v3.0.1';

export function AppLayout() {
  const { t } = useTranslation();
  const token = useAuthStore((s) => s.token);
  const me = useAuthStore((s) => s.me);
  const logout = useAuthStore((s) => s.logout);
  const openGate = useLoginGateStore((s) => s.openGate);
  const navigate = useNavigate();
  const isAuthed = !!token;
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const onLogout = async () => {
    await logout();
    toast.info(t('auth.logged_out'));
    navigate('/create/image', { replace: true });
  };

  const handleNav = (item: NavItem, e: React.MouseEvent) => {
    if (item.authed && !isAuthed) {
      e.preventDefault();
      openGate({
        hint: t('auth.gate_authed_hint', { label: t(item.labelKey) }),
        onLoggedIn: () => navigate(item.to),
      });
    }
    setMobileNavOpen(false);
  };

  return (
    <div className="min-h-full bg-surface-bg text-text-primary">
      <aside className="fixed inset-y-0 left-0 z-40 hidden w-14 border-r border-border bg-surface-1 lg:flex lg:flex-col lg:items-center">
        <button
          type="button"
          className="mt-3 grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2"
          title={t('nav.create_image')}
          onClick={() => navigate('/create/image')}
        >
          <Home size={18} />
        </button>

        <nav className="mt-6 flex flex-1 flex-col items-center gap-2">
          {NAV_ITEMS.slice(0, 4).map((item) => (
            <RailLink key={item.to} item={item} onClick={handleNav} />
          ))}
          <div className="my-2 h-px w-6 bg-border" />
          {NAV_ITEMS.slice(4, 7).map((item) => (
            <RailLink key={item.to} item={item} onClick={handleNav} />
          ))}
        </nav>

        <div className="mb-3 flex flex-col items-center gap-2">
          {NAV_ITEMS.slice(7).map((item) => (
            <RailLink key={item.to} item={item} onClick={handleNav} />
          ))}
          {/* 语言切换器：rail variant 跟其他 RailLink 同款圆形按钮，下拉弹到右侧避免被裁 */}
          <LanguageSwitcher variant="rail" />
          {isAuthed ? (
            <>
              <button
                type="button"
                className="grid h-8 w-8 place-items-center rounded-full bg-success text-xs font-semibold text-text-on-klein"
                title={me?.username || me?.email || t('settings.profile')}
                onClick={() => navigate('/settings')}
              >
                {(me?.username || me?.email || 'U').slice(0, 1).toUpperCase()}
              </button>
              <button
                type="button"
                className="grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2"
                title={t('nav.logout')}
                onClick={onLogout}
              >
                <LogOut size={17} />
              </button>
            </>
          ) : (
            <button
              type="button"
              className="grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2"
              title={t('nav.login')}
              onClick={() => openGate({ hint: t('auth.gate_default_hint') })}
            >
              <LogIn size={17} />
            </button>
          )}
        </div>
        <div className="mb-2 text-[11px] text-text-tertiary">
          <span>{APP_VERSION}</span>
        </div>
      </aside>

      <header className="sticky top-0 z-30 flex h-12 items-center justify-between border-b border-border bg-surface-1/90 px-3 backdrop-blur lg:hidden">
        <button
          type="button"
          className="grid h-9 w-9 place-items-center rounded-full hover:bg-surface-2"
          title={t('nav.create_image')}
          onClick={() => navigate('/create/image')}
        >
          <Home size={18} />
        </button>
        <div className="flex min-w-0 flex-1 items-center justify-center gap-2 px-1">
          {NAV_ITEMS.slice(0, 3).map((item) => (
            <MobileMode key={item.to} item={item} compact onClick={handleNav} />
          ))}
          <button
            type="button"
            aria-label={t('common.more')}
            className={clsx(
              'inline-flex h-8 shrink-0 items-center gap-1 rounded-full border px-2.5 text-sm',
              mobileNavOpen
                ? 'border-klein bg-klein/10 text-klein'
                : 'border-border text-text-secondary hover:bg-surface-2',
            )}
            onClick={() => setMobileNavOpen(true)}
          >
            <Menu size={16} />
            <span className="text-xs">{t('common.more')}</span>
          </button>
        </div>
        <button
          type="button"
          className="grid h-9 w-9 place-items-center rounded-full hover:bg-surface-2"
          title={t('nav.history')}
          onClick={() => {
            if (!isAuthed) {
              openGate({
                hint: t('auth.gate_authed_hint', { label: t('nav.history') }),
                onLoggedIn: () => navigate('/history'),
              });
              return;
            }
            navigate('/history');
          }}
        >
          <Clock3 size={18} />
        </button>
      </header>

      {/* 移动端完整导航抽屉（列出全部 NAV_ITEMS；顶栏仅能放下部分快捷入口） */}
      {mobileNavOpen && (
        <>
          <button
            type="button"
            aria-label="关闭导航"
            className="fixed inset-0 z-40 bg-black/45 backdrop-blur-[2px] lg:hidden"
            onClick={() => setMobileNavOpen(false)}
          />
          <div
            className="fixed inset-y-0 right-0 z-50 flex h-[100dvh] max-h-[100dvh] w-[min(20rem,calc(100vw-2.5rem))] flex-col border-l border-border bg-surface-1 shadow-lg lg:hidden"
            role="dialog"
            aria-modal="true"
            aria-labelledby="mobile-nav-title"
          >
            <div className="flex h-12 shrink-0 items-center justify-between border-b border-border px-3">
              <span id="mobile-nav-title" className="text-small font-semibold text-text-primary">
                {t('nav.create_image')}
              </span>
              <button
                type="button"
                aria-label={t('common.close')}
                className="grid h-9 w-9 place-items-center rounded-full hover:bg-surface-2"
                onClick={() => setMobileNavOpen(false)}
              >
                <X size={18} />
              </button>
            </div>
            <nav className="min-h-0 flex-1 space-y-1 overflow-y-auto overscroll-contain px-2 py-2">
              <NavLinkMobileList items={NAV_ITEMS} onNavigate={handleNav} />
              <div className="mx-3 my-4 h-px bg-border" />
              <div className="px-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-text-tertiary">
                Language / 语言
              </div>
              <div className="px-2 pb-2">
                <LanguageSwitcher variant="menu" />
              </div>
              <div className="mx-3 my-2 h-px bg-border" />
              {isAuthed ? (
                <button
                  type="button"
                  className="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-small text-text-secondary hover:bg-surface-2 hover:text-text-primary"
                  onClick={() => {
                    setMobileNavOpen(false);
                    navigate('/settings');
                  }}
                >
                  <span className="grid h-8 w-8 place-items-center rounded-full bg-success text-xs font-semibold text-text-on-klein">
                    {(me?.username || me?.email || 'U').slice(0, 1).toUpperCase()}
                  </span>
                  {t('nav.settings')} · {me?.username || me?.email || me?.invite_code || ''}
                </button>
              ) : (
                <button
                  type="button"
                  className="flex w-full items-center gap-2 rounded-lg px-3 py-2.5 text-left text-small text-text-secondary hover:bg-surface-2"
                  onClick={() => {
                    setMobileNavOpen(false);
                    openGate({ hint: t('auth.gate_default_hint') });
                  }}
                >
                  <LogIn size={17} /> {t('nav.login')}
                </button>
              )}
              {isAuthed && (
                <button
                  type="button"
                  className="flex w-full items-center gap-2 rounded-lg px-3 py-2.5 text-left text-small text-danger hover:bg-danger/10"
                  onClick={async () => {
                    setMobileNavOpen(false);
                    await onLogout();
                  }}
                >
                  <LogOut size={17} /> {t('nav.logout')}
                </button>
              )}
              <div className="pb-8 pt-2 text-center text-[11px] text-text-tertiary">{APP_VERSION}</div>
            </nav>
          </div>
        </>
      )}

      <main className="min-h-screen lg:pl-14">
        <AnnouncementBanner />
        <Outlet />
      </main>
    </div>
  );
}

function RailLink({
  item,
  onClick,
}: {
  item: NavItem;
  onClick: (item: NavItem, e: React.MouseEvent) => void;
}) {
  const { t } = useTranslation();
  const Icon = item.icon;
  const label = t(item.labelKey);
  return (
    <NavLink
      to={item.to}
      title={label}
      onClick={(e) => onClick(item, e)}
      className={({ isActive }) =>
        clsx(
          'grid h-9 w-9 place-items-center rounded-full transition',
          isActive
            ? 'bg-klein text-text-on-klein shadow-glow-soft'
            : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary',
        )
      }
    >
      <Icon size={18} />
    </NavLink>
  );
}

function MobileMode({
  item,
  onClick,
  compact,
}: {
  item: NavItem;
  onClick: (item: NavItem, e: React.MouseEvent) => void;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  const Icon = item.icon;
  const label = t(item.labelKey);
  return (
    <NavLink
      to={item.to}
      title={label}
      onClick={(e) => onClick(item, e)}
      className={({ isActive }) =>
        clsx(
          'inline-flex shrink-0 items-center justify-center rounded-full text-sm transition',
          compact ? 'h-9 w-9' : 'h-8 gap-1.5 px-3',
          isActive ? 'bg-klein text-text-on-klein' : 'text-text-secondary hover:bg-surface-2',
        )
      }
    >
      <Icon size={compact ? 16 : 15} />
      {!compact && label}
    </NavLink>
  );
}

function NavLinkMobileList({
  items,
  onNavigate,
}: {
  items: NavItem[];
  onNavigate: (item: NavItem, e: React.MouseEvent) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      {items.map((item) => {
        const Icon = item.icon;
        return (
          <NavLink
            key={item.to}
            to={item.to}
            onClick={(e) => onNavigate(item, e)}
            className={({ isActive }) =>
              clsx(
                'flex min-h-[44px] items-center gap-3 rounded-lg px-3 py-2 text-small transition',
                isActive
                  ? 'bg-klein text-text-on-klein'
                  : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary',
              )
            }
          >
            <Icon size={18} />
            {t(item.labelKey)}
          </NavLink>
        );
      })}
    </>
  );
}
