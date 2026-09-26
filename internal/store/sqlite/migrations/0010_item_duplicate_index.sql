-- 0010: index items by duplicate-candidate identity (R19, D10, FR-ING-005):
-- content hash, task, directive section, role, authority, and exact access
-- boundary. Rows written before 0004 hold NULL in f_role, which already
-- reads as the semantic role (''); it is stored as '' so equality lookups
-- see those rows too. No other value changes.
UPDATE rec_item SET f_role = '' WHERE f_role IS NULL;
CREATE INDEX item_duplicate ON rec_item(session_id, f_content_hash, f_task_id, f_section, f_role, f_authority,
  f_access_scope, f_access_session_id, f_access_workflow_id, f_access_task_id, f_access_agent_id);
