// The summary views: Overview, Owners, Rules, and Waivers.
import { el, button, icon, num, plural, stack, legend, levelParts, levelChip, sevChip, meter, mono, empty, date, daysLeft, pctOf,
  SEVERITIES, SEV_WORDS, KIND_WORDS, KIND_ONE, PACK_WORDS, PACK_ABOUT, LEVEL_WORDS, ends, pillarWord, ownerWord, ownerKey,
  rollupWords, EVENT_WORDS, EVENT_ABOUT, STATE_WORDS, STATE_ABOUT, CLEAR } from './ui.js';

const SEV_RANK = { critical: 0, high: 1, medium: 2, low: 3 };

export function head(title, sub, tools) {
  return el('header', 'page-head', [el('div', 'words', [el('h1', '', title), sub ? el('p', '', sub) : null]), tools ? el('div', 'tools', tools) : null]);
}

export function panel(title, kids, opts = {}) {
  const h = el('div', 'panel-head', [el('h2', '', title), opts.note ? el('span', 'note', opts.note) : null, opts.action || null]);
  return el('section', 'panel' + (opts.cls ? ' ' + opts.cls : ''), [h, el('div', 'panel-body', kids)]);
}

function link(text, onClick) {
  return button('link', [text, icon('arrow', 14)], onClick);
}

function tile(label, value, foot, cls, tip) {
  const t = el('div', 'tile' + (cls ? ' ' + cls : ''), [el('span', 'caps', label), el('div', 'value', value), foot ? el('div', 'foot', foot) : null]);
  if (tip) t.dataset.tip = tip;
  return t;
}

/** toFindings opens Findings with only the given filters. */
export function toFindings(ctx, patch, ev) {
  ctx.go({ ...CLEAR, view: 'findings', ...patch }, { push: true });
  ctx.event(ev || 'overview.open', patch);
}

/** Rules a person should look at, with why: [{rule, why}]. */
export function ruleHealth(r) {
  const s = r.stat || {};
  const judged = (s.pass || 0) + (s.fail || 0) + (s.nodata || 0) + (s.waived || 0);
  const out = [];
  if (s.unwritten && s.unwritten.length) out.push({ cls: 'bad', words: 'No source writes ' + s.unwritten.join(', '), tip: 'The rule reads facts no configured source writes, so it can only say no data.' });
  if (judged >= 3 && s.nodata * 2 > judged) out.push({ cls: 'warn', words: 'Mostly no data', tip: num(s.nodata) + ' of ' + num(judged) + ' subjects had no data. Its source may be broken.' });
  const counted = (s.fail || 0) + (s.waived || 0);
  if (s.waived >= 3 && s.waived * 2 >= counted) out.push({ cls: 'warn', words: 'Many waivers', tip: num(s.waived) + ' waivers press on it. The rule may be wrong.' });
  return out;
}

