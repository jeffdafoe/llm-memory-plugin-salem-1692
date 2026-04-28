-- ZBBS-082: PC WebSocket idle timeout setting.
--
-- The salem-engine Hub (engine/village_events.go) holds one WebSocket
-- per connected Godot client. Without an idle timeout a client whose
-- network drops or whose process is hard-killed leaves a zombie entry
-- in Hub.clients — broadcasts still try to push to its send channel
-- and the Hub still reports it as connected. The chronicler/overseer's
-- "is a PC observing right now?" perception line reads from Hub state,
-- so zombies make it lie.
--
-- pc_idle_timeout_seconds is the read deadline applied per connection.
-- The Hub pings each client at half this interval; the pong extends the
-- deadline. If no pong arrives within the window, the connection is
-- dropped and removed from Hub.clients.
--
-- 60s default is a balance: short enough that the overseer's view of
-- "who's watching" stays current within ~one game minute, long enough
-- to forgive a brief network blip without bouncing the player.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('pc_idle_timeout_seconds', '60', 'Drop a PC WebSocket if no pong is received within this many seconds. Hub pings every (timeout/2) seconds.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
