-- LLM-535: purge the drifted businessowner_reserved_farewell expansions.
--
-- The narration registry merges each pool's compile-time seed lines with
-- LLM-generated expansions persisted here (see sim/narration_pool.go). The
-- reserved-farewell pool was expanding under a description that told the model
-- to see a customer out "in as few words as possible", and against two seed
-- lines ("Go on, then.", "{customer}. Mm.") that were themselves closer to a
-- brush-off than a leave-taking. The expansions followed: "Off with you, now."
-- and "That will do." were observed live, spoken by keepers to the constable on
-- his rounds.
--
-- The seed lines and the description are fixed in the same change. This deletes
-- the lines already generated under the old wording, so the pool re-expands from
-- the corrected seeds instead of compounding the drift — the generated lines are
-- the model's few-shot examples for the NEXT expansion, so leaving them in place
-- would keep teaching the wrong register no matter what the description says.
--
-- Only this pool key is touched. The other eight pools are unaffected.
--
-- Takes effect on the next engine start: the registry is built from the seeds at
-- boot and these rows are re-merged by MergeNarrationExpansions, so an engine
-- already running keeps the old pool in memory until it restarts.

BEGIN;

DELETE FROM narration_pool_expansion
WHERE pool_key = 'businessowner_reserved_farewell';

COMMIT;
