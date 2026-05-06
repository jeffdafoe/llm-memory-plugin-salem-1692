-- ZBBS-125 rollback: restore single-attribute satisfaction columns.
--
-- Recreates satisfies_attribute / satisfies_amount on item_kind and
-- backfills from item_satisfies. When an item has multiple rows in
-- item_satisfies (the new capability), the rollback keeps only the
-- highest-amount entry — the legacy schema can't represent multiples.
-- Ale would lose its hunger effect on rollback.

BEGIN;

ALTER TABLE item_kind ADD COLUMN satisfies_attribute varchar(32);
ALTER TABLE item_kind ADD COLUMN satisfies_amount    integer;

UPDATE item_kind ik
   SET satisfies_attribute = s.attribute,
       satisfies_amount    = s.amount
  FROM (
      SELECT DISTINCT ON (item_kind) item_kind, attribute, amount
        FROM item_satisfies
       ORDER BY item_kind, amount DESC, attribute
  ) s
 WHERE s.item_kind = ik.name;

DROP TABLE item_satisfies;

COMMIT;
