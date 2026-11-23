// The working views: Findings, grouped by rule, and Subjects.
import { el, button, icon, num, plural, levelChip, sevChip, mono, empty, pillarWord, due, carried, stateTag, date,
  KINDS, KIND_WORDS, KIND_ONE, PACK_WORDS, SEVERITIES, SEV_WORDS, LEVELS, LEVEL_WORDS, STATES, STATE_WORDS, STATE_ABOUT,
  ownerWord, ownerKey, ownerOf, statusOf } from './ui.js';
import { head, seg, packSeg } from './views.js';

const SEV_RANK = { critical: 0, high: 1, medium: 2, low: 3 };
const LEVEL_RANK = { red: 0, amber: 1, draft: 2, grey: 3, green: 4 };
const PAGE = 100;

/** Which rule groups are open, and how far each is shown, for this visit. */
const opened = new Set();
const shown = new Map();
let subjectsShown = PAGE;

const hit = (q, ...words) => !q || words.some((w) => w && w.toLowerCase().includes(q));

function filterChip(label, clear) {
  return button('fchip', [label, icon('close', 12)], clear, 'Clear ' + label);
}

/** ownerSelect picks an owner, or every owner, among those listed. */
function ownerSelect(ctx, owners, n) {
  const sel = el('select', 'field');
  sel.setAttribute('aria-label', 'Owner');
  sel.append(new Option('Every owner', ''));
  owners.forEach((o) => sel.append(new Option(ownerWord(o) + (n ? ' (' + num(n(o)) + ')' : ''), ownerKey(o))));
  sel.value = ctx.state.owner;
  sel.addEventListener('change', () => { ctx.go({ owner: sel.value }); ctx.event('filter.set', { owner: sel.value ? 'one' : 'all' }); });
  return sel;
}

function switchBox(label, on, flip) {
  const wv = el('label', 'switch', [el('input'), el('span', 'track'), label]);
  const box = wv.querySelector('input');
  box.type = 'checkbox';
  box.checked = on;
  box.addEventListener('change', () => flip(box.checked));
  return wv;
}

export function findingsView(ctx) {
  const s = ctx.state;
  const q = s.q.toLowerCase();
  const state = s.state || 'open';
  const owners = [...new Set(ctx.board.owners.map((o) => o.owner).concat(ctx.data.findings.map((f) => f.owner)))].sort((a, b) => (a === '') - (b === '') || a.localeCompare(b));
  const base = (f) => (!s.kind || f.kind === s.kind) && (!s.rule || f.rule === s.rule) && (!s.pillar || f.pillar === s.pillar) && (!s.owner || f.owner === ownerOf(s.owner)) && (!s.overdue || f.overdue) && hit(q, f.title, f.subject);
  const inState = (f) => state === 'all' || f.state === state;
  const inPack = (f) => !s.pack || f.pack === s.pack;
  const inSev = (f) => !s.severity || f.severity === s.severity;
  const scoped = ctx.data.findings.filter(base);

  const stateSeg = seg(STATES.map((v) => ({ value: v, label: STATE_WORDS[v], n: scoped.filter((f) => inPack(f) && inSev(f) && f.state === v).length, tip: STATE_ABOUT[v] })).concat({ value: 'all', label: 'All', n: scoped.filter((f) => inPack(f) && inSev(f)).length }), state, (v) => { ctx.go({ state: v === 'open' ? '' : v }); ctx.event('filter.set', { state: v }); }, 'State');
  const inS = scoped.filter(inState);
  const sevChoices = [{ value: '', label: 'Any severity', n: inS.filter(inPack).length }].concat(SEVERITIES.map((v) => ({ value: v, label: SEV_WORDS[v], n: inS.filter((f) => inPack(f) && f.severity === v).length, dot: 'sv-' + v })));
  const kindSel = el('select', 'field');
  kindSel.setAttribute('aria-label', 'Kind');
  kindSel.append(new Option('Every kind', ''));
  KINDS.filter((k) => ctx.data.findings.some((f) => f.kind === k)).forEach((k) => kindSel.append(new Option(KIND_WORDS[k], k)));
  kindSel.value = s.kind;
  kindSel.addEventListener('change', () => { ctx.go({ kind: kindSel.value, rule: '' }); ctx.event('filter.set', { kind: kindSel.value || 'all' }); });
  const late = switchBox('Overdue only', s.overdue, (on) => { ctx.go({ overdue: on, state: on ? '' : s.state }); ctx.event('filter.set', { overdue: on ? 'only' : 'any' }); });

  const rows = inS.filter((f) => inPack(f) && inSev(f));
  const overdue = rows.filter((f) => f.overdue).length;
  const sub = plural(rows.length, (state === 'all' ? '' : STATE_WORDS[state].toLowerCase() + ' ') + 'finding', (state === 'all' ? '' : STATE_WORDS[state].toLowerCase() + ' ') + 'findings') + (overdue && !s.overdue ? ', ' + num(overdue) + ' overdue' : '');
  const out = [head('Findings', sub, [packSeg(ctx, (p) => inS.filter((f) => f.pack === p && inSev(f)).length)])];

  const chips = [];
  if (s.rule) chips.push(filterChip('Rule: ' + ((ctx.rules.get(s.rule) || {}).title || s.rule), () => ctx.go({ rule: '' })));
  if (s.pillar) chips.push(filterChip('Pillar: ' + pillarWord(s.pillar), () => ctx.go({ pillar: '' })));
  if (s.q) chips.push(filterChip('Matching "' + s.q + '"', () => ctx.go({ q: '' })));
  out.push(el('div', 'toolbar', [stateSeg, late]));
  out.push(el('div', 'toolbar', [seg(sevChoices, s.severity, (v) => { ctx.go({ severity: v }); ctx.event('filter.set', { severity: v || 'all' }); }, 'Severity'), kindSel, ownerSelect(ctx, owners, (o) => scoped.filter((f) => inState(f) && f.owner === o).length), chips.length ? el('div', 'fchips', chips) : null]));

  const groups = new Map();
  rows.forEach((f) => {
    if (!groups.has(f.rule)) groups.set(f.rule, { rule: ctx.rules.get(f.rule) || { id: f.rule, title: f.rule, severity: f.severity, pack: f.pack, kind: f.kind }, items: [] });
    groups.get(f.rule).items.push(f);
  });
  const list = [...groups.values()].sort((a, b) => SEV_RANK[a.rule.severity] - SEV_RANK[b.rule.severity] || b.items.filter((f) => f.overdue).length - a.items.filter((f) => f.overdue).length || b.items.length - a.items.length);

  if (!list.length) {
    const none = !ctx.data.findings.some((f) => f.state === 'open');
    out.push(empty(none && state === 'open' && !s.owner ? 'No open findings' : 'Nothing matches', none && state === 'open' && !s.owner ? 'Every subject meets every rule that counts.' : 'Clear a filter to see more.'));
    return out;
  }
  const auto = list.length <= 2 || !!s.rule;
  out.push(el('div', 'groups', list.map((g) => group(ctx, g, auto, state))));
  return out;
}

