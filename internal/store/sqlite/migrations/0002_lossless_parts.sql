-- 0002: lossless content parts (R8, D3).
-- 0001 stored rec_item.f_parts as JSON text, and JSON encoding replaced
-- invalid UTF-8 with U+FFFD. The column now holds a JSON array whose string
-- fields are the lowercase hex of their exact bytes (see parts.go). Rows
-- written under 0001 are rewritten byte for byte from what 0001 stored; a
-- row whose text was already altered keeps the altered bytes, so its stored
-- ContentHash no longer verifies and reads of it fail with ErrIntegrity.
ALTER TABLE rec_item ADD COLUMN f_parts_lossless BLOB;
UPDATE rec_item SET f_parts_lossless = (
  SELECT json_group_array(json_object(
    'Type', lower(hex(CAST(json_extract(p.value, '$.Type') AS BLOB))),
    'Text', lower(hex(CAST(json_extract(p.value, '$.Text') AS BLOB))),
    'MediaType', lower(hex(CAST(json_extract(p.value, '$.MediaType') AS BLOB))),
    'BlobHash', lower(hex(CAST(json_extract(p.value, '$.BlobHash') AS BLOB))),
    'BlobSize', json_extract(p.value, '$.BlobSize')) ORDER BY p.key)
  FROM json_each(rec_item.f_parts) AS p)
WHERE f_parts IS NOT NULL;
ALTER TABLE rec_item DROP COLUMN f_parts;
ALTER TABLE rec_item RENAME COLUMN f_parts_lossless TO f_parts;
