-- J3 / XREV-3.2: durable adaptive item-count bound.
ALTER TABLE rec_gc_progress ADD COLUMN f_batch_size INTEGER;
