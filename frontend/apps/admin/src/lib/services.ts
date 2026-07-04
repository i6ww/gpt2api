// 后台 API 抽象。
import { request, requestRaw } from './api';
import type {
  Announcement,
  AnnouncementCreateReq,
  AnnouncementUpdateReq,
  AccountBatchImportBody,
  AccountBatchImportResult,
  AccountBatchRefreshResp,
  AccountBulkOpResult,
  AccountCreateBody,
  AccountItem,
  AccountModelsResp,
  AccountPurgeBody,
  AccountRefreshResp,
  AccountSecretsResp,
  AccountTestResp,
  AccountUpdateBody,
  AdminUserAdjustPointsBody,
  AdminUserAdjustPointsResp,
  AdminUserCreateBody,
  AdminGenerationLogItem,
  AdminGenerationLogPurgeResp,
  AdminGenerationStuckCleanupResp,
  AdminGenerationUpstreamLogItem,
  AdminPromoBody,
  AdminPromoItem,
  AdminUserItem,
  AdminUserUpdateBody,
  AdminWalletLogItem,
  AdminWalletLogSummary,
  AdminLoginResp,
  AdminMe,
  AdminCDKBatchAppendResp,
  AdminCDKBatchItem,
  AdminCDKBatchListQuery,
  AdminCDKCodeItem,
  AdminCDKCodeListQuery,
  CDKCreateBatchBody,
  CDKCreateBatchResp,
  DashboardOverviewResp,
  PageData,
  PoolStatsResp,
  ProxyCreateBody,
  ProxyImportBody,
  ProxyImportResp,
  ProxyBatchTestResp,
  ProxyBulkOpResult,
  ProxyItem,
  ProxyTestResp,
  ProxyUpdateBody,
  SystemSettings,
  MailPoolItem,
  MailPoolImportBody,
  MailPoolImportResult,
  MailPoolCFGenerateBody,
  MailPoolCFGenerateResp,
  MailPoolUpdateBody,
  MailPoolStatsResp,
  MailPoolBulkOpResult,
  MailPoolStatus,
  MailPoolMode,
  AdobePoolItem,
  AdobePoolCreateBody,
  AdobePoolUpdateBody,
  AdobePoolImportBody,
  AdobePoolImportResult,
  AdobePoolStatsResp,
  AdobePoolBulkOpResult,
  AdobePoolStatus,
  AdobePoolSource,
  GooglePoolItem,
  GooglePoolListQuery,
  GooglePoolCreateBody,
  GooglePoolUpdateBody,
  GooglePoolImportBody,
  GooglePoolImportResult,
  GooglePoolStatsResp,
  GooglePoolBulkOpResult,
  XaiPoolItem,
  XaiPoolListQuery,
  XaiPoolCreateBody,
  XaiPoolUpdateBody,
  XaiPoolImportBody,
  XaiPoolImportResult,
  XaiPoolStatsResp,
  XaiPoolBulkOpResult,
  XaiPoolRefreshResp,
  XaiBillingResp,
  GrokPoolItem,
  GrokPoolCreateBody,
  GrokPoolUpdateBody,
  GrokPoolImportBody,
  GrokPoolImportResult,
  GrokPoolStatsResp,
  GrokPoolBulkOpResult,
  GrokPoolPurgeBody,
  GrokPoolBatchRefreshBody,
  GrokPoolBatchRefreshJob,
  GrokPoolRefreshResult,
  GrokTrialStatus,
  GrokAccountType,
  GrokSubscriptionStatus,
  GptPoolItem,
  GptPoolDetail,
  GptPoolCreateBody,
  GptPoolUpdateBody,
  GptPoolImportBody,
  GptPoolImportResult,
  GptPoolStatsResp,
  GptPoolBulkOpResult,
  GptPoolStatus,
  GptPlanType,
  GptPoolBatchRefreshBody,
  GptPoolBatchRefreshResp,
  GptPoolRefreshAllResp,
  GptPoolPurgeBody,
  RegisterTaskItem,
  RegisterTaskListReq,
  RegisterTaskCreateBody,
  RegisterTaskCreateResp,
  RegisterTaskStatsResp,
  RegisterTaskProvider,
  RegisterTaskLogEntry,
  RegisterTaskLogQuery,
  CloudPhoneItem,
  CloudPhoneCreateBody,
  CloudPhoneUpdateBody,
  CloudPhoneListReq,
  CloudPhoneImportBody,
  CloudPhoneImportResult,
  CloudPhoneBulkOpResult,
  GopayWalletItem,
  GopayWalletCreateBody,
  GopayWalletUpdateBody,
  GopayWalletListReq,
  GopayWalletSecretResp,
  GopayWalletImportBody,
  GopayWalletImportResult,
  GopayWalletBulkOpResult,
  GopayBindingItem,
  GopayBindingListReq,
  PaymentProxyItem,
  PaymentProxyCreateBody,
  PaymentProxyUpdateBody,
  PaymentProxyListReq,
  PaymentProxyImportBody,
  PaymentProxyImportResult,
  PaymentProxyTestResp,
  PaymentProxyBulkOpResult,
} from './types';

export const authApi = {
  login: (username: string, password: string) =>
    request<AdminLoginResp>({
      url: '/auth/login',
      method: 'POST',
      // 后端 dto.LoginReq 字段名为 account，前端表单仍展示「管理员账号」
      data: { account: username, password },
    }),
  me: () => request<AdminMe>({ url: '/auth/me', method: 'GET' }),
  changePassword: (body: { old_password: string; new_password: string }) =>
    request<{ ok: boolean }>({ url: '/auth/password', method: 'POST', data: body }),
};

export const dashboardApi = {
  overview: () => request<DashboardOverviewResp>({ url: '/dashboard/overview', method: 'GET' }),
};

export interface AdminUserListQuery {
  keyword?: string;
  status?: 0 | 1;
  page?: number;
  page_size?: number;
}

