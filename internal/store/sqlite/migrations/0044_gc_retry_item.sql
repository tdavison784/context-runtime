-- J4 / SPEC-3.3: retries are per item, even if another operation archives
-- the previously failing candidate between batches.
ALTER TABLE rec_gc_progress ADD COLUMN f_item_attempt_id TEXT;
