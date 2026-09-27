-- 0038: the current version of each workspace binding, by context (H2).
-- CurrentWorkspaceBindingsByContext pages one row per binding ID, at its
-- latest version and in that version's context, so a page counts live
-- bindings rather than versions. InsertWorkspaceBinding moves the row to
-- each new version; the backfill files every binding's latest version.
CREATE TABLE lookup_current_workspace_binding (
  session_id TEXT NOT NULL,
  context_kind TEXT NOT NULL,
  context_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  binding_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  PRIMARY KEY (session_id,context_kind,context_id,seq,binding_id)
);
CREATE UNIQUE INDEX current_workspace_binding_id ON lookup_current_workspace_binding(session_id,binding_id);
INSERT INTO lookup_current_workspace_binding(session_id,context_kind,context_id,seq,binding_id,version)
SELECT w.session_id, w.f_context_kind, w.f_context_id, w.f_seq, w.id, w.subkey
FROM rec_workspace_binding AS w
WHERE w.subkey = (SELECT MAX(v.subkey) FROM rec_workspace_binding AS v WHERE v.session_id = w.session_id AND v.id = w.id);
