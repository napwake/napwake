// The doorman: Napwake's character, round 3.
// A tall, door-shaped figure: face zone above a standing collar, a greatcoat to the floor,
// two rows of buttons and a braid on one shoulder, dark mittens, a short black top hat.
// Built from circles, ellipses, rects and quadratic curves; every pose is numbers.

export const PAL = {
  coat: '#14695F',      // Coat: the brand's teal
  coatDk: '#0D3F3A',    // Coat deep: sleeves, collar
  face: '#3A9A8B',      // face zone, a step lighter than the coat
  brass: '#F5B53F',     // Lamp: buttons, braid, band, cuffs
  night: '#0F1716',
  hatCol: '#141B1A',    // the hat is black: a London hotel doorman's, not a leprechaun's
  glove: '#2A3534',     // dark mittens: white gloves under a top hat read as a magician
  gloveLine: '#0F1716',
  eye: '#FFFFFF',
  shoe: '#0F1716',
  shoeRim: '#0F1716',
  paper: '#F8F8F5',
};
export const DARK = { ...PAL, coat: '#1E7A6E', coatDk: '#12524B', face: '#45A898', hatCol: '#2B3735', glove: '#3E4E4C', gloveLine: '#7A918D', shoe: '#2B4743', shoeRim: '#4C7069' };
export const MONO_INK = { coat: '#0F1716', coatDk: '#0F1716', face: '#0F1716', brass: '#F8F8F5', night: '#0F1716', glove: '#0F1716', gloveLine: '#F8F8F5', eye: '#F8F8F5', shoe: '#0F1716', shoeRim: '#0F1716', paper: '#F8F8F5', mono: true };
export const MONO_PAPER = { coat: '#F8F8F5', coatDk: '#F8F8F5', face: '#F8F8F5', brass: '#0F1716', night: '#F8F8F5', glove: '#F8F8F5', gloveLine: '#0F1716', eye: '#0F1716', shoe: '#F8F8F5', shoeRim: '#F8F8F5', paper: '#0F1716', mono: true };

let UID = 0;
const uid = (p) => `${p}${++UID}`;
const r2 = (n) => Math.round(n * 100) / 100;
const rot = (x, y, deg, ox = 0, oy = 0) => {
  const a = deg * Math.PI / 180, dx = x - ox, dy = y - oy;
  return [r2(ox + dx * Math.cos(a) - dy * Math.sin(a)), r2(oy + dx * Math.sin(a) + dy * Math.cos(a))];
};

// ---------- eyes ----------
// lid: 0 open .. 1 closed. tilt: degrees for the lid line. look: [-1..1, -1..1] pupil direction.
export function eye(x, y, r, { lid = 0.5, tilt = 0, look = [0, 0.3], col = PAL, face } = {}) {
  const fc = face || col.face;
  if (lid >= 0.95) {
    const [ax, ay] = rot(x - r * 0.95, y, tilt, x, y), [bx, by] = rot(x + r * 0.95, y, tilt, x, y), [cx, cy] = rot(x, y + r * 0.9, tilt, x, y);
    return `<path d="M${ax} ${ay} Q${cx} ${cy} ${bx} ${by}" fill="none" stroke="${col.night}" stroke-width="${r2(r * 0.42)}" stroke-linecap="round"/>`;
  }
  const id = uid('e');
  const px = r2(x + look[0] * r * 0.42), py = r2(y + look[1] * r * 0.42);
  let s = `<clipPath id="${id}"><circle cx="${x}" cy="${y}" r="${r2(r * 1.04)}"/></clipPath>`;
  s += `<circle cx="${x}" cy="${y}" r="${r}" fill="${col.eye}"/>`;
  s += `<g clip-path="url(#${id})"><circle cx="${px}" cy="${py}" r="${r2(r * 0.5)}" fill="${col.night}"/>`;
  const ly = y - r + 2 * r * lid;
  if (lid > 0.02) s += `<rect x="${x - r * 1.6}" y="${y - r * 2}" width="${r * 3.2}" height="${r2(ly - (y - r * 2))}" fill="${fc}" transform="rotate(${tilt} ${x} ${y})"/>`;
  s += `</g>`;
  if (lid > 0.02) s += `<rect x="${r2(x - r * 1.18)}" y="${r2(ly - r * 0.19)}" width="${r2(r * 2.36)}" height="${r2(r * 0.38)}" rx="${r2(r * 0.19)}" fill="${col.night}" transform="rotate(${tilt} ${x} ${y})"/>`;
  return s;
}

