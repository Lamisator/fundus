// Markdown editor: toolbar, shortcuts, list continuation, paste/drop
// uploads, live preview and an entry picker for [[id]] links.
import { html, icon, api, debounce, modal, esc, raw, fail, toast } from './util.js';
import { kindIcon } from './state.js';
import { enhanceProse } from './components.js';

export function createMdEditor(host, { value = '', entryId = null, minHeight = 380, placeholder = 'Write in Markdown…', onChange, onSubmit, mode } = {}) {
  let viewMode = mode || (() => { try { return localStorage.getItem('fundus.editorMode') || 'write'; } catch { return 'write'; } })();
  host.innerHTML = String(html`
    <div class="md-editor">
      <div class="md-toolbar">
        <button type="button" class="btn ghost" data-md="h" title="Heading">${icon('heading')}</button>
        <button type="button" class="btn ghost" data-md="b" title="Bold (Ctrl+B)">${icon('bold')}</button>
        <button type="button" class="btn ghost" data-md="i" title="Italic (Ctrl+I)">${icon('italic')}</button>
        <button type="button" class="btn ghost" data-md="code" title="Inline code">${icon('code')}</button>
        <span class="sep"></span>
        <button type="button" class="btn ghost" data-md="ul" title="Bullet list">${icon('list')}</button>
        <button type="button" class="btn ghost" data-md="ol" title="Numbered list">${icon('listOl')}</button>
        <button type="button" class="btn ghost" data-md="task" title="Checklist">${icon('listChecks')}</button>
        <button type="button" class="btn ghost" data-md="quote" title="Quote / callout">${icon('quote')}</button>
        <button type="button" class="btn ghost" data-md="fence" title="Code block">${icon('terminal')}</button>
        <button type="button" class="btn ghost" data-md="table" title="Table">${icon('table')}</button>
        <span class="sep"></span>
        <button type="button" class="btn ghost" data-md="link" title="Link (Ctrl+K)">${icon('link')}</button>
        <button type="button" class="btn ghost" data-md="ref" title="Link another entry ([[…]])">${icon('layers')}</button>
        <button type="button" class="btn ghost" data-md="file" title="Attach image or file">${icon('clip')}</button>
        <span class="spacer"></span>
        <div class="seg" style="padding:2px">
          <button type="button" data-mode="write" title="Write">${icon('edit', 'sm')}</button>
          <button type="button" data-mode="split" title="Side by side">${icon('columns', 'sm')}</button>
          <button type="button" data-mode="preview" title="Preview">${icon('eye', 'sm')}</button>
        </div>
      </div>
      <div class="md-panes">
        <textarea class="md-input" spellcheck="true" placeholder="${placeholder}" style="min-height:${minHeight}px"></textarea>
        <div class="md-preview prose" style="min-height:${minHeight}px"></div>
      </div>
      <div class="md-status"><span>Markdown · <code>[[id]]</code> links entries · paste or drop images</span><span class="spacer"></span><span class="wc"></span></div>
      <input type="file" multiple hidden>
    </div>`);
  const root = host.querySelector('.md-editor');
  const ta = root.querySelector('textarea');
  const pv = root.querySelector('.md-preview');
  const wc = root.querySelector('.wc');
  const fileInput = root.querySelector('input[type=file]');
  ta.value = value;

  const changed = () => {
    const words = ta.value.trim() ? ta.value.trim().split(/\s+/).length : 0;
    wc.textContent = `${words} words`;
    onChange?.(ta.value);
    if (viewMode !== 'write') renderPreview();
  };
  const renderPreview = debounce(async () => {
    try {
      const { html: h } = await api('render', { method: 'POST', body: { markdown: ta.value } });
      pv.innerHTML = h || '<p class="faint">Nothing to preview.</p>';
      enhanceProse(pv);
    } catch {}
  }, 250);

  const setMode = (m, focus = true) => {
    viewMode = m;
    try { localStorage.setItem('fundus.editorMode', m); } catch {}
    root.classList.toggle('split', m === 'split');
    root.classList.toggle('preview-only', m === 'preview');
    root.querySelectorAll('[data-mode]').forEach((b) => b.classList.toggle('on', b.dataset.mode === m));
    if (m !== 'write') renderPreview();
    if (m !== 'preview' && focus) ta.focus();
  };

  // ---- text manipulation
  const sel = () => ({ s: ta.selectionStart, e: ta.selectionEnd, v: ta.value });
  const replace = (s, e, text, selStart, selEnd) => {
    ta.focus();
    ta.setSelectionRange(s, e);
    // execCommand keeps the browser's undo stack intact
    if (!document.execCommand('insertText', false, text)) ta.setRangeText(text, s, e, 'end');
    ta.setSelectionRange(selStart ?? s + text.length, selEnd ?? selStart ?? s + text.length);
    changed();
  };
  const wrap = (before, after = before, ph = 'text') => {
    const { s, e, v } = sel();
    const inner = v.slice(s, e) || ph;
    replace(s, e, before + inner + after, s + before.length, s + before.length + inner.length);
  };
  const linePrefix = (fn) => {
    const { s, e, v } = sel();
    const ls = v.lastIndexOf('\n', s - 1) + 1;
    let le = v.indexOf('\n', e);
    if (le === -1) le = v.length;
    const lines = v.slice(ls, le).split('\n');
    const out = lines.map((l, i) => fn(l, i)).join('\n');
    replace(ls, le, out, ls, ls + out.length);
  };
  const insertBlock = (text, cursorOffset) => {
    const { s, e, v } = sel();
    const pre = s > 0 && v[s - 1] !== '\n' ? '\n\n' : (s > 1 && v[s - 2] !== '\n' ? '\n' : '');
    const t = pre + text;
    replace(s, e, t, s + pre.length + (cursorOffset ?? text.length));
  };
  const actions = {
    h: () => linePrefix((l) => (l.match(/^#{1,5} /) ? '#' + l : l.match(/^###### /) ? l.slice(7) : '## ' + l)),
    b: () => wrap('**'),
    i: () => wrap('_'),
    code: () => wrap('`', '`', 'code'),
    ul: () => linePrefix((l) => (l.startsWith('- ') ? l.slice(2) : '- ' + l)),
    ol: () => linePrefix((l, i) => (/^\d+\. /.test(l) ? l.replace(/^\d+\. /, '') : `${i + 1}. ${l}`)),
    task: () => linePrefix((l) => (l.startsWith('- [ ] ') ? l.slice(6) : '- [ ] ' + l.replace(/^- /, ''))),
    quote: () => linePrefix((l) => (l.startsWith('> ') ? l.slice(2) : '> ' + l)),
    fence: () => {
      const { s, e, v } = sel();
      const inner = v.slice(s, e);
      insertBlock('```sh\n' + inner + '\n```\n', 6 + inner.length);
    },
    table: () => insertBlock('| Column | Column |\n| ------ | ------ |\n| Cell   | Cell   |\n', 2),
    link: () => {
      const { s, e, v } = sel();
      const t = v.slice(s, e) || 'link text';
      replace(s, e, `[${t}](https://)`, s + t.length + 3, s + t.length + 11);
    },
    ref: async () => {
      const ent = await pickEntry();
      if (ent) { const { s, e } = sel(); replace(s, e, `[[${ent.id}]]`); }
    },
    file: () => fileInput.click(),
  };

  root.querySelector('.md-toolbar').addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    if (b.dataset.md) actions[b.dataset.md]();
    if (b.dataset.mode) setMode(b.dataset.mode);
  });

  ta.addEventListener('input', changed);
  ta.addEventListener('keydown', (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.key === 'b') { e.preventDefault(); actions.b(); }
    else if (mod && e.key === 'i') { e.preventDefault(); actions.i(); }
    else if (mod && e.key === 'k') { e.preventDefault(); e.stopPropagation(); actions.link(); }
    else if (mod && e.key === 'Enter') { e.preventDefault(); onSubmit?.(); }
    else if (e.key === 'Tab' && !mod) {
      const { s, e: en, v } = sel();
      if (s === en && !e.shiftKey) { e.preventDefault(); replace(s, en, '    '); }
      else if (s !== en) { e.preventDefault(); linePrefix((l) => (e.shiftKey ? l.replace(/^( {1,4}|\t)/, '') : '    ' + l)); }
    } else if (e.key === 'Enter' && !e.shiftKey) {
      // continue lists
      const { s, v } = sel();
      const ls = v.lastIndexOf('\n', s - 1) + 1;
      const line = v.slice(ls, s);
      const m = line.match(/^(\s*)([-*+] \[[ xX]\] |[-*+] |(\d+)\. )/);
      if (!m) return;
      e.preventDefault();
      if (line.trim() === m[2].trim()) { replace(ls, s, ''); return; } // empty item ends the list
      let marker = m[2];
      if (m[3]) marker = `${+m[3] + 1}. `;
      else if (/\[[xX]\]/.test(marker)) marker = marker.replace(/\[[xX]\]/, '[ ]');
      replace(s, s, '\n' + m[1] + marker);
    }
  });

  // ---- uploads
  const upload = async (files) => {
    for (const f of files) {
      const ph = `![Uploading ${f.name}…]()`;
      const { s, e } = sel();
      replace(s, e, ph);
      const fd = new FormData();
      fd.append('file', f);
      if (entryId) fd.append('entry_id', entryId);
      try {
        const a = await api('attachments', { method: 'POST', raw: fd });
        const md = a.content_type.startsWith('image/') && a.content_type !== 'image/svg+xml'
          ? `![${a.filename}](${a.url})` : `[${a.filename}](${a.url})`;
        const i = ta.value.indexOf(ph);
        if (i >= 0) replace(i, i + ph.length, md);
        root.dispatchEvent(new CustomEvent('uploaded', { detail: a, bubbles: true }));
      } catch (err) {
        const i = ta.value.indexOf(ph);
        if (i >= 0) replace(i, i + ph.length, '');
        fail(err);
      }
    }
  };
  fileInput.addEventListener('change', () => { upload([...fileInput.files]); fileInput.value = ''; });
  ta.addEventListener('paste', (e) => {
    const files = [...(e.clipboardData?.files || [])];
    if (files.length) { e.preventDefault(); upload(files); }
  });
  root.addEventListener('dragover', (e) => { if (e.dataTransfer?.types.includes('Files')) { e.preventDefault(); root.classList.add('drag'); } });
  root.addEventListener('dragleave', () => root.classList.remove('drag'));
  root.addEventListener('drop', (e) => {
    root.classList.remove('drag');
    const files = [...(e.dataTransfer?.files || [])];
    if (files.length) { e.preventDefault(); upload(files); }
  });

  setMode(viewMode === 'preview' && !value ? 'write' : viewMode, false);
  changed();
  return {
    get value() { return ta.value; },
    set value(v) { ta.value = v; changed(); },
    focus: () => ta.focus(),
    textarea: ta,
  };
}

