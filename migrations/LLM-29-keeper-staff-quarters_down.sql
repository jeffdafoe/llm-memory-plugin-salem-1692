-- LLM-29 rollback: remove the two keeper staff quarters.
--
-- Clear any sleeper's room scope first: inside_room_id is a persisted, checkpointed
-- actor column, so a keeper asleep in one of these rooms at rollback time would
-- otherwise boot with a dangling inside_room_id ref (validateActorStructureRefs
-- hard-fails on a room that no longer resolves to the actor's structure). Null it,
-- then drop the rooms. Apply with the engine STOPPED (the deploy does this
-- automatically; an ad-hoc rollback needs a manual stop -> SQL -> start).

BEGIN;

UPDATE actor
SET inside_room_id = NULL
WHERE inside_room_id IN (
    SELECT id FROM structure_room
    WHERE kind = 'staff'
      AND name = 'keeper_quarters'
      AND structure_id IN (
          '019d98af-ac9b-7833-8e03-5a7015bb5b0c',
          '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'
      )
);

DELETE FROM structure_room
WHERE kind = 'staff'
  AND name = 'keeper_quarters'
  AND structure_id IN (
      '019d98af-ac9b-7833-8e03-5a7015bb5b0c',
      '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'
  );

COMMIT;
