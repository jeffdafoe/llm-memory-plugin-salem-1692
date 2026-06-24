-- LLM-29: give the two live-in keepers private staff quarters to sleep in.
--
-- An off-shift innkeeper / tavernkeeper sleeps on-premises by design (they run a
-- night shift), but the v2 sleep machine only bedded LODGERS into a room — a
-- keeper slept in place, at the storefront counter, staying co-present in the
-- common room for huddle/scene logic (the fidelity bug). The engine arm
-- (executeNPCSleep -> keeperStaffRoomAt) now beds a home==work keeper into a
-- `staff` room of its own structure. But a keeper cannot bed into a `private`
-- room (those are ledger-gated for paying lodgers, canEnterRoom), and the live
-- village had ZERO staff rooms anywhere — so the engine arm is inert without this.
--
-- Scope: the only two keepers who sleep on-premises (home == work, no separate
-- residence) are Hannah Boggs (Inn) and John Ellis (Tavern) — both lodging houses.
-- Every other keeper lives in a named residence or lodges out, so they walk home
-- to sleep and need no on-site quarters.
--
-- structure_room is an engine-checkpointed table (snapshot_gen; structures.go
-- UPSERTs it and prunes by gen). LoadAll has no gen filter, so a row inserted here
-- enters memory at boot and the first checkpoint re-stamps its snapshot_gen off the
-- column default (0) — the LLM-50 pattern. Must apply with the engine STOPPED; the
-- standard deploy does this automatically (down -> migrate -> up, HOME-440). id is
-- left to the structure_subspace_id_seq default.
--
-- Sourced from `structure` (not a literal VALUES list) so it is a clean no-op when
-- a target structure is absent: both on a re-run (the NOT EXISTS guard) and on a
-- bare schema with no village data (the integration template DB) — which avoids an
-- FK-violating INSERT of a room for a non-existent structure.

BEGIN;

INSERT INTO structure_room (structure_id, kind, name)
SELECT s.id, 'staff'::public.room_kind, 'keeper_quarters'
FROM structure s
WHERE s.id IN (
    '019d98af-ac9b-7833-8e03-5a7015bb5b0c',  -- Inn    (Hannah Boggs)
    '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'   -- Tavern (John Ellis)
)
AND NOT EXISTS (
    SELECT 1 FROM structure_room sr
    WHERE sr.structure_id = s.id
      AND sr.kind = 'staff'
);

COMMIT;
