-- 0006: index obligation versions by source item (D13, R9), so graph can
-- find every version bound to a replaced source with a bounded query.
CREATE INDEX obligation_source ON rec_obligation(session_id, f_source_item_id, id, subkey);
