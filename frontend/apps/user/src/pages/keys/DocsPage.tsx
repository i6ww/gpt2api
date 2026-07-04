import type { ReactNode } from 'react';

import { Copy, Loader2 } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';

import { fmtPoints } from '../../lib/format';
import { genApi } from '../../lib/services';
import type { PublicModel } from '../../lib/types';
import { toast } from '../../stores/toast';

const EXAMPLE_BASE =
  typeof window !== 'undefined' && window.location?.origin
    ? `${window.location.origin.replace(/\/$/, '')}/v1`
    : 'https://www.gpt2api.com/v1';

// 能力 / 备注文案（按 model_code 注入；后端没存这块描述，前端维护即可）。
// 这里保留中文/英文双套（按 i18n.language 切换）。
const MODEL_CAPS_ZH: Record<string, string> = {
  'gpt-image-2': '通用图像模型，支持 1K / 2K / 4K 三档；走 ChatGPT Codex 路径，由号池摊销。',
  'nano-banana-pro': '高保真出图，最稳定的版本，建议高质量交付用。',
  'nano-banana-v2': '上一代改进版，速度 / 价格折中。',
  'nano-banana': '基础款，便宜，适合草图 / 批量出图。',
  'grok-4.20-fast': '快速答题，价格低，适合短上下文。',
  'grok-4.20-auto': '自动模式，平衡推理深度与速度。',
  'grok-4.20-expert': '专家档，复杂推理 / 长上下文。',
  'grok-4.20-heavy': '最强档，链式推理 / 代码 / 数学。',
  'grok-4.3-beta': '最新 beta，长上下文 + 工具调用。',
  'grok-imagine-video':
    '文生视频 / 图生视频统一入口：不带参考图 = 文生视频；带 image / images[] / ref_assets[] = 图生视频。支持 6s / 10s / 20s / 30s 四档。',
  lyria: '文生歌曲基础款：根据提示词生成完整歌曲（含封面与歌词），异步出片。',
  'lyria-pro': '文生歌曲高级款：更高音质 / 更长时长，适合正式交付。',
};
const MODEL_CAPS_EN: Record<string, string> = {
  'gpt-image-2': 'General-purpose image model with 1K / 2K / 4K tiers; routed via ChatGPT Codex and amortised across the account pool.',
  'nano-banana-pro': 'High-fidelity outputs, the most stable revision — recommended for production delivery.',
  'nano-banana-v2': 'Previous-gen improved build; balanced cost / speed.',
  'nano-banana': 'Entry tier — cheap; good for drafts and batch generation.',
  'grok-4.20-fast': 'Snappy answers, lowest price; suits short context.',
  'grok-4.20-auto': 'Auto mode balancing reasoning depth and speed.',
  'grok-4.20-expert': 'Expert tier: complex reasoning / long context.',
  'grok-4.20-heavy': 'Top tier: chain-of-thought / code / math.',
  'grok-4.3-beta': 'Latest beta — long context + tool calling.',
  'grok-imagine-video':
    'Unified entry for t2v and i2v: no reference = text-to-video; with image / images[] / ref_assets[] = image-to-video. Supports 6 / 10 / 20 / 30 second tiers.',
  lyria: 'Text-to-song base tier: generates a full song (with cover art and lyrics) from a prompt; async.',
  'lyria-pro': 'Text-to-song pro tier: higher fidelity / longer duration, suited for production delivery.',
};

const IMAGE_TIERS: Array<'1k' | '2k' | '4k'> = ['1k', '2k', '4k'];

// 视频时长档：跟 admin 后台 ModelPricesPage 的 videoDurationsFor 保持一致。
// 不同模型支持的档位不一样：sora 是 4/8/12s、veo3.1 是 4/6/8s、grok-imagine 是 6/10/20/30s。
function videoDurationsFor(modelCode: string): string[] {
  const code = (modelCode || '').toLowerCase();
  if (code === 'sora' || code.startsWith('sora2') || code.startsWith('sora-2')) {
    return ['4', '8', '12'];
  }
  if (code.startsWith('veo3.1') || code.startsWith('veo-3.1') || code.startsWith('veo31')) {
    return ['4', '6', '8'];
  }
  return ['6', '10', '20', '30'];
}

// ============================================================================
// 尺寸表 —— Nano Banana 与 GPT Image 2 用的尺寸白名单完全不一样，必须分开展示。
// note 改成 i18n key（noteKey），由组件渲染时翻译。
// ============================================================================
type SizeTierEntry = {
  tier: string;
  noteKey: string;
  rows: ReadonlyArray<readonly [string, string]>;
};

const BANANA_SIZES: ReadonlyArray<SizeTierEntry> = [
  {
    tier: '1K',
    noteKey: 'docs.size_banana_1k_note',
    rows: [
      ['1:1', '1024x1024'],
      ['3:2', '1264x848'],
      ['2:3', '848x1264'],
      ['4:3', '1152x864'],
      ['3:4', '864x1152'],
      ['5:4', '1152x928'],
      ['4:5', '928x1152'],
      ['16:9', '1376x768'],
      ['9:16', '768x1376'],
      ['21:9', '1584x672'],
    ],
  },
  {
    tier: '2K',
    noteKey: 'docs.size_banana_2k_note',
    rows: [
      ['1:1', '2048x2048'],
      ['3:2', '2528x1696'],
      ['2:3', '1696x2528'],
      ['4:3', '2048x1536'],
      ['3:4', '1536x2048'],
      ['5:4', '2304x1856'],
      ['4:5', '1856x2304'],
      ['16:9', '2752x1536'],
      ['9:16', '1536x2752'],
      ['21:9', '3168x1344'],
    ],
  },
  {
    tier: '4K',
    noteKey: 'docs.size_banana_4k_note',
    rows: [
      ['1:1', '4096x4096'],
      ['3:2', '5056x3392'],
      ['2:3', '3392x5056'],
      ['4:3', '4784x3584'],
      ['3:4', '3584x4784'],
      ['5:4', '4608x3712'],
      ['4:5', '3712x4608'],
      ['16:9', '5504x3072'],
      ['9:16', '3072x5504'],
      ['21:9', '6336x2688'],
    ],
  },
];

