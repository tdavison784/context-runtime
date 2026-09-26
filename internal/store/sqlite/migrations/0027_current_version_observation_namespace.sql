-- 0027: admit every domain namespace in the current-version key (P3-3,
-- P3-22). Migration 0005's CHECK admitted only DIRECTIVE and AGENT_KEY, so
-- an OBSERVATION current-state pointer could not be filed. SQLite cannot
-- alter a CHECK, so the table is rebuilt with the same columns and key and
-- every existing pointer is copied unchanged. The admitted set equals the
-- domain's DirectiveNamespace constants; TestCurrentKeyNamespacesMatchDomain
-- fails if a namespace is added to the domain without a migration here.
CREATE TABLE directives_v3 (
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  namespace TEXT NOT NULL CHECK(namespace IN ('DIRECTIVE','AGENT_KEY','OBSERVATION')),
  directive_id TEXT NOT NULL,
  boundary_scope TEXT NOT NULL,
  boundary_session_id TEXT NOT NULL,
  boundary_workflow_id TEXT NOT NULL,
  boundary_task_id TEXT NOT NULL,
  boundary_agent_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  PRIMARY KEY(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id),
  FOREIGN KEY(session_id) REFERENCES sessions(session_id)
);
INSERT INTO directives_v3(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id)
SELECT session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id
FROM directives;
DROP TABLE directives;
ALTER TABLE directives_v3 RENAME TO directives;
