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
	ActorCanReactNow        = actorCanReactNow
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
	TileOccupiedByOtherActor  = tileOccupiedByOtherActor
)

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

// WorldEventSeq exposes the per-run event counter so sim_test can assert
// EventID monotonicity / "counter starts at 1" without an exported field.
func WorldEventSeq(w *World) uint64 { return w.eventSeq }

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