const GPT_IMAGE_SIZES: ReadonlyArray<SizeTierEntry> = [
  {
    tier: '1K',
    noteKey: 'docs.size_gpt_1k_note',
    rows: [
      ['1:1', '1024x1024'],
      ['3:2', '1536x1024'],
      ['2:3', '1024x1536'],
      ['4:3', '1152x864'],
      ['3:4', '864x1152'],
      ['5:4', '1120x896'],
      ['4:5', '896x1120'],
      ['16:9', '1280x720'],
      ['9:16', '720x1280'],
      ['21:9', '1456x624'],
    ],
  },
  {
    tier: '2K',
    noteKey: 'docs.size_gpt_2k_note',
    rows: [
      ['1:1', '2048x2048'],
      ['3:2', '2496x1664'],
      ['2:3', '1664x2496'],
      ['4:3', '2304x1728'],
      ['3:4', '1728x2304'],
      ['5:4', '2240x1792'],
      ['4:5', '1792x2240'],
      ['16:9', '2560x1440'],
      ['9:16', '1440x2560'],
      ['21:9', '3024x1296'],
    ],
  },
  {
    tier: '4K',
    noteKey: 'docs.size_gpt_4k_note',
    rows: [
      ['1:1', '2480x2480'],
      ['3:2', '3056x2032'],
      ['2:3', '2032x3056'],
      ['4:3', '2880x2160'],
      ['3:4', '2160x2880'],
      ['5:4', '2784x2224'],
      ['4:5', '2224x2784'],
      ['16:9', '3328x1872'],
      ['9:16', '1872x3328'],
      ['21:9', '3808x1632'],
    ],
  },
];

// ============================================================================
// 接口总览 —— kind / sync / note 三列改成 i18n key
// ============================================================================
type EndpointEntry = {
  method: string;
  path: string;
  kindKey: string;
  syncKey: string;
  noteKey: string;
};

const ENDPOINTS: ReadonlyArray<EndpointEntry> = [
  { method: 'GET',  path: '/v1/models',                       kindKey: 'docs.ep_models',     syncKey: 'docs.sync_sync',         noteKey: 'docs.ep_models_note' },
  { method: 'POST', path: '/v1/chat/completions',             kindKey: 'docs.ep_chat',       syncKey: 'docs.sync_stream',       noteKey: 'docs.ep_chat_note' },
  { method: 'POST', path: '/v1/images/generations',           kindKey: 'docs.ep_img_gen',    syncKey: 'docs.sync_img_async',    noteKey: 'docs.ep_img_gen_note' },
  { method: 'POST', path: '/v1/images/edits',                 kindKey: 'docs.ep_img_edit',   syncKey: 'docs.sync_img_async',    noteKey: 'docs.ep_img_edit_note' },
  { method: 'GET',  path: '/v1/images/generations/:task_id',  kindKey: 'docs.ep_img_poll',   syncKey: 'docs.sync_poll',         noteKey: 'docs.ep_img_poll_note' },
  { method: 'POST', path: '/v1/video/generations',            kindKey: 'docs.ep_video_gen',  syncKey: 'docs.sync_video_async',  noteKey: 'docs.ep_video_gen_note' },
  { method: 'GET',  path: '/v1/video/generations/:task_id',   kindKey: 'docs.ep_video_poll', syncKey: 'docs.sync_poll',         noteKey: 'docs.ep_video_poll_note' },
  { method: 'POST', path: '/v1/videos/generations',           kindKey: 'docs.ep_alias',      syncKey: 'docs.sync_same',         noteKey: 'docs.ep_alias_note_post' },
  { method: 'GET',  path: '/v1/videos/generations/:task_id',  kindKey: 'docs.ep_alias',      syncKey: 'docs.sync_same',         noteKey: 'docs.ep_alias_note_get' },
  { method: 'POST', path: '/v1/music/generations',            kindKey: 'docs.ep_music_gen',  syncKey: 'docs.sync_music_async',  noteKey: 'docs.ep_music_gen_note' },
  { method: 'GET',  path: '/v1/music/generations/:task_id',   kindKey: 'docs.ep_music_poll', syncKey: 'docs.sync_poll',         noteKey: 'docs.ep_music_poll_note' },
];

// ============================================================================
// 视频参数补充 / 任务状态码
// ============================================================================
type VideoNoteEntry = { key: string; valueKey: string };
const VIDEO_NOTES: ReadonlyArray<VideoNoteEntry> = [
  { key: 'duration',                       valueKey: 'docs.video_notes_duration_v' },
  { key: 'ratio',                          valueKey: 'docs.video_notes_ratio_v' },
  { key: 'docs.video_notes_ref_k',         valueKey: 'docs.video_notes_ref_v' },
  { key: 'async',                          valueKey: 'docs.video_notes_async_v' },
  { key: 'quality',                        valueKey: 'docs.video_notes_quality_v' },
];

type TaskStatusEntry = { code: number; label: string; descKey: string };
const TASK_STATUSES: ReadonlyArray<TaskStatusEntry> = [
  { code: 0, label: 'queued',    descKey: 'docs.task_status_pending_desc' },
  { code: 1, label: 'running',   descKey: 'docs.task_status_running_desc' },
  { code: 2, label: 'succeeded', descKey: 'docs.task_status_completed_desc' },
  { code: 3, label: 'failed',    descKey: 'docs.task_status_failed_desc' },
  { code: 4, label: 'refunded',  descKey: 'docs.task_status_refunded_desc' },
];

// ============================================================================
// 代码示例（注释保留双语 # 不翻译，开发者阅读更友好）
// ============================================================================
const TEXT_CHAT_SAMPLE = String.raw`curl ${EXAMPLE_BASE}/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4.20-fast",
    "messages": [
      { "role": "system", "content": "You are a helpful assistant." },
      { "role": "user",   "content": "Write a one-liner product slogan." }
    ],
    "temperature": 0.7,
    "max_tokens": 512,
    "stream": false
  }'`;

