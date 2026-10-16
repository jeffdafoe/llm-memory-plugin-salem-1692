-- LLM-801: the Inn's upstairs hall, the room between its stairs and its
-- bedrooms (kind `hall`, added by LLM-801-room-kind-hall).
--
-- structure_room is engine-checkpointed (snapshot_gen; structures.go UPSERTs
-- it and prunes by gen). LoadAll has no gen filter, so a row inserted here
-- enters memory at boot and the first checkpoint re-stamps its snapshot_gen
-- off the column default (0) — the LLM-29 / LLM-50 pattern. Apply with the
-- engine STOPPED; the standard deploy does. id is left to the sequence.
--
-- Sourced from `structure`, so it is a clean no-op where the Inn is absent (a
-- re-run, guarded by NOT EXISTS; the bare integration template DB).
--
-- The insert runs only when the `hall` value exists. In a real apply it
-- always does: LLM-801-room-kind-hall sorts first and commits first. The
-- deploy's pre-stop dry run, though, runs each pending file and rolls it back,
-- so this file's dry run sees no `hall` value; checking pg_enum, and casting
-- in EXECUTE so nothing is resolved before the check, keeps that dry run from
-- failing on a value the real apply will have.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_enum e
        JOIN pg_type t ON t.oid = e.enumtypid
        WHERE t.typname = 'room_kind' AND e.enumlabel = 'hall'
    ) THEN
        EXECUTE $insert$
            INSERT INTO structure_room (structure_id, kind, name)
            SELECT s.id, 'hall'::public.room_kind, 'hall'
            FROM structure s
            WHERE s.id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c'  -- Inn
            AND NOT EXISTS (
                SELECT 1 FROM structure_room sr
                WHERE sr.structure_id = s.id AND sr.name = 'hall'
            )
        $insert$;
    END IF;
END
$$;

COMMIT;
