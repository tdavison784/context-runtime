-- 0005: typed namespace in the current-version key (M6, R6).
-- A parsed directive and keyed agent state (FR-TOOL-002) may share an ID
-- such as "agent.status" (FR-DIR-006), so the key gains a namespace.
-- Existing pointers take the namespace of the item they name, exactly as
-- domain.ContextItem.DirectiveNamespace derives it: a directive section means
-- DIRECTIVE, none means AGENT_KEY. A pointer whose item cannot be read is
-- kept as AGENT_KEY, so lifecycle resolution (DIRECTIVE only) never acts on
-- it.
CREATE TABLE directives_v2 (
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  namespace TEXT NOT NULL CHECK(namespace IN ('DIRECTIVE','AGENT_KEY')),
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
INSERT INTO directives_v2(session_id,task_id,namespace,directive_id,boundary_scope,boundary_session_id,boundary_workflow_id,boundary_task_id,boundary_agent_id,item_id)
SELECT d.session_id, d.task_id,
  CASE WHEN COALESCE(i.f_section, '') <> '' THEN 'DIRECTIVE' ELSE 'AGENT_KEY' END,
  d.directive_id, d.boundary_scope, d.boundary_session_id, d.boundary_workflow_id, d.boundary_task_id, d.boundary_agent_id, d.item_id
FROM directives AS d
LEFT JOIN rec_item AS i ON i.session_id = d.session_id AND i.id = d.item_id AND i.subkey = 0;
DROP TABLE directives;
ALTER TABLE directives_v2 RENAME TO directives;
