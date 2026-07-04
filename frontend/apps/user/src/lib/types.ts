// 与后端 dto / response 对齐的前端类型。
// 注意：所有 *_points / points / cost_points 字段单位为「点 *100」，展示时使用 fmtPoints 除以 100。

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

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  token_type: string;
  access_expire_in: number;
  refresh_expire_in: number;
}

export interface RegisterResp {
  uid: number;
  uuid: string;
  invite_code: string;
  token: TokenPair;
}

export interface LoginResp {
  uid: number;
  uuid: string;
  token: TokenPair;
}

export interface MeResp {
  uid: number;
  uuid: string;
  username?: string;
  email?: string;
  phone?: string;
  avatar?: string;
  points: number;
  frozen_points: number;
  plan_code: string;
  invite_code: string;
  created_at: number;
}

export interface APIKey {
  id: number;
  name: string;
  prefix: string;
  last4: string;
  mask: string;
  scope: string;
  rpm_limit: number;
  daily_quota: number;
  status: number;
  expire_at?: number;
  last_used_at?: number;
  created_at: number;
}

export interface APIKeyCreated {
  id: number;
  name: string;
  plain: string;
  prefix: string;
  last4: string;
  scope: string;
  created_at: number;
}

export interface APIKeyCreateBody {
  name: string;
  scope?: string;
  rpm_limit?: number;
  daily_quota?: number;
  expire_days?: number;
}

export interface APIKeyStat {
  key_id: number;
  call_total: number;
  call_succeeded: number;
  call_failed: number;
  consumed_points: number;
  refunded_points: number;
  last_called_at?: number;
}

export interface APIKeyStatsResp {
  since: number;
  until: number;
  total: APIKeyStat;
  per_key: APIKeyStat[];
}

export interface APIKeyStatsQuery {
  since?: number;
  until?: number;
}

export interface WalletLog {
  id: number;
  direction: 1 | -1;
  biz_type: string;
  biz_id: string;
  points: number;
  points_before: number;
  points_after: number;
  remark?: string;
  created_at: number;
}

export interface GenerationResult {
  url: string;
  thumb_url?: string;
  width?: number;
  height?: number;
  duration_ms?: number;
  resolution?: string;
  aspect_ratio?: string;
  /** 音乐结果的富信息：title / lyrics / wav_url / sound_prompt 等。 */
  meta?: Record<string, unknown>;
}

/**
 * 任务状态：
 * 0 pending / 1 running / 2 succeeded / 3 failed / 4 refunded / 5 cancelled
 */
export type TaskStatus = 0 | 1 | 2 | 3 | 4 | 5;

export interface GenerationTask {
  task_id: string;
  kind: 'image' | 'video' | 'chat' | 'music';
  status: TaskStatus;
  progress: number;
  model: string;
  prompt?: string;
  cost_points: number;
  error?: string;
  resolution?: string;
  aspect_ratio?: string;
  results?: GenerationResult[];
  created_at: number;
}

export interface CreateImageBody {
  model: string;
  prompt: string;
  neg_prompt?: string;
  mode?: 't2i' | 'i2i';
  count?: number;
  ratio?: string;
  quality?: 'draft' | 'standard' | 'hd';
  ref_assets?: string[];
  params?: Record<string, unknown>;
}

export interface CreateVideoBody {
  model: string;
  prompt: string;
  mode?: 't2v' | 'i2v';
  duration?: number;
  ratio?: string;
  quality?: 'draft' | 'standard' | 'hd';
  ref_assets?: string[];
  params?: Record<string, unknown>;
}

export interface CreateTextBody {
  model?: string;
  prompt: string;
  max_tokens?: number;
  images?: string[];
}

export interface CreateMusicBody {
  model?: string;
  prompt: string;
  params?: Record<string, unknown>;
}

export interface TextGenerationResp {
  id: string;
  model: string;
  content: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
}

export interface PublicModel {
  model_code: string;
  name: string;
  kind: 'text' | 'image' | 'video' | 'music';
  provider: string;
  upstream_model?: string;
  unit_points: number;
  input_unit_points?: number;
  output_unit_points?: number;
  video_pricing_mode?: 'scaled' | 'flat' | 'variant';
  /** 图片分档价：key = "1k" | "2k" | "4k"，value = 点数 ×100。 */
  image_pricing?: Record<string, number>;
  /** 视频分档价：key = "6" | "10" | "20" | "30"，value = 点数 ×100。 */
  video_pricing?: Record<string, number>;
  enabled: boolean;
}

export interface RedeemCDKResp {
  points: number;
  biz: string;
  message: string;
}

// 充值套餐项（用户端可见，已去掉 admin 内部 remark / 支付密钥）。
// amount 单位：元；points / bonus_points 单位：×100 整数（同钱包）。
export interface RechargeProduct {
  id: string;
  name: string;
  amount: number;
  points: number;
  bonus_points: number;
  badge?: string;
  sort_order: number;
}

export interface RechargeContactInfo {
  email?: string;
  notice?: string;
}

export interface RechargeProductsResp {
  products: RechargeProduct[];
  contact: RechargeContactInfo;
  // 后续接通在线支付后改 true；当前永远 false，前端按"显示客服邮箱"渲染。
  online_payment_enabled: boolean;
}

export interface InviteSummary {
  invite_code: string;
  invite_link: string;
  invitee_count: number;
  total_reward_points: number;
  reward_count: number;
  commission_rate_bp: number;
  commission_rate: number;
}

export interface Invitee {
  user_id: number;
  account: string;
  status: number;
  total_recharge: number;
  reward_to_inviter: number;
  bound_at: number;
}

/**
 * Announcement 系统公告（admin 后台维护，用户端首页顶部滚动条展示）。
 * 时间字段单位：unix 秒；0 表示「立即生效 / 永久有效」。
 */
export interface Announcement {
  id: number;
  title: string;
  content: string;
  /** info / success / warning / danger —— 决定 banner 配色 */
  level: 'info' | 'success' | 'warning' | 'danger';
  link_url?: string;
  link_text?: string;
  pinned: boolean;
  enabled: boolean;
  start_at?: number;
  end_at?: number;
  sort_order: number;
  created_at: number;
  updated_at: number;
}
