package sim

import (
	"sort"
	"sync"
	"time"
)

// ticker_health.go — liveness tracking for the engine's interval goroutines
// (ZBBS-WORK-334). Each ticker/sweep started by startTickers beats
// once per fire into a shared registry; the umbilical surfaces last-fire + count
// per ticker. The signal this exists for: a ticker goroutine that died or wedged
// stops beating, so a stale LastFire (or a count that stops advancing) is the
// only evidence a remote operator has that a cadence driver silently stopped —
// the engine keeps serving but, say, needs stop decaying or shifts stop firing.
//
// Distinct from the reactor telemetry ring (per-TICK deliberation records): that
// answers "are NPCs deliberating", this answers "are the cadence goroutines that
// FEED deliberation still alive". The reactor evaluator beats here too, so the
// view is one complete list even though its liveness is also inferable from the
// telemetry flow.
//
// THE CADENCE CONTRACT (LLM-395). A beat alone cannot be judged: "no beat in N
// minutes" is meaningless without knowing what N should be for THIS ticker, and
// the cadences span four orders of magnitude (the reactor evaluator at 250ms,
// the atmosphere cascade at 4h). So a ticker also DECLARES its expected interval
// via RegisterTicker, and the declaration is what makes an automated staleness
// alarm possible (see IsStale below, and httpapi's ticker_stale alarm). Before
// this, /ticker-health could only dump raw last-fire times and leave the
// judgement to an operator who happened to know every cadence from memory.
//
// Registration is OPT-IN and FAIL-SAFE: a ticker that beats without ever
// registering a cadence is listed (Registered=false) but never alarms. A beat
// site nobody has taught its cadence to must not be able to cry wolf. The cost
// of that safety is that a forgotten registration silently un-covers a ticker,
// so cmd/engine's ticker-coverage test asserts every beat name in the tree is
// registered — the guard that keeps the fail-safe from becoming a blind spot.
//
// Registration happens BEFORE the goroutine is launched (cmd/engine's
// startTickers registers each ticker ahead of its `go`, and each cascade
// RegisterX registers ahead of its own). That ordering is the whole point: if
// registration lived inside the goroutine body, a goroutine that never started
// would also never register, and the fail-safe above would render precisely the
// worst failure — a cadence driver that never came up at all — invisible. A
// registered ticker that has never fired IS stale once its deadline passes,
// measured from RegisteredAt.
//
// Concurrent-safe: beats arrive from many independent ticker goroutines; reads
// come from the umbilical's HTTP goroutine. A single mutex guards the map — the
// beat is a map lookup + two field writes at cadence intervals (seconds to
// minutes), so contention is nil.

// tickerStaleIntervalMultiplier is how many expected intervals a ticker may
// miss before it is judged stale.
//
// Three, not one, because THE BEAT IS AT THE TOP OF THE TICK BRANCH AND THE
// WORK RUNS IN-BAND RIGHT AFTER IT:
//
//	case <-ticker.C:
//	    w.BeatTicker("atmosphere")
//	    runOneAtmosphereSweep(ctx, w, client)   // LLM calls — minutes, not ms
//
// so the OBSERVED beat interval is the declared interval plus however long that
// sweep body takes. A "one missed fire plus slack" rule would leave the
// LLM-bearing cascades — which are exactly the slow-cadence ones — with only a
// few minutes of headroom before they cried wolf. Three intervals tolerates a
// sweep body running up to 2x its own cadence.
//
// The detection latency this costs is close to free: a DEAD ticker stays dead,
// so noticing an hourly one in 3h rather than 66m changes nothing, while a false
// alarm on a surface whose whole value is that it only ever screams about real
// emergencies (see httpapi/alarms.go) is expensive.
const tickerStaleIntervalMultiplier = 3

