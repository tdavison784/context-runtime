-- 0033: resource updates by the paths they may change (G2, SEC-1.7,
-- DUR-1.2). ResourceUpdatesAffectingPath reads only the updates that name
-- a path or one of its ancestor directories, plus every ALL-paths update
-- (each UNKNOWN update is ALL-paths), so a path's currency check never
-- walks unrelated edits. path_key is 'all', or 'path:' plus the hex of a
-- ChangedPaths entry: stored strings are hex in lossless lists, so the
-- backfill derives the same keys without decoding.
CREATE TABLE lookup_resource_update_path (
  session_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  path_key TEXT NOT NULL,
  seq INTEGER NOT NULL,
  update_id TEXT NOT NULL,
  PRIMARY KEY (session_id,resource_id,path_key,seq,update_id)
);
INSERT INTO lookup_resource_update_path(session_id,resource_id,path_key,seq,update_id)
SELECT u.session_id, u.f_resource_id, 'all', u.f_seq, u.id
FROM rec_resource_update AS u WHERE u.f_all_paths = 1;
INSERT INTO lookup_resource_update_path(session_id,resource_id,path_key,seq,update_id)
SELECT DISTINCT u.session_id, u.f_resource_id, 'path:' || j.value, u.f_seq, u.id
FROM rec_resource_update AS u, json_each(u.f_changed_paths) AS j
WHERE u.f_changed_paths IS NOT NULL AND u.f_changed_paths != 'null';
