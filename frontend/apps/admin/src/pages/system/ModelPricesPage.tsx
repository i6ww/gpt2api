import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { BadgeDollarSign, Plus, RefreshCw, Save, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';

import { ApiError } from '../../lib/api';
import { accountsApi, systemApi } from '../../lib/services';
import type { AccountItem } from '../../lib/types';
import { toast } from '../../stores/toast';
import { PageHeader, PageShell, Section } from '../../components/layout/PageShell';

interface PriceRow {
  model_code: string;
  name: string;
  kind: 'text' | 'image' | 'video' | 'music';
  provider: 'gpt' | 'grok' | 'adobe' | 'pic2api' | 'flowmusic' | string;
  upstream_model: string;
  unit_points: number;
  input_unit_points?: number;
  output_unit_points?: number;
  video_pricing_mode?: 'scaled' | 'flat' | 'variant';
  /**
   * image_pricing: 1K/2K/4K → 点（admin UI 用「点」展示；保存时 ×100）。
   * 留空或所有档位都是 0 表示按 unit_points 单价计费。
   */
  image_pricing?: { '1k'?: number; '2k'?: number; '4k'?: number };
  /**
   * video_pricing: 各档时长（秒）→ 点。video_pricing_mode = 'variant' 时优先用这套；
   * 不设值时退到 'scaled' 倍率。各模型档位不同：
   *   sora            → 4 / 8 / 12
   *   veo3.1 / veo3.1-flash / veo3.1-lite → 4 / 6 / 8
   *   grok-imagine-video → 6 / 10 / 20 / 30
   * 后端把它当作 map[string]int64 处理，前端用 union 类型包容所有档位。
   */
  video_pricing?: Partial<Record<string, number>>;
  enabled: boolean;
}

const IMAGE_TIERS: Array<'1k' | '2k' | '4k'> = ['1k', '2k', '4k'];
// VIDEO_ALL_DURATIONS：所有模型可能用到的视频时长档（union）。loadVariantMap 用它来过滤合法 key，
// 避免丢失 sora 的 12s 或 veo* 的 4s 档；UI 渲染时按 model_code 用 videoDurationsFor 选实际档位。
const VIDEO_ALL_DURATIONS: string[] = ['4', '6', '8', '10', '12', '15', '20', '30'];

// videoDurationsFor 给某个模型返回它实际支持的时长档（admin UI 渲染用，不影响保存）。
function videoDurationsFor(modelCode: string): string[] {
  const code = (modelCode || '').toLowerCase();
  // 官方 xAI grok-imagine-video（带 xai/ 前缀）：上游只接受 1-15 秒，三档。
  if (code.startsWith('xai/grok-imagine')) {
    return ['6', '10', '15'];
  }
  if (code === 'sora' || code.startsWith('sora2') || code.startsWith('sora-2')) {
    return ['4', '8', '12'];
  }
  if (code.startsWith('veo3.1') || code.startsWith('veo-3.1') || code.startsWith('veo31')) {
    return ['4', '6', '8'];
  }
  // grok-imagine-video / vid-i2v / 默认四档
  return ['6', '10', '20', '30'];
}

const DEFAULT_ROWS: PriceRow[] = [
  { model_code: 'gpt-5.4', name: 'GPT 5.4', kind: 'text', provider: 'gpt', upstream_model: 'gpt-5.4', unit_points: 0, input_unit_points: 2, output_unit_points: 6, enabled: true },
  { model_code: 'gpt-5.4-mini', name: 'GPT 5.4 Mini', kind: 'text', provider: 'gpt', upstream_model: 'gpt-5.4-mini', unit_points: 0, input_unit_points: 1, output_unit_points: 3, enabled: true },
  { model_code: 'gpt-5.3-codex', name: 'GPT 5.3 Codex', kind: 'text', provider: 'gpt', upstream_model: 'gpt-5.3-codex', unit_points: 0, input_unit_points: 1.5, output_unit_points: 4.5, enabled: true },
  { model_code: 'gpt-image-2', name: 'GPT Image 2', kind: 'image', provider: 'gpt', upstream_model: 'gpt-image-2', unit_points: 4, image_pricing: { '1k': 4, '2k': 15, '4k': 30 }, enabled: true },
  { model_code: 'nano-banana', name: 'Nano Banana', kind: 'image', provider: 'adobe', upstream_model: 'firefly-nano-banana', unit_points: 15, image_pricing: { '1k': 8, '2k': 15, '4k': 30 }, enabled: true },
  { model_code: 'nano-banana-v2', name: 'Nano Banana V2', kind: 'image', provider: 'adobe', upstream_model: 'firefly-nano-banana2', unit_points: 15, image_pricing: { '1k': 8, '2k': 15, '4k': 30 }, enabled: true },
  { model_code: 'nano-banana-pro', name: 'Nano Banana Pro', kind: 'image', provider: 'adobe', upstream_model: 'firefly-nano-banana-pro', unit_points: 30, image_pricing: { '1k': 15, '2k': 30, '4k': 60 }, enabled: true },
  { model_code: 'grok-imagine-video', name: 'Grok Imagine 视频', kind: 'video', provider: 'grok', upstream_model: 'grok-imagine-video', unit_points: 20, video_pricing_mode: 'variant', video_pricing: { '6': 15, '10': 25, '20': 50, '30': 75 }, enabled: true },
  { model_code: 'vid-i2v', name: 'Grok 图生视频', kind: 'video', provider: 'grok', upstream_model: 'grok-imagine-video', unit_points: 20, video_pricing_mode: 'variant', video_pricing: { '6': 20, '10': 30, '20': 60, '30': 90 }, enabled: true },
  { model_code: 'xai/grok-imagine-video', name: '官方GROK视频(xAI)', kind: 'video', provider: 'xai', upstream_model: 'grok-imagine-video', unit_points: 0.12, video_pricing_mode: 'variant', video_pricing: { '6': 0.12, '10': 0.2, '15': 0.3 }, enabled: true },
];

function loadVariantMap<T extends string>(
  raw: unknown,
  keys: readonly T[],
): Partial<Record<T, number>> | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const src = raw as Record<string, unknown>;
  const out: Partial<Record<T, number>> = {};
  let touched = false;
  for (const k of keys) {
    const v = src[k] ?? src[k.toUpperCase()] ?? src[k.toLowerCase()];
    if (typeof v === 'number' && Number.isFinite(v)) {
      out[k] = v / 100;
      touched = true;
    }
  }
  return touched ? out : undefined;
}

