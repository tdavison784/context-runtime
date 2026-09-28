-- 0030: a run has at most one closing observation (P3-16/22, DUR-1.1,
-- G1): a complete PASS/FAIL, or an ERROR, TIMEOUT or CANCELLED. The
-- predicate is closingObservation in semantic_resource.go. No Phase 2
-- database has observations, so the index builds over an empty table or
-- one the services already kept to a single closing outcome per run.
CREATE UNIQUE INDEX observation_run_closing ON rec_observation(session_id,f_run_id)
  WHERE f_outcome IN ('ERROR','TIMEOUT','CANCELLED') OR f_completeness='COMPLETE' AND f_outcome IN ('PASS','FAIL');
