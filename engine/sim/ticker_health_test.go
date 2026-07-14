package sim

import (
	"sync"
	"testing"
	"time"
)

// ticker_health_test.go — registry-internal coverage (package sim, so it can
// reach the unexported beat / newTickerHealth / w.beatTicker). The HTTP contract
// is covered separately in httpapi.

func TestTickerHealth_BeatAndSnapshot(t *testing.T) {
	h := newTickerHealth()
	h.beat("needs")
	h.beat("needs")
	h.beat("shift")

	got := h.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2", len(got))
	}
	// Sorted by name: needs before shift.
	if got[0].Name != "needs" || got[1].Name != "shift" {
		t.Errorf("snapshot not sorted by name: %+v", got)
	}
	if got[0].Count != 2 {
		t.Errorf("needs count=%d, want 2", got[0].Count)
	}
	if got[1].Count != 1 {
		t.Errorf("shift count=%d, want 1", got[1].Count)
	}
	if got[0].LastFire.IsZero() {
		t.Error("needs LastFire is zero after a beat")
	}
}

func TestTickerHealth_NilSafe(t *testing.T) {
	var h *TickerHealth
	// A nil registry must not panic on beat (a hand-built world that bypassed
	// NewWorld) and returns no entries.
	h.beat("anything")
	if got := h.Snapshot(); got != nil {
		t.Errorf("nil registry Snapshot = %v, want nil", got)
	}
}

func TestTickerHealth_ConcurrentBeats(t *testing.T) {
	h := newTickerHealth()
	const goroutines, perG = 8, 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				h.beat("needs")
			}
		}()
	}
	wg.Wait()

	got := h.Snapshot()
	if len(got) != 1 || got[0].Name != "needs" {
		t.Fatalf("snapshot=%+v, want one 'needs' entry", got)
	}
	if got[0].Count != goroutines*perG {
		t.Errorf("count=%d, want %d (lost beats under concurrency)", got[0].Count, goroutines*perG)
	}
}

func TestWorld_BeatTickerRoutesToRegistry(t *testing.T) {
	w := NewWorld(Repository{})
	w.beatTicker("phase")           // in-package sim ticker path
	w.BeatTicker("atmosphere")      // exported path (cascade-package tickers)
	w.BeatTicker("atmosphere")      //
	got := w.TickerHealthSnapshot() //
	if len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2 (phase + atmosphere)", len(got))
	}
	// Sorted by name: atmosphere before phase.
	if got[0].Name != "atmosphere" || got[0].Count != 2 {
		t.Errorf("atmosphere entry = %+v, want count=2 (exported BeatTicker)", got[0])
	}
	if got[1].Name != "phase" || got[1].Count != 1 {
		t.Errorf("phase entry = %+v, want count=1 (unexported beatTicker)", got[1])
	}
}

// --- cadence contract (LLM-395) ---

// entry is a hand-built snapshot row. The staleness rule is pure arithmetic over
// recorded state, so the tests drive it directly rather than sleeping through real
// cadences.
func entry(name string, interval, sinceLastFire time.Duration, now time.Time) TickerHealthEntry {
	return TickerHealthEntry{
		Name:         name,
		Count:        1,
		LastFire:     now.Add(-sinceLastFire),
		Registered:   true,
		RegisteredAt: now.Add(-24 * time.Hour),
		Interval:     interval,
	}
}

func TestTickerHealth_RegisterDeclaresCadence(t *testing.T) {
	h := newTickerHealth()
	h.register("needs", time.Minute)

	got := h.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot len=%d, want 1", len(got))
	}
	if !got[0].Registered {
		t.Error("Registered=false after register()")
	}
	if got[0].Interval != time.Minute {
		t.Errorf("Interval=%v, want 1m", got[0].Interval)
	}
	if got[0].RegisteredAt.IsZero() {
		t.Error("RegisteredAt is zero after register()")
	}
	// Registration alone is not a beat.
	if got[0].Count != 0 || !got[0].LastFire.IsZero() {
		t.Errorf("register() moved beat state: count=%d lastFire=%v", got[0].Count, got[0].LastFire)
	}
}

// Re-registration is the live-tuning path (the AfterFunc chains re-declare on
// every re-arm). It must update the interval and NOTHING else — if it reset the
// staleness baseline, a chain that is broken but still somehow re-registering
// could hide from the alarm forever.
func TestTickerHealth_ReregisterUpdatesIntervalOnlyAndNeverResetsBaseline(t *testing.T) {
	h := newTickerHealth()
	h.register("order_sweep", 30*time.Second)
	h.beat("order_sweep")
	h.beat("order_sweep")

	before := h.Snapshot()[0]
	time.Sleep(2 * time.Millisecond) // ensure a re-stamped RegisteredAt would differ

	h.register("order_sweep", 5*time.Minute) // operator retuned the cadence

	after := h.Snapshot()[0]
	if after.Interval != 5*time.Minute {
		t.Errorf("Interval=%v, want the retuned 5m", after.Interval)
	}
	if !after.RegisteredAt.Equal(before.RegisteredAt) {
		t.Errorf("RegisteredAt moved on re-register: %v -> %v", before.RegisteredAt, after.RegisteredAt)
	}
	if !after.LastFire.Equal(before.LastFire) {
		t.Errorf("LastFire moved on re-register: %v -> %v", before.LastFire, after.LastFire)
	}
	if after.Count != before.Count {
		t.Errorf("Count moved on re-register: %d -> %d", before.Count, after.Count)
	}
}

