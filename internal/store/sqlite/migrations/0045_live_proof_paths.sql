-- 0045: path-keyed live proof dependents (DUR-3.1). A report reads only
-- what its change can affect: lookup_live_proof_path files each live
-- proof's CURRENT_PATH dependency under 'path:' plus the hex of every
-- ancestor of its resource-relative path (so a changed directory is one
-- key), and each WORKSPACE dependency under 'ws'. lookup_live_dependents
-- counts live non-FIXED dependency rows per resource (the policy cap).
-- FIXED_CONTENT dependencies never go stale and leave every live index.
-- The frozen Go step reconcileLiveProofPathsV1 (steps_0045.go) rebuilds
-- lookup_live_dependency and fills both new tables from the live proofs.
CREATE TABLE lookup_live_proof_path (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  key TEXT NOT NULL,
  seq INTEGER NOT NULL,
  proof_id TEXT NOT NULL,
  PRIMARY KEY (session_id,resource_id,key,seq,proof_id)
);
CREATE TABLE lookup_live_dependents (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  dependents INTEGER NOT NULL,
  PRIMARY KEY (session_id,resource_id)
);
