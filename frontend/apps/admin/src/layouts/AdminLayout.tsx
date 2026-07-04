import { FormEvent, useState } from 'react';
import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import {
  Activity,
  BadgeDollarSign,
  ChevronDown,
  FileText,
  Globe2,
  Inbox,
  Layers,
  LayoutDashboard,
  LineChart,
  LockKeyhole,
  LogOut,
  Megaphone,
  Menu,
  Network,
  Settings,
  Tag,
  Ticket,
  UserCircle2,
  UserPlus,
  Users,
  Wallet,
  WalletCards,
  X,
} from 'lucide-react';
import clsx from 'clsx';

import { Logo } from '../components/Logo';
import { authApi } from '../lib/services';
import { useAuthStore } from '../stores/auth';
import { toast } from '../stores/toast';

const APP_VERSION = 'v3.0.1';

type NavItem = { to: string; label: string; icon: typeof LayoutDashboard; end?: boolean };
type NavGroup = { label: string; items: NavItem[] };

// 分组规则：按"功能性"聚合，自上而下从总览到底层配置。
const NAV_GROUPS: NavGroup[] = [
  {
    label: '总览',
    items: [{ to: '/dashboard', label: '仪表盘', icon: LayoutDashboard }],
  },
  {
    label: '号池与注册',
    items: [
      { to: '/pools', label: '号池管理', icon: Layers, end: true },
      { to: '/pools/register', label: '号池注册', icon: UserPlus },
      { to: '/mail-pool', label: '邮箱池', icon: Inbox },
    ],
  },
  {
    label: '网关',
    items: [
      // /upstreams 是上游账号（API key / cookie / token），/upstream-mgmt 是
      // 通道 + 路由 + 利润报表。两个互补：一个管"用什么号"，一个管"怎么算成本"。
      { to: '/upstreams', label: '上游账号', icon: Globe2 },
      { to: '/upstream-mgmt', label: '上游 API 管理', icon: LineChart },
      { to: '/proxies', label: '代理管理', icon: Network },
    ],
  },
  {
    label: '运营',
    items: [
      { to: '/users', label: '用户管理', icon: Users },
      { to: '/billing', label: '充值消费', icon: Wallet },
      { to: '/logs', label: '请求日志', icon: FileText },
    ],
  },
  {
    label: '营销',
    items: [
      { to: '/recharge-packages', label: '充值套餐', icon: WalletCards },
      { to: '/promo', label: '优惠码', icon: Tag },
      { to: '/cdk', label: '兑换码', icon: Ticket },
    ],
  },
  {
    label: '系统设置',
    items: [
      { to: '/cluster', label: '集群节点', icon: Activity },
      { to: '/model-prices', label: '模型价格', icon: BadgeDollarSign },
      { to: '/announcements', label: '系统公告', icon: Megaphone },
      // 「扣费规则」已并入「系统配置」第一张卡片，旧链接 /billing-settings 自动重定向。
      { to: '/config', label: '系统配置', icon: Settings },
    ],
  },
];

