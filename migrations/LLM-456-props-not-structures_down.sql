-- LLM-456 down: return the five assets to category 'structure'. All five were
-- 'structure' on prod before the up ran (checked 2026-10-01), so this is an
-- exact inverse there. As in the up, only targets that exist are asserted.

BEGIN;

UPDATE public.asset
   SET category = 'structure'
 WHERE category = 'prop'
   AND id IN (
       '35c7f051-266c-42a9-8650-f4f7ad4dc6a6',  -- Bridge
       '3500f9ce-59be-45c1-9917-50bf0e232f4e',  -- Covered Wagon
       '053ec0f2-79bd-4db6-a9df-fec642792244',  -- Outhouse 1
       'afc2110d-e882-461b-91c5-abb363a76476',  -- Well (Bucket)
       '64b37a3f-8b77-4083-bee9-37fe197b8270'   -- Well (Roofed)
   );

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM public.asset
         WHERE id IN (
             '35c7f051-266c-42a9-8650-f4f7ad4dc6a6',
             '3500f9ce-59be-45c1-9917-50bf0e232f4e',
             '053ec0f2-79bd-4db6-a9df-fec642792244',
             'afc2110d-e882-461b-91c5-abb363a76476',
             '64b37a3f-8b77-4083-bee9-37fe197b8270'
         )
           AND category <> 'structure'
    ) THEN
        RAISE EXCEPTION 'LLM-456: a well/bridge/outhouse/wagon asset is not a structure after down';
    END IF;
END $$;

COMMIT;