/** Modal to search for and pick an entry. Resolves to the entry or null. */
export function pickEntry({ title = 'Link an entry', exclude } = {}) {
  return new Promise((resolve) => {
    let picked = null;
    modal({
      title,
      body: html`<input class="input" placeholder="Search by title, error, tag…" autofocus><div class="col" style="margin-top:10px;gap:2px;max-height:340px;overflow:auto" data-list></div>`,
      onMount(el, close) {
        const inp = el.querySelector('input'), list = el.querySelector('[data-list]');
        let items = [], seq = 0;
        const search = debounce(async () => {
          const my = ++seq;
          const res = await api('entries', { query: { q: inp.value.trim(), limit: 12, sort: inp.value.trim() ? '' : 'updated' } });
          if (my !== seq) return;
          items = res.items.filter((e) => e.id !== exclude);
          list.innerHTML = items.map((e, i) => String(html`<a href="#" class="palette-item ${i === 0 ? 'on' : ''}" data-i="${i}">${kindIcon(e.kind)}<div class="grow"><div class="t ellipsis">${e.title}</div><div class="s">#${e.id}${e.status ? ' · ' + e.status : ''}</div></div></a>`)).join('')
            || '<p class="muted small">No matches.</p>';
        }, 150);
        inp.addEventListener('input', search);
        inp.addEventListener('keydown', (e) => { if (e.key === 'Enter' && items[0]) { e.preventDefault(); picked = items[0]; close(); } });
        list.addEventListener('click', (e) => {
          const a = e.target.closest('[data-i]');
          if (!a) return;
          e.preventDefault();
          picked = items[+a.dataset.i];
          close();
        });
        search();
      },
      onClose() { resolve(picked); },
    });
  });
}
