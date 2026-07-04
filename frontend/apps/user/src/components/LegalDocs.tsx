// LegalDocs.tsx
//
// 注册 / 登录页面用的「服务条款 + 隐私政策」公共组件：
//
//   1. `<LegalAgreement />` —— 必填勾选框 + 行内可点击的协议链接，
//      调用方传 onChange + value 来做表单校验；
//   2. `<LegalDocsModal />` —— 双 tab 弹层（合同 + 隐私），点击协议链接自动打开；
//   3. 文档正文以纯 React 节点写在 `TERMS_SECTIONS` / `PRIVACY_SECTIONS`，
//      不依赖任何 markdown 库。
//
// 设计目标：
//   - 用户必须 **主动** 勾选才能提交（默认不勾），满足国内强同意要求。
//   - 协议内容尽量做尽职披露 + 通用免责，把法律风险压到最低。
//   - 任何关键事项（数据上传到第三方 AI、跨境传输、未成年人、内容审核）
//     都明确告知。
//
// 维护提示：
//   - 若改了 BRAND_NAME / CONTACT_EMAIL / EFFECTIVE_DATE，记得同步通知运营，
//     用户重新登录时会再看一次（不强制弹层，但勾选状态不持久）。
//   - 不要轻易把"生效日期"提前；条款更新时建议改成"自 YYYY-MM-DD 起生效"
//     并提示已注册用户重新确认。

import { X } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

// === 站点常量（改这里 + 文档版本号即可，不需要动正文）===
const BRAND_NAME_ZH = 'AI 一站式·创意生成';
const BRAND_NAME_EN = 'AI All-in-One Creative Studio';
const SITE_DOMAIN = 'gpt2api.com';
const CONTACT_EMAIL = 'support@gpt2api.com';
const EFFECTIVE_DATE = '2026-05-25';

// 协议版本号，跟随正文一起修改。前端勾选状态可按版本号决定要不要重新弹窗确认。
export const LEGAL_DOC_VERSION = 'v1.0-2026-05-25';

