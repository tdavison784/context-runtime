-- J2 / SPEC-3.6: pin the candidate eligibility ceiling across GC batches.
ALTER TABLE rec_gc_progress ADD COLUMN f_snapshot_seq INTEGER;
