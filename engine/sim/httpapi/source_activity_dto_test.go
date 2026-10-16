package httpapi

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// snap fixture: a repairable business (structure), a gatherable bush (bare
// village object), so both resolveDwellPinLabel branches are exercised.
func sourceActivityTestSnapshot() *sim.Snapshot {
	return &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{
			"market": {ID: "market", DisplayName: "Market"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bush": {ID: "bush", DisplayName: "Berry Bush"},
		},
	}
}

func TestSnapshotSourceActivity(t *testing.T) {
	snap := sourceActivityTestSnapshot()
	cases := []struct {
		name      string
		kind      sim.SourceActivityKind
		objID     sim.VillageObjectID
		wantKind  string
		wantLabel string
	}{
		{"repair names its structure", sim.SourceActivityRepair, "market", "repair", "Market"},
		{"repair falls back to village object", sim.SourceActivityRepair, "bush", "repair", "Berry Bush"},
		{"repair with unresolvable id has no label", sim.SourceActivityRepair, "ghost", "repair", ""},
		{"harvest is place-less", sim.SourceActivityHarvest, "bush", "harvest", ""},
		{"stoke is place-less", sim.SourceActivityStoke, "market", "stoke", ""},
		{"refresh is place-less", sim.SourceActivityRefresh, "bush", "refresh", ""},
		{"bake is gated out", sim.SourceActivityBake, "market", "", ""},
		{"idle actor carries nothing", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &sim.ActorSnapshot{
				SourceActivityKind:     c.kind,
				SourceActivityObjectID: c.objID,
			}
			kind, label, objectID := snapshotSourceActivity(snap, a)
			if kind != c.wantKind || label != c.wantLabel {
				t.Errorf("snapshotSourceActivity = (%q, %q), want (%q, %q)", kind, label, c.wantKind, c.wantLabel)
			}
			// LLM-743: a rendered kind carries the worked object; a gated-out one nothing.
			wantObject := ""
			if c.wantKind != "" {
				wantObject = string(c.objID)
			}
			if objectID != wantObject {
				t.Errorf("object id = %q, want %q", objectID, wantObject)
			}
		})
	}
}

func TestSourceActivityLabel(t *testing.T) {
	snap := sourceActivityTestSnapshot()
	if got := sourceActivityLabel(snap, ""); got != "" {
		t.Errorf("empty id = %q, want empty", got)
	}
	if got := sourceActivityLabel(snap, "market"); got != "Market" {
		t.Errorf("structure = %q, want Market", got)
	}
	if got := sourceActivityLabel(snap, "bush"); got != "Berry Bush" {
		t.Errorf("village object = %q, want Berry Bush", got)
	}
	if got := sourceActivityLabel(snap, "ghost"); got != "" {
		t.Errorf("unresolvable = %q, want empty", got)
	}
}

// agentsFromSnapshot carries the source-activity fields onto the wire DTO for a
// busy actor and omits them (empty) for an idle one.
func TestAgentsFromSnapshot_SourceActivity(t *testing.T) {
	snap := sourceActivityTestSnapshot()
	snap.Actors = map[sim.ActorID]*sim.ActorSnapshot{
		"josiah": {
			DisplayName:            "Josiah",
			SourceActivityKind:     sim.SourceActivityRepair,
			SourceActivityObjectID: "market",
		},
		"idle": {DisplayName: "Idle"},
	}
	byID := map[string]AgentDTO{}
	for _, dto := range agentsFromSnapshot(snap, nil) {
		byID[dto.ID] = dto
	}
	if d := byID["josiah"]; d.SourceActivityKind != "repair" || d.SourceActivityLabel != "Market" || d.SourceActivityObjectID != "market" {
		t.Errorf("busy actor DTO = (%q, %q, %q), want (repair, Market, market)", d.SourceActivityKind, d.SourceActivityLabel, d.SourceActivityObjectID)
	}
	if d := byID["idle"]; d.SourceActivityKind != "" || d.SourceActivityLabel != "" || d.SourceActivityObjectID != "" {
		t.Errorf("idle actor DTO = (%q, %q, %q), want empty", d.SourceActivityKind, d.SourceActivityLabel, d.SourceActivityObjectID)
	}
}

// LLM-798: a camp beat reaches the client with its piece and where it stands,
// a mid-work completion sends nothing, and the last one clears the actor.
func TestTranslateCampBeat(t *testing.T) {
	target := sim.WorldPos{X: 96, Y: 160}
	frame, ok := TranslateEvent(&sim.SourceActivityStarted{
		ActorID: "pc", ObjectID: "tent", Kind: sim.SourceActivityPitch, Beat: sim.CampBeatFire, Target: &target,
	})
	if !ok {
		t.Fatal("a pitch beat was not sent")
	}
	dto, isDTO := frame.Data.(sourceActivityChangedWireDTO)
	if !isDTO || dto.Kind != "pitch" || dto.Beat != "fire" || dto.Target == nil || dto.Target.X != 96 || dto.Target.Y != 160 {
		t.Fatalf("frame = %+v, want kind pitch, beat fire, target 96,160", frame.Data)
	}
	if _, ok := TranslateEvent(&sim.SourceActivityCompleted{ActorID: "pc", Kind: sim.SourceActivityStrike, Continues: true}); ok {
		t.Error("a mid-strike completion was sent, want none (the next beat follows at once)")
	}
	frame, ok = TranslateEvent(&sim.SourceActivityCompleted{ActorID: "pc", Kind: sim.SourceActivityStrike})
	if dto, _ := frame.Data.(sourceActivityChangedWireDTO); !ok || dto.Kind != "" {
		t.Errorf("last strike completion = %+v (sent %v), want a clear", frame.Data, ok)
	}
}

// LLM-798: a client loading mid-pitch gets the beat and where it stands, so it
// can face the player the right way.
func TestAgentsFromSnapshot_CampBeat(t *testing.T) {
	snap := &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{
		"pc": {
			Kind:                   sim.KindPC,
			SourceActivityKind:     sim.SourceActivityPitch,
			SourceActivityObjectID: "tent",
			SourceActivityBeat:     sim.CampBeatStool,
			SourceActivityTarget:   sim.WorldPos{X: 128, Y: 192},
		},
		"npc": {Kind: sim.KindNPCShared, SourceActivityKind: sim.SourceActivityHarvest, SourceActivityObjectID: "bush"},
	}}
	agents := agentsFromSnapshot(snap, nil)
	byID := map[string]AgentDTO{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	pc := byID["pc"]
	if pc.SourceActivityKind != "pitch" || pc.SourceActivityBeat != "stool" || pc.SourceActivityTarget == nil ||
		pc.SourceActivityTarget.X != 128 || pc.SourceActivityTarget.Y != 192 {
		t.Errorf("pc = kind %q beat %q target %+v, want pitch / stool / 128,192", pc.SourceActivityKind, pc.SourceActivityBeat, pc.SourceActivityTarget)
	}
	if npc := byID["npc"]; npc.SourceActivityBeat != "" || npc.SourceActivityTarget != nil {
		t.Errorf("a harvest carries beat %q target %+v, want none", npc.SourceActivityBeat, npc.SourceActivityTarget)
	}
}
