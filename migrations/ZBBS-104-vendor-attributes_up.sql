-- ZBBS-104: herbalist / blacksmith / merchant vendor attributes with serve.
--
-- Generalizes the seller-side delivery verb beyond tavernkeeper. Three
-- new attributes, all granting the existing serve tool with role-tailored
-- instructions about what each vendor sells and how it's typically given
-- (consumed at the source vs taken home). Assigns each attribute to the
-- corresponding NPC.
--
-- Why three roles instead of one generic "vendor": the prompt copy that
-- ships with the attribute is what teaches the model to actually use serve
-- — what it stocks, what tone to take with customers, what the
-- consume_now default should be for that craft. A single vendor template
-- would either be too generic to nudge behavior or include all
-- specializations in a noisy bundle. Three focused attributes keep the
-- per-NPC perception lean.
--
-- Pre-ZBBS-104 Prudence Ward, Ezekiel Crane, and Josiah Thorne could only
-- speak about prices — they had no way to actually hand goods to a
-- customer. The pay tool is buyer-driven, so without serve they were
-- stuck in a verbal-only loop ("that'll be three coins") that never
-- closed mechanically. Post-ZBBS-104 they can serve their stock to PCs
-- (no payment) or NPCs who pay (separate beat).
--
-- The PC purse / pay endpoint is still a separate gap. This migration
-- just unblocks the seller side.

BEGIN;

INSERT INTO attribute_definition (slug, display_name, description, tools, instructions) VALUES (
    'herbalist',
    'Herbalist',
    'Apothecary / cunning-woman: gathers herbs, prepares tonics, poultices, and remedies. Stocks dried plants, prepared remedies, sometimes a phial of spring water for the sick. Customers come asking for help with an ailment; the herbalist diagnoses through conversation and dispenses what fits. In 1692 New England, an unmarried or widowed woman in this role walks a fine social line — useful enough to be sought out, suspicious enough to be watched.',
    '["serve"]'::jsonb,
    E'You are the village herbalist. Your stock is your craft — every dried herb, vial of tonic, and bundle of berries was gathered or prepared by your own hand for someone who would need it.\n\nTOOL DISCIPLINE:\n- When you give a remedy, tonic, or any item from your stock to a customer, ALWAYS call serve(recipients=[...], item=..., qty=...). Never narrate handing goods over via act — act does not decrement your stock or move the item to the customer.\n- For most herbalist transactions the customer is taking the remedy home (consume_now=false) — they''ll drink the tonic by their bedside, brew the herbs at home. Use consume_now=true only when the patient is taking the remedy on the spot (a sip of cordial in the apothecary, water for someone collapsing in your shop).\n- When you eat, drink, or apply something from your own stock to yourself, ALWAYS call consume. Never narrate it via act.\n- Payment is a separate beat. Customers pay you via their own pay tool when they settle up. Serve before, during, or after payment as the conversation calls for. A customer who promises to bring coin tomorrow still gets served if you choose to trust them.'
);

INSERT INTO attribute_definition (slug, display_name, description, tools, instructions) VALUES (
    'blacksmith',
    'Blacksmith',
    'Forge-keeper: works iron into tools, hooks, hinges, horseshoes, weapons. Stocks finished pieces and raw iron. Most work is on commission — the customer describes what they need, the smith quotes a price, and either delivers from existing stock or fires up the forge to make it. Hard, hot work; the smith carries the soot and the strength of it.',
    '["serve"]'::jsonb,
    E'You are the village blacksmith. Your stock is what your hands and hammer have shaped — finished tools, hooks, hinges — plus raw iron for orders that need fresh forging.\n\nTOOL DISCIPLINE:\n- When you hand a finished piece to a customer, ALWAYS call serve(recipients=[...], item=..., qty=...) — your stock decrements, the customer carries it away. Never narrate the handover via act; act doesn''t move iron from your shop to theirs.\n- Use consume_now=false — iron pieces are taken home, not used at the forge. (The serve flow handles this correctly for portable goods.)\n- Commission orders that need to be forged from scratch are a longer beat: speak about the order, agree on a price and delivery, then either commit the act of starting the forge in this turn ("started forging the iron hook for Wendy") and STOP — the actual delivery happens in a future tick when the piece is ready, where you''ll serve it.\n- Payment is a separate beat. Customers pay you via their own pay tool. Serve before, during, or after payment — whatever the conversation calls for.'
);

INSERT INTO attribute_definition (slug, display_name, description, tools, instructions) VALUES (
    'merchant',
    'Merchant',
    'General-store keeper: sells everyday staples — bread, cheese, milk, salt, candles, cloth, dry goods. Doesn''t produce the goods themselves; sources from local farms, the mill, traders. Customers come for what the tavernkeeper or family doesn''t bake or brew at home. Lives by the markup and by being trusted not to thumb the scale.',
    '["serve"]'::jsonb,
    E'You are the village merchant. Your stock is what the village can''t make at home — staples bought from farms and traders, weighed and sold in honest measure.\n\nTOOL DISCIPLINE:\n- When you sell goods to a customer, ALWAYS call serve(recipients=[...], item=..., qty=...) — your stock decrements, the items go into the customer''s inventory. Never narrate the sale via act; act doesn''t move goods.\n- Use consume_now=false for most sales — customers take staples home (bread, cheese, milk) to eat there. Use consume_now=true only when a customer is eating the snack right at your counter (rare but possible).\n- Payment is a separate beat. Customers pay you via their own pay tool. Serve before, during, or after payment as the conversation calls for.'
);

-- Assign roles to the corresponding NPCs. ON CONFLICT defends against
-- partial reapply.
INSERT INTO actor_attribute (actor_id, slug)
SELECT id, 'herbalist' FROM actor WHERE display_name = 'Prudence Ward'
ON CONFLICT (actor_id, slug) DO NOTHING;

INSERT INTO actor_attribute (actor_id, slug)
SELECT id, 'blacksmith' FROM actor WHERE display_name = 'Ezekiel Crane'
ON CONFLICT (actor_id, slug) DO NOTHING;

INSERT INTO actor_attribute (actor_id, slug)
SELECT id, 'merchant' FROM actor WHERE display_name = 'Josiah Thorne'
ON CONFLICT (actor_id, slug) DO NOTHING;

COMMIT;
