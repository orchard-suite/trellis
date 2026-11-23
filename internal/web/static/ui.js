// The board's kit: element helpers, icons, the words for levels, severities,
// kinds, and packs, and the small charts every view draws from.

// Kinds and packs are the estate's: the board names them, and learn() fills
// these from each board read. The words below are only for kinds that read
// oddly with an s on the end.
export const KINDS = [];
export const KIND_WORDS = { api: 'APIs', repository: 'Repositories', cluster: 'Cluster' };
export const KIND_ONE = { api: 'API' };
export const PACK_WORDS = {};
export const PACK_ABOUT = {};

const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1).replace(/_/g, ' ');

/** learn takes the kinds and packs a board read names. */
export function learn(b) {
  KINDS.splice(0, KINDS.length, ...b.kinds.map((k) => k.kind));
  for (const k of KINDS) {
    if (!KIND_ONE[k]) KIND_ONE[k] = cap(k);
    if (!KIND_WORDS[k]) KIND_WORDS[k] = KIND_ONE[k] + 's';
  }
  for (const p of b.packs) {
    PACK_WORDS[p.pack] = p.title || cap(p.pack);
    PACK_ABOUT[p.pack] = p.about || '';
  }
}

/** pillarWord is a pillar's catalog key as a person reads it. */
export const pillarWord = (key) => cap(key.slice(key.lastIndexOf('/') + 1));

/** set says whether a time from the board is set: Go writes a zero one as year 1. */
export const set = (iso) => !!iso && !iso.startsWith('0001');

/** ends is when a waiver ends: a mitigated one's review, else its expiry. */
export const ends = (w) => {
  const mitigated = w.category === 'mitigated' || (!w.category && w.control);
  if (mitigated && set(w.review_at)) return w.review_at;
  return set(w.expires_at) ? w.expires_at : w.review_at;
};

/** The states a finding can be in, as a person reads them. */
export const STATES = ['open', 'waived', 'announced', 'draft'];
export const STATE_WORDS = { open: 'Open', waived: 'Waived', announced: 'Coming', draft: 'Draft', all: 'All' };
export const STATE_ABOUT = {
  open: 'Counts now: it sets levels, has a deadline, and fails a gate at its severity.',
  waived: 'Set aside by a waiver until the waiver ends.',
  announced: 'Its rule starts to count on a later day.',
  draft: 'Its rule is being tried. It never counts.',
};

/** The filters a move to another view clears. */
export const CLEAR = { pack: '', kind: '', rule: '', severity: '', pillar: '', level: '', owner: '', state: '', overdue: false, q: '', open: '' };

/** An owner as a person reads it; the empty owner is nobody's. */
export const ownerWord = (o) => o || 'No owner';
/** The URL value for an owner: "-" is the empty owner. */
export const ownerKey = (o) => o || '-';
export const ownerOf = (k) => (k === '-' ? '' : k);

/** The words for each rollup. */
export function rollupWords(r) {
  if (!r || r.mode !== 'share') return 'The worst subject sets the level.';
  return 'Green at ' + r.green + '% conformant, amber at ' + r.amber + '%, red below that or on any critical finding.';
}

/** The words for what changed between two passes. */
export const EVENT_WORDS = {
  opened: 'Opened', resolved: 'Fixed', waived: 'Waived', overdue: 'Overdue', unknown: 'Went dark',
  waiver_ending: 'Waiver ending', waiver_ended: 'Waiver ended',
};
export const EVENT_ABOUT = {
  opened: 'A finding counts that did not before.',
  resolved: 'A finding that counted now passes or no longer applies.',
  waived: 'A finding that counted is now covered by a waiver.',
  overdue: 'An open finding passed its deadline.',
  unknown: 'A finding that counted now has no data, so nobody can say whether it was fixed.',
  waiver_ending: 'A waiver ends soon.',
  waiver_ended: 'A waiver ended, so what it covered counts again.',
};

/** pctOf rounds a percent the board gives, or says there is none. */
export const pctOf = (v) => (v === null || v === undefined ? 'n/a' : (Math.round(v * 10) / 10) + '%');
export const SEVERITIES = ['critical', 'high', 'medium', 'low'];
export const SEV_WORDS = { critical: 'Critical', high: 'High', medium: 'Medium', low: 'Low' };
export const LEVELS = ['red', 'amber', 'green', 'grey'];
export const LEVEL_WORDS = { red: 'Failing', amber: 'At risk', green: 'Conformant', grey: 'No data', draft: 'Draft only' };

/** A subject's status: its level, or draft when only draft packs judged it. */
export function statusOf(x) {
  return x.draft ? 'draft' : x.level;
}

/** el('div', 'cls', child | text | [children]) */
export function el(tag, cls, kids) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  add(e, kids);
  return e;
}

export function add(e, kids) {
  if (kids === undefined || kids === null || kids === false) return e;
  if (Array.isArray(kids)) { kids.forEach((k) => add(e, k)); return e; }
  e.append(kids instanceof Node ? kids : document.createTextNode(String(kids)));
  return e;
}

