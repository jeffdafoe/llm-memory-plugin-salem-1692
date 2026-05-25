package sim

import (
	"context"
	"log"
	"sort"
	"sync/atomic"
	"time"
)

// Phase is the current daypart in the world. Salem operates on a simple
// two-phase day/night cycle driven by configurable dawn/dusk boundaries.
type Phase string

const (
	PhaseDay   Phase = "day"
	PhaseNight Phase = "night"
)

// WorldEnvironment carries world-level transient state: time-of-day,
// weather, atmosphere prose (the chronicler-replacement single-string mood
// line refreshed every ~4h), and timestamps the various tickers use to
// avoid re-firing a boundary they have already processed.
type WorldEnvironment struct {
	Now                     time.Time
	Weather                 string
	Atmosphere              string
	LastAtmosphereRefreshAt time.Time // last successful atmosphere refresh (UTC); see engine/sim/atmosphere.go. Restart-lossy by design — cosmetic prose, fresh fire after restart is acceptable.
	LastTransitionAt        time.Time // last day↔night transition (UTC). Durable — persisted in world_state.last_transition_at.
	LastRotationAt          time.Time // last daily asset rotation (UTC). Durable — persisted in world_state.last_rotation_at.
	LastNeedsTickAt         time.Time // last hourly needs increment (UTC, hour-truncated). Durable — persisted in world_state.last_needs_tick_at.
}

