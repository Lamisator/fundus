// Reusable rendering pieces.
import { html, raw, icon, ago, esc } from './util.js';
import { kindIcon, kindOf, statusChip, spaceChip, state } from './state.js';

export function entryRow(e, { showSpace = true } = {}) {
  const sub = e.snippet ? raw(e.snippet) : e.summary;
  return html`<a class="entry-row" href="/e/${e.id}" data-id="${e.id}">
    ${kindIcon(e.kind)}
    <div class="grow">
      <div class="t">${e.pinned ? icon('pin', 'sm') : ''}<span>${e.title}</span>${e.starred ? html`<span style="color:var(--warn)">${icon('star', 'sm')}</span>` : ''}</div>
      ${sub ? html`<div class="s">${sub}</div>` : ''}
      <div class="m">
        ${statusChip(e.status)}
        ${e.has_accepted_solution ? html`<span class="solved-mark">${icon('checkCircle', 'sm')}solution</span>` :
          e.solution_count ? html`<span class="chip">${icon('bulb', 'sm')}${e.solution_count}</span>` : ''}
        ${showSpace && e.space_id ? spaceTag(e.space_id) : ''}
        ${e.tags.slice(0, 5).map((t) => html`<span class="chip tag">${t}</span>`)}
        ${e.tags.length > 5 ? html`<span class="faint small">+${e.tags.length - 5}</span>` : ''}
      </div>
    </div>
    <div class="r"><span title="${new Date(e.updated_at).toLocaleString()}">${ago(e.updated_at)}</span>
      <span>${kindOf(e.kind).label}</span></div>
  </a>`;
}

// Rows are links themselves, so chips inside must not be <a> (nested anchors break layout).
function spaceTag(id) {
  const s = state.spaceMap[id];
  return s ? html`<span class="chip"><span class="dot" style="--c:${s.color}"></span>${s.name}</span>` : '';
}

export function entryList(items, opts) {
  if (!items.length) return '';
  return html`<div class="card entry-list">${items.map((e) => entryRow(e, opts))}</div>`;
}

export function emptyState(ic, title, text, action = '') {
  return html`<div class="empty">${icon(ic)}<h3>${title}</h3><p>${text}</p>${action}</div>`;
}

// Turn GitHub-style "> [!NOTE]" blockquotes into callouts, add copy buttons
// to code blocks and open external links in a new tab.
export function enhanceProse(root) {
  const kinds = { NOTE: 'note', TIP: 'tip', IMPORTANT: 'important', WARNING: 'warning', CAUTION: 'caution' };
  const ics = { note: 'alert', tip: 'bulb', important: 'sparkle', warning: 'warn', caution: 'warn' };
  root.querySelectorAll('blockquote').forEach((bq) => {
    const p = bq.querySelector('p');
    const m = p && p.innerHTML.match(/^\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*(<br>)?\s*/i);
    if (!m) return;
    const k = kinds[m[1].toUpperCase()];
    p.innerHTML = p.innerHTML.slice(m[0].length);
    if (!p.innerHTML.trim()) p.remove();
    bq.classList.add('callout', 'callout-' + k);
    bq.insertAdjacentHTML('afterbegin', String(html`<div class="callout-title">${icon(ics[k], 'sm')}${k}</div>`));
  });
  root.querySelectorAll('pre').forEach((pre) => {
    if (pre.querySelector('.copy')) return;
    const b = document.createElement('button');
    b.className = 'btn sm copy';
    b.type = 'button';
    b.innerHTML = String(html`${icon('copy', 'sm')}Copy`);
    b.onclick = async () => {
      const code = pre.querySelector('code')?.innerText ?? pre.innerText;
      try { await navigator.clipboard.writeText(code.replace(/\n$/, '')); b.innerHTML = String(html`${icon('check', 'sm')}Copied`); }
      catch { b.textContent = 'Failed'; }
      setTimeout(() => (b.innerHTML = String(html`${icon('copy', 'sm')}Copy`)), 1600);
    };
    pre.append(b);
  });
  root.querySelectorAll('a[href^="http"]').forEach((a) => { a.target = '_blank'; a.rel = 'noopener noreferrer'; });
}

export function spaceOptions(selected) {
  return html`<option value="0">— No space —</option>${state.spaces.map((s) => html`<option value="${s.id}" ${s.id === selected ? raw('selected') : ''}>${s.name}</option>`)}`;
}

export { esc };
