package sim

import "time"

// export_test.go re-exports unexported, command-only world helpers under
// their pre-cleanup names so the external `sim_test` package can keep
// calling them. These aliases live in a _test.go file and therefore exist
// only in the test binary — production callers outside the sim package
// have no path to reach them, which is the property the unexport sweep
// is buying.
//
// If you find yourself wanting one of these in a non-test production
// caller, that's a signal you should be issuing a Command instead.
var (
	BuildWalkGrid            = buildWalkGrid
	CommonRoomForStructure   = commonRoomForStructure
	CanEnterRoom             = canEnterRoom
	DetermineTransitionFlips = determineTransitionFlips
	ScheduleFlips            = scheduleFlips
	RegenObjectRefresh       = regenObjectRefresh

	// FireScheduledFlip exposes the post-AfterFunc callback body so the
	// shutdown test can run it synchronously after cancelling the world.
	FireScheduledFlip = fireScheduledFlip

	// Reactor-evaluator helpers — exposed for tests so the unit tests can
	// exercise eligibility and rate-gate primitives without going through
	// a full Command round trip.
	TryStampWarrant         = tryStampWarrant
	ActorReactorDue         = actorReactorDue
	ClearWarrant            = clearWarrant
	NewTickAttemptID        = newTickAttemptID
	ResetReactorStateOnLoad = resetReactorStateOnLoad
	FireScheduledEvaluation = fireScheduledEvaluation
	ArmNextEvaluation       = armNextEvaluation

	// PR 3a reactor helpers — source-aware stamping dedup, the in-flight /
	// recently-consumed source-key bookkeeping, the admission backoff
	// floor, and the terminal-status warrant policy.
	SourceKeySet               = sourceKeySet
	RememberConsumedSourceKey  = rememberConsumedSourceKey
	LastReactorTickAt          = lastReactorTickAt
	ReopenWarrants             = reopenWarrants
	ApplyTerminalWarrantPolicy = applyTerminalWarrantPolicy
	TerminalStatusAddresses    = terminalStatusAddresses

	// NewRootedCommand exposes the internal cross-boundary root hook so
	// tests can exercise its validation (rejects root == 0 / root >
	// eventSeq) without waiting for PR 3's worker to provide a callsite.
	NewRootedCommand = newRootedCommand

	// CheckHuddleDriftAfterPositionMutation exposes the drift-detection
	// helper so PR 4a tests can exercise it directly inside a Command,
	// without needing the locomotion port (PR 4) to provide a callsite.
	CheckHuddleDriftAfterPositionMutation = checkHuddleDriftAfterPositionMutation

	// AttachHuddleToScene exposes the single-mutation-point huddle/scene
	// attach helper so PR 4a tests can exercise the area-scene 1:1
	// invariant directly, without waiting for PR 4's StartOutdoorHuddle
	// to provide a callsite.
	AttachHuddleToScene = attachHuddleToScene

	// NormalizeOutdoorSceneRadius exposes the LoadWorld settings
	// normalizer so PR 4a can table-test default + clamp behavior.
	NormalizeOutdoorSceneRadius = normalizeOutdoorSceneRadius

	// PR 4 structure-anchor layer — the door / loiter pin / visitor slot
	// helpers. All command-only (read w.VillageObjects / w.Assets /
	// w.Actors) except ComputeLoiterTile, which is a pure function exposed
	// so tests can table-drive the resolution order without a World.
	VillageObjectForStructure = villageObjectForStructure
	ComputeLoiterTile         = computeLoiterTile
	EffectiveLoiterTile       = effectiveLoiterTile
	PickVisitorSlot           = pickVisitorSlot
	PickObjectVisitorSlot     = pickObjectVisitorSlot
	TileOccupiedByOtherActor  = tileOccupiedByOtherActor
)

// ActorCanReactNow / ActorCanReactNowAt expose the eligibility gate for
// sim_test. The bare form uses wall-clock now (back-compat with existing call
// sites); the -At form injects a fixed now so timestamp-driven sleep/break
// cases can be pinned deterministically (ZBBS-HOME-329 threaded now through
// actorCanReactNow).
func ActorCanReactNow(w *World, a *Actor) (bool, bool) {
	return actorCanReactNow(w, a, time.Now().UTC())
}

func ActorCanReactNowAt(w *World, a *Actor, now time.Time) (bool, bool) {
	return actorCanReactNow(w, a, now)
}

// VisitorSlotOffsets exposes a copy of the visitor-slot ring so tests can
// assert pickVisitorSlot's scan order without re-declaring the offsets.
var VisitorSlotOffsets = visitorSlotOffsets

