// Entry detail page.
import { html, raw, icon, mount, api, $, $$, on, ago, fmtDate, avatar, userName, bytes, copy, toast, fail, confirmDialog, popMenu, modal, plural } from '../util.js';
import { state, canEdit, isAdmin, kindOf, kindChip, statusChip, tagChip, spaceChip } from '../state.js';
import { setCrumbs, setTitle, navigate, refreshSidebar } from '../app.js';
import { enhanceProse } from '../components.js';
import { createMdEditor, pickEntry } from '../mdeditor.js';

const REL_LABEL = {
  'related': ['Related', 'Related'], 'duplicate-of': ['Duplicate of', 'Duplicated by'], 'caused-by': ['Caused by', 'Causes'],
  'depends-on': ['Depends on', 'Required by'], 'supersedes': ['Supersedes', 'Superseded by'], 'mentioned-by': ['Mentioned in', 'Mentioned in'],
};

export default async function ({ el, params }) {
  const id = +params[0];
  const load = async (q) => {
    const x = await api(`entries/${id}`, { query: { expand: 'all', ...q } });
    x.solutions ||= []; x.links ||= []; x.attachments ||= [];
    return x;
  };
  let e = await load({ count_view: 1 });
  const kind = kindOf(e.kind);
  setTitle(e.title);

  const render = () => {
    setCrumbs([{ label: kind.plural || kind.label, href: '/entries?kind=' + kind.key }, { label: e.title }], actionsBar());
    mount(el, html`
      ${e.deleted_at ? html`<div class="trash-banner">${icon('trash')}This entry is in the trash.<span class="spacer"></span>
        <button class="btn sm" data-act="restore">${icon('restore', 'sm')}Restore</button></div>` : ''}
      <div class="entry-layout">
        <article>
          <header class="entry-head">
            <div class="chips">${kindChip(e.kind)}${statusChip(e.status)}${e.space_id ? spaceChip(e.space_id) : ''}${e.pinned ? html`<span class="chip">${icon('pin', 'sm')}Pinned</span>` : ''}</div>
            <h1>${e.title}</h1>
            ${e.summary ? html`<p class="summary">${e.summary}</p>` : ''}
            <div class="byline">
              ${e.created_by ? avatar(e.created_by) : ''}
              <span>${userName(e.created_by)} · created ${fmtDate(e.created_at)}</span>
              ${e.version > 1 ? html`<span>· <a href="/e/${e.id}/history">edited ${ago(e.updated_at)}${e.updated_by && e.updated_by.id !== e.created_by?.id ? ' by ' + userName(e.updated_by) : ''}</a></span>` : ''}
              <span>· ${plural(e.view_count, 'view')}</span>
            </div>
          </header>
          ${e.error_text ? html`<div class="error-block"><div class="lbl">${icon('warn', 'sm')}Error message<span class="spacer"></span>
            <button class="btn ghost sm" data-act="copy-error" style="height:22px">${icon('copy', 'sm')}Copy</button></div><pre>${e.error_text}</pre></div>` : ''}
          <div class="prose" id="body">${e.body_html ? raw(e.body_html) : html`<p class="faint">No description yet.</p>`}</div>
          ${kind.has_solutions || e.solutions?.length ? solutionsSection() : ''}
        </article>
        <aside class="rail" id="rail">${rail()}</aside>
      </div>`);
    enhanceProse($('#body'));
    $$('.solution-body', el).forEach(enhanceProse);
    buildToc();
    if (canEdit() && !e.deleted_at && kind.has_solutions) mountSolutionEditor();
  };

  const actionsBar = () => html`<div class="entry-actions">
    <button class="btn sm ${e.starred ? 'on' : ''}" data-act="star" title="Star (s)">${icon('star', 'sm')}${e.starred ? 'Starred' : 'Star'}</button>
    ${canEdit() && !e.deleted_at ? html`<a class="btn sm" href="/e/${e.id}/edit" title="Edit (e)">${icon('edit', 'sm')}Edit</a>` : ''}
    <button class="btn sm icon" data-act="more" title="More">${icon('more', 'sm')}</button>
  </div>`;

  const solutionsSection = () => html`
    <div class="solutions-head"><h2>Solutions</h2><span class="badge">${e.solutions.length}</span></div>
    ${e.solutions.length ? '' : html`<p class="muted">No solution yet.${canEdit() ? ' Found one? Write it down below.' : ''}</p>`}
    ${e.solutions.map((s) => html`<div class="card solution ${s.accepted ? 'accepted' : ''}" data-sol="${s.id}">
      <div class="solution-head">
        ${s.accepted ? html`<span class="accepted-badge">${icon('checkCircle', 'sm')}Accepted</span>` : icon('bulb')}
        <b>${s.title || (s.accepted ? '' : 'Solution')}</b><span class="spacer"></span>
        ${canEdit() ? html`<button class="btn ghost sm icon" data-act="sol-more" data-id="${s.id}">${icon('more', 'sm')}</button>` : ''}
      </div>
      <div class="solution-body prose">${raw(s.body_html)}</div>
      <div class="solution-foot">
        <button class="btn sm ${s.voted ? 'on' : ''} works" data-act="vote" data-id="${s.id}" title="This worked for me">${icon('thumb', 'sm')}Worked for me${s.works_for ? html` · <b>${s.works_for}</b>` : ''}</button>
        ${canEdit() && !s.accepted ? html`<button class="btn sm ghost" data-act="accept" data-id="${s.id}">${icon('check', 'sm')}Accept</button>` : ''}
        <span class="spacer"></span>
        ${s.created_by ? avatar(s.created_by) : ''}<span>${userName(s.created_by)} · ${ago(s.created_at)}</span>
      </div>
    </div>`)}
    ${canEdit() && !e.deleted_at && kind.has_solutions ? html`<div class="card card-pad" id="new-sol">
      <h3>${icon('plus')}Add a solution</h3>
      <input class="input" id="sol-title" placeholder="Short title, e.g. “Raise client_max_body_size” (optional)" style="margin-bottom:10px">
      <div id="sol-editor"></div>
      <div class="row" style="margin-top:10px"><label class="check"><input type="checkbox" id="sol-accept" ${e.solutions.some((s) => s.accepted) ? '' : 'checked'}>Mark as accepted${kind.statuses.includes('solved') ? ' (problem becomes solved)' : ''}</label>
      <span class="spacer"></span><button class="btn primary" data-act="add-sol">${icon('check')}Add solution</button></div>
    </div>` : ''}`;

  let solEditor = null;
  const mountSolutionEditor = () => {
    solEditor = createMdEditor($('#sol-editor'), { entryId: e.id, minHeight: 160, mode: 'write', placeholder: 'What fixed it? Commands, config changes, links…', onSubmit: addSolution });
  };

  const rail = () => {
    const fields = Object.entries(e.fields || {});
    const kf = Object.fromEntries((kind.fields || []).map((f) => [f.key, f.label]));
    const links = e.links || [];
    return html`
      <div class="card"><h3>${icon('sliders')}Details</h3>
        <dl class="props">
          <dt>Kind</dt><dd>${kind.label}</dd>
          ${e.status ? html`<dt>Status</dt><dd>${canEdit() ? html`<select class="select" data-act="status" style="height:28px;font-size:12.5px">${kind.statuses.map((s) => html`<option ${s === e.status ? 'selected' : ''}>${s}</option>`)}</select>` : e.status}</dd>` : ''}
          ${e.space_id ? html`<dt>Space</dt><dd>${state.spaceMap[e.space_id]?.name || ''}</dd>` : ''}
          ${fields.map(([k, v]) => html`<dt>${kf[k] || k}</dt><dd>${/^https?:\/\//.test(v) ? html`<a href="${v}" target="_blank" rel="noopener noreferrer">${v}</a>` : v}</dd>`)}
          ${e.tags.length ? html`<dt>Tags</dt><dd>${e.tags.map(tagChip)}</dd>` : ''}
          <dt>Updated</dt><dd title="${new Date(e.updated_at).toLocaleString()}">${ago(e.updated_at)}</dd>
          <dt>Version</dt><dd><a href="/e/${e.id}/history">v${e.version}</a></dd>
          <dt>ID</dt><dd><span class="mono small">#${e.id}</span> <button class="btn ghost sm icon" data-act="copy-ref" title="Copy [[${e.id}]] reference" style="height:20px;width:20px">${icon('copy', 'sm')}</button></dd>
        </dl>
      </div>
      <div class="card hidden" id="toc-card"><h3>${icon('list')}On this page</h3><nav class="toc" id="toc"></nav></div>
      <div class="card"><h3>${icon('link')}Links<span class="spacer"></span>${canEdit() ? html`<button class="btn ghost sm icon" data-act="add-link" title="Add link">${icon('plus', 'sm')}</button>` : ''}</h3>
        ${links.length ? links.map((l) => html`<div class="link-item">${icon(kindOf(l.entry.kind).icon, 'sm')}<div class="grow">
          <span class="rel">${(REL_LABEL[l.rel] || [l.rel, l.rel])[l.direction === 'incoming' ? 1 : 0]}</span>
          <a href="/e/${l.entry.id}">${l.entry.title}</a></div>
          ${canEdit() && l.direction !== 'mention' ? html`<button class="btn ghost sm icon x" data-act="unlink" data-to="${l.entry.id}" data-rel="${l.rel}" title="Remove link">${icon('x', 'sm')}</button>` : ''}</div>`)
          : html`<p class="muted small" style="margin:0">No links yet. Mention another entry with <code>[[id]]</code> or add one here.</p>`}
      </div>
      <div class="card"><h3>${icon('clip')}Attachments<span class="spacer"></span>${canEdit() ? html`<button class="btn ghost sm icon" data-act="upload" title="Upload">${icon('upload', 'sm')}</button>` : ''}</h3>
        ${e.attachments?.length ? e.attachments.map((a) => html`<div class="att">${icon(a.content_type.startsWith('image/') ? 'image' : 'file', 'sm')}
          <a href="${a.url}" target="_blank" class="ellipsis" title="${a.filename}">${a.filename}</a><span class="faint small nowrap">${bytes(a.size)}</span>
          ${canEdit() ? html`<button class="btn ghost sm icon" data-act="del-att" data-id="${a.id}" title="Delete" style="height:22px;width:22px">${icon('x', 'sm')}</button>` : ''}</div>`)
          : html`<p class="muted small" style="margin:0">None. Paste or drop files into the editor.</p>`}
        <input type="file" id="att-input" multiple hidden>
      </div>
      <div class="card" id="similar-card"><h3>${icon('sparkle')}Similar entries</h3><div class="mini-list" id="similar"><p class="muted small" style="margin:0">Looking…</p></div></div>`;
  };

  const buildToc = () => {
    const hs = $$('#body h2, #body h3');
    if (hs.length < 3) return;
    $('#toc').innerHTML = hs.map((h) => String(html`<a href="#${h.id}" class="${h.tagName === 'H3' ? 'l3' : ''}">${h.textContent}</a>`)).join('');
    $('#toc-card').classList.remove('hidden');
    $('#toc').addEventListener('click', (ev) => {
      const a = ev.target.closest('a');
      if (!a) return;
      ev.preventDefault();
      document.getElementById(a.getAttribute('href').slice(1))?.scrollIntoView({ behavior: 'smooth' });
    });
  };

  const loadSimilar = async () => {
    try {
      const list = await api(`entries/${e.id}/similar`, { query: { limit: 5 } });
      const box = $('#similar');
      if (!box) return;
      const linked = new Set((e.links || []).map((l) => l.entry.id));
      const items = list.filter((x) => !linked.has(x.id));
      if (!items.length) { $('#similar-card').classList.add('hidden'); return; }
      mount(box, html`${items.map((x) => html`<a href="/e/${x.id}"><span class="dot" style="--c:${kindOf(x.kind).color}"></span><span class="ellipsis grow">${x.title}</span></a>`)}`);
    } catch {}
  };

  const reload = async () => {
    e = await load();
    const y = window.scrollY;
    render();
    loadSimilar();
    window.scrollTo(0, y);
  };

  const addSolution = async () => {
    const body = solEditor?.value.trim();
    if (!body) { toast('Write the solution first', 'error'); return; }
    try {
      await api(`entries/${e.id}/solutions`, { method: 'POST', body: { title: $('#sol-title').value.trim(), body, accepted: $('#sol-accept').checked } });
      toast('Solution added');
      await reload();
    } catch (err) { fail(err); }
  };

  const editSolution = (s) => {
    let ed;
    modal({
      title: 'Edit solution', wide: true,
      body: html`<input class="input" data-t value="${s.title}" placeholder="Title (optional)" style="margin-bottom:10px"><div data-ed></div>`,
      foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-save>Save</button>`,
      onMount(m, close) {
        ed = createMdEditor(m.querySelector('[data-ed]'), { value: s.body, entryId: e.id, minHeight: 240, mode: 'write' });
        const save = async () => {
          try {
            await api(`solutions/${s.id}`, { method: 'PATCH', body: { title: m.querySelector('[data-t]').value.trim(), body: ed.value } });
            close(); toast('Solution saved'); reload();
          } catch (err) { fail(err); }
        };
        m.querySelector('[data-save]').onclick = save;
      },
    });
  };

  const share = () => {
    modal({
      title: 'Share read-only link',
      body: html`<p class="muted" style="margin-top:0">Anyone with the link can read this entry and its solutions without signing in. Links to other entries stay private.</p>
        <div data-list></div>
        <div class="row" style="margin-top:12px"><select class="select" data-exp style="width:auto"><option value="0">Never expires</option><option value="1">Expires in 1 day</option><option value="7" selected>Expires in 7 days</option><option value="30">Expires in 30 days</option></select>
        <button class="btn primary" data-new>${icon('share', 'sm')}Create link</button></div>`,
      onMount(m) {
        const draw = async () => {
          const list = await api(`entries/${e.id}/shares`);
          mount(m.querySelector('[data-list]'), list.length ? html`<div class="card list-card">${list.map((s) => html`<div class="li">${icon('globe', 'lead')}
            <div class="grow"><div class="mono small ellipsis">${s.url}</div><div class="d">${s.expires_at ? 'expires ' + ago(s.expires_at) : 'never expires'} · ${plural(s.views, 'view')}</div></div>
            <button class="btn sm icon" data-copy="${s.url}" title="Copy">${icon('copy', 'sm')}</button>
            <button class="btn sm icon danger" data-revoke="${s.token}" title="Revoke">${icon('trash', 'sm')}</button></div>`)}</div>`
            : html`<p class="faint small">No active links.</p>`);
        };
        on(m, 'click', '[data-copy]', (ev, b) => copy(b.dataset.copy, 'Link copied'));
        on(m, 'click', '[data-revoke]', async (ev, b) => { await api(`entries/${e.id}/shares/${b.dataset.revoke}`, { method: 'DELETE' }); draw(); });
        m.querySelector('[data-new]').onclick = async () => {
          try {
            const s = await api(`entries/${e.id}/shares`, { method: 'POST', body: { expires_in_days: +m.querySelector('[data-exp]').value } });
            copy(s.url, 'Link created and copied');
            draw();
          } catch (err) { fail(err); }
        };
        draw();
      },
    });
  };

  const addLink = async () => {
    const target = await pickEntry({ title: 'Link to…', exclude: e.id });
    if (!target) return;
    modal({
      title: 'How are they related?',
      body: html`<p style="margin-top:0"><b>${e.title}</b></p><select class="select" data-rel>
        <option value="related">is related to</option><option value="duplicate-of">is a duplicate of</option><option value="caused-by">is caused by</option>
        <option value="depends-on">depends on</option><option value="supersedes">supersedes</option></select><p style="margin-bottom:0"><b>${target.title}</b></p>`,
      foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-ok>Add link</button>`,
      onMount(m, close) {
        m.querySelector('[data-ok]').onclick = async () => {
          try { await api(`entries/${e.id}/links`, { method: 'POST', body: { to: target.id, rel: m.querySelector('[data-rel]').value } }); close(); reload(); }
          catch (err) { fail(err); }
        };
      },
    });
  };

  const upload = async (files) => {
    for (const f of files) {
      const fd = new FormData();
      fd.append('file', f);
      fd.append('entry_id', e.id);
      try { await api('attachments', { method: 'POST', raw: fd }); toast(`Uploaded ${f.name}`); } catch (err) { fail(err); }
    }
    reload();
  };

  const toggleStar = async () => {
    try {
      await api(`entries/${e.id}/star`, { method: e.starred ? 'DELETE' : 'PUT' });
      e.starred = !e.starred;
      setCrumbs([{ label: kind.plural || kind.label, href: '/entries?kind=' + kind.key }, { label: e.title }], actionsBar());
      toast(e.starred ? 'Starred' : 'Unstarred');
    } catch (err) { fail(err); }
  };

  const moreMenu = (btn) => popMenu(btn, [
    { label: 'History', icon: 'history', act: () => navigate(`/e/${e.id}/history`) },
    canEdit() && { label: 'Share link…', icon: 'share', act: share },
    { label: 'Copy link', icon: 'link', act: () => copy(location.origin + '/e/' + e.id, 'Link copied') },
    { label: 'Copy as Markdown', icon: 'copy', act: async () => copy(await api(`entries/${e.id}/markdown`), 'Markdown copied') },
    { label: 'Download .md', icon: 'download', act: () => { location.href = `/api/v1/entries/${e.id}/markdown?download=1`; } },
    { label: 'Print', icon: 'file', act: () => print() },
    ...(canEdit() && !e.deleted_at ? ['sep',
      { label: e.pinned ? 'Unpin' : 'Pin to home', icon: 'pin', act: async () => { await api(`entries/${e.id}/pin`, { method: e.pinned ? 'DELETE' : 'PUT' }); reload(); } },
      { label: 'Duplicate', icon: 'copy', act: () => navigate(`/new?from=${e.id}`) },
      { label: 'Move to trash', icon: 'trash', danger: true, act: async () => {
        if (!(await confirmDialog(`“${e.title}” will be moved to the trash. You can restore it from there.`, { ok: 'Move to trash', danger: true }))) return;
        try { await api(`entries/${e.id}`, { method: 'DELETE' }); toast('Moved to trash'); refreshSidebar(); navigate('/entries'); } catch (err) { fail(err); }
      } }] : []),
  ].filter(Boolean));

  render();
  loadSimilar();

  const onClick = async (ev) => {
    const b = ev.target.closest('[data-act]');
    if (!b || !el.contains(b) && !$('#top-actions').contains(b)) return;
    const act = b.dataset.act;
    const sid = +b.dataset.id;
    const sol = e.solutions?.find((s) => s.id === sid);
    try {
      switch (act) {
        case 'star': return toggleStar();
        case 'more': return moreMenu(b);
        case 'copy-error': return copy(e.error_text, 'Error message copied');
        case 'copy-ref': return copy(`[[${e.id}]]`, `Copied [[${e.id}]] — paste it into another entry`);
        case 'restore': await api(`entries/${e.id}/restore`, { method: 'POST' }); toast('Restored'); refreshSidebar(); return reload();
        case 'add-sol': return addSolution();
        case 'vote': {
          const r = await api(`solutions/${sid}/vote`, { method: sol.voted ? 'DELETE' : 'PUT' });
          sol.voted = r.voted; sol.works_for = r.works_for;
          b.classList.toggle('on', r.voted);
          b.innerHTML = String(html`${icon('thumb', 'sm')}Worked for me${r.works_for ? html` · <b>${r.works_for}</b>` : ''}`);
          return;
        }
        case 'accept': await api(`solutions/${sid}/accept`, { method: 'PUT' }); toast('Solution accepted'); refreshSidebar(); return reload();
        case 'sol-more':
          return popMenu(b, [
            { label: 'Edit', icon: 'edit', act: () => editSolution(sol) },
            sol.accepted ? { label: 'Unaccept', icon: 'x', act: async () => { await api(`solutions/${sid}/accept`, { method: 'DELETE' }); reload(); } }
                         : { label: 'Accept', icon: 'check', act: async () => { await api(`solutions/${sid}/accept`, { method: 'PUT' }); reload(); } },
            'sep',
            { label: 'Delete', icon: 'trash', danger: true, act: async () => {
              if (!(await confirmDialog('Delete this solution?', { ok: 'Delete', danger: true }))) return;
              await api(`solutions/${sid}`, { method: 'DELETE' }); toast('Solution deleted'); reload();
            } },
          ]);
        case 'add-link': return addLink();
        case 'unlink': await api(`entries/${e.id}/links`, { method: 'DELETE', query: { to: b.dataset.to, rel: b.dataset.rel } }); return reload();
        case 'upload': return $('#att-input').click();
        case 'del-att':
          if (!(await confirmDialog('Delete this attachment? Links to it in the text will break.', { ok: 'Delete', danger: true }))) return;
          await api(`attachments/${b.dataset.id}`, { method: 'DELETE' }); return reload();
      }
    } catch (err) { fail(err); }
  };
  const onChange = async (ev) => {
    if (ev.target.id === 'att-input') { upload([...ev.target.files]); return; }
    if (ev.target.dataset.act === 'status') {
      try { e = { ...e, ...(await api(`entries/${e.id}`, { method: 'PATCH', body: { status: ev.target.value, version: e.version, note: 'Status → ' + ev.target.value } })) }; toast('Status updated'); refreshSidebar(); render(); loadSimilar(); }
      catch (err) { fail(err); }
    }
  };
  const onKey = (ev) => {
    if (ev.target.closest('input, textarea, select') || ev.metaKey || ev.ctrlKey || ev.altKey || $('.overlay')) return;
    if (ev.key === 'e' && canEdit() && !e.deleted_at) { ev.preventDefault(); navigate(`/e/${e.id}/edit`); }
    if (ev.key === 's') { ev.preventDefault(); toggleStar(); }
  };
  document.addEventListener('click', onClick);
  document.addEventListener('change', onChange);
  document.addEventListener('keydown', onKey);
  return () => {
    document.removeEventListener('click', onClick);
    document.removeEventListener('change', onChange);
    document.removeEventListener('keydown', onKey);
  };
}
