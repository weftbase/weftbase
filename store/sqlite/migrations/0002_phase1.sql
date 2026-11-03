-- Phase 1: the registry (agents, builds, manifests, AgentVersions,
-- labels), the queue (items with leases and fencing, timers) and the
-- run ledger. The registry is project-level: its rows carry
-- tenant_id ten_project. Every table leads its keys with
-- (project_id, env[, tenant_id]).

CREATE TABLE agents (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (project_id, env, name)
);

CREATE TABLE builds (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  digest     TEXT NOT NULL,
  kind       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (project_id, env, digest)
);

-- Manifests by hash: every one-agent document a build registered,
-- kept forever (a run resolves its manifest_hash after it changed).
CREATE TABLE manifests (
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  hash       TEXT NOT NULL,
  agent      TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, env, hash)
);

-- An AgentVersion: (agent, build digest, manifest hash) is unique;
-- number increments per agent; content_hash = sha256(digest, hash).
CREATE TABLE agent_versions (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL,
  env           TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  agent         TEXT NOT NULL,
  number        INTEGER NOT NULL,
  build_id      TEXT NOT NULL REFERENCES builds (id),
  build_digest  TEXT NOT NULL,
  manifest_hash TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  UNIQUE (project_id, env, agent, number),
  UNIQUE (project_id, env, agent, build_digest, manifest_hash)
);

CREATE TABLE labels (
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  agent      TEXT NOT NULL,
  label      TEXT NOT NULL,
  version_id TEXT NOT NULL REFERENCES agent_versions (id),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, env, agent, label)
);

-- Queue items. status: ready | leased | done | failed. A claim bumps
-- fence; every later write of the lease checks it.
CREATE TABLE queue_items (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  id              TEXT NOT NULL UNIQUE,
  project_id      TEXT NOT NULL,
  env             TEXT NOT NULL,
  tenant_id       TEXT NOT NULL,
  kind            TEXT NOT NULL,
  route           TEXT NOT NULL,
  priority        INTEGER NOT NULL,
  run_at          TEXT NOT NULL,
  concurrency_key TEXT NOT NULL,
  payload         TEXT NOT NULL,
  status          TEXT NOT NULL,
  attempts        INTEGER NOT NULL,
  max_attempts    INTEGER NOT NULL,
  fence           INTEGER NOT NULL,
  lease_owner     TEXT NOT NULL,
  lease_until     TEXT NOT NULL,
  last_error      TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX queue_items_claim ON queue_items (status, route, run_at);
CREATE INDEX queue_items_key ON queue_items (concurrency_key, status);

CREATE TABLE timers (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  env        TEXT NOT NULL,
  tenant_id  TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  due_at     TEXT NOT NULL,
  payload    TEXT NOT NULL,
  fired_at   TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX timers_due ON timers (fired_at, due_at);

-- The run ledger. status: queued | running | parked | succeeded |
-- failed | canceled. resolved_via: label:<l> | latest | pin.
CREATE TABLE runs (
  id               TEXT PRIMARY KEY,
  project_id       TEXT NOT NULL,
  env              TEXT NOT NULL,
  tenant_id        TEXT NOT NULL,
  agent            TEXT NOT NULL,
  agent_version    TEXT NOT NULL REFERENCES agent_versions (id),
  resolved_via     TEXT NOT NULL,
  status           TEXT NOT NULL,
  input            TEXT NOT NULL,
  metadata         TEXT NOT NULL,
  resume_of        TEXT NOT NULL,
  resumed_by       TEXT NOT NULL,
  decisions        TEXT NOT NULL,
  checkpoint       TEXT NOT NULL,
  pending          TEXT NOT NULL,
  command_id       TEXT NOT NULL,
  worker           TEXT NOT NULL,
  error            TEXT NOT NULL,
  cancel_cause     TEXT NOT NULL,
  cancel_requested INTEGER NOT NULL,
  input_tokens     INTEGER NOT NULL,
  output_tokens    INTEGER NOT NULL,
  created_at       TEXT NOT NULL,
  started_at       TEXT,
  finished_at      TEXT,
  updated_at       TEXT NOT NULL
);
CREATE INDEX runs_scope ON runs (project_id, env, tenant_id, created_at);
CREATE INDEX runs_version ON runs (project_id, env, agent_version, status);