function fromValue(v: unknown): PriceRow[] {
  if (Array.isArray(v)) {
    return v.map((r) => {
      const row = r as Partial<PriceRow> & { image_pricing?: Record<string, number>; video_pricing?: Record<string, number> };
      const kind =
        row.kind === 'text'
          ? 'text'
          : row.kind === 'video'
            ? 'video'
            : row.kind === 'music'
              ? 'music'
              : 'image';
      const videoPricing = loadVariantMap(row.video_pricing, VIDEO_ALL_DURATIONS);
      const mode = row.video_pricing_mode === 'flat'
        ? 'flat'
        : row.video_pricing_mode === 'variant' || (kind === 'video' && videoPricing)
          ? 'variant'
          : 'scaled';
      return {
        model_code: String(row.model_code || ''),
        name: String(row.name || ''),
        kind,
        provider: String(row.provider || 'gpt'),
        upstream_model: String(row.upstream_model || ''),
        unit_points: Number(row.unit_points || 0) / 100,
        input_unit_points: Number(row.input_unit_points || 0) / 100,
        output_unit_points: Number(row.output_unit_points || 0) / 100,
        video_pricing_mode: mode,
        image_pricing: kind === 'image' ? loadVariantMap(row.image_pricing, IMAGE_TIERS) : undefined,
        video_pricing: kind === 'video' ? videoPricing : undefined,
        enabled: row.enabled !== false,
      };
    });
  }
  if (v && typeof v === 'object') {
    return Object.entries(v as Record<string, number>).map(([model_code, price]) => ({
      model_code,
      name: model_code,
      kind: model_code.startsWith('vid') ? 'video' : model_code.startsWith('gpt') ? 'text' : 'image',
      provider: model_code.startsWith('vid') ? 'grok' : 'gpt',
      upstream_model: model_code,
      unit_points: Number(price || 0) / 100,
      input_unit_points: model_code.startsWith('gpt') ? Number(price || 0) / 100 : 0,
      output_unit_points: model_code.startsWith('gpt') ? Number(price || 0) / 100 : 0,
      video_pricing_mode: model_code.startsWith('vid') ? 'scaled' : undefined,
      enabled: true,
    }));
  }
  return DEFAULT_ROWS;
}

