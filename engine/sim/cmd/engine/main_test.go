package main

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// main_test.go — lifecycle smoke test for the engine entrypoint. It exercises
// the part that build-checking can't: that run() wires the full runtime, boots
// the world, drives the periodic checkpointer, and on shutdown stops the
// periodic loop, takes a final checkpoint while the world goroutine is still
// alive, and returns cleanly. The shutdown ORDERING is the subtle bit — a
// final checkpoint after the world goroutine stopped would deadlock, and a
// periodic write overlapping the final one would race.
//
// Mem-backed (sim.LoadWorld) with a fake LLM client + a capturing save, so no
// pg or network is involved. A quiet empty world fires no ticks/agent cascades;
// the atmosphere cascade does fire one immediate off-world sweep on boot, which
// calls the fake client and gets a harmless script-exhausted error (logged +
// ignored per atmosphere's failure semantics) — it doesn't touch the checkpoint
// lifecycle this test asserts.

// TestRun_LifecycleAndFinalCheckpoint boots run() against a mem world with a
// fast checkpoint cadence, lets it tick, signals shutdown, and asserts a
// checkpoint was captured (the periodic loop AND/OR the final write) and that
// run() returned.
func TestRun_LifecycleAndFinalCheckpoint(t *testing.T) {
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// Fast cadence so the periodic checkpointer fires within the test window.
	world.Settings.CheckpointInterval = 20 * time.Millisecond

	var mu sync.Mutex
	var saves int
	var last *sim.CheckpointSnapshot
	save := func(_ context.Context, cp *sim.CheckpointSnapshot) error {
		mu.Lock()
		defer mu.Unlock()
		saves++
		last = cp
		return nil
	}

	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(), // atmosphere's boot sweep calls this once → harmless script-exhausted error
		Save:      save,
		TickSink:  nil, // worker pool null-checks the sink
	}

	// Both channels wired even though these lifecycle tests only ever stop
	// gracefully — a nil force channel is a silently disabled select case, and a
	// test that can't inject force is a coverage gap waiting to happen.
	graceful := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(rt, stopSignals{force: make(chan struct{}, 1), graceful: graceful})
	}()

	// Let the world boot and the periodic checkpointer fire at least once.
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	periodicSaves := saves
	mu.Unlock()
	if periodicSaves == 0 {
		t.Error("expected at least one periodic checkpoint before shutdown")
	}

	// Signal shutdown and wait for run() to return.
	graceful <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of shutdown signal")
	}

	// run() must not return until the world goroutine has actually stopped
	// (so the caller can safely tear down the pool). A command sent after
	// return should fail rather than be serviced.
	sctx, scancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer scancel()
	if _, err := world.SendContext(sctx, sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}); err == nil {
		t.Error("world still processing commands after run() returned — run did not wait for world stop")
	}

	// The final checkpoint must have run during shutdown.
	mu.Lock()
	defer mu.Unlock()
	if last == nil {
		t.Fatal("no checkpoint snapshot was ever captured")
	}
	if saves <= periodicSaves {
		t.Errorf("expected a final checkpoint after shutdown (saves=%d, periodic=%d)", saves, periodicSaves)
	}
}

// stubSearcher is a minimal llm.MemorySearcher for registration tests.
// registerTools only needs the searcher to wire the recall tool; it never
// calls SearchMemory during registration, so a no-op satisfies the contract
// without coupling the test to the full llm.Client surface.
type stubSearcher struct{}

func (stubSearcher) SearchMemory(context.Context, string, string, string, int) ([]llm.MemoryHit, error) {
	return nil, nil
}

// stubWriter is a minimal llm.MemoryWriter for registration tests — registerTools
// wires memorize (LLM-356) but never calls it during registration, so no-ops
// satisfy the contract without coupling the test to the full client surface.
type stubWriter struct{}

func (stubWriter) SaveNote(context.Context, string, string, string, string, string) error {
	return nil
}
func (stubWriter) ListNotes(context.Context, string, string) ([]llm.NoteMeta, error) {
	return nil, nil
}
func (stubWriter) DeleteNote(context.Context, string, string) error { return nil }