// WorldSettings carries world-level config — checkpoint cadence, phase
// boundary times, admin-tunable thresholds. Fields expand per subsystem
// port; nothing here is hot-path on the tick.
type WorldSettings struct {
	CheckpointInterval time.Duration

	// Phase boundary times in HH:MM, interpreted in Timezone.
	DawnTime     string
	DuskTime     string
	RotationTime string
	Timezone     string
	Location     *time.Location

	// Client-side zoom floors — different for admins vs regular users.
	// Pure UI config; the sim package carries the values so admin endpoints
	// have one place to read/write.
	ZoomMinAdmin   float64
	ZoomMinRegular float64

	// AgentTicksPaused, when true, suppresses LLM agent activity globally —
	// reactive NPC ticks and chronicler fires both gated. Worker schedulers,
	// social hours, lamplighter, and rotation continue running. Used to halt
	// agent activity mid-session when a bad loop is being investigated.
	AgentTicksPaused bool

	// Lodging hour-of-day tunables (legacy lodging_check_in_hour /
	// lodging_check_out_hour). Interpreted in WorldSettings.Location.
	LodgingCheckInHour  int
	LodgingCheckOutHour int

	// LodgingDefaultWeeklyRate is the operator-set rent for a private room,
	// stored weekly (the booking/cadence unit) but billed and quoted per
	// night as LodgingNightlyRate(rate) = rate/7 — "4 a night" reads better
	// in a haggle than "28 a week", and the per-night figure is what the
	// keeper advertises. Consumed by the keeper/lodger perception rate hints,
	// the lodger affordability cue, and the engine-auto rebook sweep. 0 (or
	// any value < 7, which floors the nightly rate to 0) disables the rate
	// surfaces and the auto-rebook. Default 28 (4/night).
	LodgingDefaultWeeklyRate int

	// ShiftLatenessWindowMinutes staggers NPC arrivals at work so the whole
	// village doesn't head out on the same minute when shifts begin (the v2 port
	// of v1's per-NPC lateness_window_minutes, reshaped to one global tunable —
	// ZBBS-HOME-309). Each NPC's to-work duty is delayed by a deterministic
	// offset in [0, window) seeded by (actor id, shift-start) — see
	// shiftLatenessOffset in shift_duty.go. 0 disables (all due NPCs leave on the
	// same minute, the pre-HOME-309 behavior). Settings key:
	// shift_lateness_window_minutes. DB-configured only; no editor UI.
	ShiftLatenessWindowMinutes int

	// NPCSleepMaxDurationHours is the safety cap on an auto-bedded NPC's
	// sleep — wakeExpiredNPCSleepers clears SleepingUntil at this cap or at
	// shift-start, whichever comes first. Default 12.
	NPCSleepMaxDurationHours int

	// Needs tunables. NeedsTickAmount is the per-hour increment magnitude
	// applied to every eligible actor. NeedThresholds carries the per-need
	// "red" boundary; TirednessCriticalThreshold is the absolute (not pct)
	// threshold at which on-shift recovery gates lift.
	// MovementFatiguePerTileX100 is fatigue per tile of movement, stored ×100.
	NeedsTickAmount            int
	NeedThresholds             NeedThresholds
	TirednessCriticalThreshold int
	MovementFatiguePerTileX100 int

	// TirednessRecoveryPerMinuteX100 is how fast tiredness drops per
	// wall-clock minute while an actor is asleep or on break, stored ×100
	// (10 → 0.1/min). 0 disables recovery. Consumed by RunTirednessRecoveryTicker.
	TirednessRecoveryPerMinuteX100 int

	// RestockReorderPct is the reorder threshold for the buy-side restock
	// producer (ZBBS-WORK-322), expressed as a whole percent of an entry's
	// personal-carry cap: a reseller's `buy` RestockEntry is "low" — and
	// warrants a restock tick — when its on-hand quantity is strictly below
	// cap * pct / 100. Default 25 (a quarter). 0 disables the producer (no
	// restock warrant ever fires), the same off-switch posture as the other
	// per-feature tunables. Integer-percent storage, not a float, matching
	// the project's _x100 / permille / pct convention. Consumed by the
	// restock producer and the "## Restocking" perception gate.
	RestockReorderPct int

	// Reactor evaluator tunables (Phase 2 PR 2). Settings-driven gross
	// gates — no per-call cost calculation; llm-memory-api's per-VA dollar
	// budgets (MEM-052) own the hard $ ceiling.
	//
	// ReactorJitterMin/Max: stamped at warrant time as now+jitter. Provides
	// conversational pacing (1-4s default — fires feel like turn-taking,
	// not LLM-speed turbo).
	//
	// ReactorEvaluatorCadence: how often the evaluator runs. 250ms gives
	// ±250ms timing precision around the jitter floor, which is fine for
	// conversational scale.
	//
	// MaxWarrantAge: cleared on LoadWorld; not currently used at runtime
	// (warrants are ephemeral). Kept for future use if persistence lands.
	//
	// MaxReactorTicksPerActorPerMinute: per-actor rate cap over a rolling
	// 1-minute window. Drops to 0 (disabled) by default; turn on if a
	// noisy environment produces sub-jitter ping-pong loops in practice.
	// Capped actors get their WarrantDueAt pushed to the next allowed
	// time rather than silently skipped each scan. Distinct from
	// MinReactorTickGap below — that is the always-on per-tick pacing
	// floor; this is the rolling-window ceiling.
	//
	// MaxWarrantsPerActor: cap on the per-actor Warrants list size. When
	// exceeded, oldest entries drop (freshest signals are most relevant).
	// 0 = uncapped.
	//
	// MinReactorTickGap: per-actor minimum wall-clock gap between reactor
	// ticks — an always-on pacing floor independent of the optional per-
	// minute rate cap above. Default 5s (defaultMinReactorTickGap). A
	// warrant coming due inside the gap has its WarrantDueAt pushed to the
	// gap boundary; a Force warrant bypasses it.
	//
	// AdmissionBackoff: how far the evaluator pushes an actor's
	// WarrantDueAt when tick admission control turns it away (downstream
	// worker pool at capacity). Default 250ms (defaultAdmissionBackoff) ≈
	// the evaluator cadence, so a deferred warrant is re-examined on
	// roughly the next scan. The warrants stay OPEN — a deferral consumes
	// nothing.
	//
	// TickWorkerCount: number of off-world goroutines in PR 3's tick worker
	// pool. Defaults to 1 (handlers.defaultTickWorkerCount) — a pool >1
	// gives nondeterministic cross-actor commit order, so the default must
	// not imply an ordering guarantee the system lacks. The pool derives
	// its bounded job-buffer size from this; backpressure is a feature.
	ReactorJitterMin                 time.Duration
	ReactorJitterMax                 time.Duration
	ReactorEvaluatorCadence          time.Duration
	MaxWarrantAge                    time.Duration
	MaxReactorTicksPerActorPerMinute int
	MaxWarrantsPerActor              int
	MinReactorTickGap                time.Duration
	AdmissionBackoff                 time.Duration
	TickWorkerCount                  int

	// Idle-backstop tunables (engine/sim/cascade/idle_backstop.go). Both
	// fall back to defaults when zero, so tests that bypass the
	// environment loader get sensible behavior without seeding them.
	//
	// IdleBackstopThreshold: how long an actor must go without a reactor
	// tick before the idle-backstop sweep stamps a WarrantKindIdleBackstop
	// warrant. Default 30 min (defaultIdleBackstopThreshold in
	// reactor.go) — engine-injected liveness for actors no other warrant
	// has engaged. Production can tune up; sandbox / dev keeps the
	// default for visible behavior.
	//
	// IdleBackstopSweepInterval: how often the idle-backstop sweep walks
	// the actor list. Default 5 min (defaultIdleBackstopSweepInterval in
	// engine/sim/cascade/idle_backstop.go — owned by cascade since cascade
	// owns the goroutine driver). Detection latency ≤ this interval
	// against the threshold; oversample cost is trivial (per-actor field
	// reads on the world goroutine, no allocations).
	IdleBackstopThreshold     time.Duration
	IdleBackstopSweepInterval time.Duration

	// AtmosphereRefreshInterval is the cadence at which the atmosphere
	// refresh cascade slice fires a salem-generic LLM call to rewrite
	// World.Environment.Atmosphere. Default 4h
	// (defaultAtmosphereRefreshInterval in
	// engine/sim/cascade/atmosphere.go — owned by cascade since cascade
	// owns the goroutine driver). Settings-driven from day one so dev /
	// staging can tune it down for testing without rebuilding.
	AtmosphereRefreshInterval time.Duration

	// Action-log substrate tunables (engine/sim/action_log.go +
	// engine/sim/cascade/action_log.go). Both fall back to defaults
	// when zero, so tests that bypass the environment loader get
	// sensible behavior without seeding them.
	//
	// ActionLogRetention: how far back the in-memory action log
	// keeps entries. Compaction sweep drops entries with OccurredAt
	// before (now - retention). Default 48h
	// (sim.DefaultActionLogRetention) — covers atmosphere's 4h refresh
	// interval with headroom and consolidation's expected 24h window
	// cleanly. Dev / staging can tune down.
	//
	// ActionLogSweepInterval: how often the compaction sweep fires.
	// Default 1h (defaultActionLogSweepInterval in
	// engine/sim/cascade/action_log.go — owned by cascade since
	// cascade owns the goroutine driver). Stale entries past
	// retention are still tens of hours old; the sweep cadence just
	// controls how promptly memory is reclaimed.
	ActionLogRetention     time.Duration
	ActionLogSweepInterval time.Duration

	// Visitor cascade tunables (engine/sim/visitor.go +
	// engine/sim/cascade/visitor.go). All fall back to *Default constants
	// in engine/sim/visitor.go when zero, so tests that bypass the
	// environment loader get sensible behavior without seeding them.
	//
	// VisitorSpawnChancePermille: per-tick (per-thousand) probability of
	// spawning a new visitor when below the concurrent cap. Default 0 —
	// the feature is no-op until an admin opts in by raising this. At
	// VisitorTickInterval = 60s, a value of ~10-30 produces "one visitor
	// per game day on average."
	//
	// VisitorMaxConcurrent: cap on simultaneous visitors. Zero or unset
	// falls back to DefaultVisitorMaxConcurrent (2). The documented
	// halt-spawn admin dial is VisitorSpawnChancePermille=0, not a
	// sentinel here.
	//
	// VisitorMinStayMinutes / VisitorMaxStayMinutes: stay-window bounds.
	// Concrete stay is a uniform random pull from [min, max] at spawn.
	// Defaults 240 / 1440 (4h floor, 24h ceiling).
	//
	// VisitorTickInterval: how often the cascade slice runs its three
	// dispatchers (despawn → cleanup → spawn). Default 60s — matches
	// v1's runServerTickOnce cadence the visitor handlers piggybacked on.
	VisitorSpawnChancePermille int
	VisitorMaxConcurrent       int
	VisitorMinStayMinutes      int
	VisitorMaxStayMinutes      int
	VisitorTickInterval        time.Duration

	// Businessowner cascade tunables (engine/sim/businessowner.go +
	// engine/sim/cascade/businessowner.go). Both fall back to
	// *Default constants when zero, so tests that bypass the
	// environment loader get sensible behavior without seeding them.
	//
	// BusinessownerGreetCooldownMinutes: per-(keeper, customer) gap
	// between engine-spoken greet lines. Default 30 min — covers "the
	// customer popped out for an errand and came back" with a re-greet
	// on the second visit, but suppresses the redundant "welcome friend"
	// when the same customer rejoins the huddle ten seconds later after
	// stepping outside to fetch something.
	//
	// BusinessownerFarewellCooldownMinutes: mirrors the greet cooldown.
	// Same UX reasoning.
	//
	// Handover (OrderDelivered) has no cooldown by design — every
	// transaction deserves a verbal handover line.
	BusinessownerGreetCooldownMinutes    int
	BusinessownerFarewellCooldownMinutes int

	// DefaultOutdoorSceneRadius is the conversational radius used by
	// SceneBoundArea when callers don't specify one explicitly. Measured
	// in king's-move (Chebyshev) tiles around the bound's Anchor.
	// normalizeOutdoorSceneRadius applies the default and the bounds
	// clamp at LoadWorld:
	//   - 0 / unset / negative → DefaultOutdoorSceneRadiusValue (3 tiles)
	//   - above DefaultOutdoorSceneRadiusMax (10) → clamped to max
	DefaultOutdoorSceneRadius int

	// Scene-quote substrate tunables (Phase 3 PR S3). Both fall back to
	// scene_quote.go's *Default constants when zero, so tests that
	// bypass the environment loader get sensible behavior without
	// seeding them.
	//
	// SceneQuoteTTL: how long a freshly minted quote stays Active before
	// the aging sweep flips it Expired. Default 10 min — asymmetric
	// (longer) with the pay-ledger pending TTL (2-5 min) since a
	// quote is a passive ad rather than a staked offer.
	//
	// SceneQuoteSweepCadence: how often the aging sweep scans
	// World.Quotes for expired entries. Default 60s — gives ±60s expiry
	// latency against the 10-min TTL, invisible at gameplay scale.
	SceneQuoteTTL          time.Duration
	SceneQuoteSweepCadence time.Duration

	// Pay-ledger substrate tunables (Phase 3 PR S4). Both fall back to
	// pay_ledger.go's *Default constants when zero. Shorter TTL than
	// SceneQuoteTTL — a pending pay offer has the buyer staked into a
	// social moment, which decays faster than a passive quote ad does.
	//
	// PayLedgerTTL: how long a freshly minted pending entry stays
	// Pending before the aging sweep flips it Expired. Default 3 min —
	// middle of architecture § 3's 2-5 minute range.
	//
	// PayLedgerSweepCadence: how often the aging sweep scans
	// World.PayLedger for expired pending entries. Default 60s —
	// matches the scene-quote sweep cadence so admin tuning sees one
	// mental model.
	//
	// PayLedgerTerminalRetention: how long a terminal entry lingers in
	// World.PayLedger before the sweep reaps it. Bounds the otherwise-
	// unbounded growth of the offer-side map (terminal entries are never
	// otherwise removed). Default 1h; floored at PayLedgerInResponseToWindow
	// so a countered parent is never reaped while still referenceable via
	// in_response_to. See pay_ledger.go.
	PayLedgerTTL               time.Duration
	PayLedgerSweepCadence      time.Duration
	PayLedgerTerminalRetention time.Duration

	// Order substrate tunables (Phase 3 PR S6). Both fall back to
	// order.go's *Default constants when zero. The order TTL is the
	// post-acceptance fulfillment window — longer than PayLedgerTTL
	// since at this stage the buyer has already committed (coins
	// debited) and we want plenty of room for the seller's reactor
	// to fire and deliver.
	//
	// OrderTTL: how long an Order at OrderStateReady sits before
	// the aging sweep flips it OrderStateExpired. Default 10 min.
	//
	// OrderSweepCadence: how often the aging sweep scans World.Orders
	// for expired entries. Default 60s — matches the PayLedger and
	// SceneQuote sweep cadences.
	OrderTTL          time.Duration
	OrderSweepCadence time.Duration
}

