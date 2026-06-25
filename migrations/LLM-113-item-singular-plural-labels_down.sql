-- LLM-113 down: drop the singular/plural counting-phrase columns. The deleted
-- `raspberry` discovery row is NOT restored — it was junk, and a singular consume
-- would re-mint an equivalent on demand (which is the very behavior this change
-- prevents). Resolution falls back to key + display_label only.

BEGIN;

ALTER TABLE item_kind DROP COLUMN IF EXISTS display_label_plural;
ALTER TABLE item_kind DROP COLUMN IF EXISTS display_label_singular;

COMMIT;
