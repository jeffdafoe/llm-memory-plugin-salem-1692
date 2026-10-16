-- LLM-797: milk comes from the cows. A milking animal is a berry bush that
-- walks: it carries a bush's gather row (yield-only, finite, regrowing), saved
-- on its actor row; Elizabeth Ellis milks the cows one at a time instead of
-- making milk from nothing at Ellis Farm.
--
-- 1. actor.livestock_yield — the animal's gather row plus a pending feed bonus
--    (engine/sim/livestock.go LivestockYield; wire shape in
--    engine/sim/repo/pg/livestock_yield.go). NULL for every other actor.
-- 2. The four cows get a row: 4 milk, refilling to full every 12 hours. The
--    heifers (and every sheep, hen and rooster) get none. Pinned by actor id.
-- 3. Elizabeth's dairykeeper milk entry turns from `produce` to `forage`: she
--    gathers milk like the water-drawers draw water (the yield-only rule).
-- 4. The milk recipe row stays for its price, like water / wheat / carrots
--    (nobody produces those either); its sage boost goes, since sage is now fed
--    to a cow instead.
--
-- actor and actor_attribute are CHECKPOINT-WRITTEN by the engine; the deploy runs
-- migrations with the engine stopped. An ad-hoc apply must stop the engine first.

BEGIN;

ALTER TABLE public.actor ADD COLUMN IF NOT EXISTS livestock_yield jsonb;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'actor_livestock_yield_object') THEN
        ALTER TABLE public.actor ADD CONSTRAINT actor_livestock_yield_object
            CHECK (livestock_yield IS NULL OR jsonb_typeof(livestock_yield) = 'object');
    END IF;
END $$;

UPDATE public.actor
SET livestock_yield = '{"gather_item": "milk", "available_quantity": 4, "max_quantity": 4, "refresh_mode": "periodic", "refresh_period_hours": 12}'::jsonb
WHERE id IN (
    '01695995-6b2c-4019-ac92-fcfcc4ce2515',  -- Cow
    '7781e870-7186-4fd3-81d8-eaad4dc57324',  -- Cow 2
    '750f36cd-a18b-414d-aaeb-cb115091c774',  -- Cow 3
    '9259d4b3-3595-4804-9a5b-feed8aa595aa'   -- Cow 4
)
  AND livestock_yield IS NULL;

DO $$
DECLARE n int;
BEGIN
    UPDATE public.actor_attribute
    SET params = jsonb_set(params, '{restock}', (
        SELECT jsonb_agg(CASE WHEN e->>'item' = 'milk' THEN jsonb_set(e, '{source}', '"forage"') ELSE e END)
        FROM jsonb_array_elements(params->'restock') e))
    WHERE actor_id = '019da6af-c8c9-7eb8-aead-759142785789'  -- Elizabeth Ellis
      AND slug = 'dairykeeper'
      AND params ? 'restock';
    GET DIAGNOSTICS n = ROW_COUNT;
    -- 0 rows is the unseeded case (fresh schema-only DB / integration harness).
    -- If Elizabeth exists without her dairykeeper row, fail loud rather than
    -- leave her making milk from nothing beside cows nobody milks.
    IF n = 0 AND EXISTS (SELECT 1 FROM public.actor WHERE id = '019da6af-c8c9-7eb8-aead-759142785789') THEN
        RAISE EXCEPTION 'LLM-797: Elizabeth Ellis exists but her dairykeeper restock row was not found';
    END IF;
END $$;

UPDATE public.item_recipe SET boost_inputs = '[]'::jsonb, updated_at = now()
WHERE output_item = 'milk';

COMMIT;
