-- 0031: grant liveness in the grant-target index (G2, SEC-1.5, DUR-1.4).
-- LiveGrantsFor counts only grants in force at a sequence, so revoked and
-- expired history never makes a live grant unreadable. Each index row
-- carries its grant's revocation and expiry sequence (0 for none),
-- backfilled from rec_grant and kept current by RevokeGrant; the live
-- index serves the unrevoked range without visiting revoked rows.
ALTER TABLE lookup_grant_target ADD COLUMN revoked_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE lookup_grant_target ADD COLUMN expires_at_seq INTEGER NOT NULL DEFAULT 0;
UPDATE lookup_grant_target SET
  revoked_seq = COALESCE((SELECT g.f_revoked_seq FROM rec_grant AS g
    WHERE g.session_id = lookup_grant_target.session_id AND g.id = lookup_grant_target.grant_id AND g.subkey = 0), 0),
  expires_at_seq = COALESCE((SELECT g.f_expires_at_seq FROM rec_grant AS g
    WHERE g.session_id = lookup_grant_target.session_id AND g.id = lookup_grant_target.grant_id AND g.subkey = 0), 0);
CREATE INDEX lookup_grant_target_live ON lookup_grant_target(session_id,action,target_key,revoked_seq,issued_seq,grant_id,expires_at_seq);
