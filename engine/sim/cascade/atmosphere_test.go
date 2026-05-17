package cascade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// atmosphere_test.go — driver-side tests for the atmosphere cascade
// slice. The substrate Commands (sim.FetchAtmosphereContext +
// sim.ApplyAtmosphereRefresh) have their own test surface in
// engine/sim/atmosphere_test.go; these tests cover the goroutine
// lifecycle, the prompt construction, and the full sweep cycle end-to-
// end via the FakeClient.

func buildAtmosphereDriverWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() {
		cancel()
		<-done
	}
}

func TestRunOneAtmosphereSweep_HappyPath(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  The mist hangs heavy over the rooftops, and the bells toll thrice.  "},
	})

	runOneAtmosphereSweep(context.Background(), w, client)

	if got := client.CallCount(); got != 1 {
		t.Errorf("LLM call count = %d, want 1", got)
	}
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(reqs))
	}
	if got := reqs[0].Model; got != "salem-generic" {
		t.Errorf("Request.Model = %q, want salem-generic", got)
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("Request.Tools len = %d, want 0 (atmosphere is tool-free)", len(reqs[0].Tools))
	}
	if len(reqs[0].Messages) != 1 || reqs[0].Messages[0].Role != llm.RoleUser {
		t.Errorf("Request.Messages = %+v, want one user message", reqs[0].Messages)
	}

	snap := w.Published()
	if got, want := snap.Environment.Atmosphere, "The mist hangs heavy over the rooftops, and the bells toll thrice."; got != want {
		t.Errorf("Atmosphere = %q, want %q", got, want)
	}
	if snap.Environment.LastAtmosphereRefreshAt.IsZero() {
		t.Error("LastAtmosphereRefreshAt not stamped after happy-path apply")
	}
}

func TestRunOneAtmosphereSweep_EmptyReplyLeavesStateUntouched(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "   \n  "},
	})
	runOneAtmosphereSweep(context.Background(), w, client)

	snap := w.Published()
	if snap.Environment.Atmosphere != "" {
		t.Errorf("Atmosphere = %q, want untouched empty", snap.Environment.Atmosphere)
	}
	if !snap.Environment.LastAtmosphereRefreshAt.IsZero() {
		t.Errorf("LastAtmosphereRefreshAt = %v, want zero (no stamp on empty reply)", snap.Environment.LastAtmosphereRefreshAt)
	}
}

func TestRunOneAtmosphereSweep_LLMErrorLeavesStateUntouched(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Err: &llm.Error{Class: llm.ErrorTransport, Message: "boom"},
	})
	runOneAtmosphereSweep(context.Background(), w, client)

	snap := w.Published()
	if snap.Environment.Atmosphere != "" {
		t.Errorf("Atmosphere = %q, want untouched empty after LLM error", snap.Environment.Atmosphere)
	}
	if !snap.Environment.LastAtmosphereRefreshAt.IsZero() {
		t.Errorf("LastAtmosphereRefreshAt = %v, want zero (no stamp on LLM error)", snap.Environment.LastAtmosphereRefreshAt)
	}

	// Retry posture: a subsequent sweep with a working client succeeds.
	client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "retry-ok"}})
	runOneAtmosphereSweep(context.Background(), w, client)
	snap = w.Published()
	if snap.Environment.Atmosphere != "retry-ok" {
		t.Errorf("after retry: Atmosphere = %q, want retry-ok", snap.Environment.Atmosphere)
	}
}

func TestRunOneAtmosphereSweep_DedupSkipsWrite(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	// Pre-install an atmosphere so the dedup path triggers.
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Environment.Atmosphere = "The mist hangs heavy."
		world.Environment.LastAtmosphereRefreshAt = priorAt
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// LLM emits the same prose (after trim).
	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  The mist hangs heavy.  "},
	})
	runOneAtmosphereSweep(context.Background(), w, client)

	snap := w.Published()
	if snap.Environment.Atmosphere != "The mist hangs heavy." {
		t.Errorf("Atmosphere = %q, want unchanged on dedup", snap.Environment.Atmosphere)
	}
	if !snap.Environment.LastAtmosphereRefreshAt.Equal(priorAt) {
		t.Errorf("LastAtmosphereRefreshAt = %v, want unchanged prior %v on dedup", snap.Environment.LastAtmosphereRefreshAt, priorAt)
	}
}

