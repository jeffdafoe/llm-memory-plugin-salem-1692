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
		{"refresh is gated out", sim.SourceActivityRefresh, "bush", "", ""},
		{"idle actor carries nothing", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &sim.ActorSnapshot{
				SourceActivityKind:     c.kind,
				SourceActivityObjectID: c.objID,
			}
			kind, label := snapshotSourceActivity(snap, a)
			if kind != c.wantKind || label != c.wantLabel {
				t.Errorf("snapshotSourceActivity = (%q, %q), want (%q, %q)", kind, label, c.wantKind, c.wantLabel)
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
	if d := byID["josiah"]; d.SourceActivityKind != "repair" || d.SourceActivityLabel != "Market" {
		t.Errorf("busy actor DTO = (%q, %q), want (repair, Market)", d.SourceActivityKind, d.SourceActivityLabel)
	}
	if d := byID["idle"]; d.SourceActivityKind != "" || d.SourceActivityLabel != "" {
		t.Errorf("idle actor DTO = (%q, %q), want empty", d.SourceActivityKind, d.SourceActivityLabel)
	}
}
