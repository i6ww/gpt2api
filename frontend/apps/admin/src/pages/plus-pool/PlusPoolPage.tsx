import { Crown } from 'lucide-react';
import { useState } from 'react';
import clsx from 'clsx';

import CloudPhonePanel from './CloudPhonePanel';
import WalletPanel from './WalletPanel';
import ProxyPanel from './ProxyPanel';
import { PageHeader, PageShell } from '../../components/layout/PageShell';

// Plus 升级资源池
//
// 一个 Tab 容器，把开 Plus 需要的 3 类资源放在一起：
//   - 云手机池：GeeLark 云手机，dispatcher 跑 GoPay 时调 /shell/execute 读 WhatsApp OTP
//   - GoPay 钱包池：手机号 + PIN，每个钱包能开 N 个 Plus（1:N 模型）
//   - 支付代理池：印尼住宅 IP，专给 GoPay 的 Phase B（midtrans + gopay 阶段）用
//
// 跟 PoolsPage 同结构（顶部蓝色 pill 切换 tab），保持视觉一致。
const TABS = [
  { id: 'cloud-phone' as const, label: '云手机' },
  { id: 'wallet' as const, label: 'GoPay 钱包' },
  { id: 'proxy' as const, label: '印尼支付代理' },
];

type TabId = (typeof TABS)[number]['id'];

export default function PlusPoolPage() {
  const [tab, setTab] = useState<TabId>('cloud-phone');

  return (
    <PageShell>
      <PageHeader
        icon={<Crown size={16} />}
        title="Plus 升级资源池"
        right={
          <nav className="flex flex-wrap gap-1.5">
            {TABS.map((t) => (
              <button
                key={t.id}
                type="button"
                className={clsx(
                  'inline-flex h-8 items-center rounded-md border px-3 text-small transition',
                  tab === t.id
                    ? 'border-transparent bg-klein-gradient text-text-on-klein shadow-glow-soft'
                    : 'border-border bg-surface-1 text-text-secondary hover:bg-surface-2',
                )}
                onClick={() => setTab(t.id)}
              >
                {t.label}
              </button>
            ))}
          </nav>
        }
      />

      {tab === 'cloud-phone' && <CloudPhonePanel />}
      {tab === 'wallet' && <WalletPanel />}
      {tab === 'proxy' && <ProxyPanel />}
    </PageShell>
  );
}
