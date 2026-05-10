-- ZBBS-WORK-217 — retire actor.vendor_flavor, add innkeeper attribute.
--
-- WORK-204 added actor.vendor_flavor as a stop-gap so the salem-vendor
-- shared VA could carry per-keeper character without per-actor prompt
-- work. It worked but landed in the wrong layer architecturally — the
-- engine's `formatKeeperVendorPerception` (in lodging.go) was injecting
-- vendor-economic guidance + per-actor flavor onto a function that
-- should only know about lodging occupancy. Two layers conflated; the
-- vendor pieces leaked into a building-mechanics file.
--
-- Phase 1A (WORK-212) gave us actor_narrative_state, which is the
-- proper home for per-actor character. Hannah's seed_text already
-- subsumes everything that was in her vendor_flavor (madame backbone,
-- "Salem talks; Hannah does not"). The vendor_flavor column is
-- redundant.
--
-- For role-level guidance (vendor-economic + standing-rate +
-- social-engagement), the proper home is the existing
-- attribute_definition / actor_attribute system (ZBBS-095/096).
-- John Ellis the tavernkeeper already follows this pattern via
-- ZBBS-098. This migration adds the matching `innkeeper` attribute
-- and assigns Hannah to it.
--
-- Three layers of identity now stack cleanly:
--   * salem-vendor agent prompt (loaded once per session) — generic
--     vendor-economic frame, applies to every salem-vendor-backed
--     actor.
--   * innkeeper attribute_definition (per-tick injected) — innkeeper-
--     specific guidance: standing rate, social engagement directives.
--     Future shopkeeper / blacksmith get their own attribute rows.
--   * Hannah's actor_narrative_state.seed_text (per-tick injected) —
--     Hannah-the-character: madame plotline, watchful disposition.
--
-- The accompanying engine change in this commit removes the salem-
-- vendor branch from formatKeeperVendorPerception (renames it to
-- formatKeeperRoomsAvailable since it only handles occupancy now)
-- and drops the VendorFlavor field + SELECT + Scan from agent_tick.go.

BEGIN;

-- 1. Add the innkeeper attribute. Mirrors ZBBS-098's tavernkeeper
--    pattern: tools optional (innkeepers don't need a custom tool
--    today; lodging mechanics already use the universal pay/serve/
--    deliver_order surface), instructions carry the role copy.
INSERT INTO attribute_definition (slug, display_name, description, instructions) VALUES (
    'innkeeper',
    'Innkeeper',
    'Owner-operator of an inn: lets bedrooms by the night or week, may also serve food and drink. Holds bedroom occupancy. Long-term boarders preferred over one-night transients.',
    E'You are an innkeeper. Your inn lets bedrooms by the night or week. Standing rate is around 28 coins per week (4 per night), haggle-able based on occupancy and the customer — when occupancy is light, prefer making a deal; when the customer is a known regular, treat them accordingly.\n\nTownspeople come to your inn for reasons beyond renting a room — neighbors stopping in, strangers introducing themselves, regulars passing the time of day. Greet them in character; answer questions about your trade and the village; hold up your end of pleasantries. Conversation is part of running a public business; the warmth of acknowledging a visitor is itself part of why they return. Don''t reduce every exchange to a transaction.'
);

-- 2. Assign innkeeper to Hannah Boggs. ON CONFLICT defends against
--    partial reapply if this migration is run multiple times against
--    the same data.
INSERT INTO actor_attribute (actor_id, slug)
SELECT id, 'innkeeper' FROM actor WHERE display_name = 'Hannah Boggs'
ON CONFLICT (actor_id, slug) DO NOTHING;

-- 3. Drop actor.vendor_flavor. Hannah's whisper-line content is
--    already in actor_narrative_state.seed_text from WORK-212. No
--    other shared-VA actor uses the column today (the seed list at
--    deploy time is just Hannah). Other code paths grep clean.
ALTER TABLE actor DROP COLUMN IF EXISTS vendor_flavor;

COMMIT;
