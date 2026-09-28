-- 0050: freeze the GC candidate viewer (SPEC-5.2). rec_gc_progress gains
-- the principal whose visibility paged batch 1, so every later batch of
-- the same request — run by any authorized collector (SEC-4.5) — pages the
-- same frozen candidate set (J1/J2) instead of one re-derived from its own
-- collector. Per-target Archive authorization stays with each batch's
-- executing principal (P3-38); a candidate that principal cannot access
-- gets an explicit INELIGIBLE decision in the batch receipt. Rows written
-- before this migration backfill lazily: lifecycle recovers the viewer
-- from batch 1's committed collect receipt (its Principal), so pre-0050
-- requests keep exactly the candidate set their first batch saw and no
-- SQL backfill is needed. Numbering skips 0049, reserved for the K1-api.3
-- confirmation-record migration on another branch; both merge in order.
ALTER TABLE rec_gc_progress ADD COLUMN f_viewer_session_id TEXT;
ALTER TABLE rec_gc_progress ADD COLUMN f_viewer_workflow_id TEXT;
ALTER TABLE rec_gc_progress ADD COLUMN f_viewer_task_id TEXT;
ALTER TABLE rec_gc_progress ADD COLUMN f_viewer_agent_id TEXT;
ALTER TABLE rec_gc_progress ADD COLUMN f_viewer_authority TEXT;
