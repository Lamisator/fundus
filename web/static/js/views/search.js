// Search and filtered listings (/search, /entries, /trash) with facets.
import { html, icon, mount, api, $, plural, debounce, on, confirmDialog, toast, fail } from '../util.js';
import { state, canEdit, isAdmin, kindOf } from '../state.js';
import { setCrumbs, setTitle, navigate, refreshSidebar } from '../app.js';
import { entryRow, emptyState } from '../components.js';

const PAGE = 25;

export default async function ({ el, query, trash, path }) {
  const q = { ...query };
  const offset = +q.offset || 0;
  const isSearch = path === '/search';
  const space = q.space ? state.spaces.find((s) => s.slug === q.space) : null;
  const kind = q.kind ? kindOf(q.kind) : null;

  let title = 'All entries', sub = '';
  if (trash) { title = 'Trash'; sub = 'Deleted entries can be restored. Admins can delete them for good.'; }
  else if (isSearch) title = q.q ? 'Search' : 'Search';
  else if (q.starred) title = 'Starred';
  else if (space) { title = space.name; sub = space.description; }
  else if (kind) { title = kind.plural || kind.label; sub = kind.description; }
  if (q.tag && !isSearch) title = '#' + q.tag;
  setTitle(q.q ? `“${q.q}”` : title);
  setCrumbs([{ label: title }], canEdit() && !trash ? html`<a class="btn sm primary" href="/new${q.kind ? '?kind=' + q.kind : ''}">${icon('plus', 'sm')}New${kind ? ' ' + kind.label.toLowerCase() : ''}</a>` : '');

  mount(el, html`
    <div class="page-head"><div class="grow"><h1>${title}</h1>${sub ? html`<p>${sub}</p>` : ''}</div></div>
    <div class="search-layout">
      <aside class="facets" id="facets"></aside>
      <div>
        <form class="search-bar" id="sform">
          <div class="wrap">${icon('search')}<input class="input" id="sq" value="${q.q || ''}" placeholder="Search ${trash ? 'the trash' : 'everything'}… (try “quotes”, -exclude, or)" autocomplete="off"></div>
          <select class="select" id="sort" style="width:160px">
            ${[['', q.q ? 'Best match' : 'Recently updated'], ...(q.q ? [['updated', 'Recently updated']] : []), ['created', 'Newest'], ['title', 'Title A–Z'], ['views', 'Most viewed']]
              .map(([v, l]) => html`<option value="${v}" ${(q.sort || '') === v ? 'selected' : ''}>${l}</option>`)}
          </select>
        </form>
        <div class="result-meta" id="meta"></div>
        <div id="results"><div class="card" style="height:120px"></div></div>
        <div class="pager" id="pager"></div>
      </div>
    </div>`);

  const setQ = (patch) => {
    const n = { ...q, ...patch };
    delete n.offset;
    if (patch.offset) n.offset = patch.offset;
    for (const k of Object.keys(n)) if (n[k] === '' || n[k] == null) delete n[k];
    const base = trash ? '/trash' : (n.q ? '/search' : '/entries');
    const s = new URLSearchParams(n).toString();
    navigate(base + (s ? '?' + s : ''), { replace: !!patch._replace });
  };

  $('#sform').addEventListener('submit', (e) => { e.preventDefault(); setQ({ q: $('#sq').value.trim() }); });
  $('#sort').addEventListener('change', (e) => setQ({ sort: e.target.value }));
  if (isSearch && !q.q) $('#sq').focus();

  const res = await api('entries', {
    query: { q: q.q, kind: q.kind, status: q.status, tag: q.tag ? q.tag.split(',') : undefined, space: q.space, starred: q.starred,
      trash: trash ? 1 : undefined, sort: q.sort, limit: PAGE, offset, facets: 1 },
  });

  // facets
  const f = res.facets;
  const facet = (title, key, items, label) => items.length ? html`<div class="facet"><h4>${title}</h4>
    ${items.map((it) => {
      const on = (q[key] || '') === it.key;
      return html`<a href="#" data-facet="${key}" data-val="${on ? '' : it.key}" class="${on ? 'on' : ''}">${label(it.key)}<span class="n">${it.count}</span></a>`;
    })}</div>` : '';
  const spaceSlug = (id) => state.spaceMap[id]?.slug || '';
  mount($('#facets'), html`
    ${facet('Kind', 'kind', f.kinds, (k) => html`<span class="dot" style="--c:${kindOf(k).color}"></span>${kindOf(k).label}`)}
    ${facet('Status', 'status', f.statuses, (s) => html`<span class="chip status status-${s}" style="height:20px">${s}</span>`)}
    ${f.spaces.length > 1 || q.space ? facet('Space', 'space', f.spaces.filter((s) => s.key).map((s) => ({ ...s, key: spaceSlug(+s.key) })),
        (slug) => html`<span class="dot" style="--c:${state.spaces.find((s) => s.slug === slug)?.color}"></span>${state.spaces.find((s) => s.slug === slug)?.name || slug}`) : ''}
    ${f.tags.length ? html`<div class="facet"><h4>Tags</h4>${f.tags.slice(0, 18).map((t) => {
      const cur = q.tag ? q.tag.split(',') : [];
      const on = cur.includes(t.key);
      const next = on ? cur.filter((x) => x !== t.key) : [...cur, t.key];
      return html`<a href="#" data-facet="tag" data-val="${next.join(',')}" class="${on ? 'on' : ''}"><span class="faint">#</span>${t.key}<span class="n">${t.count}</span></a>`;
    })}</div>` : ''}`);
  on($('#facets'), 'click', '[data-facet]', (e, a) => { e.preventDefault(); setQ({ [a.dataset.facet]: a.dataset.val }); });

  // active filters + meta
  const filters = [];
  if (q.kind) filters.push(['kind', kindOf(q.kind).label]);
  if (q.status) filters.push(['status', q.status]);
  if (q.space) filters.push(['space', space?.name || q.space]);
  if (q.tag) q.tag.split(',').forEach((t) => filters.push(['tag:' + t, '#' + t]));
  if (q.starred) filters.push(['starred', 'starred']);
  mount($('#meta'), html`<span>${plural(res.total, 'entry', 'entries')}${q.q ? html` for <b>“${q.q}”</b>` : ''}</span>
    <span class="active-filters">${filters.map(([k, l]) => html`<span class="chip" data-clear="${k}" title="Remove filter">${l}${icon('x', 'sm')}</span>`)}</span>`);
  on($('#meta'), 'click', '[data-clear]', (e, c) => {
    const k = c.dataset.clear;
    if (k.startsWith('tag:')) setQ({ tag: q.tag.split(',').filter((t) => t !== k.slice(4)).join(',') });
    else setQ({ [k]: '' });
  });

  // results
  if (!res.items.length) {
    mount($('#results'), html`<div class="card">${trash ? emptyState('trash', 'The trash is empty', 'Deleted entries show up here.') :
      emptyState('search', 'No entries found', q.q ? 'Try fewer words, check the spelling, or remove a filter.' : 'Nothing matches these filters yet.',
        canEdit() && q.q ? html`<a class="btn primary" href="/new?kind=problem&title=${encodeURIComponent(q.q)}">${icon('plus')}Document “${q.q.length > 40 ? q.q.slice(0, 40) + '…' : q.q}”</a>` : '')}</div>`);
  } else if (trash) {
    mount($('#results'), html`<div class="card entry-list">${res.items.map((e) => html`<div class="entry-row" style="grid-template-columns:30px 1fr auto">
      <span class="kind-ic" style="--c:${kindOf(e.kind).color}">${icon(kindOf(e.kind).icon)}</span>
      <div><div class="t">${e.title}</div><div class="s">Deleted ${new Date(e.deleted_at).toLocaleString()}</div></div>
      <div class="row"><button class="btn sm" data-restore="${e.id}">${icon('restore', 'sm')}Restore</button>
      ${isAdmin() ? html`<button class="btn sm danger" data-purge="${e.id}">${icon('trash', 'sm')}Delete forever</button>` : ''}</div></div>`)}</div>`);
    on($('#results'), 'click', '[data-restore]', async (e, b) => {
      try { await api(`entries/${b.dataset.restore}/restore`, { method: 'POST' }); toast('Restored'); refreshSidebar(); navigate('/e/' + b.dataset.restore); } catch (err) { fail(err); }
    });
    on($('#results'), 'click', '[data-purge]', async (e, b) => {
      if (!(await confirmDialog('This permanently deletes the entry, its history, solutions and attachments.', { ok: 'Delete forever', danger: true }))) return;
      try { await api(`entries/${b.dataset.purge}?purge=1`, { method: 'DELETE' }); toast('Deleted'); setQ({ _replace: true }); } catch (err) { fail(err); }
    });
  } else {
    mount($('#results'), html`<div class="card entry-list">${res.items.map((e) => entryRow(e, { showSpace: !q.space }))}</div>`);
  }

  // pager
  const pages = Math.ceil(res.total / PAGE);
  const cur = Math.floor(offset / PAGE);
  if (pages > 1) {
    mount($('#pager'), html`
      <button class="btn sm" data-off="${(cur - 1) * PAGE}" ${cur === 0 ? 'disabled' : ''}>${icon('back', 'sm')}Previous</button>
      <span class="muted small" style="align-self:center">Page ${cur + 1} of ${pages}</span>
      <button class="btn sm" data-off="${(cur + 1) * PAGE}" ${cur >= pages - 1 ? 'disabled' : ''}>Next${icon('chevron', 'sm')}</button>`);
    on($('#pager'), 'click', '[data-off]', (e, b) => setQ({ offset: b.dataset.off }));
  }
}