const TEXT_STREAM_SAMPLE = String.raw`curl ${EXAMPLE_BASE}/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4.20-heavy",
    "messages": [{ "role": "user", "content": "Implement quicksort in Python." }],
    "stream": true
  }'
# Standard OpenAI SSE: data: {...}\n\n  ...  data: [DONE]\n\n`;

const IMG_T2I_SAMPLE = String.raw`# Text-to-image: 1024² Banana (optional callback_url for webhook)
curl ${EXAMPLE_BASE}/images/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "nano-banana-pro",
    "prompt": "A minimalist product ad with a fried chicken bucket on a clean white podium.",
    "n": 1,
    "size": "1024x1024",
    "quality": "1k",
    "async": true,
    "callback_url": "https://your-server.com/webhooks/gpt2api"
  }'`;

const IMG_T2I_GPT_SAMPLE = String.raw`# Text-to-image: GPT Image 2
curl ${EXAMPLE_BASE}/images/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "ultra detailed editorial poster",
    "size": "1280x720",
    "quality": "1k",
    "async": true
  }'`;

const IMG_I2I_SAMPLE = String.raw`# Image-to-image: with reference (URL or data URL both work)
curl ${EXAMPLE_BASE}/images/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "nano-banana-v2",
    "prompt": "Keep the subject identity but restyle to a cyberpunk neon street.",
    "image": "https://example.com/portrait.jpg",
    "size": "1536x2048",
    "quality": "2k",
    "async": true
  }'
# async=true returns { task_id }; then GET /v1/images/generations/:task_id to poll`;

const IMG_EDIT_SAMPLE = String.raw`# /v1/images/edits: requires a reference image — good for backdrop swap / inpainting
curl ${EXAMPLE_BASE}/images/edits \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "nano-banana-pro",
    "prompt": "Keep the subject; change background to a clean studio with soft light.",
    "image": "https://example.com/input.png",
    "n": 1,
    "size": "2048x1536",
    "quality": "2k",
    "async": true
  }'`;

const IMG_POLL_SAMPLE = String.raw`# Poll an image task (use after async=true)
curl -i ${EXAMPLE_BASE}/images/generations/29cfd6bc5d164d0aa07d20db25 \
  -H "Authorization: Bearer sk-xxx"
# While running: JSON has retry_after (seconds) + Retry-After header — wait that long before next poll.
# When succeeded: result.data[0].url is a full https://... link (not a relative path).`;

const IMG_POLL_RESPONSE_SAMPLE = String.raw`# Example: running
{
  "id": "29cfd6bc5d164d0aa07d20db25",
  "task_id": "29cfd6bc5d164d0aa07d20db25",
  "object": "image.generation.task",
  "status": "running",
  "progress": 5,
  "retry_after": 2,
  "model": "gpt-image-2",
  "usage": { "total_cost": 8, "total_points": 0.08 }
}

# Example: succeeded
{
  "status": "succeeded",
  "progress": 100,
  "result": {
    "data": [
      {
        "url": "https://www.gpt2api.com/api/v1/gen/cached/generated/2026/05/27/29cfd6bc5d164d0aa07d20db25_0.png",
        "width": 1024,
        "height": 1024
      }
    ],
    "usage": { "total_cost": 8, "total_points": 0.08 }
  }
}`;

const VID_T2V_SAMPLE = String.raw`# Text-to-video: duration supports 6 / 10 / 20 / 30 seconds
curl ${EXAMPLE_BASE}/video/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-video",
    "prompt": "Sunset on a snowy mountain ridge, cinematic.",
    "duration": 10,
    "ratio": "16:9",
    "async": true,
    "callback_url": "https://your-server.com/webhooks/gpt2api"
  }'`;

const VID_I2V_SAMPLE = String.raw`# Image-to-video: with reference image (image / images[] / ref_assets[])
curl ${EXAMPLE_BASE}/video/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-video",
    "prompt": "Camera dolly-in to the character; subtle wind in the hair.",
    "image": "https://example.com/portrait.jpg",
    "duration": 10,
    "async": true
  }'`;

const VID_LONG_SAMPLE = String.raw`# 20s / 30s long video — backend auto-stitches segments
curl ${EXAMPLE_BASE}/video/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-video",
    "prompt": "A drone flight over the city at dusk, golden hour.",
    "duration": 30,
    "ratio": "16:9",
    "async": true
  }'`;

const VID_POLL_SAMPLE = String.raw`# Poll a video task
curl -i ${EXAMPLE_BASE}/video/generations/bd5c2cd8911e44c1b6175921c3 \
  -H "Authorization: Bearer sk-xxx"
# Same retry_after / Retry-After rules as image polling.
# When succeeded: result.data[0].url = mp4 (https://...), cover_url = poster jpg if any.`;

const VID_POLL_RESPONSE_SAMPLE = String.raw`# Example: succeeded
{
  "status": "succeeded",
  "progress": 100,
  "result": {
    "object": "video.generation",
    "data": [
      {
        "url": "https://www.gpt2api.com/api/v1/gen/cached/generated/2026/05/27/bd5c2cd8911e44c1b6175921c3_0.mp4",
        "width": 1920,
        "height": 1080,
        "duration_ms": 8000
      }
    ],
    "usage": { "total_cost": 50, "total_points": 0.5 }
  }
}`;

const MUSIC_T2A_SAMPLE = String.raw`# Text-to-song: generate a full track from a prompt
curl ${EXAMPLE_BASE}/music/generations \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "lyria",
    "prompt": "An epic cinematic orchestral track with soaring strings, building to a triumphant climax.",
    "async": true,
    "callback_url": "https://your-server.com/webhooks/gpt2api"
  }'
# async=true returns { task_id }; then GET /v1/music/generations/:task_id to poll`;

const MUSIC_POLL_SAMPLE = String.raw`# Poll a music task
curl -i ${EXAMPLE_BASE}/music/generations/439a89940df24e7baa7978980d \
  -H "Authorization: Bearer sk-xxx"
# Same retry_after / Retry-After rules as image/video polling.
# When succeeded: result.data[0].url = audio (mp3, https://...), cover_url = cover art.`;

