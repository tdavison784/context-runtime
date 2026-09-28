-- 0046: Phase3Policy.MaxLiveProofDependents (W4b, DUR-3.1): the live
-- non-FIXED proof dependency rows one resource may carry, so invalidating
-- all of them fits half the transaction work budget at 5 units per row.
-- Envelopes and receipts persist their effective policy, so both gain the
-- column. Every recorded policy is backfilled with the largest value its
-- own work budget allows, capped at the default 256, so historical
-- envelopes and receipts still validate and replay verbatim (P3-38). Rows
-- without a recorded Phase 3 policy stay absent.
ALTER TABLE rec_envelope ADD COLUMN f_semantic_policy_max_live_proof_dependents INTEGER;
ALTER TABLE rec_receipt ADD COLUMN f_versions_semantic_max_live_proof_dependents INTEGER;
UPDATE rec_envelope SET f_semantic_policy_max_live_proof_dependents = MIN(256, (f_semantic_policy_max_transaction_work / 2) / 5)
WHERE f_semantic_policy_present = 1;
UPDATE rec_receipt SET f_versions_semantic_max_live_proof_dependents = MIN(256, (f_versions_semantic_max_transaction_work / 2) / 5)
WHERE f_versions_semantic_present = 1;
