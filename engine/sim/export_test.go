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
)
