-- ZBBS-HOME-220: per-item dwell-narration column.
--
-- Surfaces a configurable, period-flavored hint to the PC at consume
-- time, telling them the item has a lasting effect they need to stay
-- for. Without this, a player ordering stew sees the immediate -4
-- hunger drop, walks away, and never knew the dwell mechanic was
-- giving them another -8 over the next 16 minutes if they had stayed.
--
-- Empty / NULL = no narration; existing items unaffected. Seeded for
-- stew (the only currently-shipping item with dwell components); add
-- more rows as new dwell items land.

ALTER TABLE item_kind ADD COLUMN consume_dwell_narration TEXT;

UPDATE item_kind
   SET consume_dwell_narration = 'This stew looks really good, it''s going to take some time to enjoy it.'
 WHERE name = 'stew';
