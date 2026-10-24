// Expressive Code plugin: the brand's terminal prompt. In shell blocks, a muted "$ " starts each line
// that begins a command, as TerminalBlock shows them. It's drawn, not typed: copying skips it, and
// the copy button copies the code as written. Continuation lines (after a trailing \, |, && or ||,
// inside a quote, or inside do ... done, if ... fi, { }), indented lines, comments and blank lines
// get none, so output and comments in the same block read apart from what to type.
import { definePlugin } from '@expressive-code/core';
import { h } from '@expressive-code/core/hast';

const SHELLS = new Set(['bash', 'sh', 'shell', 'zsh', 'console']);

export function commandLines(code) {
  const lines = code.split('\n');
  const out = [];
  let cont = false, depth = 0, quote = null;
  for (const raw of lines) {
    const l = raw.replace(/\s+$/, '');
    const starts = !cont && depth === 0 && !quote && l !== '' && !/^\s/.test(l) && !l.startsWith('#') && !/^(done|fi|esac|\}|\))\b/.test(l);
    out.push(starts);
    // track quotes across lines (single and double, without escapes inside single quotes)
    for (let i = 0; i < l.length; i++) {
      const c = l[i];
      if (quote) { if (c === quote && (quote === "'" || l[i - 1] !== '\\')) quote = null; }
      else if (c === '#' && (i === 0 || /\s/.test(l[i - 1]))) break;
      else if (c === "'" || c === '"') quote = c;
    }
    const code = l.replace(/(^|\s)#.*$/, '');
    if (!quote) {
      depth += (code.match(/\b(do|then)\b\s*$|\{\s*$|\(\s*$/g) || []).length;
      depth -= (code.match(/^\s*(done|fi|\}|\))\b/g) || []).length;
      if (depth < 0) depth = 0;
    }
    cont = Boolean(quote) || /(\\|\||&&|\|\|)\s*$/.test(code);
  }
  return out;
}

export function pluginPrompt() {
  return definePlugin({
    name: 'napwake-prompt',
    baseStyles: `.nw-prompt { color: var(--muted); user-select: none; -webkit-user-select: none; }`,
    hooks: {
      postprocessRenderedLine: ({ codeBlock, lineIndex, renderData }) => {
        if (!SHELLS.has(codeBlock.language)) return;
        codeBlock._nwPrompts ??= commandLines(codeBlock.code);
        if (!codeBlock._nwPrompts[lineIndex]) return;
        const line = renderData.lineAst;
        const target = line.children.find((c) => c.type === 'element' && (c.properties?.className ?? []).includes('code')) ?? line;
        target.children.unshift(h('span.nw-prompt', { 'aria-hidden': 'true' }, '$ '));
      },
    },
  });
}
