// Fundus web app: shell, router, command palette, keyboard shortcuts.
import { $, $$, api, html, raw, icon, mount, on, esc, avatar, userName, debounce, popMenu, fail } from './util.js';
import { state, loadTaxonomy, canEdit, isAdmin, kindIcon, kindOf, themePref, setTheme } from './state.js';

const views = {
  login: () => import('./views/login.js'),
  home: () => import('./views/home.js'),
  search: () => import('./views/search.js'),
  entry: () => import('./views/entry.js'),
  editor: () => import('./views/editor.js'),
  history: () => import('./views/history.js'),
  settings: () => import('./views/settings.js'),
  admin: () => import('./views/admin.js'),
  tags: () => import('./views/tags.js'),
  apidocs: () => import('./views/apidocs.js'),
};

const routes = [
  [/^\/$/, 'home'],
  [/^\/search$/, 'search'],
  [/^\/entries$/, 'search'],
  [/^\/trash$/, 'search', { trash: true }],
  [/^\/new$/, 'editor'],
  [/^\/e\/(\d+)$/, 'entry'],
  [/^\/e\/(\d+)\/edit$/, 'editor'],
  [/^\/e\/(\d+)\/history$/, 'history'],
  [/^\/tags$/, 'tags'],
  [/^\/settings(?:\/(\w+))?$/, 'settings'],
  [/^\/admin(?:\/(\w+))?$/, 'admin'],
  [/^\/api-docs$/, 'apidocs'],
  [/^\/login$/, 'login'],
  [/^\/setup$/, 'login'],
];

let cleanup = null;
let navToken = 0;

export function navigate(url, { replace = false } = {}) {
  if (replace) history.replaceState(null, '', url); else history.pushState(null, '', url);
  route();
}
window.fundusNavigate = navigate;

// Views may register a guard (e.g. unsaved editor changes).
let leaveGuard = null;
export function setLeaveGuard(fn) { leaveGuard = fn; }

async function route() {
  const token = ++navToken;
  const path = location.pathname;
  const query = Object.fromEntries(new URLSearchParams(location.search));
  let match = null, name = null, extra = {};
  for (const [re, n, x] of routes) {
    const m = path.match(re);
    if (m) { match = m; name = n; extra = x || {}; break; }
  }
  if (!state.me && name !== 'login') {
    const back = location.pathname + location.search;
    history.replaceState(null, '', state.config?.setup_required ? '/setup' : '/login' + (back !== '/' ? '?next=' + encodeURIComponent(back) : ''));
    return route();
  }
  if (typeof cleanup === 'function') { try { cleanup(); } catch {} }
  cleanup = null;
  leaveGuard = null;
  document.querySelectorAll('.overlay, .menu').forEach((o) => o.remove());

  const app = $('#app');
  if (name === 'login') {
    document.body.classList.remove('has-shell');
    const mod = await views.login();
    if (token !== navToken) return;
    cleanup = await mod.default({ el: app, query, path });
    return;
  }
  ensureShell();
  highlightNav();
  // Fresh element per view, so listeners a view attached to it do not leak.
  const old = $('#page');
  const page = old.cloneNode(false);
  old.replaceWith(page);
  $('.shell').classList.remove('nav-open');
  if (!name) {
    setCrumbs([]);
    mount(page, html`<div class="empty">${icon('compass')}<h3>Page not found</h3><p>Nothing lives at ${path}.</p><a class="btn" href="/">Go home</a></div>`);
    return;
  }
  const mod = await views[name]();
  if (token !== navToken) return;
  page.className = 'page';
  page.innerHTML = '';
  window.scrollTo(0, 0);
  try {
    const done = await mod.default({ el: page, params: match.slice(1), query, path, ...extra });
    // A newer navigation started while this view was loading: drop it.
    if (token !== navToken) { if (typeof done === 'function') done(); return; }
    cleanup = done;
  } catch (e) {
    if (token !== navToken) return;
    if (e.status === 404) {
      mount(page, html`<div class="empty">${icon('compass')}<h3>Not found</h3><p>${e.message}</p><a class="btn" href="/">Go home</a></div>`);
    } else {
      mount(page, html`<div class="empty">${icon('alert')}<h3>Something went wrong</h3><p>${e.message}</p></div>`);
      console.error(e);
    }
  }
}

export function setTitle(t) {
  document.title = t ? `${t} · ${state.config?.site_name || 'Fundus'}` : state.config?.site_name || 'Fundus';
}

