// Revision history with diffs and restore.
import { html, raw, icon, mount, api, $, on, ago, fmtDate, avatar, userName, toast, fail, confirmDialog } from '../util.js';
import { canEdit, kindOf, state } from '../state.js';
import { setCrumbs, setTitle, navigate } from '../app.js';
import { renderDiff } from '../diff.js';

export default async function ({ el, params, query }) {
  const id = +params[0];
  const [e, revs] = await Promise.all([api(`entries/${id}`, { query: { render: 'false' } }), api(`entries/${id}/revisions`)]);
  setTitle('History · ' + e.title);
  setCrumbs([{ label: kindOf(e.kind).plural, href: '/entries?kind=' + e.kind }, { label: e.title, href: '/e/' + id }, { label: 'History' }]);
  let cur = +query.v || revs[0]?.version;

  mount(el, html`
    <div class="page-head"><div class="grow"><h1>History</h1><p>${revs.length} version${revs.length === 1 ? '' : 's'} of <a href="/e/${id}">${e.title}</a></p></div></div>
    <div class="rev-layout">
      <div class="card rev-list" id="revs">${revs.map((r) => html`<a href="?v=${r.version}" data-v="${r.version}">
        <div class="row"><span class="v">v${r.version}</span>${r.version === e.version ? html`<span class="chip" style="height:18px">current</span>` : ''}<span class="spacer"></span><span class="faint small" title="${fmtDate(r.edited_at, true)}">${ago(r.edited_at)}</span></div>
        <div class="row small" style="margin-top:3px">${r.edited_by ? avatar(r.edited_by) : ''}<span class="muted">${userName(r.edited_by)}</span></div>
        ${r.note ? html`<div class="note">${r.note}</div>` : ''}</a>`)}</div>
      <div id="diff-pane"></div>
    </div>`);

  const show = async (v) => {
    cur = v;
    history.replaceState(null, '', `?v=${v}`);
    document.querySelectorAll('#revs a').forEach((a) => a.classList.toggle('on', +a.dataset.v === v));
    const idx = revs.findIndex((r) => r.version === v);
    const prevV = revs[idx + 1]?.version;
    const [rv, pv] = await Promise.all([api(`entries/${id}/revisions/${v}`), prevV ? api(`entries/${id}/revisions/${prevV}`) : null]);
    const p = pv || { title: '', summary: '', body: '', error_text: '', status: '', tags: [], fields: {}, kind: rv.kind, space_id: null };
    const changes = [];
    const cmp = (label, a, b) => { if (a !== b) changes.push(html`<dt>${label}</dt><dd>${a ? html`<del class="muted">${a}</del> → ` : ''}<b>${b || '—'}</b></dd>`); };
    cmp('Title', p.title, rv.title);
    cmp('Kind', kindOf(p.kind).label, kindOf(rv.kind).label);
    cmp('Status', p.status, rv.status);
    cmp('Space', state.spaceMap[p.space_id]?.name || '', state.spaceMap[rv.space_id]?.name || '');
    cmp('Summary', p.summary, rv.summary);
    cmp('Tags', p.tags.join(', '), rv.tags.join(', '));
    const fs = (f) => Object.entries(f || {}).sort().map(([k, x]) => `${k}=${x}`).join(', ');
    cmp('Fields', fs(p.fields), fs(rv.fields));
    mount($('#diff-pane'), html`
      <div class="row" style="margin-bottom:12px">
        <h2 style="margin:0;font-size:18px">Version ${v}</h2>
        <span class="muted small">${pv ? `compared with v${prevV}` : 'initial version'} · ${fmtDate(rv.edited_at, true)} by ${userName(rv.edited_by)}</span>
        <span class="spacer"></span>
        ${canEdit() && v !== e.version ? html`<button class="btn sm" id="restore">${icon('restore', 'sm')}Restore this version</button>` : ''}
      </div>
      ${rv.note ? html`<div class="alert info">${rv.note}</div>` : ''}
      ${changes.length ? html`<dl class="diff-meta card card-pad">${changes}</dl>` : ''}
      ${p.error_text !== rv.error_text ? html`<div class="section-title">Error message</div><div class="diff" style="margin-bottom:16px">${raw(renderDiff(p.error_text, rv.error_text))}</div>` : ''}
      <div class="section-title">Body</div>
      <div class="diff">${raw(renderDiff(p.body, rv.body))}</div>`);
    $('#restore')?.addEventListener('click', async () => {
      if (!(await confirmDialog(`Restore version ${v}? This creates a new version with its content; nothing is lost.`, { ok: 'Restore' }))) return;
      try { await api(`entries/${id}/revisions/${v}/restore`, { method: 'POST' }); toast(`Restored version ${v}`); navigate('/e/' + id); }
      catch (err) { fail(err); }
    });
  };
  on($('#revs'), 'click', '[data-v]', (ev, a) => { ev.preventDefault(); show(+a.dataset.v); });
  if (cur) show(cur);
}
