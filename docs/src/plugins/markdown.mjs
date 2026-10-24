// Sätteri hast plugins for the docs: maths with KaTeX, Mermaid drawn at build time in the brand's
// colours, and site links given the version's base path. They run before Starlight's and Expressive
// Code's, so a Mermaid block never reaches the code highlighter.
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import katex from 'katex';
import { createMermaidRenderer } from 'mermaid-isomorphic';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const classes = (n) => [].concat(n.properties?.className ?? []).map(String);
const textOf = (n) => (n.type === 'text' ? n.value : (n.children ?? []).map(textOf).join(''));

// ---------- maths ----------
// Sätteri's maths syntax ($$x$$ inline, $$ blocks) arrives as <code class="language-math math-inline">
// and <pre><code class="language-math math-display">. KaTeX writes HTML and MathML; the page loads
// KaTeX's stylesheet only when it has maths (see src/components/Head.astro).
export function mathPlugin() {
  return {
    name: 'napwake-math',
    element: {
      filter: ['code'],
      visit(node, ctx) {
        const c = classes(node);
        if (!c.includes('language-math')) return;
        const display = c.includes('math-display');
        const html = katex.renderToString(textOf(node), { displayMode: display, throwOnError: true, output: 'htmlAndMathml', strict: 'ignore' });
        if (display) {
          const pre = ctx.parent(node);
          ctx.replaceNode(pre, { type: 'raw', value: `<div class="nw-math" role="math">${html}</div>` });
        } else {
          ctx.replaceNode(node, { type: 'raw', value: html });
        }
        ctx.data.hasMath = true;
      },
    },
  };
}