// ---------- hats ----------
// (x, y) is the centre of the hat's base, w the head width. turn shifts the crown toward the way he faces.
export function hat(style, x, y, w, col0 = PAL, tilt = 0, turn = 0) {
  const col = { ...col0, coatDk: col0.hatCol || col0.coatDk };
  let s = `<g transform="rotate(${tilt} ${x} ${y})">`;
  const tx = 3 * turn;
  if (style === 'topper') {
    const cw = w * 0.74, ct = w * 0.8, ch = w * 0.62, bw = w * 1.3, X = x + tx;
    s += `<path d="M${X - cw / 2} ${y - 2} L${X - ct / 2} ${y - ch + 3} Q${X - ct / 2} ${y - ch} ${X - ct / 2 + 4} ${y - ch} Q${X} ${y - ch - 2.5} ${X + ct / 2 - 4} ${y - ch} Q${X + ct / 2} ${y - ch} ${X + ct / 2} ${y - ch + 3} L${X + cw / 2} ${y - 2} Z" fill="${col.coatDk}"/>`;
    s += `<path d="M${X - cw / 2 + .2} ${y - 3} L${X - cw / 2 - 0.9} ${y - w * 0.2} H${X + cw / 2 + 0.9} L${X + cw / 2 - .2} ${y - 3} Z" fill="${col.brass}"/>`;
    if (turn) { const sx = turn > 0 ? X - ct / 2 : X + ct / 2, sd = turn > 0 ? 1 : -1, ww = 7 * Math.abs(turn) + 3; s += `<path d="M${sx} ${y - ch + 3} L${sx + sd * ww} ${y - ch + 2} L${X + sd * (ww - cw / 2) * (turn > 0 ? 1 : -1) * 0 + (turn > 0 ? X - cw / 2 + ww : X + cw / 2 - ww)} ${y - 3} L${turn > 0 ? X - cw / 2 : X + cw / 2} ${y - 2} Z" fill="#000" opacity=".22"/>`; }
    s += `<path d="M${x - bw / 2} ${y - 4.5} Q${x - bw / 2 + 3} ${y - 1} ${x - bw / 2 + 10} ${y - 1.5} H${x + bw / 2 - 10} Q${x + bw / 2 - 3} ${y - 1} ${x + bw / 2} ${y - 4.5} Q${x + bw / 2 + 1} ${y + 3} ${x + bw / 2 - 8} ${y + 3.5} H${x - bw / 2 + 8} Q${x - bw / 2 - 1} ${y + 3} ${x - bw / 2} ${y - 4.5} Z" fill="${col.coatDk}"/>`;
  } else if (style === 'bowler') {
    const cw = w * 0.8, ch = w * 0.5, bw = w * 1.22, X = x + tx;
    s += `<path d="M${X - cw / 2} ${y - 1} C${X - cw / 2} ${y - ch * 1.25} ${X + cw / 2} ${y - ch * 1.25} ${X + cw / 2} ${y - 1} Z" fill="${col.coatDk}"/>`;
    s += `<path d="M${X - cw / 2 + .5} ${y - 2} L${X - cw / 2 + 1.5} ${y - w * 0.15} H${X + cw / 2 - 1.5} L${X + cw / 2 - .5} ${y - 2} Z" fill="${col.brass}"/>`;
    s += `<path d="M${x - bw / 2} ${y - 5} Q${x - bw / 2 + 4} ${y + 1} ${x} ${y + 1} Q${x + bw / 2 - 4} ${y + 1} ${x + bw / 2} ${y - 5} Q${x + bw / 2} ${y + 5} ${x} ${y + 5} Q${x - bw / 2} ${y + 5} ${x - bw / 2} ${y - 5} Z" fill="${col.coatDk}"/>`;
  } else if (style === 'porter') {
    // a porter's cap without a peak: a straight round crown, brass band at the base, piping on the top
    const cw = w * 0.82, ch = w * 0.42, X = x + tx;
    s += `<path d="M${X - cw / 2} ${y + 1} V${y - ch + 4} Q${X - cw / 2} ${y - ch} ${X - cw / 2 + 5} ${y - ch} H${X + cw / 2 - 5} Q${X + cw / 2} ${y - ch} ${X + cw / 2} ${y - ch + 4} V${y + 1} Z" fill="${col.coatDk}"/>`;
    s += `<rect x="${X - cw / 2}" y="${y - w * 0.15}" width="${cw}" height="${w * 0.11}" fill="${col.brass}"/><rect x="${X - cw / 2 + 2}" y="${y - ch + 1}" width="${cw - 4}" height="2" rx="1" fill="${col.brass}"/>`;
  }
  return s + `</g>`;
}