function variantMapToSave<T extends string>(map: Partial<Record<T, number>> | undefined): Record<string, number> | undefined {
  if (!map) return undefined;
  const out: Record<string, number> = {};
  let touched = false;
  for (const [k, v] of Object.entries(map)) {
    const num = Number(v);
    if (Number.isFinite(num) && num > 0) {
      out[k] = Math.round(num * 100);
      touched = true;
    }
  }
  return touched ? out : undefined;
}

function providerLabel(provider: string): string {
  switch (provider) {
    case 'grok':
      return 'Grok';
    case 'xai':
      return '官方GROK(xAI)';
    case 'adobe':
      return 'Adobe Firefly';
    case 'pic2api':
      return 'Pic2API';
    case 'flowmusic':
      return 'FlowMusic';
    default:
      return 'GPT / OpenAI';
  }
}

function normalizeProvider(provider: string): 'gpt' | 'grok' | 'xai' | 'adobe' | 'pic2api' | 'flowmusic' {
  if (provider === 'grok') return 'grok';
  if (provider === 'xai') return 'xai';
  if (provider === 'adobe') return 'adobe';
  if (provider === 'pic2api') return 'pic2api';
  if (provider === 'flowmusic') return 'flowmusic';
  return 'gpt';
}

function collectSupportedModels(accounts: AccountItem[]): Record<string, string[]> {
  const grouped = new Map<string, Set<string>>();
  for (const account of accounts) {
    if (account.auth_type !== 'api_key') continue;
    const provider = normalizeProvider(account.provider);
    const bucket = grouped.get(provider) || new Set<string>();
    for (const model of account.supported_models || []) {
      const value = model.trim();
      if (value) bucket.add(value);
    }
    grouped.set(provider, bucket);
  }
  return Object.fromEntries(
    [...grouped.entries()].map(([provider, values]) => [provider, [...values].sort((a, b) => a.localeCompare(b))]),
  );
}

