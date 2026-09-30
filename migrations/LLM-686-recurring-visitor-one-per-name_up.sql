-- LLM-686: one returner row per visitor name.
--
-- A visitor name is now one fixed man (engine visitor_persona.go): the name
-- carries his trade and his hometown, and it is the returner identity. Before
-- this, promotion (a visitor sharing a scene with a player) minted a new
-- recurring_visitor row per visit, so one name could hold several rows with
-- different trades — live on 2026-09-30: Elias Drum x3, Caleb Wendell x2,
-- Tobias Hewes x2, Brother Ashford x2, Ephraim Pollard x2.
--
-- WHAT this changes:
--   1. Per name, keep one row: the most visits, then the earliest first_seen_at,
--      then the lowest id. It absorbs the others: visit_count summed,
--      first_seen_at earliest, last_seen_at latest, and next_return_at from the
--      row with the latest departure (an older row's date is already stale).
--   2. Acquaintances (per player) of the dropped rows move to the kept row. When
--      the kept row already knows that player, the two merge: first_met earliest,
--      last_met latest, the kept summary unless empty (then the latest non-empty
--      one), salient facts appended in time order, last_consolidated latest.
--   3. An in-flight visitor linked to a dropped row is re-linked to the kept row.
--   4. The dropped rows are deleted, and a unique index on name makes one row per
--      name a database fact.
--
-- Archetype / origin are NOT touched here: the fixed persona table lives in Go,
-- and the engine aligns every row to it at boot (rehydrateRecurringVisitorsOnLoad);
-- the next checkpoint persists that.
--
-- recurring_visitor is engine-owned: deploy.sh runs stop -> migrate -> start, so
-- no checkpoint races this. Rerun-safe: a second run finds nothing to merge and
-- the index already exists. Validation at the end fails loudly.

BEGIN;

-- Distinct (name, player) bonds before the merge — every one must survive.
CREATE TEMP TABLE llm686_bonds_before ON COMMIT DROP AS
SELECT DISTINCT r.name, a.pc_actor_id
  FROM recurring_visitor_acquaintance a
  JOIN recurring_visitor r ON r.id = a.recurring_visitor_id;

CREATE TEMP TABLE llm686_keep ON COMMIT DROP AS
SELECT DISTINCT ON (name) name, id AS keep_id
  FROM recurring_visitor
 ORDER BY name, visit_count DESC, first_seen_at ASC, id ASC;

CREATE TEMP TABLE llm686_merge ON COMMIT DROP AS
SELECT r.id AS drop_id, k.keep_id
  FROM recurring_visitor r
  JOIN llm686_keep k USING (name)
 WHERE r.id <> k.keep_id;

-- 1. Fold the dropped rows' visit history into the kept row.
UPDATE recurring_visitor k
   SET visit_count    = agg.visits,
       first_seen_at  = agg.first_seen,
       last_seen_at   = agg.last_seen,
       next_return_at = agg.next_return
  FROM (SELECT kk.keep_id,
               sum(r.visit_count)  AS visits,
               min(r.first_seen_at) AS first_seen,
               max(r.last_seen_at)  AS last_seen,
               (array_agg(r.next_return_at ORDER BY r.last_seen_at DESC))[1] AS next_return
          FROM recurring_visitor r
          JOIN llm686_keep kk USING (name)
         GROUP BY kk.keep_id
        HAVING count(*) > 1) agg
 WHERE k.id = agg.keep_id;

-- 2. Pool the dropped rows' acquaintances per (kept row, player).
CREATE TEMP TABLE llm686_pooled ON COMMIT DROP AS
SELECT m.keep_id,
       a.pc_actor_id,
       (array_agg(a.pc_display_name ORDER BY a.last_met_at DESC))[1]                    AS pc_name,
       min(a.first_met_at)                                                             AS first_met,
       max(a.last_met_at)                                                              AS last_met,
       (array_agg(a.summary_text ORDER BY a.last_met_at DESC)
            FILTER (WHERE a.summary_text <> ''))[1]                                    AS summary,
       max(a.last_consolidated_at)                                                     AS last_consolidated,
       COALESCE((SELECT jsonb_agg(f.elem ORDER BY f.elem->>'at')
                   FROM recurring_visitor_acquaintance a2
                   JOIN llm686_merge m2 ON m2.drop_id = a2.recurring_visitor_id
                  CROSS JOIN LATERAL jsonb_array_elements(a2.salient_facts) AS f(elem)
                  WHERE m2.keep_id = m.keep_id AND a2.pc_actor_id = a.pc_actor_id),
                '[]'::jsonb)                                                           AS facts
  FROM recurring_visitor_acquaintance a
  JOIN llm686_merge m ON m.drop_id = a.recurring_visitor_id
 GROUP BY m.keep_id, a.pc_actor_id;

-- 2a. The kept row already knows this player: merge into its row.
UPDATE recurring_visitor_acquaintance ka
   SET first_met_at         = LEAST(ka.first_met_at, p.first_met),
       last_met_at          = GREATEST(ka.last_met_at, p.last_met),
       pc_display_name      = CASE WHEN p.last_met > ka.last_met_at THEN p.pc_name ELSE ka.pc_display_name END,
       summary_text         = CASE WHEN ka.summary_text <> '' THEN ka.summary_text ELSE COALESCE(p.summary, '') END,
       salient_facts        = (SELECT COALESCE(jsonb_agg(e ORDER BY e->>'at'), '[]'::jsonb)
                                 FROM jsonb_array_elements(ka.salient_facts || p.facts) AS e),
       last_consolidated_at = GREATEST(ka.last_consolidated_at, p.last_consolidated)
  FROM llm686_pooled p
 WHERE ka.recurring_visitor_id = p.keep_id
   AND ka.pc_actor_id = p.pc_actor_id;

-- 2b. A player only the dropped rows knew: give the bond to the kept row.
INSERT INTO recurring_visitor_acquaintance (
    recurring_visitor_id, pc_actor_id, pc_display_name, first_met_at, last_met_at,
    salient_facts, summary_text, last_consolidated_at
)
SELECT p.keep_id, p.pc_actor_id, p.pc_name, p.first_met, p.last_met,
       p.facts, COALESCE(p.summary, ''), p.last_consolidated
  FROM llm686_pooled p
 WHERE NOT EXISTS (SELECT 1 FROM recurring_visitor_acquaintance ka
                    WHERE ka.recurring_visitor_id = p.keep_id
                      AND ka.pc_actor_id = p.pc_actor_id);

-- 3. Re-link any in-flight visitor.
UPDATE visitor v
   SET recurring_visitor_id = m.keep_id
  FROM llm686_merge m
 WHERE v.recurring_visitor_id = m.drop_id;

-- 4. Drop the merged rows (their acquaintances cascade) and enforce one per name.
DELETE FROM recurring_visitor r
 USING llm686_merge m
 WHERE r.id = m.drop_id;

CREATE UNIQUE INDEX IF NOT EXISTS recurring_visitor_name_key
    ON public.recurring_visitor (name);

DO $$
DECLARE lost int; dangling int; dup_names int; enforced int;
BEGIN
    -- CREATE ... IF NOT EXISTS skips on ANY same-named relation, so prove the
    -- index is what we need: unique, valid, on public.recurring_visitor, over
    -- exactly (name), no predicate.
    SELECT count(*) INTO enforced
      FROM pg_index i
      JOIN pg_class ic ON ic.oid = i.indexrelid
      JOIN pg_namespace n ON n.oid = ic.relnamespace
     WHERE n.nspname = 'public'
       AND ic.relname = 'recurring_visitor_name_key'
       AND i.indrelid = 'public.recurring_visitor'::regclass
       AND i.indisunique
       AND i.indisvalid
       AND i.indpred IS NULL
       AND i.indnkeyatts = 1
       AND i.indkey[0] = (SELECT attnum FROM pg_attribute
                           WHERE attrelid = 'public.recurring_visitor'::regclass
                             AND attname = 'name');
    IF enforced <> 1 THEN
        RAISE EXCEPTION 'LLM-686: public.recurring_visitor_name_key is not a unique index on recurring_visitor(name) — drop the conflicting object and re-run';
    END IF;
    SELECT count(*) INTO dup_names
      FROM (SELECT name FROM recurring_visitor GROUP BY name HAVING count(*) > 1) d;
    IF dup_names > 0 THEN
        RAISE EXCEPTION 'LLM-686: % name(s) still hold more than one returner row', dup_names;
    END IF;
    SELECT count(*) INTO lost
      FROM llm686_bonds_before b
     WHERE NOT EXISTS (SELECT 1
                         FROM recurring_visitor_acquaintance a
                         JOIN recurring_visitor r ON r.id = a.recurring_visitor_id
                        WHERE r.name = b.name AND a.pc_actor_id = b.pc_actor_id);
    IF lost > 0 THEN
        RAISE EXCEPTION 'LLM-686: % (name, player) bond(s) lost in the merge', lost;
    END IF;
    SELECT count(*) INTO dangling
      FROM visitor v
     WHERE v.recurring_visitor_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM recurring_visitor r WHERE r.id = v.recurring_visitor_id);
    IF dangling > 0 THEN
        RAISE EXCEPTION 'LLM-686: % in-flight visitor(s) link to a deleted returner row', dangling;
    END IF;
END $$;

COMMIT;
