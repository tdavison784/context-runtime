-- 0035: an item's exchanges within a conversation, by ordinal (H2,
-- SPEC-2.7). EarliestExchangeWithItem answers which exchange first holds
-- an item in a conversation with one keyed LIMIT 1 search, independent of
-- the conversation's length. A member's exchange exists when the member is
-- inserted, so the row is written with the member; rows stored before this
-- migration are backfilled from the member and exchange tables.
CREATE TABLE lookup_item_exchange (
  session_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  exchange_id TEXT NOT NULL,
  PRIMARY KEY (session_id,item_id,conversation_id,ordinal,exchange_id)
);
INSERT OR IGNORE INTO lookup_item_exchange(session_id,item_id,conversation_id,ordinal,exchange_id)
SELECT m.session_id, m.f_source_item_id, x.f_conversation_id, x.f_ordinal, x.id
FROM rec_exchange_member AS m JOIN rec_exchange AS x ON x.session_id = m.session_id AND x.id = m.f_exchange_id AND x.subkey = 0
WHERE m.subkey = 0;
