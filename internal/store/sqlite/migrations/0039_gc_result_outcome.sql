-- 0039: a GC request's terminal outcome (H3, SEC-2.4/SPEC-2.4/DUR-2.7).
-- GCResult gains Outcome (COLLECTED or FAILED) and a closed failure Reason.
-- Every result stored before this migration linked a collect receipt, so
-- it is backfilled COLLECTED with no reason.
ALTER TABLE rec_gc_result ADD COLUMN f_outcome TEXT;
ALTER TABLE rec_gc_result ADD COLUMN f_reason TEXT;
UPDATE rec_gc_result SET f_outcome = 'COLLECTED', f_reason = '';
