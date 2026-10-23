// Shared helpers: safe HTML templating, API client, toasts, dialogs, icons.

// ------------------------------------------------------------ HTML

class Safe { constructor(s) { this.s = s; } toString() { return this.s; } }
export const raw = (s) => new Safe(String(s ?? ''));

export function esc(v) {
  return String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function interp(v) {
  if (v instanceof Safe) return v.s;
  if (Array.isArray(v)) return v.map(interp).join('');
  if (v === false || v === null || v === undefined) return '';
  return esc(v);
}

/** Tagged template: interpolations are escaped unless wrapped in raw() / html``. */
export function html(strings, ...vals) {
  let out = strings[0];
  for (let i = 0; i < vals.length; i++) out += interp(vals[i]) + strings[i + 1];
  return new Safe(out);
}

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

export function mount(el, content) {
  el.innerHTML = String(content);
  return el;
}

/** Delegated event: on(root, 'click', '[data-act=x]', fn). */
export function on(root, type, sel, fn) {
  root.addEventListener(type, (ev) => {
    const t = ev.target.closest(sel);
    if (t && root.contains(t)) fn(ev, t);
  });
}

// ------------------------------------------------------------ API

export class ApiError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}

export async function api(path, { method = 'GET', body, query, raw: rawBody, headers = {} } = {}) {
  let url = path.startsWith('/') ? path : '/api/v1/' + path;
  if (query) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) {
      if (v === undefined || v === null || v === '' || v === false) continue;
      if (Array.isArray(v)) v.forEach((x) => qs.append(k, x)); else qs.set(k, v);
    }
    const s = qs.toString();
    if (s) url += (url.includes('?') ? '&' : '?') + s;
  }
  const opts = { method, headers: { 'X-Requested-With': 'fundus', ...headers }, credentials: 'same-origin' };
  if (rawBody !== undefined) opts.body = rawBody;
  else if (body !== undefined) { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json'; }
  const res = await fetch(url, opts);
  if (res.status === 204) return null;
  const ct = res.headers.get('Content-Type') || '';
  const data = ct.includes('json') ? await res.json() : await res.text();
  if (!res.ok) {
    const e = data && data.error ? data.error : { code: 'http_' + res.status, message: typeof data === 'string' ? data : res.statusText };
    if (res.status === 401 && !path.includes('/auth/')) window.dispatchEvent(new CustomEvent('fundus:unauthorized'));
    throw new ApiError(res.status, e.code, e.message);
  }
  return data;
}

