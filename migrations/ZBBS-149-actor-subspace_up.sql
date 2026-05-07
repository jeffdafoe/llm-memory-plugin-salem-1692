-- ZBBS-149: subspace primitive — model "rooms within a structure" as
-- first-class entities with per-instance IDs and access control.
--
-- Motivation: actor.inside_structure_id is binary — an actor is either
-- inside a structure or not. NPC perception's co-presence query keys
-- off this single bit, so a sleeping lodger upstairs at the Tavern is
-- "here with John" and gets greeted while in bed. Real buildings have
-- public common areas and private rooms; the engine has been forcing
-- both into the same bucket.
--
-- The four pieces:
--
--   subspace_kind ENUM      categorizes subspace purpose. 'common' is
--                           the default open-to-all area; 'private'
--                           requires explicit grant; 'staff' is
--                           workplace-only (back of house, locked
--                           offices). Future kinds can extend via
--                           ALTER TYPE ... ADD VALUE.
--
--   structure_subspace      per-structure declaration of which
--                           subspaces exist. A Tavern has 'common'
--                           plus N 'bedroom_*' subspaces; a smithy
--                           probably has just 'common' (and maybe a
--                           'staff' workshop area later). Identity
--                           lives on the row id, separate from the
--                           display name — renaming a bedroom doesn't
--                           change identity.
--
--   subspace_access         who can enter which 'private' / 'staff'
--                           subspaces. Issued by deliver_order for
--                           lodging, by structure ownership for staff.
--                           Common subspaces don't need rows here —
--                           anyone can enter.
--
--   actor.inside_subspace_id  which subspace the actor is currently
--                           in. NULL when the actor isn't in a
--                           structure, or briefly during transitions.
--                           Co-presence queries that filter by
--                           inside_structure_id should also filter by
--                           this column.
--
-- Seed:
--   Every village_object that's currently or has been an actor's
--   inside_structure_id, home_structure_id, or work_structure_id gets
--   a 'common' subspace. Existing actors with non-null
--   inside_structure_id are placed in their structure's common
--   subspace.
--
--   Tavern (identified by the tavernkeeper attribute, mirroring
--   ZBBS-134's lookup pattern) gets four 'private' bedrooms named
--   bedroom_1..bedroom_4. Lodger assignment in code (executeDeliverOrder
--   for nights_stay) picks the first bedroom with no active access row.

BEGIN;

CREATE TYPE subspace_kind AS ENUM ('common', 'private', 'staff');

CREATE TABLE structure_subspace (
    id BIGSERIAL PRIMARY KEY,
    structure_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind subspace_kind NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (structure_id, name)
);

CREATE INDEX ix_structure_subspace_structure
    ON structure_subspace(structure_id);

-- subspace_access composite PK is (subspace_id, actor_id) — one access
-- row per (subspace, actor) pair. Re-issuing access (e.g. PC books a
-- second night in the same room) updates expires_at via UPSERT rather
-- than stacking rows. granted_via_ledger_id is informational; ON DELETE
-- SET NULL preserves the access record if the underlying ledger row is
-- ever purged.
CREATE TABLE subspace_access (
    subspace_id BIGINT NOT NULL REFERENCES structure_subspace(id) ON DELETE CASCADE,
    actor_id UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    granted_via_ledger_id BIGINT NULL REFERENCES pay_ledger(id) ON DELETE SET NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NULL,
    PRIMARY KEY (subspace_id, actor_id)
);

CREATE INDEX ix_subspace_access_actor ON subspace_access(actor_id);

ALTER TABLE actor
    ADD COLUMN inside_subspace_id BIGINT NULL REFERENCES structure_subspace(id);

-- Partial index so the foreign key lookup is cheap when filtering co-
-- presence by subspace ("show me everyone whose inside_subspace_id =
-- $X"). Most actors are outdoors at any moment so the partial saves
-- index size.
CREATE INDEX ix_actor_inside_subspace
    ON actor(inside_subspace_id)
    WHERE inside_subspace_id IS NOT NULL;

-- Seed common subspaces for every village_object that's been used as
-- a structure (referenced by any actor's inside/home/work). This
-- overcollects for any abandoned structures but the rows are cheap and
-- the alternative (lazy-create on first entry) leaks complexity into
-- the engine. ON CONFLICT DO NOTHING for re-runs.
INSERT INTO structure_subspace (structure_id, name, kind)
SELECT DISTINCT s.id, 'common', 'common'::subspace_kind
  FROM village_object s
 WHERE s.id IN (SELECT inside_structure_id FROM actor WHERE inside_structure_id IS NOT NULL)
    OR s.id IN (SELECT home_structure_id   FROM actor WHERE home_structure_id   IS NOT NULL)
    OR s.id IN (SELECT work_structure_id   FROM actor WHERE work_structure_id   IS NOT NULL)
ON CONFLICT (structure_id, name) DO NOTHING;

-- Seed Tavern bedrooms. The tavernkeeper's work_structure_id
-- identifies the Tavern (mirrors ZBBS-134's lookup). Four bedrooms
-- chosen because that's enough to exercise multi-lodger flows without
-- forcing capacity-pressure UX in v1.
INSERT INTO structure_subspace (structure_id, name, kind)
SELECT a.work_structure_id, 'bedroom_' || g, 'private'::subspace_kind
  FROM actor a
  JOIN actor_attribute aa ON aa.actor_id = a.id
  CROSS JOIN generate_series(1, 4) g
 WHERE aa.slug = 'tavernkeeper'
   AND a.work_structure_id IS NOT NULL
ON CONFLICT (structure_id, name) DO NOTHING;

-- Place existing actors whose inside_structure_id is set into the
-- common subspace of their structure. Without this, every actor
-- currently inside a building has inside_subspace_id NULL post-
-- migration and the next perception build treats them as in an
-- ambiguous state.
UPDATE actor a
   SET inside_subspace_id = ss.id
  FROM structure_subspace ss
 WHERE a.inside_structure_id IS NOT NULL
   AND a.inside_structure_id = ss.structure_id
   AND ss.kind = 'common';

-- Backfill subspace_access rows for existing 'delivered' nights_stay
-- ledger rows. Without this, lodgers who paid pre-migration have no
-- access rows and can't enter their bedroom via /pc/move-subspace
-- even though their lodger status is still active. Mirrors what
-- assignBedroomForLodger would have created on deliver_order.
--
-- Allocation: rank lodgers and rooms per structure by row_number,
-- then join on rn so two lodgers in the same structure don't both
-- collapse to the lowest-named available room. The naive correlated
-- subquery sees pre-INSERT state and would assign every lodger to
-- bedroom_1 — composite PK would let the second insert succeed (same
-- subspace, different actor), giving multiple lodgers a shared room
-- in violation of the design's privacy guarantee.
--
-- Lodgers: ordered by lodger_until ASC then ledger_id ASC, so
-- earliest-checkout gets the lowest-numbered room (deterministic).
-- Rooms: alphabetical by name. Lodgers with rn beyond available rooms
-- (more lodgers than vacant bedrooms) silently get no row — they
-- effectively need to re-check-in to get assigned.
WITH active_lodging AS (
    SELECT pl.id AS ledger_id,
           pl.buyer_id,
           seller.work_structure_id AS structure_id,
           (
             (pl.ready_by + GREATEST(COALESCE(pl.qty, 1), 1) * INTERVAL '1 day')::timestamp
             + COALESCE(
                 (SELECT value::int FROM setting WHERE key = 'lodging_check_out_hour'),
                 11
               ) * INTERVAL '1 hour'
           ) AS lodger_until
      FROM pay_ledger pl
      JOIN actor seller ON seller.id = pl.seller_id
     WHERE pl.item_kind = 'nights_stay'
       AND pl.state = 'accepted'
       AND pl.fulfillment_status = 'delivered'
       AND seller.work_structure_id IS NOT NULL
),
-- Collapse to one ledger per (structure, buyer) before ranking. A buyer
-- with multiple active 'delivered' nights_stay rows in the same
-- structure (legitimate scenario: paid for tonight, paid again for
-- next week) should still occupy ONE bedroom — runtime
-- assignBedroomForLodger extends the same row, the migration must
-- mirror. Without dedup, two ranks → two rooms → privacy violation.
-- max(lodger_until) keeps the longest-running grant; max(ledger_id) is
-- a deterministic tiebreaker for granted_via_ledger_id.
dedup_lodging AS (
    SELECT structure_id,
           buyer_id,
           max(lodger_until) AS lodger_until,
           max(ledger_id) AS ledger_id
      FROM active_lodging
     WHERE lodger_until > NOW()
     GROUP BY structure_id, buyer_id
),
ranked_lodging AS (
    SELECT *,
           row_number() OVER (
             PARTITION BY structure_id
             ORDER BY lodger_until ASC, ledger_id ASC
           ) AS rn
      FROM dedup_lodging
),
available_bedrooms AS (
    SELECT ss.structure_id,
           ss.id AS subspace_id,
           row_number() OVER (
             PARTITION BY ss.structure_id
             ORDER BY ss.name ASC
           ) AS rn
      FROM structure_subspace ss
     WHERE ss.kind = 'private'
       AND NOT EXISTS (
         SELECT 1 FROM subspace_access sa
          WHERE sa.subspace_id = ss.id
            AND (sa.expires_at IS NULL OR sa.expires_at > NOW())
       )
)
INSERT INTO subspace_access (subspace_id, actor_id, granted_via_ledger_id, expires_at)
SELECT ab.subspace_id, rl.buyer_id, rl.ledger_id, rl.lodger_until
  FROM ranked_lodging rl
  JOIN available_bedrooms ab
    ON ab.structure_id = rl.structure_id
   AND ab.rn = rl.rn
ON CONFLICT (subspace_id, actor_id)
DO UPDATE SET granted_via_ledger_id = EXCLUDED.granted_via_ledger_id,
              expires_at = EXCLUDED.expires_at,
              granted_at = NOW();

-- Place currently-sleeping lodgers into their bedroom subspace.
-- Sleep is a strong signal that the actor is "in the back" rather
-- than at the bar — without this, sleeping pre-migration lodgers
-- stay in 'common' and remain visible to keepers via the perception
-- co-presence filter (defeating the whole point of the change). Awake
-- lodgers stay in common; they can transition to private via
-- /pc/move-subspace once they want to step away.
--
-- The structure_subspace JOIN ensures the access row's subspace
-- belongs to the actor's CURRENT inside_structure_id. Without this
-- guard, an actor with an unexpired access row at Tavern A but
-- currently sleeping in Structure B would get inside_subspace_id
-- pointing into Tavern A — violating the (structure, subspace)
-- pairing the rest of this patch assumes.
UPDATE actor a
   SET inside_subspace_id = sa.subspace_id
  FROM subspace_access sa
  JOIN structure_subspace ss ON ss.id = sa.subspace_id
 WHERE sa.actor_id = a.id
   AND ss.structure_id = a.inside_structure_id
   AND a.sleeping_until IS NOT NULL
   AND a.inside_structure_id IS NOT NULL
   AND (sa.expires_at IS NULL OR sa.expires_at > NOW());

COMMIT;
