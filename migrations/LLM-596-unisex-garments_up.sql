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
-- shift"). A garment sharing that word is noise in every prompt carrying both.
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
-- to hold that distinction.
--
-- coat and cloak are untouched — already unisex, and they belong to the cold
-- self-line rather than this set.
--
-- WHAT CASCADES AND WHAT DOES NOT. Every foreign key onto item_kind(name) is
-- ON UPDATE CASCADE, so the rename propagates itself through relational state. It
-- does NOT reach item names stored inside jsonb — the restock policy, an in-flight
-- visitor's plan, and item_recipe.inputs — which are handled explicitly. Missing
-- one of those is how a world ends up half-renamed, and a policy naming a dead
-- kind does not error: the entry silently stops matching and the good drops out of
-- both the price line and the restock line, which is the exact bug LLM-592 was
-- filed to fix.
--
-- HISTORY IS REWRITTEN. pay_ledger cascades, so a sale recorded as a `shift` will
-- read as `linens`. Deliberate: a split vocabulary where the ledger says one thing
-- and the catalog another is worse than a past that reads consistently.
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
    old_present int;
    new_present int;
    renamed int;
    rec record;
    plan_new jsonb;
    inv jsonb;
    mapped text;
    leftover text := NULL;
    found bool;
BEGIN
    SELECT count(*) INTO old_present FROM item_kind k JOIN llm596_rename r ON r.old_name = k.name;
    SELECT count(*) INTO new_present FROM item_kind k JOIN llm596_rename r ON r.new_name = k.name;

    -- Only three states are coherent, and a MIXED one must never be papered over
    -- (code_review). A half-renamed catalog is precisely the state that leaves
    -- policies pointing at dead kinds, and "some of the old names are gone" is not
    -- evidence the rename succeeded.
    IF old_present = 0 AND new_present = 3 THEN
        RETURN;  -- already applied
    ELSIF old_present = 0 AND new_present = 0 THEN
        RETURN;  -- fresh schema-only database (the integration harness)
    ELSIF old_present <> 3 OR new_present <> 0 THEN
        RAISE EXCEPTION 'LLM-596: refusing to run against a mixed catalog — % of 3 old names and % of 3 target names present', old_present, new_present;
    END IF;

    -- The rename itself. Every FK onto item_kind(name) carries ON UPDATE CASCADE,
    -- so this one statement moves the relational surfaces with it. The guard above
    -- has already established that none of the target names exists, so this cannot
    -- collide on the primary key.
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

    -- jsonb #1: restock policies. Rebuilt entry by entry so every other line keeps
    -- its own shape and any extra fields survive untouched.
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

    -- jsonb #2: recipe inputs. item_recipe.output_item is a real FK and cascades,
    -- but `inputs` is jsonb holding {"item": ..., "qty": ...} and does not. No
    -- recipe consumes a garment today; rewritten anyway so the guarantee does not
    -- rest on that staying true.
    UPDATE item_recipe ir
       SET inputs = (SELECT jsonb_agg(
                         CASE WHEN r.new_name IS NULL THEN entry
                              ELSE jsonb_set(entry, '{item}', to_jsonb(r.new_name))
                         END ORDER BY ord)
                       FROM jsonb_array_elements(ir.inputs)
                            WITH ORDINALITY AS t(entry, ord)
                       LEFT JOIN llm596_rename r ON r.old_name = entry->>'item')
     WHERE jsonb_typeof(ir.inputs) = 'array'
       AND EXISTS (SELECT 1 FROM jsonb_array_elements(ir.inputs) e
                     JOIN llm596_rename r ON r.old_name = e->>'item');

    -- jsonb #3: in-flight visitor plans. FIELD-AWARE, not a text replace over the
    -- whole blob (code_review). The plan carries item names in exactly two places
    -- — trade.good (a string) and inventory (an object KEYED by item name) — while
    -- the rest is actor and business names and free narrative. A blanket
    -- replace('"shift"','"linens"') would rewrite any string value equal to
    -- "shift" anywhere in the document, and "shift" is both an ordinary English
    -- word and this engine's own term for a work shift.
    FOR rec IN SELECT actor_id, plan FROM visitor WHERE plan IS NOT NULL LOOP
        plan_new := rec.plan;

        IF plan_new #>> '{trade,good}' IS NOT NULL THEN
            SELECT r.new_name INTO mapped FROM llm596_rename r WHERE r.old_name = plan_new #>> '{trade,good}';
            IF mapped IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{trade,good}', to_jsonb(mapped));
            END IF;
        END IF;

        IF jsonb_typeof(plan_new->'inventory') = 'object' THEN
            SELECT jsonb_object_agg(COALESCE(r.new_name, kv.key), kv.value) INTO inv
              FROM jsonb_each(plan_new->'inventory') kv
              LEFT JOIN llm596_rename r ON r.old_name = kv.key;
            IF inv IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{inventory}', inv);
            END IF;
        END IF;

        IF plan_new IS DISTINCT FROM rec.plan THEN
            UPDATE visitor SET plan = plan_new WHERE actor_id = rec.actor_id;
        END IF;
    END LOOP;

    -- Nothing may still refer to the retired names. The FK surfaces are discovered
    -- FROM THE CATALOG rather than hand-listed (code_review): a hand list silently
    -- goes stale the moment a table gains an item_kind reference, and the whole
    -- shape of this migration rests on every such constraint being ON UPDATE
    -- CASCADE. This asserts that rather than assuming it.
    FOR rec IN
        SELECT c.conrelid::regclass::text AS tbl, a.attname AS col
          FROM pg_constraint c
          JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
         WHERE c.confrelid = 'item_kind'::regclass AND c.contype = 'f'
    LOOP
        EXECUTE format(
            'SELECT EXISTS (SELECT 1 FROM %s WHERE %I IN (SELECT old_name FROM llm596_rename))',
            rec.tbl, rec.col) INTO found;
        IF found THEN
            leftover := concat_ws(', ', leftover, rec.tbl || '.' || rec.col);
        END IF;
    END LOOP;

    -- The jsonb surfaces have no constraint to discover, so they are named.
    IF EXISTS (SELECT 1
                 FROM actor_attribute aa
                 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(aa.params->'restock', '[]'::jsonb)) e
                WHERE e->>'item' IN (SELECT old_name FROM llm596_rename)) THEN
        leftover := concat_ws(', ', leftover, 'restock policy');
    END IF;
    IF EXISTS (SELECT 1 FROM item_recipe ir, jsonb_array_elements(ir.inputs) e
                WHERE jsonb_typeof(ir.inputs) = 'array'
                  AND e->>'item' IN (SELECT old_name FROM llm596_rename)) THEN
        leftover := concat_ws(', ', leftover, 'item_recipe.inputs');
    END IF;
    IF EXISTS (SELECT 1 FROM visitor v
                WHERE v.plan IS NOT NULL
                  AND (v.plan #>> '{trade,good}' IN (SELECT old_name FROM llm596_rename)
                       OR EXISTS (SELECT 1 FROM jsonb_each(COALESCE(v.plan->'inventory', '{}'::jsonb)) kv
                                   WHERE kv.key IN (SELECT old_name FROM llm596_rename)))) THEN
        leftover := concat_ws(', ', leftover, 'visitor.plan');
    END IF;

    IF leftover IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-596: retired garment names still referenced in: %', leftover;
    END IF;
END $$;

COMMIT;