// ---------- Mermaid ----------
// Drawn once per diagram with Chrome (Playwright drives the Chrome already on the machine, or
// MERMAID_CHROME's path) and cached by its source. Mermaid needs real colours, so it draws with
// sentinel colours that are then swapped for the theme's CSS variables: one SVG serves light and
// dark, and follows the theme toggle.
const SENTINELS = {
  '#010101': 'var(--nw-mm-node)',        // node fill
  '#020202': 'var(--nw-mm-node-line)',   // node border
  '#030303': 'var(--nw-mm-text)',        // text
  '#040404': 'var(--nw-mm-line)',        // edges and arrows
  '#050505': 'var(--nw-mm-cluster)',     // subgraph fill
  '#060606': 'var(--nw-mm-cluster-line)',// subgraph border
  '#070707': 'var(--nw-mm-label)',       // edge label background
  '#080808': 'var(--nw-mm-node)',        // secondary
  '#090909': 'var(--nw-mm-node)',        // tertiary
};
const MERMAID_CONFIG = {
  theme: 'base',
  fontFamily: '"Atkinson Hyperlegible Next", -apple-system, "Segoe UI", sans-serif',
  flowchart: { htmlLabels: true, curve: 'basis', padding: 14, nodeSpacing: 40, rankSpacing: 46, useMaxWidth: true },
  themeVariables: {
    fontFamily: '"Atkinson Hyperlegible Next", -apple-system, "Segoe UI", sans-serif',
    fontSize: '15px',
    background: '#ffffff',
    primaryColor: '#010101', mainBkg: '#010101', nodeBkg: '#010101',
    primaryBorderColor: '#020202', nodeBorder: '#020202',
    primaryTextColor: '#030303', textColor: '#030303', nodeTextColor: '#030303', titleColor: '#030303',
    lineColor: '#040404', defaultLinkColor: '#040404', arrowheadColor: '#040404',
    clusterBkg: '#050505', clusterBorder: '#060606',
    edgeLabelBackground: '#070707',
    secondaryColor: '#080808', tertiaryColor: '#090909',
    secondaryTextColor: '#030303', tertiaryTextColor: '#030303',
    secondaryBorderColor: '#020202', tertiaryBorderColor: '#020202',
  },
};
const CACHE = join(ROOT, 'node_modules/.cache/napwake-mermaid');
let renderer;
async function render(sources) {
  if (!renderer) {
    const executablePath = process.env.MERMAID_CHROME;
    renderer = createMermaidRenderer({ launchOptions: executablePath ? { executablePath } : { channel: 'chrome' } });
  }
  mkdirSync(CACHE, { recursive: true });
  const font = join(CACHE, 'font.css');
  if (!existsSync(font)) {
    const woff = pathToFileURL(join(ROOT, 'src/fonts/AtkinsonHyperlegibleNext.woff2')).href;
    writeFileSync(font, `@font-face{font-family:"Atkinson Hyperlegible Next";src:url("${woff}") format("woff2");font-weight:200 800;}`);
  }
  const results = await renderer(sources.map((s) => s.code), { mermaidConfig: MERMAID_CONFIG, css: pathToFileURL(font), prefix: 'mm' });
  return results.map((r, i) => {
    if (r.status !== 'fulfilled') throw new Error(`Mermaid couldn't draw a diagram: ${r.reason}\n${sources[i].code}`);
    return r.value;
  });
}
function themed(svg, id, description) {
  let s = svg.replace(/#mm-\d+/g, `#${id}`).replace(/id="mm-\d+"/g, `id="${id}"`).replace(/mm-\d+-/g, `${id}-`);
  for (const [hex, v] of Object.entries(SENTINELS)) s = s.replace(new RegExp(hex, 'gi'), v);
  // Mermaid's own fixed greys and whites, where the base theme still writes them
  s = s.replace(/rgba\(7, ?7, ?7, ?0?\.5\)/g, 'var(--nw-mm-label)');
  s = s.replace(/fill:#333;?/g, 'fill:var(--nw-mm-text);').replace(/color:#333;?/g, 'color:var(--nw-mm-text);').replace(/stroke:#333;?/g, 'stroke:var(--nw-mm-line);');
  s = s.replace(/<svg /, `<svg aria-labelledby="${id}-desc" `).replace(/(<svg[^>]*>)/, `$1<desc id="${id}-desc">${description}</desc>`);
  return s;
}
export function mermaidPlugin() {
  return {
    name: 'napwake-mermaid',
    async after(root, ctx) {
      const found = [];
      const visit = (n, parent) => {
        if (n.type === 'element' && n.tagName === 'pre') {
          const code = n.children?.find((c) => c.type === 'element' && c.tagName === 'code');
          if (code && classes(code).includes('language-mermaid')) found.push({ pre: n, code: textOf(code).trim() });
        }
        for (const c of n.children ?? []) visit(c, n);
      };
      visit(root);
      if (!found.length) return;
      const todo = [];
      for (const f of found) {
        f.hash = createHash('sha256').update(JSON.stringify(MERMAID_CONFIG) + f.code).digest('hex').slice(0, 12);
        f.file = join(CACHE, `${f.hash}.svg`);
        if (existsSync(f.file)) f.svg = readFileSync(f.file, 'utf8');
        else todo.push(f);
      }
      if (todo.length) {
        const out = await render(todo);
        todo.forEach((f, i) => { f.svg = out[i].svg; writeFileSync(f.file, f.svg); });
      }
      for (const f of found) {
        const id = `mm-${f.hash}`;
        const desc = 'Diagram: ' + f.code.replace(/<br\s*\/?>/g, ' ').replace(/&quot;/g, '"').replace(/[[\](){}>|-]+/g, ' ').replace(/\b(flowchart|graph|subgraph|end|TD|TB|LR)\b/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 600).replace(/[<&]/g, ' ');
        // the source stays in the page, hidden, so the Markdown copies and llms-full.txt keep the diagram
        const src = f.code.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);
        ctx.replaceNode(f.pre, { type: 'raw', value: `<figure class="nw-mermaid not-content">${themed(f.svg, id, desc)}<pre class="nw-mermaid__src" hidden><code class="language-mermaid">${src}</code></pre></figure>` });
      }
    },
  };
}

// ---------- links ----------
// Pages link to each other from the site's root (/guides/opt-in/). Each version builds under its own
// base path (/0.2/, /next/), so the base is added here; the links validator, which runs after, sees
// the links as they'll be served.
export function basePlugin(base) {
  const b = base.endsWith('/') ? base : base + '/';
  if (b === '/') return null;
  return {
    name: 'napwake-base',
    element: {
      filter: ['a'],
      visit(node, ctx) {
        const href = node.properties?.href;
        if (typeof href !== 'string' || !href.startsWith('/') || href.startsWith('//') || href.startsWith(b)) return;
        ctx.setProperty(node, 'href', b + href.slice(1));
      },
    },
  };
}
