// Napwake's docs: Astro Starlight, in the doorman's brand.
//
// One build is one version of the docs. The publishing workflow builds each into its own folder:
//   NAPWAKE_DOCS_BASE=/0.2/  NAPWAKE_DOCS_VERSION=0.2   NAPWAKE_DOCS_RELEASE=0.2.0      npm run build
//   NAPWAKE_DOCS_BASE=/next/ NAPWAKE_DOCS_VERSION=next  NAPWAKE_DOCS_RELEASE=0.3.0-rc.1 npm run build
// With none of them set it builds at / as "dev", as for a pull request. hack/docsite publishes them.
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { satteri } from '@astrojs/markdown-satteri';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';
import { ogCards } from './src/og/cards.mjs';
import { pluginPrompt } from './src/plugins/prompt.mjs';
import { mathPlugin, mermaidPlugin, basePlugin } from './src/plugins/markdown.mjs';
import { napwakeDark, napwakeLight } from './src/code-themes.mjs';
import { promql } from './src/promql.tmLanguage.mjs';
import { readFileSync, cpSync, mkdirSync, existsSync } from 'node:fs';

const env = (k, d) => (process.env[k] ?? '').trim() || d;
const site = env('NAPWAKE_DOCS_SITE', 'https://docs.napwake.dev');
const base = env('NAPWAKE_DOCS_BASE', '/').replace(/\/?$/, '/');
const version = env('NAPWAKE_DOCS_VERSION', 'dev');
const release = env('NAPWAKE_DOCS_RELEASE', '');
// The sidebar: every page, in order, with its label.
const sidebarFile = new URL('./src/sidebar.json', import.meta.url);
const sidebar = existsSync(sidebarFile) ? JSON.parse(readFileSync(sidebarFile, 'utf8')) : [];

// llms.txt (CMO decision 0004): what Napwake is, who it's for, how it works, and the key pages, each
// as Markdown. The words are the brand's (napwake/brand README) and the pages' own.
const md = (path) => new URL(`${base}${path}.md`, site).href;
const plainLine = 'Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request.';
const llmsDetails = `Napwake pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request. It never deletes a workload or its data. It's open source (Apache 2.0): an operator, a \`kubectl napwake\` plugin, a Helm chart, and these docs.

It's for platform and infrastructure engineers who run non-production environments on their own Kubernetes clusters. Behind a VPN, SSO or an IP allowlist, a paused environment wakes on its first request. On the public internet, robots would wake it every few minutes, so set it to ask first. A browser then gets a Wake it button, so people wake it and robots don't. Tooling wakes it with an annotation, and the forecast learns when people come.

In short: label a Deployment, StatefulSet or namespace \`napwake.dev/managed: "true"\`. Napwake pauses the workload, scaling it to zero, once nothing has been active for \`idleAfter\` (an hour by default): CPU above its threshold, a deploy, an activity annotation or a Prometheus query all count as activity. While it's paused, its Services are routed to the doorman, a proxy that holds the first request and wakes it. With \`autoResume\`, a forecast wakes it 15 minutes before an hour it expects to be busy. \`kubectl napwake scan\` shows what's idle with nothing installed in the cluster.

Key pages, each as Markdown:

- [Installation](${md('getting-started/installation')}): Helm or the Kustomize manifests, with Argo CD or Flux
- [Quickstart](${md('getting-started/quickstart')}): one workload from idle to paused while idle and woken when used, measured first
- [kubectl plugin](${md('getting-started/kubectl-plugin')}): scan, forecast, status, nodes, pause, wake, deps and enable
- [Opting in](${md('guides/opt-in')}): the label, every setting as an annotation, namespaces, Kustomize and GitOps
- [Public preview environments](${md('guides/preview-environments')}): asking before waking, so people wake an environment on a public URL and robots don't
- [Idle detection](${md('concepts/idle-detection')}): what counts as activity, and when a workload pauses
- [Wake on request](${md('concepts/wake-on-request')}): how the doorman holds a request and wakes a paused workload
- [Forecasting](${md('concepts/forecasting')}): the Holt-Winters model, \`autoResume\` and \`pauseEarly\`
- [GitOps tools](${md('guides/gitops')}): leaving replicas to Napwake under Argo CD, Flux, Fleet, kapp and Kluctl
- [Labels and annotations](${md('reference/labels-annotations')}): every label and annotation Napwake reads or writes
- [kubectl plugin reference](${md('reference/cli')}): every flag, JSON field, exit status and permission of the plugin's commands
- [Conditions and events](${md('reference/conditions-and-events')}): every status condition, with its reasons, and every event
- [Helm values](${md('reference/helm-values')}): every value of the chart
- [Troubleshooting](${md('operations/troubleshooting')}): common problems, and how to fix them

Every page is also at its address with \`.md\` added. These docs name a workload's states paused, resuming and running; pictures and the home page call them asleep, waking up and awake.`;

// KaTeX's stylesheet and its fonts, copied from the package into public/katex/ (git-ignored) for the
// pages with maths; Head.astro links it there only.
function katexAssets() {
  return {
    name: 'napwake-katex-assets',
    hooks: {
      'astro:config:setup': () => {
        const from = new URL('./node_modules/katex/dist/', import.meta.url);
        const to = new URL('./public/katex/', import.meta.url);
        mkdirSync(new URL('fonts/', to), { recursive: true });
        cpSync(new URL('katex.min.css', from), new URL('katex.min.css', to));
        cpSync(new URL('fonts/', from), new URL('fonts/', to), { recursive: true, filter: (p) => !/\.(woff|ttf)$/.test(p) });
      },
    },
  };
}