export function overview(ctx) {
  const b = ctx.board;
  const t = b.tally;
  const subjects = b.kinds.reduce((a, k) => a + k.subjects, 0);
  const st = b.by_state;
  const go = (patch, ev) => toFindings(ctx, patch, ev);

  const out = [head('Overview', plural(b.packs.length, 'standard', 'standards') + ', ' + plural(b.rules.length, 'rule', 'rules') + ', ' + plural(subjects, 'subject', 'subjects'))];

  if (!b.refreshed_at) {
    out.push(empty('The first pass is running', 'Every source is being read. This page fills in on its own.'));
    return out;
  }

  const judged = t.red + t.amber + t.green;
  out.push(el('div', 'tiles', [
    tile('Verdict', levelChip(b.level, LEVEL_WORDS[b.level]), b.rollup && b.rollup.mode === 'share' ? 'By share of subjects' : 'By the worst subject', 'verdict lv-' + b.level, rollupWords(b.rollup)),
    tile('Conformant', pctOf(t.conformant), num(t.green) + ' of ' + num(judged) + ' judged subjects' + (t.draft ? ', ' + num(t.draft) + ' draft only' : '')),
    tile('Open findings', num(st.open || 0), t.overdue ? plural(t.overdue, 'is overdue', 'are overdue') : t.critical ? plural(t.critical, 'subject has a critical', 'subjects have a critical') : 'None overdue', t.overdue ? 'hot' : ''),
    tile('No data', num(t.unchecked), t.unchecked ? 'Rule answers nobody could check' : 'Every rule could judge', t.unchecked ? 'warn' : '', 'A rule with no data never counts as a pass. Each one holds its subject back.'),
  ]));
  const counts = [
    { state: 'open', n: st.open || 0 },
    { state: 'overdue', n: t.overdue, words: 'Overdue', tip: 'Open past the deadline for their severity.' },
    { state: 'waived', n: st.waived || 0 },
    { state: 'announced', n: st.announced || 0 },
    { state: 'draft', n: st.draft || 0 },
  ];
  out.push(el('div', 'strip', counts.map((c) => {
    const bt = button('strip-item' + (c.n ? '' : ' zero'), [el('b', 'mono', num(c.n)), el('span', '', c.words || STATE_WORDS[c.state])], () => go(c.state === 'overdue' ? { state: 'open', overdue: true } : { state: c.state }, 'state.open'));
    bt.dataset.tip = c.tip || STATE_ABOUT[c.state];
    return bt;
  })));

  const cards = b.packs.map((p) => {
    const top = b.rules.filter((r) => r.pack === p.pack && r.stat && r.stat.phase === 'counted' && r.stat.fail > 0).sort((x, y) => y.stat.fail - x.stat.fail)[0];
    const parts = levelParts(p);
    return button('pack-card' + (p.status === 'draft' ? ' draft' : ''), [
      el('div', 'pc-head', [el('div', 'pc-name', [el('b', '', PACK_WORDS[p.pack] || p.pack), el('span', '', (p.status === 'draft' ? 'Draft, ' : '') + (PACK_ABOUT[p.pack] || '') + ', v' + p.version)]), levelChip(p.level)]),
      el('div', 'pc-num', [el('b', '', num(p.findings)), el('span', '', p.findings === 1 ? 'finding' : 'findings'), el('em', '', [pctOf(p.conformant) + ' conformant', p.overdue ? ', ' + num(p.overdue) + ' overdue' : '', p.waived ? ', ' + num(p.waived) + ' waived' : ''])]),
      stack(parts, 'lv-'),
      legend(parts, 'lv-'),
      el('div', 'pc-top', top ? [el('span', 'caps', 'Most hit'), el('span', 't', top.title), el('b', '', num(top.stat.fail))] : [el('span', 'caps', 'Most hit'), el('span', 't', 'Every rule passes')]),
    ], () => go({ pack: p.pack }, 'pack.open'));
  });
  out.push(el('div', 'pack-grid', cards));

  out.push(el('div', 'grid-2', [
    panel('Since the last pass', events(ctx, 8), { note: 'What changed', action: null }),
    panel('Owners', ownerList(ctx, 6), { note: 'Who answers for what is open', action: b.owners.length ? link('All', () => ctx.go({ ...CLEAR, view: 'owners' }, { push: true })) : null }),
  ]));

  const pillars = b.pillars.filter((p) => p.subjects > 0);
  const pillarRows = el('ul', 'kind-list', pillars.map((p) => {
    const li = el('li', '', [el('span', 'k', pillarWord(p.pillar)), stack(levelParts(p), 'lv-'), el('span', 'mono n', num(p.findings))]);
    li.dataset.tip = plural(p.rules.length, 'rule', 'rules') + ' serve it; ' + plural(p.subjects, 'subject', 'subjects') + ' judged; ' + pctOf(p.conformant) + ' conformant';
    li.addEventListener('click', () => go({ pillar: p.pillar }, 'pillar.open'));
    return li;
  }));

  const sevParts = SEVERITIES.map((s) => ({ key: s, n: b.by_severity[s] || 0, label: SEV_WORDS[s] }));
  const sevList = el('ul', 'sev-list', sevParts.map((p) => {
    const li = el('li', '', [sevChip(p.key), meter(p.n, Math.max(...sevParts.map((x) => x.n)), 'sv-' + p.key), el('b', 'mono', num(p.n))]);
    li.addEventListener('click', () => go({ severity: p.key }, 'severity.open'));
    return li;
  }));

  const kinds = b.kinds.filter((k) => k.subjects > 0);
  const kindRows = el('ul', 'kind-list', kinds.map((k) => {
    const li = el('li', '', [el('span', 'k', KIND_WORDS[k.kind]), stack(levelParts(k), 'lv-'), el('span', 'mono n', num(k.subjects))]);
    li.addEventListener('click', () => { ctx.go({ ...CLEAR, view: 'subjects', kind: k.kind }, { push: true }); ctx.event('kind.open', { kind: k.kind }); });
    return li;
  }));

  out.push(el('div', 'grid-2', [
    panel('Findings by severity', [stack(sevParts, 'sv-', { tall: true }), sevList], { note: 'Open ones; critical turns a verdict red' }),
    panel('Subjects by kind', [kindRows, legend(levelParts({ draft: kinds.reduce((a, k) => a + (k.draft || 0), 0) }).map((p) => ({ ...p, n: kinds.reduce((a, k) => a + (k[p.key] || 0), 0) })), 'lv-')], { note: 'By their worst finding' }),
  ]));
  out.push(panel('Pillars, from live state', pillars.length ? pillarRows : empty('No rule served a pillar'), { note: 'Each pillar\'s subjects by their worst finding, and its open findings' }));

  const top = b.rules.filter((r) => r.stat && r.stat.phase === 'counted' && r.stat.fail > 0).sort((x, y) => SEV_RANK[x.severity] - SEV_RANK[y.severity] || y.stat.fail - x.stat.fail).slice(0, 8);
  const max = Math.max(1, ...top.map((r) => r.stat.fail));
  const topRows = el('ul', 'rank', top.map((r) => {
    const li = el('li', '', [sevChip(r.severity), el('div', 'r-name', [el('b', '', r.title), mono(r.id)]), meter(r.stat.fail, max, 'sv-' + r.severity), el('b', 'mono n', num(r.stat.fail))]);
    li.addEventListener('click', () => go({ rule: r.id }, 'rule.open'));
    return li;
  }));
  out.push(el('div', 'grid-3', [
    panel('Rules to work on', top.length ? topRows : empty('Every rule passes'), { cls: 'span-2', action: link('All rules', () => ctx.go({ ...CLEAR, view: 'rules' }, { push: true })) }),
    panel('Waivers', waiverList(ctx, 5), { note: 'Ending first', action: b.waivers.length ? link('All', () => ctx.go({ ...CLEAR, view: 'waivers' }, { push: true })) : null }),
  ]));

  out.push(panel('Sources', sourceTable(b), { note: 'Last pass; a fact a source could not read is carried while young enough, and a place it could not read is named' }));
  return out;
}

