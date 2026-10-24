// Small helpers for the components.
/** A site path with this version's base: link('/guides/opt-in/') is /0.2/guides/opt-in/ on 0.2. */
export const link = (href: string) => {
  if (!href.startsWith('/') || href.startsWith('//')) return href;
  return import.meta.env.BASE_URL.replace(/\/?$/, '/') + href.slice(1);
};
export type State = 'asleep' | 'waking' | 'awake' | 'error';
export const WORDS = {
  plain: { asleep: 'Asleep', waking: 'Waking up', awake: 'Awake', error: 'Needs you' },
  product: { asleep: 'paused', waking: 'resuming', awake: 'running', error: 'needs attention' },
} as const;
/** A page's Markdown copy, with this version's base: mdPath('concepts/forecasting') is /0.2/concepts/forecasting.md. */
export const mdPath = (id: string) => import.meta.env.BASE_URL.replace(/\/?$/, '/') + (id && id !== 'index' ? id.replace(/\/$/, '') : 'index') + '.md';
/** A page's social card, written at build time by src/og/cards.mjs. */
export const ogPath = (id: string) => import.meta.env.BASE_URL.replace(/\/?$/, '/') + 'og/' + (id && id !== 'index' ? id.replace(/\/$/, '').replace(/\//g, '-') : 'index') + '.png';
