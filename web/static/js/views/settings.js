// Personal settings: profile, sign-in methods, sessions, API tokens.
import { html, icon, mount, api, $, on, ago, fmtDate, avatar, toast, fail, confirmDialog, promptDialog, copy, modal, esc } from '../util.js';
import { state, canEdit, isAdmin, setTheme, themePref } from '../state.js';
import { setCrumbs, setTitle, navigate } from '../app.js';
import { registerPasskey, supported } from '../webauthn.js';

const TABS = [['profile', 'user', 'Profile'], ['security', 'passkey', 'Sign-in & security'], ['tokens', 'key', 'API tokens'], ['appearance', 'sun', 'Appearance']];

export default async function ({ el, params, query }) {
  const tab = params[0] || 'profile';
  setTitle('Settings');
  setCrumbs([{ label: 'Settings' }]);
  mount(el, html`
    <div class="page-head"><h1>Settings</h1></div>
    <nav class="tabs">${TABS.map(([k, ic, l]) => html`<a href="/settings${k === 'profile' ? '' : '/' + k}" class="${k === tab ? 'on' : ''}">${icon(ic, 'sm')}${l}</a>`)}</nav>
    <div id="tab" style="max-width:820px"></div>`);
  el.classList.add('narrow');
  const t = $('#tab');
  if (query.linked) toast('SSO linked to your account');
  ({ profile, security, tokens, appearance }[tab] || profile)(t);
}

async function profile(t) {
  const u = (await api('me')).user;
  mount(t, html`
    <div class="card card-pad settings-section">
      <div class="row" style="gap:16px;margin-bottom:18px">${avatar(u, 'lg')}<div><div style="font-size:18px;font-weight:650">${u.display_name || u.username}</div>
        <div class="muted">@${u.username} · ${u.role}${u.sso ? ' · SSO linked' : ''} · member since ${fmtDate(u.created_at)}</div></div></div>
      <form id="pf">
        <div class="grid2">
          <div class="field"><label>Display name</label><input class="input" id="dn" value="${u.display_name}"></div>
          <div class="field"><label>E-mail</label><input class="input" id="em" type="email" value="${u.email}"></div>
        </div>
        <button class="btn primary">Save profile</button>
      </form>
    </div>`);
  $('#pf').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const nu = await api('me', { method: 'PATCH', body: { display_name: $('#dn').value.trim(), email: $('#em').value.trim() } });
      state.me.user = nu;
      toast('Profile saved');
      window.dispatchEvent(new CustomEvent('fundus:taxonomy'));
    } catch (err) { fail(err); }
  });
}