// ---------- hands and arms ----------
const P2 = (p) => `${r2(p[0])} ${r2(p[1])}`;
// a tapered limb segment: two round ends joined by their tangents
function limb(a, ra, b, rb, fill) {
  const dx = b[0] - a[0], dy = b[1] - a[1], L = Math.hypot(dx, dy) || 1, nx = -dy / L, ny = dx / L;
  return `<circle cx="${r2(a[0])}" cy="${r2(a[1])}" r="${ra}" fill="${fill}"/><circle cx="${r2(b[0])}" cy="${r2(b[1])}" r="${rb}" fill="${fill}"/>` +
    `<path d="M${P2([a[0] + nx * ra, a[1] + ny * ra])} L${P2([b[0] + nx * rb, b[1] + ny * rb])} L${P2([b[0] - nx * rb, b[1] - ny * rb])} L${P2([a[0] - nx * ra, a[1] - ny * ra])} Z" fill="${fill}"/>`;
}
// a mitten at (x, y) pointing along angle a (deg); thumb side +1/-1; kind: 'mitt' | 'flat' | 'point' | 'grip'
export function mitten(x, y, a, thumb = 1, kind = 'mitt', col = PAL) {
  const k = { mitt: [7.6, 6.6, 2], flat: [8.4, 5.2, 2.5], point: [10.5, 5.6, 4], grip: [6.4, 6.8, 0] }[kind] || [7.6, 6.6, 2];
  let s = `<g transform="translate(${r2(x)} ${r2(y)}) rotate(${r2(a)})">`;
  const tx = kind === 'grip' ? -1 : 0.5, ty = thumb * (kind === 'grip' ? 4.5 : 5.6);
  s += `<ellipse cx="${tx}" cy="${ty}" rx="3.9" ry="3" transform="rotate(${-thumb * 38} ${tx} ${ty})" fill="${col.glove}"/>`;
  s += `<ellipse cx="${k[2]}" cy="0" rx="${k[0]}" ry="${k[1]}" fill="${col.glove}"/>`;
  if (!col.mono) s += `<ellipse cx="${k[2] + 1}" cy="${-thumb * k[1] * 0.42}" rx="${k[0] * 0.55}" ry="${k[1] * 0.28}" fill="#FFFFFF" opacity=".16"/>`;
  if (kind === 'point') s += `<path d="M${k[2] + 2} ${-thumb * 1.5} h5" stroke="${col.gloveLine}" stroke-width="1.1" stroke-linecap="round" opacity=".5"/>`;
  return s + `</g>`;
}
// shoulder -> elbow -> hand: tapered sleeve that bends at the elbow, a crease inside the bend, a brass cuff, a mitten
export function arm(sh, el, hd, { thumb = 1, kind = 'mitt', col = PAL } = {}) {
  const a = Math.atan2(hd[1] - el[1], hd[0] - el[0]), deg = a * 180 / Math.PI;
  const wr = [hd[0] - Math.cos(a) * 8, hd[1] - Math.sin(a) * 8];
  let s = limb(sh, 7.2, el, 5.9, col.coatDk) + limb(el, 5.9, wr, 5.1, col.coatDk);
  // crease on the inside of the bend
  const v1 = [sh[0] - el[0], sh[1] - el[1]], v2 = [wr[0] - el[0], wr[1] - el[1]];
  const l1 = Math.hypot(...v1) || 1, l2 = Math.hypot(...v2) || 1;
  const bis = [v1[0] / l1 + v2[0] / l2, v1[1] / l1 + v2[1] / l2], lb = Math.hypot(...bis);
  if (lb > 0.35 && !col.mono) {
    const u = [bis[0] / lb, bis[1] / lb], c = [el[0] + u[0] * 4.2, el[1] + u[1] * 4.2], pp = [-u[1], u[0]];
    s += `<path d="M${P2([c[0] - pp[0] * 3.5, c[1] - pp[1] * 3.5])} Q${P2([c[0] - u[0] * 1.8, c[1] - u[1] * 1.8])} ${P2([c[0] + pp[0] * 3.5, c[1] + pp[1] * 3.5])}" stroke="${col.night}" stroke-width="1.5" fill="none" opacity=".35" stroke-linecap="round"/>`;
  }
  if (!col.mono) s += `<rect x="-3.2" y="-6.6" width="6.4" height="13.2" rx="2" fill="${col.brass}" transform="translate(${P2(wr)}) rotate(${r2(deg)})"/>`;
  s += mitten(hd[0], hd[1], deg, thumb, kind, col);
  return s;
}

// ---------- the figure ----------
// Local frame: feet centre at (0,0), up is negative y. Pose fields:
// lean (deg, about the feet), bow (the coat's curve), turn (-1..1; +0.5 is three-quarter, facing our right),
// headTilt, eyes{lid, lidL, lidR, tiltL, tiltR, look}, arms {L, R} (L is the arm on our left),
// each {e(sh) -> elbow, h(sh) -> hand, thumb, kind, layer: 'back'|'front'}, feet [dx, dx], hat, hatTilt, props.
export const G = { headW: 54, headTop: -172, collarY: -126, coatTop: -114, shW: 62, hemW: 74, eyeY: -146, eyeGap: 24, eyeR: 9.2 };

