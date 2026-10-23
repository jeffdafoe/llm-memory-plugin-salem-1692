package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// golden_let_in_test.go — LLM-20. A resident at home with someone knocking at
// the door: the company line places the knocker outside, and the door cue
// offers let_in. The same fixture with the knocker stepped in (or a visitor who
// lives there) carries neither.

const (
	letInHomeID   = sim.StructureID("walker-home")
	letInHostID   = sim.ActorID("silence")
	letInKnockID  = sim.ActorID("jefferey")
	letInHuddleID = sim.HuddleID("walker_door")
)

// knockAtHomeScenario: Silence Walker inside the Walker Residence, Jefferey (a
// player she knows) at its door in the doorway conversation. inside=true puts
// Jefferey through the door instead (after let_in).
func knockAtHomeScenario(inside bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 16 * 60
	published := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)
	doorX, doorY := 0, 1
	assets := map[sim.AssetID]*sim.Asset{
		"red-medium": {ID: "red-medium", Name: "Red House (Medium)", Category: "structure",
			DoorOffsetX: &doorX, DoorOffsetY: &doorY, FootprintLeft: 3, FootprintRight: 3, FootprintTop: 6},
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(letInHomeID): {ID: sim.VillageObjectID(letInHomeID), AssetID: "red-medium",
			Pos: sim.WorldPos{X: 20 * 32, Y: 20 * 32}, EntryPolicy: sim.EntryPolicyOwner, DisplayName: "Walker Residence"},
	}
	pin, ok := sim.ObjectLoiterPin(objects, assets, sim.VillageObjectID(letInHomeID))
	if !ok {
		panic("knockAtHomeScenario: no loiter pin")
	}
	host := &sim.ActorSnapshot{
		Kind: sim.KindNPCStateful, DisplayName: "Silence Walker", Role: "villager", State: sim.StateIdle,
		CurrentHuddleID: letInHuddleID, Needs: map[sim.NeedKey]int{}, Coins: 5,
		HomeStructureID: letInHomeID, InsideStructureID: letInHomeID,
		Acquaintances: map[string]sim.Acquaintance{"Jefferey": {FirstInteractedAt: published.Add(-48 * time.Hour)}},
	}
	knocker := &sim.ActorSnapshot{
		Kind: sim.KindPC, DisplayName: "Jefferey", State: sim.StateIdle,
		CurrentHuddleID: letInHuddleID, Needs: map[sim.NeedKey]int{}, Pos: pin, LastPCSeenAt: &published,
	}
	if inside {
		knocker.InsideStructureID = letInHomeID
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           assets,
		VillageObjects:   objects,
		Structures:       map[sim.StructureID]*sim.Structure{letInHomeID: {ID: letInHomeID, DisplayName: "Walker Residence"}},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{letInHostID: host, letInKnockID: knocker},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			letInHuddleID: {ID: letInHuddleID, Members: map[sim.ActorID]struct{}{letInHostID: {}, letInKnockID: {}},
				RecentUtterances: []sim.Utterance{{SpeakerID: letInKnockID, SpeakerName: "Jefferey",
					Text: "Good evening, Silence. May I come in?", At: published.Add(-5 * time.Second)}}},
		},
	}
	return snap, letInHostID, nil
}

// customerAtStallCounterScenario: the same fixture as a market stall — Silence
// keeps it (owns it, works it, lives elsewhere), Jefferey stands at its loiter
// pin in her conversation: a customer at the counter (LLM-384), not a knocker.
func customerAtStallCounterScenario() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id, w := knockAtHomeScenario(false)
	stall := snap.VillageObjects[sim.VillageObjectID(letInHomeID)]
	stall.DisplayName = "Walker Stall"
	stall.OwnerActorID = letInHostID
	stall.Tags = []string{"business", "shop", sim.TagMarketStall}
	snap.Structures[letInHomeID].DisplayName = "Walker Stall"
	host := snap.Actors[letInHostID]
	host.HomeStructureID = "walker-cottage"
	host.WorkStructureID = letInHomeID
	return snap, id, w
}

func init() {
	perceptionScenarios = append(perceptionScenarios, perceptionScenario{
		name: "stall_keeper_with_customer_at_counter",
		summary: "LLM-20/793: Silence keeps a market stall and Jefferey stands at its loiter pin in her conversation. " +
			"Live, every stall keeper read such a customer as '(at your door, outside)' and let them in with let_in, " +
			"which put the player inside the stall and shut the stall window. The golden pins no door note and no " +
			"let_in cue. Property guard: TestLetInCueAndDoorstep.",
		build: customerAtStallCounterScenario,
	})
}

// TestLetInCueAndDoorstep pins the property across the two shapes: someone at
// the door ⇒ the outside note, the door cue and AtYourDoor (the let_in gate);
// stepped inside ⇒ none of them.
func TestLetInCueAndDoorstep(t *testing.T) {
	snap, id, w := knockAtHomeScenario(false)
	p := Build(snap, id, w)
	if len(p.AtYourDoor) != 1 || p.AtYourDoor[0] != letInKnockID {
		t.Fatalf("AtYourDoor = %v, want [jefferey]", p.AtYourDoor)
	}
	got := combinedPrompt(Render(p, DefaultRenderConfig()))
	for _, want := range []string{"Jefferey (at your door, outside)", "Jefferey is at your door, outside. To have them in, open the door with let_in."} {
		if !strings.Contains(got, want) {
			t.Errorf("knocker at the door: prompt lacks %q", want)
		}
	}

	snap, id, w = knockAtHomeScenario(true)
	p = Build(snap, id, w)
	if len(p.AtYourDoor) != 0 {
		t.Errorf("knocker inside: AtYourDoor = %v, want none", p.AtYourDoor)
	}
	got = combinedPrompt(Render(p, DefaultRenderConfig()))
	if strings.Contains(got, "at your door") || strings.Contains(got, "let_in") {
		t.Error("knocker inside: the prompt still puts them at the door")
	}

	// A household member at the door is no visitor to let in.
	snap, id, w = knockAtHomeScenario(false)
	snap.Actors[letInKnockID].HomeStructureID = letInHomeID
	if p = Build(snap, id, w); len(p.AtYourDoor) != 0 {
		t.Errorf("a household member at the door: AtYourDoor = %v, want none", p.AtYourDoor)
	}
	// Someone else's house: the subject may not open its door.
	snap, id, w = knockAtHomeScenario(false)
	snap.Actors[letInHostID].HomeStructureID = "elsewhere"
	if p = Build(snap, id, w); len(p.AtYourDoor) != 0 {
		t.Errorf("not the subject's home: AtYourDoor = %v, want none", p.AtYourDoor)
	}
	// A market stall: the customer at the loiter pin is at the counter, and
	// there is no door to open for them — even for a keeper who also lives there.
	snap, id, w = customerAtStallCounterScenario()
	p = Build(snap, id, w)
	if len(p.AtYourDoor) != 0 {
		t.Errorf("customer at a stall: AtYourDoor = %v, want none", p.AtYourDoor)
	}
	got = combinedPrompt(Render(p, DefaultRenderConfig()))
	if strings.Contains(got, "at your door") || strings.Contains(got, "let_in") {
		t.Error("customer at a stall: the prompt puts them at the door")
	}
	snap, id, w = customerAtStallCounterScenario()
	snap.Actors[letInHostID].HomeStructureID = letInHomeID
	if p = Build(snap, id, w); len(p.AtYourDoor) != 0 {
		t.Errorf("customer at a stall its keeper lives in: AtYourDoor = %v, want none", p.AtYourDoor)
	}
}
