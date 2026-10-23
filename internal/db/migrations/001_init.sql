-- Fundus initial schema.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------------------------------------------------------------- users

CREATE TABLE users (
    id             BIGSERIAL PRIMARY KEY,
    username       TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    email          TEXT NOT NULL DEFAULT '',
    password_hash  TEXT,                       -- NULL = no local password (SSO / passkey only)
    role           TEXT NOT NULL DEFAULT 'editor' CHECK (role IN ('admin', 'editor', 'viewer')),
    disabled       BOOLEAN NOT NULL DEFAULT FALSE,
    oidc_issuer    TEXT,
    oidc_subject   TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));
CREATE UNIQUE INDEX users_oidc_key ON users (oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,            -- sha256(token), hex
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    method        TEXT NOT NULL DEFAULT '',    -- password | passkey | oidc
    user_agent    TEXT NOT NULL DEFAULT '',
    ip            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE webauthn_credentials (
    id            TEXT PRIMARY KEY,            -- base64url(credential.ID)
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    data          JSONB NOT NULL,              -- serialised webauthn.Credential
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);

CREATE TABLE webauthn_sessions (
    id          TEXT PRIMARY KEY,
    data        JSONB NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE api_tokens (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,        -- sha256(token), hex
    prefix        TEXT NOT NULL,               -- first characters, shown in the UI
    scopes        TEXT[] NOT NULL DEFAULT '{read}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ
);

CREATE TABLE settings (
    key    TEXT PRIMARY KEY,
    value  JSONB NOT NULL
);

-- ---------------------------------------------------------------- taxonomy

CREATE TABLE spaces (
    id           BIGSERIAL PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    icon         TEXT NOT NULL DEFAULT 'folder',
    color        TEXT NOT NULL DEFAULT '#5b6cff',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE kinds (
    key            TEXT PRIMARY KEY,
    label          TEXT NOT NULL,
    plural         TEXT NOT NULL DEFAULT '',
    icon           TEXT NOT NULL DEFAULT 'note',
    color          TEXT NOT NULL DEFAULT '#5b6cff',
    description    TEXT NOT NULL DEFAULT '',
    template       TEXT NOT NULL DEFAULT '',       -- markdown pre-filled into new entries
    statuses       TEXT[] NOT NULL DEFAULT '{}',   -- allowed statuses, first = default; empty = no status
    has_solutions  BOOLEAN NOT NULL DEFAULT FALSE,
    fields         JSONB NOT NULL DEFAULT '[]',    -- suggested custom fields [{key,label,placeholder}]
    sort           INT NOT NULL DEFAULT 100
);

-- ---------------------------------------------------------------- entries

CREATE TABLE entries (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT NOT NULL REFERENCES kinds(key) ON UPDATE CASCADE,
    space_id     BIGINT REFERENCES spaces(id) ON DELETE SET NULL,
    title        TEXT NOT NULL,
    summary      TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    error_text   TEXT NOT NULL DEFAULT '',          -- verbatim error message / log excerpt
    status       TEXT NOT NULL DEFAULT '',
    tags         TEXT[] NOT NULL DEFAULT '{}',
    fields       JSONB NOT NULL DEFAULT '{}',        -- free key/value metadata (os, version, host, ...)
    pinned       BOOLEAN NOT NULL DEFAULT FALSE,
    version      INT NOT NULL DEFAULT 1,
    view_count   BIGINT NOT NULL DEFAULT 0,
    created_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    updated_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,
    solutions_text TEXT NOT NULL DEFAULT '',         -- denormalised, kept for search only
    search       TSVECTOR
);
CREATE INDEX entries_search_idx   ON entries USING GIN (search);
CREATE INDEX entries_tags_idx     ON entries USING GIN (tags);
CREATE INDEX entries_title_trgm   ON entries USING GIN (title gin_trgm_ops);
CREATE INDEX entries_error_trgm   ON entries USING GIN (error_text gin_trgm_ops);
CREATE INDEX entries_updated_idx  ON entries (updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX entries_kind_idx     ON entries (kind, status) WHERE deleted_at IS NULL;
CREATE INDEX entries_space_idx    ON entries (space_id) WHERE deleted_at IS NULL;

CREATE FUNCTION entries_search_update() RETURNS trigger AS $$
DECLARE
    field_text TEXT;
BEGIN
    SELECT coalesce(string_agg(key || ' ' || value, ' '), '') INTO field_text
      FROM jsonb_each_text(NEW.fields);
    NEW.search :=
        setweight(to_tsvector('simple',  coalesce(NEW.title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(NEW.title, '')), 'A') ||
        setweight(to_tsvector('simple',  array_to_string(NEW.tags, ' ')), 'A') ||
        setweight(to_tsvector('simple',  coalesce(NEW.summary, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(NEW.summary, '')), 'B') ||
        setweight(to_tsvector('simple',  coalesce(NEW.error_text, '')), 'B') ||
        setweight(to_tsvector('simple',  field_text), 'B') ||
        setweight(to_tsvector('english', coalesce(NEW.body, '')), 'C') ||
        setweight(to_tsvector('simple',  coalesce(NEW.body, '')), 'C') ||
        setweight(to_tsvector('english', coalesce(NEW.solutions_text, '')), 'D') ||
        setweight(to_tsvector('simple',  coalesce(NEW.solutions_text, '')), 'D');
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER entries_search_trg BEFORE INSERT OR UPDATE ON entries
    FOR EACH ROW EXECUTE FUNCTION entries_search_update();

CREATE TABLE entry_revisions (
    id          BIGSERIAL PRIMARY KEY,
    entry_id    BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    version     INT NOT NULL,
    kind        TEXT NOT NULL,
    space_id    BIGINT,
    title       TEXT NOT NULL,
    summary     TEXT NOT NULL,
    body        TEXT NOT NULL,
    error_text  TEXT NOT NULL,
    status      TEXT NOT NULL,
    tags        TEXT[] NOT NULL,
    fields      JSONB NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    edited_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    edited_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entry_id, version)
);

CREATE TABLE solutions (
    id          BIGSERIAL PRIMARY KEY,
    entry_id    BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    title       TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL,
    accepted    BOOLEAN NOT NULL DEFAULT FALSE,
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX solutions_entry_idx ON solutions (entry_id);

CREATE TABLE solution_votes (
    solution_id  BIGINT NOT NULL REFERENCES solutions(id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (solution_id, user_id)
);

-- Explicit, typed links between entries.
CREATE TABLE entry_links (
    from_id     BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    to_id       BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    rel         TEXT NOT NULL DEFAULT 'related'
                CHECK (rel IN ('related', 'duplicate-of', 'caused-by', 'depends-on', 'supersedes')),
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (from_id, to_id, rel),
    CHECK (from_id <> to_id)
);
CREATE INDEX entry_links_to_idx ON entry_links (to_id);

-- [[123]] mentions inside bodies; rebuilt on every save, gives backlinks.
CREATE TABLE entry_refs (
    from_id  BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    to_id    BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    PRIMARY KEY (from_id, to_id)
);
CREATE INDEX entry_refs_to_idx ON entry_refs (to_id);

CREATE TABLE stars (
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entry_id    BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, entry_id)
);

CREATE TABLE attachments (
    id            TEXT PRIMARY KEY,           -- random id, also the file name on disk
    entry_id      BIGINT REFERENCES entries(id) ON DELETE SET NULL,
    filename      TEXT NOT NULL,
    content_type  TEXT NOT NULL,
    size          BIGINT NOT NULL,
    sha256        TEXT NOT NULL,
    uploaded_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX attachments_entry_idx ON attachments (entry_id);

CREATE TABLE share_links (
    token       TEXT PRIMARY KEY,
    entry_id    BIGINT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ,
    views       BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX share_links_entry_idx ON share_links (entry_id);

CREATE TABLE webhooks (
    id          BIGSERIAL PRIMARY KEY,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,
    events      TEXT[] NOT NULL DEFAULT '{*}',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_status TEXT NOT NULL DEFAULT '',
    last_at     TIMESTAMPTZ
);

CREATE TABLE audit_log (
    id       BIGSERIAL PRIMARY KEY,
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action   TEXT NOT NULL,
    target   TEXT NOT NULL DEFAULT '',
    detail   TEXT NOT NULL DEFAULT '',
    ip       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);

-- ---------------------------------------------------------------- seed data

INSERT INTO kinds (key, label, plural, icon, color, description, statuses, has_solutions, fields, sort, template) VALUES
('problem', 'Problem', 'Problems', 'bug', '#e5484d',
 'Something broke. Document symptoms, environment and what fixed it.',
 '{open,investigating,solved,workaround,wontfix}', TRUE,
 '[{"key":"os","label":"OS / Platform","placeholder":"Debian 13, Windows 11, macOS 26 ..."},
   {"key":"software","label":"Software","placeholder":"nginx, Docker, Postgres ..."},
   {"key":"version","label":"Version","placeholder":"1.27.2"},
   {"key":"host","label":"Host / Device","placeholder":"k-inf1, laptop ..."}]', 10,
E'## Symptoms\n\nWhat happens? What did you expect instead?\n\n## Environment\n\n- \n\n## Steps to reproduce\n\n1. \n\n## Root cause\n\n_Fill in once known._\n'),
('howto', 'How-to', 'How-tos', 'compass', '#3e63dd',
 'Step-by-step instructions for getting something done.',
 '{}', FALSE,
 '[{"key":"software","label":"Software","placeholder":""},{"key":"version","label":"Tested with","placeholder":""}]', 20,
E'## Goal\n\n\n## Prerequisites\n\n- \n\n## Steps\n\n1. \n\n## Verify\n\n'),
('runbook', 'Runbook', 'Runbooks', 'terminal', '#12a594',
 'Operational procedures: deploys, restores, incident playbooks.',
 '{current,outdated}', FALSE,
 '[{"key":"service","label":"Service","placeholder":""},{"key":"host","label":"Host","placeholder":""}]', 30,
E'## When to use\n\n\n## Procedure\n\n```sh\n\n```\n\n## Rollback\n\n'),
('snippet', 'Snippet', 'Snippets', 'code', '#8e4ec6',
 'Reusable commands, configs and code fragments.',
 '{}', FALSE,
 '[{"key":"language","label":"Language","placeholder":"bash, go, yaml ..."}]', 40,
E'```sh\n\n```\n'),
('note', 'Note', 'Notes', 'note', '#f76b15',
 'Anything else worth remembering.',
 '{}', FALSE, '[]', 50, ''),
('reference', 'Reference', 'References', 'link', '#0090ff',
 'Links, documentation and external resources with your own annotations.',
 '{}', FALSE,
 '[{"key":"url","label":"URL","placeholder":"https://"}]', 60, '');

INSERT INTO spaces (slug, name, description, icon, color) VALUES
('general', 'General', 'Everything that does not belong anywhere else.', 'folder', '#5b6cff');
