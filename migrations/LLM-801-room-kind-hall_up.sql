-- LLM-801: a room kind for an inn's upstairs hall.
--
-- The Inn's upstairs becomes somewhere a player can be: they climb from the
-- entry hall into a hall with the bedroom doors in a row, and from it into
-- their own room. The hall is neither `common` (anyone may enter, and the
-- whole building hears it) nor `private` (one paid lodger's) nor `staff` (the
-- keeper's alone): its lodgers and its keeper may be in it, and speech there
-- stays there. The engine's RoomKindHall and canEnterRoom carry that rule.
--
-- Only the enum value here. Postgres refuses to use an enum value in the
-- transaction that added it ("unsafe use of new value"), and every migration
-- file is one transaction (the deploy's dry run depends on it), so the hall
-- row itself is LLM-801b-inn-upstairs-hall, which sorts after this file.

BEGIN;

ALTER TYPE public.room_kind ADD VALUE IF NOT EXISTS 'hall';

COMMIT;
