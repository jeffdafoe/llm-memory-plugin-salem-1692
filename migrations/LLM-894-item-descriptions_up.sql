-- LLM-894: descriptions for the everyday goods that have none.
--
-- WHY. A player can now tap a good in a shop or in Your Pack and read its
-- item_kind.description (pc/stall and the pack rows carry it). The clothing,
-- dyes, charms, writing goods and imports have one already; bread, ale,
-- tools and the rest did not, so their card would be empty.
--
-- Fills a description only where the good has none, so a description set
-- since by the operator (umbilical item set) is kept. Goods missing from the
-- catalog (a schema-only database) are skipped. Engine-minted discovery rows
-- (category unknown) are left out: no shop sells them.

BEGIN;

UPDATE item_kind ik
   SET description = v.description
  FROM (VALUES
    ('ale',         'A tankard of brown ale drawn from the cask.'),
    ('apples',      'Apples from the orchard, firm and tart.'),
    ('axe',         'A felling axe with an ash handle, for timber and firewood.'),
    ('blueberries', 'A handful of wild blueberries off the low bushes.'),
    ('bread',       'A round loaf of brown bread with a hard crust.'),
    ('carrots',     'Carrots pulled from the garden, the earth still on them.'),
    ('cheese',      'A wedge of hard yellow cheese, pressed and aged on the farm.'),
    ('coca_tea',    'Coca leaves from the south, steeped hot; a cup of it drives off weariness.'),
    ('firewood',    'Split logs of oak and maple, seasoned dry enough to burn well.'),
    ('flour',       'Wheat flour from the mill, ground coarse, in a cloth sack.'),
    ('fried_meat',  'Meat fried in its own fat, hot from the skillet.'),
    ('hammer',      'An iron-headed hammer on an ash handle.'),
    ('horseshoe',   'A horseshoe of wrought iron with its nail holes punched; many a household nails one over the door for luck.'),
    ('meat',        'A cut of fresh raw meat, to be cooked before it is eaten.'),
    ('milk',        'Fresh cow''s milk, best drunk before it turns.'),
    ('nail',        'Square iron nails from the smith''s anvil, for boards, shingles and mending.'),
    ('nights_stay', 'A night''s lodging: a bed in a room upstairs at the inn.'),
    ('porridge',    'A bowl of hot porridge, thick and plain.'),
    ('raspberries', 'Red raspberries, soft and sweet, picked from the canes.'),
    ('sage',        'Fresh-cut garden sage, for the pot or the cow byre.'),
    ('shovel',      'An iron-shod shovel for earth, snow and the barn floor.'),
    ('skillet',     'An iron skillet on three short legs, made to stand in the coals of the hearth.'),
    ('stew',        'A bowl of stew, meat and roots cooked long over the fire.'),
    ('water',       'Cool, clear water drawn from the well.'),
    ('wheat',       'Threshed wheat in the grain, ready for the mill.')
  ) AS v(name, description)
 WHERE ik.name = v.name
   AND (ik.description IS NULL OR btrim(ik.description) = '');

COMMIT;
