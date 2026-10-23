// Tag overview with rename / merge / delete.
import { html, icon, mount, api, $, on, toast, fail, confirmDialog, promptDialog, popMenu } from '../util.js';
import { canEdit, isAdmin } from '../state.js';
import { setCrumbs, setTitle } from '../app.js';
import { emptyState } from '../components.js';

export default async function ({ el }) {
  setTitle('Tags');
  setCrumbs([{ label: 'Tags' }]);
  let tags = [];
  let filter = '';
  mount(el, html`
    <div class="page-head"><div class="grow"><h1>Tags</h1><p>Click a tag to see its entries. Renaming to an existing tag merges the two.</p></div>
      <div class="search-bar" style="margin:0;width:280px"><div class="wrap">${icon('search')}<input class="input" id="tf" placeholder="Filter tags…"></div></div></div>
    <div id="list"></div>`);
  const draw = () => {
    const shown = tags.filter((t) => t.key.includes(filter));
    if (!tags.length) { mount($('#list'), html`<div class="card">${emptyState('tag', 'No tags yet', 'Tags appear here once entries use them.')}</div>`); return; }
    const max = Math.max(...tags.map((t) => t.count));
    mount($('#list'), html`<div class="card card-pad"><div class="tag-cloud" style="gap:8px">
      ${shown.map((t) => html`<span class="row" style="gap:0"><a class="chip tag" href="/entries?tag=${encodeURIComponent(t.key)}" style="height:${24 + Math.round((t.count / max) * 8)}px;font-size:${12 + Math.round((t.count / max) * 3)}px">${t.key}<span class="faint" style="margin-left:4px">${t.count}</span></a>
        ${canEdit() ? html`<button class="btn ghost sm icon" data-tag="${t.key}" style="width:22px;height:22px">${icon('more', 'sm')}</button>` : ''}</span>`)}
    </div></div>`);
  };
  const load = async () => { tags = await api('tags', { query: { limit: 1000 } }); draw(); };
  $('#tf').addEventListener('input', (e) => { filter = e.target.value.trim().toLowerCase(); draw(); });
  on(el, 'click', '[data-tag]', (e, b) => {
    const name = b.dataset.tag;
    popMenu(b, [
      { label: 'Rename / merge…', icon: 'edit', act: async () => {
        const to = await promptDialog('New name', { title: `Rename #${name}`, value: name, hint: 'If the tag already exists, both are merged.' });
        if (!to || to === name) return;
        try { const r = await api('tags/' + encodeURIComponent(name), { method: 'PATCH', body: { name: to } }); toast(`Renamed on ${r.entries} entries`); load(); } catch (err) { fail(err); }
      } },
      isAdmin() && { label: 'Remove from all entries', icon: 'trash', danger: true, act: async () => {
        if (!(await confirmDialog(`Remove #${name} from every entry?`, { ok: 'Remove tag', danger: true }))) return;
        try { await api('tags/' + encodeURIComponent(name), { method: 'DELETE' }); load(); } catch (err) { fail(err); }
      } },
    ].filter(Boolean));
  });
  await load();
}
