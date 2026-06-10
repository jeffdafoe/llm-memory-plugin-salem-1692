-- ZBBS-HOME-424: world-data fixes from the hud-6c849d… inn-conversation dig.
--
-- 1) Ezekiel Crane (the village blacksmith) has no home_structure_id — the
--    only working resident without one. The lodging-seeker predicate
--    (actorSnapIsLodgingSeeker: no home AND no active room grant) therefore
--    flags him permanently, so every innkeeper's "A room to let" cue targets
--    him nightly and he drains his purse renting rooms (observed live:
--    100 → 20 coins in one evening, conversation hud-6c849d…). Working
--    residents bed at their workplace in this world's seed data (John Ellis
--    and Hannah Boggs both have home = work), so the same pattern is applied
--    to any NPC left working-but-homeless by seeding. PCs (login_username)
--    are excluded — a homeless PC is a real lodging customer.
--
-- 2) Innkeepers carry a literal nights_stay item in actor_inventory (John
--    Ellis x1, Hannah Boggs x1). nights_stay is a "service"-capability kind:
--    a sale grants room_access at the seller's structure and the stock gates
--    skip inventory entirely, so these rows are never read by commerce. What
--    they DO feed is the prompt's carrying line ("You are carrying: Night's
--    Stay (x1)") and the barter path — Hannah offered "1 nights_stay for
--    1 water" as payment, which the engine accepted. The engine now rejects
--    service kinds in pay_items; this reaps the vestigial rows so they stop
--    rendering as carryable goods.

BEGIN;

UPDATE public.actor
   SET home_structure_id = work_structure_id
 WHERE home_structure_id IS NULL
   AND work_structure_id IS NOT NULL
   AND login_username IS NULL;

DELETE FROM public.actor_inventory
 WHERE item_kind IN (SELECT name FROM public.item_kind
                      WHERE 'service' = ANY(capabilities));

COMMIT;
