import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import { applyThemeMode } from '@kleinai/theme';

import App from './App';
import { setUnauthorizedHandler } from './lib/api';
import { useAuthStore } from './stores/auth';
import { useLoginGateStore } from './stores/loginGate';
import { toast } from './stores/toast';
import i18n from './i18n'; // 必须在 App 渲染前 init，否则首屏 useTranslation 拿不到 resources
import '@kleinai/theme/tokens.css';
import '@kleinai/theme/animations.css';
import './index.css';

applyThemeMode((localStorage.getItem('klein:theme') as 'dark' | 'light' | 'system' | null) ?? 'light');

// 浏览器 tab 标题随 i18n 语言切换：
//   - 启动时根据当前语言写一次 document.title
//   - 监听 languageChanged 事件，用户切换语言后立即更新
// 同时同步 <html lang>，方便屏幕阅读器和搜索引擎判断主语言。
function syncDocumentTitleAndLang() {
  const lang = (i18n.language || 'zh').toLowerCase();
  document.documentElement.lang = lang.startsWith('zh') ? 'zh-CN' : 'en';
  const title = i18n.t('app.title');
  if (title && title !== 'app.title') document.title = title;
}
syncDocumentTitleAndLang();
i18n.on('languageChanged', syncDocumentTitleAndLang);

setUnauthorizedHandler(() => {
  useAuthStore.setState({ token: null, me: null });
  // 在 React 树外（axios interceptor）拿翻译走 i18n.t 即可。
  toast.error(i18n.t('auth.gate_session_expired'));
  // 让 token 失效的请求触发登录浮层，而不是粗暴跳转
  useLoginGateStore.getState().openGate({ hint: i18n.t('auth.gate_session_expired') });
});

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
});

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
