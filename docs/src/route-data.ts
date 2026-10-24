// Every version's canonical URL is latest's, so search engines send readers to the current release,
// as mike's canonical_version did.
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { mdPath, ogPath } from './components/lib';

export const onRequest = defineRouteMiddleware((context) => {
  const route = context.locals.starlightRoute;
  const { head, entry, id } = route;
  const isHome = id === '' || id === 'index';
  const is404 = entry.data.title === 'Page not found';

  // the home's title is the site's, not "Napwake | Napwake"
  if (isHome) {
    const t = head.find((h) => h.tag === 'title');
    if (t) t.content = 'Napwake docs';
  }

  // a social card per page (src/og/cards.mjs), and the page as Markdown for readers and assistants
  if (!is404) {
    const card = new URL(ogPath(id), context.site).href;
    const alt = isHome ? 'Napwake docs: non-prod environments that sleep when nobody\'s using them, and wake when someone does.' : `${entry.data.title}, Napwake docs`;
    head.push(
      { tag: 'meta', attrs: { property: 'og:image', content: card }, content: '' },
      { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' }, content: '' },
      { tag: 'meta', attrs: { property: 'og:image:height', content: '630' }, content: '' },
      { tag: 'meta', attrs: { property: 'og:image:alt', content: alt }, content: '' },
      { tag: 'meta', attrs: { name: 'twitter:image', content: card }, content: '' },
      { tag: 'link', attrs: { rel: 'alternate', type: 'text/markdown', href: mdPath(id), title: 'This page as Markdown' }, content: '' },
    );
  }

  const version = import.meta.env.NAPWAKE_DOCS_VERSION || 'dev';
  if (version === 'dev' || version === 'latest') return;
  const base = import.meta.env.BASE_URL.replace(/\/?$/, '/');
  const latest = base.replace(/[^/]+\/$/, 'latest/');
  for (const h of context.locals.starlightRoute.head) {
    if (h.tag === 'link' && h.attrs?.rel === 'canonical' && typeof h.attrs.href === 'string') {
      const url = new URL(h.attrs.href);
      if (url.pathname.startsWith(base)) url.pathname = latest + url.pathname.slice(base.length);
      h.attrs.href = url.href;
    }
  }
});
