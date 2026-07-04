// LanguageSwitcher 语言切换器。
//
// 三种 variant：
//   - 'rail'   : 圆形 9x9 图标按钮，跟桌面左侧栏 RailLink 同款风格，下拉弹右侧
//   - 'pill'   : 带文字胶囊按钮（"中文 ⌄"），用于顶栏 / 设置页等横向布局，下拉弹下方
//   - 'menu'   : 抽屉里的菜单条，全宽 + 左对齐，已选语言高亮
//
// 切换后调 i18n.changeLanguage 写 localStorage，React 自动重渲染所有 useTranslation。
//
// 路径：暂时只支持 zh / en；加 ja / ko 时往 SUPPORTED_LANGS 加一项即可。

import { Check, Globe2 } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { SUPPORTED_LANGS, type Lang } from '../i18n';

const LANG_LABELS: Record<Lang, string> = {
  zh: '中文',
  en: 'English',
};

export type LanguageSwitcherVariant = 'rail' | 'pill' | 'menu';

export function LanguageSwitcher({ variant = 'pill' }: { variant?: LanguageSwitcherVariant }) {
  const { i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const current = (i18n.resolvedLanguage as Lang | undefined) ?? 'zh';

  const onPick = (lang: Lang) => {
    if (lang !== current) {
      void i18n.changeLanguage(lang);
    }
    setOpen(false);
  };

  // menu variant：嵌入到抽屉/菜单里的全宽行，不带 dropdown
  if (variant === 'menu') {
    return (
      <div className="space-y-0.5">
        {SUPPORTED_LANGS.map((lang) => {
          const selected = lang === current;
          return (
            <button
              key={lang}
              type="button"
              onClick={() => onPick(lang)}
              className={
                'flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-small transition ' +
                (selected
                  ? 'bg-klein-100 font-medium text-klein-600'
                  : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary')
              }
            >
              <span className="flex items-center gap-2">
                <Globe2 size={14} />
                {LANG_LABELS[lang]}
              </span>
              {selected && <Check size={14} />}
            </button>
          );
        })}
      </div>
    );
  }

  // rail variant：圆形 9x9 图标按钮（跟 RailLink 一致），下拉**弹到右侧**避免被左侧栏裁掉
  if (variant === 'rail') {
    return (
      <div
        className="relative"
        onBlur={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
        }}
      >
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          title={`Language: ${LANG_LABELS[current]}`}
          className={
            'grid h-9 w-9 place-items-center rounded-full transition ' +
            (open
              ? 'bg-surface-2 text-text-primary'
              : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary')
          }
        >
          <Globe2 size={18} />
        </button>
        {open && (
          <div className="absolute bottom-0 left-full z-50 ml-2 min-w-[140px] overflow-hidden rounded-xl border border-border bg-surface-1 p-1 shadow-3">
            {SUPPORTED_LANGS.map((lang) => {
              const selected = lang === current;
              return (
                <button
                  key={lang}
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => onPick(lang)}
                  className={
                    'flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-small transition ' +
                    (selected
                      ? 'bg-klein-100 font-medium text-klein-600'
                      : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary')
                  }
                >
                  <span>{LANG_LABELS[lang]}</span>
                  {selected && <Check size={14} />}
                </button>
              );
            })}
          </div>
        )}
      </div>
    );
  }

  // pill variant（默认）：胶囊按钮 + 文字，下拉弹下方右对齐
  return (
    <div
      className="relative"
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={
          'inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-small text-text-secondary transition hover:bg-surface-2 hover:text-text-primary ' +
          (open ? 'bg-surface-2 text-text-primary' : '')
        }
        title="Language / 语言"
      >
        <Globe2 size={14} />
        <span>{LANG_LABELS[current]}</span>
      </button>
      {open && (
        <div className="absolute right-0 top-9 z-50 min-w-[140px] overflow-hidden rounded-xl border border-border bg-surface-1 p-1 shadow-3">
          {SUPPORTED_LANGS.map((lang) => {
            const selected = lang === current;
            return (
              <button
                key={lang}
                type="button"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => onPick(lang)}
                className={
                  'flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-small transition ' +
                  (selected
                    ? 'bg-klein-100 font-medium text-klein-600'
                    : 'text-text-secondary hover:bg-surface-2 hover:text-text-primary')
                }
              >
                <span>{LANG_LABELS[lang]}</span>
                {selected && <Check size={14} />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
