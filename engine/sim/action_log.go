package sim

import "time"

// action_log.go — engine-internal append-only audit trail. World-scoped
// in-memory slice of recent agent + engine-source actions, consumed by
// the atmosphere refresh cascade (group-by-actor-by-action since last
// fire) and per-actor narrative consolidation (own + peer rows within
// a recent window). Mirrors v1's agent_action_log pg table at a
// v2-scale-appropriate shape: in-memory only, capped retention,
// happy-path-only.
//
// Storage shape: flat []ActionLogEntry on World. No secondary indices
// — consumers walk the slice with a time-cutoff filter, which at
// Hannah scale (<10 NPCs, low TPS, 48h retention) is microseconds. If
// atmosphere or C2 reads ever measure meaningfully, retrofit
// per-actor / per-huddle indices on the same slice.
//
// Result field deliberately absent — failed/rejected actions don't
// land in the log. v1 logged failures for admin investigation reads
// (which happen against the durable pg projection at cutover, not
// against this in-memory cache). Every entry here is OK by
// construction. Deliberation outcomes (declined / countered pay) are
// their own ActionType values when those handlers port.
//
// No Source field — v2's magistrate role is gone; agent-vs-player-vs-
// engine inferable from ActorID kind via World.Actors lookup.
//
// No SpeakerName field — derive from
// Snapshot.Actors[ActorID].DisplayName at render time. v1
// denormalized to avoid a SQL JOIN; v2's snapshot reader has the data
// in hand.
//
// Durability: in-memory only at MVP. The ActionLogSink interface in
// repo.go stays a noop (mem.noopActionLog) — a future cutover will
// wire a pg projection if external admin reads need historical rows.
// Restart-loss is acceptable: atmosphere's last-fire stamp resets on
// restart and consolidation re-snapshots from current state.

// ActionType is the typed enum for entries appended to the action log.
// Open string set — new values land as commit-bearing handlers port.
// Matches the InteractionKind / WarrantKind posture (typed string,
// not free TEXT like v1's column).
type ActionType string

const (
	// ActionTypeSpoke — committed speak tool call. ActorID is the
	// speaker; Text is the utterance (rune-truncated to
	// MaxActionLogTextLen); HuddleID is the speaker's huddle at emit
	// time.
	ActionTypeSpoke ActionType = "spoke"

	// ActionTypePaid — committed pay tool call. ActorID is the
	// buyer; Text is the ForText (may be empty); HuddleID is the
	// buyer's huddle at append time (the same-huddle gate guarantees
	// the seller shares it).
	ActionTypePaid ActionType = "paid"

	// ActionTypeConsumed — committed consume tool call. ActorID is
	// the actor that ate; Text is the item kind (with qty prefix
	// when qty > 1); HuddleID is the actor's huddle if any.
	ActionTypeConsumed ActionType = "consumed"

	// ActionTypeDelivered — committed deliver_order tool call.
	// ActorID is the seller (the deliver action is theirs); Text is
	// the item kind (with qty prefix when qty > 1); HuddleID is the
	// seller's huddle at append time.
	ActionTypeDelivered ActionType = "delivered"

	// ActionTypeWalked — arrival at a movement destination. ActorID is
	// the mover; Text is the DESTINATION's DisplayName — the structure or
	// village object the mover walked TO (names a visited shop even when the
	// actor stopped at a loiter slot outside it, and an ObjectVisit well/
	// tree/pile). Empty only for a bare outdoor Position arrival with no
	// nameable place. HuddleID is empty (arrival precedes any encounter-
	// cascade huddle join that may follow).
	ActionTypeWalked ActionType = "walked"

	// ActionTypeTookBreak — committed take_break tool call
	// (ZBBS-HOME-284 #4). ActorID is the actor that stepped away; Text
	// is the model-supplied reason; HuddleID is the actor's huddle at
	// append time (usually empty — a break closes the post).
	ActionTypeTookBreak ActionType = "took_break"

	// ActionTypeSummoned — a summon messenger delivered a summons to the
	// target (ZBBS-HOME-311). ActorID is the TARGET (the summons is the
	// event that happened to them, not an action they took); Text is the
	// engine-authored delivery line; HuddleID is the target's huddle at
	// delivery time. Engine-sourced, not a tool call — the messenger is a
	// non-VA NPC.
	ActionTypeSummoned ActionType = "summoned"
)

// ActionLogEntry is one row in the in-memory action log. Carries the
// minimum the in-engine consumers (atmosphere digest + C2
// consolidation) need; see the package doc for what's dropped vs v1's
// pg schema and why.
type ActionLogEntry struct {
	ActorID    ActorID
	OccurredAt time.Time
	ActionType ActionType
	Text       string   // freeform, rune-bounded at write time
	HuddleID   HuddleID // "" for outdoor / pre-huddle / non-huddle actions
}

// MaxActionLogTextLen bounds the Text field at write time. Same value
// as MaxSalientFactTextLen — both feed the LLM and share a
// per-token-budget concern.
const MaxActionLogTextLen = MaxSalientFactTextLen

// DefaultActionLogRetention is the fallback for
// WorldSettings.ActionLogRetention when unset. 48h covers atmosphere's
// 4h refresh interval with comfortable headroom and consolidation's
// expected 24h window cleanly. Tunable via settings for dev / staging
// to drop closer to the sweep cadence.
const DefaultActionLogRetention = 48 * time.Hour

// CloneActionLogEntry is a value-copy. The struct has no nested
// pointers or maps today, so a plain dereference suffices. Kept as a
// named helper so the republish path uses the same idiom as the other
// clone helpers — if a field grows that requires deep-copy (a slice
// or map payload), this is the single chokepoint to update.
func CloneActionLogEntry(e ActionLogEntry) ActionLogEntry {
	return e
}

// CloneActionLog returns a value-copy of the slice. Used by republish
// to produce Snapshot.ActionLog without exposing world-goroutine-owned
// storage to readers. Returns nil for an empty input so the snapshot's
// field semantics match an unset slice exactly.
func CloneActionLog(in []ActionLogEntry) []ActionLogEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]ActionLogEntry, len(in))
	copy(out, in)
	return out
}
