-- ZBBS-WORK-328: gatherable sources via object_refresh.gather_item.
--
-- Revives v1's `gather` as a general environmental-harvest substrate. A
-- non-NULL gather_item marks the refresh row's source object as harvestable:
-- an actor loitering at it can mint that item_kind into inventory — an NPC
-- via the `gather` tool, a PC via POST /api/village/pc/gather. Both actor
-- kinds draw down the SAME available_quantity counter (one shared stock per
-- source) and the existing regen tick (object_refresh continuous/periodic
-- mode) refills it.
--
-- The yield rides on the arrival-need-drop row by design: a well or a bush is
-- one shared stock (drinking/eating in place AND filling a pail both deplete
-- it). varchar(32) matches the attribute column width; the item is validated
-- against the live catalog at gather time, so no FK here.
--
-- Backfill: the well-water source, so v1's "fill a pail at the well" works at
-- boot. Wells are the thirst-refresh sources tagged 'well'; `water` is the
-- existing drink item_kind. NOTE: this depends on wells carrying the 'well'
-- tag in village_object.tags — if the tag differs the UPDATE is a harmless
-- no-op; confirm it hit rows post-deploy. Berry bushes (gather_item =
-- 'berries') are seeded separately once the bush objects + a `berries`
-- item_kind are confirmed in the world data.
--
-- Companion design: work/tasks/zbbs-work-328-gather-harvest/design

BEGIN;

ALTER TABLE object_refresh
    ADD COLUMN gather_item character varying(32);

UPDATE object_refresh r
   SET gather_item = 'water'
  FROM village_object o
 WHERE o.id = r.object_id
   AND r.attribute = 'thirst'
   AND 'well' = ANY (o.tags);

COMMIT;
