import { Layers } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import clsx from 'clsx';

import AdobePoolPanel from './AdobePoolPanel';
import GrokPoolPanel from './GrokPoolPanel';
import XAIPoolPanel from './XAIPoolPanel';
import GptPoolPanel from './GptPoolPanel';
import GooglePoolPanel from './GooglePoolPanel';
import CloudPhonePanel from '../plus-pool/CloudPhonePanel';
import WalletPanel from '../plus-pool/WalletPanel';
import ProxyPanel from '../plus-pool/ProxyPanel';
import { PageHeader, PageShell } from '../../components/layout/PageShell';

// 顶层 tab：三个 provider 的号池。
const TABS = [
  { id: 'adobe' as const, label: 'BANANA' },
  { id: 'grok' as const, label: 'GROK' },
  { id: 'xai' as const, label: '官方GROK(xAI)' },
  { id: 'gpt' as const, label: 'GPT' },
  { id: 'google' as const, label: '歌曲(FlowMusic)' },
];

type TabId = (typeof TABS)[number]['id'];

// GPT 下的二级 sub-tab：原 "Plus 升级资源池" 三件套被并到这里。
//   - accounts：现成的 GptPoolPanel（GPT 账号表）
//   - cloud-phone / wallet / payment-proxy：开 Plus 所需的三类基础资源
const GPT_SUBTABS = [
  { id: 'accounts' as const, label: 'GPT 账号' },
  { id: 'cloud-phone' as const, label: '云手机' },
  { id: 'wallet' as const, label: 'GoPay 钱包' },
  { id: 'payment-proxy' as const, label: '印尼支付代理' },
];

type GptSub = (typeof GPT_SUBTABS)[number]['id'];

function isTabId(v: string | null): v is TabId {
  return v === 'adobe' || v === 'grok' || v === 'xai' || v === 'gpt' || v === 'google';
}
function isGptSub(v: string | null): v is GptSub {
  return v === 'accounts' || v === 'cloud-phone' || v === 'wallet' || v === 'payment-proxy';
}

export default function PoolsPage() {
  const [search, setSearch] = useSearchParams();
  // 顶层 tab 与子 tab 都支持 URL 反查：?tab=gpt&sub=wallet
  // /plus-pool 旧链接会 Navigate 到 /pools?tab=gpt&sub=cloud-phone，正好在这里被消费。
  const initialTab: TabId = isTabId(search.get('tab')) ? (search.get('tab') as TabId) : 'adobe';
  const initialSub: GptSub = isGptSub(search.get('sub')) ? (search.get('sub') as GptSub) : 'accounts';

  const [tab, setTab] = useState<TabId>(initialTab);
  const [gptSub, setGptSub] = useState<GptSub>(initialSub);

  // 切顶层 tab 时把 sub 重置（除非已经在 gpt 上）；同步 URL 便于刷新/分享。
  useEffect(() => {
    const next = new URLSearchParams(search);
    if (tab === 'adobe') {
      next.delete('tab');
      next.delete('sub');
    } else {
      next.set('tab', tab);
      if (tab === 'gpt') {
        next.set('sub', gptSub);
      } else {
        next.delete('sub');
      }
    }
    // 仅在变化时写回，避免无限循环。
    if (next.toString() !== search.toString()) {
      setSearch(next, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, gptSub]);

  return (
    <PageShell>
      <PageHeader
        icon={<Layers size={16} />}
        title="号池管理"
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

      {tab === 'adobe' && <AdobePoolPanel />}
      {tab === 'grok' && <GrokPoolPanel />}
      {tab === 'xai' && <XAIPoolPanel />}
      {tab === 'google' && <GooglePoolPanel />}
      {tab === 'gpt' && (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-1.5 rounded-md border border-border bg-surface-1 p-1.5">
            {GPT_SUBTABS.map((s) => (
              <button
                key={s.id}
                type="button"
                className={clsx(
                  'inline-flex h-7 items-center rounded px-3 text-small transition',
                  gptSub === s.id
                    ? 'bg-klein-soft text-klein-600 shadow-1'
                    : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary',
                )}
                onClick={() => setGptSub(s.id)}
              >
                {s.label}
              </button>
            ))}
          </div>
          {gptSub === 'accounts' && <GptPoolPanel />}
          {gptSub === 'cloud-phone' && <CloudPhonePanel />}
          {gptSub === 'wallet' && <WalletPanel />}
          {gptSub === 'payment-proxy' && <ProxyPanel />}
        </div>
      )}
    </PageShell>
  );
}