function sourceTable(b) {
  if (!b.sources.length) return empty('No source has run');
  return el('div', 'table sources-t', [
    el('div', 'tr th', [el('span', '', 'Source'), el('span', 'hide-s', 'Type'), el('span', '', 'Outcome'), el('span', 'r', 'Missed'), el('span', 'r', 'Carried'), el('span', 'r hide-s', 'Took')]),
    ...b.sources.map((s) => {
      const ok = s.outcome === 'ok';
      const outcome = el('span', '', [levelChip(ok ? (s.missed || s.carried ? 'amber' : 'green') : 'red', ok ? (s.missed || s.carried ? 'Partly read' : 'Read') : 'Failed')]);
      return el('div', 'tr', [
        el('div', 'r-name', [el('b', '', s.source), s.error ? el('span', 'dim err', s.error) : null, (s.gaps || []).map((g) => el('span', 'dim err', 'No ' + g.fact + ' for ' + g.kind + ': ' + g.reason))]),
        el('span', 'hide-s dim', s.type || ''),
        outcome,
        el('span', 'r mono' + (s.missed ? '' : ' dim'), num(s.missed)),
        el('span', 'r mono' + (s.carried ? '' : ' dim'), num(s.carried)),
        el('span', 'r mono dim hide-s', secs(s.duration)),
      ]);
    }),
  ]);
}

