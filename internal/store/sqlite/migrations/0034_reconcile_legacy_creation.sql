-- 0034: reconcile pre-upgrade creation declarations (G5, SPEC-1.3, FROZEN
-- C-1, P3-4/41). The work is this migration's frozen Go step,
-- reconcileLegacyCreationV1 (steps_0034.go): each keyed item stored without
-- an explicit namespace gets one creation declaration, known when its
-- ingest receipt snapshot establishes its creation identity and unknown
-- otherwise, so an identical restatement dedups or fails closed but never
-- replaces or rebinds. No other record changes.
SELECT 1;
