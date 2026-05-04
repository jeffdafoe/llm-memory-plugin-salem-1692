-- ZBBS-119: chronicler tick budget — promote the per-fire iteration cap to a setting.
--
-- The chronicler's harness loop in fireChronicler runs up to N
-- iterations per fire, where each iteration is one model API call
-- processing one tool call (set_environment, record_event, recall,
-- attend_to, or done). The prior in-code constant capped this at 4,
-- which was generous when each chronicler fire saw one event (the
-- pre-buffering "one cascade per arrival" world). With the buffered
-- dispatcher consolidating 5+ events into one fire, the chronicler
-- needs more iterations to process them all — at 4 iterations the
-- attend ceiling is effectively ~3 NPCs per fire (one slot for done).
--
-- Default 8 doubles practical attend throughput. Bounds [1, 32] in
-- code; 32 is a sanity ceiling, not an expected operating point. The
-- per-fire attend cap (chronicler_dispatch_ceiling) remains the
-- higher ceiling on attend specifically.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('chronicler_tick_budget', '8', 'Max iterations per chronicler fire (1-32). Each iteration is one model API call processing one tool call. Default 8 — pre-buffering this was a 4 in-code constant; bumped to give the chronicler room to process more events per consolidated fire. Higher = more attend dispatches per fire, more cost per fire; lower = tighter cost cap, fewer attends. Out-of-range values fall back to the default.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