const MUSIC_POLL_RESPONSE_SAMPLE = String.raw`# Example: succeeded
{
  "status": "succeeded",
  "progress": 100,
  "result": {
    "object": "music.generation",
    "data": [
      {
        "url": "https://www.gpt2api.com/api/v1/m/ItOUioFwT0u_2F6MTwBqHpbnAJUeSjBFIfqwxxc.mp3",
        "cover_url": "https://www.gpt2api.com/api/v1/m/Q5qJlA3yTnuqeXiYDQBqHpO-AXPfYSjkan8UaZw.jpg",
        "duration_ms": 120000,
        "title": "Legend's Rise",
        "lyrics": "[Instrumental]"
      }
    ],
    "usage": { "total_cost": 20, "total_points": 0.2 }
  }
}`;

const WEBHOOK_SAMPLE = String.raw`# Webhook POST body (same shape as poll response + event field)
{
  "event": "generation.succeeded",
  "id": "29cfd6bc5d164d0aa07d20db25",
  "task_id": "29cfd6bc5d164d0aa07d20db25",
  "object": "image.generation.task",
  "status": "succeeded",
  "progress": 100,
  "model": "gpt-image-2",
  "kind": "image",
  "result": {
    "data": [{ "url": "https://www.gpt2api.com/api/v1/gen/cached/...", "width": 1024, "height": 1024 }]
  },
  "usage": { "total_cost": 8, "total_points": 0.08 }
}
# Failures send event=generation.failed with error.message. HTTPS only; private IPs blocked.`;

const PYTHON_SAMPLE = String.raw`# pip install openai
from openai import OpenAI

client = OpenAI(
    base_url="${EXAMPLE_BASE}",
    api_key="sk-xxx",
)

resp = client.chat.completions.create(
    model="grok-4.20-fast",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)
`;

const JS_SAMPLE = String.raw`// npm i openai
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: '${EXAMPLE_BASE}',
  apiKey: 'sk-xxx',
});

const resp = await client.chat.completions.create({
  model: 'grok-4.20-fast',
  messages: [{ role: 'user', content: 'Hello' }],
});
console.log(resp.choices[0].message.content);
`;

