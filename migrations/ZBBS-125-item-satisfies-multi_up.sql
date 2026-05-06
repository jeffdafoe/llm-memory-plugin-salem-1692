-- ZBBS-125: multi-attribute item satisfaction (item_satisfies table).
--
-- Replaces the single satisfies_attribute / satisfies_amount columns
-- on item_kind with a one-to-many item_satisfies relation. Lets a
-- single item ease multiple needs at different magnitudes — e.g. ale
-- now sates a little hunger alongside its thirst, and the schema
-- doesn't need a third / fourth column to add tiredness or any future
-- attribute.
--
-- Migration order:
--   1. Create item_satisfies, copy rows from the old columns.
--   2. Adjust values per ZBBS-125 calibration (water 4→8, ale
--      thirst 8→4 + new hunger=2).
--   3. Drop the old columns from item_kind.
--
-- Engine code that previously SELECTed satisfies_attribute /
-- satisfies_amount is migrated in the same PR (pay.go, inventory.go,
-- satiation.go, room_narration.go, inventory_api.go). Client-side
-- callers (config_panel, editor_panel) read from the inventory_api
-- response, which switches to a `satisfies` array shape — those panel
-- updates ride along.
--
-- Existing item_satisfies row for ale.thirst is updated rather than
-- inserted so the data history is preserved by the audit trail
-- (item_kind table doesn't carry version info, so the simplest way to
-- attest the recalibration is the migration commit itself).
--
-- The PRIMARY KEY on (item_kind, attribute) means an item can have at
-- most one row per attribute — sensible: ale that sates two units of
-- hunger plus three would just be five units of hunger.

BEGIN;

CREATE TABLE item_satisfies (
    item_kind  varchar(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE ON DELETE CASCADE,
    attribute  varchar(32) NOT NULL,
    amount     integer     NOT NULL CHECK (amount > 0),
    PRIMARY KEY (item_kind, attribute)
);

-- Copy existing single-attribute satisfactions across.
INSERT INTO item_satisfies (item_kind, attribute, amount)
SELECT name, satisfies_attribute, satisfies_amount
  FROM item_kind
 WHERE satisfies_attribute IS NOT NULL
   AND satisfies_amount IS NOT NULL;

-- Recalibrations. Water doubles (a sip → a full draught). Ale's
-- thirst halves (still a drink, but the ale pull is the social
-- gesture, not the hydration), and gains a small hunger effect for
-- the grain calories.
UPDATE item_satisfies SET amount = 8 WHERE item_kind = 'water' AND attribute = 'thirst';
UPDATE item_satisfies SET amount = 4 WHERE item_kind = 'ale'   AND attribute = 'thirst';
INSERT INTO item_satisfies (item_kind, attribute, amount) VALUES ('ale', 'hunger', 2)
ON CONFLICT (item_kind, attribute) DO UPDATE SET amount = EXCLUDED.amount;

-- Drop the legacy columns. Engine code in this PR no longer reads
-- them; leaving them around would invite future drift between the
-- two sources of truth.
ALTER TABLE item_kind DROP COLUMN satisfies_attribute;
ALTER TABLE item_kind DROP COLUMN satisfies_amount;

COMMIT;
