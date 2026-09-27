-- 0032: live-only subject states by resource (G2, SEC-1.8, DUR-1.2).
-- SubjectStatesByResource returns only CURRENT states, so STALE and
-- UNKNOWN history never costs a report's invalidation work. The partial
-- index holds exactly the CURRENT rows in first-filing order; a state
-- enters and leaves it as its applicability changes.
CREATE INDEX subject_state_resource_current ON rec_subject_state(session_id,f_resource,f_first_seq,f_state_semantic_meta_id)
  WHERE f_state_applicability = 'CURRENT';
