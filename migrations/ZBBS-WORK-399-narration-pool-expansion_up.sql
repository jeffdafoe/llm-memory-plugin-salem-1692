-- ZBBS-WORK-399: narration_pool_expansion — LLM-expanded narration pool lines.
--
-- The engine's deterministic narration moments (businessowner hospitality
-- greet/handover/farewell, lodging checkout / morning descent, the NPC
-- retire farewell) draw from small compile-time phrase pools
-- (engine/sim/businessowner.go, lodging_narration.go, npc_sleep.go).
-- When a pool has been drawn enough times to cycle (engine/sim/
-- narration_pool.go), the narration-expansion cascade asks salem-generic
-- for a handful of new lines in the same voice and persists the accepted
-- ones here. The engine merges these rows into the in-memory pools at
-- boot (main.go: NarrationExpansionRepo.LoadAll →
-- World.MergeNarrationExpansions) and appends at expansion time
-- (write-through, never part of the checkpoint).
--
-- pool_key is the engine-side registry key ("businessowner_flamboyant_greet",
-- "lodging_checkout", "npc_retire", ...). The PK doubles as the dedupe
-- guard: the cascade INSERTs ON CONFLICT DO NOTHING, so a re-emitted
-- phrase is idempotent. generated_by records the VA slug the engine
-- asked (the provider model behind it is memory-api's concern).
--
-- No FK anywhere — pool keys are engine constants, not rows. Rows for a
-- pool retired from the code are harmless orphans (logged and skipped at
-- merge time).

BEGIN;

CREATE TABLE public.narration_pool_expansion (
    pool_key     character varying(64) NOT NULL,
    phrase       text NOT NULL,
    generated_by character varying(64) NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pool_key, phrase)
);

COMMIT;
