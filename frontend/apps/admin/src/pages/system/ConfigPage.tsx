import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Activity,
  ArrowRight,
  Cloud,
  CreditCard,
  Crown,
  Database,
  Mail,
  Plus,
  ReceiptText,
  RefreshCw,
  Save,
  ShieldAlert,
  ShieldCheck,
  Smartphone,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';

import { ApiError } from '../../lib/api';
import { clusterApi, proxiesApi, systemApi } from '../../lib/services';
import type {
  CaptchaProviderEntry,
  MailCFSettings,
  MailOutlookSettings,
  MailTempmailSettings,
  ProxyItem,
  SystemSettings,
} from '../../lib/types';
import { toast } from '../../stores/toast';
import { PageHeader, PageShell } from '../../components/layout/PageShell';
import { useConfirm } from '../../components/ConfirmDialog';
import { Settings as SettingsIcon } from 'lucide-react';

/** 单行 fallback 配置（前端用，stable id 让 React key 稳定）。 */
interface CaptchaFallbackRow {
  id: string;
  provider: string;
  api_key: string;
  endpoint: string;
}

/** Arkose 备选服务商下拉值（不含 'local'，那是 Turnstile 专属）。 */
const ARKOSE_PROVIDERS = [
  'anti-captcha',
  'nopecha',
  'yescaptcha',
  '2captcha',
  'capsolver',
] as const;

/** Turnstile 备选服务商下拉值（含 local 本地 Camoufox）。 */
const TURNSTILE_PROVIDERS = [
  'yescaptcha',
  'capsolver',
  '2captcha',
  'anti-captcha',
  'nopecha',
  'local',
] as const;

/** provider → 默认 endpoint，UI 自动填值。 */
const PROVIDER_DEFAULT_ENDPOINT: Record<string, string> = {
  'anti-captcha': 'https://api.anti-captcha.com',
  nopecha: 'https://api.nopecha.com',
  yescaptcha: 'https://api.yescaptcha.com',
  '2captcha': 'https://api.2captcha.com',
  capsolver: 'https://api.capsolver.com',
  local: 'http://127.0.0.1:5072',
};

let fallbackRowSeq = 0;
function makeFallbackRowID(): string {
  fallbackRowSeq += 1;
  return `fb_${Date.now().toString(36)}_${fallbackRowSeq}`;
}

interface FormState {
  // ---- UI 偏好 ----
  ui_default_page_size: number;
  ui_page_size_options: string; // 逗号分隔，例 "10,20,50,100,500,1000"
  retry_max_attempts: number;
  retry_base_delay_ms: number;
  retry_timeout_seconds: number;
  tolerance_circuit_failures: number;
  tolerance_circuit_cooldown_seconds: number;
  safety_keyword_blocklist_enabled: boolean;
  safety_keyword_blocklist_words: string;
  safety_keyword_blocklist_match_mode: 'contains' | 'exact';
  register_worker_concurrency: number;
  proxy_global_enabled: boolean;
  proxy_global_id: number;
  proxy_adobe_enabled: boolean;
  proxy_adobe_id: number;
  adobe_submit_mode: 'clio' | 'psweb';
  oauth_refresh_before_hours: number;
  storage_history_retention_days: number;
  storage_result_retention_days: number;
  storage_result_cache_max_gb: number;
  storage_result_cache_driver: string;
  storage_redirect_ttl_sec: number;
  oss_enabled: boolean;
  oss_provider: string;
  oss_endpoint: string;
  oss_region: string;
  oss_bucket: string;
  oss_access_key_id: string;
  oss_access_key_secret: string;
  oss_public_base_url: string;
  oss_path_prefix: string;
  payment_enabled: boolean;
  payment_provider: string;
  payment_notify_url: string;
  alipay_app_id: string;
  alipay_private_key: string;
  wechat_mch_id: string;
  wechat_api_v3_key: string;
  mail_default_backend: 'outlook_imap' | 'outlook_graph' | 'tempmail' | 'cf';
  mail_poll_timeout_sec: number;
  mail_max_failures: number;
  mail_outlook_mode: 'imap' | 'graph';
  mail_outlook_scope_imap: string;
  mail_outlook_scope_graph: string;
  mail_tempmail_api_base_url: string;
  mail_tempmail_new_address_path: string;
  mail_tempmail_mails_path: string;
  mail_tempmail_address_name: string;
  mail_tempmail_address_domains: string;
  mail_cf_worker_domain: string;
  mail_cf_email_domain: string;
  mail_cf_admin_password: string;
  // Arkose / FunCaptcha — Banana / GPT 用
  // 注意：CapSolver 已于 2024-12 废弃 FunCaptcha，对 Adobe Arkose 不可用。
  // Adobe 公钥解题率 anti-captcha (70-85%) > nopecha (60-80%) > yescaptcha (50-60%) > 2captcha (30-45%)。
  captcha_arkose_provider:
    | 'anti-captcha'
    | 'nopecha'
    | 'yescaptcha'
    | '2captcha'
    | 'capsolver'
    | 'none';
  captcha_arkose_api_key: string;
  captcha_arkose_endpoint: string;
  /**
   * Arkose 备用服务商列表：主配置 (anti-captcha) 失败时按顺序 fallback。
   * 例：[{provider:'nopecha',api_key:'NP-...'}, {provider:'yescaptcha',api_key:'...'}]
   * 留空 → 单家模式（行为不变）。
   */
  captcha_arkose_fallbacks: CaptchaFallbackRow[];
  // Turnstile — Grok 用（额外多一个 local 选项）
  captcha_turnstile_provider:
    | 'capsolver'
    | '2captcha'
    | 'yescaptcha'
    | 'anti-captcha'
    | 'nopecha'
    | 'local'
    | 'none';
  captcha_turnstile_api_key: string;
  captcha_turnstile_endpoint: string;
  /** Turnstile 备用服务商列表 */
  captcha_turnstile_fallbacks: CaptchaFallbackRow[];
  // SMS 接码 — GPT 注册被 OpenAI 强制 /add-phone 时用
  sms_provider: 'herosms';
  sms_api_url: string;
  sms_api_key: string;
  sms_service: string;
  sms_country: string;
  sms_max_price: number;
  sms_max_uses: number;
  sms_phone_prefix_allowlist: string;
  /** Plus 开通成功后在云手机 GoPay 内自动移除 OpenAI 已连接应用 */
  plus_upgrade_auto_gopay_unlink: boolean;
  /** 生成失败时是否自动返还预扣积分 */
  billing_refund_on_failure: boolean;
  /** 新用户注册成功后赠送的初始积分（页面以"点"显示，内部 ×100） */
  billing_free_initial_points: number;
  /** 集群：是否启用 lease/result 跨节点调度 */
  cluster_enabled: boolean;
  /** 集群：每个任务从 agent 抢到后 lease 续期上限秒；超时未上报会被回收 */
  cluster_lease_ttl_sec: number;
  /** 集群：agent 心跳缺失多久判定离线（用于 ResolveDownload / 调度排除） */
  cluster_heartbeat_dead_sec: number;
  /** 集群：下载 ticket 签发 TTL 秒；越短越安全但缓存命中越低 */
  cluster_ticket_ttl_sec: number;
}

const DEFAULT_FORM: FormState = {
  ui_default_page_size: 10,
  ui_page_size_options: '10,20,50,100,200,500,1000',
  retry_max_attempts: 2,
  retry_base_delay_ms: 800,
  retry_timeout_seconds: 300,
  tolerance_circuit_failures: 3,
  tolerance_circuit_cooldown_seconds: 300,
  safety_keyword_blocklist_enabled: false,
  safety_keyword_blocklist_words: '',
  safety_keyword_blocklist_match_mode: 'contains',
  register_worker_concurrency: 5,
  proxy_global_enabled: false,
  proxy_global_id: 0,
  proxy_adobe_enabled: false,
  proxy_adobe_id: 0,
  adobe_submit_mode: 'clio',
  oauth_refresh_before_hours: 6,
  storage_history_retention_days: 180,
  storage_result_retention_days: 30,
  storage_result_cache_max_gb: 0,
  storage_result_cache_driver: 'local',
  storage_redirect_ttl_sec: 86400,
  oss_enabled: false,
  oss_provider: 'aliyun',
  oss_endpoint: '',
  oss_region: '',
  oss_bucket: '',
  oss_access_key_id: '',
  oss_access_key_secret: '',
  oss_public_base_url: '',
  oss_path_prefix: 'uploads/{yyyy}/{mm}/{dd}',
  payment_enabled: false,
  payment_provider: 'alipay',
  payment_notify_url: '',
  alipay_app_id: '',
  alipay_private_key: '',
  wechat_mch_id: '',
  wechat_api_v3_key: '',
  mail_default_backend: 'outlook_graph',
  mail_poll_timeout_sec: 180,
  mail_max_failures: 3,
  mail_outlook_mode: 'graph',
  mail_outlook_scope_imap: 'https://outlook.office.com/IMAP.AccessAsUser.All offline_access',
  mail_outlook_scope_graph: 'https://graph.microsoft.com/Mail.Read offline_access',
  mail_tempmail_api_base_url: '',
  mail_tempmail_new_address_path: '/api/new_address',
  mail_tempmail_mails_path: '/api/mails?limit=10&offset=0',
  mail_tempmail_address_name: '',
  mail_tempmail_address_domains: '',
  mail_cf_worker_domain: '',
  mail_cf_email_domain: '',
  mail_cf_admin_password: '',
  captcha_arkose_provider: 'capsolver',
  captcha_arkose_api_key: '',
  captcha_arkose_endpoint: 'https://api.capsolver.com',
  captcha_arkose_fallbacks: [],
  captcha_turnstile_provider: 'yescaptcha',
  captcha_turnstile_api_key: '',
  captcha_turnstile_endpoint: 'https://api.yescaptcha.com',
  captcha_turnstile_fallbacks: [],
  sms_provider: 'herosms',
  sms_api_url: 'https://hero-sms.com/stubs/handler_api.php',
  sms_api_key: '',
  sms_service: 'dr',
  sms_country: '6',
  sms_max_price: 0.05,
  sms_max_uses: 3,
  sms_phone_prefix_allowlist: '628389',
  plus_upgrade_auto_gopay_unlink: true,
  billing_refund_on_failure: true,
  billing_free_initial_points: 0,
  cluster_enabled: false,
  cluster_lease_ttl_sec: 300,
  cluster_heartbeat_dead_sec: 60,
  cluster_ticket_ttl_sec: 600,
};

