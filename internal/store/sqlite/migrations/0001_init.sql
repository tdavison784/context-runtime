CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    last_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE records (
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    id TEXT NOT NULL,
    subkey INTEGER NOT NULL DEFAULT 0,
    seq INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 0,
    revision INTEGER NOT NULL DEFAULT 0,
    task_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    directive_id TEXT NOT NULL DEFAULT '',
    event_id TEXT NOT NULL DEFAULT '',
    from_id TEXT NOT NULL DEFAULT '',
    to_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    data BLOB NOT NULL,
    PRIMARY KEY (session_id, kind, id, subkey),
    FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE INDEX records_order ON records(session_id, kind, seq, id);
CREATE INDEX records_task ON records(session_id, kind, task_id);
CREATE INDEX records_from ON records(session_id, kind, from_id);
-- V2 reserves one provider operation per conversation. This partial index
-- backs the store's ErrCallInFlight check even if a future writer bypasses it.
CREATE UNIQUE INDEX one_reserving_call ON records(session_id, from_id)
    WHERE kind = 'call' AND state IN ('PREPARED', 'SENT', 'UNKNOWN');
CREATE TABLE blobs (
    session_id TEXT NOT NULL,
    hash TEXT NOT NULL,
    media_type TEXT NOT NULL,
    data BLOB NOT NULL,
    PRIMARY KEY (session_id, hash),
    FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
CREATE TABLE directives (
    session_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    directive_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    PRIMARY KEY (session_id, task_id, directive_id),
    FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
