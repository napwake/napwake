// Social cards (og:image), one per page, in the brand's launch card style: Lamp ground, the wordmark,
// the page's section and title in Big Shoulders, its description in the mono, and the doorman
// in a pose for the page's job. Drawn after the build with Chrome (the one Mermaid uses), cached by
// what's on them. Without Chrome, every page gets the brand's launch card instead.
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { pose } from '../rig/draw.mjs';
import { chromium } from 'playwright';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CACHE = join(ROOT, 'node_modules/.cache/napwake-og');
const VERSION = 3; // bump when the card's design changes

// the doorman's pose for each page: a real job of Napwake's, or the default, on duty
const POSE = [
  [/^$/, 'door'], [/^concepts\/wake-on-request/, 'door'], [/^concepts\/forecasting/, 'list'], [/^concepts\/idle-detection/, 'watch'],
  [/^concepts\/lifecycle|^guides\/pause/, 'lights'], [/^getting-started\/kubectl-plugin|^guides\/dry-run/, 'rounds'],
  [/^getting-started\//, 'point'], [/^operations\/troubleshooting/, 'note'], [/^operations\/upgrading|^contributing/, 'tip'],
];
const poseFor = (slug) => (POSE.find(([re]) => re.test(slug)) ?? [null, 'duty'])[1];
export const cardName = (slug) => (slug ? slug.replace(/\/$/, '').replace(/\//g, '-') : 'index') + '.png';

const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);
const text = (html) => html.replace(/<[^>]+>/g, '').replace(/&#39;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/\s+/g, ' ').trim();
const font = (file) => `url(data:font/woff2;base64,${readFileSync(join(ROOT, 'src/fonts', file)).toString('base64')}) format('woff2')`;

function cardHtml({ eyebrow, title, description, poseName }) {
  const tokens = readFileSync(join(ROOT, 'src/styles/tokens.css'), 'utf8');
  const wordmark = readFileSync(join(ROOT, 'src/assets/brand/wordmark.svg'), 'utf8');
  const p = pose(poseName, { finish: true, wobble: 1.4, grain: 0.13 });
  p.svg = p.svg.replace(/nw[0-9a-z]+-/g, 'card-'); // the rig's ids differ run to run; the card's mustn't
  const size = title.length <= 18 ? 116 : title.length <= 30 ? 96 : title.length <= 48 ? 80 : 66;
  return `<!doctype html><html data-theme="light"><head><meta charset="utf-8"><style>
${tokens}
@font-face { font-family: 'Big Shoulders Display'; src: ${font('BigShouldersDisplay-800.woff2')}; font-weight: 800; }
@font-face { font-family: 'Atkinson Hyperlegible Mono'; src: ${font('AtkinsonHyperlegibleMono-400.woff2')}; font-weight: 400; }
@font-face { font-family: 'Atkinson Hyperlegible Mono'; src: ${font('AtkinsonHyperlegibleMono-700.woff2')}; font-weight: 700; }
html, body { margin: 0; width: 1200px; height: 630px; overflow: hidden; }
body { position: relative; background: var(--lamp); color: var(--night); }
.floor { position: absolute; left: 0; right: 0; bottom: 0; height: 44px; background: var(--launch-floor); }
.mark { position: absolute; left: 72px; top: 66px; display: flex; align-items: center; gap: 22px; }
.mark svg { width: 236px; height: auto; display: block; }
.mark span { font: 700 22px/1 'Atkinson Hyperlegible Mono'; letter-spacing: .06em; padding-left: 22px; border-left: 2px solid rgba(15,23,22,.35); }
.text { position: absolute; left: 72px; top: 178px; width: 690px; }
.eyebrow { font: 700 22px/1.2 'Atkinson Hyperlegible Mono'; letter-spacing: .08em; text-transform: uppercase; margin: 0 0 18px; opacity: .78; }
h1 { margin: 0; font: 800 ${size}px/.95 'Big Shoulders Display'; letter-spacing: .004em; }
p.d { margin: 26px 0 0; font: 400 23px/1.45 'Atkinson Hyperlegible Mono'; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.doorman { position: absolute; right: 58px; bottom: 18px; width: 390px; height: auto; }
</style></head><body>
<div class="floor"></div>
<div class="mark">${wordmark.replace('<svg ', '<svg aria-hidden="true" ')}<span>DOCS</span></div>
<div class="text">${eyebrow ? `<p class="eyebrow">${esc(eyebrow)}</p>` : ''}<h1>${esc(title)}</h1>${description ? `<p class="d">${esc(description)}</p>` : ''}</div>
<svg class="doorman" viewBox="${p.viewBox}" xmlns="http://www.w3.org/2000/svg">${p.svg}</svg>
</body></html>`;
}

export function ogCards() {
  return {
    name: 'napwake-og-cards',
    hooks: {
      'astro:build:done': async ({ dir, pages, logger }) => {
        const out = new URL('og/', dir);
        mkdirSync(out, { recursive: true });
        const cards = [];
        for (const { pathname } of pages) {
          const slug = pathname.replace(/^\/+|\/+$/g, '');
          if (slug === '404') continue;
          const file = new URL(slug ? `${slug}/index.html` : 'index.html', dir);
          if (!existsSync(file)) continue;
          const html = readFileSync(file, 'utf8');
          const h1 = html.match(/<h1 id="_top"[^>]*>([\s\S]*?)<\/h1>/);
          const eyebrow = html.match(/class="nw-(?:title|hero)__eyebrow[^"]*"[^>]*>([\s\S]*?)<\/p>/);
          const lede = html.match(/class="nw-title__lede[^"]*"[^>]*>([\s\S]*?)<\/p>/);
          const desc = lede ?? html.match(/<meta name="description" content="([^"]*)"/);
          const title = h1 ? text(h1[1]) : slug;
          const data = { eyebrow: eyebrow && slug ? text(eyebrow[1]) : '', title, description: slug ? (desc ? text(desc[1]) : '') : 'Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request.', poseName: poseFor(slug) };
          cards.push({ name: cardName(slug), html: cardHtml(data) });
        }
        mkdirSync(CACHE, { recursive: true });
        const todo = [];
        for (const c of cards) {
          c.cached = join(CACHE, createHash('sha256').update(`${VERSION}${c.html}`).digest('hex').slice(0, 16) + '.png');
          if (!existsSync(c.cached)) todo.push(c);
        }
        if (todo.length) {
          try {
            const executablePath = process.env.MERMAID_CHROME;
            const browser = await chromium.launch(executablePath ? { executablePath } : { channel: 'chrome' });
            const page = await browser.newPage({ viewport: { width: 1200, height: 630 } });
            for (const c of todo) {
              await page.setContent(c.html, { waitUntil: 'load' });
              await page.evaluate(() => document.fonts.ready);
              writeFileSync(c.cached, await page.screenshot({ type: 'png' }));
            }
            await browser.close();
          } catch (e) {
            logger.warn(`social cards: Chrome couldn't draw them (${e.message.split('\n')[0]}); every page gets the launch card`);
            for (const c of cards) copyFileSync(join(ROOT, 'src/og/launch-card.png'), new URL(c.name, out));
            return;
          }
        }
        for (const c of cards) copyFileSync(c.cached, new URL(c.name, out));
        logger.info(`social cards: ${cards.length} (${todo.length} drawn, the rest from the cache)`);
      },
    },
  };
}
