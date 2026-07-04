import { Suspense, lazy } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';

import { Toaster } from './components/Toaster';
import { AdminLayout } from './layouts/AdminLayout';
import RequireAuth from './routes/RequireAuth';

const LoginPage = lazy(() => import('./pages/auth/LoginPage'));
const DashboardPage = lazy(() => import('./pages/dashboard/DashboardPage'));
const PoolsPage = lazy(() => import('./pages/pools/PoolsPage'));
const PoolRegisterPage = lazy(() => import('./pages/register/PoolRegisterPage'));
const MailPoolPage = lazy(() => import('./pages/mailpool/MailPoolPage'));
const UpstreamApisPage = lazy(() => import('./pages/upstreams/UpstreamApisPage'));
const UpstreamChannelsPage = lazy(() => import('./pages/upstreams/UpstreamChannelsPage'));
const ProxiesPage = lazy(() => import('./pages/proxies/ProxiesPage'));
const UsersPage = lazy(() => import('./pages/users/UsersPage'));
const BillingPage = lazy(() => import('./pages/billing/BillingPage'));
const PromoPage = lazy(() => import('./pages/promo/PromoPage'));
const CDKPage = lazy(() => import('./pages/promo/CDKPage'));
const ConfigPage = lazy(() => import('./pages/system/ConfigPage'));
const RechargePackagesPage = lazy(() => import('./pages/system/RechargePackagesPage'));
const ModelPricesPage = lazy(() => import('./pages/system/ModelPricesPage'));
const AnnouncementsPage = lazy(() => import('./pages/system/AnnouncementsPage'));
const LogsPage = lazy(() => import('./pages/logs/LogsPage'));
const ClusterPage = lazy(() => import('./pages/cluster/ClusterPage'));

export default function App() {
  return (
    <>
      <Suspense fallback={<div className="grid h-screen place-items-center text-text-tertiary">加载中...</div>}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route element={<RequireAuth />}>
            <Route element={<AdminLayout />}>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<DashboardPage />} />
              {/* 旧 Token 管理路径自动跳到号池管理（Phase 2 收口）。 */}
              <Route path="/accounts" element={<Navigate to="/pools" replace />} />
              <Route path="/pools" element={<PoolsPage />} />
              <Route path="/pools/register" element={<PoolRegisterPage />} />
              {/* /plus-pool 已并入号池管理 → GPT 子 tab；旧链接重定向回去，避免 404。 */}
              <Route path="/plus-pool" element={<Navigate to="/pools?tab=gpt&sub=cloud-phone" replace />} />
              <Route path="/mail-pool" element={<MailPoolPage />} />
              <Route path="/upstreams" element={<UpstreamApisPage />} />
              <Route path="/upstream-mgmt" element={<UpstreamChannelsPage />} />
              <Route path="/proxies" element={<ProxiesPage />} />
              <Route path="/users" element={<UsersPage />} />
              <Route path="/billing" element={<BillingPage />} />
              <Route path="/promo" element={<PromoPage />} />
              <Route path="/cdk" element={<CDKPage />} />
              <Route path="/config" element={<ConfigPage />} />
              {/* 扣费规则已并入 /config，旧链接重定向避免书签 404。 */}
              <Route path="/billing-settings" element={<Navigate to="/config" replace />} />
              <Route path="/recharge-packages" element={<RechargePackagesPage />} />
              <Route path="/model-prices" element={<ModelPricesPage />} />
              <Route path="/announcements" element={<AnnouncementsPage />} />
              <Route path="/logs" element={<LogsPage />} />
              <Route path="/cluster" element={<ClusterPage />} />
            </Route>
          </Route>
        </Routes>
      </Suspense>
      <Toaster />
    </>
  );
}
