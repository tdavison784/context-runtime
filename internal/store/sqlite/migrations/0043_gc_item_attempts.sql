-- J4 / SEC-3.1 / SPEC-3.3: attempts belong to the unprocessed next candidate.
ALTER TABLE rec_gc_progress ADD COLUMN f_item_attempts INTEGER;
