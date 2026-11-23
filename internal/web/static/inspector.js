// The inspector: a drawer on the right with one finding or one subject in
// full. `open` in the state names it: f:<rule>|<subject> or s:<kind>|<key>.
// A subject's facts, with the source that read each and when, come from
// /v1/subject when the drawer opens.
import { el, button, icon, num, plural, levelChip, sevChip, mono, date, dateTime, ago, ends, set, pillarWord, chart, due, carried, stateTag,
  ownerWord, ownerKey, KIND_ONE, PACK_WORDS, STATE_WORDS, CLEAR } from './ui.js';
import { expiry } from './views.js';

export function inspector(ctx) {
  const drawer = document.getElementById('drawer');
  const scrim = document.getElementById('drawer-scrim');
  scrim.addEventListener('click', () => ctx.go({ open: '' }));
  let last = '';

  function find(rule, subject) {
    return ctx.data.findings.find((f) => f.rule === rule && f.subject === subject) || null;
  }

  function subjectOf(kind, key) {
    return ctx.data.subjects.find((x) => x.kind === kind && x.key === key);
  }

  function frame(eyebrow, title, sub, body) {
    const close = button('btn ghost icon', icon('close'), () => ctx.go({ open: '' }), 'Close');
    return [el('header', 'd-head', [el('div', 'd-title', [el('span', 'caps', eyebrow), el('h2', '', title), sub || null]), close]), el('div', 'd-body', body)];
  }

  function section(title, kids, cls) {
    return el('section', 'd-sec' + (cls ? ' ' + cls : ''), [el('h3', 'caps', title), kids]);
  }

  function copy(text) {
    const b = button('btn ghost icon sm', icon('copy', 13), () => {
      if (!navigator.clipboard) return;
      navigator.clipboard.writeText(text).then(() => { b.replaceChildren(icon('check', 13)); setTimeout(() => b.replaceChildren(icon('copy', 13)), 1400); });
    }, 'Copy');
    return b;
  }

  function ownerLink(owner) {
    return button('link', [ownerWord(owner), icon('arrow', 13)], () => ctx.go({ ...CLEAR, view: 'findings', owner: ownerKey(owner) }, { push: true }));
  }

  function finding(f) {
    const rule = ctx.rules.get(f.rule) || { title: f.rule };
    const subject = subjectOf(f.kind, f.subject);
    const subjLink = button('subj', [el('span', 'caps', KIND_ONE[f.kind] || f.kind), el('b', '', f.title), f.title !== f.subject ? mono(f.subject) : null, icon('chevron', 14)], () => ctx.go({ open: 's:' + f.kind + '|' + f.subject }));
    const when = [];
    if (f.state === 'open') when.push(f.due ? [el('dt', '', 'Due'), el('dd', '', [date(f.due), ' ', due(f)])] : [el('dt', '', 'Due'), el('dd', 'dim', 'No deadline for its severity')]);
    if (set(f.first_seen)) when.push([el('dt', '', 'First seen'), el('dd', '', date(f.first_seen))]);
    if (set(f.as_of)) when.push([el('dt', '', 'Facts from'), el('dd', '', [dateTime(f.as_of), ' ', carried(f)])]);
    const body = [
      subjLink,
      el('dl', 'facts', [
        el('dt', '', 'State'), el('dd', '', stateTag(f)),
        el('dt', '', 'Severity'), el('dd', '', sevChip(f.severity)),
        el('dt', '', 'Owner'), el('dd', '', ownerLink(f.owner)),
        when,
        el('dt', '', 'Pack'), el('dd', '', PACK_WORDS[f.pack] || f.pack),
        el('dt', '', 'Rule'), el('dd', 'rule-id', [mono(f.rule), copy(f.rule)]),
        el('dt', '', 'Enforces'), el('dd', 'rule-id', [mono(f.standard), copy(f.standard)]),
        el('dt', '', 'Pillar'), el('dd', '', pillarWord(f.pillar)),
        f.tier ? [el('dt', '', 'Tier'), el('dd', '', f.tier)] : null,
        f.controls && f.controls.length ? [el('dt', '', 'Controls'), el('dd', 'tags', f.controls.map((c) => mono(c)))] : null,
      ]),
    ];
    if (f.carried) body.push(el('div', 'callout warn', [el('div', 'c-head', [icon('clock', 15), el('b', '', 'Judged on old facts')]), el('p', '', 'A source could not read this subject in the last pass, so the board kept its reading from ' + (set(f.as_of) ? dateTime(f.as_of) : 'an earlier pass') + '. The finding stands until the source reads it again or the reading grows too old.')]));
    if (f.state === 'announced') body.push(el('div', 'callout', [el('div', 'c-head', [icon('clock', 15), el('b', '', 'Coming')]), el('p', '', 'This rule starts to count' + (f.counts_from ? ' on ' + date(f.counts_from) : ' later') + '. Until then it sets no level and fails no gate.')]));
    if (f.state === 'draft') body.push(el('div', 'callout', [el('div', 'c-head', [icon('eye', 15), el('b', '', 'Draft')]), el('p', '', 'This rule is being tried on the estate. It never counts.')]));
    if (f.state === 'waived' && f.waiver) {
      const w = f.waiver;
      const cat = w.category || (w.control ? 'mitigated' : 'accepted');
      const end = cat === 'mitigated' ? 'Review by ' + date(ends(w)) + (w.control ? ', held by ' + w.control : '') : 'Ends ' + date(ends(w));
      body.push(el('div', 'callout waiver', [el('div', 'c-head', [icon('waivers', 15), el('b', '', cat === 'mitigated' ? 'Waived: mitigated' : 'Waived: risk accepted'), expiry(ends(w))]), el('p', '', w.reason), el('span', 'faint', end + ', owned by ' + (w.owner || 'nobody') + (w.from ? ', from ' + w.from : '') + (f.waiver_ending ? '. It ends soon.' : ''))]));
    }
    if (f.detail) body.push(section('What was seen', el('p', 'detail', f.detail)));
    if (f.fix) body.push(el('div', 'callout fix', [el('div', 'c-head', [icon('wrench', 15), el('b', '', 'How to fix')]), el('p', '', f.fix)]));
    if (f.evidence && f.evidence.length) {
      body.push(section('Evidence', el('div', 'evidence', f.evidence.map((ev) => el('div', 'ev', [
        el('div', 'ev-head', [el('span', 'src', [icon('source', 13), ev.source]), el('span', 'got', ev.got), copy(ev.query)]),
        el('pre', '', el('code', '', ev.query)),
      ])))));
    }
    if (subject) {
      const others = subject.findings.filter((x) => x.rule !== f.rule);
      if (others.length) body.push(section('Also on this ' + (KIND_ONE[f.kind] || 'subject').toLowerCase(), findingList(others)));
    }
    const all = button('btn sm', ['Every subject this rule hits', icon('arrow', 14)], () => ctx.go({ ...CLEAR, view: 'findings', rule: f.rule, state: f.state === 'open' ? '' : f.state }, { push: true }));
    body.push(el('div', 'd-acts', all));
    return frame((PACK_WORDS[f.pack] || f.pack) + ' finding', rule.title, null, body);
  }

  function findingList(list) {
    const order = { critical: 0, high: 1, medium: 2, low: 3 };
    const st = { open: 0, announced: 1, waived: 2, draft: 3 };
    return el('ul', 'd-list', list.slice().sort((a, b) => st[a.state] - st[b.state] || order[a.severity] - order[b.severity]).map((x) => {
      const r = ctx.rules.get(x.rule) || { title: x.rule };
      const words = x.state === 'waived' && x.waiver ? 'Waived until ' + date(ends(x.waiver)) : x.state === 'announced' && x.counts_from ? 'Counts from ' + date(x.counts_from) + ': ' + x.detail : x.detail;
      const li = el('li');
      const tags = [x.state !== 'open' ? stateTag(x) : due(x), carried(x)].filter(Boolean);
      li.append(button('', [sevChip(x.severity), el('div', 'r-name', [el('b', '', r.title), el('span', 'dim', words), tags.length ? el('span', 'd-tags', tags) : null]), icon('chevron', 14)], () => ctx.go({ open: 'f:' + x.rule + '|' + x.subject })));
      return li;
    }));
  }

  /** history fills a section with a subject's tier and finding history. */
  const pastCache = new Map();
  function history(x, holder) {
    if (!ctx.board || !ctx.board.history) return;
    const key = x.kind + '|' + x.key;
    const draw = (h) => {
      if (!h) { holder.replaceChildren(el('p', 'faint', 'History could not be read.')); return; }
      const out = [];
      const packs = [...new Set(h.tiers.map((t) => t.pack))];
      for (const p of packs) {
        const spans = h.tiers.filter((t) => t.pack === p);
        const names = (ctx.board.pack_tiers && ctx.board.pack_tiers[p]) || [];
        const labels = { 0: 'None' };
        names.forEach((n, i) => { labels[i + 1] = n.charAt(0).toUpperCase() + n.slice(1); });
        const points = [];
        spans.forEach((s, i) => {
          if (i > 0 && spans[i - 1].to && spans[i - 1].to < s.from) points.push({ at: spans[i - 1].to, value: 0 });
          points.push({ at: s.from, value: s.rank });
        });
        out.push(el('div', '', [el('span', 'caps', (PACK_WORDS[p] || p) + ' tier'), chart(points, { step: true, min: 0, max: Math.max(1, names.length), labels, from: new Date(Date.now() - 90 * 864e5).toISOString() })]));
      }
      if (h.findings.length) {
        out.push(el('span', 'caps', 'Findings over time'));
        out.push(el('ul', 'spans', h.findings.slice().reverse().map((f) => {
          const r = ctx.rules.get(f.rule) || { title: f.rule };
          const cause = f.cause === 'rule' ? el('span', 'tag', 'Rule changed') : null;
          if (cause) cause.dataset.tip = 'It opened in the pass its pack\'s version changed: the rule moved, not the subject.';
          return el('li', '', [el('span', '', [r.title, ' ', cause]), el('span', 'when', date(f.from) + ' to ' + (f.to ? date(f.to) : 'now'))]);
        })));
      }
      if (!out.length) out.push(el('p', 'faint', 'Nothing recorded yet. History starts with the first pass that keeps it.'));
      holder.replaceChildren(...out);
    };
    if (pastCache.has(key)) { draw(pastCache.get(key)); return; }
    holder.replaceChildren(el('p', 'faint', 'Reading history...'));
    fetch('/v1/history?days=90&kind=' + encodeURIComponent(x.kind) + '&subject=' + encodeURIComponent(x.key), { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => { const h = j && j.history; pastCache.set(key, h); if (holder.isConnected) draw(h); })
      .catch(() => draw(null));
  }

  /** facts fills a section with the subject's facts from /v1/subject. */
  const factCache = new Map();
  function facts(x, holder) {
    const key = x.kind + '|' + x.key;
    const draw = (full) => {
      if (!full) { holder.replaceChildren(el('p', 'faint', 'The facts could not be read.')); return; }
      const names = Object.keys(full.facts || {}).sort();
      const missed = Object.keys(full.missed || {}).sort();
      const rows = names.map((n) => {
        const f = full.facts[n];
        const value = JSON.stringify(f.value, null, 2);
        const meta = el('div', 'fact-meta', [el('span', '', f.source), el('span', 'dim', 'read ' + (set(f.observed_at) ? ago(f.observed_at) : 'at an unknown time'))]);
        if (set(f.observed_at)) meta.lastChild.dataset.tip = dateTime(f.observed_at);
        if (f.carried) { const t = el('span', 'tag carried', 'Kept from an earlier pass'); t.dataset.tip = 'Its source could not read it in the last pass.'; meta.append(t); }
        const d = el('details', 'fact', [el('summary', '', [mono(n, 'name'), meta]), el('pre', '', el('code', '', value))]);
        return d;
      });
      const misses = missed.map((n) => {
        const m = full.missed[n];
        return el('div', 'fact missed', [el('div', 'fact-sum', [mono(n, 'name'), el('div', 'fact-meta', [el('span', '', m.source), el('span', 'tag bad', 'Not read')])]), el('p', 'detail', m.reason)]);
      });
      if (!rows.length && !misses.length) { holder.replaceChildren(el('p', 'faint', 'No source wrote a fact about it.')); return; }
      holder.replaceChildren(...misses, ...rows);
    };
    if (factCache.has(key)) { draw(factCache.get(key)); return; }
    holder.replaceChildren(el('p', 'faint', 'Reading the facts...'));
    fetch('/v1/subject?kind=' + encodeURIComponent(x.kind) + '&key=' + encodeURIComponent(x.key), { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => { factCache.set(key, j); if (holder.isConnected) draw(j); })
      .catch(() => draw(null));
  }

  function subject(x) {
    const byState = {};
    x.findings.forEach((f) => { (byState[f.state] = byState[f.state] || []).push(f); });
    const open = byState.open || [];
    const late = open.filter((f) => f.overdue).length;
    const body = [
      el('div', 'facts-row', [levelChip(x.draft ? 'draft' : x.level), el('span', 'dim', plural(open.length, 'open finding', 'open findings')), late ? el('span', 'exp late', num(late) + ' overdue') : null]),
      el('dl', 'facts', [el('dt', '', 'Owner'), el('dd', '', ownerLink(x.owner)), x.tiers && Object.keys(x.tiers).length ? [el('dt', '', 'Tiers'), el('dd', 'tags', Object.entries(x.tiers).map(([p, t]) => el('span', 'tag', (PACK_WORDS[p] || p) + ': ' + (t || 'none'))))] : null]),
    ];
    body.push(open.length ? section('Open findings', findingList(open)) : el('div', 'callout ok', [el('div', 'c-head', [icon('check', 15), el('b', '', 'Meets every rule that counts')])]));
    for (const st of ['announced', 'waived', 'draft']) if (byState[st]) body.push(section(STATE_WORDS[st], findingList(byState[st])));
    if (x.unchecked && x.unchecked.length) {
      body.push(section('No data for', el('ul', 'unchecked', x.unchecked.map((u) => el('li', '', [mono(u.rule), el('span', 'dim', u.reason)])))));
    }
    const holder = el('div', 'fact-list');
    body.push(section('Facts', holder));
    facts(x, holder);
    if (ctx.board && ctx.board.history) {
      const past = el('div', 'hist');
      body.push(section('Last 90 days', past));
      history(x, past);
    }
    return frame(KIND_ONE[x.kind] || x.kind, x.title, x.title !== x.key ? el('div', 'd-key', [mono(x.key), copy(x.key)]) : null, body);
  }

  return {
    sync() {
      const o = ctx.state.open;
      let content = null;
      if (o.startsWith('f:')) {
        const i = o.indexOf('|');
        const f = i > 0 ? find(o.slice(2, i), o.slice(i + 1)) : null;
        if (f) content = finding(f);
      } else if (o.startsWith('s:')) {
        const i = o.indexOf('|');
        const x = subjectOf(o.slice(2, i), o.slice(i + 1));
        if (x) content = subject(x);
      }
      const on = !!content;
      document.body.classList.toggle('drawer-open', on);
      drawer.setAttribute('aria-hidden', String(!on));
      if (!on) { drawer.replaceChildren(); last = ''; return; }
      const keep = o === last ? drawer.querySelector('.d-body') : null;
      const scroll = keep ? keep.scrollTop : 0;
      drawer.replaceChildren(...content);
      if (keep) drawer.querySelector('.d-body').scrollTop = scroll;
      else drawer.querySelector('.d-head .btn').focus({ preventScroll: true });
      last = o;
    },
    /** forget drops the cached facts after a new pass. */
    forget() { factCache.clear(); },
  };
}