func TestBuildAtmospherePrompt_StructureAndContent(t *testing.T) {
	c := sim.AtmosphereContext{
		Phase:           sim.PhaseNight,
		Weather:         "drizzle",
		PriorAtmosphere: "the morning mist hangs heavy",
		Roster: []sim.AtmosphereRosterEntry{
			{StructureLabel: "Tavern", DisplayNames: []string{"John Ellis", "Prudence Ward"}},
			{StructureLabel: "", DisplayNames: []string{"Ezekiel Crane"}},
		},
	}
	got := buildAtmospherePrompt(c)

	wantSubstrings := []string{
		"You author the village's current atmosphere",
		"There are no tools available",
		"It is night.",
		"The weather: drizzle.",
		"The previous atmosphere you wrote:",
		"the morning mist hangs heavy",
		"The village right now:",
		"- At the Tavern: John Ellis, Prudence Ward.",
		"- Out in the open: Ezekiel Crane.",
		"biblical in cadence",
		"No preamble, no sign-off",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing substring %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestBuildAtmospherePrompt_FirstFireFraming(t *testing.T) {
	c := sim.AtmosphereContext{Phase: sim.PhaseDay}
	got := buildAtmospherePrompt(c)
	if !strings.Contains(got, "You haven't written the atmosphere before now.") {
		t.Errorf("first-fire framing missing\n--- prompt ---\n%s", got)
	}
	if strings.Contains(got, "The previous atmosphere you wrote") {
		t.Error("first-fire prompt should not include prior-atmosphere section")
	}
	if strings.Contains(got, "The village right now") {
		t.Error("empty-roster prompt should not include roster section")
	}
}

func TestBuildAtmospherePrompt_OmitsEmptyWeather(t *testing.T) {
	c := sim.AtmosphereContext{Phase: sim.PhaseDay, Weather: "   "}
	got := buildAtmospherePrompt(c)
	if strings.Contains(got, "The weather") {
		t.Errorf("whitespace-only weather should be omitted\n--- prompt ---\n%s", got)
	}
}

// TestBuildAtmospherePrompt_SkipsEmptyRosterBuckets pins the
// defensive skip added per code_review R0 finding #4. A bucket with
// no DisplayNames must not produce a malformed "- At the X: ." line.
func TestBuildAtmospherePrompt_SkipsEmptyRosterBuckets(t *testing.T) {
	c := sim.AtmosphereContext{
		Phase: sim.PhaseDay,
		Roster: []sim.AtmosphereRosterEntry{
			{StructureLabel: "Tavern", DisplayNames: nil},
			{StructureLabel: "Smithy", DisplayNames: []string{"Josiah Thorne"}},
			{StructureLabel: "", DisplayNames: []string{}},
		},
	}
	got := buildAtmospherePrompt(c)
	if strings.Contains(got, "At the Tavern: .") {
		t.Errorf("empty-bucket emitted malformed line\n--- prompt ---\n%s", got)
	}
	if strings.Contains(got, "Out in the open: .") {
		t.Errorf("empty outdoor bucket emitted malformed line\n--- prompt ---\n%s", got)
	}
	if !strings.Contains(got, "- At the Smithy: Josiah Thorne.") {
		t.Errorf("non-empty bucket should render normally\n--- prompt ---\n%s", got)
	}
}

func TestRegisterAtmosphere_StampsViaSweep(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	// Tight sweep cadence so the test doesn't wait the 4h default.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.AtmosphereRefreshInterval = 20 * time.Millisecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	client := llm.NewFakeClient()
	for i := 0; i < 20; i++ {
		client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "first-fire prose"}})
	}

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	RegisterAtmosphere(driverCtx, w, client)

	// First sweep is immediate; allow time for the round-trip.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := w.Published()
		if snap.Environment.Atmosphere == "first-fire prose" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("atmosphere did not install within 500ms")
}

func TestRegisterAtmosphere_TickerFiresRepeatedly(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.AtmosphereRefreshInterval = 20 * time.Millisecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	client := llm.NewFakeClient()
	// Pre-seed a stack of distinct responses so each sweep gets a fresh
	// non-dedup prose.
	for i := 0; i < 20; i++ {
		client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "sweep-" + string(rune('A'+i))}})
	}

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	RegisterAtmosphere(driverCtx, w, client)

	// Wait for at least 3 distinct writes (proves the ticker is firing,
	// not just the immediate-first-sweep). Each write bumps the
	// Environment.Atmosphere to the next "sweep-X" string in order.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if client.CallCount() >= 3 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ticker fired only %d times, want >= 3 within 1s", client.CallCount())
}

func TestRegisterAtmosphere_CtxCancelExitsGoroutine(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.AtmosphereRefreshInterval = 20 * time.Millisecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	client := llm.NewFakeClient()
	for i := 0; i < 20; i++ {
		client.Push(llm.ScriptedTurn{Response: llm.Response{Content: "before-cancel"}})
	}

	driverCtx, driverCancel := context.WithCancel(context.Background())
	RegisterAtmosphere(driverCtx, w, client)

	// Wait for the first sweep to land.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if client.CallCount() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if client.CallCount() < 1 {
		t.Fatal("first sweep did not run before cancel")
	}

	// Cancel, drain any in-flight sweep, then sample call count and
	// verify it doesn't grow.
	driverCancel()
	time.Sleep(100 * time.Millisecond)
	stable := client.CallCount()

	time.Sleep(150 * time.Millisecond)
	if client.CallCount() > stable {
		t.Errorf("ticker kept firing after cancel: stable=%d now=%d", stable, client.CallCount())
	}
}

func TestRegisterAtmosphere_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterAtmosphere(nil world) did not panic")
		}
	}()
	RegisterAtmosphere(context.Background(), nil, llm.NewFakeClient())
}

func TestRegisterAtmosphere_PanicsOnNilClient(t *testing.T) {
	w, stop := buildAtmosphereDriverWorld(t)
	defer stop()

	defer func() {
		if recover() == nil {
			t.Error("RegisterAtmosphere(nil client) did not panic")
		}
	}()
	RegisterAtmosphere(context.Background(), w, nil)
}
