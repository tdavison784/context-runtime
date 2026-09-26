-- 0026: reconcile legacy matcher satisfaction (P3-41, Q-W2-3).
-- A Phase 2 obligation version SATISFIED by a matcher transition has no
-- applicability proof a Phase 3 binary can establish, so its Go step
-- (steps_0026.go, reconcileMatcherSatisfactionV1) returns each such current
-- version to UNRESOLVED once, through an audited SYSTEM
-- UPGRADE_RECONCILIATION transition and detail at the session's next
-- sequence, keeping the original history. USER-asserted satisfaction,
-- retired versions and Phase 3 declared versions are untouched.
SELECT 1;