export const usersApi = {
  list: (q: AdminUserListQuery = {}) =>
    request<PageData<AdminUserItem>>({ url: '/users', method: 'GET', params: q }),
  create: (body: AdminUserCreateBody) =>
    request<{ id: number }>({ url: '/users', method: 'POST', data: body }),
  update: (id: number, body: AdminUserUpdateBody) =>
    request<void>({ url: `/users/${id}`, method: 'PUT', data: body }),
  adjustPoints: (id: number, body: AdminUserAdjustPointsBody) =>
    request<AdminUserAdjustPointsResp>({ url: `/users/${id}/points`, method: 'POST', data: body }),
};

export interface GenerationLogListQuery {
  keyword?: string;
  kind?: 'image' | 'video' | 'chat' | 'text';
  status?: 0 | 1 | 2 | 3 | 4;
  page?: number;
  page_size?: number;
}

export const logsApi = {
  generations: (q: GenerationLogListQuery = {}) =>
    request<PageData<AdminGenerationLogItem>>({ url: '/logs/generations', method: 'GET', params: q }),
  generationUpstream: (taskId: string) =>
    request<AdminGenerationUpstreamLogItem[]>({ url: `/logs/generations/${taskId}/upstream`, method: 'GET' }),
  /**
   * 删除 days 天前的请求日志（软删 generation_task + generation_result）。
   * 可选 status 过滤：传入 0~4 时只删该状态的记录（典型 status=3 仅删失败）。
   */
  purgeGenerations: (days: number, status?: number) =>
    request<AdminGenerationLogPurgeResp>({
      url: '/logs/generations',
      method: 'DELETE',
      data: typeof status === 'number' ? { days, status } : { days },
    }),
  cleanupStuckGenerations: (minAgeMinutes: number) =>
    request<AdminGenerationStuckCleanupResp>({
      url: '/logs/generations/cleanup-stuck',
      method: 'POST',
      data: { min_age_minutes: minAgeMinutes },
    }),
};

export interface WalletLogListQuery {
  keyword?: string;
  user_id?: number;
  biz_type?: string;
  // 这里只允许 1 / -1 / undefined。不能塞空字符串 —— axios 会把
  // direction='' 序列化进 query string，后端 *int + oneof=-1 1 校验直接 400，
  // 整页列表全空。
  direction?: 1 | -1;
  page?: number;
  page_size?: number;
}

export const billingApi = {
  walletLogs: (q: WalletLogListQuery = {}) =>
    request<PageData<AdminWalletLogItem>>({ url: '/billing/wallet-logs', method: 'GET', params: q }),
  walletSummary: () =>
    request<AdminWalletLogSummary>({ url: '/billing/wallet-logs/summary', method: 'GET' }),
};

export interface PromoListQuery {
  keyword?: string;
  status?: 0 | 1 | '';
  discount_type?: 1 | 2 | 3 | '';
  page?: number;
  page_size?: number;
}

export const promoApi = {
  list: (q: PromoListQuery = {}) =>
    request<PageData<AdminPromoItem>>({ url: '/promo/codes', method: 'GET', params: q }),
  create: (body: AdminPromoBody) =>
    request<{ id: number }>({ url: '/promo/codes', method: 'POST', data: body }),
  update: (id: number, body: AdminPromoBody) =>
    request<void>({ url: `/promo/codes/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/promo/codes/${id}`, method: 'DELETE' }),
};