function group(ctx, g, auto, state) {
  const id = g.rule.id;
  const isOpen = auto || opened.has(id);
  const late = g.items.filter((f) => f.overdue).length;
  const headBtn = button('g-head', [
    el('span', 'chev' + (isOpen ? ' on' : ''), icon('chevron', 14)),
    sevChip(g.rule.severity),
    el('div', 'r-name', [el('b', '', g.rule.title), mono(id)]),
    el('span', 'hide-s dim', PACK_WORDS[g.rule.pack] || g.rule.pack),
    el('span', 'count', [el('b', 'mono', num(g.items.length)), late ? el('em', 'late', num(late) + ' overdue') : null]),
  ], () => {
    if (opened.has(id)) opened.delete(id); else { opened.add(id); ctx.event('rule.expand', { rule: id }); }
    ctx.go({});
  });
  headBtn.setAttribute('aria-expanded', String(isOpen));
  const sec = el('section', 'group' + (isOpen ? ' open' : ''), headBtn);
  if (!isOpen) return sec;
  const items = g.items.slice().sort((a, b) => (b.overdue - a.overdue) || ((a.due || '9') < (b.due || '9') ? -1 : 1));
  const limit = shown.get(id) || PAGE;
  const body = el('div', 'g-body', items.slice(0, limit).map((f) => findingRow(ctx, f, state === 'all')));
  if (items.length > limit) body.append(button('more', 'Show ' + Math.min(PAGE * 4, items.length - limit) + ' more of ' + num(items.length - limit), () => { shown.set(id, limit + PAGE * 4); ctx.go({}); }));
  sec.append(body);
  return sec;
}

function findingRow(ctx, f, withState) {
  const key = 'f:' + f.rule + '|' + f.subject;
  let detail = f.detail;
  if (f.state === 'waived' && f.waiver) detail = f.waiver.reason;
  const when = f.state === 'open' ? due(f) : f.state === 'announced' && f.counts_from ? el('span', 'exp', 'Counts ' + date(f.counts_from)) : f.state === 'waived' && f.waiver_ending ? el('span', 'exp soon', 'Waiver ending') : null;
  const row = button('f-row st-' + f.state + (ctx.state.open === key ? ' sel' : ''), [
    el('div', 'f-subj', [el('b', '', f.title), f.title !== f.subject ? mono(f.subject) : null]),
    el('span', 'f-owner hide-s' + (f.owner ? '' : ' dim'), ownerWord(f.owner)),
    el('span', 'f-detail', [withState || f.state !== 'open' ? stateTag(f) : null, carried(f), detail]),
    el('span', 'f-when', when),
    el('span', 'go', icon('chevron', 14)),
  ], () => { ctx.go({ open: key }); ctx.event('finding.open', { pack: f.pack }); });
  return row;
}

