-- ZBBS-098: tavernkeeper attribute + serve tool wiring.
--
-- Adds the tavernkeeper attribute_definition row that exposes the new
-- serve tool (engine handler in serve.go) and carries the tool-discipline
-- instructions covering when to use consume / serve vs the act narration
-- catch-all. Then assigns it to John Ellis so the next time he serves
-- stew or eats bread, the LLM has the right tools and prompt copy in
-- front of it.
--
-- Test scenario: John Ellis, NPC tavernkeeper at the Tavern, serves stew
-- to PCs Jefferey and Wendy. Pre-ZBBS-098 he narrates "served stew to
-- Jefferey and Wendy" via act, no inventory moves. Post-ZBBS-098 the
-- model should call serve(recipients=["Jefferey","Wendy"], item="stew",
-- qty=1) — stock decrements by 2, both PCs' hunger drops, audit row
-- captures the action.
--
-- Bread case (John eating his own bread): the universal consume tool
-- already covers this; tavernkeeper instructions reinforce the rule
-- ("ALWAYS call consume — never narrate eating via act") so the prompt
-- copy explicitly forbids the act narration the model previously fell
-- back to.

BEGIN;

INSERT INTO attribute_definition (slug, display_name, description, tools, instructions) VALUES (
    'tavernkeeper',
    'Tavernkeeper',
    'Owner-operator of a tavern: cooks, brews, serves, and often lives above the bar. Holds kitchen and bar inventory. Stocks staples (bread, ale, stew). Customers buy or are served on tab. Default tavernkeeper hours match worker shift hours unless overridden on the actor.',
    '["serve"]'::jsonb,
    E'You are the tavernkeeper. Your inventory is your business — every loaf, mug, and bowl of stock exists for service or sale.\n\nTOOL DISCIPLINE:\n- When you eat or drink your own stock, ALWAYS call consume. Never narrate eating or drinking via act — act does not decrement inventory.\n- When you serve food or drink to customers, ALWAYS call serve(recipients=[...], item=..., qty=...). Never narrate serving via act — act does not move stock or affect anyone''s hunger. Serve handles both: your stock decrements and the recipients eat or drink immediately (consume_now=true, the default).\n- Payment is a separate beat. Customers pay you via their own pay tool when they choose to settle up. You can serve before, during, or after payment — whatever the conversation calls for. A customer who hasn''t paid yet still gets served if you choose to extend a tab.'
);

-- Assign tavernkeeper to John Ellis. ON CONFLICT defends against partial
-- reapply if this migration is run multiple times against the same data.
INSERT INTO actor_attribute (actor_id, slug)
SELECT id, 'tavernkeeper' FROM actor WHERE display_name = 'John Ellis'
ON CONFLICT (actor_id, slug) DO NOTHING;

COMMIT;
