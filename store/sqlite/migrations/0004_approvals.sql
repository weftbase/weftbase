-- Approvals: one row per call a run parked on, created with the park.
-- status: pending | approved | denied | resolved | expired | cancelled.
-- A decision records who made it; resumed_by is the run that continued
-- the parked one once every call of it was decided.
CREATE TABLE approvals (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL,
  env           TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  run_id        TEXT NOT NULL REFERENCES runs (id),
  call_id       TEXT NOT NULL,
  agent         TEXT NOT NULL,
  agent_version TEXT NOT NULL,
  tool          TEXT NOT NULL,
  args          TEXT NOT NULL,
  args_sha256   TEXT NOT NULL,
  status        TEXT NOT NULL,
  reason        TEXT NOT NULL DEFAULT '',
  content       TEXT NOT NULL DEFAULT '',
  actor_kind    TEXT NOT NULL DEFAULT '',
  actor_id      TEXT NOT NULL DEFAULT '',
  resumed_by    TEXT NOT NULL DEFAULT '',
  requested_at  TEXT NOT NULL,
  decided_at    TEXT,
  UNIQUE (run_id, call_id)
);
CREATE INDEX approvals_scope ON approvals (project_id, env, tenant_id, status, requested_at);

-- A build remembers the placement that registered it (embedded | local
-- | remote), so a version says where its runs execute.
ALTER TABLE builds ADD COLUMN placement TEXT NOT NULL DEFAULT '';