const secs = (ns) => { const s = ns / 1e9; return s < 1 ? Math.round(s * 1000) + ' ms' : s < 60 ? s.toFixed(1) + ' s' : Math.round(s / 60) + ' min'; };

/** The changes since the last pass, newest kinds first. */
function events(ctx, limit) {
  const evs = ctx.board.events || [];
  if (!evs.length) return empty('Nothing changed', 'Since the pass before, no finding opened, closed, or moved.');
  const order = ['overdue', 'opened', 'unknown', 'waiver_ended', 'waiver_ending', 'waived', 'resolved'];
  const byType = new Map();
  evs.forEach((e) => { if (!byType.has(e.type)) byType.set(e.type, []); byType.get(e.type).push(e); });
  const counts = el('div', 'ev-counts', order.filter((k) => byType.has(k)).map((k) => {
    const c = el('span', 'tag ev-' + k, [EVENT_WORDS[k], ' ', el('b', 'mono', num(byType.get(k).length))]);
    c.dataset.tip = EVENT_ABOUT[k];
    return c;
  }));
  const list = evs.slice().sort((a, b) => order.indexOf(a.type) - order.indexOf(b.type)).slice(0, limit);
  const rows = el('ul', 'wlist', list.map((e) => {
    const f = e.finding, w = e.waiver;
    const rule = f ? ctx.rules.get(f.rule) || { title: f.rule } : ctx.rules.get(w.rule) || { title: w.rule };
    const li = el('li', '', [el('span', 'tag ev-' + e.type, EVENT_WORDS[e.type] || e.type), el('div', 'w-name', [el('b', '', f ? f.title : w.subject), el('span', '', rule.title + (e.owner ? ', ' + e.owner : ''))])]);
    li.dataset.tip = EVENT_ABOUT[e.type] || '';
    li.addEventListener('click', () => (f ? ctx.go({ open: 'f:' + f.rule + '|' + f.subject }) : ctx.go({ ...CLEAR, view: 'waivers' }, { push: true })));
    return li;
  }));
  return [counts, rows, evs.length > limit ? el('p', 'faint', plural(evs.length - limit, 'more change', 'more changes')) : null];
}

/** Owners ranked by overdue, then open findings. */
function ranked(b) {
  return b.owners.slice().sort((x, y) => (y.overdue - x.overdue) || (y.findings - x.findings) || ownerWord(x.owner).localeCompare(ownerWord(y.owner)));
}

function ownerList(ctx, limit) {
  const os = ranked(ctx.board).filter((o) => o.findings || o.overdue);
  if (!os.length) return empty(ctx.board.owners.length ? 'Nothing open for anyone' : 'No owners', ctx.board.owners.length ? '' : 'Name owners in the board\'s settings to see who answers for what.');
  const max = Math.max(1, ...os.map((o) => o.findings));
  return el('ul', 'rank owners-r', os.slice(0, limit).map((o) => {
    const li = el('li', '', [levelChip(o.level), el('div', 'r-name', [el('b', o.owner ? '' : 'dim', ownerWord(o.owner)), el('span', 'dim', plural(o.subjects, 'subject', 'subjects') + (o.overdue ? ', ' + num(o.overdue) + ' overdue' : ''))]), meter(o.findings, max), el('b', 'mono n', num(o.findings))]);
    li.addEventListener('click', () => toFindings(ctx, { owner: ownerKey(o.owner) }, 'owner.open'));
    return li;
  }));
}