// TestRegisterTools_RegistersDoneTerminal guards ZBBS-HOME-369: production
// registerTools must register the universal `done` terminal tool. The NPC's
// instructions tell it to end its turn with done, and the harness ends a tick
// on a ClassTerminal dispatch (sim.TickStatusDone) — but production
// registration originally omitted done, so a `done` call errored with
// unknown_tool and the NPC was forced into another tool (typically a walk-off),
// manufacturing goal-thrash. Only the test harnesses registered done, so
// nothing caught the production gap. Assert done is both registered as a
// terminal AND advertised to the model.
func TestRegisterTools_RegistersDoneTerminal(t *testing.T) {
	r := handlers.NewRegistry()
	if err := registerTools(r, stubSearcher{}, stubWriter{}); err != nil {
		t.Fatalf("registerTools: %v", err)
	}

	entry, ok := r.Lookup("done")
	if !ok {
		t.Fatal("registerTools did not register the `done` tool")
	}
	if entry.Class != handlers.ClassTerminal {
		t.Errorf("`done` Class = %s, want terminal", entry.Class)
	}

	var advertised bool
	for _, spec := range r.AdvertisedSpecs() {
		if spec.Name == "done" {
			advertised = true
			break
		}
	}
	if !advertised {
		t.Error("`done` registered but not advertised to the model (AdvertisedSpecs omits it)")
	}
}

// TestRegisterTools_RegistersBareCoinPay guards LLM-99: the bare-coin `pay`
// tool is registered AND advertised to the model. It was pulled in
// ZBBS-HOME-430 back when it was the only coin tool — NPCs reached for it to
// settle purchases and double-charged on buy-then-pay. pay_with_item is now
// the registered purchase path, so bare pay is back for the non-purchase coin
// movement it was always meant for (wages/tips/gifts), which otherwise lands
// as empty speech. pay_with_item must stay registered alongside it as the
// commerce path.
func TestRegisterTools_RegistersBareCoinPay(t *testing.T) {
	r := handlers.NewRegistry()
	if err := registerTools(r, stubSearcher{}, stubWriter{}); err != nil {
		t.Fatalf("registerTools: %v", err)
	}
	if _, ok := r.Lookup("pay"); !ok {
		t.Error("registerTools did not register the bare-coin `pay` tool — re-introduced in LLM-99 for wages/tips/gifts")
	}
	if _, ok := r.Lookup("pay_with_item"); !ok {
		t.Error("registerTools did not register `pay_with_item` — the ledger flow must remain the NPC commerce path")
	}
	// Both must be ADVERTISED, not just registered: the safety argument for
	// re-introducing bare `pay` is that the model sees the dedicated purchase
	// path (pay_with_item) alongside it, so it routes goods/lodging there
	// rather than double-paying with bare coins.
	var advertisedPay, advertisedPayWithItem bool
	for _, spec := range r.AdvertisedSpecs() {
		switch spec.Name {
		case "pay":
			advertisedPay = true
		case "pay_with_item":
			advertisedPayWithItem = true
		}
	}
	if !advertisedPay {
		t.Error("`pay` registered but not advertised to the model (AdvertisedSpecs omits it)")
	}
	if !advertisedPayWithItem {
		t.Error("`pay_with_item` registered but not advertised to the model — the model needs the commerce path alongside bare pay")
	}
}

