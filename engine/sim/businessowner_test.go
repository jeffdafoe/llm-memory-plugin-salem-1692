package sim_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestBusinessownerFlavorsExhaustive verifies BusinessownerFlavors returns
// every configured flavor in stable order. The package's init() guards
// pool exhaustiveness at load; this test catches accidental flavor
// removal that wouldn't otherwise panic at init.
func TestBusinessownerFlavorsExhaustive(t *testing.T) {
	flavors := sim.BusinessownerFlavors()
	if len(flavors) < 2 {
		t.Fatalf("BusinessownerFlavors() returned %d, want at least 2 (flamboyant + reserved)", len(flavors))
	}
	wantBoth := map[string]bool{"flamboyant": false, "reserved": false}
	for _, f := range flavors {
		wantBoth[f] = true
	}
	for f, found := range wantBoth {
		if !found {
			t.Errorf("BusinessownerFlavors() missing %q; got %v", f, flavors)
		}
	}
	// Deterministic order — flavors are returned sorted.
	for i := 1; i < len(flavors); i++ {
		if flavors[i] < flavors[i-1] {
			t.Errorf("BusinessownerFlavors() not sorted at idx %d: %q < %q", i, flavors[i], flavors[i-1])
		}
	}
}

// TestRenderBusinessownerPhrase covers the rendering helper's branches:
// known + interpolated, empty customer name (token-stripped), unknown
// flavor / trigger (empty), nil rand (empty).
func TestRenderBusinessownerPhrase(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	t.Run("known flavor + greet interpolates customer", func(t *testing.T) {
		got := sim.RenderBusinessownerPhrase(r, "flamboyant", sim.BusinessownerTriggerGreet, "Jefferey")
		if got == "" {
			t.Fatalf("expected non-empty render")
		}
		if strings.Contains(got, "{customer}") {
			t.Errorf("unrendered {customer} token in %q", got)
		}
	})

	t.Run("reserved flavor + greet works", func(t *testing.T) {
		// Reserved pool has some phrases without {customer}; ensure they
		// also render cleanly.
		got := sim.RenderBusinessownerPhrase(r, "reserved", sim.BusinessownerTriggerGreet, "Jefferey")
		if got == "" {
			t.Fatalf("expected non-empty render for reserved/greet")
		}
		if strings.Contains(got, "{customer}") {
			t.Errorf("unrendered {customer} in %q", got)
		}
	})

	t.Run("empty customer name strips ', {customer}' cleanly", func(t *testing.T) {
		// Pull every phrase from the pool — none should retain {customer}
		// after the empty-name fallback.
		for i := 0; i < 50; i++ {
			got := sim.RenderBusinessownerPhrase(r, "flamboyant", sim.BusinessownerTriggerGreet, "")
			if strings.Contains(got, "{customer}") {
				t.Errorf("empty-name render still contains {customer}: %q", got)
			}
			if strings.Contains(got, ", ,") || strings.HasSuffix(got, ",") {
				t.Errorf("dangling comma after token strip: %q", got)
			}
		}
	})

	t.Run("unknown flavor returns empty", func(t *testing.T) {
		got := sim.RenderBusinessownerPhrase(r, "no-such-flavor", sim.BusinessownerTriggerGreet, "Jefferey")
		if got != "" {
			t.Errorf("unknown flavor: got %q, want empty", got)
		}
	})

	t.Run("unknown trigger returns empty", func(t *testing.T) {
		got := sim.RenderBusinessownerPhrase(r, "flamboyant", sim.BusinessownerTrigger("no-such-trigger"), "Jefferey")
		if got != "" {
			t.Errorf("unknown trigger: got %q, want empty", got)
		}
	})

	t.Run("nil rand returns empty", func(t *testing.T) {
		got := sim.RenderBusinessownerPhrase(nil, "flamboyant", sim.BusinessownerTriggerGreet, "Jefferey")
		if got != "" {
			t.Errorf("nil rand: got %q, want empty", got)
		}
	})
}

// newBusinessownerWorld builds a minimal in-memory World for substrate
// tests — no terrain or structures needed, just actor seeding.
func newBusinessownerWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	return w
}