export function ownersView(ctx) {
  const b = ctx.board;
  const out = [head('Owners', 'Who answers for each subject, from the board\'s owner settings, and what is open on what they own.')];
  if (!b.owners.length) {
    out.push(empty('No owners', 'Every subject is nobody\'s until the board\'s settings say who owns each kind.'));
    return out;
  }
  const os = ranked(b);
  const max = Math.max(1, ...os.map((o) => o.findings));
  const table = el('div', 'table owners-t', [
    el('div', 'tr th', [el('span', '', 'Level'), el('span', '', 'Owner'), el('span', '', 'Open'), el('span', 'r', 'Overdue'), el('span', 'r hide-s', 'Waived'), el('span', 'r hide-s', 'No data'), el('span', 'r hide-s', 'Conformant')]),
    ...os.map((o) => {
      const open = (patch) => (e) => { e.stopPropagation(); toFindings(ctx, { owner: ownerKey(o.owner), ...patch }, 'owner.open'); };
      const row = button('tr', [
        levelChip(o.level),
        el('div', 'r-name', [el('b', o.owner ? '' : 'dim', ownerWord(o.owner)), el('span', 'dim', plural(o.subjects, 'subject', 'subjects'))]),
        el('span', 'hits', [meter(o.findings, max), el('b', 'mono', num(o.findings))]),
        o.overdue ? button('r cnt-link late', num(o.overdue), open({ state: 'open', overdue: true }), 'Overdue findings of ' + ownerWord(o.owner)) : el('span', 'r mono dim', '0'),
        o.waived ? button('r cnt-link hide-s', num(o.waived), open({ state: 'waived' }), 'Waived findings of ' + ownerWord(o.owner)) : el('span', 'r mono dim hide-s', '0'),
        el('span', 'r mono hide-s' + (o.unchecked ? '' : ' dim'), num(o.unchecked)),
        el('span', 'r mono hide-s', pctOf(o.conformant)),
      ], open({}));
      return row;
    }),
  ]);
  out.push(el('section', 'panel', table));
  return out;
}

function waiverList(ctx, limit) {
  const order = { ending: 0, active: 1, ended: 2, rejected: 3 };
  const ws = ctx.board.waivers.slice().sort((a, b) => order[a.status] - order[b.status] || new Date(ends(a)) - new Date(ends(b)));
  if (!ws.length) return empty('No waivers', 'Every finding counts.');
  return el('ul', 'wlist', ws.slice(0, limit).map((w) => {
    const r = ctx.rules.get(w.rule) || {};
    const li = el('li', '', [el('div', 'w-name', [el('b', '', w.subject), el('span', '', r.title || w.rule)]), waiverStatus(w)]);
    li.addEventListener('click', () => ctx.go({ ...CLEAR, view: 'waivers' }, { push: true }));
    return li;
  }));
}

export function expiry(iso) {
  const d = daysLeft(iso);
  const chip = el('span', 'exp' + (d <= 14 ? ' soon' : ''), d <= 0 ? 'Ended' : d === 1 ? '1 day left' : d + ' days left');
  chip.dataset.tip = 'Ends ' + date(iso);
  return chip;
}

const WAIVER_WORDS = { active: 'Active', ending: 'Ending soon', ended: 'Ended', rejected: 'Does not count' };

/** A waiver's status as a chip, with its end. */
export function waiverStatus(w) {
  if (w.status === 'rejected') {
    const c = el('span', 'exp late', WAIVER_WORDS.rejected);
    c.dataset.tip = w.problem || '';
    return c;
  }
  if (w.status === 'ended') {
    const c = el('span', 'exp', 'Ended ' + date(ends(w)));
    c.dataset.tip = 'What it covered counts again.';
    return c;
  }
  return expiry(ends(w));
}

/** A row of choices: [{value, label, n}]; the one equal to `on` is pressed. */
export function seg(choices, on, pick, label) {
  const g = el('div', 'seg');
  g.setAttribute('role', 'group');
  g.setAttribute('aria-label', label);
  choices.forEach((c) => {
    const b = button(c.value === on ? 'on' : '', [c.dot ? el('i', 'dot ' + c.dot) : null, c.label, c.n !== undefined ? el('span', 'c', num(c.n)) : null], () => pick(c.value));
    b.setAttribute('aria-pressed', String(c.value === on));
    if (c.tip) b.dataset.tip = c.tip;
    g.append(b);
  });
  return g;
}

export function packSeg(ctx, counts) {
  const choices = [{ value: '', label: 'All' }].concat(ctx.board.packs.map((p) => ({ value: p.pack, label: PACK_WORDS[p.pack] || p.pack, n: counts ? counts(p.pack) : undefined })));
  return seg(choices, ctx.state.pack, (v) => { ctx.go({ pack: v, rule: '' }); ctx.event('filter.set', { pack: v || 'all' }); }, 'Standard');
}

