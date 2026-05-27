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
// Concurrent-safe: beats arrive from many independent ticker goroutines; reads
// come from the umbilical's HTTP goroutine. A single mutex guards the map — the
// beat is a map lookup + two field writes at cadence intervals (seconds to
// minutes), so contention is nil.

// TickerHealth records the last-fire time and cumulative fire count for each
// named ticker/sweep goroutine.
type TickerHealth struct {
	mu      sync.Mutex
	entries map[string]*tickerStat
}

type tickerStat struct {
	count    uint64
	lastFire time.Time
}

// newTickerHealth returns an initialized registry. Called from NewWorld so every
// World (production and test) has a non-nil registry.
func newTickerHealth() *TickerHealth {
	return &TickerHealth{entries: make(map[string]*tickerStat)}
}

// beat records one fire of the named ticker at the current wall-clock time.
// Nil-safe: a registry-less World (a hand-built test fixture that bypassed
// NewWorld) silently skips, so instrumentation never panics a caller.
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
// DTO. LastFire is the wall-clock time of the most recent beat (zero value if a
// ticker registered but somehow never fired — not currently possible, since a
// name only appears once it has beaten).
type TickerHealthEntry struct {
	Name     string
	Count    uint64
	LastFire time.Time
}

// Snapshot returns every recorded ticker, sorted by name — a stable, copy-out
// view safe to read off the world goroutine.
func (h *TickerHealth) Snapshot() []TickerHealthEntry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]TickerHealthEntry, 0, len(h.entries))
	for name, s := range h.entries {
		out = append(out, TickerHealthEntry{Name: name, Count: s.count, LastFire: s.lastFire})
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

// TickerHealthSnapshot returns the current per-ticker liveness view. Read by the
// umbilical ticker-health route; safe to call from any goroutine.
func (w *World) TickerHealthSnapshot() []TickerHealthEntry {
	return w.tickerHealth.Snapshot()
}
