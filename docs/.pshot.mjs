// pshot.mjs URL OUT [width=390] [light|dark] [full|<height>] [segment height]: headless Chrome (the
// system's, through Playwright) at any width, including a phone's, which Chrome's --window-size can't
// go below. With a segment height, a full page is also cut into OUT-1.png, OUT-2.png, ...
import { chromium } from 'playwright';
const [url, out, w = '390', theme = 'light', h = 'full', seg] = process.argv.slice(2);
const b = await chromium.launch({ channel: 'chrome' });
const p = await b.newPage({ viewport: { width: Number(w), height: h === 'full' ? 900 : Number(h) }, colorScheme: theme, deviceScaleFactor: 1 });
await p.goto(url, { waitUntil: 'networkidle' });
// scroll through once so lazy images load, then back to the top
await p.evaluate(async () => { for (let y = 0; y < document.documentElement.scrollHeight; y += 600) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 40)); } window.scrollTo(0, 0); });
await p.waitForTimeout(400);
await p.screenshot({ path: out, fullPage: h === 'full' });
if (seg) {
  const total = await p.evaluate(() => document.documentElement.scrollHeight);
  const s = Number(seg);
  for (let i = 0, y = 0; y < total; i++, y += s) {
    await p.screenshot({ path: out.replace(/\.png$/, `-${i + 1}.png`), fullPage: true, clip: { x: 0, y, width: Number(w), height: Math.min(s, total - y) } });
  }
}
await b.close();