export default function ModelPricesPage() {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ['admin', 'system', 'settings'], queryFn: () => systemApi.get() });
  const accounts = useQuery({
    queryKey: ['admin', 'model-prices', 'accounts'],
    queryFn: () => accountsApi.list({ page: 1, page_size: 1000 }),
  });
  const [rows, setRows] = useState<PriceRow[]>(DEFAULT_ROWS);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (settings.data) {
      setRows(fromValue(settings.data['billing.model_prices']));
      setDirty(false);
    }
  }, [settings.data]);

  const supportedModelsByProvider = useMemo(
    () => collectSupportedModels(accounts.data?.list ?? []),
    [accounts.data?.list],
  );

  const update = (idx: number, patch: Partial<PriceRow>) => {
    setRows((old) => old.map((row, i) => (i === idx ? { ...row, ...patch } : row)));
    setDirty(true);
  };

  const removeRow = (idx: number) => {
    setRows((old) => old.filter((_, i) => i !== idx));
    setDirty(true);
  };

  const addRow = () => {
    setRows((old) => [
      ...old,
      {
        model_code: '',
        name: '',
        kind: 'image',
        provider: 'gpt',
        upstream_model: '',
        unit_points: 0,
        input_unit_points: 0,
        output_unit_points: 0,
        video_pricing_mode: 'variant',
        image_pricing: undefined,
        video_pricing: undefined,
        enabled: true,
      },
    ]);
    setDirty(true);
  };

  const updateImageVariant = (idx: number, tier: '1k' | '2k' | '4k', value: number) => {
    setRows((old) => old.map((row, i) => {
      if (i !== idx) return row;
      const prev = row.image_pricing || {};
      const next = { ...prev, [tier]: value };
      return { ...row, image_pricing: next };
    }));
    setDirty(true);
  };

  const updateVideoVariant = (idx: number, dur: string, value: number) => {
    setRows((old) => old.map((row, i) => {
      if (i !== idx) return row;
      const prev = row.video_pricing || {};
      const next = { ...prev, [dur]: value };
      return { ...row, video_pricing: next };
    }));
    setDirty(true);
  };

  const save = useMutation({
    mutationFn: () =>
      systemApi.update({
        'billing.model_prices': rows.map((row) => {
          const imagePricing = row.kind === 'image' ? variantMapToSave(row.image_pricing) : undefined;
          const videoPricing = row.kind === 'video' ? variantMapToSave(row.video_pricing) : undefined;
          return {
            ...row,
            model_code: row.model_code.trim(),
            name: row.name.trim(),
            provider: row.provider.trim(),
            upstream_model: row.upstream_model.trim(),
            unit_points: Math.round((Number(row.unit_points) || 0) * 100),
            input_unit_points: Math.round((Number(row.input_unit_points) || 0) * 100),
            output_unit_points: Math.round((Number(row.output_unit_points) || 0) * 100),
            video_pricing_mode: row.kind === 'video' ? row.video_pricing_mode || 'scaled' : '',
            image_pricing: imagePricing,
            video_pricing: videoPricing,
          };
        }),
      }),
    onSuccess: () => {
      toast.success('模型价格已保存');
      setDirty(false);
      qc.invalidateQueries({ queryKey: ['admin', 'system'] });
    },
    onError: (e: ApiError) => toast.error(e.message),
  });

  const refreshAll = () => {
    settings.refetch();
    accounts.refetch();
  };

  const gptCount = supportedModelsByProvider.gpt?.length || 0;
  const grokCount = supportedModelsByProvider.grok?.length || 0;

  return (
    <PageShell>
      <PageHeader
        icon={<BadgeDollarSign size={16} />}
        title="模型价格"
        right={
          <>
            <button
              className="btn btn-outline btn-sm"
              onClick={refreshAll}
              disabled={settings.isFetching || accounts.isFetching}
            >
              <RefreshCw
                size={14}
                className={settings.isFetching || accounts.isFetching ? 'animate-spin' : ''}
              />{' '}
              重载
            </button>
            <button className="btn btn-outline btn-sm" onClick={addRow}>
              <Plus size={14} /> 添加
            </button>
            <button
              className="btn btn-primary btn-sm"
              onClick={() => save.mutate()}
              disabled={!dirty || save.isPending}
            >
              <Save size={14} /> {save.isPending ? '保存中…' : dirty ? '保存' : '已最新'}
            </button>
          </>
        }
      />

      <div className="rounded-md border border-border bg-surface-1 px-3 py-2 text-tiny text-text-tertiary">
        已同步：GPT/OpenAI {gptCount} 个 · Grok {grokCount} 个 ·
        <code className="kbd mx-1">upstream_model</code>
        会优先提示"上游 API 管理"里已经同步到的模型
      </div>

      <Section title="模型清单" bodyClass="p-0">
        {settings.isLoading ? (
          <div className="p-6 text-center text-text-tertiary">加载中…</div>
        ) : (
          <>
            <div className="space-y-2 p-3 md:hidden">
              {rows.map((row, idx) => {
                const provider = normalizeProvider(row.provider);
                return (
                  <div key={`${row.model_code}-${idx}`} className="rounded-md border border-border bg-surface-2 p-3">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="truncate text-small font-semibold text-text-primary">
                          {row.name || '未命名模型'}
                        </div>
                        <div className="mt-0.5 flex items-center gap-2 font-mono text-tiny text-text-tertiary">
                          <span>{row.model_code || '—'}</span>
                          <span>·</span>
                          <span>{providerLabel(provider)}</span>
                        </div>
                      </div>
                      <span
                        className={
                          'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                          (row.enabled ? 'bg-success-soft text-success' : 'bg-surface-3 text-text-tertiary')
                        }
                      >
                        {row.enabled ? '启用' : '停用'}
                      </span>
                    </div>
                    <div className="mt-2 grid grid-cols-2 gap-1.5 text-tiny">
                      <Cell label="类型" value={kindLabel(row.kind)} />
                      <Cell label="单价（点）" value={row.unit_points || 0} />
                      <Cell label="上游模型" value={row.upstream_model || '—'} colSpan={2} />
                      {row.kind === 'text' && (
                        <>
                          <Cell label="输入（/百万 token）" value={row.input_unit_points || 0} />
                          <Cell label="输出（/百万 token）" value={row.output_unit_points || 0} />
                        </>
                      )}
                      {row.kind === 'image' && (
                        <Cell
                          label="1K / 2K / 4K"
                          value={
                            IMAGE_TIERS
                              .map((tier) => row.image_pricing?.[tier] ?? row.unit_points ?? 0)
                              .join(' / ')
                          }
                          colSpan={2}
                        />
                      )}
                      {row.kind === 'video' && (
                        <>
                          <Cell
                            label="视频计费"
                            value={
                              row.video_pricing_mode === 'flat'
                                ? '固定价'
                                : row.video_pricing_mode === 'variant'
                                  ? '分档单价'
                                  : '按时长倍率'
                            }
                            colSpan={2}
                          />
                          {row.video_pricing_mode === 'variant' && (() => {
                            const durs = videoDurationsFor(row.model_code);
                            return (
                              <Cell
                                label={durs.map((d) => `${d}s`).join(' / ')}
                                value={durs.map((d) => row.video_pricing?.[d] ?? '—').join(' / ')}
                                colSpan={2}
                              />
                            );
                          })()}
                        </>
                      )}
                    </div>
                    <div className="mt-2 flex justify-end gap-1">
                      <button
                        className="btn btn-outline btn-xs"
                        onClick={() => update(idx, { enabled: !row.enabled })}
                      >
                        {row.enabled ? '停用' : '启用'}
                      </button>
                      <button className="btn btn-ghost btn-xs text-danger" onClick={() => removeRow(idx)}>
                        <Trash2 size={12} /> 删除
                      </button>
                    </div>
                  </div>
                );
              })}
              {rows.length === 0 && (
                <div className="rounded-md border border-border bg-surface-2 p-6 text-center text-text-tertiary">
                  暂无模型
                </div>
              )}
            </div>

            <div className="hidden overflow-x-auto md:block">
              <table className="min-w-[1400px] text-small">
                <thead className="border-b border-border bg-surface-2 text-tiny uppercase tracking-wide text-text-tertiary">
                  <tr>
                    <th className="px-2 py-2 text-left">模型编码</th>
                    <th className="px-2 py-2 text-left">显示名称</th>
                    <th className="px-2 py-2 text-left">类型</th>
                    <th className="px-2 py-2 text-left">供应商</th>
                    <th className="px-2 py-2 text-left">上游模型</th>
                    <th className="px-2 py-2 text-left">单价（兜底）</th>
                    <th className="px-2 py-2 text-left">分档价格 / 输入输出</th>
                    <th className="px-2 py-2 text-left">视频模式</th>
                    <th className="px-2 py-2 text-left">状态</th>
                    <th className="px-2 py-2 text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row, idx) => {
                    const provider = normalizeProvider(row.provider);
                    const modelOptions = supportedModelsByProvider[provider] || [];
                    const datalistId = `upstream-models-${idx}`;
                    return (
                      <tr key={`${row.model_code}-${idx}`} className="border-b border-border last:border-0">
                        <td className="px-2 py-1.5">
                          <input
                            className="input input-sm w-32 font-mono"
                            value={row.model_code}
                            onChange={(e) => update(idx, { model_code: e.target.value })}
                          />
                        </td>
                        <td className="px-2 py-1.5">
                          <input
                            className="input input-sm w-32"
                            value={row.name}
                            onChange={(e) => update(idx, { name: e.target.value })}
                          />
                        </td>
                        <td className="px-2 py-1.5">
                          <select
                            className="select select-sm w-20"
                            value={row.kind}
                            onChange={(e) =>
                              update(idx, { kind: e.target.value as PriceRow['kind'] })
                            }
                          >
                            <option value="text">文字</option>
                            <option value="image">图片</option>
                            <option value="video">视频</option>
                            <option value="music">歌曲</option>
                          </select>
                        </td>
                        <td className="px-2 py-1.5">
                          <select
                            className="select select-sm w-28"
                            value={provider}
                            onChange={(e) =>
                              update(idx, { provider: e.target.value, upstream_model: '' })
                            }
                          >
                            <option value="gpt">GPT</option>
                            <option value="grok">Grok</option>
                            <option value="xai">官方GROK(xAI)</option>
                            <option value="adobe">Adobe</option>
                            <option value="pic2api">Pic2API</option>
                            <option value="flowmusic">FlowMusic</option>
                          </select>
                        </td>
                        <td className="px-2 py-1.5">
                          <div>
                            <input
                              className="input input-sm w-48"
                              value={row.upstream_model}
                              list={datalistId}
                              placeholder={modelOptions.length ? '可从已同步选择' : '上游同步后可选'}
                              onChange={(e) => update(idx, { upstream_model: e.target.value })}
                            />
                            <datalist id={datalistId}>
                              {modelOptions.map((model) => (
                                <option key={model} value={model} />
                              ))}
                            </datalist>
                          </div>
                        </td>
                        <td className="px-2 py-1.5">
                          <input
                            className="input input-sm w-20 tabular-nums"
                            type="number"
                            min={0}
                            value={row.unit_points}
                            onChange={(e) => update(idx, { unit_points: Number(e.target.value) || 0 })}
                            disabled={row.kind === 'text'}
                          />
                        </td>
                        <td className="px-2 py-1.5">
                          {row.kind === 'text' && (
                            <div className="flex gap-1">
                              <input
                                className="input input-sm w-16 tabular-nums"
                                type="number"
                                min={0}
                                value={row.input_unit_points || 0}
                                onChange={(e) =>
                                  update(idx, { input_unit_points: Number(e.target.value) || 0 })
                                }
                                placeholder="入"
                                title="输入 / 百万 token"
                              />
                              <input
                                className="input input-sm w-16 tabular-nums"
                                type="number"
                                min={0}
                                value={row.output_unit_points || 0}
                                onChange={(e) =>
                                  update(idx, { output_unit_points: Number(e.target.value) || 0 })
                                }
                                placeholder="出"
                                title="输出 / 百万 token"
                              />
                            </div>
                          )}
                          {row.kind === 'image' && (
                            <div className="flex items-center gap-1">
                              {IMAGE_TIERS.map((tier) => (
                                <label key={tier} className="flex flex-col items-center text-tiny text-text-tertiary">
                                  <span className="mb-0.5">{tier.toUpperCase()}</span>
                                  <input
                                    className="input input-sm w-14 tabular-nums"
                                    type="number"
                                    min={0}
                                    step="0.1"
                                    value={row.image_pricing?.[tier] ?? ''}
                                    placeholder={String(row.unit_points || 0)}
                                    onChange={(e) =>
                                      updateImageVariant(idx, tier, Number(e.target.value) || 0)
                                    }
                                    title={`${tier.toUpperCase()} 单价（点）；留空走兜底单价`}
                                  />
                                </label>
                              ))}
                            </div>
                          )}
                          {row.kind === 'video' && (
                            <div className="flex items-center gap-1">
                              {videoDurationsFor(row.model_code).map((dur) => (
                                <label key={dur} className="flex flex-col items-center text-tiny text-text-tertiary">
                                  <span className="mb-0.5">{dur}s</span>
                                  <input
                                    className="input input-sm w-14 tabular-nums"
                                    type="number"
                                    min={0}
                                    step="0.1"
                                    value={row.video_pricing?.[dur] ?? ''}
                                    placeholder={String(row.unit_points || 0)}
                                    onChange={(e) =>
                                      updateVideoVariant(idx, dur, Number(e.target.value) || 0)
                                    }
                                    disabled={row.video_pricing_mode === 'flat' || row.video_pricing_mode === 'scaled'}
                                    title={
                                      row.video_pricing_mode === 'variant'
                                        ? `${dur} 秒视频单价（点）`
                                        : '切换到「variant 分档」才能改这里'
                                    }
                                  />
                                </label>
                              ))}
                            </div>
                          )}
                        </td>
                        <td className="px-2 py-1.5">
                          {row.kind === 'video' ? (
                            <select
                              className="select select-sm w-28"
                              value={row.video_pricing_mode || 'variant'}
                              onChange={(e) =>
                                update(idx, {
                                  video_pricing_mode: e.target.value as PriceRow['video_pricing_mode'],
                                })
                              }
                              title="variant: 用各档时长（左边输入框可见）的单价；scaled: base × 时长/6；flat: 固定 base"
                            >
                              <option value="variant">分档</option>
                              <option value="scaled">按时长倍率</option>
                              <option value="flat">固定一口价</option>
                            </select>
                          ) : (
                            <span className="text-text-tertiary">—</span>
                          )}
                        </td>
                        <td className="px-2 py-1.5">
                          <button
                            className={
                              'inline-flex items-center rounded px-1.5 py-0.5 text-tiny ' +
                              (row.enabled
                                ? 'bg-success-soft text-success'
                                : 'bg-surface-3 text-text-tertiary')
                            }
                            onClick={() => update(idx, { enabled: !row.enabled })}
                          >
                            {row.enabled ? '启用' : '停用'}
                          </button>
                        </td>
                        <td className="px-2 py-1.5 text-right">
                          <button
                            className="btn btn-ghost btn-icon btn-xs text-danger"
                            onClick={() => removeRow(idx)}
                            title="删除"
                          >
                            <Trash2 size={13} />
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                  {rows.length === 0 && (
                    <tr>
                      <td colSpan={10} className="py-8 text-center text-text-tertiary">
                        暂无模型，点击右上角"添加"
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </>
        )}
      </Section>
    </PageShell>
  );
}

function Cell({ label, value, colSpan = 1 }: { label: string; value: number | string; colSpan?: number }) {
  return (
    <div
      className="rounded bg-surface-1 px-2 py-1"
      style={{ gridColumn: colSpan === 2 ? 'span 2 / span 2' : undefined }}
    >
      <div className="text-text-tertiary">{label}</div>
      <div className="mt-0.5 truncate font-medium text-text-secondary">{value}</div>
    </div>
  );
}

function kindLabel(k: PriceRow['kind']) {
  if (k === 'text') return '文字';
  if (k === 'video') return '视频';
  if (k === 'music') return '歌曲';
  return '图片';
}
