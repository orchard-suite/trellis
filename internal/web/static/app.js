// The board's shell: one fetch of /v1/board for the verdicts and rules and one
// of /v1/findings for the rows, a sidebar, a top bar, the view the URL names,
// and the inspector. State lives in the URL (?view=&pack=&kind=&rule=
// &severity=&pillar=&level=&owner=&state=&overdue=1&q=&open=), so a link opens
// the same view. An older link with only ?pack= or ?rule= opens Findings, and
// one with waived=1 opens the waived findings.
import { el, add, button, icon, num, ago, levelChip, tooltips, learn, pctOf, CLEAR, PACK_WORDS, LEVEL_WORDS } from './ui.js';
import { overview, rulesView, waiversView, ownersView } from './views.js';
import { findingsView, subjectsView } from './lists.js';
import { inspector } from './inspector.js';

const VIEWS = {
  overview: { title: 'Overview', icon: 'overview', draw: overview },
  findings: { title: 'Findings', icon: 'findings', draw: findingsView },
  subjects: { title: 'Subjects', icon: 'subjects', draw: subjectsView },
  owners: { title: 'Owners', icon: 'owners', draw: ownersView },
  rules: { title: 'Rules', icon: 'rules', draw: rulesView },
  waivers: { title: 'Waivers', icon: 'waivers', draw: waiversView },
};
const KEYS = ['view', 'pack', 'kind', 'rule', 'severity', 'pillar', 'level', 'owner', 'state', 'q', 'open'];

const $ = (id) => document.getElementById(id);
let faro = null;
const startedAt = performance.now();
let readyReported = false;

const ctx = {
  board: null,
  data: null,
  rules: new Map(),
  state: read(),
  /** Change the state and draw. A change of view starts at the top. */
  go(patch, opts = {}) {
    const next = { ...ctx.state, ...patch };
    if (patch.view && patch.view !== ctx.state.view) window.scrollTo(0, 0);
    ctx.state = next;
    write(opts.push);
    draw();
  },
  event(name, attrs) { if (faro) faro.api.pushEvent(name, attrs || {}); },
  inspect: null,
};

function read() {
  const p = new URLSearchParams(location.search);
  const s = { overdue: p.get('overdue') === '1' };
  KEYS.forEach((k) => { s[k] = p.get(k) || ''; });
  if (p.get('waived') === '1' && !s.state) s.state = 'waived';
  if (!VIEWS[s.view]) s.view = s.rule || s.pack || s.severity || s.owner ? 'findings' : 'overview';
  return s;
}

function write(push) {
  const p = new URLSearchParams();
  KEYS.forEach((k) => { if (ctx.state[k] && !(k === 'view' && ctx.state[k] === 'overview')) p.set(k, ctx.state[k]); });
  if (ctx.state.overdue) p.set('overdue', '1');
  const s = p.toString();
  history[push ? 'pushState' : 'replaceState'](null, '', s ? '?' + s : location.pathname);
}