// ------------------------------------------------------------ time & text

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
export function ago(ts) {
  if (!ts) return '';
  const d = (new Date(ts) - Date.now()) / 1000;
  const a = Math.abs(d);
  if (a < 45) return 'just now';
  if (a < 3600) return rtf.format(Math.round(d / 60), 'minute');
  if (a < 86400) return rtf.format(Math.round(d / 3600), 'hour');
  if (a < 86400 * 30) return rtf.format(Math.round(d / 86400), 'day');
  if (a < 86400 * 365) return rtf.format(Math.round(d / (86400 * 30)), 'month');
  return rtf.format(Math.round(d / (86400 * 365)), 'year');
}
export function fmtDate(ts, time = false) {
  if (!ts) return '';
  const o = { year: 'numeric', month: 'short', day: 'numeric' };
  if (time) { o.hour = '2-digit'; o.minute = '2-digit'; }
  return new Date(ts).toLocaleString(undefined, o);
}
export function bytes(n) {
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i];
}
export const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + 's'}`;
export function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}
export function initials(u) {
  const n = (u?.display_name || u?.username || '?').trim();
  const p = n.split(/[\s._-]+/).filter(Boolean);
  return ((p[0]?.[0] || '') + (p[1]?.[0] || '')).toUpperCase() || n[0].toUpperCase();
}
export function hue(s) {
  let h = 0;
  for (const c of String(s || '')) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}
export const avatar = (u, cls = '') => html`<span class="avatar ${cls}" style="--h:${hue(u?.username)}" title="${u?.display_name || u?.username || ''}">${initials(u)}</span>`;
export const userName = (u) => u ? (u.display_name || u.username) : 'someone';

export async function copy(text, what = 'Copied to clipboard') {
  try { await navigator.clipboard.writeText(text); toast(what); }
  catch { toast('Copy failed — your browser blocked clipboard access', 'error'); }
}

// ------------------------------------------------------------ toasts

export function toast(msg, kind = 'ok') {
  const el = document.createElement('div');
  el.className = 'toast ' + (kind === 'error' ? 'error' : '');
  el.innerHTML = String(html`${icon(kind === 'error' ? 'alert' : 'check')}<span>${msg}</span>`);
  document.getElementById('toasts').append(el);
  setTimeout(() => { el.style.transition = 'opacity .3s'; el.style.opacity = '0'; }, kind === 'error' ? 5000 : 2600);
  setTimeout(() => el.remove(), kind === 'error' ? 5400 : 3000);
}
export const fail = (e) => toast(e?.message || String(e), 'error');

// ------------------------------------------------------------ dialogs

/** Open a modal. Returns {el, close}. content is HTML; onMount(el, close). */
export function modal({ title, body, foot = '', wide = false, onMount, onClose, cls = '' }) {
  const ov = document.createElement('div');
  ov.className = 'overlay';
  ov.innerHTML = String(html`<div class="modal ${cls}" role="dialog" aria-modal="true" style="${wide ? 'max-width:760px' : ''}">
    ${title ? html`<div class="modal-head"><h2>${title}</h2><span class="spacer"></span><button class="btn ghost sm icon" data-close aria-label="Close">${icon('x')}</button></div>` : ''}
    <div class="modal-body">${raw(body)}</div>
    ${foot ? html`<div class="modal-foot">${raw(foot)}</div>` : ''}
  </div>`);
  const prev = document.activeElement;
  let closed = false;
  const close = (v) => {
    if (closed) return;
    closed = true;
    ov.remove();
    document.removeEventListener('keydown', key, true);
    prev?.focus?.();
    onClose?.(v);
  };
  const key = (e) => { if (e.key === 'Escape') { e.stopPropagation(); close(); } };
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(); });
  on(ov, 'click', '[data-close]', () => close());
  document.addEventListener('keydown', key, true);
  document.body.append(ov);
  onMount?.(ov.querySelector('.modal'), close);
  setTimeout(() => ov.querySelector('[autofocus], input, textarea, select')?.focus(), 10);
  return { el: ov, close };
}

export function confirmDialog(message, { title = 'Are you sure?', ok = 'Confirm', danger = false } = {}) {
  return new Promise((resolve) => {
    modal({
      title,
      body: html`<p style="margin:0;color:var(--text-2)">${message}</p>`,
      foot: html`<button class="btn" data-close>Cancel</button><button class="btn ${danger ? 'danger' : 'primary'}" data-ok>${ok}</button>`,
      onMount(el, close) {
        const b = el.querySelector('[data-ok]');
        b.focus();
        b.onclick = () => { resolve(true); close(true); };
      },
      onClose(v) { if (v !== true) resolve(false); },
    });
  });
}

export function promptDialog(label, { title = '', value = '', ok = 'Save', placeholder = '', hint = '' } = {}) {
  return new Promise((resolve) => {
    modal({
      title,
      body: html`<div class="field" style="margin:0"><label>${label}</label><input class="input" value="${value}" placeholder="${placeholder}" autofocus>${hint ? html`<span class="hint">${hint}</span>` : ''}</div>`,
      foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-ok>${ok}</button>`,
      onMount(el, close) {
        const inp = el.querySelector('input');
        const done = () => { resolve(inp.value); close(true); };
        el.querySelector('[data-ok]').onclick = done;
        inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') done(); });
        setTimeout(() => inp.select(), 20);
      },
      onClose(v) { if (v !== true) resolve(null); },
    });
  });
}