// DefaultOutdoorSceneRadiusValue is the fallback radius used when
// callers don't specify one. 3 tiles is a reasonable "stop-and-chat"
// distance on the village grid.
const DefaultOutdoorSceneRadiusValue = 3

// DefaultOutdoorSceneRadiusMax caps the configured radius. Larger
// values are clamped down at LoadWorld. Conversational radii beyond
// 10 tiles are unlikely to reflect "people standing close enough to
// chat" — the cap is a sanity floor, not a hard physics constraint.
const DefaultOutdoorSceneRadiusMax = 10

// normalizeOutdoorSceneRadius applies the default + clamp to the
// settings at load time. Called from LoadWorld after the environment
// loader returns. Unexported by design.
func normalizeOutdoorSceneRadius(s *WorldSettings) {
	if s == nil {
		return
	}
	switch {
	case s.DefaultOutdoorSceneRadius <= 0:
		s.DefaultOutdoorSceneRadius = DefaultOutdoorSceneRadiusValue
	case s.DefaultOutdoorSceneRadius > DefaultOutdoorSceneRadiusMax:
		s.DefaultOutdoorSceneRadius = DefaultOutdoorSceneRadiusMax
	}
}

// SpeechHelper is the generic-dialogue pool. Pull(type, fromActor, toActor)
// returns a line for a typed scenario; both actors nullable. v1 ignores
// actors and selects randomly; future context-aware selection becomes a
// helper-internal change (callsites already wire both actors through).
//
// TODO: port from scattered hardcoded line arrays + per-tick LLM generic
// speech during speech subsystem port.
type SpeechHelper struct{}

// reactorEvaluatorState carries the coalescing flag that gates the
// AfterFunc self-rearm chain. Owned by the world (mutated only from the
// world goroutine), exposed to the timer callback that drives the next
// evaluation. No mutex needed — the flag is read/written exclusively from
// inside Command.Fn.
type reactorEvaluatorState struct {
	scheduled bool
}

// locomotionTickerState carries the coalescing flag for the locomotion
// ticker's AfterFunc self-rearm chain (Phase 2 PR 4). Same shape and
// rules as reactorEvaluatorState — read/written exclusively from inside
// Command.Fn, so no mutex.
type locomotionTickerState struct {
	scheduled bool
}

// sceneQuoteSweepState carries the coalescing flag for the scene-quote
// aging sweep's AfterFunc self-rearm chain (Phase 3 PR S3). Same shape
// and rules as locomotionTickerState — read/written exclusively from
// inside Command.Fn.
type sceneQuoteSweepState struct {
	scheduled bool
}

// payLedgerSweepState carries the coalescing flag for the pay-ledger
// aging sweep's AfterFunc self-rearm chain (Phase 3 PR S4 step 8).
// Same shape and rules as sceneQuoteSweepState.
type payLedgerSweepState struct {
	scheduled bool
}

// orderSweepState carries the coalescing flag for the Order aging
// sweep's AfterFunc self-rearm chain (Phase 3 PR S6). Same shape
// and rules as payLedgerSweepState.
type orderSweepState struct {
	scheduled bool
}