/** crumbs: [{label, href}] ; actions: html for the right side of the top bar */
export function setCrumbs(crumbs, actions = '') {
  const c = $('#crumbs');
  if (!c) return;
  c.innerHTML = String(html`${crumbs.map((x, i) => html`${i ? html`<span class="sep">/</span>` : ''}${x.href ? html`<a href="${x.href}">${x.label}</a>` : html`<b class="ellipsis">${x.label}</b>`}`)}`);
  $('#top-actions').innerHTML = String(actions);
}

// ------------------------------------------------------------ shell

function ensureShell() {
  if ($('.shell')) { renderSidebar(); return; }
  document.body.classList.add('has-shell');
  mount($('#app'), html`
    <div class="shell">
      <aside class="sidebar" id="sidebar"></aside>
      <div class="main">
        <header class="topbar" id="topbar">
          <button class="btn ghost icon menu-btn" id="menu-btn" aria-label="Menu">${icon('menu')}</button>
          <nav class="crumbs grow" id="crumbs"></nav>
          <div class="row" id="top-actions"></div>
        </header>
        <main id="page" class="page"></main>
      </div>
    </div>`);
  renderSidebar();
  $('#menu-btn').onclick = () => $('.shell').classList.toggle('nav-open');
  $('.shell').addEventListener('click', (e) => {
    if (e.target === $('.shell')) $('.shell').classList.remove('nav-open');
  });
  const tb = $('#topbar');
  window.addEventListener('scroll', () => tb.classList.toggle('scrolled', window.scrollY > 4), { passive: true });
}

function renderSidebar() {
  const sb = $('#sidebar');
  if (!sb) return;
  const me = state.me.user;
  const total = state.kinds.reduce((n, k) => n + k.entry_count, 0);
  mount(sb, html`
    <a class="brand" href="/"><img src="/static/img/logo.svg" alt=""><b>${state.config.site_name}</b></a>
    <button class="search-trigger" id="open-palette">${icon('search')}<span>Search…</span><kbd>${navigator.platform.includes('Mac') ? '⌘' : 'Ctrl'} K</kbd></button>
    ${canEdit() ? html`<a class="btn primary new-btn" href="/new">${icon('plus')} New entry</a>` : ''}
    <nav>
      <a class="nav-item" href="/" data-nav="/">${icon('home')}Home</a>
      <a class="nav-item" href="/entries" data-nav="/entries">${icon('layers')}All entries<span class="count">${total}</span></a>
      <a class="nav-item" href="/entries?starred=1" data-nav="/entries?starred=1">${icon('star')}Starred</a>
      <a class="nav-item" href="/entries?kind=problem&status=open" data-nav="/entries?kind=problem&status=open">${icon('alert')}Open problems</a>
      <a class="nav-item" href="/tags" data-nav="/tags">${icon('tag')}Tags</a>
    </nav>
    <div class="nav-section">
      <div class="nav-title">Kinds</div>
      ${state.kinds.map((k) => html`<a class="nav-item" href="/entries?kind=${k.key}" data-nav="/entries?kind=${k.key}"><span class="dot" style="--c:${k.color}"></span>${k.plural || k.label}<span class="count">${k.entry_count || ''}</span></a>`)}
    </div>
    <div class="nav-section">
      <div class="nav-title">Spaces ${isAdmin() ? html`<a href="/admin/spaces" title="Manage spaces">${icon('plus', 'sm')}</a>` : ''}</div>
      ${state.spaces.map((s) => html`<a class="nav-item" href="/entries?space=${s.slug}" data-nav="/entries?space=${s.slug}">${icon(s.icon || 'folder')}<span class="ellipsis">${s.name}</span><span class="count">${s.entry_count || ''}</span></a>`)}
    </div>
    <div class="nav-section">
      <div class="nav-title">More</div>
      <a class="nav-item" href="/api-docs" data-nav="/api-docs">${icon('book')}API docs</a>
      ${canEdit() ? html`<a class="nav-item" href="/trash" data-nav="/trash">${icon('trash')}Trash</a>` : ''}
      ${isAdmin() ? html`<a class="nav-item" href="/admin" data-nav="/admin">${icon('shield')}Administration</a>` : ''}
    </div>
    <div class="sidebar-foot">
      <a class="user-chip" href="/settings">${avatar(me)}<span class="who ellipsis"><b class="ellipsis">${userName(me)}</b><small>${me.role}</small></span></a>
      <button class="btn ghost icon sm" id="theme-btn" title="Theme">${icon(themePref() === 'dark' ? 'moon' : themePref() === 'light' ? 'sun' : 'monitor')}</button>
      <button class="btn ghost icon sm" id="logout-btn" title="Sign out">${icon('logout')}</button>
    </div>`);
  $('#open-palette').onclick = openPalette;
  $('#theme-btn').onclick = (e) => popMenu(e.currentTarget, [
    { label: 'Light', icon: 'sun', act: () => { setTheme('light'); renderSidebar(); } },
    { label: 'Dark', icon: 'moon', act: () => { setTheme('dark'); renderSidebar(); } },
    { label: 'System', icon: 'monitor', act: () => { setTheme('auto'); renderSidebar(); } },
  ]);
  $('#logout-btn').onclick = logout;
  highlightNav();
}