// ============================================================================
// 1. 服务条款（中文版）
// ============================================================================
const TERMS_SECTIONS_ZH: { title: string; body: ReactNode }[] = [
  {
    title: '一、服务说明',
    body: (
      <>
        <p>
          欢迎使用 {BRAND_NAME_ZH}（{SITE_DOMAIN}，以下简称「本平台」）。本平台提供基于第三方人工智能模型（包括但不限于 OpenAI、Anthropic、Google Gemini、xAI Grok 等）的 AIGC 内容生成与转发服务，涵盖文本对话、图像生成、视频生成等能力。
        </p>
        <p>
          您在注册、登录、使用本平台任何功能之前，应当仔细阅读并理解本《服务条款》和《隐私政策》的全部内容。一旦您勾选「我已阅读并同意」并完成注册或登录，即视为您已充分理解并接受本协议全部条款。
        </p>
      </>
    ),
  },
  {
    title: '二、账户注册与安全',
    body: (
      <>
        <p>
          1. 您应当使用真实、合法、有效的账号信息（邮箱 / 手机号 / 用户名）进行注册，不得冒用、盗用他人身份。
        </p>
        <p>
          2. 账户密码由您本人保管，本平台不会以任何方式向您索取密码。因您本人保管不善（包括但不限于将账号借予他人、密码泄露、使用弱密码）导致的损失由您自行承担。
        </p>
        <p>
          3. 您应当对在本账户下发生的全部操作（包括但不限于内容生成、付费消费、API 调用）承担全部法律责任。
        </p>
        <p>
          4. 如发现账户被盗用或存在异常，请立即通过 <code className="font-mono">{CONTACT_EMAIL}</code> 联系本平台冻结账户。
        </p>
      </>
    ),
  },
  {
    title: '三、用户行为规范',
    body: (
      <>
        <p>您承诺在使用本平台过程中不会上传、生成、传播任何如下内容或从事如下行为：</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>违反中华人民共和国法律法规或您所在地法律法规的内容；</li>
          <li>反对宪法所确定的基本原则、危害国家安全、泄露国家秘密、颠覆国家政权、破坏国家统一的内容；</li>
          <li>损害国家荣誉与利益、煽动民族仇恨与歧视、破坏民族团结的内容；</li>
          <li>宣扬恐怖主义、极端主义、邪教、迷信，或宣扬暴力、淫秽色情、赌博、毒品的内容；</li>
          <li>侮辱、诽谤他人，侵害他人名誉、肖像、隐私、知识产权及其他合法权益的内容；</li>
          <li>未成年人不宜接触的内容，或以未成年人为对象生成不当内容；</li>
          <li>含有虚假广告、欺诈、传销、洗钱、非法集资等违法商业内容；</li>
          <li>利用平台进行刷量、薅羊毛、滥用免费额度、绕过付费 / 风控、批量爬取、二次售卖账号或 API Key 的行为；</li>
          <li>通过逆向工程、自动化脚本、DDoS、漏洞利用等手段破坏平台或上游 AI 服务正常运行的行为；</li>
          <li>其他法律法规、监管要求、平台规则明文禁止的内容或行为。</li>
        </ul>
        <p>
          一经发现，本平台有权立即冻结或注销您的账户、停止全部服务、不予退还已消费的点数 / 充值款项，并向有权机关报告。由此产生的全部法律后果由您本人承担。
        </p>
      </>
    ),
  },
  {
    title: '四、生成内容与知识产权',
    body: (
      <>
        <p>
          1. 您通过本平台输入的提示词（Prompt）、参考图片、参考视频等输入内容由您自行确保拥有合法授权，本平台不对输入内容的合法性、原创性、真实性承担审查义务或连带责任。
        </p>
        <p>
          2. 本平台调用的第三方 AI 模型生成的内容（图像、视频、文本等，以下简称「输出内容」）在适用法律允许的范围内归您所有 / 由您使用，但请注意：
        </p>
        <ul className="ml-5 list-disc space-y-1">
          <li>不同 AI 模型提供商对其模型生成内容有各自的许可与商用规则（如 OpenAI、Google 等），您在商业使用前应当阅读并遵守对应提供商的服务条款；</li>
          <li>AI 模型生成的内容可能与他人已存在的作品相似甚至雷同，本平台不对生成结果的独创性、可商用性、不侵权性做任何明示或默示的保证；</li>
          <li>若您将输出内容用于商业用途、对外传播或进一步训练其他 AI 模型，应当自行评估并承担相应法律风险。</li>
        </ul>
        <p>
          3. 本平台的所有页面设计、代码、商标、品牌名称、文案、技术架构等知识产权归本平台或其许可人所有，未经书面许可不得复制、修改、分发或用于商业目的。
        </p>
      </>
    ),
  },
  {
    title: '五、积分、计费与退款',
    body: (
      <>
        <p>
          1. 本平台采用「点数」作为内部计费单位，用户通过充值、兑换码、邀请奖励等方式获取点数后方可调用付费功能。具体单价、套餐、汇率以平台页面实时公示为准。
        </p>
        <p>
          2. 因不可抗力、上游 AI 服务故障、网络异常等客观原因导致单次生成任务失败时，本平台将自动退还本次任务对应的预扣点数（自动入账，无需申请）。
        </p>
        <p>
          3. 已成功生成内容并交付到您账户的任务，所扣点数不予退还。
        </p>
        <p>
          4. 充值后的点数原则上不支持提现 / 兑换为人民币。如确有特殊情况需要退款，请联系客服并提供完整充值凭证，本平台将依据法律法规及实际情况协商处理。
        </p>
      </>
    ),
  },
  {
    title: '六、第三方服务与免责声明',
    body: (
      <>
        <p>
          1. 本平台的核心 AIGC 能力依托第三方人工智能模型提供商，您使用相应功能即视为同时接受该提供商的服务条款。
        </p>
        <p>
          2. 第三方服务的可用性、稳定性、内容合规性由提供方负责。在如下情形下，本平台不承担直接或间接责任：
        </p>
        <ul className="ml-5 list-disc space-y-1">
          <li>第三方服务因维护、升级、故障、被监管限制等原因暂停或终止；</li>
          <li>第三方服务对输入或输出的内容做出审核、拒绝、模糊化等处理；</li>
          <li>第三方服务的网络延迟、超时、错误返回；</li>
          <li>第三方服务对生成内容的版权 / 商用许可政策发生变更。</li>
        </ul>
        <p>
          3. 本平台不保证服务在任何特定时间内不间断、无错误、绝对安全。您理解并接受 AI 生成结果具有概率性与不可预测性，本平台不对生成内容的准确性、完整性、适用性、可商用性做出任何形式的保证。
        </p>
      </>
    ),
  },
  {
    title: '七、责任限制',
    body: (
      <>
        <p>
          在适用法律允许的最大范围内，本平台对您因使用或无法使用本平台产生的间接损失、利润损失、商誉损失、数据损失等不承担责任。本平台在任何情况下对您承担的全部直接赔偿责任累计不超过您最近 12 个月内向本平台实际支付的费用总额。
        </p>
      </>
    ),
  },
  {
    title: '八、服务变更、暂停与终止',
    body: (
      <>
        <p>
          1. 本平台有权根据业务发展、监管要求、技术演进等情况修改本协议、调整计费规则、新增 / 下线功能 / 模型，相关变更将通过站内公告、邮件等方式提前通知。
        </p>
        <p>
          2. 您如不同意修改后的条款，应当停止使用本平台。继续使用即视为接受变更后的条款。
        </p>
        <p>
          3. 您可以随时通过账号设置或联系客服注销账户。账户注销后，您账户内的点数余额、生成历史、API Key 等将一并被清除且不可恢复。
        </p>
      </>
    ),
  },
  {
    title: '九、法律适用与争议解决',
    body: (
      <>
        <p>
          本协议的订立、生效、履行、解释及争议解决均适用中华人民共和国大陆地区法律（不含冲突法规则）。本协议项下的争议应优先通过友好协商解决；协商不成的，任何一方均可向本平台运营方所在地有管辖权的人民法院提起诉讼。
        </p>
      </>
    ),
  },
  {
    title: '十、其他',
    body: (
      <>
        <p>
          1. 本协议任何条款被认定无效或不可执行的，不影响其他条款的效力。
        </p>
        <p>
          2. 本平台对本协议条款保有最终解释权。
        </p>
        <p>
          3. 联系方式：<code className="font-mono">{CONTACT_EMAIL}</code>
        </p>
      </>
    ),
  },
];