export function subjectsView(ctx) {
  const s = ctx.state;
  const q = s.q.toLowerCase();
  const all = ctx.data.subjects.filter((x) => hit(q, x.title, x.key, x.owner) && (!s.owner || x.owner === ownerOf(s.owner)));
  const inKind = all.filter((x) => !s.kind || x.kind === s.kind);
  const open = (x) => x.findings.filter((f) => f.state === 'open');
  const rows = inKind.filter((x) => !s.level || statusOf(x) === s.level)
    .sort((a, b) => LEVEL_RANK[statusOf(a)] - LEVEL_RANK[statusOf(b)] || open(b).length - open(a).length || a.title.localeCompare(b.title));

  const kinds = KINDS.filter((k) => all.some((x) => x.kind === k)).map((k) => ({ value: k, label: KIND_WORDS[k], n: all.filter((x) => x.kind === k).length }));
  const kindSeg = seg([{ value: '', label: 'All', n: all.length }].concat(kinds), s.kind, (v) => { subjectsShown = PAGE; ctx.go({ kind: v }); ctx.event('filter.set', { kind: v || 'all' }); }, 'Kind');
  const levelSeg = seg([{ value: '', label: 'Any status' }].concat(LEVELS.concat(inKind.some((x) => x.draft) ? ['draft'] : []).map((l) => ({ value: l, label: LEVEL_WORDS[l], n: inKind.filter((x) => statusOf(x) === l).length, dot: 'lv-' + l }))), s.level, (v) => { subjectsShown = PAGE; ctx.go({ level: v }); }, 'Status');
  const owners = [...new Set(ctx.data.subjects.map((x) => x.owner))].sort((a, b) => (a === '') - (b === '') || a.localeCompare(b));

  const chips = [];
  if (s.q) chips.push(filterChip('Matching "' + s.q + '"', () => ctx.go({ q: '' })));
  const out = [head('Subjects', 'Everything the board checks, worst first.'), el('div', 'toolbar', [kindSeg]), el('div', 'toolbar', [levelSeg, ownerSelect(ctx, owners), chips.length ? el('div', 'fchips', chips) : null])];
  if (!rows.length) {
    out.push(empty('Nothing matches', 'Clear a filter to see more.'));
    return out;
  }
  const table = el('div', 'table subjects-t', [
    el('div', 'tr th', [el('span', '', 'Status'), el('span', '', 'Subject'), el('span', 'hide-s', 'Owner'), el('span', '', 'Open findings'), el('span', 'hide-s', 'Standards')]),
    ...rows.slice(0, subjectsShown).map((x) => {
      const key = 's:' + x.kind + '|' + x.key;
      const sev = {};
      const o = open(x);
      o.forEach((f) => { sev[f.severity] = (sev[f.severity] || 0) + 1; });
      const late = o.filter((f) => f.overdue).length;
      const packs = [...new Set(x.findings.map((f) => f.pack))];
      const counts = o.length ? SEVERITIES.filter((v) => sev[v]).map((v) => { const c = el('span', 'cnt sv-' + v, [el('i', 'mark'), num(sev[v])]); c.dataset.tip = plural(sev[v], SEV_WORDS[v].toLowerCase() + ' finding', SEV_WORDS[v].toLowerCase() + ' findings'); return c; }) : [el('span', 'dim', x.unchecked && x.unchecked.length && x.level === 'grey' ? 'No data' : 'None')];
      if (late) counts.push(el('span', 'exp late', num(late) + ' overdue'));
      return button('tr' + (s.open === key ? ' sel' : ''), [
        levelChip(statusOf(x)),
        el('div', 'r-name', [el('b', '', x.title), el('span', 'dim', (KIND_ONE[x.kind] || x.kind) + (x.title !== x.key ? ', ' + x.key : ''))]),
        el('span', 'hide-s' + (x.owner ? '' : ' dim'), ownerWord(x.owner)),
        el('span', 'counts', counts),
        el('span', 'hide-s tags', packs.map((p) => el('span', 'tag', PACK_WORDS[p] || p))),
      ], () => { ctx.go({ open: key }); ctx.event('subject.open', { kind: x.kind }); });
    }),
  ]);
  out.push(el('section', 'panel', table));
  if (rows.length > subjectsShown) out.push(button('more', 'Show ' + Math.min(PAGE * 4, rows.length - subjectsShown) + ' more of ' + num(rows.length - subjectsShown), () => { subjectsShown += PAGE * 4; ctx.go({}); }));
  return out;
}
