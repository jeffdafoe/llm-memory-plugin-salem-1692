-- LLM-456: wells, bridges, outhouses and the covered wagon are props, not
-- structures.
--
-- A root placement of a category='structure' asset is auto-promoted to a
-- Structure on drop (client world.gd _on_object_saved → the promote route), and
-- the server guard (sim.PromoteObjectToStructure) admits any category='structure'
-- asset. These five assets carried that category, so a NEW well placed in the
-- editor would become a Structure — and a structure-backed placement drops out
-- of the bare-object move_to path (resolveObjectByPerceivableName), so NPCs
-- could no longer walk to it to draw water. The eight existing placements were
-- placed before auto-promote and are all bare; this keeps future ones bare.
--
-- Moving them to 'prop' takes them out of both gates with no code change. The
-- category also gates three editor affordances — the door marker, the stand
-- marker and the home/work pick list — none of which a prop needs. Their
-- door_offset values stay: pathfinding reads them whatever the category.
--
-- Pinned by asset id, not name. These are catalog rows, not seeded by any
-- migration, so a schema-only replay has none of them; the checks below assert
-- only the targets that exist (the LLM-654 convention). On prod all five exist
-- and were 'structure' when this was written (2026-10-01).
--
-- asset is engine-owned, but deploy.sh runs stop -> migrate -> start, and the
-- catalog is boot-loaded, so the engine picks the new category up on start.

BEGIN;

DO $$
BEGIN
    -- Precondition: an existing target is a structure (or already a prop, on a
    -- rerun). Anything else means the catalog is not what this was written for.
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
           AND category NOT IN ('structure', 'prop')
    ) THEN
        RAISE EXCEPTION 'LLM-456: a well/bridge/outhouse/wagon asset has an unexpected category';
    END IF;

    -- A promoted placement of one of these assets would be a Structure on a
    -- prop asset — a state the promote guard forbids. None exists today; refuse
    -- rather than create one.
    IF EXISTS (
        SELECT 1
          FROM public.village_object vo
          JOIN public.structure s ON s.id = vo.id::text
         WHERE vo.asset_id IN (
             '35c7f051-266c-42a9-8650-f4f7ad4dc6a6',
             '3500f9ce-59be-45c1-9917-50bf0e232f4e',
             '053ec0f2-79bd-4db6-a9df-fec642792244',
             'afc2110d-e882-461b-91c5-abb363a76476',
             '64b37a3f-8b77-4083-bee9-37fe197b8270'
         )
    ) THEN
        RAISE EXCEPTION 'LLM-456: a placement of a well/bridge/outhouse/wagon asset already backs a structure';
    END IF;
END $$;

UPDATE public.asset
   SET category = 'prop'
 WHERE category = 'structure'
   AND id IN (
       '35c7f051-266c-42a9-8650-f4f7ad4dc6a6',  -- Bridge
       '3500f9ce-59be-45c1-9917-50bf0e232f4e',  -- Covered Wagon
       '053ec0f2-79bd-4db6-a9df-fec642792244',  -- Outhouse 1
       'afc2110d-e882-461b-91c5-abb363a76476',  -- Well (Bucket)
       '64b37a3f-8b77-4083-bee9-37fe197b8270'   -- Well (Roofed)
   );

-- Postcondition: every target that exists is now a prop.
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
           AND category <> 'prop'
    ) THEN
        RAISE EXCEPTION 'LLM-456: a well/bridge/outhouse/wagon asset is not a prop after update';
    END IF;
END $$;

COMMIT;
