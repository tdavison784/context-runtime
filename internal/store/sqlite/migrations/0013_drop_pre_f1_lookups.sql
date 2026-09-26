-- 0013: drop the pre-F1 lookup tables and indexes (F1).
-- ItemsByBlob, ItemsBySourceKey, the old DuplicateCandidates, and
-- UnresolvedReferences counted records the caller could not see
-- (SEC-1.1) and are removed; their data was carried into the
-- access-filtered lookup tables by 0012. Nothing reads or writes these.
DROP TABLE item_blobs;
DROP TABLE item_sources;
DROP INDEX item_duplicate;
DROP INDEX reference_locator;
