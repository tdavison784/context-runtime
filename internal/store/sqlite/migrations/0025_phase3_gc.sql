-- 0025: Phase 3 GC requests, collect receipts and results, and the
-- indexed reads collection and completion need (P3-9/38/39/41). No
-- request, receipt or result is invented for earlier data.
CREATE TABLE rec_gc_request (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_collect_intent_request_id TEXT,
  f_collect_intent_scope TEXT,
  f_collect_intent_task_id TEXT,
  f_collect_intent_trigger TEXT,
  f_origin_session_id TEXT,
  f_origin_workflow_id TEXT,
  f_origin_task_id TEXT,
  f_origin_agent_id TEXT,
  f_origin_authority TEXT,
  f_policy_version TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_collect_receipt (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_request_id TEXT,
  f_gc_request_id TEXT,
  f_policy_version TEXT,
  f_principal_session_id TEXT,
  f_principal_workflow_id TEXT,
  f_principal_task_id TEXT,
  f_principal_agent_id TEXT,
  f_principal_authority TEXT,
  f_snapshot_seq INTEGER,
  f_candidate_refs TEXT,
  f_decisions TEXT,
  f_archived_refs TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_gc_result (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_id TEXT,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_collect_receipt_id TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE UNIQUE INDEX gc_request_identity ON rec_gc_request(session_id,f_collect_intent_request_id);
CREATE UNIQUE INDEX gc_result_id ON rec_gc_result(session_id,f_id);
-- Requests without a result, maintained with the request and its result,
-- so the pending read never walks completed history.
CREATE TABLE lookup_pending_gc (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  request_id TEXT NOT NULL,
  PRIMARY KEY (session_id,seq,request_id)
);
-- Resident items by task and by session for collection candidates, and
-- OPEN goals whose declared scope is TURN or TASK for completion.
CREATE INDEX item_resident_task ON rec_item(session_id,f_task_id,f_seq,id) WHERE f_residency='RESIDENT';
CREATE INDEX item_resident ON rec_item(session_id,f_seq,id) WHERE f_residency='RESIDENT';
CREATE INDEX item_open_task_goal ON rec_item(session_id,f_task_id,f_seq,id) WHERE f_kind='goal' AND f_goal_status='OPEN' AND f_scope IN ('TASK','TURN');
