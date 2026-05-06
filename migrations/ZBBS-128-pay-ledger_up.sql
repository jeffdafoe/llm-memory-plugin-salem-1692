-- ZBBS-128: pay_ledger table — append-only record of every pay attempt.
--
-- Replaces the held-transaction approach (ZBBS-127, reverted in PR #103)
-- with a stateful ledger. Each pay() arrival inserts a `pending` row
-- BEFORE any LLM call or transfer. A subsequent state transition records
-- the resolution: accepted (transfer ran), declined (recipient refused),
-- countered (recipient asked for new amount), withdrawn (aged out / buyer
-- cancelled), failed (engine error during resolution).
--
-- Why a ledger:
--
--   1. No DB locks held across LLM calls. The deliberation tick runs
--      with no transaction open; the transfer that follows on `accepted`
--      is its own short tx.
--   2. Multi-turn haggle is natural — counter chains link via parent_id,
--      depth column denormalized for the fixed-cost cap check at
--      deliberation time.
--   3. Authoritative sales history per (buyer, seller, item) for the
--      dream pipeline, the salem-economy-smoothness investigation, and
--      future analytics. Today the only record of a pay is the
--      agent_action_log audit row plus chat history; this gives one
--      place to read "what got sold, when, for how much."
--
-- State machine (append-only, terminal states never mutate to other
-- terminals):
--
--      pending ──┬─→ accepted    (transfer ran)
--                ├─→ declined    (recipient said no)
--                ├─→ countered   (terminal-for-this-row; child row links
--                │                via parent_id when buyer pays the
--                │                counter amount)
--                ├─→ withdrawn   (aged out, currently the only cause —
--                │                buyer-cancel UI doesn't exist yet)
--                └─→ failed      (engine error during resolution)
--
-- Most fields are nullable to cover the full pay surface:
--   - item_kind / qty / consume_now: NULL for pure coin transfers (tips)
--   - quoted_unit_amount: NULL when no scene_quote on file at insert
--   - message: populated for declined / countered / failed; NULL for
--     accepted / pending / withdrawn (decline reason / counter speech /
--     failure cause respectively)
--   - counter_amount: populated only for countered
--   - parent_id: NULL on the root pay attempt; populated on a
--     buyer-retry that pays an earlier countered row's amount

BEGIN;

CREATE TABLE pay_ledger (
    id                  bigserial                PRIMARY KEY,
    huddle_id           uuid                     REFERENCES scene_huddle(id) ON DELETE SET NULL,
    scene_id            uuid,
    buyer_id            uuid                     NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    seller_id           uuid                     NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind           varchar(32)              REFERENCES item_kind(name) ON UPDATE CASCADE,
    qty                 integer                  CHECK (qty IS NULL OR qty > 0),
    offered_amount      integer                  NOT NULL CHECK (offered_amount >= 0),
    quoted_unit_amount  integer                  CHECK (quoted_unit_amount IS NULL OR quoted_unit_amount >= 0),
    consume_now         boolean                  NOT NULL DEFAULT false,
    state               varchar(16)              NOT NULL CHECK (state IN ('pending','accepted','declined','countered','withdrawn','failed')),
    message             text,
    counter_amount      integer                  CHECK (counter_amount IS NULL OR counter_amount >= 0),
    parent_id           bigint                   REFERENCES pay_ledger(id),
    depth               integer                  NOT NULL DEFAULT 0 CHECK (depth >= 0),
    created_at          timestamp with time zone NOT NULL DEFAULT NOW(),
    resolved_at         timestamp with time zone,
    -- Resolved rows must carry resolved_at; pending rows must not.
    -- Catches a state-transition write that forgets to stamp the
    -- timestamp.
    CHECK ((state = 'pending') = (resolved_at IS NULL))
);

-- Scene-grouped reads (chat correlation, debugging "what happened in
-- this scene"). The created_at second key keeps rows in arrival order.
CREATE INDEX ix_pay_ledger_scene_at ON pay_ledger (scene_id, created_at);

-- Sales-history reads. (buyer, seller, item, time DESC) covers
-- "Jefferey's last purchase from John of stew" without sorting at
-- query time. Item NULL is included so coin-only history (tips) lands
-- on the same index.
CREATE INDEX ix_pay_ledger_buyer_seller ON pay_ledger (buyer_id, seller_id, item_kind, created_at DESC);

-- Aging sweep target. Partial so the index stays small once most rows
-- are terminal.
CREATE INDEX ix_pay_ledger_pending ON pay_ledger (state, created_at) WHERE state = 'pending';

COMMIT;
