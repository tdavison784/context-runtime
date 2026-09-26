-- D16: content-free diagnostics are immutable children of an ingestion event.
CREATE TABLE diagnostics (
 session_id TEXT NOT NULL,
 event_id TEXT NOT NULL,
 span_index INTEGER NOT NULL CHECK(span_index >= 0),
 diagnostic_index INTEGER NOT NULL CHECK(diagnostic_index >= 0),
 part_index INTEGER NOT NULL CHECK(part_index >= 0),
 code TEXT NOT NULL,
 reason TEXT NOT NULL,
 section TEXT NOT NULL,
 directive_id TEXT NOT NULL,
 byte_start INTEGER NOT NULL CHECK(byte_start >= 0),
 byte_end INTEGER NOT NULL CHECK(byte_end >= byte_start),
 parser_version TEXT NOT NULL,
 PRIMARY KEY(session_id,event_id,span_index,diagnostic_index),
 FOREIGN KEY(session_id) REFERENCES sessions(session_id)
);