// World is the in-memory state of one realm's simulation. A single
// goroutine (started by World.Run) owns all mutable fields below — every
// mutation must go through the cmds channel. Readers consume the published
// Snapshot via atomic.Pointer (World.Published).
//
// Per design: zero locks, zero races by construction.
type World struct {
	// Primary state — source of truth.
	Actors         map[ActorID]*Actor
	Structures     map[StructureID]*Structure
	Huddles        map[HuddleID]*Huddle
	Scenes         map[SceneID]*Scene
	Orders         map[OrderID]*Order
	VillageObjects map[VillageObjectID]*VillageObject

	// Quotes is the world-level flat map of all SceneQuotes (active and
	// terminal). Keyed by QuoteID — the LLM-visible uint64 the buyer
	// references in pay_with_item(quote_id=N, ...) at fast-path time.
	// Mirrored by a per-scene reverse index at Scene.QuoteIDs (rebuilt
	// at LoadWorld from this map; the canonical entries live here).
	//
	// Phase 3 PR S3 substrate. No checkpoint persistence layer, and
	// none planned — pending quotes are intentionally restart-lossy
	// (decided 2026-05-20 — see work/tasks/payledger-restart-lossy/decision).
	// NewWorld / LoadWorld both start with an empty Quotes map, which
	// IS the intended restart behavior: a pending quote crossing a
	// restart should re-emit fresh via the next scene_quote tool call,
	// not be resurrected with stale ExpiresAt.
	Quotes map[QuoteID]*SceneQuote

	// PayLedger is the world-level flat map of all PayLedgerEntries
	// (pending and terminal). Keyed by LedgerID — the LLM-visible
	// uint64 the seller references in accept_pay / decline_pay /
	// counter_pay, and the buyer references in withdraw_pay /
	// pay_with_item(in_response_to=N).
	//
	// Phase 3 PR S4 substrate. Sole source of truth for the offer-side
	// state machine — there is no durable backing at all. Pending
	// pay_ledger entries are intentionally restart-lossy (decided
	// 2026-05-20 — see work/tasks/payledger-restart-lossy/decision):
	// no PayLedgerRepo, no projection sink, and pg.SaveWorld does not
	// checkpoint pending entries. NewWorld / LoadWorld both start with
	// an empty PayLedger map and stay that way until live commerce
	// mints fresh entries; the LoadWorld restart re-stamp pass is
	// dormant by design (nothing to re-stamp). Accepted entries that
	// became Orders persist separately via OrdersRepo on the shared
	// pay_ledger table.
	PayLedger map[LedgerID]*PayLedgerEntry

	// BusinessownerCooldowns is the per-(speaker, listener, trigger) gap
	// map used by the businessowner cascade slice to suppress redundant
	// engine-spoken hospitality lines (e.g. don't re-greet the same
	// customer who just popped out and came back in seconds). Lazy-
	// allocated on first stamp; nil-readable as empty. World-goroutine-
	// only; restart-loss is acceptable (first-greet on re-encounter post-
	// restart is a UX wrinkle, not a correctness failure).
	BusinessownerCooldowns map[BusinessownerCooldownKey]time.Time

	// BusinessownerSpeechAt stamps the last engine-authored hospitality
	// speech instant per keeper actor. Consulted by actorCanReactNow to
	// suppress an LLM follow-up tick on the same triggering event for
	// businessownerEngineSpeechSuppressionTTL (5s). Lazy-allocated on
	// first stamp; nil-readable as empty. World-goroutine-only; restart-
	// loss is acceptable (the in-flight reactor schedule the suppression
	// guards against is itself lost on restart).
	BusinessownerSpeechAt map[ActorID]time.Time

	// ActiveRoutes holds the in-flight per-NPC scheduled-route state
	// machines (lamplighter / washerwoman / town_crier). Keyed by the
	// running NPC's ActorID; nil-readable as empty (lazy-allocated on
	// first StartNPCRoute). The cascade ActorArrived subscriber consults
	// this map to advance an arrived actor's route — most arrivals match
	// no entry and are no-ops.
	//
	// World-goroutine-only; restart-loss is acceptable. A lamplighter or
	// washerwoman walking mid-route across an engine restart loses the
	// in-flight route; the next phase / rotation boundary re-triggers
	// the cycle. The carved-out objects sit at their pre-route state
	// until then — same UX wrinkle as a missed boundary, not a
	// correctness failure.
	ActiveRoutes map[ActorID]*NPCRoute

	// NoticeboardContent stores per-board authored prose — what the
	// town crier reads on arrival, what NPCs loitering at the board
	// will perceive once that read path ports. Keyed by VillageObjectID
	// of the noticeboard placement; nil-readable as empty (lazy-
	// allocated on first SaveNoticeboardContent).
	//
	// World-goroutine-only; restart-loss is acceptable. A board with
	// stamped content across a restart loses it; the next rotation
	// cycle authors fresh content. First cycle after cold start: crier
	// reads nothing (board empty); subsequent cycles read normally.
	NoticeboardContent map[VillageObjectID]*NoticeboardContent

	// ActionLog is the world-level append-only audit trail of
	// committed agent + engine-source actions. Consumed by the
	// atmosphere refresh cascade (group-by-actor-by-action since
	// last fire) and per-actor narrative consolidation (own + peer
	// rows within a recent window). See engine/sim/action_log.go
	// for the entry shape and the package doc explaining what's
	// dropped vs v1's pg schema.
	//
	// In-memory only at MVP — no repo wire-through; mem package
	// keeps the noopActionLog sink. Restart-loss is acceptable:
	// atmosphere's last-fire stamp resets on restart and C2
	// re-snapshots from current state.
	ActionLog []ActionLogEntry

	// PriceBook is the in-memory per-(seller, item) ring buffer of
	// recent accepted-price observations — v2's substrate for v1's
	// price-history perception cues ("you paid X coins last time").
	// Keyed by (SellerID, Item); each ring buffer holds the latest
	// PriceBookRingCapacity transactions across all buyers. Per-buyer
	// reads filter the buffer; per-seller reads aggregate it. See
	// engine/sim/price_book.go for the substrate contract.
	//
	// In-memory only — no checkpoint pass, no projection sink.
	// pay_ledger remains the source of truth; this is a perception
	// cache. Seeded at LoadWorld from OrdersRepo.LoadRecentPrices
	// over a PriceBookSeedWindow-wide tail; maintained via the
	// PayWithItemResolved{Accepted} subscriber in
	// cascade/price_book.go.
	//
	// Lazy-allocated on first SeedPriceBook or RecordPriceObservation;
	// nil-readable as empty.
	PriceBook map[PriceBookKey]*RingBuffer[PriceObservation]

	// Asset catalog — reference state, loaded at startup. Looked up by
	// VillageObject.AssetID for state resolution, footprint, anchor, etc.
	Assets map[AssetID]*Asset

	// Sprite catalog — reference state, loaded at startup. The character-
	// render definitions; looked up by Actor.SpriteID to resolve the sheet
	// + animation rows for the client read surface. Separate catalog from
	// Assets (object/terrain art). Hot-reload on SIGHUP when admin edits
	// land. See sprite.go.
	Sprites map[SpriteID]*Sprite

	// Attribute-definition catalog — reference state, loaded at startup. The
	// actor-assignable attribute vocabulary (scope actor/both), keyed by slug.
	// Surfaced to the editor's attribute-add dropdown via the npc-behaviors
	// read endpoint. Distinct from the actor_attribute rows on each Actor
	// (those are assignments; this is the catalog of what can be assigned).
	// Hot-reload on SIGHUP, same lifecycle as Assets/Sprites. See
	// attribute_definition.go.
	AttributeDefinitions map[string]*AttributeDefinition

	// Recipe catalog — reference state. Keyed by OutputItem. Used by
	// produce_tick (rate + inputs + output_qty) and pay-deliberation
	// (wholesale/retail prices).
	Recipes map[ItemKind]*ItemRecipe

	// ItemKind catalog — reference state. Keyed by Name (== ItemKind). The
	// definitional source for an item's display label, category, default
	// price, sort order, and per-need satisfies entries (port of v1's
	// item_kind + item_satisfies tables). Loaded at startup; hot-reloaded
	// on SIGHUP when admin edits land. See item_kind.go.
	//
	// IMMUTABILITY CONTRACT: the published Snapshot ALIASES this map (not a
	// clone — see Snapshot.ItemKinds). Two rules keep that race-free, and the
	// future SIGHUP hot-reload MUST preserve both: (1) never mutate the map or
	// a *ItemKindDef in place after LoadWorld — rebuild wholesale via LoadAll
	// and reassign the field (an already-published snapshot then keeps its old,
	// still-immutable map); (2) do that reassignment on the world goroutine
	// (e.g. via a Command), so it can't race a republish reading this field.
	ItemKinds map[ItemKind]*ItemKindDef

	// Terrain — reference state, loaded once at startup. MapW * MapH
	// bytes of per-tile terrain type. Hot-reload on SIGHUP if needed.
	Terrain *Terrain

	// Secondary indices — rebuildable from primary state at LoadWorld time
	// and kept consistent by command handlers thereafter.
	actorsByStructure map[StructureID]map[ActorID]struct{}
	actorsByHuddle    map[HuddleID]map[ActorID]struct{}
	// outdoorActors tracks every actor with InsideStructureID == "". Hot-
	// path optimization for the encounter subscribers (handleArrival-
	// Encounter, handleMovedEncounter): at 200+ actors, scanning w.Actors
	// linearly on every ActorMoved is the wrong shape. Most actors are
	// indoor at any moment (sleeping, working, dining), so the outdoor set
	// is a small fraction of the population and the scan stays bounded by
	// outdoor density rather than total population.
	//
	// Maintained in lockstep with InsideStructureID by setActorInside-
	// Structure (the single mutation chokepoint) and rebuilt from primary
	// state by rebuildIndices. Iterated read-only via ForEachOutdoorActor.
	outdoorActors map[ActorID]struct{}

	Environment WorldEnvironment
	Phase       Phase
	Settings    WorldSettings
	TickCounter uint64

	// LoadedAt is the wall-clock moment LoadWorld populated this world
	// from the repository. Set once by LoadWorld; never modified
	// afterward. Read by the idle-backstop cascade slice as the cold-
	// start anchor for actors with no RecentReactorTicks history (a
	// fresh-loaded actor is "active at LoadedAt," not "idle forever").
	// Other consumers don't need this — lastReactorTickAt is the
	// authoritative source for per-actor tick history, and its
	// nil-RecentReactorTicks "never ticked" semantics is what the
	// MinReactorTickGap pacing floor and rate gate both rely on.
	LoadedAt time.Time

	Speech          *SpeechHelper
	reactorEval     reactorEvaluatorState
	locomotionTick  locomotionTickerState
	sceneQuoteSweep sceneQuoteSweepState
	payLedgerSweep  payLedgerSweepState
	orderSweep      orderSweepState

	// quoteSeq is the monotonic per-run QuoteID counter — same shape
	// and rules as eventSeq. Incremented before assignment; first
	// minted QuoteID is 1 (QuoteID(0) reserved as the unset sentinel).
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	quoteSeq uint64

	// payLedgerSeq is the monotonic per-run LedgerID counter — same
	// shape and rules as quoteSeq. Incremented before assignment;
	// first minted LedgerID is 1 (LedgerID(0) reserved as the unset
	// sentinel / "no parent" / "no quote referenced").
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	payLedgerSeq uint64

	// orderSeq is the monotonic per-run OrderID counter — same shape
	// and rules as payLedgerSeq. Incremented before assignment; first
	// minted OrderID is 1 (OrderID(0) reserved as the unset sentinel).
	// World-goroutine-only (touched exclusively from inside Command.Fn).
	orderSeq uint64

	// terminalOrderSink is the synchronous durable-write target for Order
	// terminal transitions (Slice 6 write-through-then-prune). Nil by
	// default; SetTerminalOrderSink installs the pg impl at production
	// startup. When nil, finalizeOrderTerminal preserves the legacy
	// no-prune behavior so unit tests that build a world via NewWorld
	// without wiring a sink continue to see terminal entries remain in
	// w.Orders. See TerminalOrderSink doc for the contract.
	terminalOrderSink TerminalOrderSink

	cmds      chan Command
	published atomic.Pointer[Snapshot]

	// runCtx is the lifecycle context the world goroutine is running
	// under. Set by Run on entry and INTENTIONALLY RETAINED after Run
	// exits, so callbacks firing post-shutdown observe the cancelled
	// ctx (rather than a fresh background ctx) and abort cleanly via
	// ctx.Err() instead of parking on a dead cmds channel.
	//
	// Used by long-lived goroutines launched outside the ticker loop
	// (notably time.AfterFunc-driven scheduled flips) via
	// World.LifecycleContext.
	//
	// Atomic so non-world-goroutine readers (the flip timer callbacks)
	// can pick it up without going through the command channel.
	runCtx atomic.Pointer[context.Context]

	// WorldEventGen is bumped after any world-level state change that could
	// invalidate scheduled follow-ups (phase transitions, occupancy refresh,
	// asset rotation). Long-running scheduled work (e.g. spread-out object
	// flips fired via time.AfterFunc) captures the generation at schedule
	// time and skips itself when the world has moved on.
	//
	// Atomic so the goroutine-launched scheduler can read it without
	// going through the command channel. Writers (inside the world
	// goroutine) use Add to make the bump observable.
	WorldEventGen atomic.Uint64

	// subscribers receive in-world Events emitted from command handlers.
	// Registered via Subscribe before Run starts; each event is dispatched
	// to every subscriber in registration order, synchronously inside the
	// world goroutine. See events.go for the contract.
	subscribers []EventSubscriber

	// eventSeq is the monotonic per-run event counter. emit increments it
	// and assigns the value as the new event's EventID. World-goroutine-
	// only — emit runs exclusively inside Command.Fn, so no atomic is
	// needed. Starts at 0; the first emitted event gets ID 1, leaving
	// EventID(0) as the unset sentinel.
	eventSeq uint64

	// currentRootEventID is the ambient causal root for events emitted
	// within the current cascade. 0 means no cascade is active — the next
	// emit becomes a fresh root. Set and restored by withRoot (defer-
	// scoped, panic-safe). World-goroutine-only.
	currentRootEventID EventID

	// tickAdmission gates the reactor evaluator — consulted before an
	// actor's warrants are consumed (Option A — admit before consume).
	// Never nil: NewWorld sets alwaysAdmit, and PR 3's worker pool installs
	// a real one via SetTickAdmissionController.
	tickAdmission TickAdmissionController

	repo Repository
}

