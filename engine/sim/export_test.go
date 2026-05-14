package sim

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
