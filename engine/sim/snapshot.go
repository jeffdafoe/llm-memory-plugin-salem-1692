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

	// LocalMinuteOfDay is the wall-clock minute-of-day (0–1439) in the village
	// timezone at publish time, or nil when the clock can't be established
	// (hand-built snapshots, or before settings load a Location). Computed once
	// at publish via localMinuteOfDay so consumers read the local clock without
	// the village *time.Location, which is not on the snapshot. Perception
	// renders it as time-of-day prose (ZBBS-HOME-351); schedule-aware steering
	// compares it to schedule_start/end_minute (ZBBS-HOME-352). Pointer so a
	// hand-built snapshot (nil) is distinguishable from real midnight (0).
	LocalMinuteOfDay *int

	// DawnMinute / DuskMinute are the world's dawn/dusk boundary times as
	// minute-of-day (e.g. 420 = 07:00, 1140 = 19:00), parsed once at publish
	// from WorldSettings.DawnTime/DuskTime. They are the day-active window used
	// as the shift fallback for an NPC with no explicit schedule (mirrors
	// effectiveShiftWindow), so schedule-aware return-to-post steering
	// (ZBBS-HOME-352) can resolve the same window perception-side without
	// w.Settings. DawnDuskMinuteOK is true only when BOTH boundaries parsed —
	// perception uses the window only then, so a partial/failed parse can't
	// derive a bogus window from one good + one zero bound (code_review).
	DawnMinute       int
	DuskMinute       int
	DawnDuskMinuteOK bool

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

	// RestockReorderPct mirrors WorldSettings.RestockReorderPct — the reorder
	// threshold (whole percent of cap) for buy-side restock. Carried so the
	// "## Restocking" perception section can gate on the same boundary the
	// restock producer warrants on, pure over the snapshot rather than racing
	// on w.Settings. Plain int, copied at publish. 0 = restock disabled.
	RestockReorderPct int

	// ZoomMinAdmin / ZoomMinRegular mirror WorldSettings.ZoomMin* — the
	// client-side camera zoom floors (admin vs regular user). Carried on the
	// snapshot because the public GET /api/village/world read (handleWorld) is
	// lock-free off the snapshot and every client reads its zoom floor from
	// there; the admin config write routes mutate w.Settings on the world
	// goroutine, so a republish surfaces a saved change. Floats, copied at
	// publish. (The rest of the admin-tunable config — timezone, rotation time,
	// agent-ticks — is NOT mirrored here: the admin-only GET /api/village/config
	// read runs through the command channel and reads live w.Settings directly.)
	ZoomMinAdmin   float64
	ZoomMinRegular float64

	// ItemKinds is an ALIASED reference to World.ItemKinds — the item→satisfies
	// reference catalog loaded once at startup (LoadWorld) and never mutated
	// afterward (ItemKindDef is documented read-only). Unlike the mutable
	// aggregates above it is NOT cloned at publish: the map and its *ItemKindDef
	// values are immutable, so sharing the reference is race-free and avoids
	// re-cloning the whole catalog on every command's republish. Perception —
	// pure over the snapshot — reads it to answer "what does this item ease, and
	// by how much?" for the recovery-options consumable arm and the seller-side
	// satiation cues. nil only before the first LoadWorld (hand-built test
	// snapshots that don't exercise item perception leave it nil).
	ItemKinds map[ItemKind]*ItemKindDef
}
