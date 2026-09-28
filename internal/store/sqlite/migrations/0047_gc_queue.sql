-- 0047: the GC queue by trigger and its durable scan cursor (DUR-3.2).
-- lookup_pending_gc_trigger holds exactly the pending requests (no
-- GCResult) keyed by trigger, so a collector reads only its enabled
-- triggers and disabled-trigger requests never fill a page; it is
-- backfilled from lookup_pending_gc. gc_queue_cursor is each session's
-- CAS-written scan position: unsequenced operational state, like
-- rec_gc_progress, never evidence of a collection.
CREATE TABLE lookup_pending_gc_trigger (
  session_id TEXT NOT NULL,
  trigger TEXT NOT NULL,
  seq INTEGER NOT NULL,
  request_id TEXT NOT NULL,
  PRIMARY KEY (session_id,trigger,seq,request_id)
);
INSERT INTO lookup_pending_gc_trigger(session_id,trigger,seq,request_id)
SELECT p.session_id, COALESCE(r.f_collect_intent_trigger, ''), p.seq, p.request_id
FROM lookup_pending_gc AS p JOIN rec_gc_request AS r ON r.session_id = p.session_id AND r.id = p.request_id AND r.subkey = 0;
CREATE TABLE gc_queue_cursor (
  session_id TEXT NOT NULL PRIMARY KEY,
  cursor_seq INTEGER NOT NULL,
  cursor_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
