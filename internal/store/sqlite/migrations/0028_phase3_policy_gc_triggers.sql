-- 0028: the explicit enabled GC-trigger set of the recorded Phase 3 policy
-- (P3-38/39). Envelopes and receipts persist the effective Phase3Policy
-- they ran under; its new GCTriggers list is a lossless leaf list. A row
-- without a recorded Phase 3 policy (every Phase 2 envelope and receipt)
-- is unaffected: its policy stays absent. No trigger set is backfilled:
-- there is no implicit "all triggers" interpretation of a missing set.
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_gc_triggers TEXT;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_gc_triggers TEXT;
