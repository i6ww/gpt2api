// 后台管理 - 与后端 dto / response 对齐的前端类型。
// 注意：所有 *_points / points 字段单位为「点 *100」，展示请除以 100。

export interface ApiBody<T> {
  code: number;
  msg: string;
  data?: T;
  trace_id?: string;
}

export interface PageData<T> {
  list: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface AdminLoginResp {
  id: number;
  username: string;
  nickname: string;
  role_id: number;
  token: {
    access_token: string;
    refresh_token: string;
    token_type: string;
    access_expire_in: number;
    refresh_expire_in: number;
  };
}

export interface AdminMe {
  id: number;
  username: string;
  nickname: string;
  email?: string;
  role_id: number;
  role_code: string;
  role_name: string;
}

/** 账号池条目 */
export interface AdminUserItem {
  id: number;
  uuid: string;
  email?: string;
  phone?: string;
  username?: string;
  avatar?: string;
  points: number;
  frozen_points: number;
  total_recharge: number;
  plan_code: string;
  plan_expire_at?: number;
  inviter_id?: number;
  invite_code: string;
  status: 0 | 1 | number;
  register_ip?: string;
  last_login_at?: number;
  last_login_ip?: string;
  created_at: number;
  updated_at: number;
}

export interface AdminUserCreateBody {
  account: string;
  password: string;
  username?: string;
  points?: number;
  status?: 0 | 1;
}

export interface AdminUserUpdateBody {
  email?: string | null;
  phone?: string | null;
  username?: string | null;
  avatar?: string | null;
  password?: string;
  status?: 0 | 1;
  plan_code?: string;
  plan_expire_at?: number | null;
}

export interface AdminUserAdjustPointsBody {
  action: 'recharge' | 'deduct';
  points: number;
  remark?: string;
}

export interface AdminUserAdjustPointsResp {
  points_before: number;
  points_after: number;
}

export interface AdminGenerationLogItem {
  task_id: string;
  created_at: number;
  user_id: number;
  user_label: string;
  api_key_id?: number;
  key_label?: string;
  kind: 'image' | 'video' | string;
  model_code: string;
  prompt: string;
  status: 0 | 1 | 2 | 3 | 4 | number;
  duration_ms?: number;
  cost_points: number;
  resolution?: string;
  aspect_ratio?: string;
  preview_url?: string;
  asset_url?: string;
  error?: string;
}

export interface AdminGenerationLogPurgeResp {
  deleted: number;
}

export interface AdminGenerationStuckCleanupResp {
  cleaned: number;
}

export interface AdminGenerationUpstreamLogItem {
  id: number;
  task_id: string;
  provider: string;
  account_id?: number;
  stage: string;
  method?: string;
  url?: string;
  status_code: number;
  duration_ms: number;
  request_excerpt?: string;
  response_excerpt?: string;
  error?: string;
  meta?: string;
  created_at: number;
}

export interface AdminWalletLogItem {
  id: number;
  created_at: number;
  user_id: number;
  user_label: string;
  direction: 1 | -1 | number;
  biz_type: string;
  biz_id: string;
  points: number;
  points_before: number;
  points_after: number;
  remark?: string;
}

export interface AdminWalletLogSummary {
  recharge_today: number;
  recharge_total: number;
  consume_today: number;
  consume_total: number;
  refund_today: number;
  refund_total: number;
  net_today: number;
  net_total: number;
  records_today: number;
  records_total: number;
  users_touched: number;
}

export interface AdminPromoItem {
  id: number;
  code: string;
  name: string;
  discount_type: 1 | 2 | 3 | number;
  discount_val: number;
  min_amount: number;
  apply_to: string;
  total_qty: number;
  used_qty: number;
  per_user_limit: number;
  start_at: number;
  end_at: number;
  status: 0 | 1 | number;
  created_at: number;
  updated_at: number;
}

export interface AdminPromoBody {
  code?: string;
  name?: string;
  discount_type?: 1 | 2 | 3;
  discount_val?: number;
  min_amount?: number;
  apply_to?: string;
  total_qty?: number;
  per_user_limit?: number;
  start_at?: number;
  end_at?: number;
  status?: 0 | 1;
}

export interface DashboardProviderRow {
  provider: string;
  total: number;
  enabled: number;
  available: number;
  broken: number;
  test_ok: number;
  quota_remaining: number;
  quota_total: number;
  quota_used: number;
  success_count: number;
  error_count: number;
}

export interface DashboardRecentTask {
  task_id: string;
  created_at: number;
  user_label: string;
  kind: 'image' | 'video' | string;
  model_code: string;
  count: number;
  status: number;
  cost_points: number;
}

export interface DashboardTrendPoint {
  date: string;
  generated: number;
  cost_points: number;
}

export interface DashboardOverviewResp {
  generated_today: number;
  generated_total: number;
  image_today: number;
  image_total: number;
  video_today: number;
  video_total: number;
  text_tokens_today: number;
  text_tokens_total: number;
  cost_points_today: number;
  cost_points_total: number;
  wallet_spend_today: number;
  wallet_spend_total: number;
  users_total: number;
  users_today: number;
  active_users_today: number;
  success_rate_today: number;
  account_providers: DashboardProviderRow[];
  recent_generations: DashboardRecentTask[];
  trend: DashboardTrendPoint[];
}

export interface AccountItem {
  id: number;
  provider: 'gpt' | 'grok' | string;
  name: string;
  auth_type: 'api_key' | 'cookie' | 'oauth' | string;
  credential_mask: string;
  base_url?: string;
  proxy_id?: number;
  weight: number;
  rpm_limit: number;
  tpm_limit: number;
  daily_quota: number;
  monthly_quota: number;
  /** -1 软删 / 0 禁用 / 1 启用 / 2 熔断 */
  status: -1 | 0 | 1 | 2 | number;
  cooldown_until?: number;
  last_used_at?: number;
  last_error?: string;
  error_count: number;
  success_count: number;
  remark?: string;
  /** OAuth 状态 */
  has_refresh_token?: boolean;
  has_access_token?: boolean;
  access_token_expire_at?: number;
  last_refresh_at?: number;
  /** 最近一次连通性测试 */
  last_test_at?: number;
  /** 0 未测 / 1 OK / 2 FAIL */
  last_test_status?: 0 | 1 | 2 | number;
  last_test_latency_ms?: number;
  last_test_error?: string;
  plan_type?: string;
  default_model?: string;
  image_quota_remaining?: number;
  image_quota_total?: number;
  image_quota_reset_at?: number;
  supported_models?: string[];
  created_at: number;
  updated_at: number;
}

/** 账号连通性测试结果 */
export interface AccountTestResp {
  ok: boolean;
  latency_ms: number;
  error?: string;
  plan_type?: string;
  default_model?: string;
  image_quota_remaining?: number;
  image_quota_total?: number;
  image_quota_reset_at?: number;
  supported_models?: string[];
}

export interface AccountModelsResp {
  supported_models: string[];
}

/** OAuth 刷新结果 */
export interface AccountRefreshResp {
  ok: boolean;
  expires_in?: number;
  refreshed_at: number;
  has_refresh_token: boolean;
}

/** 批量刷新结果 */
export interface AccountBatchRefreshResp {
  refreshed: number;
  failed_ids: number[];
  page: number;
  page_size: number;
  total: number;
  has_more: boolean;
  next_page?: number;
}

/** 创建账号入参（明文，后端加密）；OAuth 可与 sora2ok 一致拆 AT/RT/ST/client_id。 */
export interface AccountCreateBody {
  provider: 'gpt' | 'grok' | 'pic2api';
  name: string;
  auth_type: 'api_key' | 'cookie' | 'oauth';
  /** api_key / cookie 必填；oauth 可与 access_token / refresh_token 组合 */
  credential?: string;
  access_token?: string;
  refresh_token?: string;
  session_token?: string;
  client_id?: string;
  base_url?: string;
  /** 绑定代理 ID；0/undefined = 不绑定 */
  proxy_id?: number;
  weight?: number;
  rpm_limit?: number;
  tpm_limit?: number;
  daily_quota?: number;
  monthly_quota?: number;
  remark?: string;
}

/** POST /accounts/batch-delete、/accounts/purge 响应 */
export interface AccountBulkOpResult {
  deleted: number;
}

export interface AccountPurgeBody {
  scope: 'all' | 'invalid' | 'zero_quota';
  provider?: 'gpt' | 'grok' | 'pic2api';
  auth_type?: 'api_key' | 'cookie' | 'oauth' | 'token';
  confirm?: string;
}

/** 单个账号的明文凭证（管理员编辑面板回显用，解密失败为空串） */
export interface AccountSecretsResp {
  credential?: string;
  access_token?: string;
  refresh_token?: string;
  session_token?: string;
  client_id?: string;
}

export interface AccountUpdateBody {
  name?: string;
  credential?: string;
  /** OAuth 账号专用：单独替换三件套（空字符串表示清空对应列） */
  access_token?: string;
  refresh_token?: string;
  session_token?: string;
  client_id?: string;
  base_url?: string;
  /** 绑定代理 ID；0 = 不绑定 */
  proxy_id?: number;
  weight?: number;
  rpm_limit?: number;
  tpm_limit?: number;
  daily_quota?: number;
  monthly_quota?: number;
  status?: -1 | 0 | 1 | 2;
  remark?: string;
}

/** sub2api / Codex 导出 JSON 中单条账号 */
export interface Sub2APIAccountItem {
  name?: string;
  platform?: string;
  type?: string;
  priority?: number;
  concurrency?: number;
  credentials?: {
    access_token?: string;
    refresh_token?: string;
    client_id?: string;
    id_token?: string;
    email?: string;
    chatgpt_account_id?: string;
    chatgpt_user_id?: string;
    organization_id?: string;
    plan_type?: string;
  };
}

export interface AccountBatchImportBody {
  /** 默认 lines；sub2api 为 JSON 分片导入 */
  format?: 'lines' | 'sub2api';
  provider: 'gpt' | 'grok' | 'pic2api';
  /** lines 模式必填 */
  auth_type?: 'api_key' | 'cookie' | 'oauth';
  base_url?: string;
  /** 默认绑定代理 ID；0/undefined = 不绑定 */
  proxy_id?: number;
  weight?: number;
  /**
   * lines：一行一条；支持 `<name>@@<credential>` / `<credential>@<base_url>` / `<credential>`。
   */
  text?: string;
  /** sub2api：当前分片的账号列表（建议每批 ≤500） */
  accounts?: Sub2APIAccountItem[];
}

/** POST /accounts/import 响应 */
export interface AccountBatchImportResult {
  imported: number;
  skipped: number;
}

export interface PoolStatsResp {
  pool: Record<string, number>;
}
export interface CDKCreateBatchBody {
  batch_no: string;
  name: string;
  /** 单码价值（后端 *100，传 *100 后的整数） */
  points: number;
  qty: number;
  per_user_limit?: number;
  /** unix 秒；0/不传 = 永不过期 */
  expire_at?: number;
}

export interface CDKCreateBatchResp {
  id: number;
  batch_no: string;
  total_qty: number;
}

export interface AdminCDKBatchItem {
  id: number;
  batch_no: string;
  name: string;
  reward_type: string;
  reward_points: number;
  total_qty: number;
  used_qty: number;
  revoked_qty: number;
  remaining_qty: number;
  per_user_limit: number;
  /** 0=永久 */
  expire_at: number;
  /** 0=停用 1=启用 */
  status: 0 | 1;
  created_by?: number;
  created_at: number;
}

export interface AdminCDKBatchListQuery {
  keyword?: string;
  status?: 0 | 1 | '';
  page?: number;
  page_size?: number;
}

export interface AdminCDKBatchAppendResp {
  batch_id: number;
  appended: number;
  total_qty: number;
}

export interface AdminCDKCodeItem {
  id: number;
  batch_id: number;
  code: string;
  /** 0=unused 1=used 2=revoked */
  status: 0 | 1 | 2;
  used_by?: number;
  used_at?: number;
  created_at: number;
}

export interface AdminCDKCodeListQuery {
  status?: 0 | 1 | 2 | '';
  keyword?: string;
  page?: number;
  page_size?: number;
}

// ==================== 代理 ====================

export interface ProxyItem {
  id: number;
  name: string;
  protocol: 'http' | 'https' | 'socks5' | 'socks5h' | string;
  host: string;
  port: number;
  username?: string;
  has_password: boolean;
  /** 0 禁用 / 1 启用 */
  status: 0 | 1 | number;
  last_check_at?: number;
  /** 0 未测 / 1 OK / 2 FAIL */
  last_check_ok: 0 | 1 | 2 | number;
  last_check_ms: number;
  last_error?: string;
  remark?: string;
  created_at: number;
  updated_at: number;
}

export interface ProxyCreateBody {
  name: string;
  protocol: 'http' | 'https' | 'socks5' | 'socks5h';
  host: string;
  port: number;
  username?: string;
  password?: string;
  remark?: string;
}

export interface ProxyUpdateBody {
  name?: string;
  protocol?: 'http' | 'https' | 'socks5' | 'socks5h';
  host?: string;
  port?: number;
  username?: string;
  password?: string;
  status?: 0 | 1;
  remark?: string;
}

export interface ProxyTestResp {
  ok: boolean;
  latency_ms: number;
  error?: string;
}

export interface ProxyImportBody {
  text: string;
  remark?: string;
}

export interface ProxyImportResp {
  imported: number;
  skipped: number;
}

export interface ProxyBulkOpResult {
  deleted: number;
}

export interface ProxyBatchTestResp {
  tested: number;
  success: number;
  failed: number;
  failed_ids: number[];
}

// ==================== 系统配置 ====================

/** 已知 key（前端只列展示需要的，未列的也允许保存） */
export interface SystemSettings {
  /** 是否启用全局代理 */
  'proxy.global_enabled'?: boolean;
  /** 全局代理 ID（0 表示不启用） */
  'proxy.global_id'?: number;
  /** 是否启用 Adobe Firefly 专用代理（出图链路单独出口，绕过区域风控） */
  'proxy.adobe_enabled'?: boolean;
  /** Adobe Firefly 专用代理 ID（0 表示不启用） */
  'proxy.adobe_id'?: number;
  /** Adobe 上游提交通道：clio（Firefly 网页，默认）| psweb（Photoshop Web 入口） */
  'adobe.submit_mode'?: 'clio' | 'psweb';
  /** OAuth access_token 距过期 N 小时内自动刷新 */
  'oauth.refresh_before_hours'?: number;
  /** OpenAI Codex CLI client_id */
  'oauth.openai_client_id'?: string;
  /** OpenAI OAuth Token Endpoint */
  'oauth.openai_token_url'?: string;
  /** 邮箱配置 */
  'mail.default_backend'?: 'outlook_imap' | 'outlook_graph' | 'tempmail' | 'cf';
  'mail.poll_timeout_sec'?: number;
  'mail.max_failures'?: number;
  'mail.outlook'?: MailOutlookSettings;
  'mail.tempmail'?: MailTempmailSettings;
  'mail.cf'?: MailCFSettings;
  /** 验证码服务（号池注册自动过验证码） */
  'captcha.provider'?: 'capsolver' | '2captcha' | 'none';
  'captcha.api_key'?: string;
  'captcha.endpoint'?: string;
  /** Arkose（FunCaptcha，Banana / GPT）专用 */
  'captcha.arkose.provider'?: string;
  'captcha.arkose.api_key'?: string;
  'captcha.arkose.endpoint'?: string;
  /** Arkose 备用服务商列表（主配置失败时按顺序 fallback） */
  'captcha.arkose.fallbacks'?: CaptchaProviderEntry[];
  /** Turnstile（Grok）专用 */
  'captcha.turnstile.provider'?: string;
  'captcha.turnstile.api_key'?: string;
  'captcha.turnstile.endpoint'?: string;
  /** Turnstile 备用服务商列表 */
  'captcha.turnstile.fallbacks'?: CaptchaProviderEntry[];
  /** SMS 接码（GPT 注册触发 /add-phone 时使用） */
  'sms.provider'?: 'herosms';
  'sms.api_url'?: string;
  'sms.api_key'?: string;
  'sms.service'?: string;
  /** 国家 ID，逗号 / 分号 / 空白分隔多个，按顺序尝试。例 "6,25"。 */
  'sms.country'?: string | number;
  'sms.max_price'?: number;
  'sms.max_uses'?: number;
  /** 手机号前缀白名单（E164 不带 +），例 "628389" 表示只接受 +62 838 9... 段；空表示不过滤。 */
  'sms.phone_prefix_allowlist'?: string;
  /** 号池注册任务全局并发数（1-64） */
  'register.worker_concurrency'?: number;
  /** 后台 UI 偏好 - 分页设置 */
  'ui.pagination'?: UIPaginationSettings;
  /** 内容安全 - 违禁词库 */
  'safety.keyword_blocklist.enabled'?: boolean;
  'safety.keyword_blocklist.words'?: string[] | string;
  'safety.keyword_blocklist.match_mode'?: 'contains' | 'exact' | string;
  /** 生成失败时是否自动返还预扣积分 */
  'billing.refund_on_failure'?: boolean;
  /** 新用户注册成功后赠送的初始积分（×100 入库，前端按"点"展示） */
  'billing.free_initial_points'?: number;
  [key: string]: unknown;
}

/** 打码服务商单项（用于 fallback 列表）。endpoint 留空走 provider 默认。 */
export interface CaptchaProviderEntry {
  provider: string;
  api_key: string;
  endpoint?: string;
}

/** 后台 UI 偏好 - 分页相关设置（key=ui.pagination） */
export interface UIPaginationSettings {
  /** 全站列表默认每页条数（用户没在 UI 切换过时使用） */
  default_page_size?: number;
  /** Pager 下拉的候选条数；空数组退回 [10,20,50,100] */
  page_size_options?: number[];
}

export interface MailOutlookSettings {
  mode?: 'imap' | 'graph';
  scope_imap?: string;
  scope_graph?: string;
}

export interface MailTempmailSettings {
  api_base_url?: string;
  new_address_path?: string;
  mails_path?: string;
  address_name?: string;
  address_domains?: string[];
}

export interface MailCFSettings {
  worker_domain?: string;
  email_domain?: string;
  admin_password?: string;
}

// ==================== 共享邮箱池 ====================

export type MailPoolStatus = 'available' | 'in_use' | 'registered' | 'failed' | 'disabled';
export type MailPoolMode = 'outlook_imap' | 'outlook_graph' | 'tempmail' | 'cf';

export interface MailPoolItem {
  id: number;
  email: string;
  client_id: string;
  mode: MailPoolMode | string;
  status: MailPoolStatus | string;
  failure_count: number;
  last_error?: string;
  used_by_provider?: string;
  used_by_account_id?: number;
  imported_at: number;
  used_at?: number;
  registered_at?: number;
}

export interface MailPoolImportBody {
  text: string;
  mode?: MailPoolMode;
  separator?: string;
}

export interface MailPoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface MailPoolCFGenerateBody {
  count: number;
  enable_prefix?: boolean;
  domain?: string;
  name_len?: number;
}

export interface MailPoolCFGenerateResp {
  generated: number;
  skipped: number;
  errors?: string[];
  samples?: string[];
}

export interface MailPoolUpdateBody {
  mode?: MailPoolMode;
  status?: MailPoolStatus;
  password?: string;
  client_id?: string;
  refresh_token?: string;
}

export interface MailPoolStatsResp {
  total: number;
  available: number;
  in_use: number;
  registered: number;
  failed: number;
  disabled: number;
}

export interface MailPoolBulkOpResult {
  affected: number;
}

// ==================== 号池 - ADOBE ====================

export type AdobePoolStatus = 'valid' | 'invalid' | 'disabled' | 'cooldown';
export type AdobePoolSource = 'register' | 'import';

export type AdobeEntitlementState = 'unknown' | 'blocked' | 'ok';

// 该号在各档位上的权益学习状态。
// 由后端 generation_service 在撞到 NotEntitledError 时自动写入 entitlements_json，
// 7 天后自动失效允许重新探测。详见 backend/internal/service/generation_service.go:
// accountSupportsAdobeTier / recordAdobeNotEntitled。
export interface AdobePoolEntitlements {
  image_1k: AdobeEntitlementState;
  image_1k_checked_at?: number;
  image_2k: AdobeEntitlementState;
  image_2k_checked_at?: number;
  image_4k: AdobeEntitlementState;
  image_4k_checked_at?: number;
}

export interface AdobePoolItem {
  id: number;
  email: string;
  display_name?: string;
  adobe_user_id?: string;
  has_password: boolean;
  has_access_token: boolean;
  has_cookie: boolean;
  status: AdobePoolStatus | string;
  source: AdobePoolSource | string;
  credits: number;
  expires_at?: number;
  last_checked_at?: number;
  last_credits_check_at?: number;
  last_refresh_at?: number;
  last_used_at?: number;
  refresh_enabled: 0 | 1 | number;
  failure_count: number;
  error_message?: string;
  cooldown_until?: number;
  notes?: string;
  entitlements?: AdobePoolEntitlements | null;
  created_at: number;
  updated_at: number;
}

export interface AdobePoolCreateBody {
  email: string;
  display_name?: string;
  adobe_user_id?: string;
  password?: string;
  access_token?: string;
  cookie?: string;
  status?: AdobePoolStatus;
  source?: AdobePoolSource;
  credits?: number;
  expires_at?: number;
  notes?: string;
}

export interface AdobePoolUpdateBody {
  display_name?: string;
  adobe_user_id?: string;
  password?: string;
  access_token?: string;
  cookie?: string;
  status?: AdobePoolStatus;
  credits?: number;
  expires_at?: number;
  refresh_enabled?: 0 | 1;
  notes?: string;
}

export interface AdobePoolImportBody {
  text: string;
  source?: AdobePoolSource;
}

export interface AdobePoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface AdobePoolStatsResp {
  total: number;
  valid: number;
  invalid: number;
  disabled: number;
  cooldown: number;
}

export interface AdobePoolBulkOpResult {
  affected: number;
}

// ==================== 号池 - GOOGLE (FlowMusic 歌曲) ====================

export type GooglePoolStatus = 'valid' | 'invalid' | 'disabled' | 'cooldown';

export interface GooglePoolListQuery {
  keyword?: string;
  status?: GooglePoolStatus | '';
  source?: string;
  page?: number;
  page_size?: number;
}

export interface GooglePoolItem {
  id: number;
  email: string;
  display_name?: string;
  has_credential: boolean;
  has_cookie: boolean;
  protocol_mode: string;
  status: GooglePoolStatus | string;
  source: string;
  credits: number;
  tokens_remaining: number;
  subscription_tier?: string;
  expires_at?: number;
  last_checked_at?: number;
  last_refresh_at?: number;
  last_refresh_result?: string;
  last_used_at?: number;
  refresh_enabled: 0 | 1 | number;
  failure_count: number;
  error_message?: string;
  cooldown_until?: number;
  notes?: string;
  created_at: number;
  updated_at: number;
}

export interface GooglePoolCreateBody {
  email?: string;
  display_name?: string;
  cookies?: string;
  access_token?: string;
  refresh_token?: string;
  provider_token?: string;
  provider_refresh_token?: string;
  status?: GooglePoolStatus;
  source?: string;
  credits?: number;
  expires_at?: number;
  notes?: string;
}

export interface GooglePoolUpdateBody {
  display_name?: string;
  cookies?: string;
  access_token?: string;
  refresh_token?: string;
  status?: GooglePoolStatus;
  credits?: number;
  expires_at?: number;
  refresh_enabled?: 0 | 1;
  notes?: string;
}

export interface GooglePoolImportBody {
  text: string;
  source?: string;
}

export interface GooglePoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface GooglePoolStatsResp {
  total: number;
  valid: number;
  invalid: number;
  disabled: number;
  cooldown: number;
}

export interface GooglePoolBulkOpResult {
  affected: number;
}

// ==================== 号池 - 官方 GROK (xAI API) ====================

export type XaiPoolStatus = 'valid' | 'invalid' | 'disabled' | 'cooldown';

export interface XaiPoolListQuery {
  keyword?: string;
  status?: XaiPoolStatus | '';
  account_type?: string;
  page?: number;
  page_size?: number;
}

export interface XaiPoolItem {
  id: number;
  email: string;
  subject?: string;
  has_access_token: boolean;
  has_refresh_token: boolean;
  token_endpoint?: string;
  base_url?: string;
  account_type: string;
  status: XaiPoolStatus | string;
  source: string;
  refresh_enabled: boolean;
  expires_at?: number;
  last_refresh_at?: number;
  last_refresh_result?: string;
  last_used_at?: number;
  failure_count?: number;
  success_count?: number;
  error_message?: string;
  notes?: string;
  balance_note?: string;
  billing?: XaiBilling;
  created_at: number;
  updated_at: number;
}

export interface XaiBilling {
  limit_usd: number;
  used_usd: number;
  remaining_usd: number;
  cap_usd: number;
  used_pct: number;
  period_end: string;
  updated_at: number;
}

export interface XaiBillingResp {
  limit_usd: number;
  used_usd: number;
  remaining_usd: number;
  cap_usd: number;
  period_end: string;
}

export interface XaiPoolCreateBody {
  email?: string;
  subject?: string;
  access_token?: string;
  refresh_token?: string;
  id_token?: string;
  token_endpoint?: string;
  base_url?: string;
  account_type?: string;
  expires_at?: number;
  notes?: string;
}

export interface XaiPoolUpdateBody {
  access_token?: string;
  refresh_token?: string;
  id_token?: string;
  token_endpoint?: string;
  base_url?: string;
  account_type?: string;
  status?: XaiPoolStatus;
  refresh_enabled?: boolean;
  expires_at?: number;
  notes?: string;
  balance_note?: string;
}

export interface XaiPoolImportBody {
  text: string;
}

export interface XaiPoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface XaiPoolStatsResp {
  total: number;
  valid: number;
  invalid: number;
  disabled: number;
  cooldown: number;
}

export interface XaiPoolBulkOpResult {
  affected: number;
}

export interface XaiPoolRefreshResp {
  id: number;
  status: string;
  account_type: string;
  failure_count: number;
  expires_at?: number;
}

// ==================== 号池 - GROK ====================

export type GrokTrialStatus = 'pending' | 'activating' | 'active' | 'failed' | 'expired';

export type GrokAccountType =
  | ''
  | 'free'
  | 'super_grok_lite'
  | 'super_grok'
  | 'super_grok_heavy'
  | 'team'
  | 'unknown';

export type GrokSubscriptionStatus =
  | ''
  | 'active'
  | 'trialing'
  | 'trial_ended'
  | 'past_due'
  | 'canceled'
  | 'inactive';

export interface GrokPoolItem {
  id: number;
  email: string;
  has_password: boolean;
  given_name?: string;
  family_name?: string;
  has_sso: boolean;
  has_sso_rw: boolean;
  user_agent?: string;
  trial_status: GrokTrialStatus | string;
  trial_started_at?: number;
  /** 额度刷新窗口结束时间（来自 /rest/rate-limits 的 windowSizeSeconds），不是订阅到期。 */
  trial_expires_at?: number;
  /** 真实订阅到期时间（来自 /rest/subscriptions）。无信号时为空。
   *  语义依 cancel_at_period_end：false=下次自动续费扣费日；true=真到期失效日。 */
  expires_at?: number;
  /** 用户是否已主动退订 — 区分"自动续费日"还是"真到期日"。 */
  cancel_at_period_end?: boolean;
  /** 订阅周期："monthly" / "yearly" / ""。 */
  billing_interval?: string;
  /** 订阅生命周期："active" / "trialing" / "past_due" / "canceled" / "inactive" / ""。
   *  前端用 trialing → 试用中徽章，past_due → 欠费徽章。 */
  subscription_status?: string;
  /** stripe productId — 精确识别 Lite / SuperGrok / Heavy（比 account_type 更细）。 */
  product_id?: string;
  trial_error?: string;
  account_type: GrokAccountType | string;
  credits: number;
  quota_total: number;
  failure_count?: number;
  last_checked_at?: number;
  payment_url?: string;
  notes?: string;
  registered_at: number;
  created_at: number;
  updated_at: number;
}

export type GrokPoolRefreshScope =
  | 'all'
  | 'abnormal'
  | 'zero_credits'
  | 'expiring'
  | 'unknown_type';

export interface GrokPoolPurgeBody {
  all?: boolean;
  status?: GrokTrialStatus;
  abnormal?: boolean;
  zero_credits?: boolean;
}

export interface GrokPoolBatchRefreshBody {
  scope: GrokPoolRefreshScope;
}

export interface GrokPoolBatchRefreshErrSample {
  message: string;
  count: number;
}

export interface GrokPoolBatchRefreshResult {
  ok: number;
  fail: number;
  total: number;
  errors?: GrokPoolBatchRefreshErrSample[];
}

/**
 * GrokPoolBatchRefreshJob 后台批量刷新任务的快照（轮询用）。
 *
 * 没有 total 字段：后端是流式分批扫表，"全部账号数"在跑完之前无法精确知道，
 * 前端按 scanned 显示已完成数即可。
 */
export interface GrokPoolBatchRefreshJob {
  /** 后端 idle 状态返回 {status:"idle"}，没有 job_id。 */
  job_id?: string;
  status: 'idle' | 'running' | 'completed' | 'cancelled' | 'failed';
  scope?: GrokPoolRefreshScope;
  started_at?: number;
  ended_at?: number;
  elapsed_ms?: number;
  scanned?: number;
  ok?: number;
  fail?: number;
  last_error?: string;
  errors?: GrokPoolBatchRefreshErrSample[];
}

export interface GrokPoolRefreshResult {
  id: number;
  account_type: string;
  credits: number;
  quota_total: number;
  trial_status: string;
  failure_count: number;
  last_checked_at?: number;
}

export interface GrokPoolCreateBody {
  email: string;
  password?: string;
  given_name?: string;
  family_name?: string;
  sso?: string;
  sso_rw?: string;
  user_agent?: string;
  trial_status?: GrokTrialStatus;
  trial_expires_at?: number;
  account_type?: GrokAccountType;
  credits?: number;
  payment_url?: string;
  notes?: string;
}

export interface GrokPoolUpdateBody {
  password?: string;
  given_name?: string;
  family_name?: string;
  sso?: string;
  sso_rw?: string;
  user_agent?: string;
  trial_status?: GrokTrialStatus;
  trial_expires_at?: number;
  account_type?: GrokAccountType;
  credits?: number;
  payment_url?: string;
  notes?: string;
}

export interface GrokPoolImportBody {
  text: string;
}

export interface GrokPoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface GrokPoolStatsResp {
  total: number;
  pending: number;
  activating: number;
  active: number;
  failed: number;
  expired: number;
}

export interface GrokPoolBulkOpResult {
  affected: number;
}

// ==================== 号池 - GPT ====================

export type GptPoolStatus = 'valid' | 'invalid' | 'disabled' | 'cooldown';

// GptPlanType OpenAI 账号订阅类型；wham/usage.plan_type。
//
// 'unknown' = 还没探测过；其它来自 OpenAI 真实回参。
export type GptPlanType =
  | 'free'
  | 'plus'
  | 'pro'
  | 'team'
  | 'enterprise'
  | 'unknown';

export interface GptPoolItem {
  id: number;
  email: string;
  has_password: boolean;
  has_access_token: boolean;
  has_refresh_token: boolean;
  has_id_token?: boolean;
  has_api_key?: boolean;
  oauth_issuer?: string;
  oauth_client_id?: string;
  // 账号画像 + 配额（来自 wham/usage + JWT 解码）
  plan_type?: GptPlanType | string;
  chatgpt_account_id?: string;
  quota_primary_used_percent?: number;
  quota_primary_reset_at?: number;
  quota_secondary_used_percent?: number;
  quota_secondary_reset_at?: number;
  quota_code_review_used_percent?: number;
  last_quota_check_at?: number;
  status: GptPoolStatus | string;
  expires_at?: number;
  last_checked_at?: number;
  last_refresh_at?: number;
  last_used_at?: number;
  failure_count: number;
  error_message?: string;
  notes?: string;
  registered_at: number;
  created_at: number;
  updated_at: number;
}

export interface GptPoolCreateBody {
  email: string;
  password?: string;
  access_token?: string;
  refresh_token?: string;
  id_token?: string;
  api_key?: string;
  oauth_issuer?: string;
  oauth_client_id?: string;
  status?: GptPoolStatus;
  expires_at?: number;
  notes?: string;
}

export interface GptPoolUpdateBody {
  password?: string;
  access_token?: string;
  refresh_token?: string;
  id_token?: string;
  api_key?: string;
  oauth_issuer?: string;
  oauth_client_id?: string;
  status?: GptPoolStatus;
  expires_at?: number;
  notes?: string;
}

// GptPoolDetail 含明文 password / token，仅供编辑弹窗使用。
export interface GptPoolDetail {
  id: number;
  email: string;
  password?: string;
  access_token?: string;
  refresh_token?: string;
  id_token?: string;
  api_key?: string;
  oauth_issuer?: string;
  oauth_client_id?: string;
  // 与列表 item 相同的 plan/quota 字段（只读展示）
  plan_type?: GptPlanType | string;
  chatgpt_account_id?: string;
  quota_primary_used_percent?: number;
  quota_primary_reset_at?: number;
  quota_secondary_used_percent?: number;
  quota_secondary_reset_at?: number;
  quota_code_review_used_percent?: number;
  last_quota_check_at?: number;
  status: GptPoolStatus | string;
  expires_at?: number;
  last_checked_at?: number;
  last_refresh_at?: number;
  last_used_at?: number;
  failure_count: number;
  error_message?: string;
  notes?: string;
  registered_at: number;
  created_at: number;
  updated_at: number;
}

// ---- 批量刷新 / 删除 / 单条刷新的 body / resp ----

export type GptPoolRefreshScope =
  | 'all'
  | 'abnormal'
  | 'expiring'
  | 'quota_stale';

export interface GptPoolBatchRefreshBody {
  scope?: GptPoolRefreshScope;
  only_quota?: boolean;
  max_concurrent?: number;
}

export interface GptPoolBatchRefreshResp {
  total: number;
  ok: number;
  fail: number;
}

export interface GptPoolRefreshAllResp {
  token_ok: number;
  token_fail: number;
  quota_ok: number;
  quota_fail: number;
}

export type GptPoolPurgeScope =
  | 'all'
  | 'invalid'
  | 'token_expired'
  | 'quota_exceeded'
  | 'no_refresh';

export interface GptPoolPurgeBody {
  scope: GptPoolPurgeScope;
}

export interface GptPoolImportBody {
  text: string;
  format?: 'auto' | 'colon' | 'json';
}

export interface GptPoolImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface GptPoolStatsResp {
  total: number;
  valid: number;
  invalid: number;
  disabled: number;
  cooldown: number;
}

export interface GptPoolBulkOpResult {
  affected: number;
}

// ==================== 号池注册任务 ====================

// RegisterTaskProvider 后端 dto/register_task.go 的 provider oneof：
//   - adobe / grok / gpt：传统号池注册
//   - upgrade_plus：GPT 号池开 Plus（GoPay 15 步流），payload 必带 pool_gpt_id
export type RegisterTaskProvider = 'adobe' | 'grok' | 'gpt' | 'upgrade_plus';
export type RegisterTaskStatus = 'pending' | 'running' | 'success' | 'failed' | 'cancelled';

export interface RegisterTaskItem {
  id: number;
  provider: RegisterTaskProvider | string;
  status: RegisterTaskStatus | string;
  step?: string;
  progress: number;
  mail_id?: number;
  email?: string;
  payload?: Record<string, any>;
  result?: Record<string, any>;
  error?: string;
  pool_account_id?: number;
  cancel_requested: boolean;
  created_at: number;
  started_at?: number;
  finished_at?: number;
  updated_at: number;
}

export interface RegisterTaskListReq {
  provider?: RegisterTaskProvider;
  status?: RegisterTaskStatus;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export interface RegisterTaskCreateBody {
  provider: RegisterTaskProvider;
  mail_id?: number;
  count?: number;
  payload?: Record<string, any>;
}

export interface RegisterTaskCreateResp {
  created: number;
  ids: number[];
}

export interface RegisterTaskStatsResp {
  total: number;
  pending: number;
  running: number;
  success: number;
  failed: number;
  cancelled: number;
}

export type RegisterTaskLogLevel = 'info' | 'warn' | 'error';

export interface RegisterTaskLogEntry {
  id: number;
  task_id: number;
  provider?: RegisterTaskProvider | string;
  level: RegisterTaskLogLevel;
  step?: string;
  progress?: number;
  message?: string;
  created_at: number;
}

export interface RegisterTaskLogQuery {
  task_id?: number;
  provider?: RegisterTaskProvider;
  level?: RegisterTaskLogLevel;
  limit?: number;
}

// ==================== Plus 升级资源池（GeeLark / GoPay / 印尼代理） ====================
//
// 该组类型对应后端 backend/internal/dto/{cloud_phone_pool,gopay_wallet,payment_proxy_pool}.go。
// dispatcher 跑 GoPay 15 步开 Plus 时，从这 3 张表抢资源。

// ── 云手机池 ──
export type CloudPhoneStatus = 'online' | 'offline' | 'banned' | 'disabled';

// CloudPhoneItem 一台 GeeLark 云手机 = 一个 SIM = 一个 WhatsApp 号 = 一个 GoPay 钱包
// 手机号 (country_code + phone_number) 直接挂在云手机上，钱包侧通过 cloud_phone_id 关联。
export interface CloudPhoneItem {
  id: string; // GeeLark phone_id
  name: string;
  has_gl_token: boolean;
  adb_addr?: string;
  prefer_api: 0 | 1;
  country_code: string;
  phone_number?: string;
  phone_masked?: string;
  status: CloudPhoneStatus | string;
  last_check_at?: number;
  last_check_ok: 0 | 1 | 2;
  last_error?: string;
  remark?: string;
  created_at: number;
  updated_at: number;
}

export interface CloudPhoneCreateBody {
  id: string;
  name?: string;
  gl_token: string;
  adb_addr?: string;
  prefer_api?: 0 | 1;
  country_code?: string;
  phone_number?: string;
  remark?: string;
}

export interface CloudPhoneUpdateBody {
  name?: string;
  gl_token?: string;
  adb_addr?: string;
  prefer_api?: 0 | 1;
  country_code?: string;
  phone_number?: string;
  status?: CloudPhoneStatus;
  remark?: string;
}

export interface CloudPhoneListReq {
  status?: CloudPhoneStatus | '';
  keyword?: string;
  page?: number;
  page_size?: number;
}

export interface CloudPhoneImportBody {
  text?: string;
  items?: CloudPhoneCreateBody[];
}

export interface CloudPhoneImportResult {
  imported: number;
  updated: number;
  skipped: number;
  errors?: string[];
}

export interface CloudPhoneBulkOpResult {
  affected: number;
}

// ── GoPay 钱包池 ──
export type GopayWalletStatus =
  | 'available'
  | 'leased'
  | 'cooldown'
  | 'banned'
  | 'exhausted'
  | 'disabled';

// GopayWalletItem 钱包独有信息只有 PIN，country_code/phone_number/phone_masked
// 都是后端 join cloud_phone_pool 反查出来的（用于列表展示）。
export interface GopayWalletItem {
  id: number;
  country_code?: string; // 来自 cloud_phone
  phone_number?: string; // 来自 cloud_phone
  phone_masked?: string; // 来自 cloud_phone
  has_pin: boolean;
  cloud_phone_id: string;
  cloud_phone_name?: string;
  status: GopayWalletStatus | string;
  active_plus_count: number;
  total_success: number;
  total_failed: number;
  last_used_at?: number;
  last_error?: string;
  cooldown_until?: number;
  remark?: string;
  created_at: number;
  updated_at: number;
}

export interface GopayWalletCreateBody {
  pin: string;
  cloud_phone_id: string;
  remark?: string;
}

export interface GopayWalletUpdateBody {
  pin?: string;
  cloud_phone_id?: string;
  status?: GopayWalletStatus;
  active_plus_count?: number;
  remark?: string;
}

export interface GopayWalletListReq {
  status?: GopayWalletStatus | '';
  cloud_phone_id?: string;
  keyword?: string;
  page?: number;
  page_size?: number;
  has_available_on?: boolean;
}

export interface GopayWalletSecretResp {
  pin: string;
}

export interface GopayWalletImportBody {
  text?: string;
  items?: GopayWalletCreateBody[];
}

export interface GopayWalletImportResult {
  imported: number;
  skipped: number;
  errors?: string[];
}

export interface GopayWalletBulkOpResult {
  affected: number;
}

// ── 钱包-Plus 绑定 ──
export type GopayBindingStatus = 'active' | 'cancelled' | 'expired' | 'refunded';

export interface GopayBindingItem {
  id: number;
  wallet_id: number;
  gpt_account_id: number;
  cs_id?: string;
  charge_ref?: string;
  amount_idr: number;
  charged_at: number;
  expires_at: number;
  status: GopayBindingStatus | string;
  cancelled_at?: number;
  note?: string;
}

export interface GopayBindingListReq {
  wallet_id?: number;
  gpt_account_id?: number;
  status?: GopayBindingStatus | '';
  page?: number;
  page_size?: number;
}

// ── 印尼支付代理池 ──
export type PaymentProxyStatus = 'active' | 'disabled' | 'banned';
export type PaymentProxyScheme = 'http' | 'https' | 'socks5' | 'socks5h';

export interface PaymentProxyItem {
  id: number;
  name: string;
  scheme: PaymentProxyScheme | string;
  host?: string;
  port: number;
  username?: string;
  has_password: boolean;
  api_url?: string;
  country: string;
  status: PaymentProxyStatus | string;
  total_used: number;
  total_failed: number;
  last_used_at?: number;
  last_check_at?: number;
  last_check_ok: 0 | 1 | 2;
  last_check_ms: number;
  last_error?: string;
  remark?: string;
  created_at: number;
  updated_at: number;
}

export interface PaymentProxyCreateBody {
  name?: string;
  scheme: PaymentProxyScheme;
  host?: string;
  port?: number;
  username?: string;
  password?: string;
  api_url?: string;
  country?: string;
  remark?: string;
}

export interface PaymentProxyUpdateBody {
  name?: string;
  scheme?: PaymentProxyScheme;
  host?: string;
  port?: number;
  username?: string;
  password?: string;
  api_url?: string;
  country?: string;
  status?: PaymentProxyStatus;
  remark?: string;
}

export interface PaymentProxyListReq {
  status?: PaymentProxyStatus | '';
  country?: string;
  keyword?: string;
  page?: number;
  page_size?: number;
}

export interface PaymentProxyImportBody {
  text: string;
  country?: string;
  remark?: string;
}

export interface PaymentProxyImportResult {
  imported: number;
  skipped: number;
}

export interface PaymentProxyTestResp {
  ok: boolean;
  latency_ms: number;
  ip?: string;
  country?: string;
  error?: string;
}

export interface PaymentProxyBulkOpResult {
  affected: number;
}

// ─── 系统公告 ─────────────────────────────────────────────────────────────────
export type AnnouncementLevel = 'info' | 'success' | 'warning' | 'danger';

export interface Announcement {
  id: number;
  title: string;
  content: string;
  level: AnnouncementLevel;
  link_url?: string;
  link_text?: string;
  pinned: boolean;
  enabled: boolean;
  start_at?: number;  // unix ms，null = 永久
  end_at?: number;
  sort_order: number;
  created_at: number;
  updated_at: number;
}

export interface AnnouncementCreateReq {
  title: string;
  content?: string;
  level: AnnouncementLevel;
  link_url?: string;
  link_text?: string;
  pinned?: boolean;
  enabled?: boolean;
  start_at?: number;
  end_at?: number;
  sort_order?: number;
}

export type AnnouncementUpdateReq = Partial<AnnouncementCreateReq>;
