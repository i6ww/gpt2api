// 高层 API 服务：分领域封装，UI 层只看 services.* 不直接 import axios。
import { request } from './api';
import type {
  Announcement,
  APIKey,
  APIKeyCreateBody,
  APIKeyCreated,
  APIKeyStatsQuery,
  APIKeyStatsResp,
  CreateImageBody,
  CreateMusicBody,
  CreateTextBody,
  CreateVideoBody,
  GenerationTask,
  Invitee,
  InviteSummary,
  LoginResp,
  MeResp,
  PageData,
  PublicModel,
  RechargeProductsResp,
  RedeemCDKResp,
  RegisterResp,
  TextGenerationResp,
  TokenPair,
  WalletLog,
} from './types';

export const authApi = {
  register: (body: { account: string; password: string; invite_code?: string }) =>
    request<RegisterResp>({ method: 'POST', url: '/auth/register', data: body }),
  login: (body: { account: string; password: string }) =>
    request<LoginResp>({ method: 'POST', url: '/auth/login', data: body }),
  refresh: (refresh_token: string) =>
    request<TokenPair>({ method: 'POST', url: '/auth/refresh', data: { refresh_token } }),
  logout: () => request<null>({ method: 'POST', url: '/auth/logout' }),
  me: () => request<MeResp>({ method: 'GET', url: '/users/me' }),
  changePassword: (body: { old_password: string; new_password: string }) =>
    request<null>({ method: 'POST', url: '/users/password', data: body }),
};

export const keysApi = {
  list: async () => {
    const r = await request<{ list: APIKey[] } | APIKey[] | null>({ method: 'GET', url: '/keys' });
    if (Array.isArray(r)) return r;
    return r?.list ?? [];
  },
  create: (body: APIKeyCreateBody) =>
    request<APIKeyCreated>({ method: 'POST', url: '/keys', data: body }),
  toggle: ({ id, enable }: { id: number; enable: boolean }) =>
    request<null>({
      method: 'POST',
      url: `/keys/${id}/toggle`,
      params: { enable: enable ? 1 : 0 },
    }),
  remove: (id: number) => request<null>({ method: 'DELETE', url: `/keys/${id}` }),
  stats: (q: APIKeyStatsQuery = {}) =>
    request<APIKeyStatsResp>({
      method: 'GET',
      url: '/keys/stats',
      params: {
        since: q.since && q.since > 0 ? q.since : undefined,
        until: q.until && q.until > 0 ? q.until : undefined,
      },
    }),
};

export const billingApi = {
  logs: (page = 1, pageSize = 10) =>
    request<PageData<WalletLog>>({
      method: 'GET',
      url: '/billing/logs',
      params: { page, page_size: pageSize },
    }),
  redeemCDK: (code: string) =>
    request<RedeemCDKResp>({ method: 'POST', url: '/billing/cdk/redeem', data: { code } }),
  // 充值套餐 + 客服联系方式。后端 /v1/recharge/products 免登录、已过滤敏感字段。
  rechargeProducts: () =>
    request<RechargeProductsResp>({ method: 'GET', url: '/recharge/products' }),
};

// 公告：用户端首页顶部滚动条，匿名也可调用（后端 /api/v1/announcements 不要求 auth）。
export const announcementsApi = {
  list: async (): Promise<Announcement[]> => {
    const r = await request<{ list: Announcement[] } | Announcement[] | null>({
      method: 'GET',
      url: '/announcements',
    });
    if (Array.isArray(r)) return r;
    return r?.list ?? [];
  },
};

export const genApi = {
  models: async () => {
    const r = await request<{ list: PublicModel[] } | PublicModel[] | null>({ method: 'GET', url: '/models' });
    if (Array.isArray(r)) return r;
    return r?.list ?? [];
  },
  createText: (body: CreateTextBody, idemKey?: string) =>
    request<TextGenerationResp>({
      method: 'POST',
      url: '/gen/text',
      data: body,
      headers: idemKey ? { 'Idempotency-Key': idemKey } : undefined,
    }),
  createImage: (body: CreateImageBody, idemKey?: string) =>
    request<GenerationTask>({
      method: 'POST',
      url: '/gen/image',
      data: body,
      headers: idemKey ? { 'Idempotency-Key': idemKey } : undefined,
    }),
  createVideo: (body: CreateVideoBody, idemKey?: string) =>
    request<GenerationTask>({
      method: 'POST',
      url: '/gen/video',
      data: body,
      headers: idemKey ? { 'Idempotency-Key': idemKey } : undefined,
    }),
  createMusic: (body: CreateMusicBody, idemKey?: string) =>
    request<GenerationTask>({
      method: 'POST',
      url: '/gen/music',
      data: body,
      headers: idemKey ? { 'Idempotency-Key': idemKey } : undefined,
    }),
  getTask: (taskId: string) =>
    request<GenerationTask>({ method: 'GET', url: `/gen/tasks/${taskId}` }),
  history: (params: { kind?: 'image' | 'video' | 'media' | 'music'; page?: number; page_size?: number } = {}) =>
    request<PageData<GenerationTask>>({
      method: 'GET',
      url: '/gen/history',
      params: {
        kind: params.kind,
        page: params.page ?? 1,
        page_size: params.page_size ?? 20,
      },
    }),
  deleteHistory: (scope: 'before_3d' | 'before_7d' | 'failed' | 'all') =>
    request<{ deleted: number }>({ method: 'DELETE', url: '/gen/history', params: { scope } }),
  /** 边缘节点下载失败时主动汇报；服务端把 (asset_kind, asset_key, node_id) 标 tainted。 */
  reportTainted: (body: { asset_kind?: string; asset_key: string; node_id: string; reason?: string }) =>
    request<{ ok: boolean }>({ method: 'POST', url: '/gen/cached/tainted', data: body }),
};

export const inviteApi = {
  summary: () => request<InviteSummary>({ method: 'GET', url: '/invite/summary' }),
  invitees: (page = 1, pageSize = 10) =>
    request<PageData<Invitee>>({
      method: 'GET',
      url: '/invite/invitees',
      params: { page, page_size: pageSize },
    }),
};
