-- ZBBS-047: Link the Prudence Ward NPC to the existing village_agent row
-- (llm_memory_agent = 'zbbs-prudence-ward', role 'herbalist'). Now the NPC's
-- movement can eventually be driven by LLM agent decisions, and the agent's
-- in-world presence is visible as the sprite at home.

UPDATE npc
SET llm_memory_agent = 'zbbs-prudence-ward'
WHERE display_name = 'Prudence Ward';
