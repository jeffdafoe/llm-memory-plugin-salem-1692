-- ZBBS-175 — rename `inn` village_object_tag to `lodging`
--
-- Prep step for NPC sleep landing in the same PR. The NPC sleep gate
-- needs to identify "this is a place where someone can sleep" — a
-- generalization of "inn." Renaming aligns the tag with that concept
-- while preserving the existing inn behavior (recovery_options.go and
-- pc_handlers.go both look for inn/tavern as recovery sources).
--
-- The `tavern` tag stays separate because tavern is a combo
-- inn/restaurant — keeping the distinction lets future logic treat
-- "lodging-only" and "tavern" separately without unwinding a merge.
--
-- Pure data update on village_object_tag rows. No schema change —
-- village_object_tag.tag is varchar(64) text with allowlist enforced
-- in Go (engine/assets.go allowedObjectTags), not a constraint.
-- Companion Go-side rename in this commit flips the allowlist entry
-- and the SQL queries that reference 'inn' to use 'lodging'.

BEGIN;

UPDATE village_object_tag SET tag = 'lodging' WHERE tag = 'inn';

-- Settings for the NPC sleep mechanic landing in the same commit.
-- Defaults mirror PC sleep so PC and NPC sleep behave consistently
-- out of the box; per-side knobs let them diverge later if needed.
--
-- ON CONFLICT DO NOTHING so re-running the migration after a partial
-- application or a manual operator-side seed is harmless.
INSERT INTO setting (key, value, description, is_public) VALUES
    ('npc_sleep_max_duration_hours',  '12', 'Safety cap (hours) on sleeping_until for NPCs. Recovery typically wakes them sooner via tiredness=0; this is the backstop.', false),
    ('npc_auto_sleep_min_tiredness',  '10', 'NPCs arriving home below this tiredness skip the auto-sleep trigger. Mirrors pc_idle_sleep_min_tiredness so a freshly-rested NPC dropping by home does not auto-sleep.', false)
ON CONFLICT (key) DO NOTHING;

COMMIT;
