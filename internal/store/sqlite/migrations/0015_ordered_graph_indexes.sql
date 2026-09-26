-- 0015: exact-key graph reads in (Seq, ID) order (SPEC-2.1).
-- Relationship reads by (type, source) or (type, target) and item reads by
-- task are ordered by (f_seq, id); with indexes that end at the key, SQLite
-- preferred the session-wide order indexes to avoid a sort, so each read
-- grew with the session. These indexes carry the key and the order, and
-- replace the key-only indexes they supersede.
CREATE INDEX relationship_from_seq ON rec_relationship(session_id, f_type, f_from_id, f_seq, id);
CREATE INDEX relationship_to_seq ON rec_relationship(session_id, f_type, f_to_id, f_seq, id);
CREATE INDEX item_task_seq ON rec_item(session_id, f_task_id, f_seq, id);
DROP INDEX relationship_from;
DROP INDEX relationship_to;
DROP INDEX item_task;