// ============================================================================
// 2. 隐私政策（中文版）
// ============================================================================
const PRIVACY_SECTIONS_ZH: { title: string; body: ReactNode }[] = [
  {
    title: '一、引言',
    body: (
      <>
        <p>
          {BRAND_NAME_ZH}（{SITE_DOMAIN}，以下简称「本平台」）非常重视您的个人信息保护。本《隐私政策》说明本平台在您注册和使用服务过程中如何收集、使用、存储、共享与保护您的个人信息，请您仔细阅读。
        </p>
        <p>
          一旦您勾选「我已阅读并同意」并完成注册或登录，即表明您已充分了解并同意本政策全部内容。
        </p>
      </>
    ),
  },
  {
    title: '二、我们收集哪些信息',
    body: (
      <>
        <p>为向您提供服务并保障平台安全，我们会在以下情形收集您的信息：</p>
        <p className="font-medium">（一）您主动提供的信息</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>账号信息：邮箱、手机号、用户名、密码（加密存储）；</li>
          <li>充值信息：充值时使用的支付订单号、金额、时间（不收集支付密码、银行卡号等敏感支付凭证，这些由支付渠道直接处理）；</li>
          <li>使用过程产生的内容：您输入的提示词（Prompt）、上传的参考图片 / 视频、生成的图像 / 视频 / 对话文本；</li>
          <li>客服沟通记录：您通过邮件或在线渠道联系我们时提供的内容。</li>
        </ul>
        <p className="font-medium">（二）我们在服务过程中自动收集的信息</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>设备与日志信息：浏览器 User-Agent、设备型号、操作系统、IP 地址、访问时间、访问页面、错误日志；</li>
          <li>调用与计费记录：API 调用记录、模型选择、生成耗时、点数消耗、上游响应日志；</li>
          <li>Cookie / 本地存储：用于保持登录状态、记忆偏好设置（如分页大小、主题）等。</li>
        </ul>
      </>
    ),
  },
  {
    title: '三、信息用途',
    body: (
      <>
        <p>我们收集您的信息用于：</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>提供注册、登录、内容生成、付费消费等核心服务；</li>
          <li>结算积分 / 退款 / 发票等账务处理；</li>
          <li>账户安全防护：检测异常登录、防止欺诈、风控审计；</li>
          <li>服务改进：分析使用行为以优化界面与功能（仅以聚合、去标识化形式分析）；</li>
          <li>履行法律法规规定的留存与配合监管义务。</li>
        </ul>
        <p>
          我们 <strong>不会</strong> 将您的个人信息用于推送商业广告，也 <strong>不会</strong> 出售您的个人信息给任何第三方。
        </p>
      </>
    ),
  },
  {
    title: '四、信息共享与第三方处理（重要）',
    body: (
      <>
        <p>
          1. <strong>上游 AI 模型提供商</strong>：本平台调用的图像 / 视频 / 文本生成能力由第三方 AI 服务商提供（如 OpenAI、Google、Anthropic、xAI 等）。为完成您发起的生成任务，本平台会将您输入的提示词、参考素材等内容传输到对应提供商的服务器（可能位于境外）。这些数据将受到对应提供商隐私政策的约束，请您理解并接受此项必要的跨境数据传输。
        </p>
        <p>
          2. <strong>支付渠道</strong>：当您发起充值时，订单信息会传输给对应支付服务方（如微信支付、支付宝、Stripe 等），由其完成实际支付清算。
        </p>
        <p>
          3. <strong>云服务与基础设施</strong>：我们使用云厂商提供的计算、存储、CDN 等基础设施，可能依此存储您的数据；我们要求基础设施服务商签署数据保护协议并采取与本政策一致的保护措施。
        </p>
        <p>
          4. <strong>合规与法律义务</strong>：当法律法规、司法机关、监管部门要求我们披露信息时，我们将依法配合；此种披露将限制在必要范围内。
        </p>
        <p>
          除上述情形外，未经您同意，我们不会向任何第三方共享您的个人信息。
        </p>
      </>
    ),
  },
  {
    title: '五、数据存储与安全',
    body: (
      <>
        <p>
          1. 您的密码、API Key、OAuth Token 等敏感字段在数据库中以 AES-GCM 加密存储，明文不可读。
        </p>
        <p>
          2. 您生成的图像 / 视频默认存储于本平台 / 云服务商的对象存储中，您可以随时在「创作历史」中删除。删除后我们将按既定保留期清理（一般 30 天内）。
        </p>
        <p>
          3. 我们采用 HTTPS 加密传输、最小权限访问控制、操作审计等措施保护您的信息，但请理解互联网环境无法保证 100% 安全，您也应当注意保管自己的密码与 API Key。
        </p>
        <p>
          4. 一般情形下，账户信息在您主动注销账户后会被尽快清除；为应对监管 / 反欺诈 / 财务审计，部分日志类信息会按法律最低留存期保留。
        </p>
      </>
    ),
  },
  {
    title: '六、您的权利',
    body: (
      <>
        <p>根据《个人信息保护法》及相关法律，您对自己的个人信息享有以下权利：</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>访问、查阅、复制权：可在「账户设置」中查看您提交的信息与使用记录；</li>
          <li>更正、补充权：发现信息有误时可自行修改或联系客服；</li>
          <li>删除权：在符合法定条件时，您可以要求我们删除您的个人信息；</li>
          <li>撤回授权权：可以撤回此前向我们提供的同意，但这可能影响相关功能的可用性；</li>
          <li>注销账户权：通过账户设置或联系客服注销账户；</li>
          <li>投诉权：如认为本平台的信息处理行为侵害您的权益，您有权向网信、市监、公安等监管部门投诉。</li>
        </ul>
        <p>
          您可以通过 <code className="font-mono">{CONTACT_EMAIL}</code> 行使上述权利，我们会在 15 个工作日内回复。
        </p>
      </>
    ),
  },
  {
    title: '七、未成年人保护',
    body: (
      <>
        <p>
          本平台的服务面向 14 周岁以上用户。未满 14 周岁的未成年人不得注册本平台账户；如您是 14 周岁以上但未满 18 周岁的未成年人，您应当在监护人指导下阅读本政策并使用服务。
        </p>
        <p>
          如我们发现在未取得有效监护人同意的情况下收集了未成年人的个人信息，将尽快删除相关数据。
        </p>
      </>
    ),
  },
  {
    title: '八、跨境数据传输',
    body: (
      <>
        <p>
          由于本平台调用的部分 AI 服务商位于境外，您的提示词、参考素材等数据将不可避免地经由境外服务器处理。我们会采取技术与合同手段保护跨境传输的数据安全，但您应当理解并接受此种跨境传输的必要性与潜在风险。
        </p>
      </>
    ),
  },
  {
    title: '九、Cookie 与本地存储',
    body: (
      <>
        <p>
          本平台使用 Cookie 与浏览器 localStorage 保存您的登录态、偏好设置（如分页大小、主题色等）。您可以在浏览器设置中禁用 Cookie，但这可能导致部分功能（如保持登录）不可用。
        </p>
      </>
    ),
  },
  {
    title: '十、政策更新',
    body: (
      <>
        <p>
          本政策可能根据法律法规更新、业务调整等情况进行修订。重大变更时我们将通过站内公告、邮件等方式提前通知。修订后请您重新阅读并确认；若您不同意修订后的政策，应当停止使用本平台。
        </p>
      </>
    ),
  },
  {
    title: '十一、联系我们',
    body: (
      <>
        <p>
          如您对本《隐私政策》有任何疑问、意见、投诉或行使个人信息权利的请求，可以通过以下方式联系我们：
        </p>
        <p>
          邮箱：<code className="font-mono">{CONTACT_EMAIL}</code>
        </p>
        <p>
          我们将在收到请求后 15 个工作日内予以回复。
        </p>
      </>
    ),
  },
];

