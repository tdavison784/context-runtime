-- 0048: K1 write-time validity pointers, the settlement-audit cursor and
-- the live-proof index (K1 A1, K1 A4, K1-api.2). lookup_workspace_divergence
-- holds each resource's monotone divergence raises (lost freshness or a
-- changed workspace fingerprint) and lookup_affecting_raise the (resource,
-- key) raises — path_key 'all' is the ALL key of UNKNOWN and ALL-paths
-- reports, otherwise 'path:' plus the hex of the changed path, exactly
-- migration 0033's keys. Entries are the update's resulting authoritative
-- revision and ID, so the tables read by one exact key in revision order
-- and never fan out to dependents. lookup_live_proof is every live proof
-- (the current proof of a current obligation version) in (Seq, ID) order
-- for the SYSTEM async audit worker; a settled proof leaves it.
-- settlement_cursor is the session's CAS-written audit scan position,
-- unsequenced operational state like gc_queue_cursor, never evidence that
-- a proof was settled. The backfill raises the ALL key and every changed
-- path of every stored report (conservative: reports' same-content
-- history is not reconstructible, so a backfilled raise can settle a proof
-- a live report would have spared); the frozen Go step
-- reconcileK1PointersV1 (steps_0048.go) backfills divergence exactly, from
-- each resource's fingerprint chain.
CREATE TABLE lookup_workspace_divergence (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  update_id TEXT NOT NULL,
  PRIMARY KEY (session_id,resource_id,revision)
);
CREATE TABLE lookup_affecting_raise (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  path_key TEXT NOT NULL,
  revision INTEGER NOT NULL,
  update_id TEXT NOT NULL,
  PRIMARY KEY (session_id,resource_id,path_key,revision)
);
CREATE TABLE lookup_live_proof (
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  proof_id TEXT NOT NULL,
  PRIMARY KEY (session_id,seq,proof_id)
);
CREATE TABLE settlement_cursor (
  session_id TEXT NOT NULL PRIMARY KEY,
  cursor_seq INTEGER NOT NULL,
  cursor_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT INTO lookup_affecting_raise(session_id,resource_id,path_key,revision,update_id)
SELECT u.session_id, u.f_resource_id, 'all', u.f_resulting_authoritative_revision, u.id
FROM rec_resource_update AS u WHERE u.f_freshness = 'UNKNOWN' OR u.f_all_paths = 1;
INSERT INTO lookup_affecting_raise(session_id,resource_id,path_key,revision,update_id)
SELECT DISTINCT u.session_id, u.f_resource_id, 'path:' || j.value, u.f_resulting_authoritative_revision, u.id
FROM rec_resource_update AS u, json_each(u.f_changed_paths) AS j
WHERE u.f_changed_paths IS NOT NULL AND u.f_changed_paths != 'null';
INSERT INTO lookup_live_proof(session_id,seq,proof_id)
SELECT o.session_id, p.f_seq, o.f_current_proof_id
FROM rec_obligation AS o JOIN rec_proof AS p ON p.session_id = o.session_id AND p.id = o.f_current_proof_id AND p.subkey = 0
WHERE o.f_current = 1 AND COALESCE(o.f_current_proof_id, '') != '';
