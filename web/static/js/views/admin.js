// Administration: users, spaces, kinds, webhooks, audit log, import/export.
import { html, raw, icon, mount, api, $, $$, on, ago, fmtDate, avatar, userName, toast, fail, confirmDialog, promptDialog, modal, copy, bytes, iconNames } from '../util.js';
import { state, isAdmin } from '../state.js';
import { setCrumbs, setTitle, refreshSidebar } from '../app.js';

const TABS = [['users', 'users', 'Users'], ['spaces', 'folder', 'Spaces'], ['kinds', 'layers', 'Kinds'], ['webhooks', 'bolt', 'Webhooks'], ['audit', 'activity', 'Audit log'], ['data', 'database', 'Import & export']];

export default async function ({ el, params }) {
  if (!isAdmin()) { mount(el, html`<div class="empty">${icon('shield')}<h3>Admins only</h3></div>`); return; }
  const tab = params[0] || 'users';
  setTitle('Administration');
  setCrumbs([{ label: 'Administration' }]);
  mount(el, html`
    <div class="page-head"><h1>Administration</h1></div>
    <nav class="tabs">${TABS.map(([k, ic, l]) => html`<a href="/admin${k === 'users' ? '' : '/' + k}" class="${k === tab ? 'on' : ''}">${icon(ic, 'sm')}${l}</a>`)}</nav>
    <div id="tab"></div>`);
  ({ users, spaces, kinds, webhooks, audit, data }[tab] || users)($('#tab'));
}

// ------------------------------------------------------------ users

