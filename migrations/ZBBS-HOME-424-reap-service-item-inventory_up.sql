-- ZBBS-HOME-424: reap vestigial service-item inventory rows.
--
-- Innkeepers carry a literal nights_stay item in actor_inventory (John
-- Ellis x1, Hannah Boggs x1). nights_stay is a "service"-capability kind:
-- a sale grants room_access at the seller's structure and the stock gates
-- skip inventory entirely, so these rows are never read by commerce. What
-- they DO feed is the prompt's carrying line ("You are carrying: Night's
-- Stay (x1)") and the barter path — Hannah offered "1 nights_stay for
-- 1 water" as payment, which the engine accepted (conversation
-- hud-6c849d…). The engine now rejects service kinds in pay_items; this
-- reaps the vestigial rows so they stop rendering as carryable goods.
--
-- NOTE deliberately NOT touched here: Ezekiel Crane's NULL
-- home_structure_id. That is by design — outside of innkeepers no actor
-- sleeps at their workplace (his stall is not sleepable), and he is the
-- standing test actor for the NPC-to-NPC innkeeper flow: structurally a
-- lodging seeker, renting a room each night is his intended behavior.

BEGIN;

DELETE FROM public.actor_inventory
 WHERE item_kind IN (SELECT name FROM public.item_kind
                      WHERE 'service' = ANY(capabilities));

COMMIT;