// A beat for a name that never registered still creates a VISIBLE entry — a
// forgotten registration must read as a hole in the coverage, not as nothing.
func TestTickerHealth_BeatWithoutRegisterIsVisibleButNeverAlarms(t *testing.T) {
	h := newTickerHealth()
	h.beat("mystery_ticker")

	got := h.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot len=%d, want the unregistered ticker to still be listed", len(got))
	}
	if got[0].Registered {
		t.Error("Registered=true for a ticker that only ever beat")
	}
	if got[0].AlarmEligible() {
		t.Error("an unregistered ticker is alarm-eligible — the fail-safe is broken")
	}
	// However long it has been silent, it cannot cry wolf.
	if got[0].IsStale(time.Now().Add(365 * 24 * time.Hour)) {
		t.Error("an unregistered ticker went stale — the fail-safe is broken")
	}
}

func TestTickerHealthEntry_IsStale(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name  string
		e     TickerHealthEntry
		stale bool
		why   string
	}{
		{
			name:  "fresh beat well inside cadence",
			e:     entry("needs", time.Minute, 10*time.Second, now),
			stale: false,
			why:   "a ticker beating on time is healthy",
		},
		{
			name:  "one missed fire is tolerated",
			e:     entry("needs", time.Minute, 90*time.Second, now),
			stale: false,
			why:   "the multiplier tolerates in-band work overrunning the cadence",
		},
		{
			name:  "past 3x cadence",
			e:     entry("needs", time.Minute, 4*time.Minute, now),
			stale: true,
			why:   "3 x 1m = 3m deadline, exceeded",
		},
		{
			name:  "sub-second cadence silent for 30s is NOT stale",
			e:     entry("reactor", 250*time.Millisecond, 30*time.Second, now),
			stale: false,
			why:   "3 x 250ms = 750ms, but the 2m grace floor dominates — a GC pause must not alarm",
		},
		{
			name:  "sub-second cadence past the grace floor",
			e:     entry("reactor", 250*time.Millisecond, 3*time.Minute, now),
			stale: true,
			why:   "past the floor, a dead 250ms chain is unambiguous",
		},
		{
			name:  "hour cadence silent for 2h is NOT stale",
			e:     entry("atmosphere", time.Hour, 2*time.Hour, now),
			stale: false,
			why:   "an LLM-bearing sweep body may overrun; 3h is the deadline",
		},
		{
			name:  "hour cadence silent for 4h",
			e:     entry("atmosphere", time.Hour, 4*time.Hour, now),
			stale: true,
			why:   "past 3 x 1h",
		},
		{
			name: "registered but never fired, inside deadline",
			e: TickerHealthEntry{
				Name: "needs", Registered: true, Interval: time.Minute,
				RegisteredAt: now.Add(-90 * time.Second),
			},
			stale: false,
			why:   "a healthy boot has not missed its deadline yet",
		},
		{
			name: "registered but never fired, past deadline",
			e: TickerHealthEntry{
				Name: "needs", Registered: true, Interval: time.Minute,
				RegisteredAt: now.Add(-10 * time.Minute),
			},
			stale: true,
			why:   "THE never-started goroutine — the failure the beat-only registry could not see",
		},
		{
			name: "non-positive interval never alarms",
			e: TickerHealthEntry{
				Name: "weird", Registered: true, Interval: 0,
				RegisteredAt: now.Add(-72 * time.Hour),
			},
			stale: false,
			why:   "a zero interval means 'do not judge me', not 'must beat instantly'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.IsStale(now); got != tc.stale {
				t.Errorf("IsStale=%v, want %v — %s", got, tc.stale, tc.why)
			}
		})
	}
}

// StaleSince must be derived from recorded state, not from when someone looked,
// so the alarm evaluator can stay stateless and still report a stable "since".
func TestTickerHealthEntry_StaleSinceIsDerivedFromRecordedState(t *testing.T) {
	now := time.Now().UTC()
	e := entry("needs", time.Minute, 10*time.Minute, now)

	want := e.LastFire.Add(3 * time.Minute) // 3 x 1m cadence
	if got := e.StaleSince(); !got.Equal(want) {
		t.Errorf("StaleSince=%v, want lastFire+3m=%v", got, want)
	}
	// Evaluating again later must not move it.
	if got := e.StaleSince(); !got.Equal(want) {
		t.Errorf("StaleSince moved between evaluations: %v", got)
	}
	// An ineligible entry has no staleness instant at all.
	var unreg TickerHealthEntry
	if got := unreg.StaleSince(); !got.IsZero() {
		t.Errorf("StaleSince=%v for an unregistered entry, want zero", got)
	}
}

func TestWorld_RegisterTickerRoutesToRegistry(t *testing.T) {
	w := NewWorld(Repository{})
	w.RegisterTicker("storm", 30*time.Second)

	got := w.TickerHealthSnapshot()
	if len(got) != 1 || got[0].Name != "storm" {
		t.Fatalf("snapshot=%+v, want one 'storm' entry", got)
	}
	if !got[0].Registered || got[0].Interval != 30*time.Second {
		t.Errorf("entry=%+v, want registered with a 30s cadence", got[0])
	}
}
