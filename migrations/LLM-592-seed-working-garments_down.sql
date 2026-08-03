-- LLM-592 down: remove the seeded working garments.
--
-- Matches on the exact (actor, kind, worn_minutes_left) TRIPLE the _up inserted,
-- not on (actor, kind). That is the whole point of the shape:
--
--   * a garment the engine has worn since the seed has a different remaining
--     count, so it is LEFT ALONE — reverting a seed must not destroy live world
--     state that has moved on;
--   * the two rows the _up deliberately did not touch (Joseph Scott's breeches at
--     302, Moses James's at 14399, both pre-existing) carry their own values and
--     so are never matched here either.
--
-- The cost of that precision is that an untouched seeded garment is removed while
-- a worn one survives, which is the correct trade: the down exists to undo what
-- this migration DID, not to strip the village.
--
-- actor_inventory is checkpoint-written; apply with the engine stopped (the
-- deploy's down -> migrate -> up already does), or a running world will simply
-- write the rows back from memory.

BEGIN;

DELETE FROM actor_inventory i
 USING (VALUES
    ('019da6f9-1b4c-7dda-bb6b-3248cdafb2c4', 'shift',     9720),  -- Ezekiel Crane
    ('019da6f9-1b4c-7dda-bb6b-3248cdafb2c4', 'breeches', 12960),
    ('70419d0c-3668-428c-8bd8-633993c3aa60', 'shift',     9180),  -- Hannah Boggs
    ('70419d0c-3668-428c-8bd8-633993c3aa60', 'gown',     12240),
    ('019dbcec-1149-7149-8a49-2cdb54680b86', 'shift',     8640),  -- Prudence Ward
    ('019dbcec-1149-7149-8a49-2cdb54680b86', 'gown',     11520),
    ('019da6b2-7074-7b19-ab19-89b6fc3a29a1', 'shift',     8100),  -- John Ellis
    ('019da6b2-7074-7b19-ab19-89b6fc3a29a1', 'breeches', 10800),
    ('019da6af-c8c9-7eb8-aead-759142785789', 'shift',     7560),  -- Elizabeth Ellis
    ('019da6af-c8c9-7eb8-aead-759142785789', 'gown',     10080),
    ('019da6ae-3376-73fc-8872-1cbb3ada1c78', 'shift',     7020),  -- Moses James
    ('019da6ae-3376-73fc-8872-1cbb3ada1c78', 'breeches',  9360),
    ('019dcaf9-1d10-73b8-a4a5-1debc3f2992e', 'shift',     6480),  -- Nathaniel Cole
    ('019dcaf9-1d10-73b8-a4a5-1debc3f2992e', 'breeches',  8640),
    ('019da6be-d36d-789e-9bf1-580f9982ecb9', 'shift',     5940),  -- Constance Scott
    ('019da6be-d36d-789e-9bf1-580f9982ecb9', 'gown',      7920),
    ('019da6d3-5038-79cc-a09a-1a3356bda342', 'shift',     5400),  -- Patience Walker
    ('019da6d3-5038-79cc-a09a-1a3356bda342', 'gown',      7200),
    ('019da6d0-ef1b-7e27-9163-37a3f2ce5bb0', 'shift',     4860),  -- Silence Walker
    ('019da6d0-ef1b-7e27-9163-37a3f2ce5bb0', 'gown',      6480),
    ('019da6b5-3143-71e0-9f47-6bf3af456524', 'shift',     4320),  -- Abraham Warren
    ('019da6b5-3143-71e0-9f47-6bf3af456524', 'breeches',  5760),
    ('019da6d4-24d2-7461-88b0-72b2b288bd5c', 'shift',     3780),  -- Lewis Walker
    ('019da6d4-24d2-7461-88b0-72b2b288bd5c', 'breeches',  5040),
    ('019da6d7-98fc-738d-859e-5614bae1b2d0', 'shift',     1512),  -- Anne Walker
    ('019da6d7-98fc-738d-859e-5614bae1b2d0', 'gown',      2016),
    ('4561da54-eb08-46c8-8f05-ddc0aadaebff', 'shift',     1296),  -- Constable Gideon Marsh
    ('4561da54-eb08-46c8-8f05-ddc0aadaebff', 'breeches',  1728),
    ('019da6b7-a853-79fb-91eb-645e5d9915c1', 'shift',     1080),  -- Joseph Scott
    ('019da6b7-a853-79fb-91eb-645e5d9915c1', 'breeches',  1440)
  ) AS v(actor_id, item_kind, worn_minutes_left)
 WHERE i.actor_id = v.actor_id::uuid
   AND i.item_kind = v.item_kind
   AND i.worn_minutes_left = v.worn_minutes_left
   AND i.quantity = 1;

COMMIT;
