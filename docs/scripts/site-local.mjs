#!/usr/bin/env node
// Builds the docs as the versioned site would serve them, to check the version switcher and banners:
// 0.2 (latest) and next (a release candidate) into .site/, each in its own folder, with a
// versions.json in mike's shape and a root that sends readers to latest. Then serves .site/.
//   npm run site:local            # builds both, serves http://localhost:4400/
//   npm run site:local -- --serve # serves what's there
// The versions and releases are samples. The publishing workflow does the same with the real
// ones (hack/docsite).
import { execFileSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { extname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)));
const SITE = join(ROOT, '.site');
const versions = [
  { version: 'next', title: '0.3.0-rc.1', aliases: [], properties: { release: '0.3.0-rc.1' } },
  { version: '0.2', title: '0.2', aliases: ['latest'], properties: { release: '0.2.0' } },
];

if (!process.argv.includes('--serve')) {
  rmSync(SITE, { recursive: true, force: true });
  mkdirSync(SITE, { recursive: true });
  for (const v of versions) {
    const env = { ...process.env, NAPWAKE_DOCS_BASE: `/${v.version}/`, NAPWAKE_DOCS_VERSION: v.version, NAPWAKE_DOCS_RELEASE: v.properties.release, NAPWAKE_DOCS_SITE: 'http://localhost:4400' };
    console.log(`building ${v.version} (${v.properties.release}) at /${v.version}/`);
    execFileSync('npx', ['astro', 'build', '--outDir', join(SITE, v.version)], { cwd: ROOT, env, stdio: ['ignore', 'ignore', 'inherit'] });
    // latest is built under its own base too, as the workflow does, so its links stay inside it
    for (const alias of v.aliases) {
      console.log(`building ${alias} (${v.properties.release}) at /${alias}/`);
      execFileSync('npx', ['astro', 'build', '--outDir', join(SITE, alias)], { cwd: ROOT, env: { ...env, NAPWAKE_DOCS_BASE: `/${alias}/`, NAPWAKE_DOCS_VERSION: alias }, stdio: ['ignore', 'ignore', 'inherit'] });
    }
  }
  writeFileSync(join(SITE, 'versions.json'), JSON.stringify(versions, null, 2) + '\n');
  writeFileSync(join(SITE, 'index.html'), '<!doctype html><meta charset="utf-8"><title>Napwake docs</title><meta http-equiv="refresh" content="0; url=latest/"><link rel="canonical" href="latest/">');
  // the root's own files are latest's, as the publishing workflow will place them
  for (const f of ['404.html', 'robots.txt', 'llms.txt', 'llms-full.txt', 'llms-small.txt']) cpSync(join(SITE, 'latest', f), join(SITE, f));
}

const types = { '.md': 'text/markdown; charset=utf-8', '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.js': 'text/javascript', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.webp': 'image/webp', '.woff2': 'font/woff2', '.xml': 'application/xml', '.txt': 'text/plain', '.pf_meta': 'application/octet-stream' };
createServer((req, res) => {
  let p = normalize(decodeURIComponent(new URL(req.url, 'http://x').pathname)).replace(/^(\.\.[/\\])+/, '');
  let file = join(SITE, p);
  if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html');
  if (!existsSync(file)) { res.writeHead(404, { 'content-type': types['.html'] }); res.end(readFileSync(join(SITE, '404.html'))); return; }
  res.writeHead(200, { 'content-type': types[extname(file)] || 'application/octet-stream' });
  res.end(readFileSync(file));
}).listen(4400, () => console.log('serving .site/ at http://localhost:4400/'));
