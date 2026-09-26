-- 0004: item role, creation turn, and source ranges (D8, D18, M1); obligation
-- claim names (D13).
-- Rows written before 0004 keep NULL in these columns, which reads as the
-- zero value: a semantic item (role predates transcripts), creation turn 0
-- (no owning turn recorded), no source ranges, and no declared claim. None
-- of these is an executable default: a TTL or TURN-scoped item with
-- creation turn 0 has no valid owning turn (ValidateTurnOwnership), and an
-- obligation without a claim has no declared name to match.
ALTER TABLE rec_item ADD COLUMN f_role TEXT;
ALTER TABLE rec_item ADD COLUMN f_created_turn INTEGER;
ALTER TABLE rec_item ADD COLUMN f_source_ranges TEXT;
ALTER TABLE rec_obligation ADD COLUMN f_claim TEXT;