// TestRegisterTools_CacheStableOrder guards LLM-328: the tools present for
// essentially every stationary, non-laboring actor (the common head) are
// registered — and therefore advertised — BEFORE every situationally-gated
// tool. Registration order is the advertised order (AdvertisedSpecs preserves
// it) and provider prompt-prefix caching only catches up to the first byte
// divergence between two requests, so a gated tool that appears for one actor
// but not another must fall at the TAIL of the tools block to keep the common
// head warm and shared across a burst of co-ticking NPCs. If a future tool is
// added, place it in the right section and extend the lists here.
func TestRegisterTools_CacheStableOrder(t *testing.T) {
	r := handlers.NewRegistry()
	if err := registerTools(r, stubSearcher{}, stubWriter{}); err != nil {
		t.Fatalf("registerTools: %v", err)
	}
	idx := map[string]int{}
	for i, spec := range r.AdvertisedSpecs() {
		idx[spec.Name] = i
	}

	commonHead := []string{"speak", "move_to", "consume", "pay", "sell", "offer_trade", "pay_with_item", "give"}
	// The situationally-gated tools (gateTools turns each on only for some
	// actors). `done`/`recall` are excluded — done is the terminal kept last by
	// convention, recall is registered separately; neither is part of the
	// cross-actor common/situational cache seam.
	situationalTail := []string{
		"gather", "produce", "repair", "stoke", "bake", "turn_in", "take_break", "stay_open", "deliver_order", "stop",
		"solicit_work", "offer_work", "accept_work", "decline_work",
		"accept_pay", "decline_pay", "counter_pay", "withdraw_pay",
		"accept_gift", "decline_gift", "summon",
	}

	maxCommon, maxCommonName := -1, ""
	for _, n := range commonHead {
		i, ok := idx[n]
		if !ok {
			t.Fatalf("common-head tool %q not advertised", n)
		}
		if i > maxCommon {
			maxCommon, maxCommonName = i, n
		}
	}
	for _, n := range situationalTail {
		i, ok := idx[n]
		if !ok {
			t.Fatalf("situational-tail tool %q not advertised", n)
		}
		if i < maxCommon {
			t.Errorf("situational tool %q at index %d precedes common-head tool %q at index %d — "+
				"a per-actor gate on %q would truncate the shared cacheable prefix (LLM-328)", n, i, maxCommonName, maxCommon, n)
		}
	}
}

// TestRegisterTools_AdvertisedToolNamesExact pins the FULL advertised tool
// sequence (LLM-328). The section-order test above proves common-before-
// situational but is blind to a dropped, duplicated, or newly-scattered tool;
// this asserts the exact list so any of those fails loudly and the reorder's
// "same tools, only order changed" claim stays honest. `recall` is registered
// inside registerTools (it needs the searcher) so it appears last.
func TestRegisterTools_AdvertisedToolNamesExact(t *testing.T) {
	r := handlers.NewRegistry()
	if err := registerTools(r, stubSearcher{}, stubWriter{}); err != nil {
		t.Fatalf("registerTools: %v", err)
	}

	var got []string
	seen := map[string]bool{}
	for _, spec := range r.AdvertisedSpecs() {
		if seen[spec.Name] {
			t.Fatalf("duplicate advertised tool %q", spec.Name)
		}
		seen[spec.Name] = true
		got = append(got, spec.Name)
	}

	want := []string{
		"speak", "move_to", "consume", "pay_with_item", "pay", "sell", "offer_trade", "give",
		"gather", "produce", "repair", "stoke", "bake", "turn_in", "take_break", "stay_open", "deliver_order", "stop",
		"solicit_work", "offer_work", "accept_work", "decline_work",
		"accept_pay", "decline_pay", "counter_pay", "withdraw_pay",
		"accept_gift", "decline_gift", "summon", "done", "recall", "memorize",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("advertised tool sequence drifted (LLM-328):\ngot  %v\nwant %v", got, want)
	}
}

