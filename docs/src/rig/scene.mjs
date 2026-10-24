// Scenes: the corridor at night. Every door is an arch (round top, flat bottom), the same arch as
// the wordmark's counters. Asleep: closed and dark. Waking: ajar, an amber wedge, the doorman's hand
// on it. Awake: fully open, the room lit green, a green lamp over the door.
import { figure, POSES, PAL, DARK, G, handFilter } from './doorman.mjs';

export const THEME = {
  light: { wall: '#EEEEEA', floor: '#DEDED7', frame: '#D3D3CB', leaf: '#5B6B86', leafEdge: '#4A5872', knob: '#C98A1A', amberRoom: '#FFE3A1', amber: '#F5B53F', greenRoom: '#DDF3E3', green: '#2E9F62', greenGlow: '#7FD3A0', label: '#5E6764', st: { asleep: '#56657F', waking: '#9A6400', awake: '#1F7A47', error: '#C0392B' }, col: PAL, note: '#F8F8F5' },
  dark: { wall: '#0F1716', floor: '#0A1110', frame: '#1B2A2C', leaf: '#2A3944', leafEdge: '#22303A', knob: '#F5B53F', amberRoom: '#FFE3A1', amber: '#F5B53F', greenRoom: '#C9EED6', green: '#4CC284', greenGlow: '#4CC284', label: '#8C9895', st: { asleep: '#93A3BE', waking: '#F5B53F', awake: '#4CC284', error: '#F2685E' }, col: DARK, note: '#F8F8F5' },
};
const f = (n) => Math.round(n * 100) / 100;
const arch = (x, y, w, h) => `M${f(x)} ${f(y + h)} V${f(y + w / 2)} A${f(w / 2)} ${f(w / 2)} 0 0 1 ${f(x + w)} ${f(y + w / 2)} V${f(y + h)} Z`;
const MONO = "'Atkinson Hyperlegible Mono',ui-monospace,SFMono-Regular,Menlo,Consolas,monospace";

// one door at (x, floorY), width w, height h
export function door(state, x, fy, w, h, T, { label, stateLabel = true, sc = 1 } = {}) {
  const y = fy - h, fw = 7 * sc;
  let s = `<path d="${arch(x - fw, y - fw, w + 2 * fw, h + fw)}" fill="${T.frame}"/>`;
  const leafW = w;
  if (state === 'asleep' || state === 'error') {
    s += `<path d="${arch(x, y, w, h)}" fill="${T.leaf}"/>`;
    s += `<path d="M${f(x + w * 0.5)} ${f(y + w * 0.5)} V${fy}" stroke="${T.leafEdge}" stroke-width="${f(2 * sc)}"/>`;
    s += `<circle cx="${f(x + w * 0.82)}" cy="${f(fy - h * 0.44)}" r="${f(3.6 * sc)}" fill="${T.knob}"/>`;
    if (state === 'error') s += `<g transform="rotate(5 ${f(x + w / 2)} ${f(y + h * 0.42)})"><rect x="${f(x + w * 0.24)}" y="${f(y + h * 0.3)}" width="${f(w * 0.52)}" height="${f(w * 0.62)}" rx="2" fill="${T.note}"/><rect x="${f(x + w * 0.47)}" y="${f(y + h * 0.34)}" width="${f(w * 0.07)}" height="${f(w * 0.28)}" rx="2" fill="#D9443A"/><circle cx="${f(x + w / 2)}" cy="${f(y + h * 0.34 + w * 0.39)}" r="${f(w * 0.04)}" fill="#D9443A"/><circle cx="${f(x + w / 2)}" cy="${f(y + h * 0.3)}" r="${f(w * 0.05)}" fill="#D9443A"/></g>`;
  } else if (state === 'waking') {
    s += `<path d="${arch(x, y, w, h)}" fill="${T.amberRoom}"/>`;
    // amber wedge spilling from the gap onto the floor
    s += `<path d="M${f(x + w * 0.5)} ${fy} H${f(x + w)} L${f(x + w + 130 * sc)} ${f(fy + 60 * sc)} H${f(x + w * 0.5 + 30 * sc)} Z" fill="${T.amber}" opacity=".55"/>`;
    // the leaf, swung in partway from the hinge on the left; its free edge at 50%
    s += `<path d="M${x} ${fy} V${f(y + w / 2)} A${f(w / 2)} ${f(w / 2)} 0 0 1 ${f(x + w * 0.3)} ${f(y + 3 * sc)} L${f(x + w * 0.5)} ${f(y + 12 * sc)} V${f(fy - 3 * sc)} Z" fill="${T.leaf}"/>`;
    s += `<circle cx="${f(x + w * 0.42)}" cy="${f(fy - h * 0.44)}" r="${f(3.4 * sc)}" fill="${T.knob}"/>`;
  } else if (state === 'awake') {
    s += `<path d="${arch(x, y, w, h)}" fill="${T.greenRoom}"/>`;
    s += `<path d="M${x} ${fy} H${f(x + w)} L${f(x + w + 80 * sc)} ${f(fy + 60 * sc)} H${f(x - 80 * sc)} Z" fill="${T.greenGlow}" opacity=".32"/>`;
    // the leaf, swung fully open against the wall on the hinge side
    s += `<path d="M${f(x - fw)} ${fy} V${f(y + w * 0.3)} L${f(x - fw - 14 * sc)} ${f(y + w * 0.42)} V${f(fy + 6 * sc)} Z" fill="${T.leaf}"/>`;
    // the green lamp over the door
    s += `<circle cx="${f(x + w / 2)}" cy="${f(y - fw - 9 * sc)}" r="${f(13 * sc)}" fill="${T.greenGlow}" opacity=".28"/><rect x="${f(x + w / 2 - 7 * sc)}" y="${f(y - fw - 13 * sc)}" width="${f(14 * sc)}" height="${f(8 * sc)}" rx="${f(4 * sc)}" fill="${T.green}"/>`;
  }
  return s;
}

