// 上游 API 管理：通道 / 路由 / 利润报表（Phase A + B）。
//
// 4 个 tab：
//   通道（channels）       — 后端 upstream_channel 表 CRUD
//   路由（routes）         — 内部 model_code/variant → channel 映射
//   利润总览（profit）     — 区间内 task_cost_log 聚合
//   成本日志（logs）       — task_cost_log 明细
//
// 设计原则：表格紧凑，详细编辑放对话框；每个 tab 都能独立工作。
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Pencil,
  Plus,
  RefreshCw,
  TrendingUp,
  Trash2,
  Network as NetworkIcon,
  Sparkles,
} from 'lucide-react';
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useState,
} from 'react';

import { useConfirm } from '../../components/ConfirmDialog';
import { PageHeader, PageShell, Pager, Section, Stat, StatRow, Toolbar, ToolbarSpacer } from '../../components/layout/PageShell';
import { ApiError } from '../../lib/api';
import {
  type ChannelType,
  type UpstreamChannel,
  type UpstreamChannelSaveBody,
  type UpstreamProfitDailyRow,
  type UpstreamRoute,
  type UpstreamRouteSaveBody,
  upstreamApi,
} from '../../lib/services';
import { toast } from '../../stores/toast';
import { usePageSize } from '../../stores/uiPrefs';

// 计费模式枚举与中文标签
const BILLING_MODE_LABELS: Record<UpstreamChannel['billing_mode'], string> = {
  per_call: '按次',
  per_unit: '按单位',
  per_token_io: 'Token I/O',
  per_credit: 'Credit 包',
  subscription: '订阅平摊',
  custom: '自定义',
};

// 格式化：micro_usd → "$0.0012"；可省略 6 位精度让 hover 显示完整值
function fmtMicroUSD(v: number): string {
  if (!v || Number.isNaN(v)) return '$0';
  const usd = v / 1_000_000;
  if (Math.abs(usd) >= 100) return `$${usd.toFixed(2)}`;
  if (Math.abs(usd) >= 1) return `$${usd.toFixed(3)}`;
  return `$${usd.toFixed(6)}`;
}

function fmtMicroCNY(v: number): string {
  if (!v || Number.isNaN(v)) return '¥0';
  const cny = v / 1_000_000;
  if (Math.abs(cny) >= 100) return `¥${cny.toFixed(2)}`;
  if (Math.abs(cny) >= 1) return `¥${cny.toFixed(3)}`;
  return `¥${cny.toFixed(6)}`;
}

function fmtUnitPrice(channel: UpstreamChannel): string {
  const up = channel.unit_price || {};
  switch (channel.billing_mode) {
    case 'per_call': {
      const v = (up.micro_usd as number) || 0;
      return v > 0 ? fmtMicroUSD(v) : '未配置';
    }
    case 'per_unit': {
      const v = (up.micro_usd_per_unit as number) || 0;
      return v > 0 ? `${fmtMicroUSD(v)} / 单位` : '未配置';
    }
    case 'per_token_io': {
      const inP = (up.input_per_1k_micro_usd as number) || 0;
      const outP = (up.output_per_1k_micro_usd as number) || 0;
      if (!inP && !outP) return '未配置';
      return `${fmtMicroUSD(inP)} / 1K in · ${fmtMicroUSD(outP)} / 1K out`;
    }
    case 'per_credit': {
      const credits = (up.credits_per_call as number) || 0;
      const pack = (up.monthly_pack_micro_usd as number) || 0;
      const m = (up.credits_per_month as number) || 0;
      if (!credits) return '未配置';
      const per = m > 0 ? fmtMicroUSD(pack / m) : '?';
      return `${credits} cr × ${per}`;
    }
    case 'subscription': {
      if (channel.expected_monthly_calls > 0) {
        const per = channel.monthly_fixed_cost / channel.expected_monthly_calls;
        return `${fmtMicroUSD(channel.monthly_fixed_cost)}/月 ÷ ${channel.expected_monthly_calls.toLocaleString()} = ${fmtMicroUSD(per)}/次`;
      }
      return `${fmtMicroUSD(channel.monthly_fixed_cost)}/月（缺预估次数）`;
    }
    case 'custom':
      return '调用方手填';
  }
}

type Tab = 'channels' | 'routes' | 'profit' | 'logs';

export default function UpstreamChannelsPage() {
  const [tab, setTab] = useState<Tab>('channels');
  return (
    <PageShell>
      <PageHeader icon={<NetworkIcon size={16} />} title="上游 API 管理" />
      <nav className="filter-bar flex flex-wrap gap-1 rounded-md border border-border bg-surface-1 p-1">
        {(
          [
            { v: 'channels', label: '通道' },
            { v: 'routes', label: '路由' },
            { v: 'profit', label: '利润报表' },
            { v: 'logs', label: '成本日志' },
          ] as Array<{ v: Tab; label: string }>
        ).map((it) => (
          <button
            key={it.v}
            type="button"
            className={`btn btn-sm ${tab === it.v ? 'btn-primary' : 'btn-ghost'}`}
            onClick={() => setTab(it.v)}
          >
            {it.label}
          </button>
        ))}
      </nav>
      {tab === 'channels' && <ChannelsTab />}
      {tab === 'routes' && <RoutesTab />}
      {tab === 'profit' && <ProfitTab />}
      {tab === 'logs' && <CostLogsTab />}
    </PageShell>
  );
}

