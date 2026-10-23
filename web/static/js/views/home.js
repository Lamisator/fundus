// Dashboard: search hero, stats, activity heatmap, recent & open items.
import { html, icon, mount, api, $, plural, userName } from '../util.js';
import { state, canEdit, kindIcon, tagChip } from '../state.js';
import { setCrumbs, setTitle, navigate } from '../app.js';
import { entryList, emptyState } from '../components.js';

export default async function ({ el }) {
  setTitle('');
  setCrumbs([{ label: 'Home' }]);
  const hour = new Date().getHours();
  const greet = hour < 5 ? 'Up late' : hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening';
  const name = (state.me.user.display_name || state.me.user.username).split(' ')[0];
  mount(el, html`
    <section class="hero">
      <h1>${greet}, <em>${name}</em>.</h1>
      <p>What are you trying to fix today? Search titles, solutions and error messages — or paste a whole log line.</p>
      <form class="big-search" id="hero-search">
        ${icon('search', 'lead')}
        <textarea id="hero-q" rows="1" placeholder="e.g. “docker permission denied /var/run/docker.sock”" spellcheck="false"></textarea>
        <span class="hint"><kbd>↵</kbd> search</span>
      </form>
    </section>
    <div class="stats" id="stats"></div>
    <div class="home-grid">
      <div class="col" style="gap:18px">
        <div id="pinned"></div>
        <div>
          <div class="section-title">${icon('clock')}Recently updated<span class="spacer"></span><a class="small" href="/entries">View all</a></div>
          <div id="recent"></div>
        </div>
      </div>
      <div class="col" style="gap:18px">
        ${canEdit() ? html`<div class="card card-pad"><h3>${icon('plus')}Document something</h3>
          <div class="quick-new">${state.kinds.slice(0, 6).map((k) => html`<a href="/new?kind=${k.key}" style="--c:${k.color}">${kindIcon(k.key)}${k.label}</a>`)}</div></div>` : ''}
        <div class="card card-pad"><h3>${icon('alert')}Open problems<span class="spacer"></span><a class="small" href="/entries?kind=problem&status=open">All</a></h3><div class="mini-list" id="open"></div></div>
        <div class="card card-pad"><h3>${icon('activity')}Activity<span class="spacer"></span><span class="small faint" id="act-sum"></span></h3><div class="heatmap" id="heat"></div></div>
        <div class="card card-pad"><h3>${icon('tag')}Popular tags<span class="spacer"></span><a class="small" href="/tags">All</a></h3><div class="tag-cloud" id="tags"></div></div>
      </div>
    </div>`);

  const q = $('#hero-q');
  const grow = () => { q.style.height = 'auto'; q.style.height = Math.min(q.scrollHeight, 240) + 'px'; };
  q.addEventListener('input', grow);
  q.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); go(); } });
  const go = () => { const v = q.value.trim(); if (v) navigate('/search?q=' + encodeURIComponent(v)); };
  $('#hero-search').addEventListener('submit', (e) => { e.preventDefault(); go(); });

  const [stats, recent, pinned, open, tags] = await Promise.all([
    api('stats'),
    api('entries', { query: { limit: 8, sort: 'updated' } }),
    api('entries', { query: { limit: 6, pinned: 1 } }),
    api('entries', { query: { kind: 'problem', status: 'open', limit: 6, sort: 'updated' } }),
    api('tags', { query: { limit: 24 } }),
  ]);

  const tile = (c, ic, k, v, sub) => html`<div class="card stat" style="--c:${c}"><div class="k">${icon(ic)}${k}</div><div class="v">${v}</div><div class="sub">${sub}</div></div>`;
  mount($('#stats'), html`
    ${tile('var(--accent)', 'layers', 'Entries', stats.entries, plural(stats.tags, 'tag'))}
    ${tile('var(--warn)', 'alert', 'Open problems', stats.problems_open, 'waiting for a fix')}
    ${tile('var(--success)', 'checkCircle', 'Solved', stats.problems_solved, stats.median_days_to_solve ? `median ${fmtDays(stats.median_days_to_solve)} to solve` : 'problems with a fix')}
    ${tile('#0aa5c0', 'bulb', 'Solutions', stats.solutions, `${stats.edits_this_week} edits this week`)}`);

  mount($('#recent'), recent.items.length ? entryList(recent.items) :
    html`<div class="card">${emptyState('inbox', 'Nothing here yet', 'Your knowledge base is empty. Start with the problem you solved most recently.',
      canEdit() ? html`<a class="btn primary" href="/new?kind=problem">${icon('plus')}Document a problem</a>` : '')}</div>`);
  if (pinned.items.length) {
    mount($('#pinned'), html`<div class="section-title">${icon('pin')}Pinned</div>${entryList(pinned.items)}`);
  }
  mount($('#open'), open.items.length ? html`${open.items.map((e) => html`<a href="/e/${e.id}"><span class="dot" style="--c:var(--warn)"></span><span class="ellipsis grow">${e.title}</span></a>`)}` :
    html`<p class="muted small" style="margin:0">No open problems. ✨</p>`);
  mount($('#tags'), tags.length ? html`${tags.map((t) => tagChip(t.key))}` : html`<p class="muted small" style="margin:0">No tags yet.</p>`);

  const max = Math.max(1, ...stats.activity.map((d) => d.count));
  const first = new Date(stats.activity[0].key + 'T00:00:00');
  const pad = (first.getDay() + 6) % 7; // weeks start on Monday
  const cells = Array(pad).fill('<i style="visibility:hidden"></i>');
  for (const d of stats.activity) {
    const l = d.count === 0 ? 0 : Math.min(4, Math.ceil((d.count / max) * 4));
    cells.push(`<i data-l="${l}" title="${d.key}: ${d.count} edit${d.count === 1 ? '' : 's'}"></i>`);
  }
  $('#heat').innerHTML = cells.join('');
  $('#heat').scrollLeft = 1e6;
  const total = stats.activity.reduce((n, d) => n + d.count, 0);
  $('#act-sum').textContent = `${total} edits in the last year`;
}

function fmtDays(d) {
  if (d < 1 / 24) return 'minutes';
  if (d < 1) return Math.round(d * 24) + ' h';
  return Math.round(d) + ' d';
}
