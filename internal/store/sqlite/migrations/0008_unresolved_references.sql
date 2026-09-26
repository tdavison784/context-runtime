-- 0008: unresolved references (M5, R2, R18).
-- A References entry whose path or URL matched no ingested item is kept with
-- its ownership context (access boundary and authority), keyed by an ID
-- derived from its occurrence and ordinal, so a restart never loses a
-- pending link. Locator keys are exact bytes. No earlier rows exist.
CREATE TABLE rec_reference (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_occurrence_id TEXT,
  f_ordinal INTEGER,
  f_span_index INTEGER,
  f_item_id TEXT,
  f_locator_key TEXT,
  f_rule_version TEXT,
  f_access_scope TEXT,
  f_access_session_id TEXT,
  f_access_workflow_id TEXT,
  f_access_task_id TEXT,
  f_access_agent_id TEXT,
  f_authority TEXT,
  f_seq INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE INDEX reference_locator ON rec_reference(session_id,f_locator_key,f_rule_version,f_seq,id);