// PR 4 locomotion type helpers — pure deep-copy helpers for the
// MoveDestination / MoveIntent types. Exposed so the locomotion-type
// tests can lock the pointer-identity-break contract directly, before
// step 8 wires cloneMoveIntent into CloneActor.
var (
	CloneMoveDestination = cloneMoveDestination
	CloneMoveIntent      = cloneMoveIntent
)

// PR 4 locomotion command-only helpers — read world maps, so they carry
// the "must be inside a Command.Fn" discipline and stay unexported in
// production.
var (
	StructureEntryTile        = structureEntryTile
	ResolvePathTarget         = resolvePathTarget
	StructureMembershipAllows = structureMembershipAllows
	StructureContainingTile   = structureContainingTile
)

// PR 4 locomotion ticker — the per-tick scan helpers. EvaluateLocomotion
// is the public Command (tests drive ticks through it directly); the
// rest are command-only internals exposed for focused unit tests.
var (
	ArrivedAtDestination                     = arrivedAtDestination
	ClassifyTileBlocker                      = classifyTileBlocker
	AdvanceActorLocomotion                   = advanceActorLocomotion
	ActorInActiveHuddle                      = actorInActiveHuddle
	UpdateInsideStructureIDFromTileOwnership = updateInsideStructureIDFromTileOwnership
	SetActorInsideStructure                  = setActorInsideStructure
	ArmNextLocomotionTick                    = armNextLocomotionTick
	FireScheduledLocomotionTick              = fireScheduledLocomotionTick
)

// World-rotation substrate primitives — exposed so sim_test can drive
// the determineRotationFlips helper + ticker check primitives without
// those internals being part of the public sim package surface.
var (
	DetermineRotationFlipsForTest = determineRotationFlips
	PickRandomExcluding           = pickRandomExcluding
	PickDeterministicNext         = pickDeterministicNext
	ExcludedByScope               = excludedByScope
	CheckAndRotateForTest         = checkAndRotate
)

// NPC-route substrate primitives — exposed for the npc_route_test
// suite. BuildWalkGridForTest lets tests run pathfinding against the
// real walk grid (the unexported buildWalkGrid is the production
// builder; making it exported would leak walk-grid construction into
// the public surface).
var (
	BuildWalkGridForTest   = buildWalkGrid
	BuildRouteStopsForTest = buildRouteStops
)

// Phase 3 Group A visitor cascade primitives — exposed so sim_test can
// drive the substrate-side helpers (extractSurname, pickVisitorDestination
// behind a Command, seed-need-map constructor, archetype-pool iteration)
// without those internals being part of the public sim package surface.
var (
	ExtractSurname          = extractSurname
	SeedVisitorNeedsForTest = seedVisitorNeeds
)

// VisitorArchetypePoolForTest returns a copy of the closed-set archetype
// pool. Used by the init-exhaustiveness regression test to assert every
// archetype has a sprite mapping.
func VisitorArchetypePoolForTest() []string {
	out := make([]string, len(visitorArchetypePool))
	copy(out, visitorArchetypePool)
	return out
}

// PickVisitorDestinationResult bundles pickVisitorDestination's
// (StructureID, GridPoint, bool) return tuple so the helper can be
// invoked through a Command.
type PickVisitorDestinationResult struct {
	StructureID StructureID
	Anchor      GridPoint
	Ok          bool
}

// PickVisitorDestinationForTest returns a Command that drives the
// unexported pickVisitorDestination helper and packages its three-tuple
// result. Lets sim_test exercise destination preference rules without
// reaching into the cascade tick.
func PickVisitorDestinationForTest() Command {
	return Command{Fn: func(w *World) (any, error) {
		sid, anchor, ok := pickVisitorDestination(w)
		return PickVisitorDestinationResult{StructureID: sid, Anchor: anchor, Ok: ok}, nil
	}}
}

// PR S2 inventory primitives — unexported helpers exposed for direct unit
// tests. TransferItem is called from inside larger transactions (the future
// S4 accept_pay commit path will be its first production caller);
// ResolveItemKind is a command-only internal to Consume but worth direct
// tests so failure modes (ambiguity, case, trim, empty needle) can be
// exercised without driving a full Consume round trip. The dwell-pin
// lookup it used to expose (findNearestVillageObject) was replaced by
// resolveLoiteringObject, covered by loiter_resolve_test.go.
var (
	TransferItem    = transferItem
	ResolveItemKind = resolveItemKind
)

