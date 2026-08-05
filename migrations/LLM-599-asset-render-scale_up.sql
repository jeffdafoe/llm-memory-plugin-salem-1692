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

-- The CHECK rejects the float values that would produce an unusable sprite
-- transform: zero/negative, NaN, and the infinities. Postgres has no
-- isfinite() for double precision, but its float ordering treats NaN as
-- greater than every other value INCLUDING +Infinity (so `NaN > 0` is true
-- and a bare positivity check would admit it) — `render_scale < 'Infinity'`
-- therefore excludes both NaN and +Infinity in one clause, and `> 0`
-- excludes -Infinity. The client keeps its own absent/zero guard as defense
-- in depth.

BEGIN;

ALTER TABLE asset ADD COLUMN render_scale double precision NOT NULL DEFAULT 2.0
    CONSTRAINT asset_render_scale_positive_finite
    CHECK (render_scale > 0 AND render_scale < 'Infinity'::double precision);

COMMIT;
