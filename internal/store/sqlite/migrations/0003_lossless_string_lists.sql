-- 0003: lossless string lists (R8).
-- 0001 stored string lists as JSON arrays of strings, and JSON encoding
-- replaced invalid UTF-8 with U+FFFD, so distinct IDs could be read back as
-- one another. Each list becomes a JSON array of the lowercase hex of each
-- string's bytes (see lossless.go); JSON null (a nil list) and SQL NULL (an
-- absent parent record) are kept. Rows are rewritten from exactly what 0001
-- stored.
UPDATE rec_item SET f_tags = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_item.f_tags)) WHERE json_type(f_tags) = 'array';
UPDATE rec_relationship SET f_coverage_item_ids = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_relationship.f_coverage_item_ids)) WHERE json_type(f_coverage_item_ids) = 'array';
UPDATE rec_event SET f_item_ids = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_event.f_item_ids)) WHERE json_type(f_item_ids) = 'array';
UPDATE rec_obligation SET f_evidence_ids = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_obligation.f_evidence_ids)) WHERE json_type(f_evidence_ids) = 'array';
UPDATE rec_obligation_transition SET f_evidence_ids = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_obligation_transition.f_evidence_ids)) WHERE json_type(f_evidence_ids) = 'array';
UPDATE rec_obligation_transition SET f_fingerprints = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_obligation_transition.f_fingerprints)) WHERE json_type(f_fingerprints) = 'array';
UPDATE rec_grant SET f_target_ids = (SELECT json_group_array(lower(hex(CAST(value AS BLOB))) ORDER BY key) FROM json_each(rec_grant.f_target_ids)) WHERE json_type(f_target_ids) = 'array';