// ActorsInStructure returns the actor IDs the actorsByStructure secondary
// index attributes to sid — lets PR 4 ticker tests assert the index moves
// in lockstep with InsideStructureID.
func ActorsInStructure(w *World, sid StructureID) []ActorID {
	out := make([]ActorID, 0, len(w.actorsByStructure[sid]))
	for id := range w.actorsByStructure[sid] {
		out = append(out, id)
	}
	return out
}

// OutdoorActorIDs returns the actor IDs the outdoorActors secondary index
// currently holds — lets tests assert lockstep maintenance against
// InsideStructureID across setActorInsideStructure transitions and
// rebuildIndices. Order is undefined; sort if asserting against a fixed
// expected list.
func OutdoorActorIDs(w *World) []ActorID {
	out := make([]ActorID, 0, len(w.outdoorActors))
	for id := range w.outdoorActors {
		out = append(out, id)
	}
	return out
}

// PR 3a reactor tuning constants — exposed so tests can assert TTL / cap /
// backoff behavior against the real values rather than re-declaring them.
const (
	RecentlyConsumedTTL      = recentlyConsumedTTL
	RecentlyConsumedCap      = recentlyConsumedCap
	DefaultMinReactorTickGap = defaultMinReactorTickGap
	DefaultAdmissionBackoff  = defaultAdmissionBackoff
)

// ActorInFlightSourceKeys / ActorRecentlyConsumedSourceKeys expose the
// unexported PR 3a dedup-bookkeeping maps on Actor so sim_test can assert
// the in-flight key set and recently-consumed set without those fields
// being part of the public Actor contract.
func ActorInFlightSourceKeys(a *Actor) map[WarrantSourceKey]struct{} {
	return a.inFlightSourceKeys
}

func ActorRecentlyConsumedSourceKeys(a *Actor) map[WarrantSourceKey]time.Time {
	return a.recentlyConsumedSourceKeys
}

// SetActorInFlightSourceKeys / SetActorRecentlyConsumedSourceKeys let
// sim_test seed the dedup-bookkeeping maps directly when arranging a test
// without driving a full evaluator + completion round trip.
func SetActorInFlightSourceKeys(a *Actor, m map[WarrantSourceKey]struct{}) {
	a.inFlightSourceKeys = m
}

func SetActorRecentlyConsumedSourceKeys(a *Actor, m map[WarrantSourceKey]time.Time) {
	a.recentlyConsumedSourceKeys = m
}

// ActorAwaitingReplyFrom exposes the unexported ZBBS-WORK-370 turn-state map on
// Actor so sim_test can assert which directed awaiting-reply edges an actor
// holds (addressee -> when last addressed) without that field being part of the
// public Actor contract.
func ActorAwaitingReplyFrom(a *Actor) map[ActorID]time.Time {
	return a.awaitingReplyFrom
}

// WorldEventSeq exposes the per-run event counter so sim_test can assert
// EventID monotonicity / "counter starts at 1" without an exported field.
func WorldEventSeq(w *World) uint64 { return w.eventSeq }

// QuoteSeqForTest exposes the per-run QuoteID counter so PR S3 sim_test
// can assert "counter starts at 1" and restart safety-floor behavior.
func QuoteSeqForTest(w *World) uint64 { return w.quoteSeq }

// RebuildIndicesForTest exposes World.rebuildIndices so sim_test can
// repopulate the actorsByStructure / actorsByHuddle / outdoorActors
// secondary indices after a direct map mutation (used when a test
// seeds a huddle via raw map write rather than through JoinHuddle).
func RebuildIndicesForTest(w *World) { w.rebuildIndices() }

// RestartExpireScannedQuotesForTest exposes the LoadWorld-time
// expired-scan helper. PR S3 substrate test only.
func RestartExpireScannedQuotesForTest(w *World, now time.Time) {
	restartExpireScannedQuotes(w, now)
}

// RebuildSceneQuoteIndexForTest exposes the LoadWorld-time Scene.QuoteIDs
// reverse-index rebuild helper. PR S3 substrate test only.
func RebuildSceneQuoteIndexForTest(w *World) { rebuildSceneQuoteIndex(w) }