export function figure(p = {}, col = PAL) {
  const lean = p.lean || 0, ht = p.headTilt || 0, t = p.turn || 0, bow = p.bow || 0, at = Math.abs(t);
  const hatStyle = p.hat || p.hatStyle || 'topper';
  const L = (x, y) => rot(x, y, lean);
  const sw = G.shW / 2, hw = G.hemW / 2, ct = G.coatTop;
  // shoulders: the far one tucks in as he turns
  const up = p.shrug || 0;
  const shL = L(-sw + 4 + (t < 0 ? 10 * at : 2 * at), -103 - up), shR = L(sw - 4 - (t > 0 ? 10 * at : 2 * at), -103 - up);
  const E = p.eyes || {};
  const A = p.arms || {};
  const armOf = (side) => { const a = A[side]; if (!a) return ''; const sh = side === 'L' ? shL : shR; return arm(sh, a.e(sh), a.h(sh), { thumb: a.thumb ?? (side === 'L' ? 1 : -1), kind: a.kind || 'mitt', col }); };
  const layer = (side) => A[side]?.layer || ((t > 0.25 && side === 'R') || (t < -0.25 && side === 'L') ? 'back' : 'front');
  let s = '';
  if (p.props?.before) s += p.props.before;
  if (p.ground !== false && !col.mono) s += `<ellipse cx="${lean * 1.2 + t * 6}" cy="2" rx="46" ry="5.5" fill="${col.night}" opacity=".13"/>`;
  for (const side of ['L', 'R']) if (layer(side) === 'back') s += armOf(side);
  let b = '';
  const [fl, fr] = p.feet || [-12, 12];
  const [hl, hr] = p.heels || [0, 0];
  const shoe = (x, lift, dir) => `<ellipse cx="${x}" cy="${-1 - lift * 0.5}" rx="10" ry="6" transform="rotate(${dir * lift * 3} ${x} ${-1 - lift * 0.5})" fill="${col.shoe}" stroke="${col.shoeRim}" stroke-width="1.4"/>`;
  b += shoe(fl, hl, -1) + shoe(fr, hr, 1);
  // greatcoat: shoulders to the floor; its sides curve with the pose (bow)
  const cy = (ct + 12 - 6) / 2;
  const fL = t < 0 ? -5 * at : 2 * at, fR = t > 0 ? -5 * at : 2 * at; // far edge tucks in, near edge swells
  const coatD = `M${-sw} ${ct + 12} Q${-sw} ${ct} ${-sw + 12} ${ct} H${sw - 12} Q${sw} ${ct} ${sw} ${ct + 12} Q${r2((sw + hw) / 2 - bow + fR)} ${cy} ${r2(hw + fR * 0.6)} -6 Q${hw} -2 ${hw - 4} -2 Q0 1 ${-hw + 4} -2 Q${-hw} -2 ${r2(-hw - fL * 0.6)} -6 Q${r2(-(sw + hw) / 2 - bow - fL)} ${cy} ${-sw} ${ct + 12} Z`;
  b += `<path d="${coatD}" fill="${col.coat}"/>`;
  const fx = 5 + 12 * t;
  if (!col.mono) {
    const cid = uid('c');
    // shade on the far side, a side plane when turned, light on the near edge
    const side = t >= 0 ? -1 : 1;
    b += `<clipPath id="${cid}"><path d="${coatD}"/></clipPath><g clip-path="url(#${cid})">`;
    b += `<path d="M${side * (hw + 6)} ${ct - 4} H${side * (sw - 10 - 14 * at)} L${side * (hw - 12 - 16 * at)} 4 H${side * (hw + 6)}Z" fill="${col.night}" opacity="${0.14 + 0.08 * at}"/>`;
    b += `<path d="M${-side * (sw - 3)} ${ct - 4} H${-side * (sw + 8)} V4 H${-side * (hw - 4)}Z" fill="#FFF6DD" opacity=".1"/></g>`;
    b += `<path d="M${r2(fx)} ${ct + 2} Q${r2(fx - bow * 0.5)} ${cy} ${r2(fx + 3)} -3" stroke="${col.coatDk}" stroke-width="2.2" fill="none"/>`;
    for (const yy of [-98, -78, -58]) for (const [xx, k] of [[-9 + 13 * t, 1], [17 + 8 * t, 1]]) b += `<circle cx="${r2(xx + (yy + 98) * 0.02 - bow * (yy + 114) / 220)}" cy="${yy}" r="${r2(3 - (t > 0 && xx > 0 ? 0.6 * at : 0))}" fill="${col.brass}"/>`;
    // pocket flaps and a darker hem band, the details that make it a coat
    b += `<g clip-path="url(#${cid})"><path d="M${-hw - 10} -14 H${hw + 10} V-4 H${-hw - 10}Z" fill="${col.coatDk}" opacity=".45"/></g>`;
    for (const [px, pw] of [[-24 + 10 * t, 14], [16 + 8 * t, 13 - 4 * at]]) b += `<rect x="${r2(px - bow * 0.3)}" y="-46" width="${r2(pw)}" height="4" rx="2" fill="${col.coatDk}"/>`;
    // the doorman's keys, on a brass ring at his hip
    const kx = -sw + 2 + 6 * t - bow * 0.2;
    b += `<g transform="rotate(${r2(-lean * 1.5)} ${r2(kx)} -64)"><circle cx="${r2(kx)}" cy="-58" r="4.6" fill="none" stroke="${col.brass}" stroke-width="1.8"/><rect x="${r2(kx - 3.6)}" y="-55" width="2.6" height="10" rx="1" fill="${col.brass}" transform="rotate(14 ${r2(kx)} -55)"/><rect x="${r2(kx + 0.6)}" y="-54.5" width="2.6" height="8.5" rx="1" fill="${col.brass}" transform="rotate(-10 ${r2(kx)} -55)"/></g>`;
    const ax = -9 + 13 * t;
    b += `<path d="M${-sw + 5} ${ct + 4} Q${-sw + 1} ${ct + 34} ${r2(ax)} -98 M${-sw + 6} ${ct + 4} Q${-sw - 4} ${ct + 50} ${r2(ax)} -98" stroke="${col.brass}" stroke-width="2.4" fill="none" stroke-linecap="round"/>`;
    b += `<path d="M${-sw + 1.5} ${ct + 33} l-1 9" stroke="${col.brass}" stroke-width="3.2" stroke-linecap="round"/>`;
    b += `<rect x="${-sw + 1}" y="${ct + 1}" width="13" height="6" rx="3" fill="${col.brass}"/>`;
  }
  // head: face zone with a side plane when turned; eyes shift and the far eye narrows
  let h = '';
  const hw2 = G.headW / 2, top = G.headTop;
  const headD = `M${-hw2} ${G.collarY + 6} V${top + 16} Q${-hw2} ${top} ${-hw2 + 16} ${top} H${hw2 - 16} Q${hw2} ${top} ${hw2} ${top + 16} V${G.collarY + 6} Z`;
  h += `<path d="${headD}" fill="${col.face}"/>`;
  if (!col.mono) {
    const hid = uid('h'), side = t >= 0 ? -1 : 1;
    h += `<clipPath id="${hid}"><path d="${headD}"/></clipPath><path clip-path="url(#${hid})" d="M${side * (hw2 + 2)} ${top} H${side * (hw2 - 9 - 12 * at)} V${G.collarY + 8} H${side * (hw2 + 2)}Z" fill="${col.night}" opacity="${0.13 + 0.07 * at}"/>`;
  }
  const lk = E.look || [0.15 + t * 0.5, 0.3];
  const gap = G.eyeGap - 7 * at, ex = 8 * t;
  const sxL = t < 0 ? 1 - 0.38 * at : 1, sxR = t > 0 ? 1 - 0.38 * at : 1;
  const eyeAt = (x, sx, o) => `<g transform="translate(${r2(x)} ${G.eyeY}) scale(${r2(sx)} 1)">${eye(0, 0, G.eyeR, o)}</g>`;
  h += eyeAt(ex - gap / 2, sxL, { lid: E.lidL ?? E.lid ?? 0.5, tilt: E.tiltL ?? -4, look: lk, col });
  h += eyeAt(ex + gap / 2, sxR, { lid: E.lidR ?? E.lid ?? 0.5, tilt: E.tiltR ?? 3, look: lk, col });
  if (!p.hatOff) h += hat(hatStyle, 0, top + 4, G.headW, col, p.hatTilt ?? -5, t);
  b += `<g transform="translate(0 ${r2(up * 0.6)}) rotate(${ht} 0 ${G.collarY})">${h}</g>`;
  // standing collar
  b += `<path d="M${-hw2 - 4} ${G.collarY - 2} H${hw2 + 4} V${ct + 4} Q${r2(6 * t)} ${ct + 8} ${-hw2 - 4} ${ct + 4} Z" fill="${col.coatDk}"/>`;
  if (!col.mono) b += `<rect x="${-hw2 - 4}" y="${G.collarY - 3}" width="${G.headW + 8}" height="2.6" rx="1.3" fill="${col.brass}"/>`;
  s += `<g transform="rotate(${lean})">${b}</g>`;
  if (p.props?.mid) s += p.props.mid;
  for (const side of ['L', 'R']) if (layer(side) === 'front') s += armOf(side);
  if (p.props?.after) s += p.props.after;
  return s;
}

