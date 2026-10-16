-- LLM-807: the Tavern's upstairs hall, the room between its stairs and its
-- bedrooms (kind `hall`, added by LLM-801-room-kind-hall) — the Inn's got
-- its row from LLM-801b-inn-upstairs-hall.
--
-- structure_room is engine-checkpointed (snapshot_gen; structures.go UPSERTs
-- it and prunes by gen). LoadAll has no gen filter, so a row inserted here
-- enters memory at boot and the first checkpoint re-stamps its snapshot_gen
-- off the column default (0) — the LLM-29 / LLM-50 pattern. Apply with the
-- engine STOPPED; the standard deploy does. id is left to the sequence.
--
-- Sourced from `structure`, so it is a clean no-op where the Tavern is
-- absent (a re-run, guarded by NOT EXISTS; the bare integration template DB).
--
-- The insert runs only when the `hall` value exists, as in LLM-801b: where
-- LLM-801-room-kind-hall is still pending (a database behind both), the
-- deploy's pre-stop dry run runs each pending file and rolls it back, so this
-- file's dry run would see no `hall` value; checking pg_enum, and casting in
-- EXECUTE so nothing is resolved before the check, keeps that dry run from
-- failing on a value the real apply will have.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_enum e
        JOIN pg_type t ON t.oid = e.enumtypid
        JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = 'public'
          AND t.typname = 'room_kind'
          AND e.enumlabel = 'hall'
    ) THEN
        EXECUTE $insert$
            INSERT INTO structure_room (structure_id, kind, name)
            SELECT s.id, 'hall'::public.room_kind, 'hall'
            FROM structure s
            WHERE s.id = '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'  -- Tavern
            AND NOT EXISTS (
                SELECT 1 FROM structure_room sr
                WHERE sr.structure_id = s.id AND sr.name = 'hall'
            )
        $insert$;
    END IF;
END
$$;

COMMIT;