// nextEventSeq increments the per-run event counter and returns the new
// EventID. World-goroutine-only (called from emit). The counter starts at
// 0, so the first event is EventID(1) — EventID(0) is never assigned.
func (w *World) nextEventSeq() EventID {
	w.eventSeq++
	return EventID(w.eventSeq)
}

// withRoot runs fn with currentRootEventID set to root, restoring the
// previous value on return — including on panic, via defer. World-
// goroutine-only; no atomic. Used by emit (to establish a fresh cascade
// root) and by Run (to continue an inherited root across the worker seam).
func (w *World) withRoot(root EventID, fn func()) {
	prev := w.currentRootEventID
	w.currentRootEventID = root
	defer func() { w.currentRootEventID = prev }()
	fn()
}

// NewWorld constructs an empty World bound to the given Repository.
//
// Call LoadWorld for production startup (populates primary state from
// persistence); tests typically use NewWorld + direct map seeding so they
// can control the initial state precisely.
//
// The cmds channel is buffered to absorb bursts without blocking
// producers; the world goroutine drains it.
func NewWorld(repo Repository) *World {
	w := &World{
		Actors:               make(map[ActorID]*Actor),
		Structures:           make(map[StructureID]*Structure),
		Huddles:              make(map[HuddleID]*Huddle),
		Scenes:               make(map[SceneID]*Scene),
		Orders:               make(map[OrderID]*Order),
		VillageObjects:       make(map[VillageObjectID]*VillageObject),
		Quotes:               make(map[QuoteID]*SceneQuote),
		PayLedger:            make(map[LedgerID]*PayLedgerEntry),
		Assets:               make(map[AssetID]*Asset),
		Sprites:              make(map[SpriteID]*Sprite),
		AttributeDefinitions: make(map[string]*AttributeDefinition),
		Recipes:              make(map[ItemKind]*ItemRecipe),
		ItemKinds:            make(map[ItemKind]*ItemKindDef),
		actorsByStructure:    make(map[StructureID]map[ActorID]struct{}),
		actorsByHuddle:       make(map[HuddleID]map[ActorID]struct{}),
		outdoorActors:        make(map[ActorID]struct{}),
		Speech:               &SpeechHelper{},
		cmds:                 make(chan Command, 256),
		tickAdmission:        alwaysAdmit{},
		repo:                 repo,
	}
	w.republish()
	return w
}

// SetTerminalOrderSink installs the synchronous durable-write target the
// world invokes during Order terminal transitions (Slice 6 write-through-
// then-prune). Passing nil clears the sink and restores legacy no-prune
// behavior. Safe to call before Run, or from inside a Command.Fn.
//
// Wiring order matters at production startup: SetTerminalOrderSink must
// be called BEFORE LoadWorld so the LoadWorld-time
// restartExpirePendingOrders pass also write-through-prunes any orders
// whose ExpiresAt elapsed during downtime. Calling it after LoadWorld
// leaves those restart-time terminal entries sitting in w.Orders until
// the next checkpoint reconciles them.
func (w *World) SetTerminalOrderSink(s TerminalOrderSink) {
	w.terminalOrderSink = s
}

// SetTickAdmissionController installs the controller the reactor evaluator
// consults before consuming an actor's warrants. PR 3's worker pool calls
// this at bootstrap (as one half of RegisterTickHandlers). A nil argument
// resets to the alwaysAdmit default.
//
// Safe to call before Run, or from inside a Command.Fn (the world
// goroutine). Calling it from an arbitrary goroutine while Run is
// processing races the evaluator — route it through a Command instead.
func (w *World) SetTickAdmissionController(c TickAdmissionController) {
	if c == nil {
		c = alwaysAdmit{}
	}
	w.tickAdmission = c
}

// LoadWorld constructs a World and populates primary state from the
// repository. Use this for production startup.
//
// Sub-repos implemented at this stage (Actors, Huddles, Environment)
// are loaded; remaining sub-repos land as subsystems get ported.
// Indices are rebuilt from primary state, snapshot is published, ready
// to Run.
func LoadWorld(ctx context.Context, repo Repository) (*World, error) {
	w := NewWorld(repo)

	actors, err := repo.Actors.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Actors = actors

	huddles, err := repo.Huddles.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Huddles = huddles

	scenes, err := repo.Scenes.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Scenes = scenes

	env, phase, settings, err := repo.Environment.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.Environment = env
	w.Phase = phase
	w.Settings = settings

	assets, err := repo.Assets.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Assets = assets

	sprites, err := repo.Sprites.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Sprites = sprites

	attributeDefinitions, err := repo.AttributeDefinitions.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.AttributeDefinitions = attributeDefinitions

	recipes, err := repo.Recipes.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Recipes = recipes

	itemKinds, err := repo.ItemKinds.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.ItemKinds = itemKinds

	terrain, err := repo.Terrain.Load(ctx)
	if err != nil {
		return nil, err
	}
	w.Terrain = terrain

	structures, err := repo.Structures.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.Structures = structures

	villageObjects, err := repo.VillageObjects.LoadAll(ctx)
	if err != nil {
		return nil, err
	}
	w.VillageObjects = villageObjects

	w.FinalizeLoad(ctx)
	return w, nil
}