// ============================================================================
// 3. Terms of Service (English version)
// ============================================================================
const TERMS_SECTIONS_EN: { title: string; body: ReactNode }[] = [
  {
    title: '1. About this service',
    body: (
      <>
        <p>
          Welcome to {BRAND_NAME_EN} ({SITE_DOMAIN}, the "Platform"). The Platform provides AIGC content generation and relay services powered by third-party AI models (including but not limited to OpenAI, Anthropic, Google Gemini, xAI Grok), covering text chat, image generation and video generation.
        </p>
        <p>
          Before registering, signing in or using any feature of the Platform, you should carefully read and understand the entire Terms of Service and Privacy Policy. Once you check "I have read and agree" and complete sign-up or sign-in, you are deemed to have fully understood and accepted all clauses of this agreement.
        </p>
      </>
    ),
  },
  {
    title: '2. Account registration & security',
    body: (
      <>
        <p>1. You must use real, lawful and valid information (email / phone / username) to register. You shall not impersonate others.</p>
        <p>2. You are solely responsible for keeping your password safe. The Platform will never ask for your password. Losses caused by negligent handling of your credentials (such as sharing the account, password leakage, weak passwords) are borne by you.</p>
        <p>3. You bear full legal responsibility for all actions performed under your account, including content generation, paid consumption and API calls.</p>
        <p>4. If your account is compromised or behaves abnormally, please contact <code className="font-mono">{CONTACT_EMAIL}</code> immediately to freeze it.</p>
      </>
    ),
  },
  {
    title: '3. User conduct',
    body: (
      <>
        <p>You agree not to upload, generate, distribute any of the following content or engage in any of the following acts while using the Platform:</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Content that violates applicable laws and regulations of your jurisdiction;</li>
          <li>Content that endangers national security, sovereignty, or constitutional order, or leaks state secrets;</li>
          <li>Content that incites ethnic hatred or discrimination, or undermines national unity;</li>
          <li>Content that promotes terrorism, extremism, cults, superstition, violence, pornography, gambling, or drugs;</li>
          <li>Content that defames, harms reputation, privacy, portrait, IP or other lawful rights of others;</li>
          <li>Content inappropriate for minors, or any content targeting minors with harmful material;</li>
          <li>False advertising, fraud, MLM, money laundering or other illegal commercial content;</li>
          <li>Abuse such as bot farming, exploiting free quotas, bypassing payment / risk control, mass scraping, reselling accounts or API keys;</li>
          <li>Reverse engineering, automation scripts, DDoS, exploiting vulnerabilities, or any act that disrupts the Platform or upstream AI services;</li>
          <li>Any other content or behavior explicitly prohibited by law, regulators, or Platform rules.</li>
        </ul>
        <p>
          The Platform has the right to immediately freeze or terminate your account, stop all services, withhold consumed credits / payments, and report to authorities, once any of the above is detected. All legal consequences are borne by you.
        </p>
      </>
    ),
  },
  {
    title: '4. Generated content & IP',
    body: (
      <>
        <p>1. You are responsible for the lawful sourcing of any prompts, reference images, reference videos or other inputs. The Platform has no obligation to review the legality, originality or accuracy of inputs and bears no joint liability.</p>
        <p>2. Outputs generated by third-party AI models (images, videos, text, etc., the "Outputs") are, to the extent permitted by applicable law, yours / for your use, but please note:</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Each AI provider has its own license and commercial use rules (such as OpenAI, Google, etc.); you should read and comply with the corresponding provider's terms before commercial use;</li>
          <li>AI-generated content may be similar or identical to existing works. The Platform makes no representation or warranty regarding originality, commercial usability, or non-infringement of Outputs;</li>
          <li>If you use Outputs for commercial purposes, public distribution or further AI training, you should assess and bear the associated legal risk yourself.</li>
        </ul>
        <p>3. All UI design, code, trademarks, brand names, copy and technical architecture of the Platform belong to the Platform or its licensors; reproduction, modification, distribution or commercial use without written permission is prohibited.</p>
      </>
    ),
  },
  {
    title: '5. Credits, billing & refunds',
    body: (
      <>
        <p>1. The Platform uses "credits" as the internal billing unit. You may acquire credits via top-up, redemption code, invite reward, etc. before using paid features. Pricing, packages and exchange rates are subject to real-time display on the Platform.</p>
        <p>2. When a task fails due to force majeure, upstream AI outage or network issues, the Platform automatically refunds the pre-deducted credits (no need to file a request).</p>
        <p>3. Credits consumed on tasks that successfully delivered content to your account are non-refundable.</p>
        <p>4. Topped-up credits are generally non-withdrawable / non-convertible to cash. For special cases, please contact support with complete payment receipts; the Platform will handle in accordance with law and actual circumstances.</p>
      </>
    ),
  },
  {
    title: '6. Third-party services & disclaimers',
    body: (
      <>
        <p>1. The Platform's core AIGC capability relies on third-party AI providers. By using the corresponding feature you accept the provider's terms simultaneously.</p>
        <p>2. The availability, stability and content compliance of third-party services are the responsibility of the provider. The Platform bears no direct or indirect liability under the following circumstances:</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Third-party service is paused or terminated due to maintenance, upgrade, failure, regulatory action;</li>
          <li>Third-party service moderates, rejects, or blurs your input / output;</li>
          <li>Network delay, timeout, error returned by third-party service;</li>
          <li>Change in third-party service's copyright / commercial license policy for generated content.</li>
        </ul>
        <p>3. The Platform does not guarantee that the service is uninterrupted, error-free, or absolutely secure at all times. You understand and accept that AI generation results are probabilistic and unpredictable. The Platform makes no warranty of any kind regarding accuracy, completeness, suitability, or commercial usability of generated content.</p>
      </>
    ),
  },
  {
    title: '7. Limitation of liability',
    body: (
      <>
        <p>
          To the maximum extent permitted by applicable law, the Platform is not liable for any indirect loss, loss of profit, goodwill or data arising from your use or inability to use the Platform. The Platform's total direct compensation liability to you under no circumstance exceeds the total fees you actually paid to the Platform within the most recent 12 months.
        </p>
      </>
    ),
  },
  {
    title: '8. Service changes, suspension & termination',
    body: (
      <>
        <p>1. The Platform reserves the right to modify this agreement, adjust pricing, add / remove features / models based on business, regulatory or technical evolution. Changes will be notified in advance via in-app announcement, email, etc.</p>
        <p>2. If you do not agree with the revised terms, you should stop using the Platform. Continued use is deemed acceptance of the revised terms.</p>
        <p>3. You may close your account at any time via account settings or by contacting support. Once closed, your credit balance, generation history, API keys etc. will be wiped and cannot be recovered.</p>
      </>
    ),
  },
  {
    title: '9. Governing law & dispute resolution',
    body: (
      <>
        <p>
          The formation, validity, performance, interpretation and dispute resolution of this agreement shall be governed by the laws of the People's Republic of China (excluding conflict-of-law rules). Disputes shall first be resolved through friendly negotiation; failing that, either party may file suit in the people's court of competent jurisdiction in the place where the Platform's operator is located.
        </p>
      </>
    ),
  },
  {
    title: '10. Miscellaneous',
    body: (
      <>
        <p>1. If any clause of this agreement is held invalid or unenforceable, the remaining clauses remain effective.</p>
        <p>2. The Platform reserves the right of final interpretation of this agreement.</p>
        <p>3. Contact: <code className="font-mono">{CONTACT_EMAIL}</code></p>
      </>
    ),
  },
];

