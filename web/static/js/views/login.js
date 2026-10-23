// Sign-in and first-run setup.
import { html, icon, mount, api, $, esc } from '../util.js';
import { state } from '../state.js';
import { afterLogin, setTitle } from '../app.js';
import { loginWithPasskey, supported } from '../webauthn.js';

export default async function ({ el, query, path }) {
  const cfg = state.config;
  if (state.me) { afterLogin(query.next); return; }
  if (path === '/setup' || cfg.setup_required) return setup(el);
  setTitle('Sign in');
  const err = query.error;
  mount(el, html`
    <div class="auth-wrap">
      <div class="card auth-card">
        <img class="logo" src="/static/img/logo.svg" alt="">
        <h1>${cfg.site_name}</h1>
        <p class="sub">Your knowledge base. Sign in to continue.</p>
        <div id="msg">${err ? html`<div class="alert error">${err}</div>` : ''}</div>
        ${supported() ? html`<button class="btn primary" id="pk">${icon('passkey')} Sign in with a passkey</button>` : ''}
        ${cfg.oidc ? html`<a class="btn" style="margin-top:10px" href="/auth/oidc/login${query.next ? '?return=' + encodeURIComponent(query.next) : ''}" id="sso">${icon('login')} ${cfg.oidc_label}</a>` : ''}
        ${cfg.password_login ? html`
          <div class="or">or</div>
          <form id="pw" autocomplete="on">
            <div class="field"><label for="u">Username</label><input class="input" id="u" name="username" autocomplete="username webauthn" required></div>
            <div class="field"><label for="p">Password</label><input class="input" id="p" name="password" type="password" autocomplete="current-password" required></div>
            <button class="btn" type="submit">Sign in</button>
          </form>` : ''}
      </div>
    </div>`);
  const msg = (t, kind = 'error') => mount($('#msg'), html`<div class="alert ${kind}">${t}</div>`);
  $('#pk')?.addEventListener('click', async (e) => {
    const b = e.currentTarget;
    b.disabled = true;
    try {
      await loginWithPasskey();
      await afterLogin(query.next);
    } catch (e) {
      if (e.name === 'NotAllowedError') msg('The passkey prompt was cancelled or timed out.');
      else msg(e.message);
    } finally { b.disabled = false; }
  });
  $('#pw')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const b = e.target.querySelector('button');
    b.disabled = true;
    try {
      await api('auth/login', { method: 'POST', body: { username: $('#u').value.trim(), password: $('#p').value } });
      await afterLogin(query.next);
    } catch (e) { msg(e.message); $('#p').select(); }
    finally { b.disabled = false; }
  });
  (cfg.password_login ? $('#u') : $('#pk'))?.focus();
}

function setup(el) {
  setTitle('Welcome');
  mount(el, html`
    <div class="auth-wrap">
      <div class="card auth-card" style="max-width:440px">
        <img class="logo" src="/static/img/logo.svg" alt="">
        <h1>Welcome to ${state.config.site_name}</h1>
        <p class="sub">Create the first administrator account.</p>
        <div id="msg"></div>
        <form id="setup">
          <div class="field"><label>Setup token</label><input class="input mono" id="tok" required autocomplete="off">
            <span class="hint">Printed in the server log on first start (<code>docker compose logs fundus</code>).</span></div>
          <div class="field"><label>Username</label><input class="input" id="u" required autocomplete="username"></div>
          <div class="field"><label>Display name</label><input class="input" id="dn" autocomplete="name"></div>
          <div class="field"><label>Password</label><input class="input" id="p" type="password" minlength="10" required autocomplete="new-password">
            <span class="hint">At least 10 characters. You can add a passkey or link SSO afterwards.</span></div>
          <button class="btn primary" type="submit">Create admin & sign in</button>
        </form>
      </div>
    </div>`);
  $('#setup').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      await api('auth/setup', { method: 'POST', body: { setup_token: $('#tok').value.trim(), username: $('#u').value.trim(), display_name: $('#dn').value.trim(), password: $('#p').value } });
      await afterLogin('/');
    } catch (err) { mount($('#msg'), html`<div class="alert error">${err.message}</div>`); }
  });
}
