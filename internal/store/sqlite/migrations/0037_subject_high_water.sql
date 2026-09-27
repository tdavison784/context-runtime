-- 0037: the subject high-water mark (H1, SEC-2.1/SPEC-2.1/DUR-2.1). Per
-- exact (subject, task, access) partition, the highest run ordinal with a
-- complete PASS or FAIL, raised with every such observation whatever its
-- fingerprint or applicability. partition_key is subjectPartitionKey in
-- semantic_resource.go (lowercase hex of each part joined by '.'), which
-- the backfill derives from the run's columns.
CREATE TABLE lookup_subject_high_water (
  session_id TEXT NOT NULL,
  partition_key TEXT NOT NULL,
  high_water INTEGER NOT NULL,
  PRIMARY KEY (session_id,partition_key)
);
INSERT INTO lookup_subject_high_water(session_id,partition_key,high_water)
SELECT r.session_id,
  lower(hex(COALESCE(r.f_subject_key,''))) || '.' || lower(hex(COALESCE(r.f_task_id,''))) || '.' ||
  lower(hex(COALESCE(r.f_access_scope,''))) || '.' || lower(hex(COALESCE(r.f_access_session_id,''))) || '.' ||
  lower(hex(COALESCE(r.f_access_workflow_id,''))) || '.' || lower(hex(COALESCE(r.f_access_task_id,''))) || '.' ||
  lower(hex(COALESCE(r.f_access_agent_id,''))),
  MAX(r.f_ordinal)
FROM rec_observation AS o JOIN rec_observation_run AS r ON r.session_id = o.session_id AND r.id = o.f_run_id AND r.subkey = 0
WHERE o.subkey = 0 AND o.f_completeness = 'COMPLETE' AND o.f_outcome IN ('PASS','FAIL')
GROUP BY 1, 2;
