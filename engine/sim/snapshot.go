package sim

import "time"

// Snapshot is the immutable, slim view of the world that admin endpoints,
// perception build, and the checkpoint writer all consume. The world
// goroutine publishes a fresh Snapshot via World.published (atomic.Pointer)
// after every command, so readers atomic.Load and serialize without
// touching the world goroutine.
//
// The snapshot deliberately omits secondary indices, mutable handler state,
// and any field that consumers can recompute or don't need.
type Snapshot struct {
	AtTick      uint64
	PublishedAt time.Time

	Actors         map[ActorID]*ActorSnapshot
	Huddles        map[HuddleID]*Huddle
	Scenes         map[SceneID]*Scene
	Structures     map[StructureID]*Structure
	Orders         map[OrderID]*Order
	VillageObjects map[VillageObjectID]*VillageObject

	// NoticeboardContent is the published snapshot of World.NoticeboardContent —
	// per-board authored prose, keyed by the board's village_object id. Cascade-
	// authored (mutable on the world goroutine via SaveNoticeboardContent), so
	// unlike the immutable reference catalogs it MUST ride the snapshot to be
	// read lock-free; the client read surface (ObjectDTO content_text /
	// content_posted_at, ZBBS-HOME-291) reads it here. Value-copied per entry by
	// republish (the struct is flat). nil/absent for boards with no authored
	// content yet.
	NoticeboardContent map[VillageObjectID]*NoticeboardContent

	// Quotes is the published snapshot of World.Quotes — every scene
	// quote in the world (active and terminal), deep-cloned via
	// CloneSceneQuote so snapshot readers can't reach back into world
	// state. PC client perception build looks up
	// Snapshot.Scenes[sceneID].QuoteIDs and dereferences each ID
	// against this map; NPC perception build reads the same data on
	// the world goroutine via the live World.Quotes (no snapshot
	// trip needed). Phase 3 PR S3.
	Quotes map[QuoteID]*SceneQuote

	// PayLedger is the published snapshot of World.PayLedger — every
	// pay-offer entry in the world (pending and terminal), deep-cloned
	// via ClonePayLedgerEntry. Source of truth for admin reconciliation
	// against the projection store (the projection is best-effort;
	// authoritative state lives here). Phase 3 PR S4.
	PayLedger map[LedgerID]*PayLedgerEntry

	// ActionLog is the published snapshot of World.ActionLog — the
	// append-only audit trail of committed agent + engine-source
	// actions, value-copied via CloneActionLog. Consumed by the
	// atmosphere refresh cascade and per-actor narrative
	// consolidation. See engine/sim/action_log.go for the entry
	// shape. nil for an empty log.
	ActionLog []ActionLogEntry

	// PriceBook is the published snapshot of World.PriceBook — the
	// per-(seller, item) ring buffer of recent accepted-price
	// observations, deep-cloned via ClonePriceBook so snapshot
	// readers can't reach back into world state. Consumed by the
	// (not-yet-ported) buyer-side recovery-options / satiation
	// perception blocks and the future seller-side pricing
	// perception. See engine/sim/price_book.go for the substrate
	// contract. nil for an empty book.
	PriceBook map[PriceBookKey]*RingBuffer[PriceObservation]

	Environment WorldEnvironment
	Phase       Phase

	// NeedThresholds is a cloned view of WorldSettings.NeedThresholds —
	// the per-need red-tier boundary. Consumers reading the snapshot
	// off-world (perception, noop-skip preflight) read thresholds here
	// rather than racing on w.Settings directly.
	NeedThresholds NeedThresholds

	// LodgingDefaultWeeklyRate mirrors WorldSettings.LodgingDefaultWeeklyRate
	// (the operator-set weekly rent) so perception — pure over the snapshot —
	// can surface the keeper/lodger nightly-rate hints and the affordability
	// cue without racing on w.Settings. Derive the per-night figure via
	// sim.LodgingNightlyRate. Plain int, copied at publish.
	LodgingDefaultWeeklyRate int
}