// ---------- props ----------
const P = {
  board: (x, y, a, col) => `<g transform="rotate(${a} ${x} ${y})"><rect x="${x - 15}" y="${y - 19}" width="30" height="38" rx="3" fill="#8A5A2B"/><rect x="${x - 12}" y="${y - 15}" width="24" height="31" rx="1.5" fill="${col.paper}"/><rect x="${x - 6}" y="${y - 21}" width="12" height="6" rx="2" fill="${col.night}"/><path d="M${x - 8} ${y - 8} h12 M${x - 8} ${y - 1} h16 M${x - 8} ${y + 6} h10" stroke="${col.night}" stroke-width="2" stroke-linecap="round"/><path d="M${x + 5} ${y + 4} l2.5 3 l5 -6" stroke="#2E9F62" stroke-width="2.2" fill="none" stroke-linecap="round" stroke-linejoin="round"/></g>`,
  list: (x, y, a, col) => `<g transform="rotate(${a} ${x} ${y})"><rect x="${x - 14}" y="${y - 20}" width="28" height="40" rx="2" fill="${col.paper}"/><text x="${x - 10}" y="${y - 9}" font-family="'Atkinson Hyperlegible Mono',ui-monospace,monospace" font-size="6.2" fill="${col.night}">07:45</text><path d="M${x - 10} ${y - 2} h20 M${x - 10} ${y + 5} h16 M${x - 10} ${y + 12} h18" stroke="${col.night}" stroke-width="1.8" stroke-linecap="round"/></g>`,
  switchplate: (x, y, on, col) => `<rect x="${x - 9}" y="${y - 14}" width="18" height="28" rx="3" fill="${col.paper}" stroke="#B9B4A7" stroke-width="1.2"/><rect x="${x - 3}" y="${y + (on ? -9 : 1)}" width="6" height="8" rx="1.5" fill="#9C978A"/>`,
  note: (x, y, a, col) => `<g transform="rotate(${a} ${x} ${y})"><rect x="${x - 12}" y="${y - 14}" width="24" height="28" rx="1.5" fill="${col.paper}"/><rect x="${x - 1.5}" y="${y - 9}" width="3" height="11" rx="1.5" fill="#D9443A"/><circle cx="${x}" cy="${y + 7}" r="1.8" fill="#D9443A"/><circle cx="${x}" cy="${y - 15}" r="2.4" fill="#D9443A"/></g>`,
  pencil: (x, y, a) => `<g transform="rotate(${a} ${x} ${y})"><rect x="${x - 1.6}" y="${y - 16}" width="3.2" height="16" fill="#F5B53F"/><path d="M${x - 1.6} ${y} L${x} ${y + 4} L${x + 1.6} ${y}Z" fill="#0F1716"/></g>`,
};
export { P as PROPS };