export function button(cls, kids, onClick, label) {
  const b = el('button', cls, kids);
  b.type = 'button';
  if (label) b.setAttribute('aria-label', label);
  if (onClick) b.addEventListener('click', onClick);
  return b;
}

export const num = (n) => Number(n || 0).toLocaleString('en-US');
export const plural = (n, one, many) => num(n) + ' ' + (n === 1 ? one : many);
export const pct = (n, of) => (of ? Math.round((n / of) * 100) : 0);

export function ago(iso) {
  if (!iso) return 'never';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 90) return 'just now';
  if (s < 5400) return Math.round(s / 60) + ' min ago';
  if (s < 129600) return Math.round(s / 3600) + ' h ago';
  return Math.round(s / 86400) + ' d ago';
}

export function daysLeft(iso) {
  return Math.ceil((new Date(iso).getTime() - Date.now()) / 86400000);
}

export function date(iso) {
  return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
}

export function dateTime(iso) {
  return new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}

/** due is a finding's deadline as a chip: overdue, or days left. */
export function due(f) {
  if (f.state !== 'open' || !f.due) return null;
  const d = daysLeft(f.due);
  const c = el('span', 'exp' + (f.overdue ? ' late' : d <= 7 ? ' soon' : ''), f.overdue ? 'Overdue ' + Math.max(1, -d) + ' d' : d <= 1 ? 'Due today' : 'Due in ' + d + ' d');
  c.dataset.tip = 'Due ' + date(f.due) + (set(f.first_seen) ? ', first seen ' + date(f.first_seen) : '');
  return c;
}

/** carried marks a finding judged on facts an earlier pass read. */
export function carried(f) {
  if (!f.carried) return null;
  const c = el('span', 'tag carried', 'Old facts');
  c.dataset.tip = 'Judged on facts from ' + (set(f.as_of) ? dateTime(f.as_of) : 'an earlier pass') + '. The source could not read them this time.';
  return c;
}

/** A finding's state as a tag. */
export function stateTag(f) {
  const t = el('span', 'tag st-' + f.state, STATE_WORDS[f.state] || f.state);
  t.dataset.tip = f.state === 'announced' && f.counts_from ? 'Counts from ' + date(f.counts_from) : STATE_ABOUT[f.state] || '';
  return t;
}

const PATHS = {
  overview: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  findings: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
  subjects: '<path d="m12 2 9 5-9 5-9-5 9-5z"/><path d="m3 12 9 5 9-5"/><path d="m3 17 9 5 9-5"/>',
  rules: '<path d="M9 6h11M9 12h11M9 18h11"/><path d="m3 6 1.5 1.5L7 5M3 12l1.5 1.5L7 11M3 18l1.5 1.5L7 17"/>',
  waivers: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><path d="M12 8v4l2.5 1.5"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-2.6-6.4L21 8"/><path d="M21 3v5h-5"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  close: '<path d="M18 6 6 18M6 6l12 12"/>',
  chevron: '<path d="m9 6 6 6-6 6"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  check: '<path d="m5 12 5 5L20 7"/>',
  arrow: '<path d="M5 12h14M13 6l6 6-6 6"/>',
  source: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  wrench: '<path d="M14.7 6.3a4 4 0 0 0-5.4 5.2L3 17.8V21h3.2l6.3-6.3a4 4 0 0 0 5.2-5.4l-2.6 2.6-2.4-.6-.6-2.4 2.6-2.6z"/>',
  eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  owners: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M16 4.5a3.5 3.5 0 0 1 0 7M18 14a6.5 6.5 0 0 1 3.5 6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
};

export function icon(name, size = 16) {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('width', size);
  s.setAttribute('height', size);
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.75');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  s.setAttribute('aria-hidden', 'true');
  s.innerHTML = PATHS[name] || '';
  return s;
}

/** A level as a dot and its word. */
export function levelChip(level, words) {
  return el('span', 'chip lv-' + level, [el('i', 'dot'), words || LEVEL_WORDS[level]]);
}

/** A severity as a filled marker and its word. */
export function sevChip(sev) {
  return el('span', 'sev sv-' + sev, [el('i', 'mark'), SEV_WORDS[sev] || sev]);
}

export const mono = (text, cls) => el('code', 'mono' + (cls ? ' ' + cls : ''), text);

/**
 * A stacked bar of parts [{key, n, label}] with a 2px gap between them. The
 * class `lv-<key>` or `sv-<key>` colors each part; each carries a tooltip.
 */
export function stack(parts, prefix, opts = {}) {
  const total = parts.reduce((a, p) => a + p.n, 0);
  const bar = el('div', 'stack' + (opts.tall ? ' tall' : ''));
  bar.setAttribute('role', 'img');
  bar.setAttribute('aria-label', parts.filter((p) => p.n).map((p) => p.label + ' ' + num(p.n)).join(', ') || 'none');
  if (!total) { bar.append(el('i', 'seg empty')); return bar; }
  parts.forEach((p) => {
    if (!p.n) return;
    const s = el('i', 'seg ' + prefix + p.key);
    s.style.flexGrow = String(p.n);
    s.dataset.tip = p.label + ': ' + num(p.n) + ' (' + pct(p.n, total) + '%)';
    bar.append(s);
  });
  return bar;
}