async function security(t) {
  const draw = async () => {
    const [me, keys, sessions] = await Promise.all([api('me'), api('me/passkeys'), api('me/sessions')]);
    const u = me.user;
    state.me = me;
    mount(t, html`
      <section class="settings-section">
        <h2>Passkeys</h2>
        <p>Sign in with Touch ID, Windows Hello, your phone or a security key — no password needed. Passkeys are stored as discoverable credentials, so password managers such as Bitwarden can offer them.</p>
        <div class="card list-card">
          ${keys.length ? keys.map((k) => html`<div class="li">${icon('passkey', 'lead')}<div class="grow"><div class="t">${k.name}</div>
            <div class="d">Added ${fmtDate(k.created_at)}${k.last_used_at ? ' · last used ' + ago(k.last_used_at) : ' · never used'}</div></div>
            <button class="btn sm ghost" data-rename="${k.id}" data-name="${k.name}">Rename</button>
            <button class="btn sm ghost danger" data-delpk="${k.id}">Remove</button></div>`)
            : html`<div class="li"><span class="muted">No passkeys yet.</span></div>`}
          <div class="li">${supported() ? html`<button class="btn primary sm" id="addpk">${icon('plus', 'sm')}Add a passkey</button>` : html`<span class="muted">This browser does not support passkeys.</span>`}</div>
        </div>
      </section>
      ${state.config.oidc ? html`<section class="settings-section">
        <h2>Single sign-on</h2>
        <p>${u.sso ? 'Your account is linked to your identity provider. You can sign in with it.' : 'Link your account to your identity provider to sign in with SSO.'}</p>
        <div class="card list-card"><div class="li">${icon('login', 'lead')}<div class="grow"><div class="t">${state.config.oidc_label.replace(/^Sign in with /, '')}</div><div class="d">${u.sso ? 'Linked' : 'Not linked'}</div></div>
          ${u.sso ? html`<button class="btn sm danger" id="unlink">Unlink</button>` : html`<a class="btn sm" href="/auth/oidc/link-start" id="link">Link account</a>`}</div></div>
      </section>` : ''}
      ${state.config.password_login || u.has_password ? html`<section class="settings-section">
        <h2>Password</h2>
        <p>${u.has_password ? 'Change the password used with your username.' : 'You have no password. Set one to sign in with your username as well.'}</p>
        <form class="card card-pad" id="pwf">
          ${u.has_password ? html`<div class="field"><label>Current password</label><input class="input" type="password" id="cur" autocomplete="current-password" required></div>` : ''}
          <div class="grid2"><div class="field"><label>New password</label><input class="input" type="password" id="np" minlength="10" autocomplete="new-password" ${u.has_password ? '' : 'required'}></div>
          <div class="field"><label>Repeat</label><input class="input" type="password" id="np2" autocomplete="new-password"></div></div>
          <div class="row"><button class="btn primary">${u.has_password ? 'Change password' : 'Set password'}</button>
          ${u.has_password && (keys.length || u.sso) ? html`<span class="spacer"></span><button type="button" class="btn ghost danger sm" id="rmpw">Remove password</button>` : ''}</div>
        </form>
      </section>` : ''}
      <section class="settings-section">
        <h2>Active sessions</h2>
        <p>Browsers that are signed in to your account.</p>
        <div class="card list-card">${sessions.map((s) => html`<div class="li">${icon(/Mobile|Android|iPhone/.test(s.user_agent) ? 'monitor' : 'monitor', 'lead')}
          <div class="grow"><div class="t">${browserName(s.user_agent)}${s.current ? html` <span class="chip" style="height:18px;color:var(--success)">this browser</span>` : ''}</div>
          <div class="d">${s.ip || 'unknown IP'} · via ${s.method} · signed in ${ago(s.created_at)} · active ${ago(s.last_seen_at)}</div></div>
          ${s.current ? '' : html`<button class="btn sm ghost danger" data-revoke="${s.id}">Sign out</button>`}</div>`)}</div>
      </section>`);
  };
  on(t, 'click', '#addpk', async () => {
    const name = await promptDialog('Name', { title: 'Add a passkey', value: guessDevice(), ok: 'Continue', hint: 'Helps you recognise it later, e.g. “MacBook Touch ID” or “YubiKey”.' });
    if (name === null) return;
    try { await registerPasskey(name.trim() || 'Passkey'); toast('Passkey added'); draw(); }
    catch (err) { if (err.name === 'NotAllowedError') toast('Cancelled', 'error'); else if (err.name === 'InvalidStateError') toast('This authenticator is already registered', 'error'); else fail(err); }
  });
  on(t, 'click', '[data-rename]', async (e, b) => {
    const name = await promptDialog('Name', { title: 'Rename passkey', value: b.dataset.name });
    if (!name) return;
    try { await api('me/passkeys/' + encodeURIComponent(b.dataset.rename), { method: 'PATCH', body: { name } }); draw(); } catch (err) { fail(err); }
  });
  on(t, 'click', '[data-delpk]', async (e, b) => {
    if (!(await confirmDialog('Remove this passkey? Also delete it from your device or password manager afterwards.', { ok: 'Remove', danger: true }))) return;
    try { await api('me/passkeys/' + encodeURIComponent(b.dataset.delpk), { method: 'DELETE' }); toast('Passkey removed'); draw(); } catch (err) { fail(err); }
  });
  on(t, 'click', '[data-revoke]', async (e, b) => {
    try { await api('me/sessions/' + b.dataset.revoke, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
  });
  on(t, 'click', '#link', (e) => { e.preventDefault(); location.href = '/auth/oidc/login?link=1&prompt=login'; });
  on(t, 'click', '#unlink', async () => {
    if (!(await confirmDialog('Unlink SSO? You will need a passkey or password to sign in.', { ok: 'Unlink', danger: true }))) return;
    try { await api('me/sso', { method: 'DELETE' }); toast('SSO unlinked'); draw(); } catch (err) { fail(err); }
  });
  on(t, 'click', '#rmpw', async () => {
    if (!(await confirmDialog('Remove your password? You will sign in with a passkey or SSO only.', { ok: 'Remove password', danger: true }))) return;
    try { await api('me/password', { method: 'POST', body: { current: $('#cur').value, new: '' } }); toast('Password removed'); draw(); } catch (err) { fail(err); }
  });
  on(t, 'submit', '#pwf', async (e) => {
    e.preventDefault();
    if ($('#np').value !== $('#np2').value) { toast('The passwords do not match', 'error'); return; }
    try { await api('me/password', { method: 'POST', body: { current: $('#cur')?.value || '', new: $('#np').value } }); toast('Password saved'); draw(); } catch (err) { fail(err); }
  });
  draw();
}

function browserName(ua) {
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl/.test(ua) ? 'curl' : 'Browser';
  const os = /Windows/.test(ua) ? 'Windows' : /Mac OS X/.test(ua) ? (/iPhone|iPad/.test(ua) ? 'iOS' : 'macOS') : /Android/.test(ua) ? 'Android' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${b} on ${os}` : b;
}
function guessDevice() {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return 'Android phone';
  if (/Mac OS X/.test(ua)) return 'Mac';
  if (/Windows/.test(ua)) return 'Windows Hello';
  return 'Passkey';
}

async function tokens(t) {
  const draw = async (fresh) => {
    const list = await api('me/tokens');
    const base = location.origin;
    mount(t, html`
      <section class="settings-section">
        <h2>API tokens</h2>
        <p>Tokens let scripts, CI jobs and AI assistants use the <a href="/api-docs">REST API</a> and the MCP endpoint as you. Send them as <code>Authorization: Bearer …</code>.</p>
        ${fresh ? html`<div class="token-reveal">${icon('key')}<code>${fresh.token}</code><button class="btn sm" data-copy="${fresh.token}">${icon('copy', 'sm')}Copy</button></div>
          <p class="small" style="margin-top:-6px;color:var(--warn)">Copy it now — it is shown only once.</p>` : ''}
        <form class="card card-pad" id="tf" style="margin-bottom:14px">
          <div class="grid3">
            <div class="field" style="grid-column:span 2"><label>Name</label><input class="input" id="tn" placeholder="e.g. “backup script” or “Claude Code”" required></div>
            <div class="field"><label>Expires</label><select class="select" id="te"><option value="0">Never</option><option value="7">in 7 days</option><option value="30">in 30 days</option><option value="90" selected>in 90 days</option><option value="365">in 1 year</option></select></div>
          </div>
          <div class="row wrap" style="gap:16px;margin-bottom:14px">
            <label class="check"><input type="checkbox" checked disabled>read</label>
            ${canEdit() ? html`<label class="check"><input type="checkbox" id="tw" checked>write — create and edit entries</label>` : ''}
            ${isAdmin() ? html`<label class="check"><input type="checkbox" id="ta">admin — users, spaces, kinds, import</label>` : ''}
          </div>
          <button class="btn primary">${icon('plus', 'sm')}Create token</button>
        </form>
        ${list.length ? html`<div class="card table-wrap"><table class="table"><thead><tr><th>Name</th><th>Token</th><th>Scopes</th><th>Last used</th><th>Expires</th><th></th></tr></thead><tbody>
          ${list.map((k) => html`<tr><td><b>${k.name}</b><div class="faint small">created ${fmtDate(k.created_at)}</div></td><td class="mono small">${k.prefix}…</td>
            <td>${k.scopes.map((s) => html`<span class="chip" style="margin-right:3px">${s}</span>`)}</td><td class="small">${k.last_used_at ? ago(k.last_used_at) : html`<span class="faint">never</span>`}</td>
            <td class="small">${k.expires_at ? fmtDate(k.expires_at) : html`<span class="faint">never</span>`}</td>
            <td><button class="btn sm ghost danger" data-del="${k.id}">Revoke</button></td></tr>`)}</tbody></table></div>` : ''}
      </section>
      <section class="settings-section">
        <h2>Quick start</h2>
        <p>Document a solved problem from the command line:</p>
        <div class="code-box">curl -X POST ${base}/api/v1/entries \\
  -H "Authorization: Bearer $FUNDUS_TOKEN" -H "Content-Type: application/json" \\
  -d '{"kind":"problem","title":"…","error_text":"…","tags":["linux"],
       "solutions":[{"body":"What fixed it","accepted":true}]}'</div>
        <p style="margin-top:16px">Connect Claude (or any MCP client) to search and extend your knowledge base:</p>
        <div class="code-box">claude mcp add --transport http fundus ${base}/mcp \\
  --header "Authorization: Bearer $FUNDUS_TOKEN"</div>
      </section>`);
  };
  on(t, 'click', '[data-copy]', (e, b) => copy(b.dataset.copy, 'Token copied'));
  on(t, 'click', '[data-del]', async (e, b) => {
    if (!(await confirmDialog('Revoke this token? Anything using it stops working immediately.', { ok: 'Revoke', danger: true }))) return;
    try { await api('me/tokens/' + b.dataset.del, { method: 'DELETE' }); draw(); } catch (err) { fail(err); }
  });
  on(t, 'submit', '#tf', async (e) => {
    e.preventDefault();
    const scopes = ['read'];
    if ($('#tw')?.checked) scopes.push('write');
    if ($('#ta')?.checked) scopes.push('admin');
    try { const tok = await api('me/tokens', { method: 'POST', body: { name: $('#tn').value.trim(), scopes, expires_in_days: +$('#te').value } }); draw(tok); }
    catch (err) { fail(err); }
  });
  draw();
}

function appearance(t) {
  const draw = () => mount(t, html`
    <section class="settings-section"><h2>Theme</h2><p>Stored in this browser.</p>
      <div class="seg">${[['light', 'sun', 'Light'], ['dark', 'moon', 'Dark'], ['auto', 'monitor', 'System']].map(([k, ic, l]) => html`<button data-theme-set="${k}" class="${themePref() === k ? 'on' : ''}">${icon(ic, 'sm')}${l}</button>`)}</div>
    </section>
    <section class="settings-section"><h2>Keyboard shortcuts</h2><p>Press <kbd>?</kbd> anywhere to see them.</p></section>`);
  on(t, 'click', '[data-theme-set]', (e, b) => { setTheme(b.dataset.themeSet); draw(); window.dispatchEvent(new CustomEvent('fundus:taxonomy')); });
  draw();
}