// ============================================================================
// 4. Privacy Policy (English version)
// ============================================================================
const PRIVACY_SECTIONS_EN: { title: string; body: ReactNode }[] = [
  {
    title: '1. Introduction',
    body: (
      <>
        <p>
          {BRAND_NAME_EN} ({SITE_DOMAIN}, the "Platform") takes your personal information protection seriously. This Privacy Policy explains how we collect, use, store, share and protect your personal information during registration and service use. Please read carefully.
        </p>
        <p>
          Once you check "I have read and agree" and complete sign-up or sign-in, you are deemed to have fully understood and agreed to the entire policy.
        </p>
      </>
    ),
  },
  {
    title: '2. What information we collect',
    body: (
      <>
        <p>To provide services and ensure platform safety, we collect information in the following ways:</p>
        <p className="font-medium">(a) Information you actively provide</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Account info: email, phone, username, password (stored encrypted);</li>
          <li>Top-up info: order id, amount, time of payment (we do not collect sensitive payment credentials such as PIN or card number — those are handled directly by the payment provider);</li>
          <li>Content generated during use: prompts you input, reference images / videos uploaded, images / videos / dialogues generated;</li>
          <li>Support communications: anything you submit when contacting us by email or in-app channels.</li>
        </ul>
        <p className="font-medium">(b) Information automatically collected during service</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Device & log info: browser User-Agent, device model, OS, IP, access time, visited pages, error logs;</li>
          <li>Call & billing records: API call records, model selection, generation latency, credits consumed, upstream response logs;</li>
          <li>Cookies / local storage: for session persistence and remembering preferences (page size, theme, etc.).</li>
        </ul>
      </>
    ),
  },
  {
    title: '3. How we use information',
    body: (
      <>
        <p>We use your information to:</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Provide core services: sign-up, sign-in, content generation, paid consumption;</li>
          <li>Account billing: credit settlement, refunds, invoices;</li>
          <li>Account protection: detect abnormal sign-in, prevent fraud, risk audit;</li>
          <li>Service improvement: analyze usage to optimize UI and features (only in aggregated / de-identified form);</li>
          <li>Comply with retention and cooperation obligations imposed by law.</li>
        </ul>
        <p>
          We <strong>do not</strong> use your personal info for commercial advertising, and <strong>do not</strong> sell your personal info to any third party.
        </p>
      </>
    ),
  },
  {
    title: '4. Sharing with third parties (important)',
    body: (
      <>
        <p>
          1. <strong>Upstream AI providers</strong>: image / video / text capability on the Platform is provided by third-party AI providers (such as OpenAI, Google, Anthropic, xAI). To fulfill your generation requests, the Platform transmits your prompts and reference assets to the corresponding provider's servers (which may be located outside your jurisdiction). Such data is subject to the corresponding provider's privacy policy. Please understand and accept this necessary cross-border data transfer.
        </p>
        <p>
          2. <strong>Payment channels</strong>: when you top up, order info is transmitted to the corresponding payment provider (e.g. WeChat Pay, Alipay, Stripe) to complete settlement.
        </p>
        <p>
          3. <strong>Cloud & infrastructure</strong>: we use cloud compute, storage and CDN from cloud vendors and may store your data with them; we require these vendors to sign data protection agreements and apply protections consistent with this policy.
        </p>
        <p>
          4. <strong>Legal compliance</strong>: when laws, courts or regulators require disclosure, we will cooperate within the necessary scope.
        </p>
        <p>Outside the above scenarios, we will not share your personal info with any third party without your consent.</p>
      </>
    ),
  },
  {
    title: '5. Data storage & security',
    body: (
      <>
        <p>1. Sensitive fields such as your password, API keys and OAuth tokens are stored in the database encrypted with AES-GCM; ciphertext only.</p>
        <p>2. Generated images / videos are stored in the Platform's / cloud provider's object storage by default. You may delete them at any time from Generation History. After deletion we will purge within the retention period (typically 30 days).</p>
        <p>3. We protect your information through HTTPS transport, least-privilege access control, and audit logs. However, please understand that no internet environment can guarantee 100% security. You should also keep your password and API keys safe.</p>
        <p>4. In general, account info is purged as soon as possible after you close your account; some log-type info is retained for the legal minimum period for regulatory / anti-fraud / financial audit purposes.</p>
      </>
    ),
  },
  {
    title: '6. Your rights',
    body: (
      <>
        <p>Under PIPL and relevant laws, you have the following rights with respect to your personal information:</p>
        <ul className="ml-5 list-disc space-y-1">
          <li>Access / read / copy: view your submitted info and usage records in account settings;</li>
          <li>Correct / supplement: edit incorrect info yourself or via support;</li>
          <li>Deletion: request us to delete your personal info under qualifying conditions;</li>
          <li>Withdraw consent: revoke previously granted consent (this may affect feature availability);</li>
          <li>Close account: via account settings or support;</li>
          <li>Complain: if you believe our processing violates your rights, you may complain to regulators (cyberspace, market supervision, public security, etc.).</li>
        </ul>
        <p>You may exercise the above rights via <code className="font-mono">{CONTACT_EMAIL}</code>. We will respond within 15 business days.</p>
      </>
    ),
  },
  {
    title: '7. Minors',
    body: (
      <>
        <p>This Platform is intended for users aged 14 or above. Minors under 14 may not register on the Platform; if you are a minor aged 14-18, please read this policy and use the service under guardian supervision.</p>
        <p>If we discover that we have collected personal info of minors without valid guardian consent, we will purge the data as soon as possible.</p>
      </>
    ),
  },
  {
    title: '8. Cross-border data transfer',
    body: (
      <>
        <p>Because some AI providers used by the Platform are located overseas, your prompts, reference assets and other data will inevitably be processed by overseas servers. We protect cross-border transfers with technical and contractual measures, but you should understand and accept the necessity and potential risk of such cross-border transfer.</p>
      </>
    ),
  },
  {
    title: '9. Cookies & local storage',
    body: (
      <>
        <p>The Platform uses cookies and browser localStorage to remember your session and preferences (page size, theme, etc.). You may disable cookies in your browser settings, but this may break some features (such as staying signed in).</p>
      </>
    ),
  },
  {
    title: '10. Policy updates',
    body: (
      <>
        <p>This policy may be revised based on changes in law or business. We will give advance notice via in-app announcement / email for material changes. Please re-read and confirm after revision; if you do not agree with the revised policy, you should stop using the Platform.</p>
      </>
    ),
  },
  {
    title: '11. Contact us',
    body: (
      <>
        <p>If you have any questions, comments, complaints regarding this Privacy Policy or wish to exercise your personal information rights, you can contact us at:</p>
        <p>Email: <code className="font-mono">{CONTACT_EMAIL}</code></p>
        <p>We will respond within 15 business days.</p>
      </>
    ),
  },
];