/** The legend under a stack: a marker, the label, the count. */
export function legend(parts, prefix) {
  return el('ul', 'legend', parts.map((p) => el('li', p.n ? '' : 'zero', [el('i', 'key ' + prefix + p.key), el('span', '', p.label), el('b', '', num(p.n))])));
}

export function levelParts(v) {
  const parts = LEVELS.map((k) => ({ key: k, n: v[k] || 0, label: LEVEL_WORDS[k] }));
  // A subject only draft packs judged has a reading but no level yet.
  return v.draft ? parts.concat({ key: 'draft', n: v.draft, label: 'Draft only' }) : parts;
}

/** A one-value bar against a maximum, for ranked lists. */
export function meter(n, max, cls) {
  const m = el('span', 'meter');
  const f = el('i', cls || '');
  f.style.width = (max ? Math.max(2, (n / max) * 100) : 0) + '%';
  m.append(f);
  return m;
}

export function empty(title, text) {
  return el('div', 'empty', [el('b', '', title), text ? el('p', '', text) : null]);
}

/** One floating tooltip for every element with data-tip. */
export function tooltips() {
  const tip = el('div', 'tip');
  tip.setAttribute('role', 'tooltip');
  document.body.append(tip);
  document.addEventListener('pointerover', (e) => {
    const t = e.target.closest && e.target.closest('[data-tip]');
    if (!t) { tip.classList.remove('on'); return; }
    tip.textContent = t.dataset.tip;
    const r = t.getBoundingClientRect();
    tip.classList.add('on');
    const w = tip.offsetWidth;
    tip.style.left = Math.min(window.innerWidth - w - 8, Math.max(8, r.left + r.width / 2 - w / 2)) + 'px';
    tip.style.top = Math.max(8, r.top - tip.offsetHeight - 8) + 'px';
  });
  window.addEventListener('scroll', () => tip.classList.remove('on'), true);
}

const SVG = 'http://www.w3.org/2000/svg';
function svgEl(tag, attrs) {
  const e = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, String(v));
  return e;
}

/**
 * chart draws a line over time. points are [{at, value}], oldest first;
 * step holds each value until the next point, for tiers. min and max bound
 * the value axis; labels names a value, such as a tier's rank; unit follows
 * a value. The line runs on to `to` at its last value.
 */
export function chart(points, opts = {}) {
  const w = 560, h = opts.height || 140, pad = { l: 56, r: 12, t: 10, b: 22 };
  const from = new Date(opts.from || (points[0] && points[0].at) || Date.now()).getTime();
  const to = new Date(opts.to || Date.now()).getTime();
  const min = opts.min ?? Math.min(...points.map((p) => p.value), 0);
  const max = opts.max ?? Math.max(...points.map((p) => p.value), 1);
  const x = (t) => pad.l + ((t - from) / Math.max(1, to - from)) * (w - pad.l - pad.r);
  const y = (v) => pad.t + (1 - (v - min) / Math.max(1e-9, max - min)) * (h - pad.t - pad.b);
  const svg = svgEl('svg', { viewBox: `0 0 ${w} ${h}`, class: 'chart', role: 'img' });
  const ticks = opts.labels ? Object.keys(opts.labels).map(Number).sort((a, b) => a - b) : [min, (min + max) / 2, max];
  for (const v of ticks) {
    svg.append(svgEl('line', { x1: pad.l, x2: w - pad.r, y1: y(v), y2: y(v), class: 'grid' }));
    const t = svgEl('text', { x: pad.l - 8, y: y(v) + 3.5, class: 'tick', 'text-anchor': 'end' });
    t.textContent = opts.labels ? opts.labels[String(v)] : Math.round(v) + (opts.unit || '');
    svg.append(t);
  }
  for (const t of [from, to]) {
    const lab = svgEl('text', { x: x(t), y: h - 6, class: 'tick', 'text-anchor': t === from ? 'start' : 'end' });
    lab.textContent = new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
    svg.append(lab);
  }
  if (!points.length) return svg;
  let d = '';
  points.forEach((p, i) => {
    const px = x(Math.max(from, new Date(p.at).getTime())), py = y(p.value);
    if (i === 0) d += `M${px},${py}`;
    else if (opts.step) d += `H${px}V${py}`;
    else d += `L${px},${py}`;
  });
  const last = points[points.length - 1];
  d += opts.step ? `H${x(to)}` : '';
  svg.append(svgEl('path', { d, class: 'line' }));
  svg.append(svgEl('circle', { cx: opts.step ? x(to) : x(new Date(last.at).getTime()), cy: y(last.value), r: 3, class: 'dot' }));
  return svg;
}
