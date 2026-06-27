package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildProvisionTestWorld seeds a running world for ProvisionWorker tests:
//
//   - the `worker` attribute definition (the command requires it to be seeded),
//   - "statue": a sprite-only KindDecorative actor (never ticked) — the mint target,
//   - "pip": a KindPC — the editableNPC guard must reject it.
//
// withWorkerDef=false omits the attribute definition to exercise
// ErrUnknownAttribute. The returned eventRec captures every emitted event.
func buildProvisionTestWorld(t *testing.T, withWorkerDef bool) (*sim.World, *eventRec) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	if _, err := w.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		if withWorkerDef {
			wd.AttributeDefinitions[sim.AttrWorker] = &sim.AttributeDefinition{Slug: sim.AttrWorker, DisplayName: "Worker"}
		}
		// Kind must be set explicitly — KindNPCStateful is iota 0, so a
		// zero-value Kind would NOT be decorative.
		wd.Actors["statue"] = &sim.Actor{ID: "statue", DisplayName: "Statue", Kind: sim.KindDecorative}
		wd.Actors["pip"] = &sim.Actor{ID: "pip", DisplayName: "Pip", Kind: sim.KindPC, LoginUsername: "pip"}
		// An already-live NPC (own VA) — must be refused, not re-linked.
		wd.Actors["hank"] = &sim.Actor{ID: "hank", DisplayName: "Hank", Kind: sim.KindNPCStateful, LLMAgent: "zbbs-hank"}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return w, rec
}

// provisionActor reads a live actor back through the command channel.
func provisionActor(t *testing.T, w *sim.World, id sim.ActorID) *sim.Actor {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		return wd.Actors[id], nil
	}})
	if err != nil {
		t.Fatalf("inspect %s: %v", id, err)
	}
	a, _ := res.(*sim.Actor)
	if a == nil {
		t.Fatalf("actor %s not found", id)
	}
	return a
}

func countAgentChanged(rec *eventRec) int {
	return rec.countEvents(func(e sim.Event) bool { _, ok := e.(*sim.NPCAgentChanged); return ok })
}

func countAttributesChanged(rec *eventRec) int {
	return rec.countEvents(func(e sim.Event) bool { _, ok := e.(*sim.NPCAttributesChanged); return ok })
}

// TestProvisionWorker_DecorativeComesOnline: the happy path — a sprite-only
// decorative is linked to salem-vendor, reclassified KindNPCShared (so the
// tick-eligibility gates now see it), and granted the worker attribute, all in
// one atomic command with both editor frames fired.
func TestProvisionWorker_DecorativeComesOnline(t *testing.T) {
	w, rec := buildProvisionTestWorld(t, true)

	res, err := w.Send(sim.ProvisionWorker("statue", sim.VendorAgentName))
	if err != nil {
		t.Fatalf("ProvisionWorker: %v", err)
	}
	out, ok := res.(sim.ProvisionWorkerResult)
	if !ok {
		t.Fatalf("result type = %T, want ProvisionWorkerResult", res)
	}
	if out.Kind != sim.KindNPCShared {
		t.Errorf("Kind = %v, want KindNPCShared (salem-vendor is a shared VA)", out.Kind)
	}
	if out.LLMAgent != sim.VendorAgentName {
		t.Errorf("LLMAgent = %q, want %q", out.LLMAgent, sim.VendorAgentName)
	}
	if len(out.Attributes) != 1 || out.Attributes[0] != sim.AttrWorker {
		t.Errorf("Attributes = %v, want [worker]", out.Attributes)
	}

	a := provisionActor(t, w, "statue")
	if a.Kind != sim.KindNPCShared || a.LLMAgent != sim.VendorAgentName {
		t.Errorf("live actor: Kind=%v LLMAgent=%q, want KindNPCShared/%s", a.Kind, a.LLMAgent, sim.VendorAgentName)
	}
	if _, ok := a.Attributes[sim.AttrWorker]; !ok {
		t.Errorf("live actor missing %q attribute", sim.AttrWorker)
	}

	if n := countAgentChanged(rec); n != 1 {
		t.Errorf("NPCAgentChanged count = %d, want 1", n)
	}
	if n := countAttributesChanged(rec); n != 1 {
		t.Errorf("NPCAttributesChanged count = %d, want 1", n)
	}
}