// ---------- the hand-made finish ----------
// For illustrations only, never the logo: a slight wobble on every edge and a print grain inside the fills.
export function handFilter(id = 'hand', wobble = 1.8, grain = 0.16) {
  return `<filter id="${id}" x="-5%" y="-5%" width="110%" height="110%" color-interpolation-filters="sRGB">` +
    `<feTurbulence type="fractalNoise" baseFrequency=".045" numOctaves="2" seed="3" result="w"/>` +
    `<feDisplacementMap in="SourceGraphic" in2="w" scale="${wobble}" xChannelSelector="R" yChannelSelector="G" result="d"/>` +
    `<feTurbulence type="fractalNoise" baseFrequency=".85" numOctaves="2" seed="9" result="n"/>` +
    `<feColorMatrix in="n" values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 ${grain} 0" result="na"/>` +
    `<feComposite in="na" in2="d" operator="in" result="g"/>` +
    `<feTurbulence type="fractalNoise" baseFrequency=".018" numOctaves="2" seed="21" result="m"/>` +
    `<feColorMatrix in="m" values="0 0 0 0 1  0 0 0 0 1  0 0 0 0 1  0 0 0 .09 -.01" result="ma"/>` +
    `<feComposite in="ma" in2="d" operator="in" result="ink"/>` +
    `<feMerge><feMergeNode in="d"/><feMergeNode in="ink"/><feMergeNode in="g"/></feMerge></filter>`;
}

