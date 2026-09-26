-- 0019: store a lifecycle command's execution result whole (P3-35).
-- Migration 0018 flattened CommandExecutionDetail.Result into one column per
-- nested field, but the result's Before.Version and its BeforeVersion (and
-- After.Version/AfterVersion) flatten to the same column name, so the two
-- fields would have shared one column. The result is immutable and never
-- filtered, so it is now one lossless-encoded value, like the other
-- immutable result unions. No Phase 3 command record exists before this
-- migration (0018 added the columns in the same unreleased series), so the
-- dropped columns hold only NULLs.
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_authority;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_currentness;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_expiry;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_generation;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_goal_status;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_goal_status_present;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_residency;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_source_content_hash;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_source_item_id;
ALTER TABLE rec_command DROP COLUMN f_execution_result_after_version;
ALTER TABLE rec_command DROP COLUMN f_execution_result_audit_id;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_authority;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_currentness;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_expiry;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_generation;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_goal_status;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_goal_status_present;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_residency;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_source_content_hash;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_source_item_id;
ALTER TABLE rec_command DROP COLUMN f_execution_result_before_version;
ALTER TABLE rec_command DROP COLUMN f_execution_result_explicit_protected_removal;
ALTER TABLE rec_command DROP COLUMN f_execution_result_item_id;
ALTER TABLE rec_command ADD COLUMN f_execution_result TEXT;