// FinalizeLoad runs the post-load housekeeping that turns a freshly
// populated World into a runnable one: index rebuild, reactor-state
// reset, the restart-time expiry/re-stamp passes, sequence-counter
// safety floors, the price-book seed, and the initial snapshot publish.
//
// Extracted from LoadWorld so the pg orchestrator (engine/sim/repo/pg)
// can reuse this exact finalize sequence: it lives in a different
// package and can't reach these unexported sim internals (rebuildIndices,
// the restart* helpers, the seq counters, republish) directly. Keeping
// the sequence in one place is the whole point — both LoadWorld and
// pg.LoadWorld stay in lockstep as housekeeping evolves.
//
// Callers MUST invoke this only after every aggregate has loaded — and,
// for pg.LoadWorld, after the cross-aggregate consistency checks and the
// actor carry-forwards (reconcileActorHuddleMembership in particular,
// since rebuildIndices reads actor.CurrentHuddleID).
func (w *World) FinalizeLoad(ctx context.Context) {
	normalizeOutdoorSceneRadius(&w.Settings)

	w.rebuildIndices()
	// Reactor state (warrants + in-flight + attempt-id + recent-tick ring)
	// is ephemeral by design — payloads are interface-typed and weren't
	// designed to cross the checkpoint serialization boundary. Cascade
	// origins re-engage actors via fresh events post-restart; the warrant
	// list from before the crash isn't meaningful anymore (the
	// conversational moment passed).
	for _, a := range w.Actors {
		resetReactorStateOnLoad(a)
	}
	// LoadedAt is the wall-clock moment this world woke up (not
	// w.Environment.Now, which can lag arbitrarily on a long-crash
	// recovery). Read by the idle-backstop sweep so fresh-loaded actors
	// — who have no RecentReactorTicks history yet — are treated as
	// "active at world wake-up" rather than "never ticked, idle by
	// maximum duration." Without that, the first sweep after restart
	// would stamp idle warrants on every actor simultaneously. See
	// engine/sim/idle_backstop_commands.go.
	w.LoadedAt = time.Now().UTC()
	// Scene-quote restart housekeeping. Pending scene quotes are
	// intentionally restart-lossy (decided 2026-05-20 — see
	// work/tasks/payledger-restart-lossy/decision): there is no
	// QuotesRepo and none will be built, so w.Quotes always starts
	// empty and this pass is DORMANT BY DESIGN — it iterates an empty
	// map under both LoadWorld paths. Kept (not deleted) because it
	// encodes correct behavior if that decision is ever reversed: any
	// quote already past its ExpiresAt at restart would flip to expired
	// with ResolvedAt stamped, no event emitted (the original
	// SceneQuoteCreated event is gone, so a re-stamped expired event
	// would have nothing to reference causally — restart-noncritical
	// per scene-quote-design § 7). A pending quote crossing a restart
	// is meant to re-emit fresh via the next scene_quote tool call, not
	// be resurrected with stale ExpiresAt.
	restartExpireScannedQuotes(w, time.Now())
	// QuoteIDs reverse index is rebuilt from the canonical World.Quotes
	// map so any drift loaded from a repo can't persist past startup.
	rebuildSceneQuoteIndex(w)
	// Quote sequence counter safety floor: if the loaded counter is
	// somehow below the max QuoteID actually present, bump it so the
	// next mint doesn't collide. Idempotent — both paths produce the
	// same result when the counter was correct.
	for id := range w.Quotes {
		if uint64(id) > w.quoteSeq {
			w.quoteSeq = uint64(id)
		}
	}
	// Pay-ledger restart housekeeping. Pending pay_ledger entries are
	// intentionally restart-lossy (decided 2026-05-20 — see
	// work/tasks/payledger-restart-lossy/decision): there is no
	// PayLedgerRepo and none will be built, pending entries are not
	// checkpointed by pg.SaveWorld, so w.PayLedger always starts empty
	// and this pass is DORMANT BY DESIGN — it iterates an empty map
	// under both LoadWorld paths. Losing a pending entry on crash is
	// materially harmless (architecture section 2 — a pending entry locks
	// no coins, stock, or presence; accept_pay revalidates every gate),
	// and the 3-minute TTL means most pending offers would have expired
	// during any real downtime anyway. Kept (not deleted) because it
	// encodes correct behavior if the decision is ever reversed: any
	// pending entry already past its ExpiresAt would flip to expired
	// with ResolvedAt stamped, no event emitted (the original
	// PayOfferReceived event is gone, so the flip has no causal anchor).
	restartExpirePendingEntries(w, time.Now())
	// Ledger sequence counter safety floor: same posture as quoteSeq.
	for id := range w.PayLedger {
		if uint64(id) > w.payLedgerSeq {
			w.payLedgerSeq = uint64(id)
		}
	}
	// Pay-offer warrant restart re-stamp (Phase 3 PR S4 step 7).
	// DORMANT BY DESIGN — pending pay_ledger entries are restart-lossy
	// (decided 2026-05-20 — see work/tasks/payledger-restart-lossy/decision),
	// so w.PayLedger is always empty here and there is nothing to
	// re-stamp. Kept (not deleted) because it documents the load-bearing
	// rationale for the WarrantReason.DedupDiscriminator migration: if
	// pending entries were ever reloaded, this pass would walk them and
	// stamp PayOfferWarrantReason on each seller so the seller's next
	// reactor tick still perceives the offer, with Discriminator =
	// uint64(LedgerID) so a normal-flow PayOfferReceived emit firing
	// AFTER this stamp dedupes against it cleanly. It would run after
	// restartExpirePendingEntries (so already-expired pendings are
	// skipped) and reach the actor's warrant slice directly via
	// tryStampWarrant (no subscriber needed).
	restartReStampPayOfferWarrants(w, time.Now())

	// Order restart housekeeping (Phase 3 PR S6). w.Orders is populated
	// under pg.LoadWorld (OrdersRepo.LoadAll) and empty under sim.LoadWorld
	// (the mem path doesn't load orders), so this pass is a no-op there and
	// live under pg: any Ready Order already past its
	// ExpiresAt at restart is flipped to Expired in-band, mirroring
	// restartExpirePendingEntries' pay-ledger pattern. Active Ready
	// orders survive the load with absolute ExpiresAt intact; the
	// aging sweep picks them up on its first pass.
	restartExpirePendingOrders(w, time.Now())
	// Order sequence counter safety floor: same posture as quoteSeq /
	// payLedgerSeq.
	for id := range w.Orders {
		if uint64(id) > w.orderSeq {
			w.orderSeq = uint64(id)
		}
	}

	// Price-book seed (Phase 4 Slice 7). Pulls the top-K most recent
	// accepted pay_ledger rows per (seller, item) within
	// PriceBookSeedWindow, populating the in-memory price book so
	// post-restart perception has v1-parity buyer recall ("you paid
	// X last time") and seller-side aggregates available without
	// a thundering herd of "ask the keeper" turns.
	//
	// Seed source is pay_ledger (state='accepted') — the source of
	// truth for accepted transactions across both ConsumeNow and
	// take-home flows; LoadRecentPrices reads it directly without
	// going through the (not-yet-loaded) Orders working set.
	//
	// Failures here are non-fatal to LoadWorld: a missing seed
	// produces "ask the keeper" until the cascade subscriber re-
	// populates the book through normal play. Surfaced via log so
	// operator can spot a degraded restart in stderr.
	if seedRecords, err := w.repo.Orders.LoadRecentPrices(ctx, time.Now().UTC().Add(-PriceBookSeedWindow), PriceBookRingCapacity); err != nil {
		log.Printf("sim: FinalizeLoad price-book seed: %v (continuing with empty book)", err)
	} else {
		w.SeedPriceBook(seedRecords)
	}

	w.republish()
}

