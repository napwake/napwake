// robots.txt: every crawler is welcome, AI crawlers included (CMO decision 0004), and pointed at the
// sitemap of the version every page names as canonical: latest once a release is out, next before
// (NAPWAKE_DOCS_CANONICAL, from hack/docsite). The site's root robots.txt is the root version's copy;
// on a build at / it's this one.
import type { APIRoute } from 'astro';

export const GET: APIRoute = ({ site }) => {
  const version = import.meta.env.NAPWAKE_DOCS_VERSION || 'dev';
  const base = import.meta.env.BASE_URL.replace(/\/?$/, '/');
  const canonical = import.meta.env.NAPWAKE_DOCS_CANONICAL || 'latest';
  const sitemapBase = version === 'dev' ? base : base.replace(/[^/]+\/$/, `${canonical}/`);
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