function highlightNav() {
  const cur = location.pathname + location.search;
  $$('.nav-item').forEach((a) => a.classList.toggle('active', a.dataset.nav === cur ||
    (a.dataset.nav !== '/' && a.dataset.nav === location.pathname && !location.search)));
}

async function logout() {
  try { await api('auth/logout', { method: 'POST' }); } catch {}
  state.me = null;
  location.href = '/login';
}

export async function refreshSidebar() {
  await loadTaxonomy();
  renderSidebar();
}

// ------------------------------------------------------------ command palette

const actions = () => [
  canEdit() && { label: 'New problem', sub: 'Document something that broke', icon: 'bug', go: '/new?kind=problem' },
  canEdit() && { label: 'New entry…', sub: 'How-to, runbook, snippet, note, reference', icon: 'plus', go: '/new' },
  { label: 'Open problems', icon: 'alert', go: '/entries?kind=problem&status=open' },
  { label: 'All entries', icon: 'layers', go: '/entries' },
  { label: 'Starred', icon: 'star', go: '/entries?starred=1' },
  { label: 'Tags', icon: 'tag', go: '/tags' },
  { label: 'Settings', sub: 'Profile, passkeys, API tokens', icon: 'sliders', go: '/settings' },
  { label: 'API documentation', icon: 'book', go: '/api-docs' },
  isAdmin() && { label: 'Administration', icon: 'shield', go: '/admin' },
  { label: 'Toggle dark mode', icon: 'moon', run: () => { setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'); renderSidebar(); } },
].filter(Boolean);

function openPalette() {
  if ($('.palette')) return;
  const ov = document.createElement('div');
  ov.className = 'overlay';
  ov.innerHTML = String(html`<div class="modal palette" role="dialog" aria-label="Search">
    <div class="palette-input">${icon('search')}<input placeholder="Search entries, paste an error, or type a command…" autocomplete="off" spellcheck="false"><kbd>esc</kbd></div>
    <div class="palette-list"></div>
    <div class="palette-foot"><span><kbd>↑</kbd> <kbd>↓</kbd> navigate</span><span><kbd>↵</kbd> open</span><span><kbd>⇧</kbd><kbd>↵</kbd> full search</span></div>
  </div>`);
  document.body.append(ov);
  const input = $('input', ov), list = $('.palette-list', ov);
  let items = [], sel = 0, seq = 0;
  const close = () => { ov.remove(); };
  const draw = () => {
    let lastGroup = null;
    list.innerHTML = items.map((it, i) => {
      const g = it.group !== lastGroup ? `<div class="palette-group">${esc(it.group)}</div>` : '';
      lastGroup = it.group;
      return g + String(html`<div class="palette-item ${i === sel ? 'on' : ''}" data-i="${i}">
        ${it.entry ? kindIcon(it.entry.kind) : html`<span class="kind-ic" style="--c:var(--muted)">${icon(it.icon)}</span>`}
        <div class="grow"><div class="t ellipsis">${it.label}</div>${it.sub ? html`<div class="s ellipsis">${it.snippet ? raw(it.sub) : it.sub}</div>` : ''}</div>
        ${it.entry?.status ? html`<span class="chip status status-${it.entry.status}">${it.entry.status}</span>` : ''}
      </div>`);
    }).join('') || '<div class="empty small" style="padding:24px">No matches</div>';
    list.querySelector('.on')?.scrollIntoView({ block: 'nearest' });
  };
  const pick = (i, full = false) => {
    const q = input.value.trim();
    if (full && q) { close(); navigate('/search?q=' + encodeURIComponent(q)); return; }
    const it = items[i];
    if (!it) return;
    close();
    if (it.run) it.run(); else navigate(it.go);
  };
  const update = debounce(async () => {
    const q = input.value.trim();
    const my = ++seq;
    const acts = actions().filter((a) => !q || a.label.toLowerCase().includes(q.toLowerCase())).map((a) => ({ ...a, group: 'Actions' }));
    let found = [];
    if (q) {
      try {
        const res = await api('entries', { query: { q, limit: 8 } });
        found = res.items.map((e) => ({ group: `Entries · ${res.total} found`, label: e.title, sub: e.snippet || e.summary, snippet: !!e.snippet, entry: e, go: '/e/' + e.id }));
        if (res.total > 8) found.push({ group: found[0].group, label: `Show all ${res.total} results`, icon: 'search', go: '/search?q=' + encodeURIComponent(q) });
      } catch {}
    } else {
      try {
        const res = await api('entries', { query: { limit: 6, sort: 'updated' } });
        found = res.items.map((e) => ({ group: 'Recently updated', label: e.title, sub: kindOf(e.kind).label, entry: e, go: '/e/' + e.id }));
      } catch {}
    }
    if (my !== seq) return;
    items = q ? [...found, ...acts] : [...acts.slice(0, 4), ...found];
    sel = 0;
    draw();
  }, 120);
  input.addEventListener('input', update);
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { sel = Math.min(items.length - 1, sel + 1); draw(); e.preventDefault(); }
    else if (e.key === 'ArrowUp') { sel = Math.max(0, sel - 1); draw(); e.preventDefault(); }
    else if (e.key === 'Enter') { e.preventDefault(); pick(sel, e.shiftKey); }
    else if (e.key === 'Escape') close();
  });
  list.addEventListener('mousemove', (e) => {
    const it = e.target.closest('[data-i]');
    if (it && +it.dataset.i !== sel) { sel = +it.dataset.i; draw(); }
  });
  list.addEventListener('click', (e) => { const it = e.target.closest('[data-i]'); if (it) pick(+it.dataset.i); });
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(); });
  input.focus();
  update();
}
export { openPalette };

