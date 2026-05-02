-- ZBBS-102: separate break_until column on actor.
--
-- take_break originally reused agent_override_until for both scheduler
-- suppression AND knock narration ("Josiah has stepped away — back at X").
-- That conflated two different things: the override is also bumped to
-- NOW + 30min by every routine move_to, so a vendor walking from his
-- stall to the tavern for lunch caused his stall to read as "stepped
-- away" for any PC who clicked it during the next 30 minutes.
--
-- Two columns, two purposes:
--   - agent_override_until: scheduler-suppression timer. Set by any
--     move_to to keep the worker scheduler from snapping the NPC back
--     mid-walk. Short (30min) and frequent.
--   - break_until: explicit "I'm closed for business" stamp. Set ONLY
--     by take_break. Drives knock narration so the player gets honest
--     "stepped away" text instead of false positives.

ALTER TABLE actor ADD COLUMN break_until TIMESTAMPTZ NULL;