// advertisedNameSet returns the sorted advertised tool names of r.
func advertisedNameSet(t *testing.T, r *handlers.Registry) []string {
	t.Helper()
	var names []string
	for _, spec := range r.AdvertisedSpecs() {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}

// registerAll runs each registrar into r, failing the test on the first error.
func registerAll(t *testing.T, r *handlers.Registry, fns ...func(*handlers.Registry) error) {
	t.Helper()
	for _, fn := range fns {
		if err := fn(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
}

// TestRegisterTools_FamilyMembership guards the LLM-328 reorder's one
// structural risk: production now bypasses the family composites
// (RegisterPayWithItemFamily / …Give / …Labor) for the granular registrars so
// each member can land in its cache-appropriate section. That is only safe if
// the granular set is EXACTLY the composite's set — if a composite ever wires
// an extra tool (or shared setup that mints one), the granular path would drop
// it silently. Assert composite == granular membership for each split family.
func TestRegisterTools_FamilyMembership(t *testing.T) {
	cases := []struct {
		name     string
		family   func(*handlers.Registry) error
		granular []func(*handlers.Registry) error
	}{
		{
			"pay_with_item",
			handlers.RegisterPayWithItemFamily,
			[]func(*handlers.Registry) error{
				handlers.RegisterPayWithItem, handlers.RegisterAcceptPay, handlers.RegisterDeclinePay,
				handlers.RegisterCounterPay, handlers.RegisterWithdrawPay,
			},
		},
		{
			"give",
			handlers.RegisterGiveFamily,
			[]func(*handlers.Registry) error{
				handlers.RegisterGive, handlers.RegisterAcceptGift, handlers.RegisterDeclineGift,
			},
		},
		{
			"labor",
			handlers.RegisterLaborFamily,
			[]func(*handlers.Registry) error{
				handlers.RegisterSolicitWork, handlers.RegisterOfferWork,
				handlers.RegisterAcceptWork, handlers.RegisterDeclineWork,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			composite := handlers.NewRegistry()
			registerAll(t, composite, tc.family)

			granular := handlers.NewRegistry()
			registerAll(t, granular, tc.granular...)

			c, g := advertisedNameSet(t, composite), advertisedNameSet(t, granular)
			if !reflect.DeepEqual(c, g) {
				t.Fatalf("%s family membership drifted from granular registrars (LLM-328):\ncomposite %v\ngranular  %v", tc.name, c, g)
			}
		})
	}
}

// TestRun_WiresOffWorldCascades proves run() reaches the off-world LLM cascade
// set (RegisterProductionCascades wires atmosphere / consolidation / narrative
// consolidation / noticeboard + the ActionLog substrate) into the live runtime
// — the seam build-checking can't catch. Atmosphere is the witness: its
// immediate first sweep calls the LLM unconditionally (world-level, not
// candidate-gated like the consolidations, which make no call on an empty
// world). So if RegisterProductionCascades weren't reached, Environment.
// Atmosphere would stay empty. We script one atmosphere line and assert it gets
// installed after boot.
func TestRun_WiresOffWorldCascades(t *testing.T) {
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	world.Settings.CheckpointInterval = 20 * time.Millisecond

	const wantAtmosphere = "The village lies still beneath a watchful sky."
	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: wantAtmosphere}}),
		Save:      func(context.Context, *sim.CheckpointSnapshot) error { return nil },
		TickSink:  nil,
	}

	// Both channels wired even though these lifecycle tests only ever stop
	// gracefully — a nil force channel is a silently disabled select case, and a
	// test that can't inject force is a coverage gap waiting to happen.
	graceful := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(rt, stopSignals{force: make(chan struct{}, 1), graceful: graceful})
	}()

	// The immediate atmosphere sweep applies async (via SendContext) once Run
	// starts, so poll the world for the installed prose rather than racing it.
	var got string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, sendErr := world.SendContext(context.Background(), sim.Command{Fn: func(w *sim.World) (any, error) {
			return w.Environment.Atmosphere, nil
		}})
		if sendErr == nil {
			if s, _ := res.(string); s != "" {
				got = s
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	graceful <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of shutdown signal")
	}

	if got != wantAtmosphere {
		t.Errorf("Environment.Atmosphere = %q, want %q (RegisterAtmosphere not wired into run()?)", got, wantAtmosphere)
	}
}