export default defineConfig({
  site,
  base,
  trailingSlash: 'always',
  vite: {
    define: {
      'import.meta.env.NAPWAKE_DOCS_VERSION': JSON.stringify(version),
      'import.meta.env.NAPWAKE_DOCS_RELEASE': JSON.stringify(release),
      'import.meta.env.NAPWAKE_DOCS_VERSIONS_URL': JSON.stringify(env('NAPWAKE_DOCS_VERSIONS_URL', '')),
      // Set by hack/docsite: the version pages name as canonical, latest once a release is out, and
      // whether search engines should leave this version out.
      'import.meta.env.NAPWAKE_DOCS_CANONICAL': JSON.stringify(env('NAPWAKE_DOCS_CANONICAL', 'latest')),
      'import.meta.env.NAPWAKE_DOCS_NOINDEX': JSON.stringify(env('NAPWAKE_DOCS_NOINDEX', '')),
    },
  },
  markdown: {
    processor: satteri({
      // $$x$$ inline and $$ blocks only: single dollars are prices in these docs
      features: { math: { singleDollarTextMath: false } },
      hastPlugins: [mathPlugin(), mermaidPlugin(), basePlugin(base)].filter(Boolean),
    }),
  },
  integrations: [
    katexAssets(),
    ogCards({ site, base }),
    starlight({
      title: 'Napwake',
      disable404Route: true,
      description: 'Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request.',
      favicon: '/favicon.svg',
      // The website's icon set too (napwake/website scripts/icons.mjs): an ICO for tools that don't read SVG,
      // the home-screen icon, and the manifest.
      head: [
        { tag: 'link', attrs: { rel: 'icon', href: `${base}favicon.ico`, sizes: '32x32' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', href: `${base}apple-touch-icon.png` } },
        { tag: 'link', attrs: { rel: 'manifest', href: `${base}site.webmanifest` } },
      ],
      social: [{ icon: 'github', label: 'Napwake on GitHub', href: 'https://github.com/napwake/napwake' }],
      editLink: { baseUrl: 'https://github.com/napwake/napwake/edit/main/docs/' },
      sidebar,
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
      customCss: ['./src/styles/fonts.css', './src/styles/tokens.css', './src/styles/theme.css'],
      routeMiddleware: './src/route-data.ts',
      components: {
        Head: './src/components/overrides/Head.astro',
        SiteTitle: './src/components/overrides/SiteTitle.astro',
        Header: './src/components/overrides/Header.astro',
        Banner: './src/components/overrides/Banner.astro',
        PageTitle: './src/components/overrides/PageTitle.astro',
        Footer: './src/components/overrides/Footer.astro',
      },
      expressiveCode: {
        themes: [napwakeDark, napwakeLight],
        // No line numbers: they'd go into the pages' Markdown copies as lines of their own, and nothing refers to them.
        plugins: [pluginPrompt()],
        shiki: { langs: [promql] },
        defaultProps: { wrap: false },
        styleOverrides: {
          borderRadius: '6px',
          borderWidth: '1px',
          borderColor: 'var(--line)',
          codeFontFamily: 'var(--font-mono)',
          codeFontSize: '0.875rem',
          codeLineHeight: '1.55',
          codePaddingBlock: '0.95rem',
          codePaddingInline: '1.1rem',
          uiFontFamily: 'var(--font-mono)',
          uiFontSize: '0.8125rem',
          focusBorder: 'var(--action)',
          frames: {
            frameBoxShadowCssValue: 'none',
            editorTabBarBackground: 'var(--code-chrome)',
            editorTabBarBorderBottomColor: 'var(--line)',
            editorActiveTabBackground: 'var(--code-bg)',
            editorActiveTabForeground: 'var(--text)',
            editorActiveTabBorderColor: 'transparent',
            editorActiveTabIndicatorTopColor: 'var(--brand)',
            editorActiveTabIndicatorBottomColor: 'transparent',
            editorActiveTabIndicatorHeight: '2px',
            terminalTitlebarBackground: 'var(--code-chrome)',
            terminalTitlebarForeground: 'var(--muted)',
            terminalTitlebarBorderBottomColor: 'var(--line)',
            terminalTitlebarDotsOpacity: '0',
            terminalBackground: 'var(--code-bg)',
            inlineButtonBackground: 'var(--text)',
            inlineButtonForeground: 'var(--text)',
            inlineButtonBorder: 'var(--line)',
            tooltipSuccessBackground: 'var(--coat)',
            tooltipSuccessForeground: 'var(--paper)',
          },
          lineNumbers: { foreground: 'var(--muted)', highlightForeground: 'var(--text)' },
        },
      },
      plugins: [
        starlightLinksValidator({ errorOnRelativeLinks: true, errorOnInvalidHashes: true }),
        starlightLlmsTxt({
          projectName: 'Napwake',
          description: plainLine,
          details: llmsDetails,
          optionalLinks: [
            { label: 'Source code', url: 'https://github.com/napwake/napwake', description: 'the operator, the kubectl plugin, the Helm chart and these docs' },
            { label: 'napwake.dev', url: 'https://napwake.dev/', description: "Napwake's website" },
          ],
          promote: ['index*', 'getting-started/**'],
          demote: ['contributing'],
          // drawings, the terminal's "$ " and heading anchors aren't text; Mermaid keeps its source
          customSelectors: { all: ['svg', '.nw-prompt', '.nw-term__prompt', '.nw-ref__link', '.nw-browser__bar', '.nw-day__axis', '.sl-anchor-link', '.nw-tools'] },
        }),
      ],
    }),
  ],
});
