package perception

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// BaselineStatus reports whether perception could establish a diff baseline
// for the subject actor against the primary scene's origin snapshot.
//
// The contract — see Build — is "unknown, never no-change": any Missing*
// status means perception MUST NOT claim "nothing changed since the scene
// started" and loop detection is inconclusive (not negative). A stuck-loop
// signal requires BaselinePresent; absence of evidence is not evidence of
// a loop.
type BaselineStatus int

const (
	// BaselineMissingNoScene — no scene resolved at all. Neither the
	// consumed warrants nor the actor's active huddle pointed at a scene
	// present in the snapshot. There is nothing to diff against.
	//
	// This is deliberately the zero value: a zero-value Payload is then
	// honestly degraded (no baseline) rather than falsely "present", so
	// Render(Payload{}) and any other unset path stays on the safe side of
	// the "unknown, never no-change" contract.
	BaselineMissingNoScene BaselineStatus = iota

	// BaselinePresent — the primary scene resolved and captured an origin
	// snapshot for the subject actor. SceneView.Diff is populated.
	BaselinePresent

	// BaselineMissingNoOriginSnapshot — a scene resolved but it captured no
	// participant baseline at all (ParticipantStateAtOrigin nil/empty —
	// e.g. an unbounded atmosphere-refresh scene). No actor has a baseline
	// here, so the absence carries no "joined after" signal.
	BaselineMissingNoOriginSnapshot

	// BaselineMissingJoinedAfterOrigin — a scene resolved and captured a
	// baseline for *other* participants, but not for the subject actor —
	// so the actor joined after the scene was minted. The diff baseline
	// would be meaningless; continuity claims about "since the scene
	// started" are weakened or omitted.
	BaselineMissingJoinedAfterOrigin
)

// String renders the status as a stable lowercase label — used in
// SelectionReason text, debug output, and telemetry Detail.
func (s BaselineStatus) String() string {
	switch s {
	case BaselinePresent:
		return "present"
	case BaselineMissingNoScene:
		return "missing_no_scene"
	case BaselineMissingNoOriginSnapshot:
		return "missing_no_origin_snapshot"
	case BaselineMissingJoinedAfterOrigin:
		return "missing_joined_after_origin"
	default:
		return "unknown"
	}
}

// Payload is the immutable result of Build — everything Render needs to
// produce a prompt, derived purely from a published *sim.Snapshot. It is
// "immutable" by convention (built once, never mutated), the same way
// sim.Snapshot is.
type Payload struct {
	ActorID sim.ActorID

	// Actor is the subject actor's own current decision-relevant state.
	Actor ActorView

	// Surroundings is where the actor is right now — structure, huddle,
	// and co-present actors.
	Surroundings SurroundingsView

	// Warrants is every consumed warrant, ordered by SourceEventID
	// ascending — PR 3a's monotonic EventID is the authoritative causal
	// order. Zero-lineage warrants (SourceEventID == 0, legacy/non-event-
	// sourced) sort first; ties hold input order (stable). This is the
	// canonical ordered list; the per-scene groupings below reference the
	// same WarrantMeta values.
	Warrants []sim.WarrantMeta

	// Primary is the scene the baseline diff is computed against — the
	// scene of the warrant with the maximum SourceEventID, or (when no
	// warrant carries a scene) the actor's active-huddle scene. nil when
	// no scene resolved (Baseline == BaselineMissingNoScene).
	Primary *SceneView

	// Secondary holds warrants that reference a scene *other* than the
	// primary one. They render as independent source signals with their
	// own SceneID/HuddleID — the primary scene's baseline is deliberately
	// NOT applied to them. Ordered by SceneID for determinism.
	Secondary []SceneSignal

	// Baseline reports whether Primary.Diff could be established.
	Baseline BaselineStatus

	// MultiSceneWarrantCount is the number of distinct scenes referenced
	// by the consumed warrant batch (1 for the common single-scene tick,
	// 0 when no warrant carries a scene). Surfaced for the handlers-layer
	// telemetry field of the same name.
	MultiSceneWarrantCount int

	// NarrativeState is the actor's engine-side identity continuity —
	// seed_text identity frame + evolving_summary the consolidator
	// rewrites. Non-nil ONLY for KindNPCShared actors that have a
	// populated NarrativeState in the snapshot. Stateful-VA actors get
	// this content from their own VA's <Self> system prompt block via
	// memory-api; injecting engine-side would duplicate or conflict.
	NarrativeState *NarrativeStateView

	// Relationships are per-co-huddle-peer relationship views for the
	// subject actor — summary + recent salient facts for each peer in
	// the actor's current huddle. Populated ONLY for KindNPCShared
	// actors and only for peers the actor has a Relationship row for;
	// empty otherwise. Stateful-VA actors don't get this for the same
	// reason as NarrativeState (their own VA's per-peer context notes
	// cover this — see the symmetric stateful-VA gap at
	// shared/tasks/pending/salem-stateful-va-missing-peer-context).
	//
	// Ordering: sorted by PeerID for determinism.
	Relationships []RelationshipPeerView

	// SelectionReason is a human-readable explanation of how Primary was
	// chosen (or why it wasn't) — debug/test output only, never prompt
	// content.
	SelectionReason string
}