// TickerStaleGraceFloor is the minimum staleness deadline, whatever the declared
// cadence. It absorbs two things a pure multiple of a sub-second interval cannot:
//
//   - Scheduler noise. Three times the reactor evaluator's 250ms cadence is
//     750ms — a GC pause or a briefly-saturated world command queue would trip it.
//   - The boot gap. Cascade tickers register in RegisterProductionCascades, which
//     runs BEFORE the world command loop starts (cmd/engine wires subscribers,
//     then launches World.Run). A cascade goroutine therefore cannot beat until
//     the world is up, but its RegisteredAt — the never-fired staleness baseline —
//     is already ticking. The floor covers that startup window.
const TickerStaleGraceFloor = 2 * time.Minute

// TickerHealth records the declared cadence, last-fire time, and cumulative fire
// count for each named ticker/sweep goroutine.
type TickerHealth struct {
	mu      sync.Mutex
	entries map[string]*tickerStat
}

type tickerStat struct {
	count        uint64
	lastFire     time.Time
	registered   bool
	registeredAt time.Time
	interval     time.Duration
}

// newTickerHealth returns an initialized registry. Called from NewWorld so every
// World (production and test) has a non-nil registry.
func newTickerHealth() *TickerHealth {
	return &TickerHealth{entries: make(map[string]*tickerStat)}
}

// register declares the named ticker's expected cadence, stamping RegisteredAt
// on first declaration.
//
// Idempotent, and deliberately NON-DESTRUCTIVE on re-declaration: it overwrites
// the interval and nothing else. Re-registration is a real path — the AfterFunc
// sweep chains re-declare on every re-arm, because their cadence is live-tunable
// from WorldSettings and the re-arm is the moment the new value takes effect. If
// re-registering also reset lastFire or registeredAt, a BROKEN chain could hide
// from the staleness alarm forever simply by continuing to re-register, which is
// exactly the failure this is meant to catch. Count and lastFire belong to the
// beat; only the beat may move them.
func (h *TickerHealth) register(name string, interval time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.entries[name]
	if s == nil {
		s = &tickerStat{}
		h.entries[name] = s
	}
	if !s.registered {
		s.registered = true
		s.registeredAt = time.Now().UTC()
	}
	s.interval = interval
}

// beat records one fire of the named ticker at the current wall-clock time.
// Nil-safe: a registry-less World (a hand-built test fixture that bypassed
// NewWorld) silently skips, so instrumentation never panics a caller.
//
// A beat for a name that never registered still creates the entry — an
// unregistered ticker must remain VISIBLE on /ticker-health (as
// Registered=false) rather than being dropped on the floor, so a forgotten
// registration shows up as a hole in the coverage instead of as nothing at all.
func (h *TickerHealth) beat(name string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.entries[name]
	if s == nil {
		s = &tickerStat{}
		h.entries[name] = s
	}
	s.count++
	s.lastFire = time.Now().UTC()
}

// TickerHealthEntry is one ticker's health snapshot, exported for the umbilical
// DTO and the staleness alarm.
//
// LastFire is the wall-clock time of the most recent beat, and is the ZERO VALUE
// for a ticker that registered but has not yet fired — which, since LLM-395
// registers ahead of the goroutine launch, now genuinely happens (during the
// first interval of a healthy boot, and forever for a goroutine that never
// started).
type TickerHealthEntry struct {
	Name         string
	Count        uint64
	LastFire     time.Time
	Registered   bool
	RegisteredAt time.Time
	Interval     time.Duration
}

// AlarmEligible reports whether this entry carries enough of a cadence contract
// to be judged automatically.
//
// Both halves are fail-safe outs, not oversights:
//
//   - !Registered — a beat site whose cadence nobody declared. Never alarms.
//   - Interval <= 0 — a registration that declared no meaningful cadence. Treated
//     as "do not judge me" rather than as "expected to beat instantly", which is
//     what a naive multiply would make of a zero.
func (e TickerHealthEntry) AlarmEligible() bool {
	return e.Registered && e.Interval > 0
}

