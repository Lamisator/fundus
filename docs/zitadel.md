# Single sign-on with Zitadel

Fundus speaks standard OpenID Connect (authorization code flow with PKCE), so any compliant identity provider works. These notes cover Zitadel in particular.

## 1. Create the application in Zitadel

1. **Projects → New project**, e.g. `Fundus`.
2. In the project: **New application → Web**.
   - Authentication method **PKCE** (no client secret), or **Code** if you prefer a secret.
   - Redirect URI: `https://kb.example.com/auth/oidc/callback`
3. Copy the **Client ID** (and the secret for *Code*).
4. Optional: enable **"User Info inside ID Token"** under *Token settings*. Fundus does not need it, because it fetches the userinfo endpoint and merges in any missing claims (`preferred_username`, `email`, `name`). Zitadel v4 otherwise puts only the mandatory claims into the ID token.

## 2. Configure Fundus

```ini
FUNDUS_OIDC_ISSUER=https://auth.example.com          # your Zitadel instance (no trailing slash)
FUNDUS_OIDC_CLIENT_ID=123456789@fundus
FUNDUS_OIDC_CLIENT_SECRET=                           # only for "Code" apps
FUNDUS_OIDC_BUTTON_LABEL=Sign in with Zitadel
```

Restart the stack. The sign-in page now shows the SSO button. Discovery happens lazily, so a Zitadel that is down while Fundus starts does not keep Fundus from booting.

## 3. Who gets in, and with which role

| Setting | Effect |
| --- | --- |
| `FUNDUS_OIDC_AUTO_CREATE=true` (default) | unknown users get an account on first login with `FUNDUS_OIDC_DEFAULT_ROLE` (default `editor`) |
| `FUNDUS_OIDC_AUTO_CREATE=false` | only users an admin created beforehand can sign in |
| `FUNDUS_OIDC_ALLOWED_DOMAIN=example.com` | only users of the Zitadel organisation with that primary domain; Fundus requests the scope `urn:zitadel:iam:org:domain:primary:example.com` **and** checks the returned claim |
| `FUNDUS_OIDC_ROLE_CLAIM=urn:zitadel:iam:org:project:roles` | roles come from Zitadel, see below |

**Linking existing accounts.** On the first SSO login Fundus looks for a local account with the same username. It tries both `dana@example.com` and `dana`, then an account with the same e-mail address, but only if Zitadel reports that address as verified. A matching account is linked instead of creating a duplicate. Signed-in users can also link or unlink SSO themselves under *Settings → Sign-in & security*.

**Roles from Zitadel.** Create the project roles `admin`, `editor` and `viewer` in the Zitadel project (`fundus-admin` and so on also work) and grant them to users. With `FUNDUS_OIDC_ROLE_CLAIM` set, Fundus:

- requests the scope `urn:zitadel:iam:org:project:roles`,
- sets the user's role on **every** login, using the highest one granted (admin > editor > viewer),
- refuses users who have none of these roles.

Other identity providers work the same way, as long as the claim is a list of strings, a space or comma separated string, or a map whose keys are role names.

## 4. Passkeys and SSO together

Every account can have passkeys, SSO and a password at the same time. To run Fundus without passwords, set `FUNDUS_PASSWORD_LOGIN=false`. Users then sign in with SSO and can add passkeys for quicker logins. Fundus refuses to remove a user's last way of signing in.

## Troubleshooting

- **"state mismatch" or "the sign-in took too long"**: the short-lived state cookie (path `/auth/oidc/`) was not sent back. Check that the redirect URI uses the same host the browser started on, and that Traefik passes `X-Forwarded-Proto`/`-Host`. If Fundus is reachable under several names, set `FUNDUS_BASE_URL`.
- **"redirect_uri missing or invalid"** (shown by Zitadel): the URI registered in Zitadel must exactly match `<FUNDUS_BASE_URL>/auth/oidc/callback`.
- **Users end up as `sso-1a2b3c4d`**: Zitadel returned neither `preferred_username` nor `email`. Add the `profile` and `email` scopes (they are part of the default `FUNDUS_OIDC_SCOPES`).
- Failed SSO logins and their reasons are recorded in the audit log (*Admin → Audit log*).
