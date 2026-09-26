-- 0020: Phase 3 coverage, logical membership, checkpoint, owner, and
-- request-receipt companions (P3-2/6/7/24/27/32). Each companion record has
-- its own typed rec_* table keyed by (session, ID) like every earlier
-- record; coverage members are keyed by (coverage, ordinal) in canonical
-- key order, and a conversation's membership state by its conversation.
-- Uniqueness the domain requires (owner per kind, exchange ordinal and
-- member position, one acknowledgment per exchange, one checkpoint per
-- item, one receipt per request) is a unique index. No earlier rows exist:
-- no migration invents membership, coverage, owners, or receipts (P3-41).
CREATE TABLE rec_owner (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_kind TEXT,
  f_owner_id TEXT,
  f_workflow_id TEXT,
  f_source_id TEXT,
  f_actor_session_id TEXT,
  f_actor_workflow_id TEXT,
  f_actor_task_id TEXT,
  f_actor_agent_id TEXT,
  f_actor_authority TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_coverage (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_purpose TEXT,
  f_access_scope TEXT,
  f_access_session_id TEXT,
  f_access_workflow_id TEXT,
  f_access_task_id TEXT,
  f_access_agent_id TEXT,
  f_conversation_id TEXT,
  f_membership_revision INTEGER,
  f_closed_frontier INTEGER,
  f_member_count INTEGER,
  f_signature TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_coverage_member (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_member_semantic_meta_id TEXT,
  f_member_semantic_meta_session_id TEXT,
  f_member_semantic_meta_schema_version TEXT,
  f_member_semantic_meta_seq INTEGER,
  f_member_coverage_id TEXT,
  f_member_source_present INTEGER NOT NULL DEFAULT 0,
  f_member_source_item_id TEXT,
  f_member_source_content_hash TEXT,
  f_member_lease_id TEXT,
  f_member_nested_coverage_id TEXT,
  f_member_exchange_id TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_exchange (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_conversation_id TEXT,
  f_ordinal INTEGER,
  f_principal_session_id TEXT,
  f_principal_workflow_id TEXT,
  f_principal_task_id TEXT,
  f_principal_agent_id TEXT,
  f_principal_authority TEXT,
  f_turn_id TEXT,
  f_turn INTEGER,
  f_state TEXT,
  f_acknowledgment_id TEXT,
  f_revision INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_exchange_member (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_exchange_id TEXT,
  f_position INTEGER,
  f_role TEXT,
  f_source_item_id TEXT,
  f_source_content_hash TEXT,
  f_call_id TEXT,
  f_tool_call_id TEXT,
  f_admission_id TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_exchange_ack (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_exchange_id TEXT,
  f_manifest_id TEXT,
  f_consuming_call_id TEXT,
  f_cancellation_reason TEXT,
  f_actor_session_id TEXT,
  f_actor_workflow_id TEXT,
  f_actor_task_id TEXT,
  f_actor_agent_id TEXT,
  f_actor_authority TEXT,
  f_cancelled INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_admission (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_conversation_id TEXT,
  f_exchange_id TEXT,
  f_call_id TEXT,
  f_principal_session_id TEXT,
  f_principal_workflow_id TEXT,
  f_principal_task_id TEXT,
  f_principal_agent_id TEXT,
  f_principal_authority TEXT,
  f_turn_id TEXT,
  f_purpose TEXT,
  f_coverage_id TEXT,
  f_membership_revision INTEGER,
  f_policy_version TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_membership (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_id TEXT,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_revision INTEGER,
  f_last_ordinal INTEGER,
  f_closed_frontier INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_checkpoint (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_item_id TEXT,
  f_conversation_id TEXT,
  f_issuing_exchange_id TEXT,
  f_generation_manifest_id TEXT,
  f_snapshot_seq INTEGER,
  f_membership_revision INTEGER,
  f_covered_frontier INTEGER,
  f_source_coverage_id TEXT,
  f_covered_exchanges_id TEXT,
  f_prior_checkpoint_id TEXT,
  f_policy_version TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_mutation_receipt (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_family TEXT,
  f_request_id TEXT,
  f_principal_session_id TEXT,
  f_principal_workflow_id TEXT,
  f_principal_task_id TEXT,
  f_principal_agent_id TEXT,
  f_principal_authority TEXT,
  f_canonical_method TEXT,
  f_canonical_arguments_nil INTEGER NOT NULL DEFAULT 0,
  f_canonical_arguments BLOB,
  f_request_hash_version TEXT,
  f_request_hash TEXT,
  f_policy_version TEXT,
  f_result TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE rec_tool_receipt (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_schema_version TEXT,
  f_seq INTEGER,
  f_invocation_session_id TEXT,
  f_invocation_conversation_id TEXT,
  f_invocation_call_id TEXT,
  f_invocation_tool_call_id TEXT,
  f_invocation_exchange_id TEXT,
  f_invocation_turn_id TEXT,
  f_invocation_principal_session_id TEXT,
  f_invocation_principal_workflow_id TEXT,
  f_invocation_principal_task_id TEXT,
  f_invocation_principal_agent_id TEXT,
  f_invocation_principal_authority TEXT,
  f_method TEXT,
  f_mutation_receipt_id TEXT,
  f_request_hash TEXT,
  f_result TEXT,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
-- The exact reverse source index of normalized coverage (P3-6): one row per
-- (source item, purpose, coverage), in the coverage's (Seq, ID) order.
CREATE TABLE lookup_coverage_source (
  session_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  purpose TEXT NOT NULL,
  seq INTEGER NOT NULL,
  coverage_id TEXT NOT NULL,
  PRIMARY KEY (session_id,item_id,purpose,seq,coverage_id)
);
CREATE UNIQUE INDEX owner_key ON rec_owner(session_id,f_kind,f_owner_id);
CREATE UNIQUE INDEX exchange_ordinal ON rec_exchange(session_id,f_conversation_id,f_ordinal);
CREATE INDEX exchange_conversation_seq ON rec_exchange(session_id,f_conversation_id,f_seq,id);
CREATE INDEX exchange_open_task ON rec_exchange(session_id,f_principal_task_id,f_seq,id) WHERE f_state IN ('OPEN','EXECUTING');
CREATE UNIQUE INDEX exchange_member_position ON rec_exchange_member(session_id,f_exchange_id,f_position);
CREATE INDEX exchange_member_seq ON rec_exchange_member(session_id,f_exchange_id,f_seq,id);
CREATE INDEX exchange_member_item ON rec_exchange_member(session_id,f_source_item_id,f_seq,id);
CREATE UNIQUE INDEX exchange_ack_exchange ON rec_exchange_ack(session_id,f_exchange_id);
CREATE INDEX admission_exchange ON rec_admission(session_id,f_exchange_id,f_seq,id);
CREATE UNIQUE INDEX checkpoint_item ON rec_checkpoint(session_id,f_item_id);
CREATE INDEX checkpoint_conversation ON rec_checkpoint(session_id,f_conversation_id,f_seq,id);
CREATE UNIQUE INDEX mutation_receipt_request ON rec_mutation_receipt(session_id,f_family,f_request_id);
-- Reserving calls by the task of their frozen principal, for completion's
-- in-flight check (P3-9): the partial index holds only PREPARED, SENT, and
-- UNKNOWN calls, so the read never walks a task's finished calls.
CREATE INDEX call_reserving_task ON rec_call(session_id,f_principal_task_id,f_prepared_seq,id) WHERE f_state IN ('PREPARED','SENT','UNKNOWN');
