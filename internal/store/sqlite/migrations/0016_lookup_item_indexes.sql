-- 0016: index the lookup tables by item (SPEC-3.1 item 1).
-- Every SUPERSEDES or DUPLICATE_OF edge deletes the retired item's rows
-- from the lookup tables; their primary keys start with the lookup key, so
-- those DELETEs searched the whole session. These indexes make each one a
-- (session_id, item_id) search.
CREATE INDEX lookup_canonical_item ON lookup_canonical(session_id, item_id);
CREATE INDEX lookup_working_item ON lookup_working(session_id, item_id);
CREATE INDEX lookup_source_item ON lookup_source(session_id, item_id);
CREATE INDEX lookup_blob_item ON lookup_blob(session_id, item_id);