// Run owns the world goroutine. Processes commands until ctx is cancelled
// or the cmds channel is closed. Returns when the loop exits.
//
// Caller is responsible for starting this in a goroutine. After ctx
// cancel, in-flight commands complete; queued commands are dropped.
//
// Stamps w.runCtx so callbacks scheduled inside commands (e.g. phase-
// transition flip timers) can ride the same shutdown signal — see
// World.LifecycleContext. Deliberately does NOT clear runCtx on exit:
// if the timer fires after Run has returned, the stored ctx is already
// cancelled, so the callback's SendContext sees ctx.Err() != nil and
// returns immediately instead of parking forever on the cmds channel.
func (w *World) Run(ctx context.Context) {
	w.runCtx.Store(&ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-w.cmds:
			if !ok {
				return
			}
			var value any
			var err error
			if cmd.inheritedRoot != 0 {
				// Cross-boundary command (PR 3's worker tool-call): run the
				// whole handler under the inherited cascade root so events
				// it emits continue that root and it cannot bleed into the
				// next command. See newRootedCommand.
				w.withRoot(cmd.inheritedRoot, func() {
					value, err = cmd.Fn(w)
				})
			} else {
				value, err = cmd.Fn(w)
			}
			w.TickCounter++
			w.republish()
			if cmd.Reply != nil {
				cmd.Reply <- CommandResult{Value: value, Err: err}
			}
		}
	}
}

