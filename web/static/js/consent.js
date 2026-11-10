// OAuth consent page (/oauth/authorize): posts the decision as JSON (which
// passes the CSRF check like every other UI request), then follows the
// redirect back to the app.
const form = document.getElementById('consent');
if (form) {
  const params = JSON.parse(form.dataset.params || '{}');
  const msg = document.getElementById('msg');
  const buttons = form.querySelectorAll('button');
  const decide = async (approve) => {
    buttons.forEach((b) => { b.disabled = true; });
    msg.innerHTML = '';
    try {
      const allow = form.querySelector('[name=allow_write]');
      const r = await fetch('/oauth/authorize', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fetch' },
        body: JSON.stringify({ params, approve, allow_write: !!(allow && allow.checked) }),
      });
      const body = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(body.error?.message || `HTTP ${r.status}`);
      location.assign(body.redirect);
    } catch (e) {
      const div = document.createElement('div');
      div.className = 'alert error';
      div.textContent = e.message;
      msg.append(div);
      buttons.forEach((b) => { b.disabled = false; });
    }
  };
  form.addEventListener('submit', (e) => { e.preventDefault(); decide(true); });
  form.querySelector('[data-approve="0"]').addEventListener('click', () => decide(false));
}
