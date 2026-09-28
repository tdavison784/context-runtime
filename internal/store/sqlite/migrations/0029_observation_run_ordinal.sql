-- 0029: an observation run's second key is (subject, Ordinal) (P3-22,
-- SEC-1.13). Two runs of one subject sharing an ordinal would make run
-- order, and so the subject watermark, ambiguous. No Phase 2 database has
-- runs, so the index builds over an empty or already-unique table.
CREATE UNIQUE INDEX observation_run_ordinal ON rec_observation_run(session_id,f_subject_key,f_ordinal);