// PR S4 pay-ledger substrate helpers — exposed so sim_test can exercise
// the substrate primitives without needing the (later-shipping) Command
// Fns to drive them.
//
// NextLedgerSeq is the world-goroutine-only LedgerID minter (callers
// MUST be inside a Command.Fn). EffectivePayLedgerTTL /
// EffectivePayLedgerSweepCadence wrap the WorldSettings → default
// fallbacks for direct table tests. RestartExpirePendingEntries is the
// LoadWorld-time pending-entry expiry pass. ApplyPayLedgerCounterSafetyFloor
// re-runs LoadWorld's counter-safety loop against the current
// World.PayLedger so tests can simulate "loaded from a future
// PayLedgerRepo with high-water IDs but a stale counter."
func NextLedgerSeq(w *World) LedgerID { return w.nextLedgerSeq() }

// RestartReStampPayOfferWarrants exposes the LoadWorld pay-offer
// warrant re-stamp pass for direct unit tests. PR S4 step 7.
func RestartReStampPayOfferWarrants(w *World, now time.Time) {
	restartReStampPayOfferWarrants(w, now)
}
func EffectivePayLedgerTTL(s WorldSettings) time.Duration { return effectivePayLedgerTTL(s) }
func EffectivePayLedgerSweepCadence(s WorldSettings) time.Duration {
	return effectivePayLedgerSweepCadence(s)
}
func RestartExpirePendingEntries(w *World, now time.Time)  { restartExpirePendingEntries(w, now) }
func ReapTerminalPayLedgerEntries(w *World, now time.Time) { reapTerminalPayLedgerEntries(w, now) }
func EffectivePayLedgerTerminalRetention(s WorldSettings) time.Duration {
	return effectivePayLedgerTerminalRetention(s)
}

// ApplyPayLedgerCounterSafetyFloor re-runs the floor loop LoadWorld
// performs after loading PayLedger entries from a repo. Used by the
// substrate test to simulate "loaded entries have higher IDs than the
// loaded counter" — the next mint must still avoid collisions.
func ApplyPayLedgerCounterSafetyFloor(w *World) {
	for id := range w.PayLedger {
		if uint64(id) > w.payLedgerSeq {
			w.payLedgerSeq = uint64(id)
		}
	}
}

// PayLedgerSeqForTest exposes the per-run LedgerID counter so PR S4
// sim_test can assert "counter starts at 0" and restart safety-floor
// behavior in isolation.
func PayLedgerSeqForTest(w *World) uint64 { return w.payLedgerSeq }

// NextOrderSeq is the world-goroutine-only OrderID minter for tests
// (callers MUST be inside a Command.Fn). Phase 3 PR S6.
func NextOrderSeq(w *World) OrderID { return w.nextOrderSeq() }

// EffectiveOrderTTL exposes the WorldSettings → default fallback for
// table tests. Phase 3 PR S6.
func EffectiveOrderTTL(s WorldSettings) time.Duration { return effectiveOrderTTL(s) }

// EffectiveOrderSweepCadence exposes the WorldSettings → default
// fallback for table tests. Phase 3 PR S6.
func EffectiveOrderSweepCadence(s WorldSettings) time.Duration { return effectiveOrderSweepCadence(s) }

// RestartExpirePendingOrders is the LoadWorld-time order expiry pass.
// Test hook for the PR S6 restart contract.
func RestartExpirePendingOrders(w *World, now time.Time) { restartExpirePendingOrders(w, now) }

// OrderSeqForTest exposes the per-run OrderID counter so PR S6
// sim_test can assert "counter starts at 0" and restart safety-floor
// behavior in isolation.
func OrderSeqForTest(w *World) uint64 { return w.orderSeq }

// CreateOrderForPayWithItem exposes the internal helper that mints
// an Order from a PayLedgerEntry at AcceptPay time. Test hook so
// the order_commands_test.go suite can verify substrate behavior
// without driving a full pay-with-item flow.
func CreateOrderForPayWithItem(w *World, entry *PayLedgerEntry, at time.Time) OrderID {
	return createOrderForPayWithItem(w, entry, at)
}

// FinalizeOrderTerminal exposes the helper used by both DeliverOrder
// and EvaluateOrderSweep so direct sweep-skip tests can pre-flip an
// Order's state without running the full sweep.
func FinalizeOrderTerminal(w *World, o *Order, terminal OrderState, at time.Time) {
	finalizeOrderTerminal(w, o, terminal, at)
}

// OutstandingReadyOrderQty exposes the order-reservation accounting
// helper (PR S6 R1 code_review fix) for direct tests of accept_pay's
// gate-9 / fast-path predicate-6 reservation math.
func OutstandingReadyOrderQty(w *World, sellerID ActorID, item ItemKind) int {
	return outstandingReadyOrderQty(w, sellerID, item)
}

