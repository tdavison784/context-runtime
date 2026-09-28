-- 0036: live grant ranges that never visit dead rows (H2, DUR-2.10).
-- LiveGrantsFor reads three disjoint ranges of this index: unrevoked
-- grants that never expire (revoked_seq = 0, expires_at_seq = 0, issued by
-- seq), unrevoked grants expiring at or after seq, and grants revoked
-- after seq. Revoked-before and expired-before rows fall outside all three,
-- so the read costs the live grants, not the target's history. It replaces
-- 0031's index, whose OR over revoked_seq scanned and sorted every row.
DROP INDEX lookup_grant_target_live;
CREATE INDEX lookup_grant_target_liveness ON lookup_grant_target(session_id,action,target_key,revoked_seq,expires_at_seq,issued_seq,grant_id);