export interface AccountListQuery {
  provider?: 'gpt' | 'grok' | 'pic2api';
  plan_type?: 'basic' | 'super' | 'heavy';
  auth_type?: 'api_key' | 'cookie' | 'oauth' | 'token';
  status?: -1 | 0 | 1 | 2;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const accountsApi = {
  list: (q: AccountListQuery = {}) =>
    request<PageData<AccountItem>>({
      url: '/accounts',
      method: 'GET',
      params: q,
    }),
  create: (body: AccountCreateBody) =>
    request<{ id: number }>({ url: '/accounts', method: 'POST', data: body }),
  update: (id: number, body: AccountUpdateBody) =>
    request<void>({ url: `/accounts/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/accounts/${id}`, method: 'DELETE' }),
  batchImport: (body: AccountBatchImportBody) =>
    request<AccountBatchImportResult>({
      url: '/accounts/import',
      method: 'POST',
      data: body,
    }),
  stats: () => request<PoolStatsResp>({ url: '/accounts/stats', method: 'GET' }),
  test: (id: number) =>
    request<AccountTestResp>({ url: `/accounts/${id}/test`, method: 'POST' }),
  syncModels: (id: number) =>
    request<AccountModelsResp>({ url: `/accounts/${id}/models`, method: 'POST' }),
  refresh: (id: number) =>
    request<AccountRefreshResp>({ url: `/accounts/${id}/refresh`, method: 'POST' }),
  secrets: (id: number) =>
    request<AccountSecretsResp>({ url: `/accounts/${id}/secrets`, method: 'GET' }),
  batchRefresh: (provider?: 'gpt' | 'grok' | 'pic2api' | '', page = 1, pageSize = 50) =>
    request<AccountBatchRefreshResp>({
      url: '/accounts/batch-refresh',
      method: 'POST',
      data: { provider: provider ?? '', page, page_size: pageSize },
    }),
  batchProbe: (provider?: 'gpt' | 'grok' | 'pic2api' | '', page = 1, pageSize = 20) =>
    request<{
      probed: number;
      failed_ids: number[];
      page: number;
      page_size: number;
      total: number;
      has_more: boolean;
      next_page?: number;
    }>({
      url: '/accounts/batch-probe',
      method: 'POST',
      data: { provider: provider ?? '', page, page_size: pageSize },
    }),
  batchDelete: (ids: number[]) =>
    request<AccountBulkOpResult>({
      url: '/accounts/batch-delete',
      method: 'POST',
      data: { ids },
    }),
  purge: (body: AccountPurgeBody) =>
    request<AccountBulkOpResult>({
      url: '/accounts/purge',
      method: 'POST',
      data: body,
    }),
};

export const cdkApi = {
  createBatch: (body: CDKCreateBatchBody) =>
    request<CDKCreateBatchResp>({
      url: '/cdk/batches',
      method: 'POST',
      data: body,
    }),
  listBatches: (q: AdminCDKBatchListQuery = {}) =>
    request<PageData<AdminCDKBatchItem>>({
      url: '/cdk/batches',
      method: 'GET',
      params: q,
    }),
  getBatch: (id: number) =>
    request<AdminCDKBatchItem>({ url: `/cdk/batches/${id}`, method: 'GET' }),
  toggleBatch: (id: number, status: 0 | 1) =>
    request<{ id: number; status: number }>({
      url: `/cdk/batches/${id}/toggle`,
      method: 'POST',
      data: { status },
    }),
  appendBatch: (id: number, qty: number) =>
    request<AdminCDKBatchAppendResp>({
      url: `/cdk/batches/${id}/append`,
      method: 'POST',
      data: { qty },
    }),
  listCodes: (batchID: number, q: AdminCDKCodeListQuery = {}) =>
    request<PageData<AdminCDKCodeItem>>({
      url: `/cdk/batches/${batchID}/codes`,
      method: 'GET',
      params: q,
    }),
  revokeCode: (id: number) =>
    request<{ id: number; status: number }>({
      url: `/cdk/codes/${id}/revoke`,
      method: 'POST',
    }),
  /**
   * 下载批次 CSV。
   *
   * 后端 /cdk/batches/:id/export 需要 Authorization Bearer，
   * 所以不能用 `<a href>` 直接点；改成 axios 拉 blob，前端造 ObjectURL 触发下载。
   */
  exportBatch: async (id: number, batchNo: string) => {
    const res = await requestRaw<Blob>({
      url: `/cdk/batches/${id}/export`,
      method: 'GET',
      responseType: 'blob',
    });
    const blob = res.data instanceof Blob ? res.data : new Blob([res.data as BlobPart]);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `cdk-${batchNo}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  },
};

// ==================== 代理 ====================

export interface ProxyListQuery {
  status?: 0 | 1;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const proxiesApi = {
  list: (q: ProxyListQuery = {}) =>
    request<PageData<ProxyItem>>({ url: '/proxies', method: 'GET', params: q }),
  create: (body: ProxyCreateBody) =>
    request<{ id: number }>({ url: '/proxies', method: 'POST', data: body }),
  import: (body: ProxyImportBody) =>
    request<ProxyImportResp>({ url: '/proxies/import', method: 'POST', data: body }),
  update: (id: number, body: ProxyUpdateBody) =>
    request<void>({ url: `/proxies/${id}`, method: 'PUT', data: body }),
  remove: (id: number) =>
    request<void>({ url: `/proxies/${id}`, method: 'DELETE' }),
  batchDelete: (ids: number[]) =>
    request<ProxyBulkOpResult>({ url: '/proxies/batch-delete', method: 'POST', data: { ids } }),
  batchTest: (ids: number[]) =>
    request<ProxyBatchTestResp>({ url: '/proxies/batch-test', method: 'POST', data: { ids } }),
  test: (id: number) =>
    request<ProxyTestResp>({ url: `/proxies/${id}/test`, method: 'POST' }),
};

// ==================== 系统配置 ====================

export const systemApi = {
  get: () => request<SystemSettings>({ url: '/system/settings', method: 'GET' }),
  update: (kv: Partial<SystemSettings>) =>
    request<{ updated: number }>({
      url: '/system/settings',
      method: 'PUT',
      data: kv,
    }),
  cacheStats: () =>
    request<{ root: string; files: number; bytes: number }>({ url: '/system/cache', method: 'GET' }),
  cleanCache: (body: { days?: number; all?: boolean }) =>
    request<{ deleted_files: number; deleted_bytes: number; remain_bytes: number }>({
      url: '/system/cache',
      method: 'DELETE',
      data: body,
    }),
};

// ==================== 共享邮箱池 ====================

export interface MailPoolListQuery {
  status?: MailPoolStatus | '';
  mode?: MailPoolMode | '';
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const mailPoolApi = {
  list: (q: MailPoolListQuery = {}) =>
    request<PageData<MailPoolItem>>({ url: '/mail-pool', method: 'GET', params: q }),
  stats: () => request<MailPoolStatsResp>({ url: '/mail-pool/stats', method: 'GET' }),
  import: (body: MailPoolImportBody) =>
    request<MailPoolImportResult>({ url: '/mail-pool/import', method: 'POST', data: body }),
  cfGenerate: (body: MailPoolCFGenerateBody) =>
    request<MailPoolCFGenerateResp>({ url: '/mail-pool/cf-generate', method: 'POST', data: body }),
  update: (id: number, body: MailPoolUpdateBody) =>
    request<void>({ url: `/mail-pool/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/mail-pool/${id}`, method: 'DELETE' }),
  batchDelete: (ids: number[]) =>
    request<MailPoolBulkOpResult>({ url: '/mail-pool/batch-delete', method: 'POST', data: { ids } }),
  deleteByStatus: (status: 'failed' | 'in_use' | 'registered') =>
    request<MailPoolBulkOpResult>({ url: '/mail-pool/delete-by-status', method: 'POST', data: { status } }),
  truncate: (body: { confirm: 'DELETE'; status?: string; mode?: string; keyword?: string }) =>
    request<MailPoolBulkOpResult>({ url: '/mail-pool/truncate', method: 'POST', data: body }),
  reset: (ids: number[]) =>
    request<MailPoolBulkOpResult>({ url: '/mail-pool/reset', method: 'POST', data: { ids } }),
};

// ==================== 号池 - ADOBE ====================

export interface AdobePoolListQuery {
  status?: AdobePoolStatus | '' | 'quota_recovery';
  source?: AdobePoolSource | '';
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const poolAdobeApi = {
  list: (q: AdobePoolListQuery = {}) =>
    request<PageData<AdobePoolItem>>({ url: '/pools/adobe', method: 'GET', params: q }),
  stats: () => request<AdobePoolStatsResp>({ url: '/pools/adobe/stats', method: 'GET' }),
  create: (body: AdobePoolCreateBody) =>
    request<{ id: number }>({ url: '/pools/adobe', method: 'POST', data: body }),
  update: (id: number, body: AdobePoolUpdateBody) =>
    request<void>({ url: `/pools/adobe/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/pools/adobe/${id}`, method: 'DELETE' }),
  import: (body: AdobePoolImportBody) =>
    request<AdobePoolImportResult>({ url: '/pools/adobe/import', method: 'POST', data: body }),
  batchDelete: (ids: number[]) =>
    request<AdobePoolBulkOpResult>({ url: '/pools/adobe/batch-delete', method: 'POST', data: { ids } }),
  // 立即触发一个账号的 silent refresh（换 token + 拉 profile + 拉 credits）
  refresh: (id: number, onlyCredits = false) =>
    request<{ id: number; credits: number; expires_at?: number }>({
      url: `/pools/adobe/${id}/refresh`,
      method: 'POST',
      params: onlyCredits ? { only_credits: 1 } : undefined,
    }),
  // 批量扫描：把 < 12h 即将过期的账号一次性刷一遍
  refreshAll: () =>
    request<{ ok: number; fail: number }>({ url: '/pools/adobe/refresh-all', method: 'POST' }),
  // 按条件批量软删（all / status=invalid / zero_credits / token_expired / quota_recovery_days）
  purge: (body: { all?: boolean; status?: string; zero_credits?: boolean; token_expired?: boolean; quota_recovery_days?: number }) =>
    request<AdobePoolBulkOpResult>({ url: '/pools/adobe/purge', method: 'POST', data: body }),
  // 按 scope + only_credits 模式批量刷新
  batchRefresh: (body: { scope: 'all' | 'zero_credits' | 'abnormal' | 'expiring' | 'quota_recovery'; only_credits?: boolean }) =>
    request<{ ok: number; fail: number; total: number }>({
      url: '/pools/adobe/batch-refresh',
      method: 'POST',
      data: body,
    }),
  // 导出 JSON Array（含 email/password/access_token/cookie 等所有字段，明文）
  // 返回原始 JSON 字符串 + count（解析交给前端做下载，不在这里 JSON.parse）
  exportText: async (scope: 'all' | 'valid' | 'invalid') => {
    const resp = await requestRaw<string>({
      url: '/pools/adobe/export',
      method: 'GET',
      params: { scope },
      responseType: 'text',
      transformResponse: [(d: unknown) => (typeof d === 'string' ? d : String(d))],
    });
    const count = Number(resp.headers['x-klein-export-count'] ?? '0') || 0;
    return { text: resp.data ?? '', count };
  },
};

export const poolGoogleApi = {
  list: (q: GooglePoolListQuery = {}) =>
    request<PageData<GooglePoolItem>>({ url: '/pools/google', method: 'GET', params: q }),
  stats: () => request<GooglePoolStatsResp>({ url: '/pools/google/stats', method: 'GET' }),
  create: (body: GooglePoolCreateBody) =>
    request<{ id: number }>({ url: '/pools/google', method: 'POST', data: body }),
  update: (id: number, body: GooglePoolUpdateBody) =>
    request<void>({ url: `/pools/google/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/pools/google/${id}`, method: 'DELETE' }),
  import: (body: GooglePoolImportBody) =>
    request<GooglePoolImportResult>({ url: '/pools/google/import', method: 'POST', data: body }),
  batchDelete: (ids: number[]) =>
    request<GooglePoolBulkOpResult>({ url: '/pools/google/batch-delete', method: 'POST', data: { ids } }),
  refresh: (id: number, onlyCredits = false) =>
    request<{ id: number; credits: number; expires_at?: number }>({
      url: `/pools/google/${id}/refresh`,
      method: 'POST',
      params: onlyCredits ? { only_credits: 1 } : undefined,
    }),
  refreshAll: () =>
    request<{ ok: number; fail: number }>({ url: '/pools/google/refresh-all', method: 'POST' }),
  batchRefresh: (body: { scope: 'all' | 'abnormal' | 'expiring'; only_credits?: boolean }) =>
    request<{ ok: number; fail: number; total: number }>({
      url: '/pools/google/batch-refresh',
      method: 'POST',
      data: body,
    }),
};

// ==================== 号池 - 官方 GROK (xAI API) ====================

export const poolXaiApi = {
  list: (q: XaiPoolListQuery = {}) =>
    request<PageData<XaiPoolItem>>({ url: '/pools/xai', method: 'GET', params: q }),
  stats: () => request<XaiPoolStatsResp>({ url: '/pools/xai/stats', method: 'GET' }),
  create: (body: XaiPoolCreateBody) =>
    request<{ id: number }>({ url: '/pools/xai', method: 'POST', data: body }),
  update: (id: number, body: XaiPoolUpdateBody) =>
    request<void>({ url: `/pools/xai/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/pools/xai/${id}`, method: 'DELETE' }),
  import: (body: XaiPoolImportBody) =>
    request<XaiPoolImportResult>({ url: '/pools/xai/import', method: 'POST', data: body }),
  batchDelete: (ids: number[]) =>
    request<XaiPoolBulkOpResult>({ url: '/pools/xai/batch-delete', method: 'POST', data: { ids } }),
  purge: (body: { all?: boolean; status?: string; abnormal?: boolean }) =>
    request<XaiPoolBulkOpResult>({ url: '/pools/xai/purge', method: 'POST', data: body }),
  refresh: (id: number) =>
    request<XaiPoolRefreshResp>({ url: `/pools/xai/${id}/refresh`, method: 'POST' }),
  batchRefresh: (scope: 'all' | 'expiring' | 'abnormal') =>
    request<{ ok: number; fail: number; scope: string }>({
      url: '/pools/xai/batch-refresh',
      method: 'POST',
      data: { scope },
    }),
  refreshBilling: (id: number) =>
    request<XaiBillingResp>({ url: `/pools/xai/${id}/billing`, method: 'POST' }),
  refreshBillingAll: () =>
    request<{ ok: number; fail: number }>({ url: '/pools/xai/billing/refresh-all', method: 'POST' }),
};

// ==================== 号池 - GROK ====================

export interface GrokPoolListQuery {
  trial_status?: GrokTrialStatus | '';
  /** account_type 精确匹配；空串/缺省表示不限。 */
  account_type?: GrokAccountType;
  /** subscription_status 精确匹配；空串/缺省表示不限。 */
  subscription_status?: GrokSubscriptionStatus;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const poolGrokApi = {
  list: (q: GrokPoolListQuery = {}) =>
    request<PageData<GrokPoolItem>>({ url: '/pools/grok', method: 'GET', params: q }),
  stats: () => request<GrokPoolStatsResp>({ url: '/pools/grok/stats', method: 'GET' }),
  create: (body: GrokPoolCreateBody) =>
    request<{ id: number }>({ url: '/pools/grok', method: 'POST', data: body }),
  update: (id: number, body: GrokPoolUpdateBody) =>
    request<void>({ url: `/pools/grok/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/pools/grok/${id}`, method: 'DELETE' }),
  import: (body: GrokPoolImportBody) =>
    request<GrokPoolImportResult>({ url: '/pools/grok/import', method: 'POST', data: body }),
  batchDelete: (ids: number[]) =>
    request<GrokPoolBulkOpResult>({ url: '/pools/grok/batch-delete', method: 'POST', data: { ids } }),
  expireOverdue: () =>
    request<GrokPoolBulkOpResult>({ url: '/pools/grok/expire-overdue', method: 'POST' }),
  purge: (body: GrokPoolPurgeBody) =>
    request<GrokPoolBulkOpResult>({ url: '/pools/grok/purge', method: 'POST', data: body }),
  /**
   * batchRefresh **异步**启动批量刷新。立即返回 job 快照，前端用
   * batchRefreshStatus() 轮询进度，直到 status !== 'running'。
   *
   * 409 = 已有任务在跑（resp.data 仍是当前 job 快照，前端可以直接接管轮询）。
   */
  batchRefresh: (body: GrokPoolBatchRefreshBody) =>
    request<GrokPoolBatchRefreshJob>({
      url: '/pools/grok/batch-refresh',
      method: 'POST',
      data: body,
    }),
  batchRefreshStatus: () =>
    request<GrokPoolBatchRefreshJob>({
      url: '/pools/grok/batch-refresh/status',
      method: 'GET',
    }),
  batchRefreshCancel: () =>
    request<{ cancelled: boolean; job?: GrokPoolBatchRefreshJob }>({
      url: '/pools/grok/batch-refresh/cancel',
      method: 'POST',
    }),
  refresh: (id: number) =>
    request<GrokPoolRefreshResult>({ url: `/pools/grok/${id}/refresh`, method: 'POST' }),
};

// ==================== 号池 - GPT ====================

// GptPoolPlanFilter 列表筛选用：除了官方档位之外，'__unsubscribed' 是
// 「Free 或未探测」的聚合值，用于"哪些号还能升 Plus"快查。
export type GptPoolPlanFilter = GptPlanType | '__unsubscribed';

export interface GptPoolListQuery {
  status?: GptPoolStatus | '';
  plan_type?: GptPoolPlanFilter | '';
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const poolGptApi = {
  list: (q: GptPoolListQuery = {}) =>
    request<PageData<GptPoolItem>>({ url: '/pools/gpt', method: 'GET', params: q }),
  stats: () => request<GptPoolStatsResp>({ url: '/pools/gpt/stats', method: 'GET' }),
  detail: (id: number) =>
    request<GptPoolDetail>({ url: `/pools/gpt/${id}`, method: 'GET' }),
  create: (body: GptPoolCreateBody) =>
    request<{ id: number }>({ url: '/pools/gpt', method: 'POST', data: body }),
  update: (id: number, body: GptPoolUpdateBody) =>
    request<void>({ url: `/pools/gpt/${id}`, method: 'PUT', data: body }),
  remove: (id: number) => request<void>({ url: `/pools/gpt/${id}`, method: 'DELETE' }),
  import: (body: GptPoolImportBody) =>
    request<GptPoolImportResult>({ url: '/pools/gpt/import', method: 'POST', data: body }),
  batchDelete: (ids: number[]) =>
    request<GptPoolBulkOpResult>({ url: '/pools/gpt/batch-delete', method: 'POST', data: { ids } }),
  // 单条刷新（对齐 Adobe pool refresh）：refresh AT/RT + wham/usage 拿 plan/quota。
  // onlyQuota=true 时跳过换 token，仅拉 quota（用于 30min 增量探测）。
  refresh: (id: number, onlyQuota = false) =>
    request<GptPoolDetail>({
      url: `/pools/gpt/${id}/refresh`,
      method: 'POST',
      params: onlyQuota ? { only_quota: '1' } : undefined,
    }),
  // 一键扫描即将过期 + quota 过时的账号。
  refreshAll: () =>
    request<GptPoolRefreshAllResp>({ url: '/pools/gpt/refresh-all', method: 'POST' }),
  // 按 scope 批量刷新（对齐 Adobe BatchRefresh）。
  batchRefresh: (body: GptPoolBatchRefreshBody = {}) =>
    request<GptPoolBatchRefreshResp>({
      url: '/pools/gpt/batch-refresh',
      method: 'POST',
      data: body,
    }),
  // 按 scope 物理（软）删除：all / invalid / token_expired / quota_exceeded / no_refresh。
  purge: (body: GptPoolPurgeBody) =>
    request<GptPoolBulkOpResult>({ url: '/pools/gpt/purge', method: 'POST', data: body }),
  // 批量导出（4 种格式 × 4 种 scope，selected 时 ids 必传）。
  // 返回原始字符串 + count，前端负责生成下载文件名 / Blob。
  exportText: async (
    scope: 'all' | 'valid' | 'invalid' | 'selected',
    format: 'internal' | 'crs' | 'codex' | 'account_password' = 'internal',
    ids?: number[],
  ) => {
    // selected 时走 POST + body 避免 query string 过长
    const useSelected = scope === 'selected' && ids && ids.length > 0;
    const resp = await requestRaw<string>(
      useSelected
        ? {
            url: '/pools/gpt/export',
            method: 'POST',
            params: { scope, format },
            data: { ids },
            responseType: 'text',
            transformResponse: [(d: unknown) => (typeof d === 'string' ? d : String(d))],
          }
        : {
            url: '/pools/gpt/export',
            method: 'GET',
            params: { scope, format },
            responseType: 'text',
            transformResponse: [(d: unknown) => (typeof d === 'string' ? d : String(d))],
          },
    );
    const count = Number(resp.headers['x-klein-export-count'] ?? '0') || 0;
    return { text: resp.data ?? '', count };
  },
};

// ==================== 号池注册任务 ====================

export const registerTaskApi = {
  list: (q: RegisterTaskListReq = {}) =>
    request<PageData<RegisterTaskItem>>({ url: '/register-tasks', method: 'GET', params: q }),
  stats: (provider?: RegisterTaskProvider) =>
    request<RegisterTaskStatsResp>({
      url: '/register-tasks/stats',
      method: 'GET',
      params: provider ? { provider } : undefined,
    }),
  get: (id: number) =>
    request<RegisterTaskItem>({ url: `/register-tasks/${id}`, method: 'GET' }),
  create: (body: RegisterTaskCreateBody) =>
    request<RegisterTaskCreateResp>({ url: '/register-tasks', method: 'POST', data: body }),
  cancel: (id: number) =>
    request<void>({ url: `/register-tasks/${id}/cancel`, method: 'POST' }),
  remove: (id: number) =>
    request<void>({ url: `/register-tasks/${id}`, method: 'DELETE' }),
  purge: (provider?: RegisterTaskProvider) =>
    request<{ deleted: number }>({
      url: '/register-tasks',
      method: 'DELETE',
      params: provider ? { provider } : undefined,
    }),
  logs: (q: RegisterTaskLogQuery = {}) =>
    request<{ list: RegisterTaskLogEntry[] }>({
      url: '/register-tasks/logs',
      method: 'GET',
      params: q,
    }),
  logsPurge: (q: Omit<RegisterTaskLogQuery, 'limit'> = {}) =>
    request<{ deleted: number }>({
      url: '/register-tasks/logs',
      method: 'DELETE',
      params: q,
    }),
};

// ==================== Plus 升级资源池 ====================
//
// 三组面板共用一个路由前缀，按资源类型拆开：
//   /cloud-phones    GeeLark 云手机（用于读 WhatsApp OTP）
//   /gopay-wallets   GoPay 钱包池 + 钱包-Plus 绑定
//   /payment-proxies 印尼住宅代理（GoPay Phase B 专用）

export const cloudPhoneApi = {
  list: (q: CloudPhoneListReq = {}) =>
    request<PageData<CloudPhoneItem>>({ url: '/cloud-phones', method: 'GET', params: q }),
  stats: () =>
    request<Record<string, number>>({ url: '/cloud-phones/stats', method: 'GET' }),
  create: (body: CloudPhoneCreateBody) =>
    request<{ id: string }>({ url: '/cloud-phones', method: 'POST', data: body }),
  update: (id: string, body: CloudPhoneUpdateBody) =>
    request<void>({ url: `/cloud-phones/${encodeURIComponent(id)}`, method: 'PUT', data: body }),
  remove: (id: string) =>
    request<void>({ url: `/cloud-phones/${encodeURIComponent(id)}`, method: 'DELETE' }),
  batchDelete: (ids: string[]) =>
    request<CloudPhoneBulkOpResult>({
      url: '/cloud-phones/batch-delete',
      method: 'POST',
      data: { ids },
    }),
  import: (body: CloudPhoneImportBody) =>
    request<CloudPhoneImportResult>({ url: '/cloud-phones/import', method: 'POST', data: body }),
  /** 云手机内自动化：GoPay「已连接应用」→ 移除 OpenAI（uiautomator + 模拟点击，约 1～3 分钟） */
  gopayUnlinkOpenAI: (id: string, body: { app_package?: string } = {}) =>
    request<{ ok: boolean }>({
      url: `/cloud-phones/${encodeURIComponent(id)}/gopay-unlink-openai`,
      method: 'POST',
      data: body,
    }),
};

export const gopayWalletApi = {
  list: (q: GopayWalletListReq = {}) =>
    request<PageData<GopayWalletItem>>({ url: '/gopay-wallets', method: 'GET', params: q }),
  stats: () =>
    request<Record<string, number>>({ url: '/gopay-wallets/stats', method: 'GET' }),
  create: (body: GopayWalletCreateBody) =>
    request<{ id: number }>({ url: '/gopay-wallets', method: 'POST', data: body }),
  update: (id: number, body: GopayWalletUpdateBody) =>
    request<void>({ url: `/gopay-wallets/${id}`, method: 'PUT', data: body }),
  remove: (id: number) =>
    request<void>({ url: `/gopay-wallets/${id}`, method: 'DELETE' }),
  batchDelete: (ids: number[]) =>
    request<GopayWalletBulkOpResult>({
      url: '/gopay-wallets/batch-delete',
      method: 'POST',
      data: { ids },
    }),
  import: (body: GopayWalletImportBody) =>
    request<GopayWalletImportResult>({ url: '/gopay-wallets/import', method: 'POST', data: body }),
  // 编辑表单需要明文 PIN 时调用（仅返回 PIN 字段）
  secrets: (id: number) =>
    request<GopayWalletSecretResp>({ url: `/gopay-wallets/${id}/secrets`, method: 'GET' }),
  // 钱包-Plus 绑定列表（每次成功开 Plus 写一行）
  listBindings: (q: GopayBindingListReq = {}) =>
    request<PageData<GopayBindingItem>>({
      url: '/gopay-wallets/bindings',
      method: 'GET',
      params: q,
    }),
  // 取消订阅（释放钱包配额，让 active_plus_count - 1）
  cancelBinding: (id: number, note?: string) =>
    request<void>({
      url: `/gopay-wallets/bindings/${id}/cancel`,
      method: 'POST',
      data: note ? { note } : {},
    }),
};

// ==================== 上游 API 管理（通道 / 路由 / 利润）====================

/**
 * Channel: 一个 provider + route + 计费方式的组合。
 * 单价根据 billing_mode 不同字段不同（unit_price 是 free-form JSON）。
 *
 * 后端 dto 是 dto.ChannelDTO；这里保留 snake_case 字段与之对齐。
 */
/**
 * Channel 二元类型：
 *  - local_pool   : 系统建好的唯一行（key=local.pool）；runtime 看到这条通道时按请求 model
 *                   反查 pool_gpt / pool_grok / pool_adobe 选号。不需要 api_key / base_url。
 *  - external_api : 第三方付费 API；自带 base_url + api_key + supported_models 列表，
 *                   runtime 用 OpenAI-compat 协议直接转发请求。
 */
export type ChannelType = 'local_pool' | 'external_api';

export interface UpstreamChannel {
  id: number;
  key: string;
  channel_type: ChannelType;
  provider: string;
  route: string;
  base_url: string;
  label: string;
  enabled: boolean;
  billing_mode: 'per_call' | 'per_unit' | 'per_token_io' | 'per_credit' | 'subscription' | 'custom';
  unit_price: Record<string, unknown>;
  api_key_masked?: string;
  has_api_key: boolean;
  currency: string;
  capabilities: Record<string, unknown>;
  supported_models?: string[];
  monthly_fixed_cost: number;
  expected_monthly_calls: number;
  fx_to_cny: number;
  notes?: string;
  created_at: string;
  updated_at: string;
}

export interface UpstreamChannelSaveBody {
  key?: string;
  channel_type?: ChannelType;
  provider?: string;
  route?: string;
  base_url?: string;
  label?: string;
  enabled?: boolean;
  billing_mode?: UpstreamChannel['billing_mode'];
  unit_price?: Record<string, unknown>;
  /** 留空 = 不变；填 "__CLEAR__" = 清空；其它非空 = 覆盖 */
  api_key?: string;
  currency?: string;
  capabilities?: Record<string, unknown>;
  supported_models?: string[];
  monthly_fixed_cost?: number;
  expected_monthly_calls?: number;
  fx_to_cny?: number;
  notes?: string;
}

export interface UpstreamRoute {
  id: number;
  model_code: string;
  variant_key: string;
  upstream_channel_id: number;
  channel_key?: string;
  channel_label?: string;
  channel_provider?: string;
  priority: number;
  enabled: boolean;
  cost_multiplier: number;
  notes?: string;
}

export interface UpstreamRouteSaveBody {
  model_code?: string;
  variant_key?: string;
  upstream_channel_id?: number;
  priority?: number;
  enabled?: boolean;
  cost_multiplier?: number;
  notes?: string;
}

export interface UpstreamProfitOverview {
  task_count: number;
  cost_micro_usd: number;
  sale_micro_cny: number;
  sale_points: number;
  gross_margin_micro_cny: number;
  gross_margin_rate: number;
  fx_usd_to_cny: number;
}

export interface UpstreamProfitDailyRow {
  day: string;
  task_count: number;
  cost_micro_usd: number;
  sale_micro_cny: number;
  sale_points: number;
  avg_fx?: number;
  model_code?: string;
  upstream_channel_id?: number;
  provider?: string;
}

export const upstreamApi = {
  listChannels: (q: { provider?: string; enabled?: boolean; keyword?: string; page?: number; page_size?: number } = {}) =>
    request<PageData<UpstreamChannel>>({ url: '/upstream/channels', method: 'GET', params: q }),
  createChannel: (body: UpstreamChannelSaveBody) =>
    request<{ id: number }>({ url: '/upstream/channels', method: 'POST', data: body }),
  updateChannel: (id: number, body: UpstreamChannelSaveBody) =>
    request<{ ok: boolean }>({ url: `/upstream/channels/${id}`, method: 'PUT', data: body }),
  removeChannel: (id: number) =>
    request<{ ok: boolean }>({ url: `/upstream/channels/${id}`, method: 'DELETE' }),
  // seed 15 行默认通道；通道表非空时直接 no-op。
  seedChannels: () =>
    request<{ ok: boolean }>({ url: '/upstream/channels/seed', method: 'POST' }),
  listRoutes: (q: { model_code?: string; variant_key?: string; channel_id?: number; enabled?: boolean; page?: number; page_size?: number } = {}) =>
    request<PageData<UpstreamRoute>>({ url: '/upstream/routes', method: 'GET', params: q }),
  createRoute: (body: UpstreamRouteSaveBody) =>
    request<{ id: number }>({ url: '/upstream/routes', method: 'POST', data: body }),
  updateRoute: (id: number, body: UpstreamRouteSaveBody) =>
    request<{ ok: boolean }>({ url: `/upstream/routes/${id}`, method: 'PUT', data: body }),
  removeRoute: (id: number) =>
    request<{ ok: boolean }>({ url: `/upstream/routes/${id}`, method: 'DELETE' }),
  profitOverview: (q: { from?: string; to?: string } = {}) =>
    request<UpstreamProfitOverview>({ url: '/upstream/profit/overview', method: 'GET', params: q }),
  profitDaily: (q: { from?: string; to?: string; dim?: 'day' | 'day,model' | 'day,channel' | 'day,provider' } = {}) =>
    request<{ items: UpstreamProfitDailyRow[]; fx_usd_to_cny: number }>({ url: '/upstream/profit/daily', method: 'GET', params: q }),
  /** 成本日志明细 — task_cost_log。字段是 free-form，前端按需展示。 */
  costLogs: (q: {
    page?: number;
    page_size?: number;
    ref_type?: string;
    model_code?: string;
    channel_id?: number;
    user_id?: number;
    from?: string;
    to?: string;
  } = {}) =>
    request<PageData<Record<string, unknown>>>({ url: '/upstream/logs', method: 'GET', params: q }),
};

export const paymentProxyApi = {
  list: (q: PaymentProxyListReq = {}) =>
    request<PageData<PaymentProxyItem>>({ url: '/payment-proxies', method: 'GET', params: q }),
  stats: () =>
    request<Record<string, number>>({ url: '/payment-proxies/stats', method: 'GET' }),
  create: (body: PaymentProxyCreateBody) =>
    request<{ id: number }>({ url: '/payment-proxies', method: 'POST', data: body }),
  update: (id: number, body: PaymentProxyUpdateBody) =>
    request<void>({ url: `/payment-proxies/${id}`, method: 'PUT', data: body }),
  remove: (id: number) =>
    request<void>({ url: `/payment-proxies/${id}`, method: 'DELETE' }),
  batchDelete: (ids: number[]) =>
    request<PaymentProxyBulkOpResult>({
      url: '/payment-proxies/batch-delete',
      method: 'POST',
      data: { ids },
    }),
  import: (body: PaymentProxyImportBody) =>
    request<PaymentProxyImportResult>({
      url: '/payment-proxies/import',
      method: 'POST',
      data: body,
    }),
  // 单条连通性测试（返回出口 IP / 国家 / 延迟）
  test: (id: number) =>
    request<PaymentProxyTestResp>({ url: `/payment-proxies/${id}/test`, method: 'POST' }),
};

// ── 集群节点 ─────────────────────────────────────────────────

export type ClusterNodeRole = 'control' | 'agent' | 'edge';
export type ClusterNodeStatus = 0 | 1 | 2 | 3 | 9; // 0待激活 1启用 2禁用 3维护 9吊销

export interface ClusterNodeItem {
  node_id: string;
  display_name: string;
  role: ClusterNodeRole;
  public_host: string;
  internal_host?: string;
  provider_scope: string[];
  weight: number;
  max_concurrency: number;
  download_only: boolean;
  allowed_ips?: string;
  status: ClusterNodeStatus;
  status_label: string;
  has_secret: boolean;
  bootstrap_used: boolean;
  last_heartbeat_at?: string;
  heartbeat_age_sec?: number;
  last_inflight: number;
  last_ip?: string;
  version?: string;
  /** 反向 /healthz 探活的连续失败次数；3 次会被踢到 Maintenance。 */
  ping_fail_streak: number;
  created_at: string;
  updated_at: string;
}

/** GET /cluster/overview 实时摘要，ConfigPage 集群卡片使用。 */
export interface ClusterOverview {
  enabled: boolean;
  embedded_alive: boolean;
  embedded_heartbeat_at?: string;
  embedded_inflight: number;
  total_nodes: number;
  online_agents: number;
  maintenance_nodes: number;
  lease_ttl_sec: number;
  heartbeat_dead_sec: number;
  ticket_ttl_sec: number;
}

export interface ClusterNodeUpsertBody {
  node_id: string;
  display_name?: string;
  role?: ClusterNodeRole;
  public_host?: string;
  internal_host?: string;
  provider_scope?: string[];
  weight?: number;
  max_concurrency?: number;
  download_only?: boolean;
  allowed_ips?: string;
}

export interface ClusterNodeUpsertResp {
  node: ClusterNodeItem;
  bootstrap_token?: string;
  control_url?: string;
}

export interface ClusterNodeListReq {
  role?: ClusterNodeRole;
  status?: ClusterNodeStatus;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export const clusterApi = {
  overview: () => request<ClusterOverview>({ url: '/cluster/overview', method: 'GET' }),
  list: (q: ClusterNodeListReq = {}) =>
    request<PageData<ClusterNodeItem>>({ url: '/cluster/nodes', method: 'GET', params: q }),
  upsert: (body: ClusterNodeUpsertBody) =>
    request<ClusterNodeUpsertResp>({ url: '/cluster/nodes', method: 'POST', data: body }),
  setStatus: (id: string, status: ClusterNodeStatus) =>
    request<void>({ url: `/cluster/nodes/${encodeURIComponent(id)}/status`, method: 'POST', data: { status } }),
  revoke: (id: string) =>
    request<void>({ url: `/cluster/nodes/${encodeURIComponent(id)}/revoke`, method: 'POST' }),
  remove: (id: string) =>
    request<void>({ url: `/cluster/nodes/${encodeURIComponent(id)}`, method: 'DELETE' }),
  reissueBootstrap: (id: string) =>
    request<{ node_id: string; bootstrap_token: string; control_url?: string }>({
      url: `/cluster/nodes/${encodeURIComponent(id)}/bootstrap`,
      method: 'POST',
    }),
};

// ─── 系统公告 ─────────────────────────────────────────────────────────────────
export const announcementsApi = {
  list: (p?: { page?: number; page_size?: number }) =>
    request<{ list: Announcement[]; total: number }>({ url: '/announcements', method: 'GET', params: p }),
  create: (body: AnnouncementCreateReq) =>
    request<Announcement>({ url: '/announcements', method: 'POST', data: body }),
  update: (id: number, body: AnnouncementUpdateReq) =>
    request<Announcement>({ url: `/announcements/${id}`, method: 'PUT', data: body }),
  remove: (id: number) =>
    request<void>({ url: `/announcements/${id}`, method: 'DELETE' }),
};
