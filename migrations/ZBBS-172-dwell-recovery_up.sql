-- ZBBS-172 — dwell recovery mechanic
--
-- Adds a "stay-and-recover" mechanism on top of the existing one-shot
-- arrival/consume model so trees, wells, and meals all reward dwelling
-- in place. Resolves the design half of
-- shared/tasks/pending/salem-tiredness-recovery-without-lodging.
--
-- The existing one-shot path stays exactly as-is: arrival at an
-- object_refresh-bearing structure still applies `amount`, and consuming
-- an item still applies `item_satisfies.amount`. The new columns layer
-- a per-tick credit on top:
--
--   - object_refresh.dwell_amount, dwell_period_minutes
--       Optional (both null = legacy one-shot). When set, an actor
--       remaining at the object's loiter pin gets `dwell_amount` applied
--       every `dwell_period_minutes` of continuous presence.
--   - item_satisfies.dwell_amount, dwell_period_minutes, dwell_total_ticks
--       Optional (all three null = legacy one-shot). When set, eating
--       at a structure pins the meal to that structure and applies
--       `dwell_amount` every `dwell_period_minutes` for `dwell_total_ticks`
--       ticks. Walking away ends the meal early.
--
-- actor_dwell_credit tracks last-credit timestamps and (for items)
-- the remaining countdown. Per-minute dwell tick handler in
-- engine/dwell_tick.go drives the credits.
--
-- Backfill at the bottom adjusts the existing Picnic Area, the two
-- previously-anonymous Maple trees (now "Shade Tree"), the village
-- well, and stew so the new mechanic has live content from day one.

BEGIN;

-- object_refresh: dwell columns. Sign matches existing `amount<0`
-- convention (recovery = negative).
ALTER TABLE object_refresh
    ADD COLUMN dwell_amount         smallint,
    ADD COLUMN dwell_period_minutes integer;

ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_dwell_amount_negative
        CHECK (dwell_amount IS NULL OR dwell_amount < 0),
    ADD CONSTRAINT object_refresh_dwell_period_positive
        CHECK (dwell_period_minutes IS NULL OR dwell_period_minutes > 0),
    ADD CONSTRAINT object_refresh_dwell_pair
        CHECK ((dwell_amount IS NULL) = (dwell_period_minutes IS NULL));

-- item_satisfies: dwell columns. Sign matches existing `amount>0`
-- convention (item magnitude; engine negates on apply).
ALTER TABLE item_satisfies
    ADD COLUMN dwell_amount         integer,
    ADD COLUMN dwell_period_minutes integer,
    ADD COLUMN dwell_total_ticks    integer;

ALTER TABLE item_satisfies
    ADD CONSTRAINT item_satisfies_dwell_amount_positive
        CHECK (dwell_amount IS NULL OR dwell_amount > 0),
    ADD CONSTRAINT item_satisfies_dwell_period_positive
        CHECK (dwell_period_minutes IS NULL OR dwell_period_minutes > 0),
    ADD CONSTRAINT item_satisfies_dwell_total_ticks_positive
        CHECK (dwell_total_ticks IS NULL OR dwell_total_ticks > 0),
    ADD CONSTRAINT item_satisfies_dwell_triple
        CHECK (
            (dwell_amount IS NULL AND dwell_period_minutes IS NULL AND dwell_total_ticks IS NULL)
            OR
            (dwell_amount IS NOT NULL AND dwell_period_minutes IS NOT NULL AND dwell_total_ticks IS NOT NULL)
        );

-- actor_dwell_credit — one row per (actor, object, attribute, source).
--
-- source distinguishes object dwell ("sitting under a tree") from
-- item dwell ("eating stew at this place") so an actor can be doing
-- both at once. The object_id for an item credit is the structure the
-- actor was at when they started eating — the meal is pinned to the
-- place, not the world at large.
--
-- remaining_ticks NULL = unlimited (object dwell while present).
-- remaining_ticks set = countdown (item dwell, row deletes when zero).
--
-- dwell_delta and dwell_period_minutes are SNAPSHOTS taken at the
-- arrival/consume moment — the credit applies whatever rate was live
-- when the actor sat down or paid for the meal, even if an admin
-- edits object_refresh / item_satisfies mid-dwell. Storing the rate
-- on the credit also lets the dwell tick run a single self-contained
-- query without re-joining the source tables. dwell_delta is always
-- negative (matches the consumptionDelta convention, the engine
-- negates positive item amounts on insert).
CREATE TABLE actor_dwell_credit (
    actor_id             uuid                     NOT NULL REFERENCES actor(id)          ON DELETE CASCADE,
    object_id            uuid                     NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    attribute            varchar(32)              NOT NULL REFERENCES refresh_attribute(name) ON UPDATE CASCADE,
    source               varchar(16)              NOT NULL CHECK (source IN ('object', 'item')),
    last_credited_at     timestamp with time zone NOT NULL,
    remaining_ticks      integer                  CHECK (remaining_ticks IS NULL OR remaining_ticks > 0),
    dwell_delta          smallint                 NOT NULL CHECK (dwell_delta < 0),
    dwell_period_minutes integer                  NOT NULL CHECK (dwell_period_minutes > 0),
    PRIMARY KEY (actor_id, object_id, attribute, source),
    -- Item credits MUST carry a remaining_ticks countdown; object credits MUST NOT.
    -- Catches a misclassified upsert at the schema layer.
    CONSTRAINT actor_dwell_credit_remaining_matches_source
        CHECK (
            (source = 'item'   AND remaining_ticks IS NOT NULL)
            OR
            (source = 'object' AND remaining_ticks IS NULL)
        )
);