// TestEmitBusinessownerSpeech_HappyPath drives the substrate Command end
// to end on a fresh world. Verifies:
//   - Returns Fired=true.
//   - Stamps the cooldown map.
//   - Stamps the engine-speech suppression map.
//   - Emits exactly one Spoke event observable via Subscribe.
func TestEmitBusinessownerSpeech_HappyPath(t *testing.T) {
	w := newBusinessownerWorld(t)
	keeper := &sim.Actor{
		ID:                 "keeper",
		DisplayName:        "Hannah",
		Kind:               sim.KindNPCShared,
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
	}
	customer := &sim.Actor{ID: "customer", DisplayName: "Jefferey", Kind: sim.KindPC}
	w.Actors[keeper.ID] = keeper
	w.Actors[customer.ID] = customer

	// Observe Spoke events.
	var spokes []*sim.Spoke
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if s, ok := evt.(*sim.Spoke); ok {
			spokes = append(spokes, s)
		}
	}))

	now := time.Now().UTC()
	args := sim.BusinessownerSpeechArgs{
		SpeakerID:       keeper.ID,
		SpeakerName:     keeper.DisplayName,
		ListenerID:      customer.ID,
		ListenerName:    customer.DisplayName,
		Trigger:         sim.BusinessownerTriggerGreet,
		HuddleID:        "h1",
		RecipientIDs:    []sim.ActorID{customer.ID},
		CooldownMinutes: 30,
		Rand:            rand.New(rand.NewSource(42)),
		Now:             now,
	}
	out, err := sim.EmitBusinessownerSpeech(args).Fn(w)
	if err != nil {
		t.Fatalf("EmitBusinessownerSpeech: %v", err)
	}
	res, ok := out.(sim.BusinessownerSpeechResult)
	if !ok {
		t.Fatalf("Fn returned %T, want BusinessownerSpeechResult", out)
	}
	if !res.Fired {
		t.Fatalf("Fired=false, want true (skipReason=%q)", res.SkipReason)
	}
	if len(spokes) != 1 {
		t.Fatalf("got %d Spoke events, want 1", len(spokes))
	}
	if spokes[0].SpeakerID != keeper.ID {
		t.Errorf("Spoke.SpeakerID = %q, want %q", spokes[0].SpeakerID, keeper.ID)
	}
	if spokes[0].HuddleID != "h1" {
		t.Errorf("Spoke.HuddleID = %q, want h1", spokes[0].HuddleID)
	}
	if len(spokes[0].RecipientIDs) != 1 || spokes[0].RecipientIDs[0] != customer.ID {
		t.Errorf("Spoke.RecipientIDs = %v, want [%q]", spokes[0].RecipientIDs, customer.ID)
	}
	if strings.Contains(spokes[0].Text, "{customer}") {
		t.Errorf("Spoke.Text still contains {customer}: %q", spokes[0].Text)
	}
	// Cooldown stamped.
	if w.BusinessownerCooldowns == nil {
		t.Fatalf("BusinessownerCooldowns map nil after fire")
	}
	key := sim.BusinessownerCooldownKey{
		Speaker:  keeper.ID,
		Listener: customer.ID,
		Trigger:  sim.BusinessownerTriggerGreet,
	}
	stamp, ok := w.BusinessownerCooldowns[key]
	if !ok || !stamp.Equal(now) {
		t.Errorf("cooldown stamp = %v, want %v", stamp, now)
	}
	// Suppression stamped.
	if w.BusinessownerSpeechAt == nil {
		t.Fatalf("BusinessownerSpeechAt nil after fire")
	}
	if stamp := w.BusinessownerSpeechAt[keeper.ID]; !stamp.Equal(now) {
		t.Errorf("suppression stamp = %v, want %v", stamp, now)
	}
}

