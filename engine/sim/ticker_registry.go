package sim

// ticker_registry.go — the cadence contract for every sim-package ticker
// (LLM-395). One table, declared in one place, so the staleness alarm has a
// number to judge each ticker against.
//
// Why this lives in package sim rather than in cmd/engine, next to the
// startTickers goroutines it describes: most of these cadences are NOT exported.
// Six of them are settings-derived (effectiveOrderSweepCadence and friends) and
// several more are package-private constants. Registering from cmd/engine would
// mean exporting the engine's entire cadence configuration purely so the caller
// could read it back and hand it straight to the world — so the declaration
// belongs with the knowledge instead.
//
// The cascade-package tickers declare themselves the same way, from their own
// RegisterX helpers (they can't see these symbols either, and their intervals are
// their own). Both land in the one TickerHealth registry.

// RegisterCoreTickers declares the expected cadence of every ticker and sweep
// that cmd/engine's startTickers launches, opting each into the ticker_stale
// alarm.
//
// MUST BE CALLED BEFORE THOSE GOROUTINES START. Registration is what makes a
// ticker visible to the alarm, and an unregistered ticker never alarms (the
// fail-safe in ticker_health.go), so declaring from inside the goroutine bodies
// would leave the single worst failure — a cadence driver that never came up —
// silently uncovered. Declaring here, ahead of the `go` statements, means a
// ticker that never fires is stale from its registration stamp onward.
//
// The cadences below are the BOOT-TIME values. Six of these tickers are AfterFunc
// chains whose cadence is live-tunable at runtime; each re-declares its current
// cadence on every re-arm (see armNextOrderSweep and its siblings), which is the
// moment a retuned value actually takes effect. So the registry always describes
// the cadence the chain is ACTUALLY scheduled at, not a stale boot snapshot.
//
// Keep this in step with startTickers: cmd/engine's ticker-coverage test fails if
// any beaten ticker is undeclared, or any declared ticker is never beaten.
func RegisterCoreTickers(w *World) {
	// Uniform time.NewTicker loops — fixed cadence for the life of the goroutine.
	w.RegisterTicker("locomotion", LocomotionTickInterval)
	w.RegisterTicker("phase", PhaseTickerInterval)
	w.RegisterTicker("needs", NeedsTickerInterval)
	w.RegisterTicker("tiredness_recovery", TirednessRecoveryTickerInterval)
	w.RegisterTicker("sleep", SleepTickerInterval)
	w.RegisterTicker("shift", ShiftTickerInterval)
	w.RegisterTicker("dwell", DwellTickerInterval)
	w.RegisterTicker("produce", ProduceTickerInterval)
	w.RegisterTicker("restock", RestockTickerInterval)
	w.RegisterTicker("production_choice", ProductionChoiceTickerInterval)
	w.RegisterTicker("object_refresh_regen", ObjectRefreshRegenInterval)
	w.RegisterTicker("source_activity", SourceActivityTickerInterval)
	w.RegisterTicker("room_sweep", RoomSweepInterval)
	w.RegisterTicker("pc_presence", PCPresenceSweepInterval)
	w.RegisterTicker("rotation", RotationTickerInterval)

	// Coalesced AfterFunc self-rearm chains. Continuous despite the AfterFunc
	// shape — every one of them re-arms unconditionally at the end of its scan,
	// work or no work, so silence from any of these means the chain is BROKEN, not
	// idle. (AfterFunc was chosen for coalescing and for re-reading the cadence
	// from settings on each re-arm, not because the beat is event-driven.)
	w.RegisterTicker("reactor", effectiveReactorEvaluatorCadence(w.Settings))
	w.RegisterTicker("order_sweep", effectiveOrderSweepCadence(w.Settings))
	w.RegisterTicker("pay_ledger_sweep", effectivePayLedgerSweepCadence(w.Settings))
	w.RegisterTicker("labor_ledger_sweep", effectiveLaborLedgerSweepCadence())
	w.RegisterTicker("huddle_silence_sweep", effectiveHuddleSilenceSweepCadence(w.Settings))
	w.RegisterTicker("huddle_loop_sweep", effectiveHuddleLoopSweepCadence(w.Settings))
	w.RegisterTicker("scene_quote_sweep", effectiveSceneQuoteSweepCadence(w.Settings))

	// The odd one out: not a substrate driver but the world command path's own
	// liveness probe (LLM-402). It is declared here with the rest so ticker_stale
	// watches the watchman — if the prober goroutine dies, its silence raises the
	// staleness alarm and the direct instrument cannot fail quietly. It beats
	// BEFORE each send, so a wedged world does NOT silence it; that is what keeps
	// the two signals independent (see world_command_probe.go).
	w.RegisterTicker(WorldCommandProbeTickerName, WorldCommandProbeInterval)
}
