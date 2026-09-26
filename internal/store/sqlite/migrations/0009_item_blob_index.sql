-- 0009: index items by referenced blob hash (R19, R5), so blob-reference
-- authorization finds the referencing items without a session-wide scan.
-- Each item appears once per distinct blob it references. Existing rows are
-- backfilled from their lossless parts, whose string fields are hex.
CREATE TABLE item_blobs (
  session_id TEXT NOT NULL,
  blob_hash TEXT NOT NULL,
  item_id TEXT NOT NULL,
  PRIMARY KEY (session_id, blob_hash, item_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);
INSERT OR IGNORE INTO item_blobs(session_id, blob_hash, item_id)
SELECT i.session_id, CAST(unhex(json_extract(p.value, '$.BlobHash')) AS TEXT), i.id
FROM rec_item AS i, json_each(i.f_parts) AS p
WHERE i.subkey = 0 AND json_extract(p.value, '$.BlobHash') <> '';
