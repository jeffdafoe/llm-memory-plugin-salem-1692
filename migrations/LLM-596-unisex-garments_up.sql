-- LLM-596: rename the three working garments so none of them names a sex.
--
-- WHY. The catalog's three working garments were gendered two ways and neither
-- was modelled: `gown` is described as "a woman's gown of good cloth", `breeches`
-- as "cut to the knee for a working man", and `shift` — authored neutrally as
-- "worn next to the skin" — is historically a WOMAN'S undergarment; the man's
-- equivalent is a shirt, which the catalog has never carried.
--
-- The engine models no sex and never has. sim.ResolveWorkGarmentTier treats every
-- working garment as interchangeable, so the LLM-592 seed put shifts on nine men
-- and the working-clothes cue then steered them to buy more — Constable Gideon
-- Marsh being told a fresh shift would set him right. Naming the LAYER instead of
-- the wearer removes the mismatch at the source, without teaching the engine a sex
-- to reason about.
--
-- A SECOND REASON, independent of gender: `shift` collides with the engine's own
-- work-shift vocabulary. NPCs read "It is your working hours and you are at your
-- post", the ticker registry has a "shift" beat, and the word turns up as a verb
-- constantly in their own narrative memory ("shifts the work", "her prices
-- shift"). A garment sharing that word is noise in every prompt that carries both.
--
-- THE NAMES. Three layers, all worn by both sexes in 1692 New England, all
-- plausibly LABOURED in — which matters, because the engine puts all three in the
-- working-garment set and will wear them at the forge:
--
--     shift    -> linens     the body layer, washed oftener than anything else
--     breeches -> woolens    the stout outer layer of the working day
--     gown     -> homespun   rough home-woven cloth, the colonial staple
--
-- "good clothes" was considered for the third and dropped: it reads as Sunday
-- best, which is exactly what one would NOT labour in, and the engine has no way
-- to hold that distinction. Homespun carries no such implication.
--
-- coat and cloak are untouched — already unisex, and they belong to the cold
-- self-line rather than this set.
--
-- WHAT CASCADES AND WHAT DOES NOT. Every foreign key onto item_kind(name) is
-- ON UPDATE CASCADE — actor_inventory, item_recipe, pay_ledger, scene_quote,
-- item_satisfies — so the rename propagates itself through real relational state.
-- It does NOT reach item names stored inside jsonb, which is why the restock
-- policy and any in-flight visitor plan are rewritten explicitly below. Missing
-- those is how a world ends up half-renamed.
--
-- HISTORY IS REWRITTEN. pay_ledger cascades, so a sale recorded tonight as a
-- `shift` will read as `linens`. That is deliberate: a split vocabulary where the
-- ledger says one thing and the catalog another is worse than a past that reads
-- consistently. Called out because it is not reversible in meaning, only in
-- spelling.
--
-- Checkpoint-written tables throughout; the deploy runs migrations with the
-- engine stopped.

BEGIN;

CREATE TEMP TABLE llm596_rename (old_name text, new_name text, label text, singular text, plural text, descr text) ON COMMIT DROP;
INSERT INTO llm596_rename (old_name, new_name, label, singular, plural, descr) VALUES
    ('shift', 'linens', 'Linens', 'set of linens', 'sets of linens',
     'Plain linen worn next to the skin, and washed oftener than anything else.'),
    ('breeches', 'woolens', 'Woolens', 'set of woolens', 'sets of woolens',
     'Stout wool cut for the working day, and mended more than once.'),
    ('gown', 'homespun', 'Homespun', 'suit of homespun', 'suits of homespun',
     'Rough cloth woven at home, worn by most folk on most days.');

DO $$
DECLARE
    renamed int;
    leftover text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name IN ('shift', 'breeches', 'gown')) THEN
        -- Already renamed (a re-apply), or a fresh schema-only database. Either
        -- way there is nothing to do and nothing to assert against.
        RETURN;
    END IF;

    -- The rename itself. Every FK onto item_kind(name) carries ON UPDATE CASCADE,
    -- so this one statement moves actor_inventory, item_recipe, pay_ledger,
    -- scene_quote and item_satisfies with it.
    UPDATE item_kind k
       SET name                   = r.new_name,
           display_label          = r.label,
           display_label_singular = r.singular,
           display_label_plural   = r.plural,
           description            = r.descr
      FROM llm596_rename r
     WHERE k.name = r.old_name;

    GET DIAGNOSTICS renamed = ROW_COUNT;
    IF renamed <> 3 THEN
        RAISE EXCEPTION 'LLM-596: expected to rename 3 garment kinds, renamed %', renamed;
    END IF;

    -- jsonb does NOT cascade. Restock policies store the item as a plain string,
    -- so a policy left un-rewritten would name a kind that no longer exists — the
    -- entry would silently stop matching and the good would fall out of both the
    -- price line and the restock line, which is the exact bug LLM-592 was filed to
    -- fix. Rebuilt entry by entry so every other line keeps its own shape.
    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               (SELECT jsonb_agg(
                           CASE WHEN r.new_name IS NULL THEN entry
                                ELSE jsonb_set(entry, '{item}', to_jsonb(r.new_name))
                           END ORDER BY ord)
                  FROM jsonb_array_elements(aa.params->'restock')
                       WITH ORDINALITY AS t(entry, ord)
                  LEFT JOIN llm596_rename r ON r.old_name = entry->>'item'))
     WHERE aa.params ? 'restock'
       AND EXISTS (SELECT 1
                     FROM jsonb_array_elements(aa.params->'restock') e
                     JOIN llm596_rename r ON r.old_name = e->>'item');

    -- An in-flight merchant visitor carries his errand good and pack in jsonb too.
    -- Zero rows matched when this was written, but a factor spawning between now
    -- and the deploy would carry the old names, and his errand would then name a
    -- kind that does not exist.
    UPDATE visitor v
       SET plan = replace(replace(replace(v.plan::text,
               '"shift"', '"linens"'), '"breeches"', '"woolens"'), '"gown"', '"homespun"')::jsonb
     WHERE v.plan IS NOT NULL
       AND v.plan::text ~ '"(shift|breeches|gown)"';

    -- Nothing may still refer to the retired names by FK. Cheap, and it catches a
    -- table whose constraint was NOT declared ON UPDATE CASCADE — the one failure
    -- mode this migration's whole shape depends on not existing.
    SELECT string_agg(t, ', ') INTO leftover FROM (
        SELECT 'actor_inventory' t WHERE EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind IN ('shift','breeches','gown'))
        UNION ALL
        SELECT 'item_recipe'      WHERE EXISTS (SELECT 1 FROM item_recipe WHERE output_item IN ('shift','breeches','gown'))
        UNION ALL
        SELECT 'pay_ledger'       WHERE EXISTS (SELECT 1 FROM pay_ledger WHERE item_kind IN ('shift','breeches','gown'))
        UNION ALL
        SELECT 'restock policy'   WHERE EXISTS (
            SELECT 1 FROM actor_attribute aa, jsonb_array_elements(COALESCE(aa.params->'restock','[]'::jsonb)) e
             WHERE e->>'item' IN ('shift','breeches','gown'))
    ) s;
    IF leftover IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-596: retired garment names still referenced in: %', leftover;
    END IF;
END $$;

COMMIT;
