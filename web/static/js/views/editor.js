// Create / edit an entry.
import { html, raw, icon, mount, api, $, $$, on, debounce, toast, fail, esc, confirmDialog } from '../util.js';
import { state, kindOf } from '../state.js';
import { setCrumbs, setTitle, navigate, refreshSidebar, setLeaveGuard } from '../app.js';
import { createMdEditor } from '../mdeditor.js';
import { spaceOptions } from '../components.js';

const DRAFT_KEY = 'fundus.draft.new';

export default async function ({ el, params, query }) {
  const editing = params[0] ? +params[0] : null;
  let orig = null;
  let e;
  if (editing) {
    orig = await api(`entries/${editing}`, { query: { render: 'false' } });
    e = structuredClone(orig);
  } else {
    let src = null;
    if (query.from) src = await api(`entries/${query.from}`, { query: { render: 'false' } }).catch(() => null);
    const k = kindOf(src?.kind || query.kind || (state.kindMap.problem ? 'problem' : state.kinds[0]?.key));
    e = {
      kind: k.key, title: src ? src.title + ' (copy)' : query.title || '', summary: src?.summary || '', body: src ? src.body : k.template || '',
      error_text: src?.error_text || '', status: src?.status || k.statuses[0] || '', tags: src ? [...src.tags] : (query.tag ? [query.tag] : []),
      fields: src ? { ...src.fields } : {}, space_id: src?.space_id || (query.space ? state.spaces.find((s) => s.slug === query.space)?.id : null) || null,
    };
  }
  let dirty = false;
  let saving = false;
  const kind = () => kindOf(e.kind);

  setTitle(editing ? 'Edit · ' + e.title : 'New ' + kind().label.toLowerCase());
  setCrumbs(editing ? [{ label: kind().plural, href: '/entries?kind=' + e.kind }, { label: e.title, href: '/e/' + editing }, { label: 'Edit' }] : [{ label: 'New entry' }]);

  mount(el, html`
    <div class="editor-layout">
      <div>
        ${!editing ? html`<div class="seg" id="kinds" style="margin:8px 0 18px">${state.kinds.map((k) => html`<button type="button" data-kind="${k.key}" style="--c:${k.color}"><span class="dot" style="--c:${k.color}"></span>${k.label}</button>`)}</div>` : ''}
        <div id="restore"></div>
        <textarea class="title-input" id="title" rows="1" placeholder="Title"></textarea>
        <textarea class="summary-input" id="summary" rows="1" placeholder="One-line summary (optional)"></textarea>
        <div id="similar"></div>
        <div id="error-wrap" style="margin-bottom:18px">
          <div class="row" style="margin-bottom:6px"><span class="label">${icon('warn', 'sm')} Error message / log excerpt</span><span class="faint small">verbatim — it is matched when someone pastes the same error into search</span></div>
          <textarea class="textarea mono" id="error" rows="3" placeholder="Paste the exact error output here…" spellcheck="false"></textarea>
        </div>
        <div id="body-editor"></div>
        ${!editing ? html`<div id="sol-wrap" style="margin-top:22px">
          <div class="row" style="margin-bottom:8px"><span class="label">${icon('bulb', 'sm')} Solution</span><span class="faint small">optional — already know the fix? It becomes the accepted solution.</span></div>
          <div id="sol-editor"></div></div>` : ''}
        ${editing ? html`<div class="field" style="margin-top:18px"><label>What changed? <span class="faint">(optional, shown in the history)</span></label><input class="input" id="note" placeholder="e.g. Added root cause"></div>` : ''}
        <div class="save-bar">
          <button class="btn primary" id="save">${icon('check')}${editing ? 'Save changes' : 'Create entry'}</button>
          <a class="btn ghost" href="${editing ? '/e/' + editing : '/'}" id="cancel">Cancel</a>
          <span class="faint small" id="save-state"></span>
          <span class="spacer"></span><span class="faint small"><kbd>Ctrl</kbd> <kbd>S</kbd> save · <kbd>Ctrl</kbd> <kbd>↵</kbd> save & close</span>
        </div>
      </div>
      <aside class="rail" style="top:64px">
        <div class="card">
          ${editing ? html`<div class="field"><label>Kind</label><select class="select" id="kind-sel">${state.kinds.map((k) => html`<option value="${k.key}" ${k.key === e.kind ? 'selected' : ''}>${k.label}</option>`)}</select></div>` : ''}
          <div class="field" id="status-field"><label>Status</label><select class="select" id="status"></select></div>
          <div class="field"><label>Space</label><select class="select" id="space">${spaceOptions(e.space_id)}</select></div>
          <div class="field"><label>Tags</label><div class="tag-input" id="tags"><input placeholder="Add tag…" autocomplete="off"></div><span class="hint">Enter or comma to add · Backspace removes</span></div>
          <div class="field" style="margin-bottom:6px"><label>Fields</label><div id="fields"></div>
            <button type="button" class="btn ghost sm" id="add-field" style="align-self:flex-start">${icon('plus', 'sm')}Add field</button></div>
          ${!editing ? html`<label class="check small" style="margin-top:8px"><input type="checkbox" id="pin">Pin to home page</label>` : ''}
        </div>
      </aside>
    </div>`);

  // ---- basic inputs
  const title = $('#title'), summary = $('#summary'), errorTa = $('#error');
  const autogrow = (t) => { t.style.height = 'auto'; t.style.height = t.scrollHeight + 'px'; };
  title.value = e.title; summary.value = e.summary; errorTa.value = e.error_text;
  [title, summary].forEach((t) => {
    t.addEventListener('input', () => autogrow(t));
    t.addEventListener('keydown', (ev) => { if (ev.key === 'Enter') { ev.preventDefault(); (t === title ? summary : body).focus(); } });
    requestAnimationFrame(() => autogrow(t));
  });

  const markDirty = () => { dirty = true; $('#save-state').textContent = 'Unsaved changes'; saveDraft(); };
  el.addEventListener('input', markDirty);
  el.addEventListener('change', markDirty);

  const body = createMdEditor($('#body-editor'), { value: e.body, entryId: editing });
  const sol = !editing ? createMdEditor($('#sol-editor'), { minHeight: 140, mode: 'write', placeholder: 'What fixed it?' }) : null;
  body.textarea.addEventListener('input', markDirty);

  // ---- kind-dependent parts
  const drawKind = () => {
    const k = kind();
    $$('#kinds [data-kind]').forEach((b) => b.classList.toggle('on', b.dataset.kind === k.key));
    $('#status-field').classList.toggle('hidden', !k.statuses.length);
    if (!k.statuses.includes(e.status)) e.status = k.statuses[0] || '';
    $('#status').innerHTML = k.statuses.map((s) => String(html`<option ${s === e.status ? 'selected' : ''}>${s}</option>`)).join('');
    const showErr = k.key === 'problem' || !!errorTa.value.trim();
    $('#error-wrap').classList.toggle('hidden', !showErr);
    $('#sol-wrap')?.classList.toggle('hidden', !k.has_solutions);
    title.placeholder = k.key === 'problem' ? 'What went wrong? e.g. “Docker: permission denied on /var/run/docker.sock”' : `${k.label} title`;
    drawFields();
  };
  on(el, 'click', '[data-kind]', (ev, b) => {
    const prevTemplate = kind().template || '';
    e.kind = b.dataset.kind;
    // swap the template if the body is still the untouched template of the previous kind
    if (body.value.trim() === prevTemplate.trim()) body.value = kind().template || '';
    drawKind();
    setTitle('New ' + kind().label.toLowerCase());
  });
  $('#kind-sel')?.addEventListener('change', (ev) => { e.kind = ev.target.value; drawKind(); });
  $('#status').addEventListener('change', (ev) => (e.status = ev.target.value));

  // ---- fields (key/value)
  const drawFields = () => {
    const sugg = kind().fields || [];
    const keys = [...new Set([...sugg.map((f) => f.key), ...Object.keys(e.fields)])];
    mount($('#fields'), html`${keys.map((k) => {
      const s = sugg.find((f) => f.key === k);
      return html`<div class="kv-row"><input class="input" data-fk value="${s ? s.label : k}" ${s ? raw('readonly tabindex="-1" style="background:var(--surface-2)"') : ''} data-key="${k}">
        <input class="input" data-fv="${k}" value="${e.fields[k] || ''}" placeholder="${s?.placeholder || ''}">
        ${s ? html`<span></span>` : html`<button type="button" class="btn ghost sm icon" data-rmf="${k}">${icon('x', 'sm')}</button>`}</div>`;
    })}`);
  };
  on($('#fields'), 'input', '[data-fv]', (ev, i) => { e.fields[i.dataset.fv] = i.value; });
  on($('#fields'), 'change', '[data-fk]:not([readonly])', (ev, i) => {
    const old = i.dataset.key, nk = i.value.trim().toLowerCase().replace(/\s+/g, '_');
    if (!nk || nk === old) return;
    e.fields[nk] = e.fields[old] || '';
    delete e.fields[old];
    drawFields();
  });
  on($('#fields'), 'click', '[data-rmf]', (ev, b) => { delete e.fields[b.dataset.rmf]; drawFields(); markDirty(); });
  $('#add-field').onclick = () => {
    let n = 'field', i = 1;
    while (n in e.fields) n = 'field' + ++i;
    e.fields[n] = '';
    drawFields();
    $$('#fields [data-fk]:not([readonly])').pop()?.select();
  };

  // ---- tags with autocomplete
  const tagBox = $('#tags'), tagIn = $('input', tagBox);
  let sugg = null, suggSel = 0;
  const drawTags = () => {
    $$('.chip', tagBox).forEach((c) => c.remove());
    tagIn.insertAdjacentHTML('beforebegin', e.tags.map((t) => String(html`<span class="chip tag">${t}<button type="button" data-rmt="${t}" aria-label="Remove">${icon('x', 'sm')}</button></span>`)).join(''));
  };
  const addTag = (t) => {
    t = t.trim().toLowerCase().replace(/^#/, '').replace(/\s+/g, '-');
    if (t && !e.tags.includes(t)) { e.tags.push(t); drawTags(); markDirty(); }
    tagIn.value = '';
    closeSugg();
  };
  const closeSugg = () => { sugg?.remove(); sugg = null; };
  const suggest = debounce(async () => {
    const p = tagIn.value.trim().toLowerCase();
    if (!p) { closeSugg(); return; }
    const list = (await api('tags', { query: { prefix: p, limit: 8 } })).filter((t) => !e.tags.includes(t.key));
    closeSugg();
    if (!list.length) return;
    suggSel = 0;
    sugg = document.createElement('div');
    sugg.className = 'suggest';
    sugg.innerHTML = list.map((t, i) => String(html`<button type="button" data-t="${t.key}" class="${i === 0 ? 'on' : ''}">#${t.key}<span class="n">${t.count}</span></button>`)).join('');
    tagBox.append(sugg);
  }, 120);
  tagBox.addEventListener('click', (ev) => {
    const rm = ev.target.closest('[data-rmt]');
    if (rm) { e.tags = e.tags.filter((t) => t !== rm.dataset.rmt); drawTags(); markDirty(); return; }
    const s = ev.target.closest('[data-t]');
    if (s) { addTag(s.dataset.t); tagIn.focus(); return; }
    tagIn.focus();
  });
  tagIn.addEventListener('input', () => { if (tagIn.value.includes(',')) tagIn.value.split(',').forEach(addTag); else suggest(); });
  tagIn.addEventListener('keydown', (ev) => {
    const items = sugg ? $$('[data-t]', sugg) : [];
    if (ev.key === 'Enter') { ev.preventDefault(); if (items[suggSel]) addTag(items[suggSel].dataset.t); else addTag(tagIn.value); }
    else if (ev.key === 'ArrowDown' && items.length) { ev.preventDefault(); suggSel = (suggSel + 1) % items.length; items.forEach((b, i) => b.classList.toggle('on', i === suggSel)); }
    else if (ev.key === 'ArrowUp' && items.length) { ev.preventDefault(); suggSel = (suggSel - 1 + items.length) % items.length; items.forEach((b, i) => b.classList.toggle('on', i === suggSel)); }
    else if (ev.key === 'Backspace' && !tagIn.value && e.tags.length) { e.tags.pop(); drawTags(); markDirty(); }
    else if (ev.key === 'Escape') closeSugg();
  });
  tagIn.addEventListener('blur', () => setTimeout(() => { if (tagIn.value.trim()) addTag(tagIn.value); closeSugg(); }, 150));

  // ---- duplicate detection
  const checkSimilar = debounce(async () => {
    const t = title.value.trim(), err = errorTa.value.trim();
    if (t.length < 6 && err.length < 10) { mount($('#similar'), ''); return; }
    try {
      const list = await api('similar', { query: { title: t, error: err, exclude: editing || undefined, limit: 4 } });
      const good = list.filter((x) => x.rank > 0.5);
      mount($('#similar'), good.length ? html`<div class="similar-box"><h4>${icon('warn', 'sm')}Is this already documented?</h4>
        ${good.map((x) => html`<a href="/e/${x.id}" target="_blank"><span class="dot" style="--c:${kindOf(x.kind).color}"></span><span class="ellipsis grow">${x.title}</span>${x.status ? html`<span class="chip status status-${x.status}">${x.status}</span>` : ''}${icon('external', 'sm')}</a>`)}</div>` : '');
    } catch {}
  }, 500);
  title.addEventListener('input', checkSimilar);
  errorTa.addEventListener('input', checkSimilar);

  // ---- drafts (new entries only)
  const collect = () => ({
    kind: e.kind, title: title.value.trim(), summary: summary.value.trim(), body: body.value, error_text: errorTa.value,
    status: kind().statuses.length ? $('#status').value : '', tags: e.tags,
    fields: Object.fromEntries(Object.entries(e.fields).filter(([, v]) => String(v).trim())),
    space_id: +$('#space').value || 0,
  });
  const saveDraft = debounce(() => {
    if (editing) return;
    try { localStorage.setItem(DRAFT_KEY, JSON.stringify({ ...collect(), solution: sol?.value || '', at: Date.now() })); } catch {}
  }, 600);
  const clearDraft = () => { try { localStorage.removeItem(DRAFT_KEY); } catch {} };
  if (!editing && !query.from && !query.title) {
    let d = null;
    try { d = JSON.parse(localStorage.getItem(DRAFT_KEY) || 'null'); } catch {}
    if (d && (d.title || d.body.trim() !== (kindOf(d.kind).template || '').trim())) {
      mount($('#restore'), html`<div class="alert info row">${icon('history', 'sm')}<span class="grow">You have an unsaved draft${d.title ? html`: <b>${d.title}</b>` : ''}.</span>
        <button class="btn sm" id="restore-yes">Restore</button><button class="btn sm ghost" id="restore-no">Discard</button></div>`);
      $('#restore-yes').onclick = () => {
        Object.assign(e, { kind: d.kind, tags: d.tags || [], fields: d.fields || {}, status: d.status });
        title.value = d.title; summary.value = d.summary; errorTa.value = d.error_text; body.value = d.body;
        if (sol) sol.value = d.solution || '';
        $('#space').value = d.space_id || 0;
        [title, summary].forEach(autogrow);
        drawKind(); drawTags(); mount($('#restore'), '');
      };
      $('#restore-no').onclick = () => { clearDraft(); mount($('#restore'), ''); };
    }
  }

  // ---- save
  const save = async (close = false, force = false) => {
    if (saving) return;
    const data = collect();
    if (!data.title) { toast('A title is required', 'error'); title.focus(); return; }
    saving = true;
    $('#save').disabled = true;
    $('#save-state').textContent = 'Saving…';
    try {
      let saved;
      if (editing) {
        const patch = { ...data, note: $('#note').value.trim() };
        if (!force) patch.version = orig.version;
        saved = await api(`entries/${editing}`, { method: 'PATCH', body: patch });
        orig = saved;
        $('#note').value = '';
      } else {
        const payload = { ...data, pinned: $('#pin')?.checked || false };
        if (!payload.space_id) delete payload.space_id;
        if (sol && sol.value.trim() && kind().has_solutions) payload.solutions = [{ body: sol.value, accepted: true }];
        saved = await api('entries', { method: 'POST', body: payload });
        clearDraft();
      }
      dirty = false;
      refreshSidebar();
      if (!editing || close) { toast(editing ? 'Saved' : 'Entry created'); navigate('/e/' + saved.id, { replace: !editing }); return; }
      $('#save-state').textContent = 'Saved ' + new Date().toLocaleTimeString();
      toast('Saved');
    } catch (err) {
      $('#save-state').textContent = '';
      if (err.code === 'version_conflict') {
        if (await confirmDialog(err.message + ' Overwrite their changes with yours? (Their version stays in the history.)', { title: 'Edit conflict', ok: 'Overwrite', danger: true })) {
          saving = false; $('#save').disabled = false;
          return save(close, true);
        }
      } else fail(err);
    } finally {
      saving = false;
      $('#save') && ($('#save').disabled = false);
    }
  };
  $('#save').onclick = () => save(true);

  const onKey = (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 's') { ev.preventDefault(); save(false); }
    if ((ev.ctrlKey || ev.metaKey) && ev.key === 'Enter') { ev.preventDefault(); save(true); }
  };
  const beforeUnload = (ev) => { if (dirty) { ev.preventDefault(); ev.returnValue = ''; } };
  document.addEventListener('keydown', onKey);
  window.addEventListener('beforeunload', beforeUnload);
  setLeaveGuard(() => !dirty || confirm('Discard unsaved changes?'));

  drawKind();
  drawTags();
  if (!editing && !e.title) title.focus(); else if (!editing) body.focus();

  return () => {
    document.removeEventListener('keydown', onKey);
    window.removeEventListener('beforeunload', beforeUnload);
  };
}