export function AdminLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const me = useAuthStore((s) => s.me);
  const logout = useAuthStore((s) => s.logout);
  const nav = useNavigate();

  const handleLogout = () => {
    logout();
    toast.info('已退出登录');
    nav('/login', { replace: true });
  };

  const displayName = me?.nickname || me?.username || '管理员';
  const roleName = me?.role_name || me?.role_code || '管理员';
  const initial = displayName.slice(0, 1).toUpperCase();

  return (
    <div className="grid min-h-screen bg-surface-bg lg:grid-cols-[240px_1fr]">
      <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-3 border-b border-border bg-surface-1 px-4 lg:hidden">
        <Logo size="sm" suffix="管理后台" />
        <button
          aria-label="打开菜单"
          className="btn btn-ghost btn-icon btn-sm"
          onClick={() => setMobileOpen((v) => !v)}
        >
          {mobileOpen ? <X size={20} /> : <Menu size={20} />}
        </button>
      </header>

      <aside
        className={clsx(
          'min-h-0 flex-col border-r border-border bg-surface-1 lg:sticky lg:top-0 lg:flex lg:h-screen',
          mobileOpen ? 'fixed inset-y-0 left-0 z-40 flex h-[100dvh] max-h-[100dvh] w-[86vw] max-w-[320px] shadow-3' : 'hidden',
        )}
      >
        <div className="flex h-14 items-center justify-between border-b border-border px-4 lg:hidden">
          <Logo size="sm" suffix="管理后台" />
          <button
            type="button"
            aria-label="关闭菜单"
            className="btn btn-ghost btn-icon btn-sm"
            onClick={() => setMobileOpen(false)}
          >
            <X size={18} />
          </button>
        </div>
        <div className="hidden h-16 items-center border-b border-border px-5 lg:flex">
          <Logo suffix="管理后台" />
        </div>
        <nav className="min-h-0 flex-1 space-y-3 overflow-y-auto overscroll-contain px-3 py-3">
          {NAV_GROUPS.map((group) => (
            <div key={group.label} className="space-y-0.5">
              <div className="px-3 pb-1 pt-1 text-tiny font-semibold uppercase tracking-wider text-text-tertiary">
                {group.label}
              </div>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.end}
                    onClick={() => setMobileOpen(false)}
                    className={({ isActive }) =>
                      clsx(
                        'flex h-9 min-w-[44px] items-center gap-2.5 rounded-md px-3 text-small transition',
                        isActive
                          ? 'bg-klein-gradient text-text-on-klein shadow-glow-soft'
                          : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary',
                      )
                    }
                  >
                    <Icon size={16} />
                    {item.label}
                  </NavLink>
                );
              })}
            </div>
          ))}
        </nav>
        <div className="border-t border-border px-4 py-4 text-tiny text-text-tertiary">
          <div className="flex items-center gap-2">
            <span>{APP_VERSION}</span>
          </div>
        </div>
      </aside>

      {mobileOpen && (
        <button
          type="button"
          aria-label="关闭菜单"
          className="fixed inset-0 z-30 bg-surface-overlay lg:hidden"
          onClick={() => setMobileOpen(false)}
        />
      )}

      <main className="flex min-w-0 flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center justify-between border-b border-border bg-surface-1/90 px-4 backdrop-blur sm:px-6">
          <h1 className="truncate text-h5 text-text-primary">管理后台</h1>
          <div
            className="relative"
            onBlur={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setMenuOpen(false);
            }}
          >
            <button
              type="button"
              className="inline-flex items-center gap-2 rounded-full px-2 py-1 transition hover:bg-surface-2"
              onClick={() => setMenuOpen((v) => !v)}
            >
              <span className="hidden rounded-full bg-surface-2 px-3 py-1 text-small text-text-secondary sm:inline-flex">
                {roleName}
              </span>
              <span className="grid h-8 w-8 flex-shrink-0 place-items-center rounded-pill bg-klein-gradient text-small text-white">
                {initial}
              </span>
              <ChevronDown
                size={15}
                className={clsx('text-text-tertiary transition', menuOpen && 'rotate-180')}
              />
            </button>

            {menuOpen && (
              <div className="absolute right-0 top-12 z-40 w-[260px] overflow-hidden rounded-xl border border-border bg-surface-1 shadow-4">
                <div className="border-b border-border px-4 py-3">
                  <div className="flex items-center gap-3">
                    <span className="grid h-10 w-10 place-items-center rounded-pill bg-klein-gradient text-white">
                      {initial}
                    </span>
                    <div className="min-w-0">
                      <p className="truncate text-body text-text-primary">{displayName}</p>
                      <p className="truncate text-small text-text-tertiary">{me?.username || 'admin'}</p>
                    </div>
                  </div>
                  <div className="mt-3 inline-flex rounded-full bg-surface-2 px-2.5 py-1 text-tiny text-text-secondary">
                    {roleName}
                  </div>
                </div>
                <button
                  type="button"
                  className="flex h-11 w-full items-center gap-3 px-4 text-left text-small text-text-secondary hover:bg-surface-2 hover:text-text-primary"
                  onClick={() => {
                    setMenuOpen(false);
                    setPasswordOpen(true);
                  }}
                >
                  <LockKeyhole size={16} />
                  修改密码
                </button>
                <button
                  type="button"
                  className="flex h-11 w-full items-center gap-3 px-4 text-left text-small text-text-secondary hover:bg-surface-2 hover:text-text-primary"
                  disabled
                >
                  <UserCircle2 size={16} />
                  账号信息
                </button>
                <button
                  type="button"
                  className="flex h-11 w-full items-center gap-3 border-t border-border px-4 text-left text-small text-danger hover:bg-danger-soft"
                  onClick={handleLogout}
                >
                  <LogOut size={16} />
                  退出登录
                </button>
              </div>
            )}
          </div>
        </header>
        <div className="min-w-0 flex-1">
          <Outlet />
        </div>
      </main>

      {passwordOpen && <PasswordDialog onClose={() => setPasswordOpen(false)} />}
    </div>
  );
}

function PasswordDialog({ onClose }: { onClose: () => void }) {
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (newPassword.length < 8) {
      toast.error('新密码至少 8 位');
      return;
    }
    if (newPassword !== confirm) {
      toast.error('两次输入的新密码不一致');
      return;
    }
    setSaving(true);
    try {
      await authApi.changePassword({ old_password: oldPassword, new_password: newPassword });
      toast.success('密码已修改');
      onClose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '修改失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-surface-overlay px-3 sm:px-4">
      <button className="absolute inset-0" type="button" aria-label="关闭" onClick={onClose} />
      <form className="dialog-surface relative w-full max-w-md p-4 sm:p-6" onSubmit={submit}>
        <div className="mb-5 flex items-center justify-between">
          <div>
            <h2 className="text-h3 text-text-primary">修改密码</h2>
            <p className="mt-1 text-small text-text-tertiary">建议及时修改默认密码。</p>
          </div>
          <button className="btn btn-ghost btn-icon btn-sm" type="button" onClick={onClose}>
            <X size={18} />
          </button>
        </div>
        <div className="space-y-3">
          <label className="field">
            <span className="field-label">原密码</span>
            <input
              className="input"
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              autoComplete="current-password"
            />
          </label>
          <label className="field">
            <span className="field-label">新密码</span>
            <input
              className="input"
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              autoComplete="new-password"
            />
          </label>
          <label className="field">
            <span className="field-label">确认新密码</span>
            <input
              className="input"
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
            />
          </label>
        </div>
        <div className="mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <button className="btn btn-outline btn-md" type="button" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary btn-md" type="submit" disabled={saving}>
            {saving ? '保存中...' : '保存修改'}
          </button>
        </div>
      </form>
    </div>
  );
}