/** Small popup menu anchored at an element. items: [{label, icon, act, danger}] or 'sep'. */
export function popMenu(anchor, items) {
  document.querySelectorAll('.menu').forEach((m) => m.remove());
  const m = document.createElement('div');
  m.className = 'menu';
  m.innerHTML = items.map((it, i) => it === 'sep' ? '<hr>' :
    String(html`<button data-i="${i}" class="${it.danger ? 'danger' : ''}">${it.icon ? icon(it.icon) : ''}${it.label}</button>`)).join('');
  document.body.append(m);
  const r = anchor.getBoundingClientRect();
  const w = m.offsetWidth, hgt = m.offsetHeight;
  m.style.left = Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8)) + 'px';
  m.style.top = (r.bottom + hgt + 6 > window.innerHeight ? r.top - hgt - 6 : r.bottom + 6) + 'px';
  const close = () => { m.remove(); document.removeEventListener('mousedown', out, true); };
  const out = (e) => { if (!m.contains(e.target)) close(); };
  setTimeout(() => document.addEventListener('mousedown', out, true));
  m.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-i]');
    if (!b) return;
    close();
    items[+b.dataset.i].act();
  });
}

// ------------------------------------------------------------ icons

const P = {
  search: '<circle cx="11" cy="11" r="7.5"/><path d="m20.5 20.5-4.2-4.2"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  home: '<path d="M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z"/>',
  bug: '<rect x="8" y="6" width="8" height="14" rx="4"/><path d="M12 20v-9M8 13H4M20 13h-4M8 9 5 7M16 9l3-2M8 17l-3 2M16 17l3 2M9.2 6.4a3 3 0 0 1 5.6 0"/>',
  compass: '<circle cx="12" cy="12" r="9.5"/><path d="m15.5 8.5-2 5-5 2 2-5z"/>',
  terminal: '<path d="m4 17 6-6-6-6M12 19h8"/>',
  code: '<path d="m16 18 6-6-6-6M8 6l-6 6 6 6"/>',
  note: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6M8 13h8M8 17h5"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  star: '<path d="m12 2.8 2.8 5.8 6.3.9-4.6 4.4 1.1 6.3L12 17.2l-5.6 3 1.1-6.3L2.9 9.5l6.3-.9z"/>',
  tag: '<path d="M12.6 2.6A2 2 0 0 0 11.2 2H4a2 2 0 0 0-2 2v7.2a2 2 0 0 0 .6 1.4l8.7 8.7a2.4 2.4 0 0 0 3.4 0l6.6-6.6a2.4 2.4 0 0 0 0-3.4z"/><circle cx="7.5" cy="7.5" r="1.3"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1.5 14h5M9.5 8h5M17.5 16h5"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  login: '<path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4M10 17l5-5-5-5M15 12H3"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
  monitor: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>',
  edit: '<path d="M17 3a2.8 2.8 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5z"/>',
  trash: '<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6M10 11v6M14 11v6"/>',
  history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3.5 2"/>',
  share: '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><path d="m8.6 13.5 6.8 4M15.4 6.5l-6.8 4"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  checkCircle: '<circle cx="12" cy="12" r="9.5"/><path d="m8.5 12 2.5 2.5 4.5-5"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
  upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
  clip: '<path d="m21.4 11.1-9.2 9.2a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5"/>',
  pin: '<path d="M12 17v5M9 3h6M10 3v7l-4 4v2h12v-2l-4-4V3"/>',
  key: '<circle cx="7.5" cy="15.5" r="5"/><path d="m11 12 10-10M16 7l3 3M19 4l2 2"/>',
  passkey: '<circle cx="9" cy="7" r="4"/><path d="M2 21v-1a6 6 0 0 1 9.5-4.9"/><circle cx="18" cy="13" r="2.5"/><path d="M18 15.5V22M18 19.5h2.2"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  users: '<circle cx="9" cy="8" r="4"/><path d="M2 21a7 7 0 0 1 14 0M16 3.1a4 4 0 0 1 0 7.8M22 21a7 7 0 0 0-5-6.7"/>',
  activity: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  bolt: '<path d="M13 2 3 14h9l-1 8 10-12h-9z"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01"/>',
  bold: '<path d="M6 4h8a4 4 0 0 1 0 8H6zM6 12h9a4 4 0 0 1 0 8H6z"/>',
  italic: '<path d="M19 4h-9M14 20H5M15 4 9 20"/>',
  heading: '<path d="M6 4v16M18 4v16M6 12h12"/>',
  quote: '<path d="M7 7h4v4H7zM7 11c0 3-1 5-3 6M15 7h4v4h-4zM15 11c0 3-1 5-3 6"/>',
  listChecks: '<path d="m3 17 2 2 4-4M3 7l2 2 4-4M13 6h8M13 12h8M13 18h8"/>',
  listOl: '<path d="M10 6h11M10 12h11M10 18h11M4 6h1v4M4 10h2M6 18H4c0-1 2-2 2-3s-1-1.5-2-1"/>',
  image: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.1-3.1a2 2 0 0 0-2.8 0L6 21"/>',
  table: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M3 15h18M12 3v18"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  columns: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M12 3v18"/>',
  external: '<path d="M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  back: '<path d="M19 12H5M12 19l-7-7 7-7"/>',
  book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/>',
  alert: '<circle cx="12" cy="12" r="9.5"/><path d="M12 8v4M12 16h.01"/>',
  warn: '<path d="m10.3 3.9-8.2 14A2 2 0 0 0 3.8 21h16.4a2 2 0 0 0 1.7-3.1l-8.2-14a2 2 0 0 0-3.4 0zM12 9v4M12 17h.01"/>',
  restore: '<path d="M3 12a9 9 0 1 0 9-9 9.8 9.8 0 0 0-6.7 2.7L3 8"/><path d="M3 3v5h5"/>',
  thumb: '<path d="M7 10v12M15 5.9 14 10h5.8a2 2 0 0 1 1.9 2.6l-2.3 8a2 2 0 0 1-1.9 1.4H4a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h2.8a2 2 0 0 0 1.8-1.1L12 2a3.1 3.1 0 0 1 3 3.9z"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  globe: '<circle cx="12" cy="12" r="9.5"/><path d="M2.5 12h19M12 2.5a14.5 14.5 0 0 1 0 19M12 2.5a14.5 14.5 0 0 0 0 19"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/>',
  clock: '<circle cx="12" cy="12" r="9.5"/><path d="M12 6.5V12l3.5 2"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5M3 12a9 3 0 0 0 18 0"/>',
  cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M15 2v2M15 20v2M2 15h2M2 9h2M20 15h2M20 9h2M9 2v2M9 20v2"/>',
  bulb: '<path d="M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5M9 18h6M10 22h4"/>',
  inbox: '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.5 5.1 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1z"/>',
  hash: '<path d="M4 9h16M4 15h16M10 3 8 21M16 3l-2 18"/>',
  file: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/>',
  chevron: '<path d="m9 18 6-6-6-6"/>',
  chevronDown: '<path d="m6 9 6 6 6-6"/>',
  sparkle: '<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6.3 6.3l2.1 2.1M15.6 15.6l2.1 2.1M6.3 17.7l2.1-2.1M15.6 8.4l2.1-2.1"/>',
  refresh: '<path d="M21 12a9 9 0 0 1-15.5 6.3L3 16M3 12a9 9 0 0 1 15.5-6.3L21 8M21 3v5h-5M3 21v-5h5"/>',
  layers: '<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 12 10 5 10-5M2 17l10 5 10-5"/>',
};

export function icon(name, cls = '') {
  return raw(`<svg class="ic ${cls}" viewBox="0 0 24 24" aria-hidden="true">${P[name] || P.note}</svg>`);
}
export const iconNames = Object.keys(P);
