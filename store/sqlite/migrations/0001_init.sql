-- The control store's first schema: the events outbox, its relay
-- cursors, API keys and Console sessions. Every tenant-scoped table
-- leads its keys with (project_id, env, tenant_id).

CREATE TABLE events (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  id            TEXT NOT NULL UNIQUE,
  project_id    TEXT NOT NULL,
  env           TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  type          TEXT NOT NULL,
  resource_kind TEXT NOT NULL,
  resource_id   TEXT NOT NULL,
  actor         TEXT NOT NULL,
  data          TEXT NOT NULL,
  audited       INTEGER NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX events_tenant_seq ON events (project_id, env, tenant_id, seq);
CREATE INDEX events_resource ON events (project_id, env, resource_kind, resource_id, seq);

CREATE TABLE event_cursors (
  consumer   TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);

-- API keys: only a hash of the secret is stored. tenant_id is the
-- tenant a tenant-bound key is restricted to, ten_project for a
-- project-level key.
CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  env          TEXT NOT NULL,
  tenant_id    TEXT NOT NULL,
  kind         TEXT NOT NULL,
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL,
  secret_hash  BLOB NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  revoked_at   TEXT,
  last_used_at TEXT
);
CREATE INDEX api_keys_scope ON api_keys (project_id, env, tenant_id, created_at);

-- One-time sign-in links (printed by `weftbase dev`) and the Console
-- sessions they are exchanged for. Only hashes are stored.
CREATE TABLE console_links (
  token_hash BLOB PRIMARY KEY,
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at    TEXT
);

CREATE TABLE console_sessions (
  id_hash    BLOB PRIMARY KEY,
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  operator   TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
