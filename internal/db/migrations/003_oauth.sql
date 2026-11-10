-- OAuth 2.1 for MCP clients (ChatGPT & co.): dynamically registered clients,
-- single-use authorization codes and rotating refresh tokens. Access tokens
-- are ordinary api_tokens rows tagged with the client.
CREATE TABLE oauth_clients (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    redirect_uris  TEXT[] NOT NULL,
    secret_hash    TEXT,                       -- NULL = public client (PKCE only)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at   TIMESTAMPTZ
);

CREATE TABLE oauth_codes (
    code_hash       TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri    TEXT NOT NULL,
    code_challenge  TEXT NOT NULL,
    scopes          TEXT[] NOT NULL,
    resource        TEXT NOT NULL DEFAULT '',
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE oauth_refresh_tokens (
    token_hash  TEXT PRIMARY KEY,
    client_id   TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scopes      TEXT[] NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX oauth_refresh_tokens_user ON oauth_refresh_tokens (user_id, client_id);

ALTER TABLE api_tokens ADD COLUMN oauth_client_id TEXT REFERENCES oauth_clients(id) ON DELETE CASCADE;