async function users(t) {
  const draw = async () => {
    const list = await api('users');
    mount(t, html`
      <div class="row" style="margin-bottom:14px"><p class="muted grow" style="margin:0">${list.length} account${list.length === 1 ? '' : 's'}. SSO users are created automatically on first sign-in when auto-provisioning is on.</p>
        <button class="btn primary sm" id="new-user">${icon('plus', 'sm')}New user</button></div>
      <div class="card table-wrap"><table class="table"><thead><tr><th>User</th><th>Role</th><th>Sign-in</th><th>Last sign-in</th><th></th></tr></thead><tbody>
      ${list.map((u) => html`<tr style="${u.disabled ? 'opacity:.55' : ''}">
        <td><div class="row">${avatar(u)}<div><b>${userName(u)}</b>${u.id === state.me.user.id ? html` <span class="faint small">(you)</span>` : ''}<div class="faint small">@${u.username}${u.email ? ' · ' + u.email : ''}</div></div></div></td>
        <td><select class="select" data-role="${u.id}" style="height:30px;width:110px">${['admin', 'editor', 'viewer'].map((r) => html`<option ${r === u.role ? 'selected' : ''}>${r}</option>`)}</select></td>
        <td class="small">${[u.has_password && 'password', u.sso && 'SSO'].filter(Boolean).join(', ') || html`<span class="faint">passkey only</span>`}${u.disabled ? html` <span class="chip status status-wontfix">disabled</span>` : ''}</td>
        <td class="small">${u.last_login_at ? ago(u.last_login_at) : html`<span class="faint">never</span>`}</td>
        <td style="text-align:right"><button class="btn ghost sm icon" data-user="${u.id}">${icon('more', 'sm')}</button></td></tr>`)}
      </tbody></table></div>
      <p class="faint small" style="margin-top:14px"><b>admin</b> manages everything · <b>editor</b> creates and edits entries · <b>viewer</b> reads and votes.</p>`);
    t._list = list;
  };
  on(t, 'change', '[data-role]', async (e, s) => {
    try { await api('users/' + s.dataset.role, { method: 'PATCH', body: { role: s.value } }); toast('Role updated'); } catch (err) { fail(err); draw(); }
  });
  on(t, 'click', '[data-user]', (e, b) => {
    const u = t._list.find((x) => x.id === +b.dataset.user);
    import('../util.js').then(({ popMenu }) => popMenu(b, [
      { label: 'Edit profile', icon: 'edit', act: () => editUser(u, draw) },
      { label: 'Set password…', icon: 'key', act: async () => {
        const pw = await promptDialog('New password (min. 10 characters, empty removes it)', { title: `Password for ${u.username}` });
        if (pw === null) return;
        try { await api(`users/${u.id}/password`, { method: 'POST', body: { password: pw } }); toast('Password set; the user was signed out'); draw(); } catch (err) { fail(err); }
      } },
      { label: u.disabled ? 'Enable' : 'Disable', icon: u.disabled ? 'check' : 'x', act: async () => {
        try { await api('users/' + u.id, { method: 'PATCH', body: { disabled: !u.disabled } }); draw(); } catch (err) { fail(err); }
      } },
      'sep',
      { label: 'Delete', icon: 'trash', danger: true, act: async () => {
        if (!(await confirmDialog(`Delete ${u.username}? Their entries stay, but lose the author.`, { ok: 'Delete user', danger: true }))) return;
        try { await api('users/' + u.id, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
      } },
    ]));
  });
  on(t, 'click', '#new-user', () => editUser(null, draw));
  draw();
}

function editUser(u, done) {
  modal({
    title: u ? 'Edit user' : 'New user',
    body: html`<form id="uf">
      ${u ? '' : html`<div class="field"><label>Username</label><input class="input" name="username" required autofocus></div>`}
      <div class="grid2"><div class="field"><label>Display name</label><input class="input" name="display_name" value="${u?.display_name || ''}"></div>
      <div class="field"><label>E-mail</label><input class="input" name="email" type="email" value="${u?.email || ''}"></div></div>
      ${u ? '' : html`<div class="grid2"><div class="field"><label>Role</label><select class="select" name="role"><option>editor</option><option>viewer</option><option>admin</option></select></div>
      <div class="field"><label>Password</label><input class="input" name="password" type="password" minlength="10" placeholder="optional"><span class="hint">Leave empty for SSO-only users (linked on first SSO sign-in by username or verified e-mail).</span></div></div>`}
    </form>`,
    foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-ok>${u ? 'Save' : 'Create user'}</button>`,
    onMount(m, close) {
      m.querySelector('[data-ok]').onclick = async () => {
        const f = Object.fromEntries(new FormData(m.querySelector('#uf')));
        try {
          if (u) await api('users/' + u.id, { method: 'PATCH', body: { display_name: f.display_name, email: f.email } });
          else await api('users', { method: 'POST', body: f });
          close(); toast(u ? 'Saved' : 'User created'); done();
        } catch (err) { fail(err); }
      };
    },
  });
}

// ------------------------------------------------------------ spaces

const SPACE_ICONS = ['folder', 'home', 'monitor', 'cpu', 'database', 'globe', 'terminal', 'code', 'shield', 'book', 'users', 'bolt', 'layers', 'star'];

async function spaces(t) {
  const draw = async () => {
    await refreshSidebar();
    mount(t, html`
      <div class="row" style="margin-bottom:14px"><p class="muted grow" style="margin:0">Spaces group entries by area — e.g. Homelab, Work, Customer X. An entry lives in at most one space.</p>
        <button class="btn primary sm" id="new-space">${icon('plus', 'sm')}New space</button></div>
      <div class="card list-card">${state.spaces.map((s) => html`<div class="li"><span class="kind-ic" style="--c:${s.color}">${icon(s.icon)}</span>
        <div class="grow"><div class="t">${s.name} <span class="faint small mono">${s.slug}</span></div><div class="d">${s.description || '—'} · ${s.entry_count} entries</div></div>
        <button class="btn sm ghost" data-edit="${s.id}">Edit</button><button class="btn sm ghost danger" data-del="${s.id}">Delete</button></div>`)}</div>`);
  };
  const edit = (s) => modal({
    title: s ? 'Edit space' : 'New space',
    body: html`<form id="sf">
      <div class="grid2"><div class="field"><label>Name</label><input class="input" name="name" value="${s?.name || ''}" required autofocus></div>
      <div class="field"><label>Slug</label><input class="input mono" name="slug" value="${s?.slug || ''}" placeholder="auto"></div></div>
      <div class="field"><label>Description</label><input class="input" name="description" value="${s?.description || ''}"></div>
      <div class="grid2"><div class="field"><label>Color</label><input class="input" type="color" name="color" value="${s?.color || '#5b6cff'}" style="padding:3px;height:36px"></div>
      <div class="field"><label>Icon</label><select class="select" name="icon">${SPACE_ICONS.map((i) => html`<option ${i === (s?.icon || 'folder') ? 'selected' : ''}>${i}</option>`)}</select></div></div>
    </form>`,
    foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-ok>Save</button>`,
    onMount(m, close) {
      m.querySelector('[data-ok]').onclick = async () => {
        const f = Object.fromEntries(new FormData(m.querySelector('#sf')));
        try {
          if (s) await api('spaces/' + s.id, { method: 'PATCH', body: f }); else await api('spaces', { method: 'POST', body: f });
          close(); draw();
        } catch (err) { fail(err); }
      };
    },
  });
  on(t, 'click', '#new-space', () => edit(null));
  on(t, 'click', '[data-edit]', (e, b) => edit(state.spaceMap[+b.dataset.edit]));
  on(t, 'click', '[data-del]', async (e, b) => {
    const s = state.spaceMap[+b.dataset.del];
    if (!(await confirmDialog(`Delete the space “${s.name}”? Its ${s.entry_count} entries are kept without a space.`, { ok: 'Delete space', danger: true }))) return;
    try { await api('spaces/' + s.id, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
  });
  draw();
}

// ------------------------------------------------------------ kinds

async function kinds(t) {
  const draw = async () => {
    await refreshSidebar();
    mount(t, html`
      <div class="row" style="margin-bottom:14px"><p class="muted grow" style="margin:0">Kinds define what an entry is: its icon, statuses, suggested fields, Markdown template and whether it collects solutions.</p>
        <button class="btn primary sm" id="new-kind">${icon('plus', 'sm')}New kind</button></div>
      <div class="card list-card">${state.kinds.map((k) => html`<div class="li"><span class="kind-ic" style="--c:${k.color}">${icon(k.icon)}</span>
        <div class="grow"><div class="t">${k.label} <span class="faint small mono">${k.key}</span>${k.has_solutions ? html` <span class="chip">${icon('bulb', 'sm')}solutions</span>` : ''}</div>
        <div class="d">${k.statuses.length ? 'Statuses: ' + k.statuses.join(' → ') + ' · ' : ''}${k.fields.length ? 'Fields: ' + k.fields.map((f) => f.key).join(', ') + ' · ' : ''}${k.entry_count} entries</div></div>
        <button class="btn sm ghost" data-edit="${k.key}">Edit</button>${k.entry_count ? '' : html`<button class="btn sm ghost danger" data-del="${k.key}">Delete</button>`}</div>`)}</div>`);
  };
  const edit = (k) => modal({
    title: k ? `Edit kind “${k.label}”` : 'New kind', wide: true,
    body: html`<form id="kf">
      <div class="grid3"><div class="field"><label>Key</label><input class="input mono" name="key" value="${k?.key || ''}" ${k ? 'readonly' : 'required'} placeholder="e.g. incident"></div>
      <div class="field"><label>Label</label><input class="input" name="label" value="${k?.label || ''}" required></div>
      <div class="field"><label>Plural</label><input class="input" name="plural" value="${k?.plural || ''}"></div></div>
      <div class="field"><label>Description</label><input class="input" name="description" value="${k?.description || ''}"></div>
      <div class="grid3"><div class="field"><label>Color</label><input class="input" type="color" name="color" value="${k?.color || '#5b6cff'}" style="padding:3px;height:36px"></div>
      <div class="field"><label>Icon</label><select class="select" name="icon">${iconNames.map((i) => html`<option ${i === (k?.icon || 'note') ? 'selected' : ''}>${i}</option>`)}</select></div>
      <div class="field"><label>Sort order</label><input class="input" type="number" name="sort" value="${k?.sort ?? 100}"></div></div>
      <div class="field"><label>Statuses</label><input class="input" name="statuses" value="${(k?.statuses || []).join(', ')}" placeholder="e.g. open, investigating, solved (first = default; empty = no status)"></div>
      <div class="field"><label>Suggested fields</label><input class="input" name="fields" value="${(k?.fields || []).map((f) => f.key + (f.label && f.label !== f.key ? ':' + f.label : '')).join(', ')}" placeholder="key:Label, e.g. os:OS / Platform, version:Version"></div>
      <label class="check" style="margin-bottom:14px"><input type="checkbox" name="has_solutions" ${k?.has_solutions ? 'checked' : ''}>Collect solutions (with “worked for me” votes and an accepted answer)</label>
      <div class="field"><label>Template</label><textarea class="textarea mono" name="template" rows="8">${k?.template || ''}</textarea><span class="hint">Pre-filled into the body of new entries of this kind.</span></div>
    </form>`,
    foot: html`<button class="btn" data-close>Cancel</button><button class="btn primary" data-ok>Save kind</button>`,
    onMount(m, close) {
      m.querySelector('[data-ok]').onclick = async () => {
        const f = Object.fromEntries(new FormData(m.querySelector('#kf')));
        const old = Object.fromEntries((k?.fields || []).map((x) => [x.key, x]));
        const body = {
          label: f.label, plural: f.plural, description: f.description, color: f.color, icon: f.icon, sort: +f.sort || 100,
          template: f.template, has_solutions: !!f.has_solutions,
          statuses: f.statuses.split(',').map((s) => s.trim()).filter(Boolean),
          fields: f.fields.split(',').map((s) => s.trim()).filter(Boolean).map((s) => {
            const [key, ...rest] = s.split(':');
            const kk = key.trim().toLowerCase().replace(/\s+/g, '_');
            return { key: kk, label: rest.join(':').trim() || old[kk]?.label || key.trim(), placeholder: old[kk]?.placeholder || '' };
          }),
        };
        try { await api('kinds/' + encodeURIComponent((k?.key || f.key).trim()), { method: 'PUT', body }); close(); toast('Kind saved'); draw(); }
        catch (err) { fail(err); }
      };
    },
  });
  on(t, 'click', '#new-kind', () => edit(null));
  on(t, 'click', '[data-edit]', (e, b) => edit(state.kindMap[b.dataset.edit]));
  on(t, 'click', '[data-del]', async (e, b) => {
    if (!(await confirmDialog(`Delete the kind “${b.dataset.del}”?`, { ok: 'Delete', danger: true }))) return;
    try { await api('kinds/' + b.dataset.del, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
  });
  draw();
}

// ------------------------------------------------------------ webhooks

const EVENTS = ['entry.created', 'entry.updated', 'entry.deleted', 'solution.created', 'solution.accepted'];

async function webhooks(t) {
  const draw = async (fresh) => {
    const list = await api('webhooks');
    mount(t, html`
      <p class="muted" style="margin-top:0">Fundus POSTs JSON to these URLs when things change — handy for chat notifications or syncing. Each request is signed:
        <code>X-Fundus-Signature: sha256=HMAC(secret, body)</code>.</p>
      <form class="card card-pad" id="wf" style="margin-bottom:14px">
        <div class="field"><label>URL</label><input class="input mono" name="url" type="url" placeholder="https://chat.example.com/hooks/…" required></div>
        <div class="row wrap" style="gap:14px;margin-bottom:14px"><label class="check"><input type="checkbox" name="ev" value="*" checked>all events</label>
          ${EVENTS.map((ev) => html`<label class="check"><input type="checkbox" name="ev" value="${ev}">${ev}</label>`)}</div>
        <button class="btn primary sm">${icon('plus', 'sm')}Add webhook</button>
      </form>
      ${fresh ? html`<div class="token-reveal">${icon('key')}<span class="small">Signing secret:</span><code>${fresh.secret}</code><button class="btn sm" data-copy="${fresh.secret}">${icon('copy', 'sm')}Copy</button></div>` : ''}
      ${list.length ? html`<div class="card list-card">${list.map((w) => html`<div class="li">${icon('bolt', 'lead')}
        <div class="grow"><div class="t mono small ellipsis">${w.url}</div><div class="d">${w.events.join(', ')} · ${w.last_at ? 'last: ' + w.last_status + ' ' + ago(w.last_at) : 'not called yet'}</div></div>
        <label class="check small"><input type="checkbox" data-active="${w.id}" ${w.active ? 'checked' : ''}>active</label>
        <button class="btn sm ghost" data-test="${w.id}">Test</button><button class="btn sm ghost" data-secret="${w.secret}">Secret</button><button class="btn sm ghost danger" data-del="${w.id}">Delete</button></div>`)}</div>` : ''}`);
  };
  on(t, 'submit', '#wf', async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const ev = fd.getAll('ev');
    try { const w = await api('webhooks', { method: 'POST', body: { url: fd.get('url'), events: ev.includes('*') || !ev.length ? ['*'] : ev } }); draw(w); }
    catch (err) { fail(err); }
  });
  on(t, 'click', '[data-copy]', (e, b) => copy(b.dataset.copy, 'Secret copied'));
  on(t, 'click', '[data-secret]', (e, b) => copy(b.dataset.secret, 'Secret copied'));
  on(t, 'click', '[data-test]', async (e, b) => {
    b.disabled = true;
    try { const r = await api(`webhooks/${b.dataset.test}/test`, { method: 'POST' }); toast('Response: ' + r.status, /^2/.test(r.status) ? 'ok' : 'error'); draw(); }
    catch (err) { fail(err); } finally { b.disabled = false; }
  });
  on(t, 'change', '[data-active]', async (e, c) => { try { await api('webhooks/' + c.dataset.active, { method: 'PATCH', body: { active: c.checked } }); } catch (err) { fail(err); } });
  on(t, 'click', '[data-del]', async (e, b) => {
    if (!(await confirmDialog('Delete this webhook?', { ok: 'Delete', danger: true }))) return;
    try { await api('webhooks/' + b.dataset.del, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
  });
  draw();
}

// ------------------------------------------------------------ audit

async function audit(t) {
  let offset = 0;
  const draw = async () => {
    const list = await api('audit', { query: { limit: 100, offset } });
    mount(t, html`<div class="card table-wrap"><table class="table"><thead><tr><th>When</th><th>Who</th><th>Action</th><th>Target</th><th>Detail</th><th>IP</th></tr></thead><tbody>
      ${list.map((a) => html`<tr><td class="small nowrap" title="${fmtDate(a.at, true)}">${ago(a.at)}</td><td class="small">${a.user ? userName(a.user) : html`<span class="faint">—</span>`}</td>
        <td><span class="chip ${a.action.includes('failed') ? 'status status-open' : ''}">${a.action}</span></td><td class="small">${a.target}</td><td class="small muted">${a.detail}</td><td class="small mono faint">${a.ip}</td></tr>`)}
      </tbody></table></div>
      <div class="pager"><button class="btn sm" id="prev" ${offset === 0 ? 'disabled' : ''}>Newer</button><button class="btn sm" id="next" ${list.length < 100 ? 'disabled' : ''}>Older</button></div>`);
    $('#prev').onclick = () => { offset = Math.max(0, offset - 100); draw(); };
    $('#next').onclick = () => { offset += 100; draw(); };
  };
  draw();
}

// ------------------------------------------------------------ import / export

async function data(t) {
  const st = await api('stats');
  mount(t, html`
    <div class="grid2" style="gap:16px">
      <div class="card card-pad"><h3>${icon('download')}Export</h3>
        <p class="muted small">Everything as one JSON document: kinds, spaces, entries with solutions and links. Attachments are not included — back up <code>./data</code> for those.</p>
        <a class="btn" href="/api/v1/export" download>${icon('download', 'sm')}Download export</a></div>
      <div class="card card-pad"><h3>${icon('upload')}Import</h3>
        <p class="muted small">Load a Fundus export (<code>fundus-export/1</code>). Entries are added as new entries; links and <code>[[id]]</code> references are re-mapped.</p>
        <input type="file" id="imp" accept="application/json,.json" hidden><button class="btn" id="imp-btn">${icon('upload', 'sm')}Choose file…</button></div>
    </div>
    <div class="card card-pad" style="margin-top:16px"><h3>${icon('database')}Storage</h3>
      <dl class="props"><dt>Entries</dt><dd>${st.entries} (${st.trash} in trash)</dd><dt>Solutions</dt><dd>${st.solutions}</dd>
      <dt>Attachments</dt><dd>${st.attachments} · ${bytes(st.attachment_bytes)}</dd><dt>Users</dt><dd>${st.users}</dd><dt>Version</dt><dd>${state.config.version}</dd>
      <dt>SSO</dt><dd>${state.config.oidc ? 'configured' : 'not configured'}</dd><dt>Password login</dt><dd>${state.config.password_login ? 'enabled' : 'disabled'}</dd></dl></div>`);
  $('#imp-btn').onclick = () => $('#imp').click();
  $('#imp').onchange = async (e) => {
    const f = e.target.files[0];
    if (!f) return;
    if (!(await confirmDialog(`Import “${f.name}” (${bytes(f.size)})?`, { ok: 'Import' }))) return;
    try {
      const r = await api('import', { method: 'POST', raw: await f.text(), headers: { 'Content-Type': 'application/json' } });
      toast(`Imported ${r.imported} entries`);
      refreshSidebar();
    } catch (err) { fail(err); }
  };
}