// NarrativeStateView is the kind-aware "Who you are:" content. Slim by
// design — Render combines SeedText and EvolvingSummary into one
// section.
type NarrativeStateView struct {
	SeedText        string
	EvolvingSummary string
}

// RelationshipPeerView is the per-peer entry in the "What you remember
// of those here:" section. RecentFacts holds the most-recent N facts
// (most-recent-first) — Build slices them from the actor's
// Relationships[peerID].SalientFacts, which is stored oldest-first.
type RelationshipPeerView struct {
	PeerID      sim.ActorID
	PeerName    string
	SummaryText string
	RecentFacts []sim.SalientFact
}

// ActorView is the subject actor's own current state, lifted from the
// snapshot's ActorSnapshot. Slim by design — content fills in incrementally
// (PR 3c ships the mechanism, not the final prompt surface).
type ActorView struct {
	State             sim.ActorState
	InsideStructureID sim.StructureID
	Position          sim.Position
	CurrentHuddleID   sim.HuddleID
	Coins             int
	Needs             map[sim.NeedKey]int
}

// SurroundingsView is the actor's immediate context — the structure it is
// in and the huddle it belongs to, with co-present actors named or
// rendered as descriptors based on acquaintance.
type SurroundingsView struct {
	InsideStructureID sim.StructureID

	// StructureName is the structure's DisplayName, or empty when the
	// actor is outdoors or the structure is absent from the snapshot.
	StructureName string

	// HuddleID is the actor's current huddle, empty when not huddled.
	HuddleID sim.HuddleID

	// HuddleMembers are the *other* members of the actor's current huddle
	// (the subject actor is excluded), sorted by ID for determinism.
	// Each carries acquaintance info so Render can pick name vs.
	// descriptor without re-reading the snapshot.
	HuddleMembers []HuddleMember
}

// HuddleMember is one co-huddle peer's identity slice for the
// SurroundingsView. Render emits DisplayName when Acquainted is true,
// otherwise falls back to Role ("the blacksmith") or a generic
// stranger label. Mirrors v1's coLocatedHuddleMembers name-vs-
// descriptor gating.
type HuddleMember struct {
	ID          sim.ActorID
	DisplayName string
	Role        string
	Acquainted  bool
}

// SceneView describes the primary scene and, when a baseline could be
// established, the actor's diff against that scene's origin snapshot.
type SceneView struct {
	SceneID    sim.SceneID
	OriginKind string
	OriginAt   time.Time

	// Warrants are the consumed warrants that reference this scene,
	// ordered by SourceEventID ascending. May be empty when the primary
	// scene was resolved from the actor's active huddle rather than from a
	// scene-bearing warrant.
	Warrants []sim.WarrantMeta

	// Diff is the actor's change since the scene's origin snapshot. Set
	// iff the enclosing Payload's Baseline == BaselinePresent; nil
	// otherwise (the Missing* statuses all mean "no diff").
	Diff *Diff
}

// SceneSignal is a secondary scene referenced by the warrant batch — a
// scene other than the primary. It carries no baseline diff by design:
// the primary scene's origin snapshot says nothing about a different
// scene, and reverse-resolving a baseline per secondary scene would
// multiply the cost and the failure modes for marginal value.
type SceneSignal struct {
	SceneID  sim.SceneID
	HuddleID sim.HuddleID
	Warrants []sim.WarrantMeta
}

// Diff is the subject actor's change between a scene's origin snapshot and
// its current snapshot. It is the loop-detection seam: AnyChange == false
// across several consecutive ticks is the "this actor is stuck" signal.
// Every field is only meaningful when the enclosing Baseline is
// BaselinePresent.
type Diff struct {
	StateChanged     bool
	PositionChanged  bool
	StructureChanged bool
	HuddleChanged    bool
	CoinsChanged     bool
	InventoryChanged bool
	NeedsChanged     bool

	// AnyChange is the OR of every field above — the single value loop
	// detection reads.
	AnyChange bool
}
