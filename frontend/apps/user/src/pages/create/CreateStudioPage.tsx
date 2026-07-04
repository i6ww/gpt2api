import { useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from 'react-router-dom';
import {
  ArrowUp,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FileImage,
  Image,
  Loader2,
  Maximize2,
  Mic,
  MoreHorizontal,
  Music,
  Paperclip,
  Play,
  Sparkles,
  Trash2,
  Video,
  X,
} from 'lucide-react';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';

import { useEnsureLoggedIn } from '../../hooks/useEnsureLoggedIn';
import { ApiError } from '../../lib/api';
import { fmtPoints, fmtRelative } from '../../lib/format';
import { genApi } from '../../lib/services';
import type { GenerationTask, PublicModel } from '../../lib/types';
import { useAuthStore } from '../../stores/auth';
import { toast } from '../../stores/toast';
import { useConfirm } from '../../components/ConfirmDialog';

type StudioMode = 'image' | 'text' | 'video' | 'music';

const MODES: Array<{ value: StudioMode; labelKey: string; icon: typeof Image }> = [
  // labelKey 是 i18n key，渲染时再用 t() 拿翻译，避免硬编码中文。
  { value: 'image', labelKey: 'studio.mode_image', icon: Image },
  { value: 'text', labelKey: 'studio.mode_text', icon: Sparkles },
  { value: 'video', labelKey: 'studio.mode_video', icon: Video },
  { value: 'music', labelKey: 'studio.mode_music', icon: Music },
];

// 生成等待时的轮转文案。
// 不固定节奏（GeneratingDots 内会按 3 ~ 5 秒之间的随机时间切换），
// 同时下一句从池子里随机抽（且避开当前这句），让长时间生成时不会有
// 「重复的几句话固定节奏在循环」的机械感。多写几条让随机更有趣。
const GENERATING_PHRASES = [
  '正在为您设计中...',
  '灵感正在慢慢成形',
  '细节正在被认真打磨',
  '画面很快就会出现',
  '调色板正在挑选合适的颜色',
  '正在反复推敲构图',
  '让光线再温柔一点点',
  '马上就要落笔了',
  '正在把脑海里的画面翻译出来',
  '正在让每一个像素都到位',
  '稍等一下，下一帧很惊喜',
];

type SelectModel = {
  code: string;
  label: string;
  /** 单价（分，×100 整数。1 元 = 100 分）。展示给用户时用 fmtPoints 还原成元的小数。 */
  cost?: number;
  /** 文字模型 1K token 输入单价（分，×100 整数）。 */
  input?: number;
  /** 文字模型 1K token 输出单价（分，×100 整数）。 */
  output?: number;
  videoPricingMode?: 'scaled' | 'flat' | 'variant';
  /** 图片分档价（分，×100 整数）。key = '1k'/'2k'/'4k'。 */
  imagePricing?: Record<string, number>;
  /** 视频分档价（分，×100 整数）。key = '6'/'10'/'20'/'30'。 */
  videoPricing?: Record<string, number>;
};

// 注意：cost / input / output 这些 *Points 字段单位都是「分（×100 整数）」，
// 展示给用户时用 fmtPoints 还原成元的小数。这里 fallback 跟 admin 后台模型价格表对齐：
//   gpt-image-2=8 → 0.08 元；nano-banana=8 → 0.08 元；nano-banana-pro=10 → 0.10 元
// 实际值由 /api/v1/models 动态覆盖；这里只是后端不可达时的兜底。
const IMAGE_MODELS: SelectModel[] = [
  { code: 'gpt-image-2', label: 'GPT Image 2', cost: 8 },
  { code: 'nano-banana-pro', label: 'Nano Banana Pro', cost: 10 },
  { code: 'nano-banana-v2', label: 'Nano Banana V2', cost: 8 },
  { code: 'nano-banana', label: 'Nano Banana', cost: 8 },
];

// 视频模型：
//   1) grok-imagine-video           主通道（官方 xAI），扣额度，支持 6/10/15s + 自定义比例
//      - 不带参考图 → 文生视频 (t2v)
//      - 带参考图   → 图生视频 (i2v)
//   2) grok-imagine-video-6s-free   免额度通道（Imagine Pipeline）
//      - i2v-only，必须传参考图；服务端固定 6 秒，比例服务端定（参考图比例）
//      - 主通道 429/quota 时后端会自动 fallback 到这条；用户也可直接选这个免费跑
// fallback 单位：分（×100 整数）。实际值由 /api/v1/models 覆盖。
// grok-imagine-video-6s-free 已不对外暴露（后端主通道 fallback 内部仍可用），不在此 fallback 列表中。
const VIDEO_MODELS: SelectModel[] = [
  { code: 'grok-imagine-video', label: 'Grok Imagine Video', cost: 10 },
  { code: 'sora', label: 'Sora 2', cost: 100 },
  { code: 'veo3.1', label: 'VEO 3.1', cost: 150 },
  { code: 'veo3.1-flash', label: 'VEO 3.1 Flash', cost: 80 },
  { code: 'veo3.1-lite', label: 'VEO 3.1 Lite', cost: 50 },
];

// VIDEO_MODEL_SPECS 把每个视频模型的「可选时长 / 可选比例 / 参考图上限 / 1080P」固化到前端，
// 避免用户在 composer 选 sora + 1080P 或 veo3.1-lite + 3 张参考图，被后端 422 退回。
//
// 来源：firefly /v2/models/discovery schema + HAR 抓包。
// veo 系列虽然 schema 都接 4/6/8s，但 admin 后台 video_pricing 只配了 4/6/8 三档，
// 用户选 10/20/30 的话 estimateVideoCost 会落到 scaled 倍率，前端按支持的 4/6/8 限定。
type VideoModelSpec = {
  durations: number[];
  ratios: string[];
  maxRefs: number;
  // i18n key（studio.ref_*_hint），渲染时再 t()。给参考图上传按钮的 tooltip。
  refUsageHintKey?: string;
  resolutions?: string[];
};
const VIDEO_MODEL_SPECS: Record<string, VideoModelSpec> = {
  // 官方 xAI（api.x.ai）：上游只接受 1-15 秒，原生输出 720p。
  // 不带参考图=文生视频；带参考图=自动切 grok-imagine-video-1.5 图生视频（后端处理，同价）。
  'xai/grok-imagine-video': {
    durations: [6, 10, 15],
    ratios: ['16:9', '9:16', '1:1'],
    maxRefs: 7,
    refUsageHintKey: 'studio.ref_t2v_hint',
    resolutions: ['720p'],
  },
  // grok-imagine-video 已切到官方 xAI（api.x.ai）通道：上游只接受 1-15 秒、原生 720p，
  // 不再走网页版 extension 拼接，所以时长统一成 6/10/15，与 xai/grok-imagine-video 同档同价。
  'grok-imagine-video': {
    durations: [6, 10, 15],
    ratios: ['16:9', '9:16', '1:1'],
    maxRefs: 7,
    refUsageHintKey: 'studio.ref_t2v_hint',
    resolutions: ['720p'],
  },
  sora: {
    durations: [4, 8, 12],
    ratios: ['16:9', '9:16'],
    maxRefs: 1,
    refUsageHintKey: 'studio.ref_first_frame_hint',
    resolutions: ['720p'],
  },
  'veo3.1': {
    durations: [4, 6, 8],
    ratios: ['16:9', '9:16'],
    maxRefs: 3,
    refUsageHintKey: 'studio.ref_subject_hint',
    resolutions: ['1080p', '720p'],
  },
  'veo3.1-flash': {
    durations: [4, 6, 8],
    ratios: ['16:9', '9:16'],
    maxRefs: 2,
    refUsageHintKey: 'studio.ref_first_last_hint',
    resolutions: ['1080p', '720p'],
  },
  'veo3.1-lite': {
    durations: [4, 6, 8],
    ratios: ['16:9', '9:16'],
    maxRefs: 2,
    refUsageHintKey: 'studio.ref_first_last_hint',
    resolutions: ['1080p', '720p'],
  },
};
const DEFAULT_VIDEO_SPEC: VideoModelSpec = {
  durations: [6, 10, 20, 30],
  ratios: ['16:9', '9:16', '1:1'],
  maxRefs: 7,
};

function videoSpecFor(modelCode: string): VideoModelSpec {
  return VIDEO_MODEL_SPECS[modelCode] ?? DEFAULT_VIDEO_SPEC;
}

// fallback 单位：分（×100 整数），1K token 单价。实际值由 /api/v1/models 覆盖。
const TEXT_MODELS: SelectModel[] = [
  { code: 'grok-4.20-fast', label: 'Grok Fast', input: 10, output: 30 },
  { code: 'grok-4.20-auto', label: 'Grok Auto', input: 15, output: 45 },
  { code: 'grok-4.20-expert', label: 'Grok Expert', input: 20, output: 60 },
  { code: 'grok-4.20-heavy', label: 'Grok Heavy', input: 40, output: 120 },
];

// 音乐模型（FlowMusic / Lyria）。fallback 单位：分（×100 整数），实际值由 /api/v1/models 覆盖。
const MUSIC_MODELS: SelectModel[] = [
  { code: 'lyria', label: 'Lyria', cost: 2000 },
  { code: 'lyria-pro', label: 'Lyria Pro', cost: 3000 },
];

// 歌曲示例：点击把 prompt 填入输入框。封面用本地占位图（可后续替换）。
const MUSIC_SUGGESTIONS: { titleKey: string; prompt: string }[] = [
  { titleKey: 'studio.music_suggestion_1', prompt: '一首轻快温暖的中文流行歌，关于夏天的海边，女声，吉他与合成器，110 BPM' },
  { titleKey: 'studio.music_suggestion_2', prompt: 'An epic cinematic orchestral track with soaring strings and powerful percussion, building to a triumphant climax' },
  { titleKey: 'studio.music_suggestion_3', prompt: '深夜城市 Lo-fi hip-hop，慵懒的钢琴旋律，轻柔的鼓点，适合学习和放松' },
  { titleKey: 'studio.music_suggestion_4', prompt: 'An upbeat K-pop dance track with catchy synth hooks, energetic beats and a memorable chorus' },
];

const IMAGE_RATIOS = ['1:1', '3:2', '2:3', '4:3', '3:4', '5:4', '4:5', '16:9', '9:16', '21:9'] as const;
const IMAGE_RESOLUTIONS = ['1K', '2K', '4K'] as const;
const VIDEO_RATIOS = ['16:9', '9:16', '1:1'] as const;
// Grok 视频走 grok.com web 链路（super_grok SSO cookie）。单次 conversations/new 调用
// 上游限制 videoLength ≤ 10 秒，但 20s / 30s 是「初始 10s + 1~2 次 extension」拼出来的：
// 第二次起 isVideoExtension=true / extendPostId / stitchWithExtendPostId=true，由上游
// 服务端把片段合成一条完整 mp4 返回（grok.com.har 抓包证实）。后端 WebClient
// GenerateVideo 已经实现这条 extension 链，前端这里恢复 6/10/20/30 四档。
const VIDEO_DURATIONS = [6, 10, 15, 20, 30] as const;
const HISTORY_PAGE_SIZES = [20, 50, 100] as const;
type HistoryDeleteScope = 'before_3d' | 'before_7d' | 'all';
const TEXT_MAX_ATTACHMENTS = 5;
const VIDEO_MAX_ATTACHMENTS = 7;
// 示例卡片：title 改成 i18n key（titleKey），渲染时再 t()，prompt 仍是英文 / 中文模板，
// 用户点击会把 prompt 整段填进 textarea，所以 prompt 内容保持原样（用户自己改）。
const SUGGESTIONS: { titleKey: string; image: string; prompt: string }[] = [
  {
    titleKey: 'studio.suggestion_1',
    image: '/examples/case-1.jpg',
    prompt: `A minimalist product advertisement with a {argument name="product" default="fried chicken bucket"} placed on a clean white podium.

Background: soft gradient ({argument name="background gradient" default="light cream to white"}), clean studio.

Lighting: soft diffused, premium Apple-style.

Typography (center): "{argument name="headline" default="PURE CRUNCH"}"

Small text below: "Nothing extra. Just perfection."

Style: ultra clean, editorial minimal, high-end branding, 8K.`,
  },
  {
    titleKey: 'studio.suggestion_2',
    image: '/examples/case-2.jpg',
    prompt: `A striking Spring 2026 city poster for Boston with an elegant celebratory mood and a bold contemporary design. On a clean off-white textured background with large areas of negative space, a miniature single sculler rows across the lower right corner of the image on a narrow ribbon of reflective water. The wake from the oar sweeps upward in a dynamic calligraphic curve, gradually transforming into the Charles River and then into a dreamlike hand-painted panorama of Boston. Inside this flowing river-shaped composition are iconic Boston elements: the Back Bay skyline, Beacon Hill brownstones, Acorn Street, Boston Public Garden, Swan Boats, Zakim Bridge, Fenway-inspired details, historic brick architecture, harbor ferries, and the city's waterfront atmosphere. Soft morning fog, golden spring light, subtle festive accents in crimson and gold, rich detail, layered depth, sophisticated city-poster aesthetics, fresh and refined, visually powerful but not overcrowded. Elegant typography in the lower left reads "SPRING 2026" with a vertical slogan "BOSTON, A CITY OF RIVER, MEMORY, AND INVENTION", text clear and beautifully composed, premium graphic design, 9:16`,
  },
  {
    titleKey: 'studio.suggestion_3',
    image: '/examples/case-3.jpg',
    prompt: `Photorealistic high-quality studio photo of a modern digital art workspace, showing the concept of "from 3D virtual character to real collectible figure."

In the foreground, a highly realistic collectible figurine of [Character Name / Character Identity] is placed on a round wooden display stand. The character has [facial features / appearance], [hairstyle], and a [expression / personality vibe]. The figure is wearing [outfit / costume]. The overall design is refined, premium, and instantly recognizable. The figurine should have realistic collectible statue quality, with subtle resin/sculpture material feel, while still looking highly believable and visually realistic.

The pose is [character pose], natural, stable, elegant, and display-worthy. Shot from a low-angle close-up perspective with slight wide-angle distortion, vertical composition, emphasizing the full figure, clothing structure, leg lines, and pose.

In the background, there is a professional 3D character design workstation with two large curved monitors. Both monitors must show the exact same character as the foreground figurine - same face, same hairstyle, same outfit, same pose, and same overall vibe - clearly expressing the idea of turning a digital 3D character into a real physical figure.

The left monitor shows a gray sculpt / clay model view in a professional 3D sculpting software interface, similar to ZBrush. The gray model must match the foreground figure exactly in character design, pose, outfit structure, and facial identity.

The right monitor shows the fully rendered colored version of the same character, also matching the foreground figure exactly in face, hairstyle, outfit, pose, and temperament. Together, the two monitors reinforce the workflow of "digital character design -> physical collectible statue."

On the desk are a keyboard, mouse, monitor arms, drawing tablet, stylus, and other 3D modeling tools. The workspace is clean, professional, and visually premium. Optional extra elements: [weapon / accessories / theme props / IP-style design details].

Lighting is a mix of soft studio lighting and indoor workspace lighting. The foreground figurine is evenly lit with clear facial and material detail, while the monitors emit cool-toned tech light. Overall mood is realistic, clean, premium, slightly shallow depth of field, ultra-detailed, emphasizing the collectible figure quality, professional 3D design studio atmosphere, and the visual concept of "from digital model to real figure."

photorealistic, ultra detailed, cinematic studio lighting, realistic figurine, collectible statue, 3D character design studio, from digital model to real figure, vertical composition`,
  },
  {
    titleKey: 'studio.suggestion_4',
    image: '/examples/case-4.jpg',
    prompt: '生成鹿鼎记海报，展现韦小宝跟老婆XXX，忠于原著的描述，夸大特点，强调女性的美艳和男性的气质',
  },
  {
    titleKey: 'studio.suggestion_5',
    image: '/examples/case-5.jpg',
    prompt: `{
  "type": "evolutionary timeline infographic",
  "instruction": "Using REFERENCE_0 as a structural base, transform the flat vector design into a highly realistic 3D infographic. Replace the smooth ramps with distinct stone steps and upgrade all organisms to photorealistic 3D models.",
  "style": {
    "background": "{argument name=\\"background style\\" default=\\"vintage textured parchment paper\\"}",
    "staircase": "{argument name=\\"staircase material\\" default=\\"realistic textured stone blocks\\"}",
    "subjects": "{argument name=\\"organism style\\" default=\\"highly detailed photorealistic 3D renders\\"}"
  },
  "layout": {
    "main_title": "{argument name=\\"main title\\" default=\\"人类演化\\"}",
    "sections": [
      {
        "position": "left sidebar",
        "count": 8,
        "labels": ["L0: 单细胞生命", "L1: 多细胞生物", "L2: 动物界", "L3: 脊索动物", "L4: 上陆革命", "L5: 哺乳纲", "L6: 人科演化", "L7: 智人纪元"]
      },
      {
        "position": "top right",
        "title": "获得的功能 / 失去的功能",
        "description": "Legend with plus and minus icons"
      },
      {
        "position": "bottom center",
        "title": "演化关键里程碑",
        "count": 6,
        "description": "Timeline with a silhouette graphic of 6 figures showing ape-to-human evolution"
      }
    ],
    "centerpiece": {
      "description": "Winding stone staircase with 25 numbered steps featuring specific organisms.",
      "count": 25,
      "notable_elements": [
        "Step 07: Jellyfish",
        "Step 09: Ammonite",
        "Step 10: Trilobite",
        "Step 24: Walking human",
        "Step 25: {argument name=\\"future evolution concept\\" default=\\"glowing cosmic silhouette with a question mark\\"}"
      ]
    }
  }
}`,
  },
];

export default function CreateStudioPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const ensureLoggedIn = useEnsureLoggedIn();
  const refreshMe = useAuthStore((s) => s.refreshMe);
  const token = useAuthStore((s) => s.token);

  const modelCatalog = useQuery({
    queryKey: ['gen.models'],
    queryFn: () => genApi.models(),
    staleTime: 60_000,
  });

  const imageModels = useMemo(() => modelsByKind(modelCatalog.data, 'image', IMAGE_MODELS), [modelCatalog.data]);
  const textModels = useMemo(() => modelsByKind(modelCatalog.data, 'text', TEXT_MODELS), [modelCatalog.data]);
  const videoModels = useMemo(() => modelsByKind(modelCatalog.data, 'video', VIDEO_MODELS), [modelCatalog.data]);
  const musicModels = useMemo(() => modelsByKind(modelCatalog.data, 'music', MUSIC_MODELS), [modelCatalog.data]);

  const mode = modeFromPath(location.pathname);
  const [prompt, setPrompt] = useState('');
  const [textModel, setTextModel] = useState(TEXT_MODELS[0]!.code);
  const [imageModel, setImageModel] = useState(IMAGE_MODELS[0]!.code);
  const [videoModel, setVideoModel] = useState(VIDEO_MODELS[0]!.code);
  const [musicModel, setMusicModel] = useState(MUSIC_MODELS[0]!.code);
  const [imageRatio, setImageRatio] = useState<(typeof IMAGE_RATIOS)[number]>('1:1');
  const [imageResolution, setImageResolution] = useState<(typeof IMAGE_RESOLUTIONS)[number]>('1K');
  const [videoRatio, setVideoRatio] = useState<(typeof VIDEO_RATIOS)[number]>('16:9');
  const [count, setCount] = useState(1);
  const [duration, setDuration] = useState<(typeof VIDEO_DURATIONS)[number]>(6);
  const [attachments, setAttachments] = useState<Array<{ id: string; name: string; dataUrl: string }>>([]);
  const [textResult, setTextResult] = useState('');
  // 多任务：每条提交后单独跟踪。key = task_id。
  // 切模式 / 卸载时不会一刀切清空——让正在生成的任务在后台继续 polling，
  // 用户切回来时还能看到结果。
  const [tasks, setTasks] = useState<Record<string, GenerationTask>>({});
  const [historyPageSize, setHistoryPageSize] = useState<(typeof HISTORY_PAGE_SIZES)[number]>(20);
  const [preview, setPreview] = useState<{ url: string; type: 'image' | 'video'; title: string } | null>(null);
  // 每个 task_id 一个独立 interval，互不干扰。
  const pollIntervalsRef = useRef<Record<string, number>>({});
  // 每个 task_id 一个独立 session 号：startPolling 时递增，请求返回时先核对，
  // 防止「慢回来的 status=1 把已经 setTasks 过的 status=2 又盖回去」的乱序覆盖。
  const pollSessionsRef = useRef<Record<string, number>>({});
  // 哪些 task_id 已经弹过终态 toast：避免乱序响应或快速完成时同一任务 toast 两次。
  const toastedTasksRef = useRef<Set<string>>(new Set());
  const promptRef = useRef<HTMLTextAreaElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const isVideoReferenceMode = mode === 'video' && attachments.length > 0;
  // 视频模型规格：每次切换 videoModel 时自动归位 ratio / duration / 截断超额参考图。
  const currentVideoSpec = useMemo(() => videoSpecFor(videoModel), [videoModel]);
  useEffect(() => {
    if (mode !== 'video') return;
    if (!currentVideoSpec.ratios.includes(videoRatio)) {
      setVideoRatio(currentVideoSpec.ratios[0]! as (typeof VIDEO_RATIOS)[number]);
    }
    if (!currentVideoSpec.durations.includes(duration)) {
      setDuration(currentVideoSpec.durations[0]! as (typeof VIDEO_DURATIONS)[number]);
    }
    setAttachments((prev) => prev.slice(0, currentVideoSpec.maxRefs));
  }, [videoModel, mode, currentVideoSpec, duration, videoRatio]);

  useEffect(() => () => {
    // 卸载时：把所有 task 的 session 递增（让在途请求被丢弃）并清掉 interval。
    for (const id of Object.keys(pollIntervalsRef.current)) {
      pollSessionsRef.current[id] = (pollSessionsRef.current[id] || 0) + 1;
      window.clearInterval(pollIntervalsRef.current[id]);
    }
    pollIntervalsRef.current = {};
  }, []);

  useEffect(() => {
    // 切模式只清表单状态（输入框文案 / 附件），不动 tasks——
    // 让上一模式提交但还没完成的任务在后台继续跑、并继续在「我的作品」里展示进度。
    setTextResult('');
    setAttachments([]);
  }, [mode]);

  useEffect(() => {
    if (imageModels.length && !imageModels.some((m) => m.code === imageModel)) setImageModel(imageModels[0]!.code);
    if (textModels.length && !textModels.some((m) => m.code === textModel)) setTextModel(textModels[0]!.code);
    if (videoModels.length && !videoModels.some((m) => m.code === videoModel)) setVideoModel(videoModels[0]!.code);
    if (musicModels.length && !musicModels.some((m) => m.code === musicModel)) setMusicModel(musicModels[0]!.code);
  }, [imageModel, imageModels, textModel, textModels, videoModel, videoModels, musicModel, musicModels]);

  useEffect(() => {
    const el = promptRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 260)}px`;
    el.style.overflowY = el.scrollHeight > 260 ? 'auto' : 'hidden';
  }, [prompt, mode]);

  const history = useQuery({
    queryKey: ['gen.history', 'studio', token, historyPageSize],
    enabled: !!token,
    queryFn: () => genApi.history({ kind: 'media', page: 1, page_size: historyPageSize }),
  });

  const deleteHistory = useMutation({
    mutationFn: (scope: HistoryDeleteScope) => genApi.deleteHistory(scope),
    onSuccess: (res, scope) => {
      const label =
        scope === 'before_3d'
          ? t('studio.del_label_3d')
          : scope === 'before_7d'
            ? t('studio.del_label_7d')
            : t('studio.del_label_all');
      toast.success(t('studio.del_success', { n: res.deleted, label }));
      // 清理：本地 tasks 也一起清掉。后台还在跑的不太可能（删的是 3天/7天前），
      // 但保险起见把 session 递增让在途请求作废。
      for (const id of Object.keys(pollIntervalsRef.current)) {
        pollSessionsRef.current[id] = (pollSessionsRef.current[id] || 0) + 1;
        window.clearInterval(pollIntervalsRef.current[id]);
      }
      pollIntervalsRef.current = {};
      setTasks({});
      toastedTasksRef.current.clear();
      qc.invalidateQueries({ queryKey: ['gen.history'] });
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('studio.delete_failed')),
  });

  const createImage = useMutation({
    mutationFn: () => genApi.createImage({
      model: imageModel,
      prompt,
      count,
      ratio: imageRatio,
      ref_assets: attachments.map((item) => item.dataUrl),
      mode: attachments.length ? 'i2i' : 't2i',
      // 注意：不能在这里硬塞 quality:'high' —— Adobe / firefly.ResolvePublicAlias 的
      // tier 选择只认 1k/2k/4k/standard/ultra，"high" 会让分档退化到默认 2K（历史 bug）。
      // 让 resolution 单独决定分档；quality 留给 OpenAI 兼容层等外部入口。
      params: { resolution: imageResolution },
    }),
    onSuccess: (task) => handleTask(task),
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('studio.gen_failed')),
  });

  const createVideo = useMutation({
    mutationFn: () => {
      // 免额度 Imagine Pipeline 通道：服务端固定 6s + 服务端定比例，
      // 这里把 duration / ratio 标准化掉，避免前端记账和后端实际产物不一致。
      const isPipelineFree = videoModel === 'grok-imagine-video-6s-free';
      return genApi.createVideo({
        model: videoModel,
        prompt,
        duration: isPipelineFree ? 6 : duration,
        // ratio 一直传：i2v 时后端走 firstNonEmpty(req.AspectRatio, inferAspectRatioFromRef)，
        // 用户显式指定 ratio 时优先按用户的来；不指定时再从参考图推断。
        // 免额度通道传空串让后端跳过 ratio 透传。
        ratio: isPipelineFree ? '' : videoRatio,
        quality: 'hd',
        ref_assets: attachments.map((item) => item.dataUrl),
        mode: isPipelineFree ? 'i2v' : (isVideoReferenceMode ? 'i2v' : 't2v'),
      });
    },
    onSuccess: (task) => handleTask(task),
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('studio.gen_failed')),
  });

  const createText = useMutation({
    mutationFn: () => genApi.createText({ model: textModel, prompt, max_tokens: 1600, images: attachments.map((item) => item.dataUrl) }),
    onSuccess: async (res) => {
      setTextResult(res.content || '');
      toast.success(t('studio.text_done'));
      await refreshMe();
      qc.invalidateQueries({ queryKey: ['gen.history'] });
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('studio.gen_failed')),
  });

  const createMusic = useMutation({
    mutationFn: () => genApi.createMusic({ model: musicModel, prompt }),
    onSuccess: (task) => handleTask(task),
    onError: (e) => toast.error(e instanceof ApiError ? e.message : t('studio.gen_failed')),
  });

  // 任何一个本地 tasks 还在 0/1 阶段，就视为有在跑的生成。
  // 仅作为「轻量提示」用（按钮上的小 spinner），不再用来锁输入框——
  // 用户可以一边等一边继续提交新任务。
  const hasPendingTask = useMemo(
    () => Object.values(tasks).some((t) => t.status === 0 || t.status === 1),
    [tasks],
  );
  const resultItems = useMemo(() => {
    const visible = (item: GenerationTask) =>
      (item.kind === 'image' || item.kind === 'video' || item.kind === 'music') && item.status !== 3;
    const isTerminal = (s: number) => s === 2 || s === 3 || s === 4;
    // 把 history 行用 live tasks 覆盖，让 polling 的 0 → 1 → 2 状态变化实时穿透到 UI。
    // 保险：history 已经是终态、但 live task 还卡在中间态（罕见，polling 偶发吞响应），
    // 那就保留 history 的终态版本，不要被陈旧的 task 盖回 GeneratingDots。
    const merged = (history.data?.list ?? []).map((it) => {
      const live = tasks[it.task_id];
      if (!live) return it;
      if (isTerminal(it.status) && !isTerminal(live.status)) return it;
      return live;
    });
    // history 还没刷新到的新任务，按创建时间倒序顶到最前。
    const inListIds = new Set(merged.map((it) => it.task_id));
    const head = Object.values(tasks)
      .filter((t) => !inListIds.has(t.task_id) && visible(t))
      .sort((a, b) => (b.created_at ?? 0) - (a.created_at ?? 0));
    return [...head, ...merged.filter(visible)];
  }, [history.data?.list, tasks]);

  const expectedCost = mode === 'video'
    ? estimateVideoCost(videoModels.find((m) => m.code === videoModel), duration)
    : mode === 'text'
      ? t('studio.by_token')
      : mode === 'music'
        ? (musicModels.find((m) => m.code === musicModel)?.cost ?? 0)
        : estimateImageCost(imageModels.find((m) => m.code === imageModel), imageResolution, count);
  // 视频模式按当前模型的 maxRefs 限制（sora=1 / veo3.1=3 / veo3.1-flash/lite=2 / grok=7）；
  // 文字 / 图片模式继续用全局 5 上限。
  const maxAttachments = mode === 'video'
    ? Math.min(VIDEO_MAX_ATTACHMENTS, currentVideoSpec.maxRefs)
    : TEXT_MAX_ATTACHMENTS;

  const handleTask = (t: GenerationTask) => {
    setTasks((prev) => ({ ...prev, [t.task_id]: t }));
    startPolling(t.task_id, t.kind);
    void refreshMe();
    qc.invalidateQueries({ queryKey: ['gen.history'] });
  };

  // 每个 taskId 独立一个 setInterval + session 号，多任务并发互不干扰。
  // kind 用来决定 polling 间隔（视频慢一点）。
  const startPolling = (taskId: string, kind: string) => {
    pollSessionsRef.current[taskId] = (pollSessionsRef.current[taskId] || 0) + 1;
    const sessionId = pollSessionsRef.current[taskId];
    if (pollIntervalsRef.current[taskId]) {
      window.clearInterval(pollIntervalsRef.current[taskId]);
      delete pollIntervalsRef.current[taskId];
    }

    const tick = async () => {
      if (sessionId !== pollSessionsRef.current[taskId]) return;
      try {
        const fresh = await genApi.getTask(taskId);
        // 期间被新一轮 polling 顶掉 / 已经终态收尾 / 组件卸载，丢弃这次结果。
        if (sessionId !== pollSessionsRef.current[taskId]) return;
        setTasks((prev) => ({ ...prev, [taskId]: fresh }));
        if (fresh.status === 2 || fresh.status === 3 || fresh.status === 4) {
          // 关闭该 task 的 session，让后续慢回来的旧请求被丢弃。
          pollSessionsRef.current[taskId] = (pollSessionsRef.current[taskId] || 0) + 1;
          if (pollIntervalsRef.current[taskId]) {
            window.clearInterval(pollIntervalsRef.current[taskId]);
            delete pollIntervalsRef.current[taskId];
          }
          // 每个 task 只 toast 一次，避免乱序响应触发重复提示。
          if (!toastedTasksRef.current.has(taskId)) {
            toastedTasksRef.current.add(taskId);
            if (fresh.status === 2) toast.success(t('studio.task_done'));
            else if (fresh.status === 3) toast.error(fresh.error || t('studio.gen_failed'));
            else toast.info(t('studio.task_refunded'));
          }
          await refreshMe();
          qc.invalidateQueries({ queryKey: ['gen.history'] });
        }
      } catch {
        // 短暂网络抖动忽略，下次 tick 会重试
      }
    };

    // 立刻先拉一次，不等 1.5/2s 间隔——对短任务体验更好。
    void tick();
    pollIntervalsRef.current[taskId] = window.setInterval(tick, kind === 'video' ? 2000 : 1500);
  };

  const submit = () => {
    if (!prompt.trim()) {
      toast.info(t('studio.toast_need_prompt'));
      return;
    }
    if (mode === 'video' && videoModel === 'grok-imagine-video-6s-free' && attachments.length === 0) {
      toast.info(t('studio.free_channel_need_ref'));
      return;
    }
    ensureLoggedIn(() => {
      if (mode === 'text') createText.mutate();
      else if (mode === 'video') createVideo.mutate();
      else if (mode === 'music') createMusic.mutate();
      else createImage.mutate();
    }, t('studio.login_to_start'));
  };

  const readFileAsDataURL = (file: File) => new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ''));
    reader.onerror = () => reject(reader.error || new Error('read file failed'));
    reader.readAsDataURL(file);
  });

  const handleAttachFiles = async (files: FileList | null) => {
    if (!files?.length) return;
    const imageFiles = Array.from(files).filter((file) => file.type.startsWith('image/'));
    if (!imageFiles.length) {
      toast.info(t('studio.toast_pick_image_file'));
      return;
    }
    const slots = Math.max(0, maxAttachments - attachments.length);
    if (slots <= 0) {
      toast.info(t('studio.ref_max', { n: maxAttachments }));
      return;
    }
    const picked = imageFiles.slice(0, slots);
    try {
      const data = await Promise.all(picked.map(async (file) => ({
        id: `${file.name}-${file.size}-${file.lastModified}`,
        name: file.name,
        dataUrl: await readFileAsDataURL(file),
      })));
      setAttachments((prev) => [...prev, ...data]);
      if (imageFiles.length > slots) toast.info(t('studio.ref_truncated', { n: maxAttachments }));
    } catch {
      toast.error(t('studio.toast_read_image_failed'));
    } finally {
      if (fileInputRef.current) fileInputRef.current.value = '';
    }
  };

  return (
    <div className="mx-auto min-h-screen w-full max-w-[1500px] px-4 pb-12 pt-10 sm:px-8 lg:px-12">
      <section className="mx-auto max-w-[760px]">
        <div className="mb-6 flex items-center justify-between">
          <h1 className="text-[28px] font-medium tracking-normal text-text-primary">{modeTitle(mode, t)}</h1>
          <ModeSwitch mode={mode} onChange={(next) => navigate(`/create/${next}`)} />
        </div>

        <div className="rounded-[28px] border border-border bg-surface-1 p-4 shadow-3">
          <textarea
            ref={promptRef}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder={
              mode === 'image'
                ? t('studio.prompt_placeholder_image')
                : mode === 'video'
                  ? t('studio.prompt_placeholder_video')
                  : mode === 'music'
                    ? t('studio.prompt_placeholder_music')
                    : t('studio.prompt_placeholder_text')
            }
            className="studio-prompt min-h-[66px] w-full resize-none border-0 bg-transparent px-2 pt-1 text-[15px] font-normal leading-7 text-text-primary outline-none ring-0 placeholder:font-normal placeholder:text-text-tertiary focus:border-0 focus:outline-none focus:ring-0"
            maxLength={5000}
          />
          <div className="mt-2 flex items-center justify-between gap-3">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <input
                ref={fileInputRef}
                type="file"
                accept="image/*"
                multiple
                className="hidden"
                onChange={(e) => void handleAttachFiles(e.target.files)}
              />
              <button
                className="grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2"
                title={mode === 'video'
                  ? t('studio.ref_upload_video_title', {
                      hint: currentVideoSpec.refUsageHintKey ? t(currentVideoSpec.refUsageHintKey) : t('studio.ref_default_hint'),
                      n: maxAttachments,
                    })
                  : t('studio.ref_upload_title', { n: maxAttachments })}
                type="button"
                onClick={() => fileInputRef.current?.click()}
              >
                <Paperclip size={18} />
              </button>
              <ComposerSelect
                value={mode === 'video' ? videoModel : mode === 'text' ? textModel : mode === 'music' ? musicModel : imageModel}
                onChange={(v) => mode === 'video' ? setVideoModel(v) : mode === 'text' ? setTextModel(v) : mode === 'music' ? setMusicModel(v) : setImageModel(v)}
                options={(mode === 'video' ? videoModels : mode === 'text' ? textModels : mode === 'music' ? musicModels : imageModels).map((m) => ({ value: m.code, label: m.label }))}
                wide
                emptyLabel={
                  mode === 'video'
                    ? t('models.empty_video')
                    : mode === 'text'
                      ? t('models.empty_text')
                      : mode === 'music'
                        ? t('models.empty_music')
                        : t('models.empty_image')
                }
              />
              {mode === 'image' && (
                <>
                  <ComposerSelect value={imageRatio} onChange={(v) => setImageRatio(v as typeof IMAGE_RATIOS[number])} options={IMAGE_RATIOS.map((r) => ({ value: r, label: r }))} />
                  <ComposerSelect value={imageResolution} onChange={(v) => setImageResolution(v as typeof IMAGE_RESOLUTIONS[number])} options={IMAGE_RESOLUTIONS.map((r) => ({ value: r, label: r }))} />
                  <ComposerSelect value={String(count)} onChange={(v) => setCount(Number(v))} options={[1, 2, 4].map((n) => ({ value: String(n), label: t('studio.n_pieces', { n }) }))} />
                </>
              )}
              {mode === 'video' && (() => {
                // 免额度 Imagine Pipeline 通道（已下线但保留前端兼容）由服务端固定 6s + 服务端定比例。
                // 其他模型按 VIDEO_MODEL_SPECS 表约束 ratio / duration / refs 上限：
                //   sora=4/8/12s × 16:9 / 9:16 × 1 张参考图
                //   veo3.1=4/6/8s × 16:9 / 9:16 × 3 张主体参考图
                //   veo3.1-flash/lite=4/6/8s × 16:9 / 9:16 × 2 张首尾帧
                //   grok-imagine-video=6/10/15s × 16:9 / 9:16 / 1:1 × 7 张
                const isPipelineFree = videoModel === 'grok-imagine-video-6s-free';
                return (
                  <>
                    <ComposerSelect
                      value={isPipelineFree ? 'auto' : videoRatio}
                      onChange={(v) => setVideoRatio(v as typeof VIDEO_RATIOS[number])}
                      options={isPipelineFree
                        ? [{ value: 'auto', label: t('studio.ratio_follow_ref') }]
                        : currentVideoSpec.ratios.map((r) => ({ value: r, label: r }))}
                      disabled={isPipelineFree}
                    />
                    <ComposerSelect
                      value={String(isPipelineFree ? 6 : duration)}
                      onChange={(v) => setDuration(Number(v) as typeof VIDEO_DURATIONS[number])}
                      options={isPipelineFree
                        ? [{ value: '6', label: t('studio.free_channel_fixed_6s') }]
                        : currentVideoSpec.durations.map((n) => ({ value: String(n), label: `${n}s` }))}
                      disabled={isPipelineFree}
                    />
                  </>
                );
              })()}
            </div>
            <div className="flex items-center gap-2">
              <span className="hidden text-sm text-text-tertiary sm:inline">{typeof expectedCost === 'number' ? `${fmtPoints(expectedCost)} ${t('common.points')}` : expectedCost}</span>
              <button className="grid h-8 w-8 place-items-center rounded-full text-text-secondary hover:bg-surface-2" title={t('studio.voice_input')} type="button">
                <Mic size={18} />
              </button>
              {(() => {
                // submitting 只看「这一次 POST 是否在飞」：是 → 短暂 spinner + 禁用，
                // 避免一次点击连发多次。POST 一返回 (~几百 ms) 就立刻还原成 ArrowUp，
                // 让用户能看到"按钮已就绪、可以再提交一条"。
                // hasPendingTask（后台 polling 中）不再影响这个按钮的外观，
                // 否则只要还有任务在跑按钮就一直转圈，看起来像锁住了。
                const submitting =
                  mode === 'text'
                    ? createText.isPending
                    : mode === 'video'
                      ? createVideo.isPending
                      : mode === 'music'
                        ? createMusic.isPending
                        : createImage.isPending;
                // 当前模式下没有可用模型时（运营把整个 kind 全禁了 / 后端返空），
                // 禁用提交按钮 + 提示文案，避免用户点了发请求拿到 400 "model not found"。
                const noModel =
                  (mode === 'text' && textModels.length === 0) ||
                  (mode === 'video' && videoModels.length === 0) ||
                  (mode === 'music' && musicModels.length === 0) ||
                  (mode === 'image' && imageModels.length === 0);
                return (
                  <button
                    type="button"
                    onClick={submit}
                    disabled={submitting || noModel}
                    className="grid h-10 w-10 place-items-center rounded-full bg-klein text-text-on-klein transition hover:bg-klein-700 disabled:cursor-not-allowed disabled:bg-surface-3 disabled:text-text-disabled"
                    title={
                      noModel
                        ? t(
                            mode === 'video'
                              ? 'models.no_model_hint_video'
                              : mode === 'text'
                                ? 'models.no_model_hint_text'
                                : mode === 'music'
                                  ? 'models.no_model_hint_music'
                                  : 'models.no_model_hint_image',
                          )
                        : hasPendingTask
                          ? t('studio.generating_hint')
                          : t('studio.generate')
                    }
                  >
                    {submitting ? <Loader2 size={18} className="animate-spin" /> : <ArrowUp size={19} />}
                  </button>
                );
              })()}
            </div>
          </div>
          {attachments.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {attachments.map((item) => (
                <div key={item.id} className="group relative h-14 w-14 overflow-hidden rounded-[12px] bg-surface-2">
                  <img src={item.dataUrl} alt={item.name} className="h-full w-full object-cover" />
                  <button
                    type="button"
                    onClick={() => setAttachments((prev) => prev.filter((x) => x.id !== item.id))}
                    className="absolute right-1 top-1 grid h-5 w-5 place-items-center rounded-full bg-black/60 text-white opacity-0 transition group-hover:opacity-100"
                    title={t('common.remove')}
                    aria-label={t('common.remove')}
                  >
                    <X size={12} />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {mode === 'text' && textResult && (
        <section className="mx-auto mt-8 max-w-[760px] rounded-[24px] border border-border bg-surface-1 p-5 text-[15px] leading-7 text-text-primary shadow-1">
          <div className="mb-3 flex items-center justify-between text-sm text-text-tertiary">
            <span>{textModels.find((m) => m.code === textModel)?.label ?? textModel}</span>
            <span>{t('studio.text_chars_count', { n: textResult.length })}</span>
          </div>
          <div className="whitespace-pre-wrap">{textResult}</div>
        </section>
      )}

      {mode === 'image' && (
        <section className="mx-auto mt-12 max-w-[760px]">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-[20px] font-medium text-text-primary">{t('studio.examples_image_title')}</h2>
            <div className="flex items-center gap-2 text-text-tertiary">
              <button type="button" aria-label={t('studio.examples_prev')} title={t('studio.examples_prev')} className="grid h-9 w-9 place-items-center rounded-full border border-border hover:text-text-primary"><ChevronLeft size={18} /></button>
              <button type="button" aria-label={t('studio.examples_next')} title={t('studio.examples_next')} className="grid h-9 w-9 place-items-center rounded-full border border-border hover:text-text-primary"><ChevronRight size={18} /></button>
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
            {SUGGESTIONS.map((item) => {
              const title = t(item.titleKey);
              return (
                <button
                  key={item.titleKey}
                  type="button"
                  onClick={() => setPrompt(item.prompt)}
                  className="group relative aspect-[4/5] overflow-hidden rounded-[22px] text-left shadow-1"
                >
                  <img src={item.image} alt={title} className="absolute inset-0 h-full w-full object-cover transition duration-300 group-hover:scale-[1.03]" loading="lazy" />
                  <div className="absolute inset-0 bg-gradient-to-t from-black/70 via-black/10 to-transparent" />
                  <span className="absolute bottom-3 left-3 right-3 text-sm font-medium text-white">{title}</span>
                </button>
              );
            })}
          </div>
        </section>
      )}

      {mode === 'music' && (
        <section className="mx-auto mt-12 max-w-[760px]">
          <h2 className="mb-4 text-[20px] font-medium text-text-primary">{t('studio.examples_music_title')}</h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {MUSIC_SUGGESTIONS.map((item) => (
              <button
                key={item.titleKey}
                type="button"
                onClick={() => setPrompt(item.prompt)}
                className="group flex items-center gap-3 rounded-[18px] border border-border bg-surface-1 p-3 text-left transition hover:border-klein hover:shadow-1"
              >
                <span className="grid h-11 w-11 shrink-0 place-items-center rounded-[14px] bg-klein-100 text-klein-500">
                  <Music size={20} />
                </span>
                <span className="min-w-0">
                  <span className="block text-sm font-medium text-text-primary">{t(item.titleKey)}</span>
                  <span className="mt-0.5 block truncate text-xs text-text-tertiary">{item.prompt}</span>
                </span>
              </button>
            ))}
          </div>
        </section>
      )}

      <section className="mt-14">
        <div className="mx-auto mb-4 flex max-w-[1500px] items-center justify-between gap-3 px-0">
          <h2 className="text-[20px] font-medium text-text-primary">{t('studio.my_works')}</h2>
          <div className="flex items-center gap-2">
            <ComposerSelect
              value={String(historyPageSize)}
              onChange={(v) => setHistoryPageSize(Number(v) as typeof HISTORY_PAGE_SIZES[number])}
              options={HISTORY_PAGE_SIZES.map((n) => ({ value: String(n), label: t('studio.page_size_label', { n }) }))}
            />
            <HistoryActionMenu
              disabled={!token || deleteHistory.isPending}
              onDeleteBefore3Days={async () => {
                const ok = await confirm({
                  title: t('studio.del_3d_title'),
                  description: t('studio.del_3d_desc'),
                  tone: 'danger',
                  confirmLabel: t('studio.del_btn_clean'),
                });
                if (ok) deleteHistory.mutate('before_3d');
              }}
              onDeleteBefore7Days={async () => {
                const ok = await confirm({
                  title: t('studio.del_7d_title'),
                  description: t('studio.del_7d_desc'),
                  tone: 'danger',
                  confirmLabel: t('studio.del_btn_clean'),
                });
                if (ok) deleteHistory.mutate('before_7d');
              }}
              onDeleteAll={async () => {
                const ok = await confirm({
                  title: t('studio.del_all_title'),
                  description: t('studio.del_all_desc'),
                  tone: 'danger',
                  confirmLabel: t('studio.del_btn_clear'),
                });
                if (ok) deleteHistory.mutate('all');
              }}
            />
          </div>
        </div>
        {resultItems.length === 0 ? (
          <div className="mx-auto grid max-w-[1500px] place-items-center rounded-[24px] border border-dashed border-border py-14 text-text-tertiary">
            <FileImage size={28} />
            <p className="mt-2 text-sm">{token ? t('history.empty_desc') : t('auth.gate_default_hint')}</p>
          </div>
        ) : (
          <div
            className="mx-auto max-w-[1500px] columns-1 gap-3 sm:columns-2 lg:columns-3 xl:columns-4 2xl:columns-5"
            style={{ columnWidth: '220px' }}
          >
            {resultItems.map((item) =>
              item.kind === 'music'
                ? <MusicCard key={item.task_id} item={item} />
                : <WorkCard key={item.task_id} item={item} onOpen={setPreview} />,
            )}
          </div>
        )}
      </section>
      {preview && <PreviewLightbox preview={preview} onClose={() => setPreview(null)} />}
      {confirmDialog}
    </div>
  );
}

function ModeSwitch({ mode, onChange }: { mode: StudioMode; onChange: (mode: StudioMode) => void }) {
  const { t } = useTranslation();
  return (
    <div className="inline-flex rounded-full bg-surface-2 p-1">
      {MODES.map((m) => {
        const Icon = m.icon;
        return (
          <button
            key={m.value}
            type="button"
            onClick={() => onChange(m.value)}
            className={clsx(
              'inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-sm transition',
              mode === m.value
                ? 'bg-surface-1 text-text-primary shadow-1'
                : 'text-text-secondary hover:text-text-primary',
            )}
          >
            <Icon size={15} />
            {t(m.labelKey)}
          </button>
        );
      })}
    </div>
  );
}

function ComposerSelect({ value, options, onChange, disabled, wide, emptyLabel }: { value: string; options: { value: string; label: string }[]; onChange: (value: string) => void; disabled?: boolean; wide?: boolean; emptyLabel?: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const current = options.find((o) => o.value === value) ?? options[0];
  // 空 options：禁用按钮 + 显示 emptyLabel 文案（如"暂无接入模型"），
  // 避免之前那种空紫椭圆既无文字又能展开的奇怪状态。
  const isEmpty = options.length === 0;
  const effectiveDisabled = disabled || isEmpty;
  const displayLabel = isEmpty ? (emptyLabel ?? t('studio.composer_no_options')) : current?.label;

  return (
    <div
      className="relative"
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button
        type="button"
        disabled={effectiveDisabled}
        onClick={() => setOpen((v) => !v)}
        className={clsx(
          'inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-sm outline-none transition',
          wide ? 'min-w-[240px] max-w-[360px] justify-between' : 'min-w-[72px] justify-between',
          effectiveDisabled
            ? 'cursor-not-allowed text-text-disabled hover:bg-transparent'
            : open
              ? 'bg-klein-100 text-klein-500'
              : 'text-klein-500 hover:bg-surface-2',
        )}
      >
        <span className={clsx('min-w-0 truncate whitespace-nowrap text-left', wide && 'flex-1')}>
          {displayLabel}
        </span>
        <ChevronDown size={15} className={clsx('shrink-0 transition', open && 'rotate-180')} />
      </button>

      {open && !effectiveDisabled && (
        <div
          className={clsx(
            'absolute left-0 top-10 z-30 overflow-hidden rounded-[18px] border border-border bg-surface-1 p-1.5 shadow-3',
            wide ? 'min-w-[320px] max-w-[420px]' : 'min-w-[132px]',
          )}
        >
          {options.map((o) => {
            const selected = o.value === value;
            return (
              <button
                key={o.value}
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => {
                  onChange(o.value);
                  setOpen(false);
                }}
                className={clsx(
                  'flex min-h-10 w-full items-center justify-between gap-3 rounded-[12px] px-3 py-2 text-left text-sm transition',
                  selected
                    ? 'bg-surface-2 text-text-primary'
                    : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary',
                )}
              >
                <span className="min-w-0 flex-1 truncate whitespace-nowrap leading-5">{o.label}</span>
                {selected && <Check size={16} className="shrink-0" />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function HistoryActionMenu({
  disabled,
  onDeleteBefore3Days,
  onDeleteBefore7Days,
  onDeleteAll,
}: {
  disabled?: boolean;
  onDeleteBefore3Days: () => void;
  onDeleteBefore7Days: () => void;
  onDeleteAll: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <div
      className="relative"
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button
        type="button"
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        className="inline-flex h-8 items-center gap-1.5 rounded-full px-3 text-sm text-text-secondary outline-none transition hover:bg-surface-2 disabled:cursor-not-allowed disabled:text-text-disabled"
      >
        <MoreHorizontal size={16} />
        {t('history.manage_btn')}
      </button>
      {open && !disabled && (
        <div className="absolute right-0 top-10 z-30 min-w-[150px] overflow-hidden rounded-[18px] border border-border bg-surface-1 p-1.5 shadow-3">
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              setOpen(false);
              onDeleteBefore3Days();
            }}
            className="flex h-10 w-full items-center gap-2 rounded-[12px] px-3 text-left text-sm text-text-secondary transition hover:bg-surface-2"
          >
            <Trash2 size={15} />
            {t('history.del_before_3d')}
          </button>
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              setOpen(false);
              onDeleteBefore7Days();
            }}
            className="flex h-10 w-full items-center gap-2 rounded-[12px] px-3 text-left text-sm text-text-secondary transition hover:bg-surface-2"
          >
            <Trash2 size={15} />
            {t('history.del_before_7d')}
          </button>
          <button
            type="button"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => {
              setOpen(false);
              onDeleteAll();
            }}
            className="flex h-10 w-full items-center gap-2 rounded-[12px] px-3 text-left text-sm text-danger transition hover:bg-danger-soft"
          >
            <Trash2 size={15} />
            {t('history.del_all')}
          </button>
        </div>
      )}
    </div>
  );
}

function WorkCard({ item, onOpen }: { item: GenerationTask; onOpen: (preview: { url: string; type: 'image' | 'video'; title: string }) => void }) {
  const { t } = useTranslation();
  const result = item.results?.[0];
  const thumb = result?.thumb_url;
  const original = result?.url;
  const [thumbFailed, setThumbFailed] = useState(false);
  const [loadedRatio, setLoadedRatio] = useState<string | null>(null);
  const isVideo = item.kind === 'video';
  const showThumb = !!thumb && !thumbFailed;
  const declaredRatio = result?.width && result?.height ? `${result.width} / ${result.height}` : '';
  const mediaRatio = loadedRatio || declaredRatio || (isVideo ? '16 / 9' : '1 / 1');
  const canOpen = item.status === 2 && !!original;
  const prompt = compactPrompt(item.prompt);
  const setRatioFromImage = (el: HTMLImageElement) => {
    if (el.naturalWidth > 0 && el.naturalHeight > 0) {
      setLoadedRatio(`${el.naturalWidth} / ${el.naturalHeight}`);
    }
  };
  const setRatioFromVideo = (el: HTMLVideoElement) => {
    if (el.videoWidth > 0 && el.videoHeight > 0) {
      setLoadedRatio(`${el.videoWidth} / ${el.videoHeight}`);
    }
  };

  return (
    <article className="mb-3 break-inside-avoid overflow-hidden rounded-[6px] bg-surface-2">
      <button
        type="button"
        disabled={!canOpen}
        onClick={() => original && onOpen({ url: original, type: isVideo ? 'video' : 'image', title: item.model })}
        style={{ aspectRatio: mediaRatio }}
        className={clsx(
          'relative grid w-full place-items-center overflow-hidden text-text-tertiary transition-[height]',
          !original && item.status === 1 && 'bg-surface-1',
          canOpen && 'group cursor-zoom-in',
        )}
      >
        {original ? (
          isVideo ? (
            showThumb ? (
              <img
                src={thumb}
                alt=""
                className="h-full w-full object-cover"
                loading="lazy"
                onLoad={(e) => setRatioFromImage(e.currentTarget)}
                onError={() => setThumbFailed(true)}
              />
            ) : (
              <video
                src={original}
                className="h-full w-full object-cover"
                muted
                playsInline
                preload="metadata"
                onLoadedMetadata={(e) => setRatioFromVideo(e.currentTarget)}
              />
            )
          ) : (
            <img src={original} alt="" className="h-full w-full object-cover" loading="lazy" onLoad={(e) => setRatioFromImage(e.currentTarget)} />
          )
        ) : item.status === 1 ? (
          <GeneratingDots />
        ) : (
          <div className="flex flex-col items-center gap-2 text-sm">
            <FileImage size={24} />
            <span>{statusText(item.status, t)}</span>
          </div>
        )}
        <div className="absolute left-2 top-2 rounded-full bg-black/55 px-2 py-0.5 text-xs text-white">
          {item.kind === 'video' ? t('studio.mode_video') : t('studio.mode_image')}
        </div>
        {canOpen && (
          <div className="absolute inset-0 grid place-items-center bg-black/0 opacity-0 transition group-hover:bg-black/20 group-hover:opacity-100">
            <span className="grid h-10 w-10 place-items-center rounded-full bg-white/90 text-neutral-950 shadow-1">
              {isVideo ? <Play size={18} fill="currentColor" /> : <Maximize2 size={18} />}
            </span>
          </div>
        )}
      </button>
      <div className="flex items-center gap-1.5 px-2.5 py-2 text-xs text-text-tertiary">
        <span className="shrink-0">{fmtRelative(item.created_at)}</span>
        {prompt && <span className="truncate text-text-secondary">{prompt}</span>}
      </div>
    </article>
  );
}

function MusicCard({ item }: { item: GenerationTask }) {
  const { t } = useTranslation();
  const [showLyrics, setShowLyrics] = useState(false);
  const result = item.results?.[0];
  const meta = (result?.meta ?? {}) as Record<string, unknown>;
  const audioUrl = result?.url;
  const cover = result?.thumb_url;
  const title = typeof meta.title === 'string' && meta.title ? meta.title : (item.prompt || t('studio.mode_music'));
  const lyrics = typeof meta.lyrics === 'string' ? meta.lyrics : '';
  const soundPrompt = typeof meta.sound_prompt === 'string' ? meta.sound_prompt : '';
  const ready = item.status === 2 && !!audioUrl;

  return (
    <article className="mb-3 break-inside-avoid overflow-hidden rounded-[14px] border border-border bg-surface-1 shadow-1">
      <div className="relative aspect-square w-full overflow-hidden bg-surface-2">
        {cover ? (
          <img src={cover} alt={title} className="h-full w-full object-cover" loading="lazy" />
        ) : (
          <div className="grid h-full w-full place-items-center text-text-tertiary">
            {item.status === 1 ? <GeneratingDots /> : <Music size={32} />}
          </div>
        )}
        <div className="absolute left-2 top-2 rounded-full bg-black/55 px-2 py-0.5 text-xs text-white">
          {t('studio.mode_music')}
        </div>
      </div>
      <div className="p-3">
        <div className="truncate text-sm font-medium text-text-primary" title={title}>{title}</div>
        {soundPrompt && <div className="mt-0.5 truncate text-xs text-text-tertiary" title={soundPrompt}>{soundPrompt}</div>}
        {ready ? (
          <>
            <audio src={audioUrl} controls preload="none" className="mt-2 h-9 w-full">
              <track kind="captions" />
            </audio>
            <div className="mt-2 flex items-center gap-3 text-xs">
              <a href={audioUrl} download className="text-klein-500 hover:underline">{t('studio.music_download')}</a>
              {lyrics && (
                <button type="button" onClick={() => setShowLyrics((v) => !v)} className="text-text-secondary hover:text-text-primary">
                  {showLyrics ? t('studio.music_hide_lyrics') : t('studio.music_show_lyrics')}
                </button>
              )}
              <span className="ml-auto text-text-tertiary">{fmtRelative(item.created_at)}</span>
            </div>
            {showLyrics && lyrics && (
              <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap rounded-[10px] bg-surface-2 p-2 text-xs leading-5 text-text-secondary">{lyrics}</pre>
            )}
          </>
        ) : (
          <div className="mt-2 text-xs text-text-tertiary">{statusText(item.status, t)}</div>
        )}
      </div>
    </article>
  );
}

function compactPrompt(prompt?: string) {
  const text = String(prompt || '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length > 28 ? text.slice(0, 28) + '...' : text;
}

function GeneratingDots() {
  const { t } = useTranslation();
  // 每次切换语言时，phrases 重新从 i18n 里取（数组），并按当前语言渲染。
  const phrases = t('studio.generating_phrases', { returnObjects: true }) as string[] | string;
  const phraseList: string[] = Array.isArray(phrases) && phrases.length > 0 ? phrases : GENERATING_PHRASES;

  // 首屏初始化也随机：每次进生成状态进来看到的第一句不固定。
  const [phraseIndex, setPhraseIndex] = useState(() => Math.floor(Math.random() * phraseList.length));

  useEffect(() => {
    // setTimeout 链而不是 setInterval：每次切换都重新摇个新的间隔。
    // 3000 ~ 5000 ms 之间，让用户有充足时间看清这句话，不会觉得文案在闪。
    // 下一句从池子里随机选，并避开当前这一句，避免连续两次相同。
    let timer: number | null = null;
    let cancelled = false;

    const schedule = () => {
      const delay = 3000 + Math.floor(Math.random() * 2000);
      timer = window.setTimeout(() => {
        if (cancelled) return;
        setPhraseIndex((idx) => {
          if (phraseList.length <= 1) return 0;
          let next = Math.floor(Math.random() * (phraseList.length - 1));
          if (next >= idx) next += 1;
          return next;
        });
        schedule();
      }, delay);
    };

    schedule();
    return () => {
      cancelled = true;
      if (timer != null) window.clearTimeout(timer);
    };
    // 依赖 phraseList.length，语言切换时重新调度，避免 idx 越界。
  }, [phraseList.length]);

  // 越界保护：语言切换后旧 phraseIndex 可能 >= 新 phraseList.length。
  const safeIdx = phraseIndex < phraseList.length ? phraseIndex : 0;

  return (
    <div className="generating-dots" aria-label={t('studio.generating_aria')}>
      <div className="generating-dots__phrases">
        <span className="generating-dots__phrase generating-dots__phrase--active" key={safeIdx}>
          {phraseList[safeIdx]}
        </span>
      </div>
    </div>
  );
}

function PreviewLightbox({ preview, onClose }: { preview: { url: string; type: 'image' | 'video'; title: string }; onClose: () => void }) {
  const { t } = useTranslation();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4" onMouseDown={onClose}>
      <div className="relative max-h-[92vh] max-w-[92vw]" onMouseDown={(e) => e.stopPropagation()}>
        <button
          type="button"
          onClick={onClose}
          className="absolute right-3 top-3 z-10 grid h-9 w-9 place-items-center rounded-full bg-white/90 text-neutral-900 shadow-1 transition hover:bg-white"
          title={t('common.close')}
          aria-label={t('common.close')}
        >
          <X size={18} />
        </button>
        {preview.type === 'video' ? (
          <video src={preview.url} controls autoPlay className="max-h-[92vh] max-w-[92vw] rounded-[12px] bg-black shadow-2xl" />
        ) : (
          <img src={preview.url} alt={preview.title} className="max-h-[92vh] max-w-[92vw] rounded-[12px] object-contain shadow-2xl" />
        )}
      </div>
    </div>
  );
}

function modeFromPath(pathname: string): StudioMode {
  if (pathname.includes('/create/video')) return 'video';
  if (pathname.includes('/create/text')) return 'text';
  if (pathname.includes('/create/music')) return 'music';
  return 'image';
}

// modeTitle 现在传 t() 进来翻译；保留中文回退（极小概率被 caller 跳过 t 误用）。
function modeTitle(mode: StudioMode, t: (k: string) => string) {
  if (mode === 'video') return t('studio.mode_video');
  if (mode === 'text') return t('studio.mode_text');
  if (mode === 'music') return t('studio.mode_music');
  return t('studio.mode_image');
}

function statusText(status: number, t: (k: string) => string) {
  if (status === 2) return t('history.status_succeeded');
  if (status === 3) return t('history.status_failed');
  if (status === 4) return t('history.status_refunded');
  if (status === 1) return t('history.status_running');
  return t('history.status_pending');
}

function modelsByKind(models: PublicModel[] | undefined, kind: PublicModel['kind'], fallback: SelectModel[]): SelectModel[] {
  // 注意：所有 *_points / image_pricing / video_pricing 的 value 都是「分（×100 整数）」，
  // 全链路保持整数，最终 UI 展示时统一用 fmtPoints 还原成元的小数。
  //
  // 加载中（models===undefined）：用 hardcoded fallback 占位，避免下拉一开始空白闪烁；
  // 加载完成（哪怕是空数组）：**完全尊重后端**，禁用的模型 / 没启用的 kind 直接不显示。
  // 老逻辑用 `rows.length ? rows : fallback` 会让"运营把某 kind 全禁了"或"后端筛掉 0 个"
  // 时把 hardcoded 模型反而冒出来，违反"后台禁用 → 前端不显示"的直觉。
  if (models === undefined) {
    return fallback;
  }
  return models
    .filter((m) => m.enabled !== false && m.kind === kind && m.model_code)
    .map((m) => ({
      code: m.model_code,
      label: m.name || m.model_code,
      cost: typeof m.unit_points === 'number' ? m.unit_points : undefined,
      input: typeof m.input_unit_points === 'number' ? m.input_unit_points : undefined,
      output: typeof m.output_unit_points === 'number' ? m.output_unit_points : undefined,
      videoPricingMode: m.video_pricing_mode,
      imagePricing: normalizeVariantMap(m.image_pricing),
      videoPricing: normalizeVariantMap(m.video_pricing),
    }));
}

function normalizeVariantMap(src: Record<string, number> | undefined): Record<string, number> | undefined {
  if (!src) return undefined;
  const out: Record<string, number> = {};
  let touched = false;
  for (const [k, v] of Object.entries(src)) {
    if (typeof v === 'number' && v > 0) {
      // value 已经是「分（×100 整数）」，直接透传，最终展示再除 100。
      out[k.toLowerCase()] = v;
      touched = true;
    }
  }
  return touched ? out : undefined;
}

// estimateImageCost：优先 imagePricing[1k|2k|4k]，否则 model.cost。
// 返回单位：分（×100 整数），展示时用 fmtPoints。
function estimateImageCost(model: SelectModel | undefined, resolution: string, count: number): number {
  const tier = resolution.toLowerCase();
  const variant = model?.imagePricing?.[tier];
  const base = typeof variant === 'number' ? variant : (model?.cost ?? 0);
  return Math.round(base * count);
}

// estimateVideoCost：
//   - variant：先从 videoPricing[duration] 拿；map miss 退到 scaled 倍率
//   - flat：固定 base
//   - scaled（默认）：base × duration / 6
// 返回单位：分（×100 整数），展示时用 fmtPoints。
function estimateVideoCost(model: SelectModel | undefined, duration: number): number {
  const variant = model?.videoPricing?.[String(duration)];
  if (typeof variant === 'number' && variant > 0) {
    return Math.round(variant);
  }
  const baseCost = model?.cost ?? 0;
  if (model?.videoPricingMode === 'flat') {
    return Math.round(baseCost);
  }
  return Math.round((baseCost * duration) / 6);
}