// TestEmitBusinessownerSpeech_CooldownActive verifies a second fire
// within the cooldown window is skipped (no new Spoke emission, cooldown
// stamp unchanged).
func TestEmitBusinessownerSpeech_CooldownActive(t *testing.T) {
	w := newBusinessownerWorld(t)
	w.Actors["keeper"] = &sim.Actor{
		ID: "keeper", DisplayName: "Hannah", Kind: sim.KindNPCShared,
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
	}
	w.Actors["customer"] = &sim.Actor{ID: "customer", DisplayName: "Jefferey", Kind: sim.KindPC}

	var spokes []*sim.Spoke
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		if s, ok := evt.(*sim.Spoke); ok {
			spokes = append(spokes, s)
		}
	}))

	now := time.Now().UTC()
	args := sim.BusinessownerSpeechArgs{
		SpeakerID: "keeper", SpeakerName: "Hannah",
		ListenerID: "customer", ListenerName: "Jefferey",
		Trigger: sim.BusinessownerTriggerGreet, HuddleID: "h1",
		RecipientIDs:    []sim.ActorID{"customer"},
		CooldownMinutes: 30,
		Rand:            rand.New(rand.NewSource(1)),
		Now:             now,
	}
	// First fire — fires.
	out1, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	res1 := out1.(sim.BusinessownerSpeechResult)
	if !res1.Fired {
		t.Fatalf("first fire skipped: %q", res1.SkipReason)
	}
	// Second fire 1 minute later, well inside the 30-min cooldown.
	args.Now = now.Add(1 * time.Minute)
	out2, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	res2 := out2.(sim.BusinessownerSpeechResult)
	if res2.Fired {
		t.Fatalf("second fire fired inside cooldown")
	}
	if res2.SkipReason != "cooldown active" {
		t.Errorf("skip reason = %q, want \"cooldown active\"", res2.SkipReason)
	}
	if len(spokes) != 1 {
		t.Errorf("got %d Spoke events, want 1 (second suppressed)", len(spokes))
	}
}

// TestEmitBusinessownerSpeech_CooldownExpired verifies a second fire
// past the cooldown window does fire.
func TestEmitBusinessownerSpeech_CooldownExpired(t *testing.T) {
	w := newBusinessownerWorld(t)
	w.Actors["keeper"] = &sim.Actor{
		ID: "keeper", DisplayName: "Hannah", Kind: sim.KindNPCShared,
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
	}
	w.Actors["customer"] = &sim.Actor{ID: "customer", DisplayName: "Jefferey", Kind: sim.KindPC}

	now := time.Now().UTC()
	args := sim.BusinessownerSpeechArgs{
		SpeakerID: "keeper", SpeakerName: "Hannah",
		ListenerID: "customer", ListenerName: "Jefferey",
		Trigger: sim.BusinessownerTriggerGreet, HuddleID: "h1",
		RecipientIDs:    []sim.ActorID{"customer"},
		CooldownMinutes: 30,
		Rand:            rand.New(rand.NewSource(1)),
		Now:             now,
	}
	sim.EmitBusinessownerSpeech(args).Fn(w)

	// Second fire 31 minutes later — past the window.
	args.Now = now.Add(31 * time.Minute)
	out, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	res := out.(sim.BusinessownerSpeechResult)
	if !res.Fired {
		t.Fatalf("post-cooldown fire skipped: %q", res.SkipReason)
	}
}

// TestEmitBusinessownerSpeech_NoCooldownForHandover verifies passing
// CooldownMinutes=0 bypasses the cooldown gate (handover path) AND
// doesn't stamp the cooldown map.
func TestEmitBusinessownerSpeech_NoCooldownForHandover(t *testing.T) {
	w := newBusinessownerWorld(t)
	w.Actors["keeper"] = &sim.Actor{
		ID: "keeper", DisplayName: "Hannah", Kind: sim.KindNPCShared,
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
	}
	w.Actors["customer"] = &sim.Actor{ID: "customer", DisplayName: "Jefferey", Kind: sim.KindPC}

	now := time.Now().UTC()
	args := sim.BusinessownerSpeechArgs{
		SpeakerID: "keeper", SpeakerName: "Hannah",
		ListenerID: "customer", ListenerName: "Jefferey",
		Trigger: sim.BusinessownerTriggerHandover, HuddleID: "h1",
		RecipientIDs:    []sim.ActorID{"customer"},
		CooldownMinutes: 0,
		Rand:            rand.New(rand.NewSource(1)),
		Now:             now,
	}
	out1, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	if !out1.(sim.BusinessownerSpeechResult).Fired {
		t.Fatalf("first handover skipped")
	}
	// Cooldown map should be empty (no stamp on CooldownMinutes=0).
	if len(w.BusinessownerCooldowns) != 0 {
		t.Errorf("handover stamped cooldown: %v", w.BusinessownerCooldowns)
	}
	// Second fire 1 second later — fires (no cooldown).
	args.Now = now.Add(1 * time.Second)
	out2, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	if !out2.(sim.BusinessownerSpeechResult).Fired {
		t.Fatalf("second handover skipped — handover should not cooldown")
	}
}