// ------------------------------------------------------------ global handlers

document.addEventListener('click', (e) => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = e.target.closest('a[href]');
  if (!a || a.target || a.hasAttribute('download')) return;
  const href = a.getAttribute('href');
  if (!href.startsWith('/') || href.startsWith('//')) return;
  if (/^\/(api|files|s|auth|static|mcp|healthz)(\/|$|\?)/.test(href)) return;
  e.preventDefault();
  if (leaveGuard && !leaveGuard()) return;
  if (href === location.pathname + location.search && !href.includes('#')) { route(); return; }
  navigate(href);
});

window.addEventListener('popstate', () => route());
window.addEventListener('fundus:unauthorized', () => {
  if (state.me) { state.me = null; navigate('/login?next=' + encodeURIComponent(location.pathname + location.search), { replace: true }); }
});

let gPending = false;
document.addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    if (state.me) { e.preventDefault(); openPalette(); }
    return;
  }
  const t = e.target;
  if (t.closest('input, textarea, select, [contenteditable]') || e.metaKey || e.ctrlKey || e.altKey || !state.me) return;
  if ($('.overlay')) return;
  if (gPending) {
    gPending = false;
    const map = { h: '/', e: '/entries', t: '/tags', s: '/settings', p: '/entries?kind=problem&status=open', a: '/admin' };
    if (map[e.key]) { e.preventDefault(); navigate(map[e.key]); }
    return;
  }
  if (e.key === '/') { e.preventDefault(); openPalette(); }
  else if (e.key === 'n' && canEdit()) { e.preventDefault(); navigate('/new'); }
  else if (e.key === 'g') { gPending = true; setTimeout(() => (gPending = false), 900); }
  else if (e.key === '?') { e.preventDefault(); shortcutsHelp(); }
});

function shortcutsHelp() {
  import('./util.js').then(({ modal }) => modal({
    title: 'Keyboard shortcuts',
    body: html`<table class="table"><tbody>
      ${[['Ctrl/⌘ K or /', 'Search & commands'], ['n', 'New entry'], ['e', 'Edit the open entry'], ['s', 'Star the open entry'],
         ['g h', 'Go home'], ['g e', 'All entries'], ['g p', 'Open problems'], ['g t', 'Tags'], ['g s', 'Settings'],
         ['Ctrl/⌘ S', 'Save while editing'], ['Ctrl/⌘ Enter', 'Save and close the editor'], ['?', 'This help']]
        .map(([k, d]) => html`<tr><td><kbd>${k}</kbd></td><td>${d}</td></tr>`)}
    </tbody></table>`,
  }));
}

// ------------------------------------------------------------ boot

async function boot() {
  try {
    state.config = await api('auth/config');
    if (state.config.authenticated) {
      state.me = await api('me');
      await loadTaxonomy();
    }
  } catch (e) {
    mount($('#app'), html`<div class="auth-wrap"><div class="card auth-card"><h1>Unavailable</h1><p class="sub">${e.message}</p></div></div>`);
    return;
  }
  setTitle('');
  route();
}

export async function afterLogin(next) {
  state.config = await api('auth/config');
  state.me = await api('me');
  await loadTaxonomy();
  navigate(next && next.startsWith('/') && !next.startsWith('//') ? next : '/', { replace: true });
}

window.addEventListener('fundus:taxonomy', () => renderSidebar());
boot().catch(fail);
