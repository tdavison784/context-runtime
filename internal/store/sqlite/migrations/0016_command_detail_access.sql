-- 0016: lifecycle-command records carry DetailAccess, the boundary that may
-- read their resolution (SEC-3.2); the record itself stays readable at its
-- transcript boundary. Records written before 0016 keep NULL, which reads
-- as the zero boundary and means "same as Access": their resolution stays
-- exactly as visible as it was when recorded (M8).
ALTER TABLE rec_command ADD COLUMN f_detail_access_scope TEXT;
ALTER TABLE rec_command ADD COLUMN f_detail_access_session_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_detail_access_workflow_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_detail_access_task_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_detail_access_agent_id TEXT;