function initFaro() {
  if (!window.GrafanaFaroWebSdk) return;
  try {
    const instr = window.GrafanaFaroWebSdk.getWebInstrumentations({ captureConsole: false });
    if (window.GrafanaFaroWebTracing) instr.push(new window.GrafanaFaroWebTracing.TracingInstrumentation({ instrumentationOptions: { propagateTraceHeaderCorsUrls: [] }, ignoreUrls: [/\/api\/telemetry/, /\/static\//] }));
    faro = window.GrafanaFaroWebSdk.initializeFaro({ url: '/api/telemetry', app: { name: 'trellis-web', version: (ctx.board && ctx.board.version) || 'unknown' }, trackResources: false, instrumentations: instr, ignoreUrls: [/\/api\/telemetry/] });
  } catch (e) { faro = null; }
}

async function getJSON(path) {
  const r = await fetch(path, { credentials: 'same-origin' });
  if (!r.ok) throw new Error(path + ' answered ' + r.status);
  return r.json();
}

async function load() {
  try {
    const [board, data] = await Promise.all([getJSON('/v1/board'), getJSON('/v1/findings')]);
    learn(board);
    if (ctx.board && ctx.board.refreshed_at !== board.refreshed_at) ctx.inspect.forget();
    ctx.board = board;
    ctx.data = data;
    ctx.rules = new Map(board.rules.map((r) => [r.id, r]));
    if (!faro) initFaro();
    draw();
    if (!readyReported) {
      readyReported = true;
      if (faro) faro.api.pushMeasurement({ type: 'board_ready', values: { ready_ms: Math.round(performance.now() - startedAt) } });
    }
  } catch (err) {
    $('view').replaceChildren(el('div', 'empty', [el('b', '', 'The board could not load'), el('p', '', err.message)]));
    if (faro) faro.api.pushError(err);
  }
}

function navLink(key) {
  const v = VIEWS[key];
  const a = el('a', '', [icon(v.icon), el('span', 'label', v.title)]);
  a.href = '?view=' + key;
  if (ctx.state.view === key) a.setAttribute('aria-current', 'page');
  const b = ctx.board;
  if (b && key === 'findings' && b.by_state.open) a.append(el('span', 'n', num(b.by_state.open)));
  if (b && key === 'subjects') a.append(el('span', 'n', num(ctx.data.subjects.length)));
  if (b && key === 'owners' && b.owners.length) a.append(el('span', 'n', num(b.owners.length)));
  if (b && key === 'rules') a.append(el('span', 'n', num(b.rules.length)));
  if (b && key === 'waivers' && b.waivers.length) a.append(el('span', 'n', num(b.waivers.length)));
  a.addEventListener('click', (e) => {
    if (e.metaKey || e.ctrlKey) return;
    e.preventDefault();
    closeNav();
    ctx.go({ ...CLEAR, view: key }, { push: true });
    ctx.event('view.open', { view: key });
  });
  return a;
}

function sidebar() {
  const b = ctx.board;
  const side = $('side');
  side.replaceChildren();
  const brand = el('a', 'brand', [el('span', 'logo', icon('check', 14)), el('span', 'word', 'trellis')]);
  brand.href = '/';
  side.append(el('div', 'side-top', [brand, button('btn ghost icon side-close', icon('close'), closeNav, 'Close the menu')]));
  side.append(el('nav', 'nav', Object.keys(VIEWS).map(navLink)));

  if (b && b.packs.length) {
    const packs = b.packs.map((p) => {
      const a = el('a', '', [el('i', 'dot lv-' + p.level), el('span', 'label', PACK_WORDS[p.pack] || p.pack), el('span', 'n', num(p.findings))]);
      a.href = '?view=findings&pack=' + p.pack;
      if (ctx.state.view === 'findings' && ctx.state.pack === p.pack) a.setAttribute('aria-current', 'page');
      a.title = LEVEL_WORDS[p.level] + ', ' + pctOf(p.conformant) + ' conformant';
      a.addEventListener('click', (e) => {
        e.preventDefault();
        closeNav();
        ctx.go({ ...CLEAR, view: 'findings', pack: p.pack }, { push: true });
        ctx.event('pack.open', { pack: p.pack });
      });
      return a;
    });
    side.append(el('div', 'side-sec', [el('span', 'caps', 'Standards'), el('nav', 'nav', packs)]));
  }

  if (b) {
    const runs = b.sources.map((p) => {
      const ok = p.outcome === 'ok';
      const shaky = ok && (p.missed || p.carried);
      const row = el('li', ok ? '' : 'bad', [el('i', 'dot ' + (!ok ? 'lv-red' : shaky ? 'lv-amber' : 'lv-green')), el('span', 'label', p.source), el('span', 'n', ok ? secs(p.duration) : 'failed')]);
      const notes = [];
      if (p.error) notes.push(p.error);
      (p.gaps || []).forEach((g) => notes.push('No ' + g.fact + ' for ' + g.kind + ': ' + g.reason));
      if (p.missed) notes.push(num(p.missed) + ' facts could not be read');
      if (p.carried) notes.push(num(p.carried) + ' facts kept from an earlier pass');
      if (notes.length) row.dataset.tip = notes.join('. ');
      return row;
    });
    side.append(el('div', 'side-sec sources', [el('span', 'caps', 'Sources'), runs.length ? el('ul', 'runs', runs) : el('p', 'faint', 'First pass running')]));
  }

  const foot = el('div', 'side-foot');
  if (b && b.person) foot.append(el('div', 'me', [el('span', 'avatar', initials(b.person)), el('span', 'who', b.person)]));
  if (b) foot.append(el('p', 'release', 'trellis ' + b.version));
  side.append(foot);
}

const secs = (ns) => { const s = ns / 1e9; return s < 1 ? Math.round(s * 1000) + ' ms' : s < 60 ? s.toFixed(1) + ' s' : Math.round(s / 60) + ' min'; };
const initials = (email) => email.split('@')[0].split(/[._-]+/).filter(Boolean).slice(0, 2).map((w) => w[0]).join('').toUpperCase() || '?';

function topbar() {
  const b = ctx.board;
  const bar = $('bar');
  const status = $('status');
  status.replaceChildren();
  if (!b) return;
  const chip = levelChip(b.level, b.level === 'grey' ? 'No data' : LEVEL_WORDS[b.level]);
  if (b.tally && b.tally.conformant !== undefined) chip.dataset.tip = pctOf(b.tally.conformant) + ' of judged subjects conformant';
  status.append(chip);
  const when = el('span', 'when', 'Checked ' + ago(b.refreshed_at));
  if (b.took_ms) when.dataset.tip = 'The last pass took ' + (b.took_ms / 1000).toFixed(1) + ' s';
  status.append(when);
  const q = bar.querySelector('input[name=q]');
  if (document.activeElement !== q) q.value = ctx.state.q;
}

function draw() {
  sidebar();
  topbar();
  const v = VIEWS[ctx.state.view];
  document.title = v.title + ' · trellis';
  const main = $('view');
  if (!ctx.board) return;
  main.replaceChildren();
  add(main, v.draw(ctx));
  ctx.inspect.sync();
}

function openNav() { document.body.classList.add('nav-open'); }
function closeNav() { document.body.classList.remove('nav-open'); }

function wire() {
  $('menu').addEventListener('click', openNav);
  $('scrim').addEventListener('click', closeNav);
  const q = document.querySelector('#bar input[name=q]');
  let timer = 0;
  q.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      const view = ctx.state.view === 'findings' || ctx.state.view === 'subjects' ? ctx.state.view : 'subjects';
      ctx.go({ q: q.value.trim(), view });
    }, 160);
  });
  document.addEventListener('keydown', (e) => {
    const t = e.target;
    const typing = t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
    if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey) { e.preventDefault(); q.focus(); q.select(); }
    if (e.key === 'Escape') { if (document.body.classList.contains('nav-open')) closeNav(); else if (ctx.state.open) ctx.go({ open: '' }); else if (t === q) q.blur(); }
  });
  const refresh = $('refresh');
  refresh.addEventListener('click', async () => {
    refresh.disabled = true;
    refresh.classList.add('busy');
    ctx.event('board.refresh');
    try { await fetch('/v1/refresh', { method: 'POST', credentials: 'same-origin' }); } catch (e) { /* the next load says */ }
    setTimeout(async () => { await load(); refresh.disabled = false; refresh.classList.remove('busy'); }, 8000);
  });
  window.addEventListener('popstate', () => { ctx.state = read(); draw(); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
  setInterval(() => { if (!document.hidden && !ctx.state.open) load(); }, 60000);
}

ctx.inspect = inspector(ctx);
tooltips();
wire();
load();