// SendContext enqueues a command and waits for the reply, honoring ctx
// cancellation on both the send and receive halves. Returns ctx.Err() if
// the context expires before the world goroutine accepts the command or
// before the reply comes back.
//
// Use this from tickers / long-lived goroutines that need to unblock when
// the world is shutting down — Send (no context) deadlocks if Run has
// already exited.
//
// Caller MUST NOT call SendContext from inside a command Fn — that would
// deadlock the single world goroutine. Use direct mutation instead.
func (w *World) SendContext(ctx context.Context, cmd Command) (any, error) {
	reply := make(chan CommandResult, 1)
	cmd.Reply = reply
	select {
	case w.cmds <- cmd:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-reply:
		return r.Value, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Send enqueues a command and waits for the reply. Returns the command's
// Value and Err.
//
// SAFETY CONTRACT: only call Send when the caller knows the world is
// running. There is no context plumbed in — if the world goroutine has
// already exited (or hasn't started), Send blocks on the cmds channel
// forever. Tickers, long-lived background goroutines, and anything
// launched via time.AfterFunc MUST use SendContext with a context that
// gets cancelled on shutdown (see World.LifecycleContext for the
// world's own ctx).
//
// Caller MUST NOT call Send from inside a command Fn — that would
// deadlock the single world goroutine. Use direct mutation (you already
// hold the world goroutine) instead.
func (w *World) Send(cmd Command) (any, error) {
	return w.SendContext(context.Background(), cmd)
}

// LifecycleContext returns the context Run is currently using, or a
// background context if Run has never been called. Goroutines launched
// from inside a command (notably time.AfterFunc-driven scheduled flips)
// call this to get a ctx that unblocks on world shutdown.
//
// After Run exits the cancelled ctx remains in place, so a callback
// firing post-shutdown sees ctx.Err() != nil and aborts cleanly instead
// of deadlocking on a send to a dead cmds channel.
//
// Pulled fresh each time — the schedule-to-fire window can be many
// seconds, and an admin force-phase mid-window could in principle
// re-enter Run with a new ctx in the future (not today; Run is run-once).
func (w *World) LifecycleContext() context.Context {
	if p := w.runCtx.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// Submit enqueues a fire-and-forget command. Returns immediately. Caller
// does not get to observe the outcome — use Send if you need the result.
func (w *World) Submit(fn func(*World) (any, error)) {
	w.cmds <- Command{Fn: fn}
}

// Subscribe registers an EventSubscriber to receive in-world Events emitted
// by command handlers. Subscribers run synchronously inside the world
// goroutine after each event is emitted; they may mutate world state
// freely (atomic with the emitting command) but MUST NOT block on I/O or
// call Send/SendContext (would deadlock the single goroutine).
//
// Safe to call (a) before Run has started, or (b) from inside a Command.Fn
// (which runs on the world goroutine). Calling from an arbitrary goroutine
// while Run is processing commands races against the dispatch loop in
// emit — surface those registrations through a Command instead.
//
// Subscribers fire in registration order; later subscribers see any state
// changes earlier subscribers made.
func (w *World) Subscribe(s EventSubscriber) {
	w.subscribers = append(w.subscribers, s)
}

// emit assigns the event its per-run identity and dispatches it to every
// registered subscriber. Called from command Fn implementations after the
// underlying state mutation lands. Inline dispatch keeps subscriber side
// effects atomic with the mutation — readers of the next Snapshot see the
// post-mutation, post-subscriber state.
//
// Identity: every event gets a fresh monotonic EventID. The RootEventID
// depends on whether a cascade is already active:
//
//   - No ambient root (currentRootEventID == 0): this is a fresh-origin
//     event and is its own causal root. Subscriber dispatch runs under
//     withRoot(id, ...) so events emitted by subscribers (the cascade)
//     inherit this root, and the ambient root restores to 0 on unwind —
//     even if a subscriber panics.
//
//   - Ambient root set: this is a consequent event; it inherits the
//     ambient cascade root. Dispatch needs no extra withRoot — the
//     ambient value is already correct for any nested emits.
func (w *World) emit(evt Event) {
	id := w.nextEventSeq()
	root := w.currentRootEventID
	if root == 0 {
		root = id
		evt.setEventBase(id, root)
		w.withRoot(root, func() {
			for _, s := range w.subscribers {
				s.Handle(w, evt)
			}
		})
		return
	}
	evt.setEventBase(id, root)
	for _, s := range w.subscribers {
		s.Handle(w, evt)
	}
}

// Published returns the most recently published Snapshot. Safe to call
// from any goroutine — atomic load, no coordination.
func (w *World) Published() *Snapshot {
	return w.published.Load()
}

// rebuildIndices populates the actorsByStructure / actorsByHuddle /
// outdoorActors secondary indices from primary state. Called by
// LoadWorld and as a defensive recovery path if drift is ever detected.
func (w *World) rebuildIndices() {
	w.actorsByStructure = make(map[StructureID]map[ActorID]struct{})
	w.actorsByHuddle = make(map[HuddleID]map[ActorID]struct{})
	w.outdoorActors = make(map[ActorID]struct{})
	for id, a := range w.Actors {
		if a.InsideStructureID != "" {
			if w.actorsByStructure[a.InsideStructureID] == nil {
				w.actorsByStructure[a.InsideStructureID] = make(map[ActorID]struct{})
			}
			w.actorsByStructure[a.InsideStructureID][id] = struct{}{}
		} else {
			w.outdoorActors[id] = struct{}{}
		}
		if a.CurrentHuddleID != "" {
			if w.actorsByHuddle[a.CurrentHuddleID] == nil {
				w.actorsByHuddle[a.CurrentHuddleID] = make(map[ActorID]struct{})
			}
			w.actorsByHuddle[a.CurrentHuddleID][id] = struct{}{}
		}
	}
}

// ForEachOutdoorActor invokes fn for every actor currently outdoors
// (InsideStructureID == ""). Iteration stops if fn returns false. Order
// is undefined; callers needing a deterministic order must sort the
// IDs they collect.
//
// Backed by the outdoorActors secondary index — O(K) where K is the
// outdoor population, not O(N) where N is total actor count. Intended
// for hot-path subscribers (encounter detection on ActorMoved /
// ActorArrived) at 200+ actor scale.
//
// MUST be called from inside a Command.Fn or a subscriber dispatched
// from emit (both run on the world goroutine).
//
// SNAPSHOT SEMANTICS. Iteration is over a snapshot of outdoor IDs taken
// at entry, then each ID is re-checked against w.outdoorActors and
// w.Actors before fn is invoked. So fn MAY safely mutate world state —
// including calls that flow through setActorInsideStructure — without
// breaking iteration: an actor moved indoor mid-iteration is skipped
// on its re-check, and newly-outdoor actors after entry are not seen
// by this call (they will be by the next ForEachOutdoorActor on the
// next event). Allocation is O(K) per call; this is intentional to
// avoid exposing range-while-mutating map semantics to callbacks.
func (w *World) ForEachOutdoorActor(fn func(*Actor) bool) {
	ids := make([]ActorID, 0, len(w.outdoorActors))
	for id := range w.outdoorActors {
		ids = append(ids, id)
	}
	for _, id := range ids {
		// Re-check membership: fn from a prior iteration may have moved
		// this actor indoor (e.g. by calling setActorInsideStructure via
		// a command). Skip rather than visit a now-indoor actor.
		if _, ok := w.outdoorActors[id]; !ok {
			continue
		}
		a, ok := w.Actors[id]
		if !ok {
			// Defensive: index drift would only happen if a caller
			// bypassed setActorInsideStructure or removed an actor
			// without unhooking the index. Skip rather than panic.
			continue
		}
		if !fn(a) {
			return
		}
	}
}

// republish builds and atomically swaps a fresh Snapshot. Called from the
// world goroutine after every command.
//
// Per-aggregate snapshot helpers deep-copy each entity so the published
// Snapshot is genuinely immutable from a reader's perspective — readers
// can't reach into world state through a Snapshot pointer to race against
// the world goroutine.
//
// v1 publishes a fresh map per command (cheap allocations). If snapshot
// allocation becomes hot on profiling, the contained replacement is a
// copy-on-write per-entity scheme — same external Snapshot type, lower
// allocation pressure.
func (w *World) republish() {
	snap := &Snapshot{
		AtTick:                   w.TickCounter,
		PublishedAt:              time.Now(),
		Actors:                   make(map[ActorID]*ActorSnapshot, len(w.Actors)),
		Huddles:                  make(map[HuddleID]*Huddle, len(w.Huddles)),
		Scenes:                   make(map[SceneID]*Scene, len(w.Scenes)),
		Structures:               make(map[StructureID]*Structure, len(w.Structures)),
		Orders:                   make(map[OrderID]*Order, len(w.Orders)),
		VillageObjects:           make(map[VillageObjectID]*VillageObject, len(w.VillageObjects)),
		Quotes:                   make(map[QuoteID]*SceneQuote, len(w.Quotes)),
		PayLedger:                make(map[LedgerID]*PayLedgerEntry, len(w.PayLedger)),
		ActionLog:                CloneActionLog(w.ActionLog),
		NoticeboardContent:       make(map[VillageObjectID]*NoticeboardContent, len(w.NoticeboardContent)),
		PriceBook:                ClonePriceBook(w.PriceBook),
		Environment:              w.Environment,
		Phase:                    w.Phase,
		NeedThresholds:           w.Settings.NeedThresholds.Clone(),
		LodgingDefaultWeeklyRate: w.Settings.LodgingDefaultWeeklyRate,
		RestockReorderPct:        w.Settings.RestockReorderPct,
		// Aliased, not cloned — immutable post-startup catalog. See Snapshot.ItemKinds.
		ItemKinds: w.ItemKinds,
	}
	for id, a := range w.Actors {
		snap.Actors[id] = snapshotActor(a, w.TickCounter)
	}
	for id, h := range w.Huddles {
		snap.Huddles[id] = CloneHuddle(h)
	}
	for id, s := range w.Scenes {
		snap.Scenes[id] = CloneScene(s)
	}
	for id, s := range w.Structures {
		snap.Structures[id] = CloneStructure(s)
	}
	for id, o := range w.Orders {
		snap.Orders[id] = CloneOrder(o)
	}
	for id, v := range w.VillageObjects {
		snap.VillageObjects[id] = CloneVillageObject(v)
	}
	for id, q := range w.Quotes {
		snap.Quotes[id] = CloneSceneQuote(q)
	}
	for id, e := range w.PayLedger {
		snap.PayLedger[id] = ClonePayLedgerEntry(e)
	}
	for id, n := range w.NoticeboardContent {
		if n == nil {
			continue
		}
		nc := *n
		snap.NoticeboardContent[id] = &nc
	}
	w.published.Store(snap)
}

// snapshotActor produces an ActorSnapshot — the slim immutable view of an
// actor for consumers.
//
// InventoryHash is a v1 stub (sum of quantities). Future change to a real
// hash (xxhash over sorted kind+qty) is a contained change behind the same
// type.
func snapshotActor(a *Actor, atTick uint64) *ActorSnapshot {
	var hash uint64
	var inventoryCopy map[ItemKind]int
	if len(a.Inventory) > 0 {
		inventoryCopy = make(map[ItemKind]int, len(a.Inventory))
	}
	for k, q := range a.Inventory {
		hash += uint64(q)
		inventoryCopy[k] = q
	}
	needsCopy := make(map[NeedKey]int, len(a.Needs))
	for k, v := range a.Needs {
		needsCopy[k] = v
	}
	// Attribute slugs for the editor chip list — sorted keys only, the param
	// payloads stay on the live Actor (the read surface never needs them).
	var attributeSlugs []string
	if len(a.Attributes) > 0 {
		attributeSlugs = make([]string, 0, len(a.Attributes))
		for slug := range a.Attributes {
			attributeSlugs = append(attributeSlugs, slug)
		}
		sort.Strings(attributeSlugs)
	}
	return &ActorSnapshot{
		AtTick:             atTick,
		DisplayName:        a.DisplayName,
		Kind:               a.Kind,
		State:              a.State,
		Role:               a.Role,
		LLMAgent:           a.LLMAgent,
		LoginUsername:      a.LoginUsername,
		InsideStructureID:  a.InsideStructureID,
		InsideRoomID:       a.InsideRoomID,
		Pos:                a.Pos,
		CurrentHuddleID:    a.CurrentHuddleID,
		SpriteID:           a.SpriteID,
		Facing:             a.Facing,
		AttributeSlugs:     attributeSlugs,
		HomeStructureID:    a.HomeStructureID,
		WorkStructureID:    a.WorkStructureID,
		ScheduleStartMin:   copyIntPtr(a.ScheduleStartMin),
		ScheduleEndMin:     copyIntPtr(a.ScheduleEndMin),
		SocialTag:          a.SocialTag,
		SocialStartMin:     copyIntPtr(a.SocialStartMin),
		SocialEndMin:       copyIntPtr(a.SocialEndMin),
		Needs:              needsCopy,
		InventoryHash:      hash,
		Inventory:          inventoryCopy,
		Coins:              a.Coins,
		Acquaintances:      cloneAcquaintances(a.Acquaintances),
		Relationships:      cloneRelationships(a.Relationships),
		Narrative:          cloneNarrativeState(a.Narrative),
		VisitorState:       cloneVisitorState(a.VisitorState),
		BusinessownerState: cloneBusinessownerState(a.BusinessownerState),
		DwellCredits:       cloneDwellCredits(a.DwellCredits),
		RoomAccess:         cloneRoomAccess(a.RoomAccess),
		RestockPolicy:      a.RestockPolicy,
		TickInFlight:       a.TickInFlight,
		TickAttemptID:      a.TickAttemptID,
	}
}
