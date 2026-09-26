-- 0011: index items by source locator key (R19, M5, R2), so matching a
-- reference to already ingested sources is not a session-wide scan. Keys
-- follow domain.LocatorKey under the recorded rule version. The key rule
-- (lexical path cleaning) is not expressible in SQL, so the Go step for
-- this migration (steps.go) backfills existing items in the same
-- transaction.
CREATE TABLE item_sources (
  session_id TEXT NOT NULL,
  rule_version TEXT NOT NULL,
  locator_key TEXT NOT NULL,
  item_id TEXT NOT NULL,
  PRIMARY KEY (session_id, rule_version, locator_key, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