const PHASE_WORDS = { counted: 'Counts', announced: 'Coming', draft: 'Draft' };

export function rulesView(ctx) {
  const b = ctx.board;
  const out = [head('Rules', 'What each standard checks, and how each rule answered across the estate in the last pass.', [packSeg(ctx)])];
  const packs = b.packs.map((p) => p.pack).filter((p) => !ctx.state.pack || p === ctx.state.pack);
  const flagged = b.rules.filter((r) => ruleHealth(r).length && (!ctx.state.pack || r.pack === ctx.state.pack));
  if (flagged.length) {
    out.push(panel('Rules to look at', el('ul', 'wlist', flagged.map((r) => {
      const li = el('li', '', [el('div', 'w-name', [el('b', '', r.title), mono(r.id)]), el('div', 'flags', ruleHealth(r).map((h) => { const t = el('span', 'tag ' + h.cls, h.words); t.dataset.tip = h.tip; return t; }))]);
      li.addEventListener('click', () => toFindings(ctx, { rule: r.id, state: 'all' }, 'rule.open'));
      return li;
    })), { note: 'A rule mostly at no data reads a broken source; one with many waivers may be wrong' }));
  }
  packs.forEach((pack) => {
    const rules = b.rules.filter((r) => r.pack === pack).sort((x, y) => (y.stat.fail > 0) - (x.stat.fail > 0) || SEV_RANK[x.severity] - SEV_RANK[y.severity] || y.stat.fail - x.stat.fail);
    const failing = rules.filter((r) => r.stat.phase === 'counted' && r.stat.fail > 0).length;
    const table = el('div', 'table rules-t', [
      el('div', 'tr th', [el('span', '', 'Severity'), el('span', '', 'Rule'), el('span', 'hide-s', 'Checks'), el('span', '', 'Answers'), el('span', 'r', 'Fail'), el('span', 'r hide-s', 'No data'), el('span', 'r hide-s', 'Waived')]),
      ...rules.map((r) => {
        const s = r.stat;
        const parts = [
          { key: 'fail', n: s.fail, label: 'Fail' }, { key: 'waived', n: s.waived, label: 'Waived' },
          { key: 'nodata', n: s.nodata, label: 'No data' }, { key: 'pass', n: s.pass, label: 'Pass' }, { key: 'na', n: s.na, label: 'Does not apply' },
        ];
        const bits = [];
        if (s.phase !== 'counted') bits.push(el('span', 'tag st-' + (s.phase === 'announced' ? 'announced' : 'draft'), PHASE_WORDS[s.phase] + (s.phase === 'announced' && r.from ? ' from ' + date(r.from) : '')));
        if (r.scope) { const t = el('span', 'tag', 'Scope: ' + r.scope); t.dataset.tip = 'It judges only the subjects inside the pack\'s ' + r.scope + ' scope.'; bits.push(t); }
        if (s.carried) { const t = el('span', 'tag carried', num(s.carried) + ' on old facts'); t.dataset.tip = 'Answers judged on facts kept from an earlier pass.'; bits.push(t); }
        ruleHealth(r).forEach((h) => { const t = el('span', 'tag ' + h.cls, h.words); t.dataset.tip = h.tip; bits.push(t); });
        const row = button('tr' + (s.fail || s.waived || s.nodata ? '' : ' quiet'), [
          sevChip(r.severity),
          el('div', 'r-name', [el('b', '', r.title), el('div', 'line', [mono(r.id)].concat(bits))]),
          el('span', 'hide-s dim', KIND_ONE[r.kind] || r.kind),
          el('div', 'answers', stack(parts, 'an-')),
          el('span', 'r mono' + (s.fail ? '' : ' dim'), num(s.fail)),
          el('span', 'r mono hide-s' + (s.nodata ? '' : ' dim'), num(s.nodata)),
          el('span', 'r mono hide-s' + (s.waived ? '' : ' dim'), num(s.waived)),
        ], () => { toFindings(ctx, { rule: r.id, state: s.phase === 'counted' ? '' : s.phase }, 'rule.open'); });
        return row;
      }),
    ]);
    out.push(panel(PACK_WORDS[pack] || pack, table, { note: failing ? failing + ' of ' + rules.length + ' rules fail somewhere' : 'No counted rule fails' }));
  });
  out.push(el('ul', 'legend', [['fail', 'Fail'], ['waived', 'Waived'], ['nodata', 'No data'], ['pass', 'Pass'], ['na', 'Does not apply']].map(([k, l]) => el('li', '', [el('i', 'key an-' + k), el('span', '', l)]))));
  return out;
}

