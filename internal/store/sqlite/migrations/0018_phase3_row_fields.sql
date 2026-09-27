-- 0018: Phase 3 fields on existing record tables (P3-3/5/6/12/13/35/40/41).
-- Every new column reads NULL on a row an earlier binary wrote, decoded as
-- the field's zero value, which the domain treats as the frozen legacy form:
-- item Namespace "" is the pre-Phase-3 directive/agent-key fallback, grant
-- Targets nil leaves legacy TargetIDs (never an exact obligation-version
-- target), obligation DeclarationKind "" and transition Cause "" carry no
-- executable binding or proof, relationship CoverageID "" keeps legacy
-- Coverage, lifecycle commands without Execution stay v1
-- PARSED_NOT_EXECUTED, and an envelope/receipt RequestHashVersion "" is the
-- frozen ingest-payload/v2 schema. Nothing is invented for old rows.


ALTER TABLE rec_command ADD COLUMN f_execution_diagnostics TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_grant_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_mutation_receipt_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_outcome TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_authority TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_currentness TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_expiry TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_generation TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_goal_status TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_goal_status_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_residency TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_source_content_hash TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_source_item_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_after_version INTEGER;
ALTER TABLE rec_command ADD COLUMN f_execution_result_audit_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_authority TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_currentness TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_expiry TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_generation TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_goal_status TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_goal_status_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_residency TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_source_content_hash TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_source_item_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_before_version INTEGER;
ALTER TABLE rec_command ADD COLUMN f_execution_result_explicit_protected_removal INTEGER;
ALTER TABLE rec_command ADD COLUMN f_execution_result_item_id TEXT;
ALTER TABLE rec_command ADD COLUMN f_execution_result_present INTEGER NOT NULL DEFAULT 0;

ALTER TABLE rec_envelope ADD COLUMN f_event_control INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_event_operations TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_attribute_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_attributes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_blob_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_diagnostics_per_span INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_event_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_event_diagnostics INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_event_items INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_heading_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_heading_level INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_id_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_items_per_span INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_parts INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_reference_links INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_relationships INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_span_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_limits_max_spans INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_request_hash_version TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_checkpoint_generation TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_checkpoint_retention TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_claim TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_coverage TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_dedup TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_default_lease_calls INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_eligibility TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_locator TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_matcher TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_checkpoint_semantic_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_coverage_members INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_evidence INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_gc_decisions INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_lease_calls INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_metadata_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_operations INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_page_size INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_receipt_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_targets INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_tool_result_bytes INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_transaction_work INTEGER;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_observation_state TEXT;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_version TEXT;

ALTER TABLE rec_event ADD COLUMN f_request_hash_version TEXT;

ALTER TABLE rec_grant ADD COLUMN f_targets TEXT;

ALTER TABLE rec_item ADD COLUMN f_namespace TEXT;

ALTER TABLE rec_obligation ADD COLUMN f_binding_reason TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_binding_state TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_claim_pattern_version TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_current_assertion_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_current_proof_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_kind TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_actor_agent_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_actor_authority TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_actor_session_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_actor_task_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_actor_workflow_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_event_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_grant_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_operation_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_provenance_source_item_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_declaration_slot TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_legacy INTEGER;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_locator_base_dir TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_locator_path TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_locator_resource_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_mode TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_file_required_hash TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_base_dir TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_coverage_spec TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_environment_spec TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_resource_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_suite_spec TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_spec_tests_working_dir TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_target_subject_key TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_workspace_binding_ref_id TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_workspace_binding_ref_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_obligation ADD COLUMN f_workspace_binding_ref_version INTEGER;

ALTER TABLE rec_obligation_transition ADD COLUMN f_assertion_mode TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_cause TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_cause_record_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_actor_agent_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_actor_authority TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_actor_session_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_actor_task_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_actor_workflow_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_grant_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_seq INTEGER;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_target_obligation_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_target_session_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_target_version INTEGER;
ALTER TABLE rec_obligation_transition ADD COLUMN f_origin_authorization_ref_transition_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_prior_proof_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_proof_id TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_rationale TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_reason_code TEXT;
ALTER TABLE rec_obligation_transition ADD COLUMN f_request_id TEXT;

ALTER TABLE rec_receipt ADD COLUMN f_mutation_receipt_ids TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_operations TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_request_hash_version TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_checkpoint_generation TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_checkpoint_retention TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_claim TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_coverage TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_dedup TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_default_lease_calls INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_eligibility TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_locator TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_matcher TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_checkpoint_semantic_bytes INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_coverage_members INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_evidence INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_gc_decisions INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_lease_calls INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_metadata_bytes INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_operations INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_page_size INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_receipt_bytes INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_targets INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_tool_result_bytes INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_transaction_work INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_observation_state TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_present INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_version TEXT;

ALTER TABLE rec_receipt_item ADD COLUMN f_item_namespace TEXT;

ALTER TABLE rec_relationship ADD COLUMN f_coverage_id TEXT;

-- An event record whose request no replayable envelope preserves cannot
-- prove which request schema its PayloadHash used, so its identity is
-- "unknown" and never authorizes replay (P3-40, fail closed). Every caller
-- keyed event since 0007 has an envelope with its event ID.
UPDATE rec_event SET f_request_hash_version = 'unknown'
WHERE NOT EXISTS (
  SELECT 1 FROM rec_envelope e
  WHERE e.session_id = rec_event.session_id AND e.f_event_id = rec_event.id
);
