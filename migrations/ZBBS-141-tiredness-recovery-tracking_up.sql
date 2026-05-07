-- ZBBS-141: per-minute tiredness recovery sweep needs a cursor column.
--
-- ZBBS-133 added in-needs_tick recovery for actors with break_until in
-- the future, applying a flat hour's worth of recovery at each hourly
-- needs_tick. That dropped sub-hour breaks (a 30-min break opening and
-- closing between two needs_ticks got zero recovery) and over-credited
-- breaks that happened to span a tick boundary.
--
-- The new runTirednessRecoverySweep goroutine fires every minute, tracks
-- a per-actor cursor, and applies recovery proportional to the elapsed
-- break minutes since the cursor's last advance. The cursor is stamped
-- to NOW() when take_break commits and advanced by exactly units / rate
-- minutes per applied chunk so leftover fractional minutes carry over
-- to the next sweep. NULL means "no break has ever stamped this actor"
-- — the sweep skips NULL rows; the next take_break commit fills it in.

ALTER TABLE actor ADD COLUMN last_tiredness_recovery_at TIMESTAMPTZ NULL;
