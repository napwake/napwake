// The doorman's rig (doorman.mjs, scene.mjs, copied unchanged from the brand's system-2-character),
// drawn with the brand's CSS variables instead of fixed colours, so one inline SVG serves light and
// dark and follows the theme toggle. Every drawing of the doorman and the doors in the docs comes
// from here: never by hand. The illustrations are CC BY 4.0 (github.com/napwake/brand).
import { figure, POSES, handFilter, poseProps } from './doorman.mjs';
import { corridor, glyph, THEME } from './scene.mjs';

export { POSES };

const DOORMAN = {
  coat: 'var(--doorman-coat)', coatDk: 'var(--doorman-coat-deep)', face: 'var(--doorman-face)', brass: 'var(--doorman-brass)',
  night: 'var(--doorman-night)', hatCol: 'var(--doorman-hat)', glove: 'var(--doorman-mitten)', gloveLine: 'var(--doorman-mitten-line)',
  eye: 'var(--doorman-eye)', shoe: 'var(--doorman-shoe)', shoeRim: 'var(--doorman-shoe-rim)', paper: 'var(--paper)',
};
const SCENE = {
  wall: 'var(--scene-wall)', floor: 'var(--scene-floor)', frame: 'var(--scene-frame)', leaf: 'var(--scene-leaf)', leafEdge: 'var(--scene-leaf-edge)',
  knob: 'var(--scene-knob)', amberRoom: 'var(--scene-room-amber)', amber: 'var(--scene-amber)', greenRoom: 'var(--scene-room-green)',
  green: 'var(--scene-green)', greenGlow: 'var(--scene-green-glow)', label: 'var(--scene-label)',
  st: { asleep: 'var(--asleep)', waking: 'var(--waking)', awake: 'var(--awake)', error: 'var(--error)' },
  col: DOORMAN, note: 'var(--paper)',
};
THEME.light = SCENE;
THEME.dark = SCENE;

// Fixed colours the rig keeps in its props and glyphs, mapped to the tokens they are.
const FIXED = {
  '#FFE3A1': 'var(--scene-room-amber)', '#5B6B86': 'var(--scene-leaf)', '#4A5872': 'var(--glyph-leaf)', '#D9443A': 'var(--note-pin)',
  '#8A5A2B': 'var(--prop-clipboard)', '#F5B53F': 'var(--doorman-brass)', '#B9B4A7': 'var(--prop-plate-edge)', '#9C978A': 'var(--prop-switch)',
  '#2E9F62': 'var(--glyph-green)', '#0F1716': 'var(--doorman-night)', '#F8F8F5': 'var(--paper)',
};
const GLYPH = { '#5B6B86': 'var(--glyph-slate)', '#F5B53F': 'var(--glyph-amber)', '#2E9F62': 'var(--glyph-green)', '#D9443A': 'var(--glyph-red)', '#FFE3A1': 'var(--glyph-room-amber)', '#DDF3E3': 'var(--glyph-room-green)', '#4A5872': 'var(--glyph-leaf)', '#F8F8F5': 'var(--paper)' };
const swap = (s, map) => s.replace(/#[0-9A-Fa-f]{6}\b/g, (m) => map[m.toUpperCase()] ?? m);

// ids the rig makes (clip paths, filters) get a prefix of their own, unique on the page
let n = 0;
const scope = (s) => {
  const p = `nw${(++n).toString(36)}`;
  return s.replace(/id="([A-Za-z][\w-]*)"/g, `id="${p}-$1"`).replace(/url\(#([A-Za-z][\w-]*)\)/g, `url(#${p}-$1)`);
};
export const STATES = ['asleep', 'waking', 'awake', 'error'];
export const WORDS = {
  plain: { asleep: 'Asleep', waking: 'Waking up', awake: 'Awake', error: 'Needs you' },
  product: { asleep: 'paused', waking: 'resuming', awake: 'running', error: 'needs attention' },
};

/** The door glyph for a state, on a 24 grid, in the glyph tokens. */
export function door(state) {
  return swap(glyph(STATES.includes(state) ? state : 'asleep', 'light'), GLYPH);
}

/** A pose, 240 x 262 unless told otherwise, with the print finish when `finish`. */
export function pose(name, { finish = true, w = 240, h = 262, floor = 244, cx = 100, wobble = 1.3, grain = 0.13 } = {}) {
  const p = { ...POSES[name], props: poseProps(name, DOORMAN) };
  const id = 'f';
  const body = `<g transform="translate(${cx} ${floor})"${finish ? ` filter="url(#${id})"` : ''}>${figure(p, DOORMAN)}</g>`;
  return { viewBox: `0 0 ${w} ${h}`, svg: scope((finish ? `<defs>${handFilter(id, wobble, grain)}</defs>` : '') + swap(body, FIXED)) };
}

/** The corridor: one door per room, from state, the doorman at the waking one. */
export function scene(opts) {
  const { w = 1280, h = 400 } = opts;
  let svg = corridor({ ...opts, theme: 'light' });
  // the cast shadow and the wall's grain are stronger in the dark: the theme sets --nw-cast and --nw-grain
  svg = svg.replace(/fill="#000" opacity="0\.09"/g, 'fill="#000" style="opacity:var(--nw-cast)"');
  svg = svg.replace(/0 0 0 0\.07 0"\/><\/filter><rect /g, '0 0 0 1 0"/></filter><rect style="opacity:var(--nw-grain)" ');
  return { viewBox: `0 0 ${w} ${h}`, svg: scope(swap(svg, FIXED)) };
}
