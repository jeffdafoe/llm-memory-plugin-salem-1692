-- ZBBS-083: Overseer attend_to — settings for need thresholds and dispatch ceiling.
--
-- Reframes the "chronicler" as an active overseer/director. Adds the
-- attend_to(villager) tool that lets the overseer rouse an NPC whose body
-- is in distress. Wake-driver lives entirely in the overseer's tool calls
-- — the engine does NOT auto-tick NPCs based on need state; need-driven
-- attention is the overseer's directorial responsibility.
--
-- Per-need thresholds for "in distress." Surfaced in both the NPC's own
-- self-perception (via banded labels) and in the overseer's
-- "Villagers needing attention" list (binary: at-or-above threshold). The
-- defaults reflect rough realism — thirst outpaces hunger which outpaces
-- tiredness — but are tunable from the admin UI without code changes.
--
-- chronicler_dispatch_ceiling caps the number of attend_to calls one fire
-- can issue. Without it a single phase fire could rouse every NPC in the
-- village and burn budget. Default 12 lets the overseer attend to most of
-- a small village in one fire while preventing runaway. The counter is
-- per-fire (not per-day); each fire starts fresh.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('hunger_red_threshold',       '18', 'Hunger value (0-24) at which an NPC is surfaced to the overseer as in distress, and at which the NPC perceives themselves as "hungry" rather than "peckish".'),
    ('thirst_red_threshold',       '12', 'Thirst value (0-24) at which an NPC is surfaced to the overseer as in distress, and at which the NPC perceives themselves as "parched" rather than "thirsty". Lower than hunger because real bodies notice thirst sooner.'),
    ('tiredness_red_threshold',    '20', 'Tiredness value (0-24) at which an NPC is surfaced to the overseer as in distress, and at which the NPC perceives themselves as "weary" rather than "tired". Higher than the others because people carry on tired longer than hungry.'),
    ('chronicler_dispatch_ceiling', '12', 'Maximum number of attend_to(villager) calls the overseer may make in a single fire (per fire, NOT per day — three phase fires + cascade fires each get their own counter). Caps cost; further calls within the same fire are rejected with a "[Limit reached]" tool result.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