// RepublishForTest invokes World.republish so substrate tests can swap
// the published Snapshot without driving a full Command round trip
// (which would require starting Run). Production callers never need
// this — republish is invoked automatically after every command on the
// world goroutine.
func RepublishForTest(w *World) { w.republish() }

// WorldCurrentRootEventID exposes the ambient cascade root so sim_test can
// assert withRoot's defer-scoped restore (including the panic path).
func WorldCurrentRootEventID(w *World) EventID { return w.currentRootEventID }

// EmitForTest invokes the unexported World.emit so sim_test can drive
// event identity / nested-root behavior directly. MUST be called on the
// world goroutine (inside a Command.Fn) or against a non-Run world in a
// single-threaded test — same contract as a production subscriber's emit.
func EmitForTest(w *World, evt Event) { w.emit(evt) }

// SourceKey / EventSourced expose the unexported WarrantMeta dedup-key
// helpers for sim_test.
func SourceKey(m WarrantMeta) WarrantSourceKey { return m.sourceKey() }
func EventSourced(m WarrantMeta) bool          { return m.eventSourced() }

// --- summon errand test accessors (ZBBS-HOME-311) -------------------------
// The errand map + struct are unexported; sim_test drives the machine
// through these read accessors and the chat-pause driver.

// SummonErrandCount returns the number of active errands in the world.
// Used by the bounded-membership assertions ("map empty after every
// terminal path").
func SummonErrandCount(w *World) int { return len(w.SummonErrands) }

// SummonErrandStateByID returns the string state of the errand with the
// given id and ok=false when no such errand exists. ErrandID is exported,
// so sim_test holds the id returned by DispatchSummon.
func SummonErrandStateByID(w *World, id ErrandID) (string, bool) {
	e, ok := w.SummonErrands[id]
	if !ok || e == nil {
		return "", false
	}
	return string(e.State), true
}

// SummonErrandMessengerByID returns the messenger actor selected for the
// errand (so a test can position it for the next leg's arrival).
func SummonErrandMessengerByID(w *World, id ErrandID) (ActorID, bool) {
	e, ok := w.SummonErrands[id]
	if !ok || e == nil {
		return "", false
	}
	return e.MessengerID, true
}

// SummonErrandLegAttemptByID returns the MovementAttemptID of the walk leg
// the errand is currently waiting on — the value a synthetic ActorArrived
// must carry to advance the machine.
func SummonErrandLegAttemptByID(w *World, id ErrandID) (MovementAttemptID, bool) {
	e, ok := w.SummonErrands[id]
	if !ok || e == nil {
		return 0, false
	}
	return e.LegAttemptID, true
}

// RunSummonCommissionForTest fires the commissioning chat-pause beat
// synchronously (the AfterFunc body) so tests drive the messenger-at-point
// → messenger-to-target/refusal transition without a real-time wait.
func RunSummonCommissionForTest(w *World, id ErrandID, now time.Time) {
	runSummonChatPause(id, summonCommission, now).Fn(w)
}

// RunSummonDeliverForTest fires the delivery chat-pause beat synchronously
// so tests drive the messenger-at-target → messenger-returning/refusal
// transition without a real-time wait.
func RunSummonDeliverForTest(w *World, id ErrandID, now time.Time) {
	runSummonChatPause(id, summonDeliver, now).Fn(w)
}

// ClearSummonCuesForTest exposes the cue-clear helper so sim_test can assert
// the nil-safe / both-fields-cleared behavior directly.
func ClearSummonCuesForTest(a *Actor) { clearSummonCues(a) }

// RunSummonErrandTTLForTest fires the bounded-lifetime TTL body synchronously
// so a test can assert a stuck/superseded errand (one whose tracked leg never
// produced a matching ActorArrived) is swept from the map — the guarantee that
// a leaked errand can't suppress the summoner's arrival warrants forever.
func RunSummonErrandTTLForTest(w *World, id ErrandID) { expireSummonErrand(id).Fn(w) }

// SuppressArrivalWarrantForTest invokes the world's installed
// suppressArrivalWarrant hook for actorID (the work-domain seam consulted by
// finishArrival). Returns false when no hook is installed (the default) or
// the actor is unknown — matching finishArrival's "stamp unless suppressed"
// gate.
func SuppressArrivalWarrantForTest(w *World, actorID ActorID) bool {
	if w.suppressArrivalWarrant == nil {
		return false
	}
	a, ok := w.Actors[actorID]
	if !ok {
		return false
	}
	return w.suppressArrivalWarrant(a)
}
