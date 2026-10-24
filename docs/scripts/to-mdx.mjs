#!/usr/bin/env node
// Turns a converted Markdown page into MDX, so a designed page can use components:
//   node scripts/to-mdx.mjs src/content/docs/concepts/wake-on-request.md
// It renames the file to .mdx and escapes what MDX reads as code in prose: { } as \{ \}, and a "<"
// that doesn't start a tag as &lt;. Fenced code, code spans and $$ maths are left as they are.
// Syntax only: the words don't change.
import { readFileSync, writeFileSync, renameSync } from 'node:fs';

const FENCE = /^(\s*)(`{3,}|~{3,})(.*)$/;
const TAGS = /^<\/?(Tabs|TabItem|details|summary|br|img|kbd|sup|sub|[A-Z][A-Za-z]*)\b/;

function escapeProse(s) {
  return s.replace(/<(https?:\/\/[^>\s]+)>/g, '[$1]($1)').replace(/[{}]/g, (c) => `\\${c}`).replace(/</g, (c, off, str) => (TAGS.test(str.slice(off)) ? c : '&lt;'));
}
function escapeLine(line) {
  // keep code spans and $$ maths; escape the rest
  return line.split(/(`+[^`]*?`+|\$\$[^$]*\$\$)/g).map((part, i) => (i % 2 ? part : escapeProse(part))).join('');
}

for (const file of process.argv.slice(2)) {
  if (!file.endsWith('.md')) throw new Error(`${file} isn't a .md page`);
  const text = readFileSync(file, 'utf8');
  const end = text.startsWith('---\n') ? text.indexOf('\n---\n', 4) + 5 : 0;
  const fm = text.slice(0, end), body = text.slice(end).split('\n');
  let fence = null, math = false;
  const out = body.map((l) => {
    const f = l.match(FENCE);
    if (fence) { if (f && f[2][0] === fence[0] && f[2].length >= fence.length && f[3].trim() === '') fence = null; return l; }
    if (f) { fence = f[2]; return l; }
    if (/^\s*\$\$\s*$/.test(l)) { math = !math; return l; }
    if (math || /^\s*import\s/.test(l)) return l;
    return escapeLine(l);
  });
  writeFileSync(file, fm + out.join('\n'));
  renameSync(file, file.replace(/\.md$/, '.mdx'));
  console.log(`${file} -> .mdx`);
}