// TestProvisionWorker_StatefulAgent: a non-shared agent slug classifies as
// KindNPCStateful (its own persistent VA) — the command is general over the VA.
func TestProvisionWorker_StatefulAgent(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, true)
	res, err := w.Send(sim.ProvisionWorker("statue", "zbbs-statue"))
	if err != nil {
		t.Fatalf("ProvisionWorker: %v", err)
	}
	if out := res.(sim.ProvisionWorkerResult); out.Kind != sim.KindNPCStateful {
		t.Errorf("Kind = %v, want KindNPCStateful (own VA)", out.Kind)
	}
}

// TestProvisionWorker_EmptyAgentRejected: a blank agent can never tick, so it's
// rejected at the command (the HTTP layer defaults an omitted agent before this).
func TestProvisionWorker_EmptyAgentRejected(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, true)
	if _, err := w.Send(sim.ProvisionWorker("statue", "  ")); !errors.Is(err, sim.ErrInvalidAgentLink) {
		t.Errorf("err = %v, want ErrInvalidAgentLink", err)
	}
}

// TestProvisionWorker_PCRejected: editableNPC refuses to convert a human player.
func TestProvisionWorker_PCRejected(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, true)
	if _, err := w.Send(sim.ProvisionWorker("pip", sim.VendorAgentName)); !errors.Is(err, sim.ErrActorNotFound) {
		t.Errorf("err = %v, want ErrActorNotFound (PCs not provisionable)", err)
	}
}

// TestProvisionWorker_UnknownActor: a missing actor id is ErrActorNotFound.
func TestProvisionWorker_UnknownActor(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, true)
	if _, err := w.Send(sim.ProvisionWorker("ghost", sim.VendorAgentName)); !errors.Is(err, sim.ErrActorNotFound) {
		t.Errorf("err = %v, want ErrActorNotFound", err)
	}
}

// TestProvisionWorker_UnseededWorkerDef: minting fails loudly if the `worker`
// attribute_definition was never seeded (the FK would otherwise trip at checkpoint).
func TestProvisionWorker_UnseededWorkerDef(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, false)
	if _, err := w.Send(sim.ProvisionWorker("statue", sim.VendorAgentName)); !errors.Is(err, sim.ErrUnknownAttribute) {
		t.Errorf("err = %v, want ErrUnknownAttribute", err)
	}
}

// TestProvisionWorker_AlreadyMintedRejected: once minted, the actor is
// KindNPCShared, so a second provision is refused — the route is a one-way
// decorative -> worker transition, not a re-link. The refused call mutates
// nothing and emits no extra frame.
func TestProvisionWorker_AlreadyMintedRejected(t *testing.T) {
	w, rec := buildProvisionTestWorld(t, true)
	if _, err := w.Send(sim.ProvisionWorker("statue", sim.VendorAgentName)); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if _, err := w.Send(sim.ProvisionWorker("statue", sim.VendorAgentName)); !errors.Is(err, sim.ErrActorNotProvisionable) {
		t.Errorf("re-provision err = %v, want ErrActorNotProvisionable", err)
	}
	a := provisionActor(t, w, "statue")
	if len(a.Attributes) != 1 {
		t.Errorf("attributes = %d, want 1", len(a.Attributes))
	}
	if n := countAgentChanged(rec); n != 1 {
		t.Errorf("NPCAgentChanged count = %d, want 1 (no second frame)", n)
	}
	if n := countAttributesChanged(rec); n != 1 {
		t.Errorf("NPCAttributesChanged count = %d, want 1 (no second frame)", n)
	}
}

// TestProvisionWorker_ExistingLiveNPCRejected: an actor that is already a live
// NPC (own VA, KindNPCStateful) is refused — relinking a ticking actor could
// race in-flight reaction work, so the command mints only never-ticked decoratives.
func TestProvisionWorker_ExistingLiveNPCRejected(t *testing.T) {
	w, _ := buildProvisionTestWorld(t, true)
	if _, err := w.Send(sim.ProvisionWorker("hank", sim.VendorAgentName)); !errors.Is(err, sim.ErrActorNotProvisionable) {
		t.Errorf("err = %v, want ErrActorNotProvisionable (already a live NPC)", err)
	}
}
