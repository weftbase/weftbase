-- The ledger's chains, sessions and one status spelling.
--
-- chain_id is the first run of the park-and-resume chain a run belongs
-- to (the run itself when it resumes nothing): a page of runs resolves
-- its chains with one query instead of walking resume_of row by row.
-- session_id and turn are the weft.session.id and weft.turn the ledger
-- stamped on a session's turn (a resume carries its parked run's): the
-- admission of the next turn checks the session and numbers the turn
-- inside its transaction. A cancelled run reads "cancelled", the one
-- spelling of the API.
ALTER TABLE runs ADD COLUMN chain_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN turn INTEGER NOT NULL DEFAULT 0;

UPDATE runs SET
  session_id = COALESCE(json_extract(metadata, '$."weft.session.id"'), ''),
  turn = CAST(COALESCE(json_extract(metadata, '$."weft.turn"'), '0') AS INTEGER)
WHERE json_valid(metadata);

WITH RECURSIVE chain (id, head) AS (
  SELECT id, id FROM runs WHERE resume_of = ''
  UNION ALL
  SELECT r.id, c.head FROM runs AS r JOIN chain AS c ON r.resume_of = c.id
)
UPDATE runs SET chain_id = COALESCE((SELECT head FROM chain WHERE chain.id = runs.id), id);

UPDATE runs SET status = 'cancelled' WHERE status = 'canceled';

CREATE INDEX runs_chain ON runs (project_id, env, chain_id);
CREATE INDEX runs_session ON runs (project_id, env, session_id) WHERE session_id <> '';
CREATE INDEX runs_status ON runs (project_id, env, status, created_at);
