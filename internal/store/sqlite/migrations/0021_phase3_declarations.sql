-- 0021: Phase 3 creation and snapshot declarations, semantic change
-- records, and the indexed grant and audit reads (P3-3/4/5/36/39/41).
-- A creation declaration is keyed by its item (one per item) with its own
-- ID unique; absence is unknown identity and nothing is backfilled, so no
-- migration invents a declaration for a Phase 2 item (P3-41).
CREATE TABLE rec_creation_declaration (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_id TEXT,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_policy_version TEXT,
  f_signature TEXT,
  f_legacy_known INTEGER,
  f_accepted_semantics_key_session_id TEXT,
  f_accepted_semantics_key_task_id TEXT,
  f_accepted_semantics_key_access_scope TEXT,
  f_accepted_semantics_key_access_session_id TEXT,
  f_accepted_semantics_key_access_workflow_id TEXT,
  f_accepted_semantics_key_access_task_id TEXT,
  f_accepted_semantics_key_access_agent_id TEXT,
  f_accepted_semantics_key_namespace TEXT,
  f_accepted_semantics_key_id TEXT,
  f_accepted_semantics_authority TEXT,
  f_accepted_semantics_workflow_id TEXT,
  f_accepted_semantics_agent_id TEXT,
  f_accepted_semantics_section TEXT,
  f_accepted_semantics_kind TEXT,
  f_accepted_semantics_content_hash TEXT,
  f_accepted_semantics_obligation_declaration_hash TEXT,
  f_accepted_semantics_accepted_attributes TEXT,
  f_accepted_semantics_support_ids TEXT,
  f_accepted_semantics_generation TEXT,
  f_accepted_semantics_retention TEXT,
  f_accepted_semantics_residency TEXT,
  f_accepted_semantics_goal_status_present INTEGER NOT NULL DEFAULT 0,
  f_accepted_semantics_goal_status TEXT,
  f_accepted_semantics_origin_task_id TEXT,
  f_accepted_semantics_origin_turn_id TEXT,
  f_accepted_semantics_created_turn INTEGER,
  f_accepted_semantics_ttl_turns_present INTEGER NOT NULL DEFAULT 0,
  f_accepted_semantics_ttl_turns INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_snapshot_declaration (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_task_id TEXT,
  f_authority TEXT,
  f_access_scope TEXT,
  f_access_session_id TEXT,
  f_access_workflow_id TEXT,
  f_access_task_id TEXT,
  f_access_agent_id TEXT,
  f_policy_version TEXT,
  f_members TEXT,
  f_signature TEXT,
  f_legacy_known INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_semantic_change (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_target_kind TEXT,
  f_target_session_id TEXT,
  f_target_item_id TEXT,
  f_target_obligation_id TEXT,
  f_target_version INTEGER,
  f_target_authorization_key TEXT,
  f_source_authority TEXT,
  f_actor_session_id TEXT,
  f_actor_workflow_id TEXT,
  f_actor_task_id TEXT,
  f_actor_agent_id TEXT,
  f_actor_authority TEXT,
  f_access_scope TEXT,
  f_access_session_id TEXT,
  f_access_workflow_id TEXT,
  f_access_task_id TEXT,
  f_access_agent_id TEXT,
  f_action TEXT,
  f_cause TEXT,
  f_before_revision INTEGER,
  f_after_revision INTEGER,
  f_before_status TEXT,
  f_after_status TEXT,
  f_before_currentness TEXT,
  f_after_currentness TEXT,
  f_audit_id TEXT,
  f_cause_id TEXT,
  f_grant_id TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE UNIQUE INDEX creation_declaration_id ON rec_creation_declaration(session_id,f_id);
CREATE INDEX semantic_change_target ON rec_semantic_change(session_id,f_target_authorization_key,f_seq,id);
-- Audit events by exact target, for LifecycleByTarget and completion and
-- change inspection without a whole-session scan.
CREATE INDEX lifecycle_target ON rec_lifecycle(session_id,f_target_kind,f_target_id,f_seq,id);
-- Grants by each exact (action, target) they name (P3-5). target_key is
-- 'typed:' plus the hex of a typed target's canonical authorization key, or
-- 'legacy-item:' plus the hex of a legacy TargetIDs entry: stored strings
-- are hex in lossless lists, so both forms are derived here without
-- decoding, and the prefixes keep them from aliasing. Legacy entries are
-- read only for item targets and item actions, so a stable-ID obligation
-- grant never authorizes an exact obligation version.
CREATE TABLE lookup_grant_target (
  session_id TEXT NOT NULL,
  action TEXT NOT NULL,
  target_key TEXT NOT NULL,
  issued_seq INTEGER NOT NULL,
  grant_id TEXT NOT NULL,
  PRIMARY KEY (session_id,action,target_key,issued_seq,grant_id)
);
INSERT INTO lookup_grant_target(session_id,action,target_key,issued_seq,grant_id)
SELECT g.session_id, g.f_action, 'legacy-item:' || j.value, g.f_issued_seq, g.id
FROM rec_grant AS g, json_each(g.f_target_ids) AS j
WHERE g.f_target_ids IS NOT NULL AND g.f_target_ids != 'null';
INSERT INTO lookup_grant_target(session_id,action,target_key,issued_seq,grant_id)
SELECT g.session_id, g.f_action, 'typed:' || json_extract(j.value, '$.AuthorizationKey'), g.f_issued_seq, g.id
FROM rec_grant AS g, json_each(g.f_targets) AS j
WHERE g.f_targets IS NOT NULL AND g.f_targets != 'null';
