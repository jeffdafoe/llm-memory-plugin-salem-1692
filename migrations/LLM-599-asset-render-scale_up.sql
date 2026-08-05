-- LLM-599: per-asset render scale for object sprites.
--
-- Character sprites have had this dial since the ducks shipped too small:
-- npc_sprite.render_scale (default 2.0, ducks live at 1.4). Object assets
-- never got one — the Godot client hardcodes 2x for every placed object —
-- so when a purchased art pack draws its subject at a different apparent
-- scale than the village (the Mana Seed school-of-fish frames carry fish
-- a quarter the ink of a duck in the same 32x32 frame), there is no way
-- to reconcile it short of rescaling every object in the village at once.
--
-- The 2.0 default reproduces the current hardcoded rendering exactly, so
-- this is inert until a row is deliberately changed. Like npc_sprite,
-- tuning is DB-only (no editor UI); the catalog is boot-loaded, so a
-- change takes effect at engine restart, not live.

BEGIN;

ALTER TABLE asset ADD COLUMN render_scale double precision NOT NULL DEFAULT 2.0;

COMMIT;
