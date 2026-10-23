// Global client state: who is signed in, taxonomy caches, preferences.
import { api, html, icon } from './util.js';

export const state = {
  config: null,  // /auth/config
  me: null,      // /me
  kinds: [],
  kindMap: {},
  spaces: [],
  spaceMap: {},
};

export async function loadTaxonomy() {
  const [kinds, spaces] = await Promise.all([api('kinds'), api('spaces')]);
  state.kinds = kinds;
  state.kindMap = Object.fromEntries(kinds.map((k) => [k.key, k]));
  state.spaces = spaces;
  state.spaceMap = Object.fromEntries(spaces.map((s) => [s.id, s]));
  window.dispatchEvent(new CustomEvent('fundus:taxonomy'));
}

export const canEdit = () => !!state.me?.can_edit;
export const isAdmin = () => !!state.me?.is_admin;

export function kindOf(key) {
  return state.kindMap[key] || { key, label: key, plural: key, icon: 'note', color: '#888888', statuses: [], fields: [] };
}

export function kindIcon(key, cls = '') {
  const k = kindOf(key);
  return html`<span class="kind-ic ${cls}" style="--c:${k.color}">${icon(k.icon)}</span>`;
}

export function kindChip(key) {
  const k = kindOf(key);
  return html`<a class="chip kind-chip" style="--c:${k.color}" href="/entries?kind=${k.key}">${icon(k.icon, 'sm')}${k.label}</a>`;
}

export function statusChip(st) {
  if (!st) return '';
  return html`<span class="chip status status-${st}">${st}</span>`;
}

export function tagChip(t) {
  return html`<a class="chip tag" href="/entries?tag=${encodeURIComponent(t)}">${t}</a>`;
}

export function spaceChip(id) {
  const s = state.spaceMap[id];
  if (!s) return '';
  return html`<a class="chip" href="/entries?space=${s.slug}"><span class="dot" style="--c:${s.color}"></span>${s.name}</a>`;
}

// ------------------------------------------------------------ theme

export function themePref() {
  try { return localStorage.getItem('fundus.theme') || 'auto'; } catch { return 'auto'; }
}
export function setTheme(pref) {
  try { localStorage.setItem('fundus.theme', pref); } catch {}
  applyTheme();
}
export function applyTheme() {
  const pref = themePref();
  const dark = pref === 'dark' || (pref === 'auto' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
}
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme);
