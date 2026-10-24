#!/usr/bin/env node
// Writes src/styles/tokens.css from the brand's tokens.json, every colour as a CSS variable, light
// and dark, in Starlight's convention: dark on :root, light on [data-theme='light'].
//   node scripts/tokens.mjs [path/to/tokens.json]   (default ~/Workspace/napwake/brand/tokens/tokens.json)
import { readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const src = process.argv[2] || join(homedir(), 'Workspace/napwake/brand/tokens/tokens.json');
const t = JSON.parse(readFileSync(src, 'utf8'));
const byName = new Map(t.color.tokens.map((k) => [k.name, k]));
const ref = (v) => (typeof v === 'string' && /^\{.+\}$/.test(v) ? `var(--${v.slice(1, -1)})` : v);
const pick = (k, theme) => (typeof k.value === 'object' ? k.value[theme] : k.value);

const light = [], dark = [], both = [];
for (const k of t.color.tokens) {
  if (typeof k.value === 'object') { light.push(`  --${k.name}: ${ref(pick(k, 'light'))};`); dark.push(`  --${k.name}: ${ref(pick(k, 'dark'))};`); }
  else both.push(`  --${k.name}: ${ref(k.value)};`);
}
for (const [k, v] of Object.entries(t.type.families)) both.push(`  --font-${k}: ${v};`);
for (const g of ['spacing', 'radius']) for (const k of t[g].tokens) both.push(`  --${k.name}: ${k.value};`);
for (const k of t.shadow.tokens) {
  if (typeof k.value === 'object') { light.push(`  --${k.name}: ${k.value.light};`); dark.push(`  --${k.name}: ${k.value.dark};`); }
  else both.push(`  --${k.name}: ${k.value};`);
}
for (const g of ['opacity', 'easing', 'duration']) for (const k of t[g].tokens) both.push(`  --${k.name}: ${k.value};`);

const out = `/* Napwake's tokens, written by scripts/tokens.mjs from the brand's tokens.json (${t.name} v${t.version}).
   Don't edit by hand: change tokens.json and run the script. Dark is the default, as in Starlight. */
:root {
${both.join('\n')}
}
:root,
::backdrop {
${dark.join('\n')}
}
:root[data-theme='light'],
[data-theme='light'] ::backdrop {
${light.join('\n')}
}
`;
writeFileSync(new URL('../src/styles/tokens.css', import.meta.url), out);
console.log(`wrote src/styles/tokens.css: ${t.color.tokens.length} colours from ${src}`);
