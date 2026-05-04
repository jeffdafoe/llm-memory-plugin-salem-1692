-- ZBBS-120: widen setting.description to TEXT and backfill ZBBS-119 settings.
--
-- The ZBBS-119 migrations (chronicler-buffered-dispatch and
-- chronicler-tick-budget) silently failed at deploy time. Their INSERTs
-- carried description strings >255 chars, but setting.description was
-- defined as varchar(255). Each INSERT errored with `value too long for
-- type character varying(255)` inside its BEGIN/COMMIT block, rolling
-- back. The deploy.yml migration runner invoked psql without
-- ON_ERROR_STOP, so psql exited 0 anyway and the && chain still
-- recorded the migration as applied. Net: the rows are missing from
-- setting, but migrations_applied claims they ran. See the playbook
-- fix in this same commit for the deploy-side half.
--
-- The description column is a human-readable explanation of what each
-- setting does; capping it at 255 chars served no purpose. Promoted to
-- TEXT so future settings with thorough descriptions don't repeat the
-- failure.
--
-- Then re-INSERT the three setting rows that ZBBS-119 should have
-- landed. ON CONFLICT DO NOTHING — if a future hand-fix or admin UI
-- create has already populated any of these, leave the existing row
-- alone. Engine code-side defaults match the values below, so even
-- without these rows behavior is unchanged; the rows exist so admins
-- can tweak via the dashboard.

BEGIN;

ALTER TABLE setting ALTER COLUMN description TYPE TEXT;

INSERT INTO setting (key, value, description) VALUES
    ('chronicler_buffer_window_seconds', '60', 'Buffered chronicler dispatch window, in seconds (5-600). Routine events (arrival, shift_boundary, atmosphere, needs_resolved) accumulate in the dispatcher queue and flush as a single consolidated chronicler fire when the window elapses. Default 60 aligns with the per-minute scheduler beat. Higher = cheaper + more coalesced narrative; lower = more responsive + more fires. Only consulted when chronicler_buffered_dispatch=true.'),
    ('chronicler_buffered_dispatch',     'false', 'Feature flag for the buffered chronicler dispatcher. When false, the legacy immediate-fire path runs unchanged (every cascade origin fires its own chronicler scene). When true, routine events are buffered through the dispatcher and high-priority events (PC speech, PC arrival, admin attend-now) early-flush the buffer. All fires serialize through one in-flight slot per world. Set to true once the new path has been observed clean for a session.'),
    ('chronicler_tick_budget',           '8', 'Max iterations per chronicler fire (1-32). Each iteration is one model API call processing one tool call. Default 8 — pre-buffering this was a 4 in-code constant; bumped to give the chronicler room to process more events per consolidated fire. Higher = more attend dispatches per fire, more cost per fire; lower = tighter cost cap, fewer attends. Out-of-range values fall back to the default.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