export function labelsFor(state, x, fy, w, h, T, label, sc, stateLabel = true) {
  const y = fy - h, word = { asleep: 'asleep', waking: 'waking up', awake: 'awake', error: 'needs you' }[state];
  let s = '';
  if (label) s += `<text x="${f(x + w / 2)}" y="${f(y - (state === 'awake' ? 32 : 18) * sc)}" text-anchor="middle" font-family="${MONO}" font-size="${f(12.5 * sc)}" fill="${T.label}">${label}</text>`;
  if (stateLabel) s += `<text x="${f(x + w / 2)}" y="${f(fy + 22 * sc)}" text-anchor="middle" font-family="${MONO}" font-size="${f(12 * sc)}" fill="${T.st[state]}" font-weight="700" letter-spacing=".3">${word}</text>`;
  return s;
}

// The hand of the 'door' pose, in figure coordinates (feet at 0,0): the mitten that grips the leaf
export function doorHand() {
  const p = POSES.door, t = p.turn || 0, at = Math.abs(t), a = p.lean * Math.PI / 180;
  const lx = G.shW / 2 - 4 - (t > 0 ? 10 * at : 2 * at), ly = -103 - (p.shrug || 0);
  const sx = lx * Math.cos(a) - ly * Math.sin(a), sy = lx * Math.sin(a) + ly * Math.cos(a);
  return p.arms.R.h([sx, sy]);
}