// ============================================================================
// 组件
// ============================================================================
export default function DocsPage() {
  const { t, i18n } = useTranslation();
  const isZh = (i18n.language || '').toLowerCase().startsWith('zh');
  const modelCaps = isZh ? MODEL_CAPS_ZH : MODEL_CAPS_EN;

  const copy = async (s: string, label: string) => {
    await navigator.clipboard.writeText(s);
    toast.success(`${label} ${t('common.copied')}`);
  };

  const modelsQuery = useQuery({
    queryKey: ['docs.models'],
    queryFn: () => genApi.models(),
    staleTime: 60_000,
  });
  const allModels = modelsQuery.data ?? [];
  const imageModels = allModels.filter((m) => m.enabled !== false && m.kind === 'image');
  const textModels = allModels.filter((m) => m.enabled !== false && m.kind === 'text');
  const videoModels = allModels.filter((m) => m.enabled !== false && m.kind === 'video');
  const musicModels = allModels.filter((m) => m.enabled !== false && m.kind === 'music');

  return (
    <div className="page space-y-4">
      <header className="page-header">
        <div>
          <h1 className="page-title">{t('docs.title')}</h1>
          <p className="page-subtitle leading-loose">
            {t('docs.subtitle_lead')} <span className="gradient-text">{t('docs.subtitle_strong')}</span>
            {t('docs.subtitle_tail')}
            <code className="kbd mx-1">base_url</code>
            {t('docs.subtitle_tail2')}
          </p>
        </div>
      </header>

      <div className="grid gap-4 xl:grid-cols-[1.1fr_0.9fr]">
        <DocSection title={t('docs.endpoint_url')} actionLabel={t('docs.copy')} onCopy={() => copy(EXAMPLE_BASE, t('docs.endpoint_url'))}>
          <div className="rounded-md border border-border bg-surface-2 p-4 font-mono text-body break-all">
            {EXAMPLE_BASE}
          </div>
          <p className="mt-3 text-small leading-loose text-text-tertiary">
            {t('docs.auth_intro')}
            <code className="kbd mx-1">Authorization: Bearer sk-xxx</code>
            {t('docs.auth_after_kbd')}
            <code className="kbd mx-1">{t('docs.auth_kbd_keys')}</code>
            {t('docs.auth_after_keys')}
            <strong>{t('docs.auth_strong')}</strong>
            {t('docs.auth_tail')}
            <code className="kbd mx-1">Idempotency-Key</code>
            {t('docs.auth_after_idem')}
          </p>
        </DocSection>

        <DocSection title={t('docs.tip_title')} actionLabel={t('docs.copy_text_btn')} onCopy={() => copy(TEXT_CHAT_SAMPLE, t('docs.py_title'))}>
          <ul className="space-y-2 text-sm leading-7 text-text-secondary">
            <li>{t('docs.tip_text')}<code className="kbd">stream=true</code>{t('docs.tip_text_tail')}</li>
            <li>{t('docs.tip_image')}<code className="kbd">async=true</code>{t('docs.tip_image_tail')}</li>
            <li>{t('docs.tip_video')}<code className="kbd">async=false</code>{t('docs.tip_video_tail')}</li>
            <li>{t('docs.tip_poll')}<code className="kbd">retry_after</code>{t('docs.tip_poll_mid')}<code className="kbd">Retry-After</code>{t('docs.tip_poll_tail')}</li>
            <li>{t('docs.tip_callback')}<code className="kbd">callback_url</code>{t('docs.tip_callback_tail')}</li>
            <li>{t('docs.tip_url')}</li>
            <li>{t('docs.tip_i2i')}<code className="kbd">image / images[] / ref_assets[]</code>{t('docs.tip_i2i_tail')}</li>
            <li>{t('docs.tip_billing')}</li>
            <li>{t('docs.tip_fail')}<code className="kbd">status=failed</code>{t('docs.tip_fail_mid')}<code className="kbd">error</code>{t('docs.tip_fail_tail')}</li>
          </ul>
        </DocSection>
      </div>

      {/* ========================= 模型清单 ========================= */}
      <DocSection
        title={t('docs.models_title')}
        actionLabel={t('docs.copy_list_btn')}
        onCopy={() => copy(
          allModels.map((m) => `${m.model_code}\t${m.kind}\t${m.name}`).join('\n'),
          t('docs.models_copy_label'),
        )}
      >
        <p className="mb-3 text-small leading-7 text-text-tertiary">
          {t('docs.models_intro_prefix')}
          <strong>{t('docs.models_intro_strong')}</strong>
          {t('docs.models_intro_after')}
          <strong>{t('docs.models_intro_tier')}</strong>
          {t('docs.models_intro_after2')}
          <strong>{t('docs.models_intro_token')}</strong>
          {t('docs.models_intro_after3')}
        </p>

        {modelsQuery.isLoading ? (
          <div className="flex items-center gap-2 text-text-tertiary">
            <Loader2 size={16} className="animate-spin" /> {t('docs.models_loading')}
          </div>
        ) : modelsQuery.isError ? (
          <div className="rounded-md border border-warning/30 bg-warning-soft p-3 text-small leading-7 text-warning">
            {t('docs.models_load_failed_prefix')}
            <code className="kbd mx-1">GET /v1/models</code>
            {t('docs.models_load_failed_after')}
          </div>
        ) : (
          <>
            <ImageModelTable title={t('docs.models_image_title')} rows={imageModels} caps={modelCaps} t={t} />
            <div className="mt-4" />
            <TextModelTable title={t('docs.models_text_title')} rows={textModels} caps={modelCaps} t={t} />
            <div className="mt-4" />
            <VideoModelTable title={t('docs.models_video_title')} rows={videoModels} caps={modelCaps} t={t} />
            <div className="mt-4" />
            <MusicModelTable title={t('docs.models_music_title')} rows={musicModels} caps={modelCaps} t={t} />
          </>
        )}
      </DocSection>

      {/* ========================= 接口总览 ========================= */}
      <DocSection title={t('docs.endpoints_title')} actionLabel={t('docs.copy_path_btn')} onCopy={() => copy(ENDPOINTS.map((item) => `${item.method} ${item.path}`).join('\n'), t('docs.endpoints_copy_label'))}>
        <div className="overflow-x-auto">
          <table className="min-w-full border-separate border-spacing-0 text-sm">
            <thead>
              <tr className="text-left text-text-tertiary">
                <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_method')}</th>
                <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_path')}</th>
                <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_kind')}</th>
                <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_sync')}</th>
                <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_note')}</th>
              </tr>
            </thead>
            <tbody>
              {ENDPOINTS.map((item) => (
                <tr key={`${item.method}-${item.path}`} className="align-top">
                  <td className="border-b border-border px-3 py-3 font-mono text-klein-600">{item.method}</td>
                  <td className="border-b border-border px-3 py-3 font-mono">{item.path}</td>
                  <td className="border-b border-border px-3 py-3">{t(item.kindKey)}</td>
                  <td className="border-b border-border px-3 py-3 text-text-secondary">{t(item.syncKey)}</td>
                  <td className="border-b border-border px-3 py-3 text-text-tertiary">{t(item.noteKey)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </DocSection>

      {/* ========================= 文本 ========================= */}
      <DocSection title={t('docs.text_section_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(TEXT_CHAT_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.text_section_intro_prefix')}
          <code className="kbd">model</code> / <code className="kbd">messages</code> /{' '}
          <code className="kbd">temperature</code> / <code className="kbd">max_tokens</code> /{' '}
          <code className="kbd">top_p</code> / <code className="kbd">stream</code>
          {t('docs.text_section_intro_after')}
        </p>
        <CodeBlock>{TEXT_CHAT_SAMPLE}</CodeBlock>
        <p className="mb-2 mt-4 text-sm leading-7 text-text-secondary">{t('docs.text_stream_label')}</p>
        <CodeBlock>{TEXT_STREAM_SAMPLE}</CodeBlock>
      </DocSection>

      {/* ========================= 图片 ========================= */}
      <DocSection title={t('docs.img_t2i_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(IMG_T2I_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.img_t2i_intro_prefix')}
          <code className="kbd">model</code>、<code className="kbd">prompt</code>、{' '}
          <code className="kbd">n</code>（{t('docs.img_t2i_intro_n')}）、<code className="kbd">size</code>（{t('docs.img_t2i_intro_size')}）、{' '}
          <code className="kbd">quality</code>（{t('docs.img_t2i_intro_q')}：<code className="kbd">1k</code> / <code className="kbd">2k</code> /{' '}
          <code className="kbd">4k</code>，<strong>{t('docs.img_t2i_intro_quality_prio')}</strong>{t('docs.img_t2i_intro_quality_after')}）、{' '}
          <code className="kbd">async</code>（{t('docs.img_t2i_intro_async')}）、{' '}
          <code className="kbd">callback_url</code>（{t('docs.img_t2i_intro_callback')}）。
        </p>
        <CodeBlock>{IMG_T2I_SAMPLE}</CodeBlock>
        <p className="mb-2 mt-4 text-sm leading-7 text-text-secondary">{t('docs.img_t2i_gpt_label')}</p>
        <CodeBlock>{IMG_T2I_GPT_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.img_i2i_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(IMG_I2I_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.img_i2i_intro_prefix')}
          <code className="kbd">image</code>（{t('docs.img_i2i_intro_single')}）、<code className="kbd">images[]</code> {t('docs.img_i2i_intro_or')}{' '}
          <code className="kbd">ref_assets[]</code>（{t('docs.img_i2i_intro_multi')}）{t('docs.img_i2i_intro_url_or')}
          <code className="kbd">data:image/...;base64,...</code>{t('docs.img_i2i_intro_data_url')}
        </p>
        <CodeBlock>{IMG_I2I_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.img_edit_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(IMG_EDIT_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.img_edit_intro_prefix')}
          <code className="kbd">/edits</code>{t('docs.img_edit_intro_force_ref')}
        </p>
        <CodeBlock>{IMG_EDIT_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.img_poll_title')} actionLabel={t('docs.copy_sample_btn')} onCopy={() => copy(IMG_POLL_SAMPLE, t('docs.copy_sample_btn'))}>
        <CodeBlock>{IMG_POLL_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">{t('docs.img_poll_intro')}</p>
        <p className="mb-2 mt-4 text-sm leading-7 text-text-secondary">{t('docs.poll_response_label')}</p>
        <CodeBlock>{IMG_POLL_RESPONSE_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">
          {t('docs.img_poll_result_prefix')}
          <code className="kbd mx-1">result.data[0].url</code>
          {t('docs.img_poll_url')}
          {t('docs.img_poll_dims')}
        </p>
      </DocSection>

      {/* ========================= 视频 ========================= */}
      <DocSection title={t('docs.vid_t2v_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(VID_T2V_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.vid_t2v_intro_prefix')}
          <code className="kbd">grok-imagine-video</code>
          {t('docs.vid_t2v_intro_after')}
          <strong>{t('docs.vid_t2v_strong')}</strong>
          {t('docs.vid_t2v_intro_norefs')}
          <code className="kbd">image</code> / <code className="kbd">images[]</code> {t('docs.vid_t2v_intro_or')}{' '}
          <code className="kbd">ref_assets[]</code>
          {t('docs.vid_t2v_intro_i2v')}
          <code className="kbd">async=false</code>
          {t('docs.vid_t2v_intro_tail')}
        </p>
        <CodeBlock>{VID_T2V_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.vid_i2v_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(VID_I2V_SAMPLE, 'cURL')}>
        <CodeBlock>{VID_I2V_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.vid_long_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(VID_LONG_SAMPLE, 'cURL')}>
        <CodeBlock>{VID_LONG_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.vid_poll_title')} actionLabel={t('docs.copy_sample_btn')} onCopy={() => copy(VID_POLL_SAMPLE, t('docs.copy_sample_btn'))}>
        <CodeBlock>{VID_POLL_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">{t('docs.vid_poll_intro')}</p>
        <p className="mb-2 mt-4 text-sm leading-7 text-text-secondary">{t('docs.poll_response_label')}</p>
        <CodeBlock>{VID_POLL_RESPONSE_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">
          {t('docs.vid_poll_result_prefix')}
          <code className="kbd mx-1">result.data[0].url</code>
          {t('docs.vid_poll_url')}
          <code className="kbd mx-1">result.data[0].cover_url</code>
          {t('docs.vid_poll_thumb')}
        </p>
      </DocSection>

      {/* ========================= 音乐 ========================= */}
      <DocSection title={t('docs.music_t2a_title')} actionLabel={t('docs.copy_curl_btn')} onCopy={() => copy(MUSIC_T2A_SAMPLE, 'cURL')}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">
          {t('docs.music_t2a_intro_prefix')}
          <code className="kbd">model</code>（<code className="kbd">lyria</code> / <code className="kbd">lyria-pro</code>）、{' '}
          <code className="kbd">prompt</code>、<code className="kbd">async</code>、<code className="kbd">callback_url</code>
          {t('docs.music_t2a_intro_after')}
        </p>
        <CodeBlock>{MUSIC_T2A_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.music_poll_title')} actionLabel={t('docs.copy_sample_btn')} onCopy={() => copy(MUSIC_POLL_SAMPLE, t('docs.copy_sample_btn'))}>
        <CodeBlock>{MUSIC_POLL_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">{t('docs.music_poll_intro')}</p>
        <p className="mb-2 mt-4 text-sm leading-7 text-text-secondary">{t('docs.poll_response_label')}</p>
        <CodeBlock>{MUSIC_POLL_RESPONSE_SAMPLE}</CodeBlock>
        <p className="mt-3 text-small leading-7 text-text-tertiary">
          {t('docs.music_poll_result_prefix')}
          <code className="kbd mx-1">result.data[0].url</code>
          {t('docs.music_poll_url')}
          <code className="kbd mx-1">result.data[0].cover_url</code>
          {t('docs.music_poll_thumb')}
        </p>
      </DocSection>

      <DocSection title={t('docs.webhook_title')} actionLabel={t('docs.copy_sample_btn')} onCopy={() => copy(WEBHOOK_SAMPLE, t('docs.webhook_copy_label'))}>
        <p className="mb-3 text-sm leading-7 text-text-secondary">{t('docs.webhook_intro')}</p>
        <CodeBlock>{WEBHOOK_SAMPLE}</CodeBlock>
        <ul className="mt-3 space-y-2 text-small leading-7 text-text-tertiary">
          <li>{t('docs.webhook_note_1')}</li>
          <li>{t('docs.webhook_note_2')}</li>
          <li>{t('docs.webhook_note_3')}</li>
        </ul>
      </DocSection>

      {/* ========================= 尺寸表 ========================= */}
      <DocSection
        title={t('docs.sizes_banana_title')}
        actionLabel={t('docs.copy_table_btn')}
        onCopy={() => copy(
          BANANA_SIZES.map((tier) => `${tier.tier}\n${tier.rows.map(([r, s]) => `${r} -> ${s}`).join('\n')}`).join('\n\n'),
          t('docs.sizes_banana_title'),
        )}
      >
        <p className="mb-3 text-small leading-7 text-text-tertiary">
          {t('docs.sizes_banana_intro_prefix')}
          <code className="kbd mx-1">nano-banana-pro</code> / <code className="kbd mx-1">nano-banana-v2</code> /{' '}
          <code className="kbd mx-1">nano-banana</code>。<strong>{t('docs.sizes_banana_intro_strong')}</strong>
          {t('docs.sizes_banana_intro_tail')}
          <code className="kbd mx-1">size</code>
          {t('docs.sizes_banana_intro_size')}
        </p>
        <SizeTier sizes={BANANA_SIZES} t={t} />
      </DocSection>

      <DocSection
        title={t('docs.sizes_gpt_title')}
        actionLabel={t('docs.copy_table_btn')}
        onCopy={() => copy(
          GPT_IMAGE_SIZES.map((tier) => `${tier.tier}\n${tier.rows.map(([r, s]) => `${r} -> ${s}`).join('\n')}`).join('\n\n'),
          t('docs.sizes_gpt_title'),
        )}
      >
        <p className="mb-3 text-small leading-7 text-text-tertiary">
          {t('docs.sizes_gpt_intro_prefix')}
          <code className="kbd mx-1">gpt-image-2</code>。
          <strong>{t('docs.sizes_gpt_intro_note')}</strong>
          {t('docs.sizes_gpt_intro_after_note')}
        </p>
        <SizeTier sizes={GPT_IMAGE_SIZES} t={t} />
      </DocSection>

      {/* ========================= 任务状态 ========================= */}
      <DocSection
        title={t('docs.status_title')}
        actionLabel={t('docs.copy_desc_btn')}
        onCopy={() => copy(TASK_STATUSES.map((s) => `${s.code} (${s.label}): ${t(s.descKey)}`).join('\n'), t('docs.status_copy_label'))}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {TASK_STATUSES.map((s) => (
            <div key={s.code} className="rounded-lg border border-border bg-surface-2 p-4">
              <div className="mb-1 text-sm font-medium text-text-primary">
                <span className="font-mono text-klein-600">{s.code}</span>
                <span className="ml-2 font-mono text-text-secondary">{s.label}</span>
              </div>
              <div className="text-small leading-7 text-text-tertiary">{t(s.descKey)}</div>
            </div>
          ))}
        </div>
      </DocSection>

      <DocSection
        title={t('docs.video_notes_title')}
        actionLabel={t('docs.copy_points_btn')}
        onCopy={() => copy(
          VIDEO_NOTES.map(({ key, valueKey }) => `${key.startsWith('docs.') ? t(key) : key}: ${t(valueKey)}`).join('\n'),
          t('docs.video_notes_copy_label'),
        )}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {VIDEO_NOTES.map(({ key, valueKey }) => (
            <div key={key} className="rounded-lg border border-border bg-surface-2 p-4">
              <div className="mb-1 text-sm font-medium text-text-primary">{key.startsWith('docs.') ? t(key) : key}</div>
              <div className="text-small leading-7 text-text-tertiary">{t(valueKey)}</div>
            </div>
          ))}
        </div>
      </DocSection>

      {/* ========================= 代码示例 ========================= */}
      <DocSection title={t('docs.py_title')} actionLabel={t('docs.copy_code_btn')} onCopy={() => copy(PYTHON_SAMPLE, t('docs.py_copy_label'))}>
        <CodeBlock>{PYTHON_SAMPLE}</CodeBlock>
      </DocSection>

      <DocSection title={t('docs.js_title')} actionLabel={t('docs.copy_code_btn')} onCopy={() => copy(JS_SAMPLE, t('docs.js_copy_label'))}>
        <CodeBlock>{JS_SAMPLE}</CodeBlock>
      </DocSection>
    </div>
  );
}

// ============================================================================
// 子组件
// ============================================================================
type TFn = (key: string, options?: Record<string, unknown>) => string;

function priceDisplay(p: number | undefined, t: TFn, notSupported = false): ReactNode {
  if (notSupported) {
    return <span className="text-text-tertiary/50">·</span>;
  }
  if (p == null || p <= 0) {
    return <span className="text-text-tertiary">—</span>;
  }
  return (
    <span className="inline-flex items-baseline gap-0.5 whitespace-nowrap tabular-nums">
      <span>{fmtPoints(p)}</span>
      <span className="text-tiny text-text-tertiary">{t('common.points')}</span>
    </span>
  );
}

function ImageModelTable({ title, rows, caps, t }: { title: string; rows: PublicModel[]; caps: Record<string, string>; t: TFn }) {
  if (rows.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-surface-1 p-4">
        <h3 className="section-title mb-2">{title}</h3>
        <p className="text-small text-text-tertiary">{t('docs.image_table_empty')}</p>
      </div>
    );
  }
  return (
    <div className="rounded-xl border border-border bg-surface-1 p-4">
      <h3 className="section-title mb-3">{title}</h3>
      <div className="overflow-x-auto">
        <table className="min-w-full border-separate border-spacing-0 text-sm">
          <thead>
            <tr className="text-left text-text-tertiary">
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_id')}</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_name')}</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">1K</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">2K</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">4K</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_caps')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((m) => {
              const priced = IMAGE_TIERS.map((tier) => m.image_pricing?.[tier] ?? m.unit_points);
              return (
                <tr key={m.model_code} className="align-top">
                  <td className="border-b border-border px-3 py-2 font-mono text-klein-600">{m.model_code}</td>
                  <td className="border-b border-border px-3 py-2 text-text-primary">{m.name}</td>
                  {priced.map((p, i) => (
                    <td
                      key={IMAGE_TIERS[i]}
                      className="border-b border-border px-3 py-2 font-mono text-right text-text-secondary"
                    >
                      {priceDisplay(p, t)}
                    </td>
                  ))}
                  <td className="border-b border-border px-3 py-2 text-text-tertiary leading-6">
                    {caps[m.model_code] ?? '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function TextModelTable({ title, rows, caps, t }: { title: string; rows: PublicModel[]; caps: Record<string, string>; t: TFn }) {
  if (rows.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-surface-1 p-4">
        <h3 className="section-title mb-2">{title}</h3>
        <p className="text-small text-text-tertiary">{t('docs.text_table_empty')}</p>
      </div>
    );
  }
  return (
    <div className="rounded-xl border border-border bg-surface-1 p-4">
      <h3 className="section-title mb-3">{title}</h3>
      <div className="overflow-x-auto">
        <table className="min-w-full border-separate border-spacing-0 text-sm">
          <thead>
            <tr className="text-left text-text-tertiary">
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_id')}</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_name')}</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">{t('docs.th_input_1k')}</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">{t('docs.th_output_1k')}</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_caps')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((m) => (
              <tr key={m.model_code} className="align-top">
                <td className="border-b border-border px-3 py-2 font-mono text-klein-600">{m.model_code}</td>
                <td className="border-b border-border px-3 py-2 text-text-primary">{m.name}</td>
                <td className="border-b border-border px-3 py-2 font-mono text-right text-text-secondary">
                  {priceDisplay(m.input_unit_points, t)}
                </td>
                <td className="border-b border-border px-3 py-2 font-mono text-right text-text-secondary">
                  {priceDisplay(m.output_unit_points, t)}
                </td>
                <td className="border-b border-border px-3 py-2 text-text-tertiary leading-6">
                  {caps[m.model_code] ?? '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function VideoModelTable({ title, rows, caps, t }: { title: string; rows: PublicModel[]; caps: Record<string, string>; t: TFn }) {
  if (rows.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-surface-1 p-4">
        <h3 className="section-title mb-2">{title}</h3>
        <p className="text-small text-text-tertiary">{t('docs.video_table_empty')}</p>
      </div>
    );
  }
  return (
    <div className="rounded-xl border border-border bg-surface-1 p-4">
      <h3 className="section-title mb-3">{title}</h3>
      <ul className="divide-y divide-border">
        {rows.map((m) => {
          const supported = videoDurationsFor(m.model_code);
          const chips = supported.map((d) => {
            const variant = m.video_pricing?.[d];
            let price: number | undefined;
            if (typeof variant === 'number' && variant > 0) {
              price = variant;
            } else {
              const base = m.unit_points ?? 0;
              if (m.video_pricing_mode === 'flat') price = base;
              else if (m.video_pricing_mode === 'variant') price = undefined;
              else price = Math.round((base * Number(d)) / 6);
            }
            return { duration: d, price };
          });
          return (
            <li
              key={m.model_code}
              className="grid grid-cols-1 gap-3 py-4 first:pt-0 last:pb-0 md:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]"
            >
              <div className="min-w-0">
                <div className="font-mono text-small text-klein-600 truncate">{m.model_code}</div>
                <div className="mt-0.5 text-small text-text-primary truncate">{m.name}</div>
              </div>
              <div className="space-y-2 min-w-0">
                <div className="flex flex-wrap gap-1.5">
                  {chips.map((c) => (
                    <span
                      key={c.duration}
                      className="inline-flex items-baseline gap-1 rounded-md border border-border bg-surface-2 px-2 py-1 text-tiny"
                    >
                      <span className="font-medium text-text-secondary">{c.duration}s</span>
                      <span className="text-text-tertiary">·</span>
                      {c.price && c.price > 0 ? (
                        <span className="font-mono tabular-nums text-text-primary">
                          {fmtPoints(c.price)}
                          <span className="ml-0.5 text-text-tertiary">{t('common.points')}</span>
                        </span>
                      ) : (
                        <span className="text-text-tertiary">{t('docs.base_price_label')}</span>
                      )}
                    </span>
                  ))}
                </div>
                {caps[m.model_code] && (
                  <p className="text-tiny text-text-tertiary leading-relaxed">
                    {caps[m.model_code]}
                  </p>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function MusicModelTable({ title, rows, caps, t }: { title: string; rows: PublicModel[]; caps: Record<string, string>; t: TFn }) {
  if (rows.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-surface-1 p-4">
        <h3 className="section-title mb-2">{title}</h3>
        <p className="text-small text-text-tertiary">{t('docs.music_table_empty')}</p>
      </div>
    );
  }
  return (
    <div className="rounded-xl border border-border bg-surface-1 p-4">
      <h3 className="section-title mb-3">{title}</h3>
      <div className="overflow-x-auto">
        <table className="min-w-full border-separate border-spacing-0 text-sm">
          <thead>
            <tr className="text-left text-text-tertiary">
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_id')}</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_name')}</th>
              <th className="border-b border-border px-3 py-2 font-normal text-right">{t('docs.th_per_song')}</th>
              <th className="border-b border-border px-3 py-2 font-normal">{t('docs.th_caps')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((m) => (
              <tr key={m.model_code} className="align-top">
                <td className="border-b border-border px-3 py-2 font-mono text-klein-600">{m.model_code}</td>
                <td className="border-b border-border px-3 py-2 text-text-primary">{m.name}</td>
                <td className="border-b border-border px-3 py-2 font-mono text-right text-text-secondary">
                  {priceDisplay(m.unit_points, t)}
                </td>
                <td className="border-b border-border px-3 py-2 text-text-tertiary leading-6">
                  {caps[m.model_code] ?? '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function SizeTier({ sizes, t }: { sizes: ReadonlyArray<SizeTierEntry>; t: TFn }) {
  return (
    <div className="space-y-4">
      {sizes.map((tier) => (
        <div key={tier.tier} className="rounded-xl border border-border bg-surface-1 p-4">
          <div className="mb-3 flex items-center justify-between gap-3">
            <h3 className="section-title">{tier.tier}</h3>
            <span className="text-small text-text-tertiary">{t(tier.noteKey)}</span>
          </div>
          <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
            {tier.rows.map(([ratio, size]) => (
              <div key={`${tier.tier}-${ratio}`} className="flex items-center justify-between rounded-lg border border-border bg-surface-2 px-3 py-2 text-sm">
                <span className="text-text-secondary">{ratio}</span>
                <span className="font-mono text-klein-600">{size}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function DocSection({
  title,
  actionLabel,
  onCopy,
  children,
}: {
  title: string;
  actionLabel: string;
  onCopy: () => void;
  children: ReactNode;
}) {
  return (
    <section className="card card-section">
      <header className="section-header mb-3">
        <h3 className="section-title">{title}</h3>
        <button className="btn btn-outline btn-sm" onClick={onCopy} type="button">
          <Copy size={14} /> {actionLabel}
        </button>
      </header>
      {children}
    </section>
  );
}

function CodeBlock({ children }: { children: ReactNode }) {
  return (
    <pre className="overflow-x-auto rounded-md border border-border bg-surface-2 p-4 font-mono text-small leading-7 whitespace-pre">
      {children}
    </pre>
  );
}