// TestEmitBusinessownerSpeech_GateSkips covers the early-return paths.
func TestEmitBusinessownerSpeech_GateSkips(t *testing.T) {
	cases := []struct {
		name       string
		seed       func(*sim.World)
		args       sim.BusinessownerSpeechArgs
		wantFired  bool
		wantReason string
	}{
		{
			name: "missing speaker",
			seed: func(*sim.World) {},
			args: sim.BusinessownerSpeechArgs{
				SpeakerID: "missing",
				Trigger:   sim.BusinessownerTriggerGreet,
				Rand:      rand.New(rand.NewSource(1)),
				Now:       time.Now(),
			},
			wantReason: "speaker missing",
		},
		{
			name: "speaker not businessowner",
			seed: func(w *sim.World) {
				w.Actors["s"] = &sim.Actor{ID: "s", DisplayName: "Plain"}
			},
			args: sim.BusinessownerSpeechArgs{
				SpeakerID: "s",
				Trigger:   sim.BusinessownerTriggerGreet,
				Rand:      rand.New(rand.NewSource(1)),
				Now:       time.Now(),
			},
			wantReason: "speaker not businessowner",
		},
		{
			name: "speaker flavor empty",
			seed: func(w *sim.World) {
				w.Actors["s"] = &sim.Actor{
					ID:                 "s",
					DisplayName:        "Voiceless",
					BusinessownerState: &sim.BusinessownerState{Flavor: ""},
				}
			},
			args: sim.BusinessownerSpeechArgs{
				SpeakerID: "s",
				Trigger:   sim.BusinessownerTriggerGreet,
				Rand:      rand.New(rand.NewSource(1)),
				Now:       time.Now(),
			},
			wantReason: "speaker flavor empty",
		},
		{
			name: "unknown flavor → phrase empty",
			seed: func(w *sim.World) {
				w.Actors["s"] = &sim.Actor{
					ID:                 "s",
					DisplayName:        "Mystery",
					BusinessownerState: &sim.BusinessownerState{Flavor: "no-such-flavor"},
				}
			},
			args: sim.BusinessownerSpeechArgs{
				SpeakerID: "s",
				Trigger:   sim.BusinessownerTriggerGreet,
				Rand:      rand.New(rand.NewSource(1)),
				Now:       time.Now(),
			},
			wantReason: "phrase empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newBusinessownerWorld(t)
			c.seed(w)
			out, err := sim.EmitBusinessownerSpeech(c.args).Fn(w)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			res := out.(sim.BusinessownerSpeechResult)
			if res.Fired != c.wantFired {
				t.Errorf("Fired = %v, want %v", res.Fired, c.wantFired)
			}
			if res.SkipReason != c.wantReason {
				t.Errorf("SkipReason = %q, want %q", res.SkipReason, c.wantReason)
			}
		})
	}
}

// TestEmitBusinessownerSpeech_CallerBugs verifies the two caller-bug
// error paths (zero SpeakerID, nil Rand).
func TestEmitBusinessownerSpeech_CallerBugs(t *testing.T) {
	w := newBusinessownerWorld(t)

	t.Run("zero SpeakerID errors", func(t *testing.T) {
		args := sim.BusinessownerSpeechArgs{
			Trigger: sim.BusinessownerTriggerGreet,
			Rand:    rand.New(rand.NewSource(1)),
			Now:     time.Now(),
		}
		_, err := sim.EmitBusinessownerSpeech(args).Fn(w)
		if err == nil {
			t.Errorf("zero SpeakerID: expected error")
		}
	})
	t.Run("nil Rand errors", func(t *testing.T) {
		args := sim.BusinessownerSpeechArgs{
			SpeakerID: "s",
			Trigger:   sim.BusinessownerTriggerGreet,
			Now:       time.Now(),
		}
		_, err := sim.EmitBusinessownerSpeech(args).Fn(w)
		if err == nil {
			t.Errorf("nil Rand: expected error")
		}
	})
}

