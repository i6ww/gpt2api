// i18n 入口：根据 user 偏好（localStorage > 浏览器语言 > zh-CN 兜底）初始化 i18next。
//
// 加载方式：所有词条 import 进来内联到 bundle 里。当前体量小（一个 page 100-200 条），
// 不需要 dynamic import / chunk split；后续如果某语言 > 50KB 再换成 lazy load。
//
// 使用方式：
//   import { useTranslation } from 'react-i18next';
//   const { t } = useTranslation();
//   t('common.submit')                  // "提交" / "Submit"
//   t('errors.no_account')              // 带变量也支持: t('plan.points', { n: 100 })
//
// 切换语言：
//   import { useTranslation } from 'react-i18next';
//   const { i18n } = useTranslation();
//   i18n.changeLanguage('en');          // 同时会写 localStorage

import i18n from 'i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { initReactI18next } from 'react-i18next';

import zh from './locales/zh.json';
import en from './locales/en.json';

export const SUPPORTED_LANGS = ['zh', 'en'] as const;
export type Lang = (typeof SUPPORTED_LANGS)[number];

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      zh: { translation: zh },
      en: { translation: en },
    },
    // 站点默认语言：英文。
    // 用户主动在 UI 里点过语言切换器（写到 localStorage.klein.lang）后，
    // 之后所有访问都按该偏好走；没设置过 → 显示 'en'，不再跟浏览器走（避免
    // 中文浏览器默认就把站点变中文，与运营希望的"全球默认英文"诉求相悖）。
    lng: undefined, // 让 detector 决定；detector 顺序里没有 navigator 就只剩 fallback
    fallbackLng: 'en',
    supportedLngs: SUPPORTED_LANGS,
    nonExplicitSupportedLngs: true, // 'zh-CN' / 'zh-TW' / 'zh-HK' 都匹配到 'zh'
    interpolation: {
      escapeValue: false, // React 自带 XSS 防护，不需要 i18next 再 escape
    },
    detection: {
      // 只看 localStorage：用户切换后持久化；从未设置 → 走 fallbackLng=en
      order: ['localStorage'],
      lookupLocalStorage: 'klein.lang',
      caches: ['localStorage'],
    },
  });

export default i18n;
