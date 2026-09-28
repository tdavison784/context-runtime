-- 0049: K1-api.3 path confirmations (XREV-5.2). A broad raise — the ALL
-- key or an ancestor-directory key — does not invalidate a CURRENT_PATH
-- dependency on a path the same report explicitly confirmed with the
-- path's prior content. lookup_path_confirmation holds, per (resource,
-- confirmed path, broad affect key), the latest confirming raise's
-- revision and the latest unconfirmed raise it overtook; affect_key 'all'
-- is the ALL key, otherwise 'path:' plus the hex of the broad key, and
-- path_key is 'path:' plus the hex of the confirmed path, exactly
-- migration 0048's key encodings. lookup_unconfirmed_gap holds the
-- immutable closed runs of unconfirmed raises (first and last revision)
-- the settlement cause seeks: the earliest raise that did NOT confirm the
-- path (K1-api.3 SPEC-2). The backfill writes nothing: reports'
-- same-content history is not reconstructible, so pre-0049 history keeps
-- its recorded raises and behaves exactly as before (conservative
-- over-invalidation, never under).
CREATE TABLE lookup_path_confirmation (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  path_key TEXT NOT NULL,
  affect_key TEXT NOT NULL,
  confirmed_rev INTEGER NOT NULL,
  unconfirmed_rev INTEGER NOT NULL,
  PRIMARY KEY (session_id,resource_id,path_key,affect_key)
);
CREATE TABLE lookup_unconfirmed_gap (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  path_key TEXT NOT NULL,
  affect_key TEXT NOT NULL,
  first_rev INTEGER NOT NULL,
  last_rev INTEGER NOT NULL,
  PRIMARY KEY (session_id,resource_id,path_key,affect_key,last_rev)
);
