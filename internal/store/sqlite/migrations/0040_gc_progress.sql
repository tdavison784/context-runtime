-- 0040: GC batch progress (H3). One CAS-written row per pending GC request:
-- the durable (Seq, ID) candidate cursor, completed batches and attempts.
-- It is operational metadata, never a substitute for a batch's collect
-- receipt or the request's result, and carries no semantic sequence.
CREATE TABLE rec_gc_progress (
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  subkey INTEGER NOT NULL DEFAULT 0,
  f_cursor_seq INTEGER,
  f_cursor_id TEXT,
  f_batches INTEGER,
  f_attempts INTEGER,
  f_revision INTEGER,
  PRIMARY KEY (session_id,id,subkey),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