// TestActorCanReactNow_BusinessownerSuppression verifies the engine-
// speech stamp gates actorCanReactNow for the suppression window. A
// fresh stamp suppresses; a stale stamp (>TTL ago) does not.
func TestActorCanReactNow_BusinessownerSuppression(t *testing.T) {
	w := newBusinessownerWorld(t)
	actor := &sim.Actor{ID: "keeper", DisplayName: "Hannah", State: sim.StateIdle}
	w.Actors[actor.ID] = actor

	// Baseline: no stamp → eligible.
	eligible, stale := sim.ActorCanReactNow(w, actor)
	if !eligible || stale {
		t.Fatalf("baseline: eligible=%v stale=%v, want true,false", eligible, stale)
	}

	// Stamp the keeper as having just engine-spoken. Use a fire via
	// EmitBusinessownerSpeech to get the suppression stamp without
	// reaching into private state. We need a real flavor for the fire
	// to succeed.
	actor.BusinessownerState = &sim.BusinessownerState{Flavor: "flamboyant"}
	w.Actors["listener"] = &sim.Actor{ID: "listener", DisplayName: "Jefferey"}
	now := time.Now().UTC()
	args := sim.BusinessownerSpeechArgs{
		SpeakerID: actor.ID, SpeakerName: actor.DisplayName,
		ListenerID: "listener", ListenerName: "Jefferey",
		Trigger:         sim.BusinessownerTriggerGreet,
		HuddleID:        "h1",
		RecipientIDs:    []sim.ActorID{"listener"},
		CooldownMinutes: 30,
		Rand:            rand.New(rand.NewSource(1)),
		Now:             now,
	}
	out, _ := sim.EmitBusinessownerSpeech(args).Fn(w)
	if !out.(sim.BusinessownerSpeechResult).Fired {
		t.Fatalf("fire skipped")
	}

	// Within suppression window → not eligible, not stale.
	eligible, stale = sim.ActorCanReactNow(w, actor)
	if eligible || stale {
		t.Fatalf("inside suppression: eligible=%v stale=%v, want false,false", eligible, stale)
	}

	// Manually overwrite the stamp to 10 seconds ago — past the 5s TTL.
	w.BusinessownerSpeechAt[actor.ID] = time.Now().Add(-10 * time.Second)
	eligible, stale = sim.ActorCanReactNow(w, actor)
	if !eligible || stale {
		t.Errorf("past TTL: eligible=%v stale=%v, want true,false", eligible, stale)
	}
}

// TestBusinessownerStateClone verifies cloneBusinessownerState (via
// CloneActor) deep-copies the pointer so the source and copy don't
// alias. Mirrors visitor's clone-isolation test.
func TestBusinessownerStateClone(t *testing.T) {
	a := &sim.Actor{
		ID:                 "k",
		DisplayName:        "Hannah",
		BusinessownerState: &sim.BusinessownerState{Flavor: "flamboyant"},
	}
	cp := sim.CloneActor(a)
	if cp == nil {
		t.Fatalf("CloneActor returned nil")
	}
	if cp.BusinessownerState == nil {
		t.Fatalf("clone BusinessownerState nil")
	}
	if cp.BusinessownerState == a.BusinessownerState {
		t.Fatalf("clone aliased source pointer")
	}
	if cp.BusinessownerState.Flavor != "flamboyant" {
		t.Errorf("clone flavor = %q, want flamboyant", cp.BusinessownerState.Flavor)
	}
	// Mutate the copy; source unaffected.
	cp.BusinessownerState.Flavor = "reserved"
	if a.BusinessownerState.Flavor != "flamboyant" {
		t.Errorf("source mutated through clone: %q", a.BusinessownerState.Flavor)
	}
}