-- The dwell tick scans by (last_credited_at) within the eligible set;
-- a partial index on the source split avoids the most common scan
-- being slowed by the other source's rows.
CREATE INDEX ix_actor_dwell_credit_object_lcred ON actor_dwell_credit (last_credited_at) WHERE source = 'object';
CREATE INDEX ix_actor_dwell_credit_item_lcred   ON actor_dwell_credit (last_credited_at) WHERE source = 'item';

-- Backfill: existing tiredness/thirst sources get dwell rates so the
-- new perception block has something live to surface.

-- Picnic Area (Tree 2 with display_name 'Picnic Area'): smaller arrival
-- hit, modest dwell. A 23-tile walk loses ~3 tiredness to movement
-- fatigue; arrival -2 nets -0 on first hit, dwell rewards staying.
UPDATE object_refresh
   SET amount               = -2,
       dwell_amount         = -2,
       dwell_period_minutes = 15
 WHERE object_id  = '019dc5f4-306f-7607-a887-a8941c4bf176'
   AND attribute  = 'tiredness';

-- Two previously-anonymous Maple trees inside the village. Operator
-- (Jeff) confirmed naming both "Shade Tree" is fine — closest-wins
-- lookups, duplicate is just a cosmetic dropdown dup.
UPDATE village_object SET display_name = 'Shade Tree'
 WHERE id IN ('019d79ef-d9dc-71d0-84b7-53c64b79e98d', '019d79ef-d9dc-7b9e-a46b-56c819d0f758');

UPDATE object_refresh
   SET dwell_amount         = -1,
       dwell_period_minutes = 10
 WHERE object_id IN ('019d79ef-d9dc-71d0-84b7-53c64b79e98d', '019d79ef-d9dc-7b9e-a46b-56c819d0f758')
   AND attribute  = 'tiredness';

-- Wells: kill the drive-by full-thirst-reset. -8 arrival is a real
-- gulp (still meaningful for someone passing through), and the dwell
-- rate (-1 every 2 minutes) means fully quenching from 24 takes
-- ~32 minutes of standing at the well. Real cost in time.
--
-- Scoped to the village well (UUID below) rather than all thirst
-- refresh rows. Sweeping by attribute='thirst' would silently rewrite
-- any operator-added thirst source with different rate intent.
-- If new wells are added later they can opt into the same rates via a
-- targeted migration or admin tool.
UPDATE object_refresh
   SET amount               = -8,
       dwell_amount         = -1,
       dwell_period_minutes = 2
 WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute = 'thirst';

-- Stew: shift from one-shot 12-hunger to a 16-minute meal. Total
-- recovery is preserved (4 immediate + 8 ticks × 1 = 12), but the
-- buyer must remain at the structure for the full meal to get the
-- dwell payoff. Walking out of the tavern mid-meal forfeits the rest.
UPDATE item_satisfies
   SET amount               = 4,
       dwell_amount         = 1,
       dwell_period_minutes = 2,
       dwell_total_ticks    = 8
 WHERE item_kind = 'stew'
   AND attribute = 'hunger';

-- Critical-tier threshold for tiredness perception gates. Stored as a
-- percentage of needMax so the absolute value tracks if needMax ever
-- changes. 90% of 24 = 21.6 → 22 (engine ceil). Two ticks of grace
-- past red (20) before max-collapse (24).
INSERT INTO setting (key, value, description, is_public)
     VALUES ('tiredness_critical_threshold_pct',
             '90',
             'Critical-tier tiredness threshold as percent of needMax. Engine computes the absolute as ceil(needMax * pct / 100). Lifts the on-shift gate that hides home/inn/tavern from tired-NPC recovery options. Default 90.',
             false)
ON CONFLICT (key) DO NOTHING;

-- The deploy playbook stamps migrations_applied for us after running
-- this file (see infrastructure/playbooks/deploy.yml). Including the
-- INSERT here causes a duplicate-key violation when the playbook
-- re-stamps the row.

COMMIT;