// The corridor. rooms: [{state,label}], doormanAt: index of the waking door the doorman holds.
export function corridor({ w = 1280, h = 400, rooms, theme = 'dark', x0 = 40, floorY = null, sc = 1, doormanAt = -1, labels = true, stateLabels = true, wall = true, hand = true, pose = 'door' } = {}) {
  const T = THEME[theme];
  const dw = 78 * sc, dh = 156 * sc, gap = 30 * sc, pgap = 112 * sc, fy = floorY ?? h - 46 * sc;
  let x = x0, art = '', txt = '';
  let bg = '';
  if (wall) bg += `<rect width="${w}" height="${h}" fill="${T.wall}"/><rect y="${fy}" width="${w}" height="${h - fy}" fill="${T.floor}"/>`;
  const fid = 'hf' + Math.random().toString(36).slice(2, 7);
  let figs = '';
  rooms.forEach((rm, i) => {
    if (i === doormanAt) x += pgap - gap;
    art += door(rm.state, x, fy, dw, dh, T, { sc });
    if (labels || stateLabels) txt += labelsFor(rm.state, x, fy, dw, dh, T, labels ? rm.label : null, sc, stateLabels);
    if (i === doormanAt) {
      // place the doorman so his mitten sits on the leaf's free edge
      const [hx, hy] = doorHand(), k = sc * 0.86;
      const ex = x + dw * 0.5;
      const tx = ex - hx * k, ty = fy;
      // cast shadow away from the light
      figs += `<path d="M${f(tx - 30 * k)} ${f(fy)} H${f(tx + 34 * k)} L${f(tx - 90 * k)} ${f(fy + 30 * k)} H${f(tx - 170 * k)} Z" fill="#000" opacity="${theme === 'dark' ? .3 : .09}"/>`;
      figs += `<g transform="translate(${f(tx)} ${f(ty)}) scale(${f(k)})">${figure({ ...POSES[pose], ground: false }, T.col)}</g>`;
    }
    x += dw + gap;
  });
  const gid = fid + 'g';
  const wallGrain = wall && hand ? `<filter id="${gid}" x="0" y="0" width="100%" height="100%"><feTurbulence type="fractalNoise" baseFrequency=".9" numOctaves="2" seed="4"/><feColorMatrix values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 ${theme === 'dark' ? .22 : .07} 0"/></filter><rect width="${w}" height="${h}" filter="url(#${gid})"/>` : '';
  return bg + (hand ? `<defs>${handFilter(fid, 1.4, 0.13)}</defs>${wallGrain}<g filter="url(#${fid})">${art}${figs}</g>` : art + figs) + txt;
}

// ---------- state glyphs: the same arch doors, on a 24 grid ----------
export const GLYPH = {
  fill: { asleep: '#5B6B86', waking: '#F5B53F', awake: '#2E9F62', error: '#D9443A' },
};
export function glyph(state, theme = 'light') {
  const A = 'M6 22V10A6 6 0 0 1 18 10V22Z';
  const lite = theme === 'dark' ? { slate: '#8C9BB5', amber: '#F5B53F', green: '#4CC284', red: '#F0645A', room: '#FFE3A1', groom: '#C9EED6', leaf: '#2A3944' } : { slate: '#5B6B86', amber: '#F5B53F', green: '#2E9F62', red: '#D9443A', room: '#FFE3A1', groom: '#DDF3E3', leaf: '#4A5872' };
  if (state === 'asleep') return `<path d="${A}" fill="${lite.slate}"/><circle cx="15.3" cy="15" r="1.2" fill="#F8F8F5"/>`;
  if (state === 'waking') return `<path d="${A}" fill="${lite.room}" stroke="${lite.amber}" stroke-width="1.6"/><path d="M6 22V10A6 6 0 0 1 9.5 4.6L12 6V22Z" fill="${lite.leaf}"/><path d="M12 22.2H18L23 23.6H13Z" fill="${lite.amber}"/>`;
  if (state === 'awake') return `<path d="${A}" fill="${lite.groom}" stroke="${lite.green}" stroke-width="1.6"/><rect x="10" y="0.6" width="4" height="2.2" rx="1.1" fill="${lite.green}"/><path d="M6.5 22.2H17.5L21 23.6H3Z" fill="${lite.green}" opacity=".6"/>`;
  return `<path d="${A}" fill="${lite.slate}"/><rect x="8.2" y="9.5" width="7.6" height="8.6" rx="1" fill="#F8F8F5" transform="rotate(6 12 13.8)"/><rect x="11.3" y="10.8" width="1.5" height="3.6" rx=".75" fill="${lite.red}"/><circle cx="12.1" cy="16.2" r=".9" fill="${lite.red}"/>`;
}
