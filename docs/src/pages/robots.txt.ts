// robots.txt: every crawler is welcome, AI crawlers included (CMO decision 0004), and pointed at the
// sitemap of latest, the version every page names as canonical. The site's root robots.txt is
// latest's copy (hack/docsite); on a build at / it's this one.
import type { APIRoute } from 'astro';

export const GET: APIRoute = ({ site }) => {
  const version = import.meta.env.NAPWAKE_DOCS_VERSION || 'dev';
  const base = import.meta.env.BASE_URL.replace(/\/?$/, '/');
  const sitemapBase = version === 'dev' ? base : base.replace(/[^/]+\/$/, 'latest/');
  const body = [
    '# Napwake docs. Every crawler is welcome, AI crawlers included.',
    'User-agent: *',
    'Allow: /',
    '',
    `Sitemap: ${new URL(`${sitemapBase}sitemap-index.xml`, site).href}`,
    '',
  ].join('\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
