-- ZBBS-WORK-307: drop the two dead v1 PC-feature tables.
--
-- sealed_note (backed pc/deliver-note) and npc_errand_offer (backed
-- pc/accept-errand / pc/complete-errand) were scaffolded server-side in v1 but
-- never wired into gameplay: there is no INSERT path for either table anywhere
-- in the codebase, the Godot client never called the routes, so both tables are
-- always empty. The v2 engine (engine/sim) never references them. Confirmed
-- FK-clean — nothing references these tables — so DROP needs no CASCADE; each
-- table's owned sequence + indexes drop automatically with it.
--
-- NOTE: summon_errand is a SEPARATE, live NPC<->NPC system and is intentionally
-- NOT touched here.
--
-- v1 reader code (engine/sealed_note.go, engine/npc_errand_offer.go, the dead
-- routes, the visibleDeliveredNotes call) is left in place: the v1 monolith is
-- being deleted wholesale a few weeks after v2 go-live and will not boot again
-- against this schema, so per-file cleanup would be wasted churn.

BEGIN;

DROP TABLE IF EXISTS public.sealed_note;
DROP TABLE IF EXISTS public.npc_errand_offer;

COMMIT;
