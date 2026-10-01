-- LLM-456 down: return the five assets to category 'structure'. Category-only
-- change, so this reverts cleanly on its own.

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

COMMIT;
