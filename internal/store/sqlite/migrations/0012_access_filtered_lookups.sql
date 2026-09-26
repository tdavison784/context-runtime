-- 0012: access-filtered, exact-key ingest lookups (F1: SEC-1.1, SEC-1.2,
-- DUR-1.1, SPEC-1.3).
-- Each lookup table carries the item's owner columns (workflow, task,
-- agent) so a viewer's access filter is an index equality probe applied
-- before any limit; records a viewer cannot see are never counted.
-- lookup_canonical, lookup_working, and lookup_source hold live items only
-- (no incoming SUPERSEDES, no outgoing DUPLICATE_OF); the store deletes an
-- item's rows in the same write that retires it. Existing rows are
-- backfilled below with the same rules. relationship_to indexes the
-- liveness checks and graph reads by target.
CREATE INDEX relationship_to ON rec_relationship(session_id, f_type, f_to_id);

CREATE TABLE lookup_blob (
  session_id TEXT NOT NULL, blob_hash TEXT NOT NULL,
  workflow_id TEXT NOT NULL, task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  seq INTEGER NOT NULL, item_id TEXT NOT NULL,
  PRIMARY KEY (session_id, blob_hash, workflow_id, task_id, agent_id, seq, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT INTO lookup_blob
SELECT b.session_id, b.blob_hash, i.f_access_workflow_id, i.f_access_task_id, i.f_access_agent_id, i.f_seq, i.id
FROM item_blobs AS b JOIN rec_item AS i ON i.session_id = b.session_id AND i.id = b.item_id AND i.subkey = 0;

CREATE TABLE lookup_canonical (
  session_id TEXT NOT NULL, content_hash TEXT NOT NULL, task_id TEXT NOT NULL,
  section TEXT NOT NULL, directive_id TEXT NOT NULL, kind TEXT NOT NULL, role TEXT NOT NULL, authority TEXT NOT NULL,
  scope TEXT NOT NULL, access_session_id TEXT NOT NULL, workflow_id TEXT NOT NULL, access_task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  seq INTEGER NOT NULL, item_id TEXT NOT NULL,
  PRIMARY KEY (session_id, content_hash, task_id, section, directive_id, kind, role, authority,
    scope, access_session_id, workflow_id, access_task_id, agent_id, seq, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT INTO lookup_canonical
SELECT i.session_id, i.f_content_hash, i.f_task_id, i.f_section, i.f_directive_id, i.f_kind, COALESCE(i.f_role, ''), i.f_authority,
  i.f_access_scope, i.f_access_session_id, i.f_access_workflow_id, i.f_access_task_id, i.f_access_agent_id, i.f_seq, i.id
FROM rec_item AS i
WHERE i.subkey = 0
  AND NOT EXISTS (SELECT 1 FROM rec_relationship AS r WHERE r.session_id = i.session_id AND r.f_type = 'SUPERSEDES' AND r.f_to_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM rec_relationship AS r WHERE r.session_id = i.session_id AND r.f_type = 'DUPLICATE_OF' AND r.f_from_id = i.id);

CREATE TABLE lookup_working (
  session_id TEXT NOT NULL, task_id TEXT NOT NULL, authority TEXT NOT NULL,
  scope TEXT NOT NULL, access_session_id TEXT NOT NULL, workflow_id TEXT NOT NULL, access_task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  seq INTEGER NOT NULL, item_id TEXT NOT NULL, directive_id TEXT NOT NULL,
  PRIMARY KEY (session_id, task_id, authority, scope, access_session_id, workflow_id, access_task_id, agent_id, seq, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT INTO lookup_working
SELECT session_id, task_id, authority, scope, access_session_id, workflow_id, access_task_id, agent_id, seq, item_id, directive_id
FROM lookup_canonical WHERE section = 'WORKING';

CREATE TABLE lookup_source (
  session_id TEXT NOT NULL, rule_version TEXT NOT NULL, locator_key TEXT NOT NULL,
  workflow_id TEXT NOT NULL, task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  seq INTEGER NOT NULL, item_id TEXT NOT NULL,
  PRIMARY KEY (session_id, rule_version, locator_key, workflow_id, task_id, agent_id, seq, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT INTO lookup_source
SELECT s.session_id, s.rule_version, s.locator_key, i.f_access_workflow_id, i.f_access_task_id, i.f_access_agent_id, i.f_seq, i.id
FROM item_sources AS s JOIN rec_item AS i ON i.session_id = s.session_id AND i.id = s.item_id AND i.subkey = 0
WHERE NOT EXISTS (SELECT 1 FROM rec_relationship AS r WHERE r.session_id = i.session_id AND r.f_type = 'SUPERSEDES' AND r.f_to_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM rec_relationship AS r WHERE r.session_id = i.session_id AND r.f_type = 'DUPLICATE_OF' AND r.f_from_id = i.id);

CREATE INDEX reference_visible ON rec_reference(session_id, f_locator_key, f_rule_version,
  f_access_workflow_id, f_access_task_id, f_access_agent_id, f_seq, id);