// ---------- the pose library ----------
// Each pose is a real job Napwake does, or a screen it has to fill. Arms: L is the arm on our left.
const at = (dx, dy) => ([x, y]) => [x + dx, y + dy];
const abs = (x, y) => () => [x, y];
const relaxed = (side) => side === 'L' ? { e: at(-4, 30), h: at(1, 56), thumb: 1 } : { e: at(4, 30), h: at(-1, 56), thumb: -1 };
const akimbo = (side) => side === 'L' ? { e: at(-17, 20), h: at(-3, 40), thumb: 1, kind: 'grip' } : { e: at(17, 20), h: at(3, 40), thumb: -1, kind: 'grip' };
export const POSES = {
  duty: { title: 'On duty', job: 'Napwake, running', lean: -1, headTilt: -2, eyes: { lid: 0.5, tiltL: -5, tiltR: 3, look: [0.2, 0.35] },
    arms: { L: { e: at(-6, 30), h: abs(-5, -60), thumb: 1 }, R: { e: at(6, 30), h: abs(6, -58), thumb: -1 } } },
  door: { title: 'Holds the door', job: 'wakes a room when someone knocks', lean: 7, bow: 6, turn: 0.5, headTilt: 5, feet: [-17, 13], heels: [3, 0], eyes: { lid: 0.45, tiltL: -2, tiltR: 6, look: [-0.2, 0.25] },
    arms: { R: { e: at(22, 10), h: at(37, -12), thumb: -1, kind: 'grip' }, L: relaxed('L') } },
  list: { title: 'The morning list', job: 'Napwake wakes rooms before the team arrives', lean: -3, turn: 0.2, headTilt: 8, eyes: { lid: 0.62, tiltL: 2, tiltR: 6, look: [0.1, 0.85] },
    arms: { L: { e: at(-4, 30), h: abs(-11, -84), thumb: 1, kind: 'grip' }, R: { e: at(4, 30), h: abs(13, -86), thumb: -1, kind: 'grip' } } },
  rounds: { title: 'Does the rounds', job: 'Napwake checks which rooms are empty', lean: 7, bow: 7, turn: 0.6, headTilt: 6, feet: [-22, 17], heels: [5, 0], eyes: { lid: 0.55, tiltL: -3, tiltR: 4, look: [0.4, 0.8] },
    arms: { L: { e: at(-1, 28), h: abs(-2, -80), thumb: 1, kind: 'grip' }, R: { e: at(3, 27), h: abs(10, -74), thumb: -1, kind: 'grip', layer: 'front' } } },
  lights: { title: 'Lights out', job: 'Napwake puts an empty room to sleep', lean: 5, bow: 3, turn: 0.5, headTilt: 4, eyes: { lid: 0.66, tiltL: 4, tiltR: 6, look: [0.8, 0.2] },
    arms: { R: { e: at(22, 4), h: at(43, -4), thumb: -1, kind: 'point' }, L: relaxed('L') } },
  note: { title: 'Pins a note', job: 'something needs you', lean: 6, bow: 3, turn: 0.5, headTilt: 2, eyes: { lid: 0.2, tiltL: -2, tiltR: 2, look: [0.7, 0.1] },
    arms: { R: { e: at(21, 4), h: at(37, -14), thumb: -1, kind: 'flat' }, L: relaxed('L') } },
  shrug: { title: 'No room by that name', job: 'page not found', lean: 0, shrug: 6, headTilt: -8, eyes: { lidL: 0.58, lidR: 0.4, tiltL: -10, tiltR: 6, look: [0, 0.2] },
    arms: { L: { e: at(-17, 22), h: at(-31, 0), thumb: -1, kind: 'flat' }, R: { e: at(17, 22), h: at(31, 0), thumb: 1, kind: 'flat' } } },
  rest: { title: 'Resting his eyes', job: 'everything is asleep', lean: -2, bow: -2, headTilt: 9, eyes: { lid: 1, tiltL: 4, tiltR: 6 },
    arms: { L: { e: at(-6, 30), h: abs(-5, -60), thumb: 1 }, R: { e: at(6, 30), h: abs(6, -58), thumb: -1 } } },
  tip: { title: 'Tips his hat', job: 'thanks, and what\'s new', lean: 9, bow: 5, turn: -0.15, headTilt: 10, heels: [0, 2], eyes: { lid: 0.55, tiltL: -2, tiltR: 4, look: [-0.2, 0.3] }, hatTilt: -16,
    arms: { R: { e: at(21, -6), h: at(5, -70), thumb: -1, kind: 'grip' }, L: relaxed('L') } },
  watch: { title: 'Checks his watch', job: 'how long a room has been idle', lean: -2, turn: -0.35, headTilt: 9, feet: [-13, 12], eyes: { lid: 0.62, tiltL: 3, tiltR: 7, look: [-0.1, 0.85] },
    arms: { R: { e: at(4, 30), h: abs(6, -88), thumb: -1, kind: 'grip' }, L: akimbo('L') } },
  point: { title: 'This way', job: 'getting started', lean: -5, bow: -3, turn: 0.4, headTilt: -4, feet: [-13, 16], eyes: { lid: 0.48, tiltL: -4, tiltR: 2, look: [0.9, 0.15] },
    arms: { R: { e: at(26, 2), h: at(54, -4), thumb: -1, kind: 'point', layer: 'front' }, L: akimbo('L') } },
};