// staleDeadline is how long this ticker may go silent before it is stale.
func (e TickerHealthEntry) staleDeadline() time.Duration {
	d := tickerStaleIntervalMultiplier * e.Interval
	if d < TickerStaleGraceFloor {
		d = TickerStaleGraceFloor
	}
	return d
}

// StaleSince returns the instant at which this ticker becomes (or became) stale.
// Zero when the entry is not alarm-eligible, or when it carries no baseline to
// measure from at all.
//
// Derived purely from recorded state — last fire, or the registration stamp for
// a ticker that has never fired — so it is STABLE across evaluations. The alarm
// evaluator holds no state of its own (see httpapi/alarms.go), so "since when"
// must come from here rather than from whenever some HTTP request first noticed.
func (e TickerHealthEntry) StaleSince() time.Time {
	if !e.AlarmEligible() {
		return time.Time{}
	}
	baseline := e.LastFire
	if baseline.IsZero() {
		baseline = e.RegisteredAt
	}
	if baseline.IsZero() {
		return time.Time{}
	}
	return baseline.Add(e.staleDeadline())
}

// IsStale reports whether this ticker has missed its declared cadence as of now.
func (e TickerHealthEntry) IsStale(now time.Time) bool {
	since := e.StaleSince()
	if since.IsZero() {
		return false
	}
	return now.After(since)
}

// Snapshot returns every recorded ticker, sorted by name — a stable, copy-out
// view safe to read off the world goroutine. Values are copied out under the
// mutex and the lock released before the caller does anything with them (JSON
// encoding, alarm prose), so the registry is never held across real work.
func (h *TickerHealth) Snapshot() []TickerHealthEntry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]TickerHealthEntry, 0, len(h.entries))
	for name, s := range h.entries {
		out = append(out, TickerHealthEntry{
			Name:         name,
			Count:        s.count,
			LastFire:     s.lastFire,
			Registered:   s.registered,
			RegisteredAt: s.registeredAt,
			Interval:     s.interval,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// beatTicker records one fire of the named ticker into the world's health
// registry. Called from each interval goroutine started by startTickers (the
// uniform t.C loops beat at the top of their tick branch; the AfterFunc-driven
// chains beat in their fireScheduledX callback). Nil-safe via TickerHealth.beat.
func (w *World) beatTicker(name string) {
	w.tickerHealth.beat(name)
}

// BeatTicker is the exported beat entry for ticker goroutines defined OUTSIDE
// package sim — specifically the ticker-driven cascades in package cascade
// (atmosphere, consolidation, narrative_consolidation, idle_backstop, visitor,
// action_log), which fold their cadence liveness into this same registry. They
// can't reach the unexported beatTicker; both land in the one TickerHealth
// registry so the umbilical ticker-health view is a single unified list across
// the sim-package tickers and the cascade-package ones. Nil-safe via beat.
func (w *World) BeatTicker(name string) {
	w.tickerHealth.beat(name)
}

// RegisterTicker declares a ticker's expected cadence, opting it in to the
// staleness alarm. Call it BEFORE launching the goroutine that beats the name —
// registering from inside the goroutine body would make a never-started ticker
// invisible (see the file header).
//
// Exported for both callers: cmd/engine's startTickers (the sim-package
// tickers) and the cascade package's RegisterX helpers. Safe to call repeatedly
// — the AfterFunc sweep chains re-declare their live-tunable cadence on every
// re-arm, which only ever overwrites the interval.
//
// An interval of zero or less means "listed, but do not judge" — see
// TickerHealthEntry.AlarmEligible.
func (w *World) RegisterTicker(name string, interval time.Duration) {
	w.tickerHealth.register(name, interval)
}

// TickerHealthSnapshot returns the current per-ticker liveness view. Read by the
// umbilical ticker-health route and by the ticker_stale alarm evaluator; safe to
// call from any goroutine.
func (w *World) TickerHealthSnapshot() []TickerHealthEntry {
	return w.tickerHealth.Snapshot()
}
