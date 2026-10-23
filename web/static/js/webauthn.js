// WebAuthn helpers: convert the server's JSON options to ArrayBuffers and
// the browser's credential back to JSON (base64url everywhere).
import { api } from './util.js';

export function b64url(buf) {
  return btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function fromB64url(s) {
  const pad = s + '==='.slice((s.length + 3) % 4);
  const bin = atob(pad.replace(/-/g, '+').replace(/_/g, '/'));
  const b = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) b[i] = bin.charCodeAt(i);
  return b.buffer;
}

export const supported = () => !!(window.PublicKeyCredential && navigator.credentials);

/** Register a new passkey for the signed-in user. */
export async function registerPasskey(name) {
  const opts = await api('me/passkeys/begin', { method: 'POST', body: {} });
  const pk = opts.publicKey;
  pk.challenge = fromB64url(pk.challenge);
  pk.user.id = fromB64url(pk.user.id);
  (pk.excludeCredentials || []).forEach((c) => (c.id = fromB64url(c.id)));
  const cred = await navigator.credentials.create({ publicKey: pk });
  const body = {
    id: cred.id,
    rawId: b64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: b64url(cred.response.clientDataJSON),
      attestationObject: b64url(cred.response.attestationObject),
      transports: cred.response.getTransports ? cred.response.getTransports() : [],
    },
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
  // The name travels in the query: the server hands the raw body to the WebAuthn library.
  return api('me/passkeys/finish?name=' + encodeURIComponent(name || 'Passkey'), { method: 'POST', body });
}

/** Usernameless sign-in with a discoverable credential. */
export async function loginWithPasskey() {
  const opts = await api('auth/passkey/begin', { method: 'POST', body: {} });
  const pk = opts.publicKey;
  pk.challenge = fromB64url(pk.challenge);
  (pk.allowCredentials || []).forEach((c) => (c.id = fromB64url(c.id)));
  const cred = await navigator.credentials.get({ publicKey: pk });
  const body = {
    id: cred.id,
    rawId: b64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: b64url(cred.response.clientDataJSON),
      authenticatorData: b64url(cred.response.authenticatorData),
      signature: b64url(cred.response.signature),
      userHandle: cred.response.userHandle ? b64url(cred.response.userHandle) : null,
    },
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
  return api('auth/passkey/finish', { method: 'POST', body });
}