// props, by layer: before (behind him), mid (behind his front arms), after (in front)
export function poseProps(name, col = PAL) {
  if (name === 'list') return { mid: P.list(1, -90, -5, col) };
  if (name === 'rounds') return { mid: P.board(-3, -88, -8, col), after: P.pencil(13, -70, 32) };
  if (name === 'lights') return { before: P.switchplate(84, -104, false, col) };
  if (name === 'note') return { before: `<rect x="62" y="-200" width="70" height="200" rx="4" fill="#5B6B86"/>` + P.note(72, -116, 4, col) };
  if (name === 'watch') return { after: `<path d="M-9 -98 Q-2 -84 3 -86" stroke="${col.brass}" stroke-width="1.6" fill="none"/><circle cx="10" cy="-92" r="8.5" fill="${col.brass}"/><circle cx="10" cy="-92" r="6.2" fill="${col.paper}"/><path d="M10 -92 V-96.5 M10 -92 L13 -90.5" stroke="${col.night}" stroke-width="1.3" stroke-linecap="round"/>` };
  if (name === 'door') return { before: `<path d="M62 0 V-176 Q62 -196 82 -196 H140 V0 Z" fill="#FFE3A1"/><path d="M62 0 V-176 Q62 -196 82 -196 L94 -192 V-2 Z" fill="#5B6B86"/>` };
  return {};
}

export function poseSvg(name, { col = PAL, hat = 'topper', hatStyle, w = 240, h = 260, floor = 244, cx = 100, extra = '', hand = false } = {}) {
  const p = { ...POSES[name], hat: hatStyle || hat, props: poseProps(name, col) };
  const id = 'h' + (++UID);
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}">${hand ? `<defs>${handFilter(id)}</defs>` : ''}${extra}<g transform="translate(${cx} ${floor})"${hand ? ` filter="url(#${id})"` : ''}>${figure(p, col)}</g></svg>`;
}

// ---------- the symbol: head, hat, collar and shoulders on a 96 grid ----------
export function bust({ col = PAL, lid = 0.5, tiltL = -5, tiltR = 3, look = [0, 0.3], hatTilt = -5 } = {}) {
  let s = '';
  // shoulders (coat), cut by the bottom edge
  s += `<path d="M12 96 V88 Q12 79 22 79 H74 Q84 79 84 88 V96 Z" fill="${col.coat}"/>`;
  if (!col.mono) s += `<rect x="16" y="80" width="13" height="5.5" rx="2.75" fill="${col.brass}"/><circle cx="40" cy="91" r="2.6" fill="${col.brass}"/><circle cx="58" cy="91" r="2.6" fill="${col.brass}"/>`;
  // face zone
  s += `<path d="M26 76 V44 Q26 36 34 36 H62 Q70 36 70 44 V76 Z" fill="${col.face}"/>`;
  // eyes: radius 0.17 and centres 0.44 of the face's width apart, as on the full figure
  s += eye(38.3, 55, 7.5, { lid, tilt: tiltL, look, col });
  s += eye(57.7, 55, 7.5, { lid, tilt: tiltR, look, col });
  // standing collar
  s += `<path d="M23 71 H73 V80 Q48 83 23 80 Z" fill="${col.coatDk}"/>`;
  if (!col.mono) s += `<rect x="23" y="70" width="50" height="2.4" rx="1.2" fill="${col.brass}"/>`;
  // top hat: crown, band, a brim well past the head so it reads as a hat at any size
  s += `<g transform="rotate(${hatTilt} 48 38)">`;
  const hc = col.hatCol || col.coatDk;
  s += `<path d="M31 38 L29.5 13 Q29.5 10 32.5 10 Q48 8.5 63.5 10 Q66.5 10 66.5 13 L65 38 Z" fill="${hc}"/>`;
  s += `<path d="M30.6 31 L30.2 25 H65.8 L65.4 31 Z" fill="${col.mono ? (col.paper) : col.brass}"/>`;
  s += `<path d="M14 37 Q15 34 20 34.5 H76 Q81 34 82 37 Q82 41.5 76 41.5 H20 Q14 41.5 14 37 Z" fill="${hc}"/>`;
  s += `</g>`;
  return s;
}

// The 16-pixel cut: drawn on whole pixels, not scaled.
export function favicon16(col = PAL) {
  const R = (x, y, w, h, f) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" fill="${f}"/>`;
  return R(2, 13, 12, 3, col.coat) + R(3, 12, 10, 1, col.coat) +
    R(4, 6, 8, 6, col.face) +
    R(4, 11, 8, 2, col.coatDk) + (col.mono ? '' : R(4, 11, 8, 1, col.brass)) +
    R(5, 8, 2, 2, col.eye) + R(9, 8, 2, 2, col.eye) + R(5, 8, 2, 1, col.night) + R(9, 8, 2, 1, col.night) + R(6, 9, 1, 1, col.night) + R(10, 9, 1, 1, col.night) +
    R(5, 1, 6, 4, col.hatCol || col.coatDk) + R(5, 3, 6, 1, col.mono ? col.paper : col.brass) + R(2, 5, 12, 1, col.hatCol || col.coatDk);
}