const asBool = (v: unknown, fallback = false) => (v == null ? fallback : Boolean(v));
const asNum = (v: unknown, fallback: number) => {
  const n = Number(v);
  return Number.isFinite(n) ? n : fallback;
};
const asStr = (v: unknown, fallback = '') => (typeof v === 'string' ? v : fallback);

function asObj<T>(v: unknown): T | undefined {
  if (v && typeof v === 'object' && !Array.isArray(v)) return v as T;
  return undefined;
}

function joinDomains(v: unknown): string {
  if (Array.isArray(v)) return v.filter((x) => typeof x === 'string').join(',');
  return '';
}

function splitDomains(v: string): string[] {
  return v
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function joinKeywordWords(v: unknown): string {
  if (Array.isArray(v)) {
    return v.filter((x) => typeof x === 'string' && x.trim()).join('\n');
  }
  return typeof v === 'string' ? v : '';
}

function splitKeywordWords(v: string): string[] {
  const seen = new Set<string>();
  return (v || '')
    .split(/[\n\r,，]+/)
    .map((s) => s.trim())
    .filter((s) => {
      if (!s) return false;
      const key = s.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

/** 从 settings 里把 captcha fallback 数组反序列化为表单行。 */
function parseFallbackRows(v: unknown): CaptchaFallbackRow[] {
  if (!Array.isArray(v)) return [];
  return v
    .map((raw) => {
      if (!raw || typeof raw !== 'object') return null;
      const o = raw as Record<string, unknown>;
      const provider = typeof o.provider === 'string' ? o.provider.toLowerCase().trim() : '';
      const apiKey = typeof o.api_key === 'string' ? o.api_key.trim() : '';
      const endpoint = typeof o.endpoint === 'string' ? o.endpoint.trim() : '';
      if (!provider || provider === 'none') return null;
      return { id: makeFallbackRowID(), provider, api_key: apiKey, endpoint };
    })
    .filter((x): x is CaptchaFallbackRow => x !== null);
}

/** 把表单行序列化为后端 JSON 数组；空 / 缺 api_key 项自动剔除。 */
function serializeFallbackRows(rows: CaptchaFallbackRow[]): CaptchaProviderEntry[] {
  return rows
    .map((r) => ({
      provider: r.provider.toLowerCase().trim(),
      api_key: r.api_key.trim(),
      endpoint: r.endpoint.trim(),
    }))
    .filter((r) => r.provider && r.provider !== 'none' && r.api_key !== '');
}

function fromSettings(s: SystemSettings | undefined): FormState {
  if (!s) return DEFAULT_FORM;
  const outlook = asObj<MailOutlookSettings>(s['mail.outlook']) ?? {};
  const tempmail = asObj<MailTempmailSettings>(s['mail.tempmail']) ?? {};
  const cf = asObj<MailCFSettings>(s['mail.cf']) ?? {};
  const backendRaw = asStr(s['mail.default_backend'], 'outlook_graph');
  const backend = (
    ['outlook_imap', 'outlook_graph', 'tempmail', 'cf'] as const
  ).includes(backendRaw as 'outlook_graph')
    ? (backendRaw as FormState['mail_default_backend'])
    : 'outlook_graph';
  const outlookMode = outlook.mode === 'imap' ? 'imap' : 'graph';
  const ui = (s['ui.pagination'] && typeof s['ui.pagination'] === 'object' ? s['ui.pagination'] : {}) as {
    default_page_size?: number;
    page_size_options?: number[];
  };
  return {
    ui_default_page_size: asNum(ui.default_page_size, 10),
    ui_page_size_options: Array.isArray(ui.page_size_options) && ui.page_size_options.length > 0
      ? ui.page_size_options.join(',')
      : '10,20,50,100,200,500,1000',
    retry_max_attempts: asNum(s['retry.max_attempts'], 2),
    retry_base_delay_ms: asNum(s['retry.base_delay_ms'], 800),
    retry_timeout_seconds: asNum(s['retry.timeout_seconds'], 300),
    tolerance_circuit_failures: asNum(s['tolerance.circuit_failures'], 3),
    tolerance_circuit_cooldown_seconds: asNum(s['tolerance.circuit_cooldown_seconds'], 300),
    safety_keyword_blocklist_enabled: asBool(s['safety.keyword_blocklist.enabled'], false),
    safety_keyword_blocklist_words: joinKeywordWords(s['safety.keyword_blocklist.words']),
    safety_keyword_blocklist_match_mode:
      asStr(s['safety.keyword_blocklist.match_mode'], 'contains') === 'exact' ? 'exact' : 'contains',
    register_worker_concurrency: asNum(s['register.worker_concurrency'], 5),
    proxy_global_enabled: asBool(s['proxy.global_enabled']),
    proxy_global_id: asNum(s['proxy.global_id'], 0),
    proxy_adobe_enabled: asBool(s['proxy.adobe_enabled']),
    proxy_adobe_id: asNum(s['proxy.adobe_id'], 0),
    adobe_submit_mode: asStr(s['adobe.submit_mode'], 'clio') === 'psweb' ? 'psweb' : 'clio',
    oauth_refresh_before_hours: asNum(s['oauth.refresh_before_hours'], 6),
    storage_history_retention_days: asNum(s['storage.history_retention_days'], 180),
    storage_result_retention_days: asNum(s['storage.result_retention_days'], 30),
    storage_result_cache_max_gb: asNum(s['storage.result_cache_max_gb'], 0),
    storage_result_cache_driver: asStr(s['storage.result_cache_driver'], 'local'),
    storage_redirect_ttl_sec: asNum(s['storage.redirect_ttl_sec'], 86400),
    oss_enabled: asBool(s['oss.enabled']),
    oss_provider: asStr(s['oss.provider'], 'aliyun'),
    oss_endpoint: asStr(s['oss.endpoint']),
    oss_region: asStr(s['oss.region']),
    oss_bucket: asStr(s['oss.bucket']),
    oss_access_key_id: asStr(s['oss.access_key_id']),
    oss_access_key_secret: asStr(s['oss.access_key_secret']),
    oss_public_base_url: asStr(s['oss.public_base_url']),
    oss_path_prefix: asStr(s['oss.path_prefix'], 'uploads/{yyyy}/{mm}/{dd}'),
    payment_enabled: asBool(s['payment.enabled']),
    payment_provider: asStr(s['payment.provider'], 'alipay'),
    payment_notify_url: asStr(s['payment.notify_url']),
    alipay_app_id: asStr(s['payment.alipay_app_id']),
    alipay_private_key: asStr(s['payment.alipay_private_key']),
    wechat_mch_id: asStr(s['payment.wechat_mch_id']),
    wechat_api_v3_key: asStr(s['payment.wechat_api_v3_key']),
    mail_default_backend: backend,
    mail_poll_timeout_sec: asNum(s['mail.poll_timeout_sec'], 180),
    mail_max_failures: asNum(s['mail.max_failures'], 3),
    mail_outlook_mode: outlookMode,
    mail_outlook_scope_imap: asStr(
      outlook.scope_imap,
      'https://outlook.office.com/IMAP.AccessAsUser.All offline_access',
    ),
    mail_outlook_scope_graph: asStr(
      outlook.scope_graph,
      'https://graph.microsoft.com/Mail.Read offline_access',
    ),
    mail_tempmail_api_base_url: asStr(tempmail.api_base_url),
    mail_tempmail_new_address_path: asStr(tempmail.new_address_path, '/api/new_address'),
    mail_tempmail_mails_path: asStr(tempmail.mails_path, '/api/mails?limit=10&offset=0'),
    mail_tempmail_address_name: asStr(tempmail.address_name),
    mail_tempmail_address_domains: joinDomains(tempmail.address_domains),
    mail_cf_worker_domain: asStr(cf.worker_domain),
    mail_cf_email_domain: asStr(cf.email_domain),
    mail_cf_admin_password: asStr(cf.admin_password),
    captcha_arkose_provider: parseCaptchaProvider(
      asStr(s['captcha.arkose.provider'], asStr(s['captcha.provider'], 'capsolver')),
      false,
    ) as FormState['captcha_arkose_provider'],
    captcha_arkose_api_key: asStr(s['captcha.arkose.api_key'], asStr(s['captcha.api_key'])),
    captcha_arkose_endpoint: asStr(
      s['captcha.arkose.endpoint'],
      asStr(s['captcha.endpoint'], 'https://api.capsolver.com'),
    ),
    captcha_arkose_fallbacks: parseFallbackRows(s['captcha.arkose.fallbacks']),
    captcha_turnstile_provider: parseCaptchaProvider(
      asStr(s['captcha.turnstile.provider'], asStr(s['captcha.provider'], 'yescaptcha')),
      true,
    ),
    captcha_turnstile_api_key: asStr(s['captcha.turnstile.api_key'], asStr(s['captcha.api_key'])),
    captcha_turnstile_endpoint: asStr(
      s['captcha.turnstile.endpoint'],
      asStr(s['captcha.endpoint'], 'https://api.yescaptcha.com'),
    ),
    captcha_turnstile_fallbacks: parseFallbackRows(s['captcha.turnstile.fallbacks']),
    sms_provider: 'herosms',
    sms_api_url: asStr(s['sms.api_url'], 'https://hero-sms.com/stubs/handler_api.php'),
    sms_api_key: asStr(s['sms.api_key']),
    sms_service: asStr(s['sms.service'], 'dr'),
    sms_country: asStr(s['sms.country'], '6'),
    sms_max_price: asNum(s['sms.max_price'], 0.05),
    sms_max_uses: asNum(s['sms.max_uses'], 3),
    sms_phone_prefix_allowlist: asStr(s['sms.phone_prefix_allowlist'], ''),
    plus_upgrade_auto_gopay_unlink: asBool(s['plus_upgrade.auto_gopay_unlink'], true),
    billing_refund_on_failure: asBool(s['billing.refund_on_failure'], true),
    billing_free_initial_points: asNum(s['billing.free_initial_points'], 0) / 100,
    cluster_enabled: asBool(s['cluster.enabled']),
    cluster_lease_ttl_sec: asNum(s['cluster.lease_ttl_sec'], 300),
    cluster_heartbeat_dead_sec: asNum(s['cluster.heartbeat_dead_sec'], 60),
    cluster_ticket_ttl_sec: asNum(s['cluster.ticket_ttl_sec'], 600),
  };
}

// parseCaptchaProvider 把后端字符串归一化到前端枚举。
// allowLocal=true 时 'local' 是合法值（仅 Turnstile 那组接受）。
type AnyCaptchaProvider =
  | FormState['captcha_arkose_provider']
  | FormState['captcha_turnstile_provider'];

function parseCaptchaProvider(raw: string, allowLocal: boolean): AnyCaptchaProvider {
  const v = (raw || '').toLowerCase();
  if (v === 'none') return 'none';
  if (v === 'anti-captcha' || v === 'anticaptcha') return 'anti-captcha';
  if (v === 'nopecha') return 'nopecha';
  if (v === '2captcha' || v === 'twocaptcha') return '2captcha';
  if (v === 'yescaptcha') return 'yescaptcha';
  if (v === 'capsolver') return 'capsolver';
  if (v === 'local' || v === 'camoufox' || v === 'local_camoufox') {
    return allowLocal ? 'local' : 'anti-captcha';
  }
  // 历史默认是 capsolver；2026-05 起 Adobe Arkose 官方废弃，新部署默认 anti-captcha
  return 'anti-captcha';
}

function toPayload(f: FormState): Partial<SystemSettings> {
  // 解析"逗号 / 空白 / 中文逗号"分隔的 page size 候选项
  const sizeOpts = (f.ui_page_size_options || '')
    .split(/[,，\s]+/)
    .map((s) => Number(s.trim()))
    .filter((n) => Number.isFinite(n) && n > 0);
  return {
    'ui.pagination': {
      default_page_size: Math.max(1, Math.min(10000, Number(f.ui_default_page_size) || 10)),
      page_size_options: sizeOpts.length > 0 ? sizeOpts : [10, 20, 50, 100, 200, 500, 1000],
    },
    'retry.max_attempts': Number(f.retry_max_attempts) || 0,
    'retry.base_delay_ms': Number(f.retry_base_delay_ms) || 0,
    'retry.timeout_seconds': Number(f.retry_timeout_seconds) || 0,
    'tolerance.circuit_failures': Number(f.tolerance_circuit_failures) || 0,
    'tolerance.circuit_cooldown_seconds': Number(f.tolerance_circuit_cooldown_seconds) || 0,
    'safety.keyword_blocklist.enabled': f.safety_keyword_blocklist_enabled,
    'safety.keyword_blocklist.words': splitKeywordWords(f.safety_keyword_blocklist_words),
    'safety.keyword_blocklist.match_mode': f.safety_keyword_blocklist_match_mode,
    'register.worker_concurrency': Math.max(1, Math.min(256, Number(f.register_worker_concurrency) || 5)),
    'proxy.global_enabled': f.proxy_global_enabled,
    'proxy.global_id': Number(f.proxy_global_id) || 0,
    'proxy.adobe_enabled': f.proxy_adobe_enabled,
    'proxy.adobe_id': Number(f.proxy_adobe_id) || 0,
    'adobe.submit_mode': f.adobe_submit_mode === 'psweb' ? 'psweb' : 'clio',
    'oauth.refresh_before_hours': Number(f.oauth_refresh_before_hours) || 6,
    'storage.history_retention_days': Number(f.storage_history_retention_days) || 0,
    'storage.result_retention_days': Number(f.storage_result_retention_days) || 0,
    'storage.result_cache_max_gb': Number(f.storage_result_cache_max_gb) || 0,
    'storage.result_cache_driver': f.storage_result_cache_driver,
    'storage.redirect_ttl_sec': Number(f.storage_redirect_ttl_sec) || 0,
    'oss.enabled': f.oss_enabled,
    'oss.provider': f.oss_provider.trim(),
    'oss.endpoint': f.oss_endpoint.trim(),
    'oss.region': f.oss_region.trim(),
    'oss.bucket': f.oss_bucket.trim(),
    'oss.access_key_id': f.oss_access_key_id.trim(),
    'oss.access_key_secret': f.oss_access_key_secret.trim(),
    'oss.public_base_url': f.oss_public_base_url.trim(),
    'oss.path_prefix': f.oss_path_prefix.trim(),
    'payment.enabled': f.payment_enabled,
    'payment.provider': f.payment_provider.trim(),
    'payment.notify_url': f.payment_notify_url.trim(),
    'payment.alipay_app_id': f.alipay_app_id.trim(),
    'payment.alipay_private_key': f.alipay_private_key.trim(),
    'payment.wechat_mch_id': f.wechat_mch_id.trim(),
    'payment.wechat_api_v3_key': f.wechat_api_v3_key.trim(),
    'mail.default_backend': f.mail_default_backend,
    'mail.poll_timeout_sec': Number(f.mail_poll_timeout_sec) || 180,
    'mail.max_failures': Number(f.mail_max_failures) || 3,
    'mail.outlook': {
      mode: f.mail_outlook_mode,
      scope_imap: f.mail_outlook_scope_imap.trim(),
      scope_graph: f.mail_outlook_scope_graph.trim(),
    },
    'mail.tempmail': {
      api_base_url: f.mail_tempmail_api_base_url.trim(),
      new_address_path: f.mail_tempmail_new_address_path.trim(),
      mails_path: f.mail_tempmail_mails_path.trim(),
      address_name: f.mail_tempmail_address_name.trim(),
      address_domains: splitDomains(f.mail_tempmail_address_domains),
    },
    'mail.cf': {
      worker_domain: f.mail_cf_worker_domain.trim(),
      email_domain: f.mail_cf_email_domain.trim(),
      admin_password: f.mail_cf_admin_password,
    },
    // 旧版单组 captcha.* 在 UI 上已隐藏；不再下发，避免覆盖历史值。
    // 后端读取 arkose / turnstile 任意字段缺失时仍会回落到旧值。
    'captcha.arkose.provider': f.captcha_arkose_provider,
    'captcha.arkose.api_key': f.captcha_arkose_api_key.trim(),
    'captcha.arkose.endpoint': f.captcha_arkose_endpoint.trim(),
    'captcha.arkose.fallbacks': serializeFallbackRows(f.captcha_arkose_fallbacks),
    'captcha.turnstile.provider': f.captcha_turnstile_provider,
    'captcha.turnstile.api_key': f.captcha_turnstile_api_key.trim(),
    'captcha.turnstile.endpoint': f.captcha_turnstile_endpoint.trim(),
    'captcha.turnstile.fallbacks': serializeFallbackRows(f.captcha_turnstile_fallbacks),
    'sms.provider': f.sms_provider,
    'sms.api_url': f.sms_api_url.trim(),
    'sms.api_key': f.sms_api_key.trim(),
    'sms.service': f.sms_service.trim() || 'dr',
    'sms.country': (f.sms_country || '').trim(),
    'sms.max_price': Math.max(0, Number(f.sms_max_price) || 0),
    'sms.max_uses': Math.max(1, Math.min(10, Number(f.sms_max_uses) || 3)),
    'sms.phone_prefix_allowlist': (f.sms_phone_prefix_allowlist || '').trim(),
    'plus_upgrade.auto_gopay_unlink': f.plus_upgrade_auto_gopay_unlink,
    'billing.refund_on_failure': f.billing_refund_on_failure,
    'billing.free_initial_points': Math.round((Number(f.billing_free_initial_points) || 0) * 100),
    'cluster.enabled': f.cluster_enabled,
    'cluster.lease_ttl_sec': Math.max(30, Math.min(3600, Number(f.cluster_lease_ttl_sec) || 300)),
    'cluster.heartbeat_dead_sec': Math.max(15, Math.min(900, Number(f.cluster_heartbeat_dead_sec) || 60)),
    'cluster.ticket_ttl_sec': Math.max(30, Math.min(3600, Number(f.cluster_ticket_ttl_sec) || 600)),
  };
}

export default function ConfigPage() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const settings = useQuery({ queryKey: ['admin', 'system', 'settings'], queryFn: () => systemApi.get() });
  const cacheStats = useQuery({ queryKey: ['admin', 'system', 'cache'], queryFn: () => systemApi.cacheStats() });
  const proxies = useQuery({
    queryKey: ['admin', 'proxies', 'options'],
    queryFn: () => proxiesApi.list({ page: 1, page_size: 200, status: 1 }),
  });
  const [form, setForm] = useState<FormState>(DEFAULT_FORM);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (settings.data) {
      setForm(fromSettings(settings.data));
      setDirty(false);
    }
  }, [settings.data]);

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    setDirty(true);
  };

  const save = useMutation({
    mutationFn: () => systemApi.update(toPayload(form)),
    onSuccess: () => {
      toast.success('已保存');
      setDirty(false);
      qc.invalidateQueries({ queryKey: ['admin', 'system'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  const cleanCache = useMutation({
    mutationFn: (body: { days?: number; all?: boolean }) => systemApi.cleanCache(body),
    onSuccess: (r) => {
      toast.success(`已清理 ${formatBytes(r.deleted_bytes)} / ${r.deleted_files} 个缓存文件`);
      qc.invalidateQueries({ queryKey: ['admin', 'system', 'cache'] });
    },
    onError: (e: ApiError | Error) => toast.error(e.message),
  });

  const proxyOptions: ProxyItem[] = proxies.data?.list ?? [];

  return (
    <PageShell>
      <PageHeader
        icon={<SettingsIcon size={16} />}
        title="系统配置"
        right={
          <>
            <button className="btn btn-outline btn-sm" onClick={() => settings.refetch()} disabled={settings.isFetching}>
              <RefreshCw size={14} className={settings.isFetching ? 'animate-spin' : ''} /> 重载
            </button>
            <button className="btn btn-primary btn-sm" onClick={() => save.mutate()} disabled={!dirty || save.isPending}>
              <Save size={14} /> {save.isPending ? '保存中…' : dirty ? '保存' : '已最新'}
            </button>
          </>
        }
      />

      <ClusterStatusBanner />

      {settings.isLoading ? (
        <div className="card p-6 text-center text-text-tertiary">加载中…</div>
      ) : (
        // 多列瀑布流布局：CSS column-fill: balance 会自动把卡片均分到两列里、
        // 让左右两列总高度尽量接近。比之前 grid 行对齐的版本节省大量空白，
        // 也避免「左 1 个超高 + 右 1 个超矮」的尴尬空挡。
        // 每张 Section 自身加了 break-inside-avoid + mb-4，保证不被拆栏。
        <div className="columns-1 gap-4 xl:columns-2">
          <Section
            icon={<ReceiptText size={18} />}
            title="扣费规则"
            desc="生成相关计费策略；从原「扣费规则」菜单合并过来，集中管理减少跳转。"
          >
            <div className="flex items-center justify-between gap-3 rounded-md border border-border bg-surface-2 px-3 py-2.5">
              <div className="min-w-0">
                <div className="text-small font-medium text-text-primary">失败自动退款</div>
                <div className="mt-0.5 text-tiny text-text-tertiary">建议开启；关闭后失败任务不返还已扣积分</div>
              </div>
              <Toggle
                inline
                label=""
                checked={form.billing_refund_on_failure}
                onChange={(v) => set('billing_refund_on_failure', v)}
              />
            </div>
            <Field label={<HeadingLine icon={<Sparkles size={14} />} title="新用户赠送积分（点）" hint="注册成功后自动赠送的初始积分，0 表示不赠送" />}>
              <input
                className="input input-sm tabular-nums"
                type="number"
                min={0}
                value={form.billing_free_initial_points}
                onChange={(e) => set('billing_free_initial_points', Number(e.target.value) || 0)}
              />
              <span className="field-hint">保存时按系统内部精度入库（×100），页面按"点"显示。</span>
            </Field>
          </Section>

          <Section
            icon={<SettingsIcon size={18} />}
            title="界面偏好 - 分页"
            desc="控制后台所有列表页（用户、号池、日志、订单、CDK 等）的默认每页条数；管理员可改候选下拉值。"
          >
            <Field label="默认每页条数">
              <input
                type="number"
                className="input input-sm tabular-nums"
                min={1}
                max={10000}
                value={form.ui_default_page_size}
                onChange={(e) => set('ui_default_page_size', Number(e.target.value) || 10)}
              />
              <span className="field-hint">
                所有列表页首次打开时使用的默认值；用户可在每个页面右下角"每页"下拉里临时切换（保存到本地）。
              </span>
            </Field>
            <Field label="候选每页条数（逗号分隔）">
              <input
                type="text"
                className="input input-sm font-mono"
                value={form.ui_page_size_options}
                placeholder="10,20,50,100,200,500,1000"
                onChange={(e) => set('ui_page_size_options', e.target.value)}
              />
              <span className="field-hint">
                Pager 下拉里出现的候选项；保存后所有列表的"每页"下拉立即生效。例 <code className="font-mono">10,20,100,1000</code>。
              </span>
            </Field>
          </Section>

          <Section icon={<ShieldAlert size={18} />} title="重试与容错" desc="控制生成请求失败后的重试次数、超时和账号熔断策略。">
            <NumberField label="最大重试次数" value={form.retry_max_attempts} min={0} max={10} onChange={(v) => set('retry_max_attempts', v)} />
            <NumberField label="重试基础延迟（毫秒）" value={form.retry_base_delay_ms} min={0} onChange={(v) => set('retry_base_delay_ms', v)} />
            <NumberField label="请求超时（秒）" value={form.retry_timeout_seconds} min={30} onChange={(v) => set('retry_timeout_seconds', v)} />
            <NumberField label="熔断失败次数" value={form.tolerance_circuit_failures} min={1} onChange={(v) => set('tolerance_circuit_failures', v)} />
            <NumberField label="熔断冷却时间（秒）" value={form.tolerance_circuit_cooldown_seconds} min={30} onChange={(v) => set('tolerance_circuit_cooldown_seconds', v)} />
            <Field label="号池注册并发数（1–256）">
              <input
                type="number"
                className="input input-sm tabular-nums"
                min={1}
                max={256}
                value={form.register_worker_concurrency}
                onChange={(e) => set('register_worker_concurrency', Number(e.target.value) || 1)}
              />
              <span className="field-hint">
                调大可加速 BANANA / GROK / GPT 注册任务并行执行；调小则更稳。保存后扩容立即生效，缩容需重启 API 容器。
                <br />
                <strong>经验值</strong>：5–10（单代理 + 单邮箱后端，稳跑）；16–32（多代理供应商 + catch-all 邮箱）；64+（需 ≥200 IP 代理池 + 多邮箱后端 + 高额度 captcha key）。
              </span>
            </Field>
          </Section>

          <Section
            icon={<ShieldCheck size={18} />}
            title="内容安全 - 违禁词库"
            desc="命中词库的新提交会在扣费和创建任务前直接拦截；每行一个词，保存后约 30 秒内热生效。"
          >
            <Toggle
              label="启用违禁词拦截"
              checked={form.safety_keyword_blocklist_enabled}
              onChange={(v) => set('safety_keyword_blocklist_enabled', v)}
            />
            <Field label="匹配模式">
              <select
                className="select select-sm"
                value={form.safety_keyword_blocklist_match_mode}
                onChange={(e) => set('safety_keyword_blocklist_match_mode', e.target.value === 'exact' ? 'exact' : 'contains')}
              >
                <option value="contains">包含匹配（推荐）</option>
                <option value="exact">完整匹配</option>
              </select>
              <span className="field-hint">
                包含匹配会拦截 prompt 中出现的任意词条；大小写不敏感，中文按原文包含判断。
              </span>
            </Field>
            <Field label="违禁词（一行一个）">
              <textarea
                className="textarea min-h-[180px] font-mono text-small"
                placeholder={'bad keyword\n违禁词'}
                value={form.safety_keyword_blocklist_words}
                onChange={(e) => set('safety_keyword_blocklist_words', e.target.value)}
              />
              <span className="field-hint">
                命中后统一返回：<code>Your keyword is unsafe. Please update or change your keyword.</code>
              </span>
            </Field>
          </Section>

          <Section icon={<Database size={18} />} title="刷新与存储" desc="控制 OAuth 刷新窗口、全局代理和生成历史保留周期。">
            <Toggle label="启用全局代理" checked={form.proxy_global_enabled} onChange={(v) => set('proxy_global_enabled', v)} />
            <Field label="全局默认代理">
              <select className="select select-sm" value={form.proxy_global_id} onChange={(e) => set('proxy_global_id', Number(e.target.value) || 0)} disabled={!form.proxy_global_enabled}>
                <option value={0}>不指定</option>
                {proxyOptions.map((p) => <option key={p.id} value={p.id}>[{p.protocol}] {p.name} - {p.host}:{p.port}</option>)}
              </select>
            </Field>
            <Toggle label="启用 Adobe 专用代理" checked={form.proxy_adobe_enabled} onChange={(v) => set('proxy_adobe_enabled', v)} />
            <Field label="Adobe 专用代理">
              <select className="select select-sm" value={form.proxy_adobe_id} onChange={(e) => set('proxy_adobe_id', Number(e.target.value) || 0)} disabled={!form.proxy_adobe_enabled}>
                <option value={0}>不指定</option>
                {proxyOptions.map((p) => <option key={p.id} value={p.id}>[{p.protocol}] {p.name} - {p.host}:{p.port}</option>)}
              </select>
              <span className="ml-2 text-tiny text-text-tertiary">
                启用后 Adobe Firefly 出图链路单独走该代理（绕过 451 区域限制），其它 provider 仍按全局 / 账号绑定走。
              </span>
            </Field>
            <Field label="Adobe 提交通道">
              <select
                className="select select-sm"
                value={form.adobe_submit_mode}
                onChange={(e) => set('adobe_submit_mode', e.target.value === 'psweb' ? 'psweb' : 'clio')}
              >
                <option value="clio">Firefly 网页（clio-playground-web，默认）</option>
                <option value="psweb">Photoshop Web（PSWebApp1，用 cookie 现铸 token）</option>
              </select>
              <span className="field-hint">
                切到 <code className="font-mono">Photoshop Web</code> 后，所有 Adobe 生成会用账号 cookie 现铸 PSWebApp1 token、
                以 Photoshop Web 身份提交（x-api-key=PSWebApp1，去掉 x-nonce / x-arp-session-id），
                用于规避合作模型（nano-banana / gpt-image / flux）在 Firefly 网页入口的配额/授权报错。
                <br />
                需要账号有 cookie；铸造失败会自动回退到 Firefly 网页通道。保存后约 30 秒热生效。
              </span>
            </Field>
            <NumberField label="OAuth 提前刷新窗口（小时）" value={form.oauth_refresh_before_hours} min={1} max={48} onChange={(v) => set('oauth_refresh_before_hours', v)} />
            <NumberField label="生成历史保留（天）" value={form.storage_history_retention_days} min={0} onChange={(v) => set('storage_history_retention_days', v)} />
            <NumberField label="生成结果文件保留（天）" value={form.storage_result_retention_days} min={0} onChange={(v) => set('storage_result_retention_days', v)} />
            <NumberField label="本地缓存上限（GB）" value={form.storage_result_cache_max_gb} min={0} onChange={(v) => set('storage_result_cache_max_gb', v)} />
            <Field label="生成结果缓存位置">
              <select className="select select-sm" value={form.storage_result_cache_driver} onChange={(e) => set('storage_result_cache_driver', e.target.value)}>
                <option value="local">本地缓存</option>
                <option value="oss">OSS 存储</option>
                <option value="proxy">流式代理（不落地，服务器转发，隐藏真实地址）</option>
                <option value="redirect">重定向直链（不落地，302 跳上游，抓包可见地址）</option>
                <option value="off">不缓存</option>
              </select>
            </Field>
            {(form.storage_result_cache_driver === 'redirect' || form.storage_result_cache_driver === 'proxy') && (
              <NumberField
                label="直链有效期（秒）"
                value={form.storage_redirect_ttl_sec}
                min={0}
                onChange={(v) => set('storage_redirect_ttl_sec', v)}
              />
            )}
          </Section>

          <Section icon={<Trash2 size={18} />} title="缓存清理" desc="查看并清理本地生成结果缓存，清理后旧作品可能无法继续预览原文件。">
            <div className="grid gap-3 md:grid-cols-3">
              <div className="rounded-md border border-border bg-surface-2 p-3">
                <div className="text-small text-text-tertiary">缓存大小</div>
                <div className="mt-1 text-h4 text-text-primary">{formatBytes(cacheStats.data?.bytes ?? 0)}</div>
              </div>
              <div className="rounded-md border border-border bg-surface-2 p-3">
                <div className="text-small text-text-tertiary">文件数量</div>
                <div className="mt-1 text-h4 text-text-primary">{cacheStats.data?.files ?? 0}</div>
              </div>
              <div className="rounded-md border border-border bg-surface-2 p-3">
                <div className="text-small text-text-tertiary">缓存目录</div>
                <div className="mt-1 truncate text-small text-text-secondary" title={cacheStats.data?.root}>{cacheStats.data?.root || '-'}</div>
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              <button className="btn btn-outline btn-sm" disabled={cleanCache.isPending} onClick={() => cacheStats.refetch()}>
                <RefreshCw size={14} className={cacheStats.isFetching ? 'animate-spin' : ''} /> 刷新占用
              </button>
              <button className="btn btn-outline btn-sm" disabled={cleanCache.isPending} onClick={() => cleanCache.mutate({ days: 7 })}>
                清理 7 天前
              </button>
              <button className="btn btn-outline btn-sm" disabled={cleanCache.isPending} onClick={() => cleanCache.mutate({ days: 3 })}>
                清理 3 天前
              </button>
              <button
                className="btn btn-danger btn-sm"
                disabled={cleanCache.isPending}
                onClick={async () => {
                  const ok = await confirm({
                    title: '清空全部生成缓存',
                    description: '该操作会删除所有用户的图片 / 视频生成产物。已上传到 OSS 的旧作品仍能播放原 URL，但本地缓存的预览文件将丢失。确认继续？',
                    tone: 'danger',
                    confirmLabel: '清空',
                  });
                  if (ok) cleanCache.mutate({ all: true });
                }}
              >
                <Trash2 size={14} /> 清空全部缓存
              </button>
            </div>
          </Section>

          <Section
            icon={<Database size={18} />}
            title="集群调度"
            desc="多节点开关与心跳 / lease / 下载 ticket 的时间窗。改完点底部 保存配置 即可热生效，无需重启。"
          >
            <Toggle
              label="启用集群调度"
              checked={form.cluster_enabled}
              onChange={(v) => set('cluster_enabled', v)}
            />
            <div className="rounded-md border border-border bg-surface-2 p-3 text-tiny text-text-tertiary">
              <strong className="text-text-secondary">开启后</strong>：新生成任务交给在线 agent 抢锁执行（按 provider 范围匹配）；
              <strong className="text-text-secondary">关闭</strong>：所有任务全部回到主控本地 goroutine 跑，老兼容模式。
              <br />
              注意：未注册任何 agent 时也安全 —— 没有合格 agent 自动回退到本地。
            </div>
            <div className="grid gap-3 md:grid-cols-3">
              <NumberField
                label="Lease TTL（秒）"
                value={form.cluster_lease_ttl_sec}
                min={30}
                max={3600}
                onChange={(v) => set('cluster_lease_ttl_sec', v)}
              />
              <NumberField
                label="心跳死机阈值（秒）"
                value={form.cluster_heartbeat_dead_sec}
                min={15}
                max={900}
                onChange={(v) => set('cluster_heartbeat_dead_sec', v)}
              />
              <NumberField
                label="下载 Ticket TTL（秒）"
                value={form.cluster_ticket_ttl_sec}
                min={30}
                max={3600}
                onChange={(v) => set('cluster_ticket_ttl_sec', v)}
              />
            </div>
            <div className="text-tiny text-text-tertiary">
              Lease 过期未上报的任务会在 30s 内被主控自动回收并重新分发；心跳超过死机阈值的节点会从下载路由和 lease 候选中暂时移除，节点恢复后自动复原。
            </div>
          </Section>

          <Section icon={<Cloud size={18} />} title="OSS 存储" desc="配置图片、视频和用户上传素材的对象存储位置。">
            <Toggle label="启用 OSS 存储" checked={form.oss_enabled} onChange={(v) => set('oss_enabled', v)} />
            <div className="grid gap-3 md:grid-cols-2">
              <TextField label="服务商" value={form.oss_provider} onChange={(v) => set('oss_provider', v)} placeholder="aliyun / s3 / cos" />
              <TextField label="Region" value={form.oss_region} onChange={(v) => set('oss_region', v)} />
              <TextField label="Endpoint" value={form.oss_endpoint} onChange={(v) => set('oss_endpoint', v)} />
              <TextField label="Bucket" value={form.oss_bucket} onChange={(v) => set('oss_bucket', v)} />
              <TextField label="AccessKey ID" value={form.oss_access_key_id} onChange={(v) => set('oss_access_key_id', v)} />
              <TextField label="AccessKey Secret" value={form.oss_access_key_secret} onChange={(v) => set('oss_access_key_secret', v)} type="password" />
            </div>
            <TextField label="公开访问域名" value={form.oss_public_base_url} onChange={(v) => set('oss_public_base_url', v)} placeholder="https://cdn.example.com" />
            <TextField label="存储路径前缀" value={form.oss_path_prefix} onChange={(v) => set('oss_path_prefix', v)} />
          </Section>

          <Section
            icon={<Mail size={18} />}
            title="邮箱配置"
            desc="号池注册时用于接收验证码的收件源；支持 Outlook OAuth、临时邮箱 API 和 Cloudflare Worker。"
          >
            <Field label="默认收件后端">
              <select
                className="select select-sm"
                value={form.mail_default_backend}
                onChange={(e) => set('mail_default_backend', e.target.value as FormState['mail_default_backend'])}
              >
                <option value="outlook_graph">Outlook Graph（推荐）</option>
                <option value="outlook_imap">Outlook IMAP</option>
                <option value="tempmail">临时邮箱 API</option>
                <option value="cf">CF Worker</option>
              </select>
            </Field>
            <div className="grid gap-3 md:grid-cols-2">
              <NumberField
                label="邮件等待超时（秒）"
                value={form.mail_poll_timeout_sec}
                min={30}
                max={1800}
                onChange={(v) => set('mail_poll_timeout_sec', v)}
              />
              <NumberField
                label="单封最大失败次数"
                value={form.mail_max_failures}
                min={1}
                max={100}
                onChange={(v) => set('mail_max_failures', v)}
              />
            </div>

            <MailBackendCard
              show={form.mail_default_backend === 'outlook_graph' || form.mail_default_backend === 'outlook_imap'}
              title="Outlook 收件"
              tag="active"
              hint="Outlook 邮箱及 client_id / refresh_token 在「邮箱池」批量导入；这里只保存全局 OAuth scope。"
            >
              <div className="grid gap-3 md:grid-cols-2">
                <Field label="模式">
                  <select
                    className="select select-sm"
                    value={form.mail_outlook_mode}
                    onChange={(e) => set('mail_outlook_mode', e.target.value as 'imap' | 'graph')}
                  >
                    <option value="graph">Graph API</option>
                    <option value="imap">IMAP</option>
                  </select>
                </Field>
                <TextField
                  label="IMAP scope"
                  value={form.mail_outlook_scope_imap}
                  onChange={(v) => set('mail_outlook_scope_imap', v)}
                />
                <TextField
                  label="Graph scope"
                  value={form.mail_outlook_scope_graph}
                  onChange={(v) => set('mail_outlook_scope_graph', v)}
                />
              </div>
            </MailBackendCard>

            <MailBackendCard
              show={form.mail_default_backend === 'tempmail'}
              title="临时邮箱 API（tempmail）"
              tag="active"
            >
              <div className="grid gap-3 md:grid-cols-2">
                <TextField
                  label="API Base URL"
                  value={form.mail_tempmail_api_base_url}
                  onChange={(v) => set('mail_tempmail_api_base_url', v)}
                  placeholder="https://tempmail.example.com"
                />
                <TextField
                  label="申请地址路径"
                  value={form.mail_tempmail_new_address_path}
                  onChange={(v) => set('mail_tempmail_new_address_path', v)}
                  placeholder="/api/new_address"
                />
                <TextField
                  label="收件路径"
                  value={form.mail_tempmail_mails_path}
                  onChange={(v) => set('mail_tempmail_mails_path', v)}
                  placeholder="/api/mails?limit=10&offset=0"
                />
                <TextField
                  label="自定义前缀"
                  value={form.mail_tempmail_address_name}
                  onChange={(v) => set('mail_tempmail_address_name', v)}
                  placeholder="留空使用随机名"
                />
              </div>
              <Field label="可用域名">
                <input
                  className="input input-sm"
                  value={form.mail_tempmail_address_domains}
                  placeholder="example.com, abc.dev, mail.io"
                  onChange={(e) => set('mail_tempmail_address_domains', e.target.value)}
                />
                <span className="mt-1 text-tiny text-text-tertiary">
                  支持多个域名，用 <code className="font-mono">逗号 / 空格</code> 分隔；注册时会随机轮选其中一个域名拼接邮箱。
                </span>
              </Field>
            </MailBackendCard>

            <MailBackendCard
              show={form.mail_default_backend === 'cf'}
              title="Cloudflare Worker 邮箱"
              tag="active"
            >
              <div className="grid gap-3 md:grid-cols-2">
                <TextField
                  label="Worker 域名"
                  value={form.mail_cf_worker_domain}
                  onChange={(v) => set('mail_cf_worker_domain', v)}
                  placeholder="https://mail.example.com"
                />
                <TextField
                  label="管理密码"
                  value={form.mail_cf_admin_password}
                  onChange={(v) => set('mail_cf_admin_password', v)}
                  type="password"
                />
              </div>
              <Field label="邮箱域名">
                <input
                  className="input input-sm"
                  value={form.mail_cf_email_domain}
                  placeholder="qq.qkmss.com, mail.qkmss.com, mail.jzqkwl.com, qq.jzqkwl.com"
                  onChange={(e) => set('mail_cf_email_domain', e.target.value)}
                />
                <span className="mt-1 block text-tiny text-text-tertiary">
                  支持多个域名，用 <code className="font-mono">逗号 / 空格 / 换行</code> 分隔；须与 Worker 侧允许的收件域名<strong>逐字一致</strong>（含子域），DNS catch-all 已指向该 Worker。                  错配或 DNS 未生效时 <code className="font-mono">POST /admin/new_address</code> 常返回 400「Failed to create address」。留空则由 Worker 使用其默认域。
                </span>
              </Field>
            </MailBackendCard>

            <details className="group rounded-md border border-dashed border-border bg-surface-1 p-3">
              <summary className="cursor-pointer text-small text-text-secondary marker:text-text-tertiary">
                展开未启用的备用后端配置（保存后可随时切换默认）
              </summary>
              <div className="mt-3 space-y-3">
                <MailBackendCard
                  show={form.mail_default_backend !== 'outlook_graph' && form.mail_default_backend !== 'outlook_imap'}
                  title="Outlook 收件（备用）"
                  tag="idle"
                >
                  <div className="grid gap-3 md:grid-cols-2">
                    <Field label="模式">
                      <select
                        className="select select-sm"
                        value={form.mail_outlook_mode}
                        onChange={(e) => set('mail_outlook_mode', e.target.value as 'imap' | 'graph')}
                      >
                        <option value="graph">Graph API</option>
                        <option value="imap">IMAP</option>
                      </select>
                    </Field>
                    <TextField label="IMAP scope" value={form.mail_outlook_scope_imap} onChange={(v) => set('mail_outlook_scope_imap', v)} />
                    <TextField label="Graph scope" value={form.mail_outlook_scope_graph} onChange={(v) => set('mail_outlook_scope_graph', v)} />
                  </div>
                </MailBackendCard>

                <MailBackendCard show={form.mail_default_backend !== 'tempmail'} title="临时邮箱 API（备用）" tag="idle">
                  <div className="grid gap-3 md:grid-cols-2">
                    <TextField label="API Base URL" value={form.mail_tempmail_api_base_url} onChange={(v) => set('mail_tempmail_api_base_url', v)} />
                    <TextField label="申请地址路径" value={form.mail_tempmail_new_address_path} onChange={(v) => set('mail_tempmail_new_address_path', v)} />
                    <TextField label="收件路径" value={form.mail_tempmail_mails_path} onChange={(v) => set('mail_tempmail_mails_path', v)} />
                    <TextField label="自定义前缀" value={form.mail_tempmail_address_name} onChange={(v) => set('mail_tempmail_address_name', v)} />
                  </div>
                  <Field label="可用域名">
                    <input
                      className="input input-sm"
                      value={form.mail_tempmail_address_domains}
                      placeholder="example.com, abc.dev"
                      onChange={(e) => set('mail_tempmail_address_domains', e.target.value)}
                    />
                  </Field>
                </MailBackendCard>

                <MailBackendCard show={form.mail_default_backend !== 'cf'} title="Cloudflare Worker（备用）" tag="idle">
                  <div className="grid gap-3 md:grid-cols-2">
                    <TextField label="Worker 域名" value={form.mail_cf_worker_domain} onChange={(v) => set('mail_cf_worker_domain', v)} />
                    <TextField label="管理密码" value={form.mail_cf_admin_password} onChange={(v) => set('mail_cf_admin_password', v)} type="password" />
                  </div>
                  <Field label="邮箱域名（多个用逗号分隔）">
                    <input
                      className="input input-sm"
                      value={form.mail_cf_email_domain}
                      placeholder="qq.qkmss.com, mail.qkmss.com"
                      onChange={(e) => set('mail_cf_email_domain', e.target.value)}
                    />
                    <span className="mt-1 block text-tiny text-text-tertiary">
                      同上：域名须与 Worker 配置一致；错配易导致 400「Failed to create address」。留空走 Worker 默认域。
                    </span>
                  </Field>
                </MailBackendCard>
              </div>
            </details>
          </Section>

          <Section
            icon={<ShieldCheck size={18} />}
            title="验证码服务 — Arkose（Banana / GPT）"
            desc="Banana 与 GPT 注册的 FunCaptcha 求解。Adobe 公钥实测解题率：Anti-Captcha 70-85%（首推）> NopeCHA 60-80% > YesCaptcha 50-60% > 2Captcha 30-45%。CapSolver 已废弃 FunCaptcha 不可用。本地 Camoufox 不支持 Arkose。"
          >
            <Field label="服务商">
              <select
                className="select select-sm"
                value={form.captcha_arkose_provider}
                onChange={(e) => {
                  const v = e.target.value as FormState['captcha_arkose_provider'];
                  set('captcha_arkose_provider', v);
                  const ep = form.captcha_arkose_endpoint.trim();
                  const loose =
                    ep === '' ||
                    /capsolver\.com|2captcha\.com|yescaptcha\.com|anti-captcha\.com|nopecha\.com/i.test(
                      ep,
                    );
                  if (!loose) return;
                  if (v === 'anti-captcha') set('captcha_arkose_endpoint', 'https://api.anti-captcha.com');
                  else if (v === 'nopecha') set('captcha_arkose_endpoint', 'https://api.nopecha.com');
                  else if (v === 'yescaptcha') set('captcha_arkose_endpoint', 'https://api.yescaptcha.com');
                  else if (v === '2captcha') set('captcha_arkose_endpoint', 'https://api.2captcha.com');
                  else if (v === 'capsolver') set('captcha_arkose_endpoint', 'https://api.capsolver.com');
                }}
              >
                <option value="anti-captcha">Anti-Captcha（推荐）</option>
                <option value="nopecha">NopeCHA</option>
                <option value="yescaptcha">YesCaptcha</option>
                <option value="2captcha">2Captcha（弱）</option>
                <option value="capsolver">CapSolver（已废弃，不可用）</option>
                <option value="none">不启用</option>
              </select>
            </Field>
            <div className="grid gap-3 md:grid-cols-2">
              <TextField
                label="API Key"
                value={form.captcha_arkose_api_key}
                onChange={(v) => set('captcha_arkose_api_key', v)}
                type="password"
                placeholder="CAP-... / clientKey"
              />
              <TextField
                label="API Endpoint"
                value={form.captcha_arkose_endpoint}
                onChange={(v) => set('captcha_arkose_endpoint', v)}
                placeholder={
                  form.captcha_arkose_provider === 'anti-captcha'
                    ? 'https://api.anti-captcha.com'
                    : form.captcha_arkose_provider === 'nopecha'
                      ? 'https://api.nopecha.com'
                      : form.captcha_arkose_provider === 'yescaptcha'
                        ? 'https://api.yescaptcha.com'
                        : form.captcha_arkose_provider === '2captcha'
                          ? 'https://api.2captcha.com'
                          : 'https://api.capsolver.com'
                }
              />
            </div>
            <p className="text-tiny text-text-tertiary">
              Anti-Captcha / NopeCHA / 2Captcha / YesCaptcha / CapSolver 走同一套 anti-captcha v2 API（clientKey + task），后端按 provider 自动拼 task.type。
              {form.captcha_arkose_provider === 'capsolver' && (
                <>
                  {' '}
                  <strong className="text-warning-600">⚠ CapSolver 自 2024-12 起废弃 FunCaptcha 支持，对 Adobe / GPT 注册不可用，请改选 Anti-Captcha。</strong>
                </>
              )}
            </p>
            <CaptchaFallbackEditor
              label="备用服务商（主家失败时按顺序 fail-over，提升整体解题率）"
              hint={
                <>
                  Adobe 单家解题率 anti-captcha ~70-85%；加 1 个备用 → ~91%，加 2 个 → ~97%。
                  单家超时 / UNSOLVABLE 时立刻切到下家，<strong>不消耗邮箱 / 代理 / blob</strong>；
                  per-attempt 超时按链长度自适应（1 家 60s、2 家 45s/家、3 家 35s/家）。
                  留空 → 行为完全等同旧版单家模式。
                </>
              }
              providers={ARKOSE_PROVIDERS}
              rows={form.captcha_arkose_fallbacks}
              onChange={(rows) => set('captcha_arkose_fallbacks', rows)}
            />
          </Section>

          <Section
            icon={<ShieldCheck size={18} />}
            title="验证码服务 — Turnstile（Grok）"
            desc="Grok 注册的 Cloudflare Turnstile 求解。商用 API 之外，可指向自建 Camoufox solver（python api_solver.py --browser_type camoufox --thread 5）。"
          >
            <Field label="服务商">
              <select
                className="select select-sm"
                value={form.captcha_turnstile_provider}
                onChange={(e) => {
                  const v = e.target.value as FormState['captcha_turnstile_provider'];
                  set('captcha_turnstile_provider', v);
                  const ep = form.captcha_turnstile_endpoint.trim();
                  const loose =
                    ep === '' ||
                    /capsolver\.com|2captcha\.com|yescaptcha\.com|127\.0\.0\.1:5072/i.test(ep);
                  if (!loose) return;
                  if (v === 'capsolver') set('captcha_turnstile_endpoint', 'https://api.capsolver.com');
                  else if (v === '2captcha') set('captcha_turnstile_endpoint', 'https://api.2captcha.com');
                  else if (v === 'yescaptcha') set('captcha_turnstile_endpoint', 'https://api.yescaptcha.com');
                  else if (v === 'local') set('captcha_turnstile_endpoint', 'http://127.0.0.1:5072');
                }}
              >
                <option value="yescaptcha">YesCaptcha</option>
                <option value="capsolver">CapSolver</option>
                <option value="2captcha">2Captcha</option>
                <option value="local">本地 Camoufox</option>
                <option value="none">不启用</option>
              </select>
            </Field>
            <div className="grid gap-3 md:grid-cols-2">
              <TextField
                label={form.captcha_turnstile_provider === 'local' ? 'API Key（本地 solver 可留空）' : 'API Key'}
                value={form.captcha_turnstile_api_key}
                onChange={(v) => set('captcha_turnstile_api_key', v)}
                type="password"
                placeholder={form.captcha_turnstile_provider === 'local' ? '—' : 'CAP-... / clientKey'}
              />
              <TextField
                label={form.captcha_turnstile_provider === 'local' ? 'Solver URL' : 'API Endpoint'}
                value={form.captcha_turnstile_endpoint}
                onChange={(v) => set('captcha_turnstile_endpoint', v)}
                placeholder={
                  form.captcha_turnstile_provider === '2captcha'
                    ? 'https://api.2captcha.com'
                    : form.captcha_turnstile_provider === 'capsolver'
                      ? 'https://api.capsolver.com'
                      : form.captcha_turnstile_provider === 'local'
                        ? 'http://127.0.0.1:5072'
                        : 'https://api.yescaptcha.com'
                }
              />
            </div>
            <p className="text-tiny text-text-tertiary">
              本地 Camoufox solver 暴露 <code>/turnstile</code> + <code>/result</code>，HTTP 直连不经代理，仅支持 Turnstile。
            </p>
            <CaptchaFallbackEditor
              label="备用服务商（主家失败时按顺序 fail-over）"
              hint={
                <>
                  Grok 注册时 Cloudflare Turnstile 单家成功率较稳，备用主要用于"队列堵 / API key 限频"兜底。
                  本地 Camoufox 可作为 fallback，但记得起服务（<code>python api_solver.py --browser_type camoufox --thread 5</code>）。
                </>
              }
              providers={TURNSTILE_PROVIDERS}
              rows={form.captcha_turnstile_fallbacks}
              onChange={(rows) => set('captcha_turnstile_fallbacks', rows)}
            />
          </Section>

          <Section
            icon={<Smartphone size={18} />}
            title="接码服务 — SMS（GPT 注册）"
            desc="OpenAI 在 Codex 授权阶段强制 /add-phone 时，从 hero-sms.com 取号、轮询 OTP 并自动提交。同一号码默认可复用 3 次。"
          >
            <Field label="服务商">
              <select
                className="select select-sm"
                value={form.sms_provider}
                onChange={(e) => set('sms_provider', e.target.value as FormState['sms_provider'])}
              >
                <option value="herosms">Hero-SMS（hero-sms.com）</option>
              </select>
            </Field>
            <div className="grid gap-3 md:grid-cols-2">
              <TextField
                label="API Endpoint"
                value={form.sms_api_url}
                onChange={(v) => set('sms_api_url', v)}
                placeholder="https://hero-sms.com/stubs/handler_api.php"
              />
              <TextField
                label="API Key"
                value={form.sms_api_key}
                onChange={(v) => set('sms_api_key', v)}
                type="password"
                placeholder="hero-sms 控制台分发的 32 位 key"
              />
            </div>
            <div className="grid gap-3 md:grid-cols-3">
              <TextField
                label="服务代号 (service)"
                value={form.sms_service}
                onChange={(v) => set('sms_service', v)}
                placeholder="dr = OpenAI / ChatGPT"
              />
              <TextField
                label="国家代号 (country)"
                value={form.sms_country}
                onChange={(v) => set('sms_country', v)}
                placeholder="6 或 6,25,73（按顺序尝试）"
              />
              <Field label="单号最大价格（USD）">
                <input
                  className="input input-sm tabular-nums"
                  type="number"
                  step="0.001"
                  min={0}
                  value={form.sms_max_price}
                  onChange={(e) => set('sms_max_price', Number(e.target.value) || 0)}
                />
              </Field>
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              <NumberField
                label="单号复用次数（OpenAI 上限 3）"
                value={form.sms_max_uses}
                min={1}
                max={10}
                onChange={(v) => set('sms_max_uses', v)}
              />
              <TextField
                label="号码前缀白名单"
                value={form.sms_phone_prefix_allowlist}
                onChange={(v) => set('sms_phone_prefix_allowlist', v)}
                placeholder="628389（仅接受 +62 838 9... 段；空=不过滤）"
              />
            </div>
            <p className="text-tiny text-text-tertiary">
              <span>常用 country 代号：0=俄罗斯、1=乌克兰、2=哈萨克、6=印尼、12=美国、52=墨西哥、187=巴西。多个用 <code>,</code> 分隔按顺序尝试。</span>
              <span className="ml-1">service：<code>dr</code> = ChatGPT / OpenAI；其他业务请参考 hero-sms 文档。</span>
              <span className="ml-1">号码前缀白名单：E.164 不带 + 号的纯数字前缀，逗号分隔多个；不命中的号会被立即丢弃换号（实测印尼 hero-sms 池只有 <code>628389</code> 段能投递 OpenAI SMS）。</span>
            </p>
          </Section>

          <Section
            icon={<Crown size={18} />}
            title="Plus 升级（GoPay）"
            desc="自动开通 ChatGPT Plus 任务成功后的收尾行为；依赖 GeeLark 云手机与 GoPay 前台自动化。"
          >
            <Toggle
              label="开通成功后自动在 GoPay 内移除 OpenAI（已连接应用）"
              checked={form.plus_upgrade_auto_gopay_unlink}
              onChange={(v) => set('plus_upgrade_auto_gopay_unlink', v)}
            />
            <p className="text-tiny text-text-tertiary">
              关闭后仅跳过自动解绑，仍可在「Plus 升级资源池 → 云手机」对单台手动点「解绑 OpenAI」。配置键：<code>plus_upgrade.auto_gopay_unlink</code>。
            </p>
          </Section>

          <Section icon={<CreditCard size={18} />} title="支付配置" desc="保存支付通道基础参数，后续充值下单与回调会读取这些配置。">
            <Toggle label="启用在线支付" checked={form.payment_enabled} onChange={(v) => set('payment_enabled', v)} />
            <div className="grid gap-3 md:grid-cols-2">
              <TextField label="默认支付通道" value={form.payment_provider} onChange={(v) => set('payment_provider', v)} placeholder="alipay / wechat" />
              <TextField label="支付回调地址" value={form.payment_notify_url} onChange={(v) => set('payment_notify_url', v)} />
              <TextField label="支付宝 AppID" value={form.alipay_app_id} onChange={(v) => set('alipay_app_id', v)} />
              <TextField label="微信商户号" value={form.wechat_mch_id} onChange={(v) => set('wechat_mch_id', v)} />
            </div>
            <Field label="支付宝私钥"><textarea className="textarea font-mono text-small min-h-[96px]" value={form.alipay_private_key} onChange={(e) => set('alipay_private_key', e.target.value)} /></Field>
            <TextField label="微信 API v3 Key" value={form.wechat_api_v3_key} onChange={(v) => set('wechat_api_v3_key', v)} type="password" />
          </Section>
        </div>
      )}
      {confirmDialog}
    </PageShell>
  );
}

function Section({ icon, title, children }: { icon: ReactNode; title: string; desc?: string; children: ReactNode }) {
  // mb-4 + break-inside-avoid 让卡片在 CSS columns 瀑布流里不被拆栏，且与下一张保持间距。
  return (
    <section className="card mb-4 overflow-hidden rounded-md border border-border bg-surface-1 break-inside-avoid">
      <header className="flex items-center gap-2 border-b border-border bg-surface-1 px-3 py-2">
        <span className="grid h-5 w-5 place-items-center rounded bg-info-soft text-klein-500">{icon}</span>
        <h2 className="text-small font-semibold text-text-primary">{title}</h2>
      </header>
      <div className="grid gap-2.5 p-3">{children}</div>
    </section>
  );
}

function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  return <label className="field"><span className="field-label">{label}</span>{children}</label>;
}

function HeadingLine({ icon, title, hint }: { icon: ReactNode; title: string; hint?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="grid h-4 w-4 place-items-center rounded bg-info-soft text-klein-500">{icon}</span>
      <span>{title}</span>
      {hint && <span className="ml-1 hidden text-tiny font-normal text-text-tertiary md:inline">— {hint}</span>}
    </span>
  );
}

function TextField({ label, value, onChange, placeholder, type = 'text' }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; type?: string }) {
  return <Field label={label}><input className="input input-sm" type={type} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} /></Field>;
}

function NumberField({ label, value, min, max, onChange }: { label: string; value: number; min?: number; max?: number; onChange: (v: number) => void }) {
  return <Field label={label}><input type="number" className="input input-sm tabular-nums" min={min} max={max} value={value} onChange={(e) => onChange(Number(e.target.value) || 0)} /></Field>;
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function CaptchaFallbackEditor({
  label,
  hint,
  providers,
  rows,
  onChange,
}: {
  label: string;
  hint?: ReactNode;
  providers: readonly string[];
  rows: CaptchaFallbackRow[];
  onChange: (rows: CaptchaFallbackRow[]) => void;
}) {
  const addRow = () => {
    const defaultProvider = providers[0] ?? 'anti-captcha';
    onChange([
      ...rows,
      {
        id: makeFallbackRowID(),
        provider: defaultProvider,
        api_key: '',
        endpoint: PROVIDER_DEFAULT_ENDPOINT[defaultProvider] ?? '',
      },
    ]);
  };
  const updateRow = (idx: number, patch: Partial<CaptchaFallbackRow>) => {
    onChange(rows.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
  };
  const removeRow = (idx: number) => {
    onChange(rows.filter((_, i) => i !== idx));
  };
  return (
    <div className="mt-2 rounded-md border border-dashed border-border bg-surface-2 p-3">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div className="text-small font-medium text-text-primary">{label}</div>
        <button
          type="button"
          className="btn btn-outline btn-xs"
          onClick={addRow}
        >
          <Plus size={12} /> 添加备用
        </button>
      </div>
      {rows.length === 0 ? (
        <p className="text-tiny text-text-tertiary">
          未配置备用服务商；当前为单家模式（主家失败立即跳号）。建议至少配 1 个备用以提升成功率。
        </p>
      ) : (
        <div className="space-y-2">
          {rows.map((row, idx) => (
            <div
              key={row.id}
              className="grid grid-cols-1 gap-2 rounded-md bg-surface-1 p-2 md:grid-cols-[max-content_1fr_1fr_max-content] md:items-center"
            >
              <span className="text-tiny text-text-tertiary md:pl-1">#{idx + 1}</span>
              <select
                className="select select-sm"
                value={row.provider}
                onChange={(e) => {
                  const v = e.target.value;
                  const ep = row.endpoint.trim();
                  const looseEp =
                    ep === '' ||
                    Object.values(PROVIDER_DEFAULT_ENDPOINT).includes(ep);
                  updateRow(idx, {
                    provider: v,
                    endpoint: looseEp ? (PROVIDER_DEFAULT_ENDPOINT[v] ?? ep) : ep,
                  });
                }}
              >
                {providers.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
              <input
                className="input input-sm font-mono"
                type="password"
                placeholder="API Key"
                value={row.api_key}
                onChange={(e) => updateRow(idx, { api_key: e.target.value })}
              />
              <div className="flex items-center gap-2">
                <input
                  className="input input-sm font-mono"
                  type="text"
                  placeholder={PROVIDER_DEFAULT_ENDPOINT[row.provider] ?? 'Endpoint'}
                  value={row.endpoint}
                  onChange={(e) => updateRow(idx, { endpoint: e.target.value })}
                />
                <button
                  type="button"
                  className="btn btn-ghost btn-xs text-danger-600"
                  aria-label="删除"
                  onClick={() => removeRow(idx)}
                >
                  <X size={14} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
      {hint && <p className="mt-2 text-tiny text-text-tertiary">{hint}</p>}
    </div>
  );
}

function Toggle({ label, checked, onChange, inline = false }: { label: string; checked: boolean; onChange: (v: boolean) => void; inline?: boolean }) {
  const switchEl = (
    <button type="button" role="switch" aria-checked={checked} onClick={() => onChange(!checked)} className={'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition ' + (checked ? 'bg-klein-500' : 'bg-surface-3')}>
      <span className={'inline-block h-5 w-5 rounded-full bg-white shadow transition transform ' + (checked ? 'translate-x-5' : 'translate-x-0.5')} />
    </button>
  );
  // inline=true：只渲染开关本体，由外层自带文案 & 背景卡片（用于已有 label 行的复合排版）。
  if (inline) return switchEl;
  return (
    <div className="flex items-center justify-between gap-4 rounded-md border border-border bg-surface-2 p-3">
      <div className="text-small font-medium text-text-primary">{label}</div>
      {switchEl}
    </div>
  );
}

function MailBackendCard({
  show,
  title,
  tag,
  hint,
  children,
}: {
  show: boolean;
  title: string;
  tag: 'active' | 'idle';
  hint?: string;
  children: ReactNode;
}) {
  if (!show) return null;
  const isActive = tag === 'active';
  return (
    <div
      className={
        'rounded-md border p-3 ' +
        (isActive
          ? 'border-klein-500/40 bg-info-soft shadow-glow-soft'
          : 'border-border bg-surface-2')
      }
    >
      <div className="mb-2 flex items-center gap-2">
        <span className="text-small font-medium text-text-primary">{title}</span>
        {isActive && (
          <span className="inline-flex items-center rounded-full bg-klein-gradient px-2 py-0.5 text-tiny text-text-on-klein">
            当前生效
          </span>
        )}
      </div>
      <div className="space-y-3">{children}</div>
      {hint && <p className="mt-2 text-tiny text-text-tertiary">{hint}</p>}
    </div>
  );
}

/**
 * ClusterStatusBanner —— 系统配置页顶部的「集群健康一览」横条。
 *
 * 设计目标：
 *   - 让运维进设置页就立刻看到主控 lease 通道（control-main）是不是活着；
 *   - 同时显示 在线 agent 数 / Maintenance 数；
 *   - 一键跳到「集群节点」管理页（已有 /cluster 路由）；
 *   - 5s 自动刷新；离线 / 关闭集群 / 加载失败都有明确视觉态。
 *
 * 故意做成「banner」而非藏在下方某张卡片，避免用户找不到设置面板。
 */
function ClusterStatusBanner() {
  const overview = useQuery({
    queryKey: ['admin', 'cluster', 'overview'],
    queryFn: () => clusterApi.overview(),
    refetchInterval: 5000,
    retry: false,
  });

  if (overview.isLoading) {
    return (
      <div className="card mb-4 flex items-center gap-3 p-3 text-tiny text-text-tertiary">
        <Activity size={14} className="animate-pulse" />
        正在加载集群状态…
      </div>
    );
  }
  if (overview.isError || !overview.data) {
    return (
      <div className="card mb-4 flex items-center gap-3 p-3 text-tiny text-text-tertiary">
        <Activity size={14} />
        集群状态不可用（接口异常）。可以继续在下方「集群调度」卡片里修改配置。
      </div>
    );
  }

  const d = overview.data;
  const tone = !d.enabled
    ? 'border-border bg-surface-2'
    : d.embedded_alive
    ? 'border-emerald-500/40 bg-emerald-500/10'
    : 'border-amber-500/40 bg-amber-500/10';
  const dotColor = !d.enabled
    ? 'bg-text-tertiary'
    : d.embedded_alive
    ? 'bg-emerald-500'
    : 'bg-amber-500';
  const stateText = !d.enabled
    ? '集群已关闭（所有任务走主控本地 goroutine）'
    : d.embedded_alive
    ? '集群在线 · 主控自带 lease 通道活跃'
    : '集群已开启，但主控心跳静默 → 任务可能堆积';
  const heartbeatAge =
    d.embedded_heartbeat_at != null
      ? Math.max(0, Math.round((Date.now() - new Date(d.embedded_heartbeat_at).getTime()) / 1000))
      : null;

  return (
    <div className={'card mb-4 flex flex-wrap items-center gap-3 border p-3 ' + tone}>
      <div className="flex items-center gap-2">
        <span className={'inline-block h-2.5 w-2.5 rounded-full ' + dotColor + (d.embedded_alive ? ' animate-pulse' : '')} />
        <span className="text-small font-medium text-text-primary">{stateText}</span>
      </div>
      <div className="ml-auto flex flex-wrap items-center gap-2 text-tiny text-text-tertiary">
        <Pill label="control-main" value={d.embedded_alive ? `inflight=${d.embedded_inflight}` : '离线'} />
        <Pill label="在线 agent" value={d.online_agents} />
        <Pill label="维护中" value={d.maintenance_nodes} tone={d.maintenance_nodes > 0 ? 'warn' : 'plain'} />
        <Pill label="节点总数" value={d.total_nodes} />
        {heartbeatAge != null && d.enabled && (
          <Pill label="心跳" value={`${heartbeatAge}s 前`} />
        )}
        <Link to="/cluster" className="btn btn-outline btn-xs">
          管理节点 <ArrowRight size={12} />
        </Link>
      </div>
    </div>
  );
}

function Pill({
  label,
  value,
  tone = 'plain',
}: {
  label: string;
  value: number | string;
  tone?: 'plain' | 'warn';
}) {
  const cls =
    tone === 'warn'
      ? 'border-amber-500/40 bg-amber-500/10 text-amber-600'
      : 'border-border bg-surface-2 text-text-tertiary';
  return (
    <span className={'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-tiny ' + cls}>
      <span className="opacity-70">{label}</span>
      <span className="font-medium text-text-primary tabular-nums">{value}</span>
    </span>
  );
}