// TestRun_WiresStormCascade proves run() reaches RegisterStorm (LLM-117) by
// witnessing its boot behavior: the storm sweep's first act is SeedWeatherClear,
// which forces a (simulated persisted) mid-storm weather back to clear. We seed
// Environment.Weather = "storm" before boot and assert it becomes "clear" after
// run() starts — if RegisterStorm weren't wired, the seeded "storm" would
// persist.
func TestRun_WiresStormCascade(t *testing.T) {
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	world.Settings.CheckpointInterval = 20 * time.Millisecond
	// Simulate a mid-storm weather loaded from world_state. Safe to mutate
	// directly — the world goroutine hasn't started.
	world.Environment.Weather = sim.WeatherStorm

	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(),
		Save:      func(context.Context, *sim.CheckpointSnapshot) error { return nil },
		TickSink:  nil,
	}

	// Both channels wired even though these lifecycle tests only ever stop
	// gracefully — a nil force channel is a silently disabled select case, and a
	// test that can't inject force is a coverage gap waiting to happen.
	graceful := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(rt, stopSignals{force: make(chan struct{}, 1), graceful: graceful})
	}()

	// The boot SeedWeatherClear applies async once Run starts; poll for clear.
	var got string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, sendErr := world.SendContext(context.Background(), sim.Command{Fn: func(w *sim.World) (any, error) {
			return w.Environment.Weather, nil
		}})
		if sendErr == nil {
			if s, _ := res.(string); s == sim.WeatherClear {
				got = s
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	graceful <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of shutdown signal")
	}

	if got != sim.WeatherClear {
		t.Errorf("Environment.Weather = %q, want %q (RegisterStorm not wired into run()?)", got, sim.WeatherClear)
	}
}

// TestTerminalToolsMatchPerceptionInvariantList pins the set of tools whose
// success ends the tick against the literal list the LLM-350 perception invariant
// (perception.TestGoldensNoCueInstructsTwoTerminalVerbs, via its terminalToolNames
// slice) scans rendered cues for.
//
// The invariant lives in package perception, which cannot import handlers —
// handlers imports perception — so it cannot read the registry it is really about.
// This test closes the loop from the side that can. Add a terminal tool without
// telling the invariant about it and the new tool's cues go unchecked; this test
// fails first, naming the file to edit.
//
// `done` is excluded: it is ClassTerminal, not a TerminalOnSuccess commit, and no
// cue pairs it with a speak (it IS the end-of-turn verb).
func TestTerminalToolsMatchPerceptionInvariantList(t *testing.T) {
	// Keep sorted; mirrors perception/golden_test.go's terminalToolNames.
	wantTerminal := []string{
		"accept_pay", "accept_work", "bake", "counter_pay", "decline_pay", "decline_work",
		"gather", "move_to", "offer_trade", "offer_work", "pay_with_item", "repair",
		"sell", "solicit_work", "speak", "stoke", "stop", "summon", "turn_in",
		"withdraw_pay",
	}
	// The comparison below sorts only `got`, so an out-of-order insertion here would
	// fail with a confusing diff rather than an honest one (code_review).
	if !sort.StringsAreSorted(wantTerminal) {
		t.Fatalf("wantTerminal must stay sorted; got %v", wantTerminal)
	}

	r := handlers.NewRegistry()
	if err := registerTools(r, stubSearcher{}, stubWriter{}); err != nil {
		t.Fatalf("registerTools: %v", err)
	}
	var got []string
	for _, spec := range r.AdvertisedSpecs() {
		entry, ok := r.Lookup(spec.Name)
		if !ok {
			t.Fatalf("advertised tool %q has no registry entry", spec.Name)
		}
		if entry.Class == handlers.ClassCommit && entry.TerminalPolicy == handlers.TerminalOnSuccess {
			got = append(got, spec.Name)
		}
	}
	sort.Strings(got)

	if !slices.Equal(got, wantTerminal) {
		t.Errorf("terminal-on-success tools drifted from the LLM-350 perception invariant list.\n"+
			" registry: %v\n perception/golden_test.go terminalToolNames: %v\n"+
			"Update terminalToolNames in engine/sim/perception/golden_test.go to match, so the "+
			"two-terminal-verb invariant keeps checking every cue that names one.", got, wantTerminal)
	}
}