// ============================================================================
// UI 组件
// ============================================================================

export type LegalTab = 'terms' | 'privacy';

/**
 * LegalAgreement 行内同意区块：必填勾选 + 两个可点击的协议链接。
 *
 * 受控组件：调用方负责持有 checked 状态并把它送进 form schema 做校验。
 * 校验文案由调用方控制（例如 zod 报「请阅读并同意服务条款与隐私政策」）。
 */
export function LegalAgreement({
  checked,
  onChange,
  error,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  error?: string;
}) {
  const { t } = useTranslation();
  const [modalTab, setModalTab] = useState<LegalTab | null>(null);
  return (
    <>
      <label className="flex cursor-pointer items-start gap-2 text-small text-text-secondary">
        <input
          type="checkbox"
          className="checkbox mt-0.5 shrink-0"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
        />
        <span>
          {t('auth.agree_prefix')}
          <button
            type="button"
            className="mx-1 text-klein-500 hover:underline"
            onClick={() => setModalTab('terms')}
          >
            {t('auth.terms')}
          </button>
          {t('auth.agree_and')}
          <button
            type="button"
            className="mx-1 text-klein-500 hover:underline"
            onClick={() => setModalTab('privacy')}
          >
            {t('auth.privacy')}
          </button>
        </span>
      </label>
      {error && <p className="mt-1 text-tiny text-danger">{error}</p>}
      {modalTab && (
        <LegalDocsModal initialTab={modalTab} onClose={() => setModalTab(null)} />
      )}
    </>
  );
}

