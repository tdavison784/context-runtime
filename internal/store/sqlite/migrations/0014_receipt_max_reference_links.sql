-- 0014: receipts record domain.Limits.MaxReferenceLinks (D14, D17).
-- Receipts written before 0014 keep NULL, which reads as 0: the limit was
-- not recorded for them. It is never replaced by the current default, so a
-- replayed old receipt reports exactly what was recorded (M8).
ALTER TABLE rec_receipt ADD COLUMN f_versions_limits_max_reference_links INTEGER;
