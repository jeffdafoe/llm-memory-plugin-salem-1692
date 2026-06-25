-- LLM-113: give every item kind a singular + plural COUNTING noun phrase, so
-- (a) consume / pay_with_item / scene_quote resolution accepts an item named by
-- ANY of key / display_label / singular / plural (the LLM names "Raspberry",
-- "raspberries", "a tankard of ale" interchangeably), and (b) in-world prose +
-- buy/gather cues render the right grammatical number ("you consume 1 raspberry"
-- / "3 raspberries", "buy a wedge of cheese"). The bug this closes: the catalog
-- stored only the plural ("raspberries"), so a singular "Raspberry" missed
-- resolution, minted a phantom inert kind, and the consume failed.
--
-- Two nullable columns + a backfill of every authored kind. Count nouns are
-- regular ("axe"/"axes"); mass nouns carry a period measure word ("tankard of
-- ale", "loaf of bread"). The phrases are article-less — the engine adds "a"/"an"
-- at render time. Keys (item_kind.name) are NOT renamed: resolution and render
-- key off the labels, so actor_inventory / item_recipe / pay_ledger / scene_quote
-- references are untouched (no referential migration).
--
-- Also drops the junk `raspberry` discovery row (ZBBS-WORK-412) a singular
-- consume minted before this fix — economically inert (category 'unknown', 0 in
-- world) and never re-minted once singular resolution lands.
--
-- ENGINE-OWNED TABLE. item_kind is reference data the engine reads at boot and
-- the checkpoint discovery-upsert writes. Apply with the engine STOPPED (stop ->
-- migrate -> start, the standard deploy order): the new columns must exist before
-- LoadAll's SELECT runs, and the discovery-row delete must not race a re-mint.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS; the UPDATE sets fixed values (idempotent);
-- the discovery delete is scoped and a no-op once gone. The guard then fails loud
-- if any authored (non-'unknown') kind is left without both phrases.

BEGIN;

ALTER TABLE item_kind ADD COLUMN IF NOT EXISTS display_label_singular character varying(64);
ALTER TABLE item_kind ADD COLUMN IF NOT EXISTS display_label_plural   character varying(64);

UPDATE item_kind AS k
   SET display_label_singular = v.singular,
       display_label_plural   = v.plural
  FROM (VALUES
    ('ale',         'tankard of ale',   'tankards of ale'),
    ('water',       'flask of water',   'flasks of water'),
    ('milk',        'jug of milk',      'jugs of milk'),
    ('coca_tea',    'cup of coca tea',  'cups of coca tea'),
    ('bread',       'loaf of bread',    'loaves of bread'),
    ('cheese',      'wedge of cheese',  'wedges of cheese'),
    ('meat',        'cut of meat',      'cuts of meat'),
    ('porridge',    'bowl of porridge', 'bowls of porridge'),
    ('stew',        'bowl of stew',     'bowls of stew'),
    ('flour',       'sack of flour',    'sacks of flour'),
    ('wheat',       'sheaf of wheat',   'sheaves of wheat'),
    ('iron',        'ingot of iron',    'ingots of iron'),
    ('nights_stay', 'night''s stay',    'nights'' stay'),
    ('axe',         'axe',              'axes'),
    ('hammer',      'hammer',           'hammers'),
    ('horseshoe',   'horseshoe',        'horseshoes'),
    ('nail',        'nail',             'nails'),
    ('skillet',     'skillet',          'skillets'),
    ('berries',     'berry',            'berries'),
    ('blueberries', 'blueberry',        'blueberries'),
    ('raspberries', 'raspberry',        'raspberries'),
    ('carrots',     'carrot',           'carrots')
  ) AS v(name, singular, plural)
 WHERE k.name = v.name;

-- Best-effort cleanup of the phantom row. item_kind.name is referenced by
-- actor_inventory / pay_ledger / scene_quote with ON UPDATE CASCADE but NOT
-- ON DELETE CASCADE, so a stray reference would abort the whole migration. The
-- row is economically inert (0 in world), so it should always delete — but
-- swallow a foreign-key violation rather than fail the deploy over a junk row;
-- it simply stays inert if it can't be removed.
DO $$
BEGIN
    DELETE FROM item_kind WHERE name = 'raspberry' AND category = 'unknown';
EXCEPTION WHEN foreign_key_violation THEN
    RAISE NOTICE 'LLM-113: phantom raspberry row still referenced; left in place (inert)';
END $$;

DO $$
DECLARE
    missing int;
BEGIN
    -- Treat '' the same as NULL — a row with empty strings behaves like "missing"
    -- in the Go fallback (Singular()/Plural() skip empty), so the guard must too.
    SELECT count(*) INTO missing
      FROM item_kind
     WHERE category <> 'unknown'
       AND (NULLIF(trim(display_label_singular), '') IS NULL
            OR NULLIF(trim(display_label_plural), '') IS NULL);
    IF missing > 0 THEN
        RAISE EXCEPTION 'LLM-113: % authored item_kind row(s) missing a singular/plural label', missing;
    END IF;
END $$;

COMMIT;