/**
 * LegalDocsModal 双 tab 弹层：服务条款 / 隐私政策。
 *
 * - 内容按当前 i18n 语言渲染 zh / en 不同的章节常量。
 * - 内容溢出时弹层内部滚动，整页背景不滚动。
 * - 点击遮罩或右上角 X 关闭。
 */
export function LegalDocsModal({
  initialTab,
  onClose,
}: {
  initialTab: LegalTab;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [tab, setTab] = useState<LegalTab>(initialTab);
  const isEn = (i18n.resolvedLanguage ?? 'zh').startsWith('en');
  const sections = tab === 'terms'
    ? (isEn ? TERMS_SECTIONS_EN : TERMS_SECTIONS_ZH)
    : (isEn ? PRIVACY_SECTIONS_EN : PRIVACY_SECTIONS_ZH);
  const title = tab === 'terms' ? t('auth.terms_title') : t('auth.privacy_title');
  const brandName = isEn ? BRAND_NAME_EN : BRAND_NAME_ZH;
  return (
    <div
      className="fixed inset-0 z-[100] grid place-items-center bg-surface-overlay px-4 py-6 backdrop-blur-sm"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label={title}
    >
      <div
        className="dialog-surface klein-fade-in flex h-full max-h-[85vh] w-full max-w-3xl flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-center justify-between border-b border-border px-5 py-4">
          <div className="flex items-center gap-1">
            <button
              type="button"
              className={
                'rounded-md px-3 py-1.5 text-small transition-colors ' +
                (tab === 'terms'
                  ? 'bg-klein-500/10 font-medium text-klein-500'
                  : 'text-text-secondary hover:bg-surface-2')
              }
              onClick={() => setTab('terms')}
            >
              {t('auth.terms_title')}
            </button>
            <button
              type="button"
              className={
                'rounded-md px-3 py-1.5 text-small transition-colors ' +
                (tab === 'privacy'
                  ? 'bg-klein-500/10 font-medium text-klein-500'
                  : 'text-text-secondary hover:bg-surface-2')
              }
              onClick={() => setTab('privacy')}
            >
              {t('auth.privacy_title')}
            </button>
          </div>
          <button
            type="button"
            className="btn btn-ghost btn-icon btn-sm"
            onClick={onClose}
            aria-label={t('common.close')}
          >
            <X size={16} />
          </button>
        </header>

        <div className="flex-1 overflow-y-auto px-6 py-5">
          <h2 className="mb-2 text-h3 text-text-primary">
            {brandName} · {title}
          </h2>
          <p className="mb-5 text-tiny text-text-tertiary">
            {t('auth.doc_version_prefix')} {LEGAL_DOC_VERSION} · {t('auth.doc_effective_prefix')}{EFFECTIVE_DATE}
          </p>
          <article className="space-y-5 text-small leading-relaxed text-text-secondary">
            {sections.map((s) => (
              <section key={s.title}>
                <h3 className="mb-2 text-body font-semibold text-text-primary">{s.title}</h3>
                <div className="space-y-2">{s.body}</div>
              </section>
            ))}
            <p className="border-t border-border pt-4 text-tiny text-text-tertiary">
              {t('auth.doc_footer')} <code className="font-mono">{CONTACT_EMAIL}</code>.
            </p>
          </article>
        </div>

        <footer className="flex items-center justify-end border-t border-border px-5 py-3">
          <button className="btn btn-primary btn-sm" onClick={onClose}>
            {t('common.ok')}
          </button>
        </footer>
      </div>
    </div>
  );
}