// =====================================================
// Channels
// =====================================================

function ChannelsTab() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [provider, setProvider] = useState<string>('');
  const [enabled, setEnabled] = useState<'' | '1' | '0'>('');
  const [keyword, setKeyword] = useState('');
  const [dialog, setDialog] = useState<
    | { mode: 'create' }
    | { mode: 'edit'; row: UpstreamChannel }
    | null
  >(null);

  useEffect(() => setPage(1), [keyword, provider, enabled, pageSize]);

  const list = useQuery({
    queryKey: ['admin', 'upstream', 'channels', { page, pageSize, provider, enabled, keyword }],
    queryFn: () =>
      upstreamApi.listChannels({
        page,
        page_size: pageSize,
        provider: provider || undefined,
        enabled: enabled === '' ? undefined : enabled === '1',
        keyword: keyword.trim() || undefined,
      }),
  });

  const seedMutation = useMutation({
    mutationFn: () => upstreamApi.seedChannels(),
    onSuccess: () => {
      toast.success('默认通道已写入（如有现有通道则跳过）');
      qc.invalidateQueries({ queryKey: ['admin', 'upstream'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (id: number) => upstreamApi.removeChannel(id),
    onSuccess: () => {
      toast.success('通道已删除');
      qc.invalidateQueries({ queryKey: ['admin', 'upstream'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const total = list.data?.total ?? 0;
  const items = list.data?.list ?? [];

  return (
    <>
      <Toolbar>
        <select className="select select-sm" value={provider} onChange={(e) => setProvider(e.target.value)}>
          <option value="">全部 Provider</option>
          <option value="gpt">GPT</option>
          <option value="grok">Grok</option>
          <option value="adobe">Adobe</option>
          <option value="pic2api">pic2api</option>
          <option value="geelark">GeeLark</option>
          <option value="smspool">SMS</option>
          <option value="capsolver">Captcha</option>
          <option value="proxy">Proxy</option>
        </select>
        <select className="select select-sm" value={enabled} onChange={(e) => setEnabled(e.target.value as '' | '1' | '0')}>
          <option value="">启用状态</option>
          <option value="1">已启用</option>
          <option value="0">已停用</option>
        </select>
        <input
          className="input input-sm flex-1 min-w-[200px]"
          placeholder="按 key / label / provider 搜索"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
        />
        <ToolbarSpacer />
        <button className="btn btn-outline btn-sm" onClick={() => qc.invalidateQueries({ queryKey: ['admin', 'upstream'] })}>
          <RefreshCw size={14} /> 刷新
        </button>
        <button className="btn btn-outline btn-sm" disabled={seedMutation.isPending} onClick={() => seedMutation.mutate()}>
          <Sparkles size={14} /> 灌默认 15 条
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setDialog({ mode: 'create' })}>
          <Plus size={14} /> 新增通道
        </button>
      </Toolbar>

      <Section bodyClass="p-0">
        <div className="overflow-x-auto">
          <table className="data-table min-w-[1300px]">
            <thead>
              <tr>
                <th>Key / Label</th>
                <th>类型</th>
                <th>Provider · Route</th>
                <th>Billing</th>
                <th>单价 / 凭据</th>
                <th>启用</th>
                <th>支持模型</th>
                <th className="text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={8} className="py-10 text-center text-small text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={8}>
                    <div className="empty-state">
                      <p className="empty-state-title">尚无通道</p>
                      <p className="empty-state-desc">
                        点击右上「灌默认 15 条」一键导入；或直接新增。
                      </p>
                    </div>
                  </td>
                </tr>
              )}
              {items.map((row) => {
                const isLocal = row.channel_type === 'local_pool';
                return (
                  <tr key={row.id}>
                    <td>
                      <div className="font-mono text-small text-text-primary">{row.key}</div>
                      <div className="mt-0.5 text-tiny text-text-tertiary">{row.label || '—'}</div>
                    </td>
                    <td>
                      {isLocal ? (
                        <span className="badge badge-success">本地号池</span>
                      ) : (
                        <span className="badge badge-info">外部 API</span>
                      )}
                    </td>
                    <td className="text-small">
                      <span className="badge">{row.provider}</span>
                      {row.route && <span className="ml-1 text-text-tertiary">· {row.route}</span>}
                    </td>
                    <td className="text-small">
                      <span className="badge badge-info">{BILLING_MODE_LABELS[row.billing_mode]}</span>
                    </td>
                    <td
                      className="font-mono text-tiny text-text-secondary"
                      title={JSON.stringify(row.unit_price)}
                    >
                      {fmtUnitPrice(row)}
                      {!isLocal && (
                        <div className="mt-0.5">
                          {row.has_api_key ? (
                            <span className="badge badge-success text-tiny">key 已配</span>
                          ) : (
                            <span className="badge badge-warning text-tiny">缺 key</span>
                          )}
                        </div>
                      )}
                    </td>
                    <td>
                      {row.enabled ? (
                        <span className="badge badge-success">启用</span>
                      ) : (
                        <span className="badge">停用</span>
                      )}
                    </td>
                    <td className="text-tiny text-text-tertiary">
                      {isLocal ? (
                        '全部'
                      ) : row.supported_models?.length ? (
                        <span title={row.supported_models.join(', ')}>
                          {row.supported_models.slice(0, 3).join(', ')}
                          {row.supported_models.length > 3 ? ` +${row.supported_models.length - 3}` : ''}
                        </span>
                      ) : (
                        <span className="text-text-tertiary">不限制</span>
                      )}
                    </td>
                    <td>
                      <div className="inline-flex gap-1">
                        <button
                          className="btn btn-ghost btn-icon btn-sm"
                          title="编辑"
                          onClick={() => setDialog({ mode: 'edit', row })}
                        >
                          <Pencil size={14} />
                        </button>
                        <button
                          className="btn btn-danger-ghost btn-icon btn-sm"
                          title={isLocal ? '本地号池通道不可删（系统内置）' : '删除'}
                          disabled={isLocal}
                          onClick={async () => {
                            if (isLocal) return;
                            const ok = await confirm({
                              title: '删除上游通道',
                              description: (
                                <>
                                  通道 <span className="font-mono text-text-primary">{row.key}</span> 将被删除，
                                  其下所有路由也会被一并清掉，是否继续？
                                </>
                              ),
                              tone: 'danger',
                              confirmLabel: '删除',
                            });
                            if (ok) removeMutation.mutate(row.id);
                          }}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <Pager total={total} page={page} pageSize={pageSize} onChange={setPage} onPageSizeChange={setPageSize} sizeOptions={sizeOptions} />
      </Section>

      {dialog && (
        <ChannelDialog
          mode={dialog.mode}
          row={dialog.mode === 'edit' ? dialog.row : undefined}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            qc.invalidateQueries({ queryKey: ['admin', 'upstream'] });
          }}
        />
      )}
      {confirmDialog}
    </>
  );
}

function ChannelDialog({
  mode,
  row,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  row?: UpstreamChannel;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isLocalPool = row?.channel_type === 'local_pool';
  const [body, setBody] = useState<UpstreamChannelSaveBody>(() => ({
    key: row?.key || '',
    channel_type: row?.channel_type || 'external_api',
    provider: row?.provider || 'gpt',
    route: row?.route || '',
    base_url: row?.base_url || '',
    label: row?.label || '',
    enabled: row?.enabled ?? true,
    billing_mode: row?.billing_mode || 'per_call',
    unit_price: row?.unit_price || {},
    currency: row?.currency || 'USD',
    capabilities: row?.capabilities || {},
    supported_models: row?.supported_models || [],
    monthly_fixed_cost: row?.monthly_fixed_cost || 0,
    expected_monthly_calls: row?.expected_monthly_calls || 0,
    fx_to_cny: row?.fx_to_cny || 7.2,
    notes: row?.notes || '',
  }));
  /**
   * api_key 三态：
   *   undefined = 不动（编辑时保留旧值）
   *   ''       = 用户主动留空（在 UI 上点了"清空"按钮）
   *   非空      = 用户填了新值
   */
  const [apiKey, setApiKey] = useState<string | undefined>(undefined);
  const [supportedModelsText, setSupportedModelsText] = useState((row?.supported_models || []).join(', '));

  const [unitPriceText, setUnitPriceText] = useState(JSON.stringify(body.unit_price ?? {}, null, 2));
  const [capabilitiesText, setCapabilitiesText] = useState(JSON.stringify(body.capabilities ?? {}, null, 2));

  const create = useMutation({
    mutationFn: (b: UpstreamChannelSaveBody) => upstreamApi.createChannel(b),
    onSuccess: () => {
      toast.success('通道已创建');
      onSaved();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const update = useMutation({
    mutationFn: (b: UpstreamChannelSaveBody) => upstreamApi.updateChannel(row!.id, b),
    onSuccess: () => {
      toast.success('通道已更新');
      onSaved();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    let unitPrice: Record<string, unknown> = {};
    let caps: Record<string, unknown> = {};
    try {
      unitPrice = unitPriceText.trim() ? JSON.parse(unitPriceText) : {};
    } catch {
      toast.error('单价 JSON 解析失败');
      return;
    }
    try {
      caps = capabilitiesText.trim() ? JSON.parse(capabilitiesText) : {};
    } catch {
      toast.error('能力 JSON 解析失败');
      return;
    }
    const supportedModels = supportedModelsText
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    const payload: UpstreamChannelSaveBody = {
      ...body,
      key: body.key?.trim(),
      provider: body.provider?.trim(),
      route: body.route?.trim() || '',
      label: body.label?.trim() || '',
      base_url: body.base_url?.trim() || '',
      unit_price: unitPrice,
      capabilities: caps,
      supported_models: supportedModels,
      notes: body.notes?.trim() || '',
    };
    if (apiKey !== undefined) {
      // 用户动过 api_key 输入框；空字符串 = 清空、非空 = 覆盖
      payload.api_key = apiKey === '' ? '__CLEAR__' : apiKey;
    }
    if (mode === 'create') {
      create.mutate(payload);
    } else {
      update.mutate(payload);
    }
  };

  const submitting = create.isPending || update.isPending;

  return (
    <Modal
      title={
        mode === 'create'
          ? '新增上游通道'
          : isLocalPool
            ? '编辑本地号池通道'
            : '编辑外部 API 通道'
      }
      onClose={onClose}
    >
      <form className="space-y-3" onSubmit={submit}>
        {isLocalPool && (
          <div className="rounded-md border border-info-border bg-info-soft px-3 py-2 text-tiny text-text-secondary">
            这是系统内置的「本地号池」通道。runtime 看到它会自动按请求 model 反查 pool_gpt / pool_grok /
            pool_adobe 三张本地号池表选号。不需要 API key / base_url；只用配标签、月度成本估算 +
            启用状态。<strong className="text-text-primary"> Key、Provider、Channel Type 等不可改。</strong>
          </div>
        )}
        <div className="grid grid-cols-3 gap-3">
          <Field label="通道类型">
            <select
              className="select"
              value={body.channel_type}
              onChange={(e) => setBody((p) => ({ ...p, channel_type: e.target.value as ChannelType }))}
              disabled={mode === 'edit'}
            >
              <option value="local_pool">local_pool（本地号池，系统内置）</option>
              <option value="external_api">external_api（第三方付费 API）</option>
            </select>
          </Field>
          <Field label="Key（唯一标识，不可重复）">
            <input
              className="input font-mono"
              placeholder="pic2api.gemini.flash"
              value={body.key}
              onChange={(e) => setBody((p) => ({ ...p, key: e.target.value }))}
              disabled={mode === 'edit'}
            />
          </Field>
          <Field label="Provider（标签）">
            <input
              className="input"
              placeholder="pic2api / xxapi / openai ..."
              value={body.provider}
              onChange={(e) => setBody((p) => ({ ...p, provider: e.target.value }))}
              disabled={isLocalPool}
            />
          </Field>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Route（子路径，仅展示）">
            <input
              className="input"
              placeholder="api / firefly / rent ..."
              value={body.route}
              onChange={(e) => setBody((p) => ({ ...p, route: e.target.value }))}
              disabled={isLocalPool}
            />
          </Field>
          <Field label="Label（运营备注名）">
            <input
              className="input"
              placeholder="pic2api · gemini-2.5-flash"
              value={body.label}
              onChange={(e) => setBody((p) => ({ ...p, label: e.target.value }))}
            />
          </Field>
        </div>
        {!isLocalPool && (
          <>
            <Field label="Base URL（API 入口）" hint="OpenAI-compat 协议；不要带 /v1/chat/completions 后缀">
              <input
                className="input font-mono text-small"
                placeholder="https://api.pic2api.com"
                value={body.base_url}
                onChange={(e) => setBody((p) => ({ ...p, base_url: e.target.value }))}
              />
            </Field>
            <Field
              label="API Key"
              hint={
                mode === 'edit'
                  ? row?.has_api_key
                    ? '已配置；留空表示不变；填新值 = 覆盖；填空字符串 = 清空。'
                    : '该通道尚未配置 API key；填入即生效。'
                  : '填入即生效。提交后不会再回显明文。'
              }
            >
              <input
                className="input font-mono text-small"
                placeholder={mode === 'edit' && row?.has_api_key ? '••••（保留现有 key）' : 'sk-...'}
                value={apiKey ?? ''}
                onChange={(e) => setApiKey(e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
              {mode === 'edit' && row?.has_api_key && (
                <button
                  type="button"
                  className="btn btn-ghost btn-xs mt-1 text-danger"
                  onClick={() => {
                    if (window.confirm('确认清空此通道的 API key？清空后通道将不可用，直到重新填入。')) {
                      setApiKey('');
                    }
                  }}
                >
                  清空 API key
                </button>
              )}
            </Field>
            <Field
              label="支持的模型列表"
              hint='逗号分隔，例：gpt-image-2, gpt-4o, grok-4-fast 。留空 = 不限制（任何 model_code 都可路由到此通道）'
            >
              <input
                className="input font-mono text-small"
                placeholder="gpt-image-2, gpt-4o, grok-4-fast"
                value={supportedModelsText}
                onChange={(e) => setSupportedModelsText(e.target.value)}
              />
            </Field>
          </>
        )}

        <div className="grid grid-cols-2 gap-3">
          <Field label="计费模式">
            <select
              className="select"
              value={body.billing_mode}
              onChange={(e) => setBody((p) => ({ ...p, billing_mode: e.target.value as UpstreamChannel['billing_mode'] }))}
            >
              {Object.entries(BILLING_MODE_LABELS).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}（{k}）
                </option>
              ))}
            </select>
          </Field>
          <Field label="计价币种">
            <input
              className="input"
              value={body.currency || 'USD'}
              onChange={(e) => setBody((p) => ({ ...p, currency: e.target.value.toUpperCase() }))}
            />
          </Field>
        </div>

        <Field
          label="单价 JSON"
          hint={
            <>
              字段随计费模式不同：
              <br />
              · per_call → <code>{`{"micro_usd": 40000}`}</code> (= $0.04 / 次)
              <br />
              · per_unit → <code>{`{"micro_usd_per_unit": 6500}`}</code>
              <br />
              · per_token_io → <code>{`{"input_per_1k_micro_usd": 150, "output_per_1k_micro_usd": 600}`}</code>
              <br />
              · per_credit → <code>{`{"credits_per_call": 4, "credits_per_month": 5000, "monthly_pack_micro_usd": 9990000}`}</code>
              <br />
              · subscription / custom 留空 <code>{`{}`}</code>
            </>
          }
        >
          <textarea
            className="textarea min-h-[120px] font-mono text-small"
            value={unitPriceText}
            onChange={(e) => setUnitPriceText(e.target.value)}
          />
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="订阅月费 (micro_usd)" hint="仅 subscription / per_credit 用到。例：$9.99 写 9990000">
            <input
              type="number"
              className="input font-mono"
              value={body.monthly_fixed_cost ?? 0}
              onChange={(e) => setBody((p) => ({ ...p, monthly_fixed_cost: Number(e.target.value) || 0 }))}
            />
          </Field>
          <Field label="预估月调用次数">
            <input
              type="number"
              className="input"
              value={body.expected_monthly_calls ?? 0}
              onChange={(e) => setBody((p) => ({ ...p, expected_monthly_calls: Number(e.target.value) || 0 }))}
            />
          </Field>
        </div>

        <Field
          label="能力 JSON"
          hint={
            <>
              声明这个通道支持哪些 kind / variant，admin 路由匹配会按此过滤：
              <br />
              <code>{`{"kinds":["image"], "variants":["2k"]}`}</code>
            </>
          }
        >
          <textarea
            className="textarea min-h-[80px] font-mono text-small"
            value={capabilitiesText}
            onChange={(e) => setCapabilitiesText(e.target.value)}
          />
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="本币 → CNY 兜底汇率">
            <input
              type="number"
              step="0.0001"
              className="input"
              value={body.fx_to_cny ?? 7.2}
              onChange={(e) => setBody((p) => ({ ...p, fx_to_cny: Number(e.target.value) || 0 }))}
            />
          </Field>
          <Field label="启用">
            <label className="inline-flex h-9 items-center gap-2 px-1">
              <input
                type="checkbox"
                checked={body.enabled ?? true}
                onChange={(e) => setBody((p) => ({ ...p, enabled: e.target.checked }))}
              />
              <span className="text-small text-text-secondary">通道启用后才会被 CostRecorder 使用</span>
            </label>
          </Field>
        </div>

        <Field label="备注">
          <textarea
            className="textarea min-h-[60px]"
            placeholder="可选；记下这个通道的特殊配置 / 价格出处"
            value={body.notes || ''}
            onChange={(e) => setBody((p) => ({ ...p, notes: e.target.value }))}
          />
        </Field>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={submitting}>
            {submitting ? '保存中...' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// =====================================================
// Routes
// =====================================================

function RoutesTab() {
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();
  const [model, setModel] = useState('');
  const [dialog, setDialog] = useState<
    | { mode: 'create' }
    | { mode: 'edit'; row: UpstreamRoute }
    | null
  >(null);

  const list = useQuery({
    queryKey: ['admin', 'upstream', 'routes', { page, pageSize, model }],
    queryFn: () =>
      upstreamApi.listRoutes({
        page,
        page_size: pageSize,
        model_code: model.trim() || undefined,
      }),
  });
  const channels = useQuery({
    queryKey: ['admin', 'upstream', 'channels', 'all'],
    queryFn: () => upstreamApi.listChannels({ page: 1, page_size: 500 }),
  });

  const removeMutation = useMutation({
    mutationFn: (id: number) => upstreamApi.removeRoute(id),
    onSuccess: () => {
      toast.success('路由已删除');
      qc.invalidateQueries({ queryKey: ['admin', 'upstream'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const total = list.data?.total ?? 0;
  const items = list.data?.list ?? [];

  return (
    <>
      <Toolbar>
        <input
          className="input input-sm flex-1 min-w-[200px]"
          placeholder="按 model_code 搜索（例：gpt-image-2）"
          value={model}
          onChange={(e) => {
            setModel(e.target.value);
            setPage(1);
          }}
        />
        <ToolbarSpacer />
        <button className="btn btn-outline btn-sm" onClick={() => qc.invalidateQueries({ queryKey: ['admin', 'upstream'] })}>
          <RefreshCw size={14} /> 刷新
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setDialog({ mode: 'create' })}>
          <Plus size={14} /> 新增路由
        </button>
      </Toolbar>

      <Section bodyClass="p-0">
        <div className="overflow-x-auto">
          <table className="data-table min-w-[1100px]">
            <thead>
              <tr>
                <th>Model</th>
                <th>Variant</th>
                <th>→ 通道</th>
                <th>优先级</th>
                <th>乘数</th>
                <th>启用</th>
                <th>备注</th>
                <th className="text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={8} className="py-10 text-center text-small text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={8}>
                    <div className="empty-state">
                      <p className="empty-state-title">暂无路由</p>
                      <p className="empty-state-desc">点击「新增路由」，把内部 model_code 映射到一个上游通道。</p>
                    </div>
                  </td>
                </tr>
              )}
              {items.map((row) => (
                <tr key={row.id}>
                  <td className="font-mono text-small text-text-primary">{row.model_code}</td>
                  <td className="text-small">{row.variant_key || <span className="text-text-tertiary">—</span>}</td>
                  <td className="text-small">
                    <div className="font-mono text-text-primary">{row.channel_key || `#${row.upstream_channel_id}`}</div>
                    {row.channel_label && <div className="text-tiny text-text-tertiary">{row.channel_label}</div>}
                  </td>
                  <td className="text-small text-text-secondary">{row.priority}</td>
                  <td className="font-mono text-small text-text-secondary">{row.cost_multiplier.toFixed(3)}×</td>
                  <td>
                    {row.enabled ? <span className="badge badge-success">启用</span> : <span className="badge">停用</span>}
                  </td>
                  <td className="max-w-[200px] truncate text-tiny text-text-tertiary" title={row.notes || ''}>
                    {row.notes || '—'}
                  </td>
                  <td>
                    <div className="inline-flex gap-1">
                      <button className="btn btn-ghost btn-icon btn-sm" title="编辑" onClick={() => setDialog({ mode: 'edit', row })}>
                        <Pencil size={14} />
                      </button>
                      <button
                        className="btn btn-danger-ghost btn-icon btn-sm"
                        title="删除"
                        onClick={async () => {
                          const ok = await confirm({
                            title: '删除路由',
                            description: (
                              <>
                                确定删除 <span className="font-mono text-text-primary">{row.model_code}</span>
                                {row.variant_key ? ` / ${row.variant_key}` : ''} → {row.channel_key || `#${row.upstream_channel_id}`} 这条路由？
                              </>
                            ),
                            tone: 'danger',
                            confirmLabel: '删除',
                          });
                          if (ok) removeMutation.mutate(row.id);
                        }}
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pager total={total} page={page} pageSize={pageSize} onChange={setPage} onPageSizeChange={setPageSize} sizeOptions={sizeOptions} />
      </Section>

      {dialog && (
        <RouteDialog
          mode={dialog.mode}
          row={dialog.mode === 'edit' ? dialog.row : undefined}
          channels={channels.data?.list ?? []}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            qc.invalidateQueries({ queryKey: ['admin', 'upstream'] });
          }}
        />
      )}
      {confirmDialog}
    </>
  );
}

function RouteDialog({
  mode,
  row,
  channels,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  row?: UpstreamRoute;
  channels: UpstreamChannel[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [body, setBody] = useState<UpstreamRouteSaveBody>(() => ({
    model_code: row?.model_code || '',
    variant_key: row?.variant_key || '',
    upstream_channel_id: row?.upstream_channel_id || (channels[0]?.id ?? 0),
    priority: row?.priority || 1,
    enabled: row?.enabled ?? true,
    cost_multiplier: row?.cost_multiplier ?? 1.0,
    notes: row?.notes || '',
  }));

  const create = useMutation({
    mutationFn: (b: UpstreamRouteSaveBody) => upstreamApi.createRoute(b),
    onSuccess: () => {
      toast.success('路由已创建');
      onSaved();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });
  const update = useMutation({
    mutationFn: (b: UpstreamRouteSaveBody) => upstreamApi.updateRoute(row!.id, b),
    onSuccess: () => {
      toast.success('路由已更新');
      onSaved();
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!body.model_code?.trim()) {
      toast.error('请填写 model_code');
      return;
    }
    if (!body.upstream_channel_id) {
      toast.error('请选择上游通道');
      return;
    }
    const payload: UpstreamRouteSaveBody = {
      ...body,
      model_code: body.model_code?.trim(),
      variant_key: body.variant_key?.trim() || '',
      notes: body.notes?.trim() || '',
    };
    if (mode === 'create') {
      create.mutate(payload);
    } else {
      update.mutate(payload);
    }
  };

  const submitting = create.isPending || update.isPending;

  return (
    <Modal title={mode === 'create' ? '新增路由' : '编辑路由'} onClose={onClose}>
      <form className="space-y-3" onSubmit={submit}>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Model Code">
            <input
              className="input font-mono"
              placeholder="gpt-image-2"
              value={body.model_code}
              onChange={(e) => setBody((p) => ({ ...p, model_code: e.target.value }))}
            />
          </Field>
          <Field label="Variant" hint="可选；图片 1k/2k/4k，视频 6/10/20/30，文字留空">
            <input
              className="input font-mono"
              placeholder="2k"
              value={body.variant_key || ''}
              onChange={(e) => setBody((p) => ({ ...p, variant_key: e.target.value }))}
            />
          </Field>
        </div>
        <Field label="上游通道">
          <select
            className="select"
            value={body.upstream_channel_id}
            onChange={(e) => setBody((p) => ({ ...p, upstream_channel_id: Number(e.target.value) }))}
          >
            <option value="0">请选择...</option>
            {channels.map((c) => (
              <option key={c.id} value={c.id}>
                {c.key} {c.label ? `· ${c.label}` : ''} ({BILLING_MODE_LABELS[c.billing_mode]})
              </option>
            ))}
          </select>
        </Field>
        <div className="grid grid-cols-3 gap-3">
          <Field label="优先级" hint="同 model+variant 内 priority 越小越优先">
            <input
              type="number"
              className="input"
              min={1}
              value={body.priority ?? 1}
              onChange={(e) => setBody((p) => ({ ...p, priority: Number(e.target.value) || 1 }))}
            />
          </Field>
          <Field label="成本乘数" hint="同通道下分档定价；4K = 4× 等">
            <input
              type="number"
              step="0.01"
              className="input font-mono"
              value={body.cost_multiplier ?? 1.0}
              onChange={(e) => setBody((p) => ({ ...p, cost_multiplier: Number(e.target.value) || 0 }))}
            />
          </Field>
          <Field label="启用">
            <label className="inline-flex h-9 items-center gap-2 px-1">
              <input
                type="checkbox"
                checked={body.enabled ?? true}
                onChange={(e) => setBody((p) => ({ ...p, enabled: e.target.checked }))}
              />
              <span className="text-small text-text-secondary">启用</span>
            </label>
          </Field>
        </div>
        <Field label="备注">
          <input
            className="input"
            value={body.notes || ''}
            onChange={(e) => setBody((p) => ({ ...p, notes: e.target.value }))}
          />
        </Field>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" className="btn btn-outline btn-md" onClick={onClose}>
            取消
          </button>
          <button type="submit" className="btn btn-primary btn-md" disabled={submitting}>
            {submitting ? '保存中...' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// =====================================================
// Profit
// =====================================================

function ProfitTab() {
  // 默认最近 30 天
  const today = useMemo(() => new Date(), []);
  const defaultFrom = useMemo(() => {
    const d = new Date(today);
    d.setDate(d.getDate() - 30);
    return d.toISOString().slice(0, 10);
  }, [today]);
  const [from, setFrom] = useState(defaultFrom);
  const [to, setTo] = useState(today.toISOString().slice(0, 10));
  const [dim, setDim] = useState<'day' | 'day,model' | 'day,channel' | 'day,provider'>('day');

  const overview = useQuery({
    queryKey: ['admin', 'upstream', 'profit', 'overview', { from, to }],
    queryFn: () => upstreamApi.profitOverview({ from, to }),
  });
  const daily = useQuery({
    queryKey: ['admin', 'upstream', 'profit', 'daily', { from, to, dim }],
    queryFn: () => upstreamApi.profitDaily({ from, to, dim }),
  });

  const o = overview.data;
  const fx = o?.fx_usd_to_cny || 7.2;
  const costCNY = (o?.cost_micro_usd ?? 0) * fx;
  const margin = o?.gross_margin_micro_cny ?? 0;
  const rateText = o ? `${(o.gross_margin_rate * 100).toFixed(1)}%` : '—';

  return (
    <>
      <Toolbar>
        <label className="inline-flex items-center gap-2 text-small text-text-secondary">
          从
          <input className="input input-sm" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          到
          <input className="input input-sm" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <select className="select select-sm" value={dim} onChange={(e) => setDim(e.target.value as typeof dim)}>
          <option value="day">按天</option>
          <option value="day,model">按天 × 模型</option>
          <option value="day,channel">按天 × 通道</option>
          <option value="day,provider">按天 × Provider</option>
        </select>
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary">汇率 1 USD = ¥{fx.toFixed(4)}</span>
      </Toolbar>

      <StatRow cols={6}>
        <Stat label="任务数" value={overview.isLoading ? '...' : (o?.task_count ?? 0).toLocaleString()} />
        <Stat label="销售（CNY）" value={overview.isLoading ? '...' : fmtMicroCNY(o?.sale_micro_cny ?? 0)} tone="text-text-primary" />
        <Stat label="上游成本（USD）" value={overview.isLoading ? '...' : fmtMicroUSD(o?.cost_micro_usd ?? 0)} tone="text-text-secondary" />
        <Stat label="上游成本（CNY 折算）" value={overview.isLoading ? '...' : fmtMicroCNY(costCNY)} tone="text-text-secondary" />
        <Stat label="毛利（CNY）" value={overview.isLoading ? '...' : fmtMicroCNY(margin)} tone={margin >= 0 ? 'text-success' : 'text-danger'} />
        <Stat label="毛利率" value={overview.isLoading ? '...' : rateText} tone={margin >= 0 ? 'text-success' : 'text-danger'} />
      </StatRow>

      <Section
        title={
          <span className="inline-flex items-center gap-2">
            <TrendingUp size={14} /> 每日明细（{dim}）
          </span>
        }
        bodyClass="p-0"
      >
        <div className="overflow-x-auto">
          <table className="data-table min-w-[900px]">
            <thead>
              <tr>
                <th>日期</th>
                {dim === 'day,model' && <th>Model</th>}
                {dim === 'day,channel' && <th>Channel ID</th>}
                {dim === 'day,provider' && <th>Provider</th>}
                <th>任务数</th>
                <th>销售（CNY）</th>
                <th>上游成本（USD）</th>
                <th>毛利（CNY）</th>
              </tr>
            </thead>
            <tbody>
              {daily.isLoading && (
                <tr>
                  <td colSpan={6} className="py-10 text-center text-small text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!daily.isLoading && (daily.data?.items?.length ?? 0) === 0 && (
                <tr>
                  <td colSpan={6}>
                    <div className="empty-state">
                      <p className="empty-state-title">区间内暂无成本日志</p>
                      <p className="empty-state-desc">
                        若新通道刚配好，先跑几次生成任务，CostRecorder 会自动落账。
                      </p>
                    </div>
                  </td>
                </tr>
              )}
              {(daily.data?.items ?? []).map((row: UpstreamProfitDailyRow, idx) => {
                const day = String(row.day || '').slice(0, 10);
                const costMicroCNYHere = (row.cost_micro_usd || 0) * (daily.data?.fx_usd_to_cny || fx);
                const m = (row.sale_micro_cny || 0) - costMicroCNYHere;
                return (
                  <tr key={`${day}-${idx}`}>
                    <td className="font-mono text-small">{day}</td>
                    {dim === 'day,model' && <td className="text-small text-text-secondary">{row.model_code || '—'}</td>}
                    {dim === 'day,channel' && <td className="text-small text-text-secondary">#{row.upstream_channel_id || '—'}</td>}
                    {dim === 'day,provider' && <td className="text-small text-text-secondary">{row.provider || '—'}</td>}
                    <td>{(row.task_count || 0).toLocaleString()}</td>
                    <td className="font-mono">{fmtMicroCNY(row.sale_micro_cny || 0)}</td>
                    <td className="font-mono text-text-secondary">{fmtMicroUSD(row.cost_micro_usd || 0)}</td>
                    <td className={`font-mono ${m >= 0 ? 'text-success' : 'text-danger'}`}>{fmtMicroCNY(m)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Section>
    </>
  );
}

// =====================================================
// Cost Logs
// =====================================================

function CostLogsTab() {
  // 暂用 task_cost_log 直接展示；后续可加 channel/model 过滤、CSV 导出
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize, sizeOptions] = usePageSize();

  const list = useQuery({
    queryKey: ['admin', 'upstream', 'logs', { page, pageSize }],
    queryFn: () => upstreamApi.costLogs({ page, page_size: pageSize }),
  });

  const total = list.data?.total ?? 0;
  const items = list.data?.list ?? [];

  return (
    <>
      <Toolbar>
        <ToolbarSpacer />
        <span className="text-tiny text-text-tertiary">最近 {total} 条成本日志</span>
      </Toolbar>
      <Section bodyClass="p-0">
        <div className="overflow-x-auto">
          <table className="data-table min-w-[1100px]">
            <thead>
              <tr>
                <th>时间</th>
                <th>Ref</th>
                <th>Model / Variant</th>
                <th>Channel</th>
                <th>Account</th>
                <th>Qty</th>
                <th>Cost USD</th>
                <th>Sale CNY</th>
              </tr>
            </thead>
            <tbody>
              {list.isLoading && (
                <tr>
                  <td colSpan={8} className="py-10 text-center text-small text-text-tertiary">
                    加载中...
                  </td>
                </tr>
              )}
              {!list.isLoading && items.length === 0 && (
                <tr>
                  <td colSpan={8}>
                    <div className="empty-state">
                      <p className="empty-state-title">暂无日志</p>
                      <p className="empty-state-desc">配置通道 / 路由后，跑一次任务即会出现。</p>
                    </div>
                  </td>
                </tr>
              )}
              {items.map((row, idx) => (
                <tr key={(row.id as number) ?? idx}>
                  <td className="font-mono text-tiny text-text-secondary">{String(row.recorded_at || '').slice(0, 19).replace('T', ' ')}</td>
                  <td className="text-small">
                    <span className="badge">{String(row.ref_type || '')}</span>
                    <span className="ml-1 font-mono text-tiny text-text-tertiary">{String(row.ref_id || '').slice(0, 12)}</span>
                  </td>
                  <td className="text-small">
                    {row.model_code ? <span className="font-mono">{String(row.model_code)}</span> : <span className="text-text-tertiary">—</span>}
                    {row.variant_key ? <span className="ml-1 text-tiny text-text-tertiary">· {String(row.variant_key)}</span> : null}
                  </td>
                  <td className="text-small text-text-secondary">#{String(row.upstream_channel_id || '—')}</td>
                  <td className="text-small text-text-secondary">{row.account_id ? `#${row.account_id}` : '—'}</td>
                  <td className="font-mono text-small text-text-secondary">{Number(row.unit_qty || 0).toFixed(2)}</td>
                  <td className="font-mono text-small">{fmtMicroUSD(Number(row.cost_micro_usd || 0))}</td>
                  <td className="font-mono text-small">{fmtMicroCNY(Number(row.sale_micro_cny || 0))}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pager total={total} page={page} pageSize={pageSize} onChange={setPage} onPageSizeChange={setPageSize} sizeOptions={sizeOptions} />
      </Section>
    </>
  );
}

// =====================================================
// Helpers
// =====================================================

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return (
    <div className="fixed inset-0 z-[80] grid place-items-center bg-black/40 p-4 backdrop-blur-sm">
      <div className="dialog-surface klein-fade-in w-full max-w-3xl">
        <header className="flex h-12 items-center justify-between border-b border-border px-5">
          <h3 className="font-semibold text-text-primary">{title}</h3>
          <button className="btn btn-ghost btn-icon btn-sm" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </header>
        <div className="max-h-[80vh] overflow-y-auto p-5">{children}</div>
      </div>
    </div>
  );
}

function Field({
  label,
  hint,
  className,
  children,
}: {
  label: string;
  hint?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <label className={`field ${className || ''}`}>
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