export function waiversView(ctx) {
  const b = ctx.board;
  const order = { ending: 0, active: 1, rejected: 2, ended: 3 };
  const ws = b.waivers.slice().sort((x, y) => order[x.status] - order[y.status] || new Date(ends(x)) - new Date(ends(y)));
  const out = [head('Waivers', 'Exceptions with a reason, an owner, and an end. A waived finding counts again when its waiver ends.')];
  if (!b.waivers_known) out.push(el('div', 'callout warn', [el('div', 'c-head', [icon('waivers', 15), el('b', '', 'No waiver store could be read')]), el('p', '', 'Every finding stands until one can be read.')]));
  if (!ws.length) {
    out.push(empty('No waivers', 'A waiver names the rule, the subject, a reason, an owner, and an end: an expiry for an accepted risk, or a review date and a control for a mitigated one.'));
    return out;
  }
  const counts = {};
  ws.forEach((w) => { counts[w.status] = (counts[w.status] || 0) + 1; });
  out.push(el('div', 'strip', ['ending', 'active', 'rejected', 'ended'].map((k) => el('div', 'strip-item static' + (counts[k] ? '' : ' zero'), [el('b', 'mono', num(counts[k] || 0)), el('span', '', WAIVER_WORDS[k])]))));
  const table = el('div', 'table waivers-t', [
    el('div', 'tr th', [el('span', '', 'Subject'), el('span', '', 'Rule'), el('span', 'hide-s', 'Why'), el('span', 'hide-s', 'Covers'), el('span', 'r', 'Ends')]),
    ...ws.map((w) => {
      const r = ctx.rules.get(w.rule) || {};
      const cat = w.category || (w.control ? 'mitigated' : 'accepted');
      const why = el('div', 'r-name', [
        el('span', 'clamp', w.reason),
        el('div', 'line', [
          (() => { const t = el('span', 'tag cat-' + cat, cat === 'mitigated' ? 'Mitigated' : 'Accepted risk'); t.dataset.tip = cat === 'mitigated' ? 'Another control meets the rule\'s intent' + (w.control ? ': ' + w.control : '') + '.' : 'The owner takes on the risk until it ends.'; return t; })(),
          el('span', 'dim', w.owner || 'No owner'),
          w.from ? (() => { const f = el('span', 'dim', 'from ' + w.from); if (w.id) f.dataset.tip = 'Granted as ' + w.id; return f; })() : null,
          w.match ? (() => { const m = mono('match: ' + w.match); m.dataset.tip = 'Covers only findings whose detail holds this text.'; return m; })() : null,
        ]),
        w.status === 'rejected' ? el('span', 'err', 'Does not count: ' + w.problem) : null,
      ]);
      const covers = el('span', 'hide-s mono' + (w.covers ? '' : ' dim'), w.covers ? plural(w.covers, 'finding', 'findings') : 'none now');
      const row = button('tr' + (w.status === 'ended' || w.status === 'rejected' ? ' quiet' : ''), [
        el('div', 'r-name', [el('b', '', w.subject)]),
        el('div', 'r-name', [el('span', '', r.title || w.rule), mono(w.rule)]),
        why,
        covers,
        el('span', 'r', waiverStatus(w)),
      ], () => toFindings(ctx, { rule: w.rule, state: 'all', q: w.subject }, 'waiver.open'));
      return row;
    }),
  ]);
  out.push(el('section', 'panel', table));
  return out;
}
