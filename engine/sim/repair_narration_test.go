package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// repair_narration_test.go — LLM-354 coverage of the observer-facing repair
// narration. Driven through the real StartRepair command rather than by calling
// emitRepairNarration directly: the gate reads the mender's conversational scope,
// which only resolves once the command has validated co-location, so a test that
// skipped the command would assert against a world the engine never produces.

// backRoomID is the private subspace inside the store used by the leak test.
const backRoomID = sim.RoomID(7)

// buildRepairNarrationWorld seeds a running world with a "store" business owned by
// the keeper, worn past the repair threshold, with the keeper standing inside it
// holding enough nails to mend. When withPC is true a PC stands inside the store
// too — the co-present observer whose talk panel should receive the line. When
// inBackRoom is true the keeper mends from the store's private back room, which
// must suppress the room_id-less live line.
func buildRepairNarrationWorld(t *testing.T, withPC, inBackRoom bool) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"store-asset": {ID: "store-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"store": {
			ID:            "store",
			AssetID:       "store-asset",
			DisplayName:   "General Store",
			Pos:           sim.WorldPos{X: 320, Y: 320},
			LoiterOffsetX: intp(0),
			LoiterOffsetY: intp(5),
			OwnerActorID:  "keeper",
			Tags:          []string{sim.TagBusiness},
			Wear:          sim.DefaultStallWearRepairThreshold,
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"store": {
			ID: "store", DisplayName: "General Store",
			Rooms: []*sim.Room{
				{ID: backRoomID, StructureID: "store", Kind: sim.RoomKindPrivate, Name: "back_room"},
			},
		},
	})
	keeperRoom := sim.RoomID(0)
	if inBackRoom {
		keeperRoom = backRoomID
	}
	actors := map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Josiah Thorne", Kind: sim.KindNPCStateful,
			Pos:               sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			InsideStructureID: "store",
			InsideRoomID:      keeperRoom,
			Inventory:         map[sim.ItemKind]int{sim.NailItemKind: sim.DefaultStallNailsPerRepair},
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
		},
	}
	if withPC {
		actors["pc-1"] = &sim.Actor{
			ID: "pc-1", DisplayName: "Player One", Kind: sim.KindPC,
			Pos:               sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			InsideStructureID: "store",
		}
	}
	handles.Actors.Seed(actors)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// The mem repo seeds zero settings; StartRepair's wear/nails gates need the
	// real thresholds. Set before Run so the world goroutine never sees a torn read.
	w.Settings.StallWearRepairThreshold = sim.DefaultStallWearRepairThreshold
	w.Settings.StallWearDegradeThreshold = sim.DefaultStallWearDegradeThreshold
	w.Settings.StallNailsPerRepair = sim.DefaultStallNailsPerRepair
	w.Settings.StallRepairDurationSeconds = sim.DefaultStallRepairDurationSeconds
	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel, rec
}

func repairNarrations(rec *eventRec) []*sim.ActorRepairNarrated {
	var out []*sim.ActorRepairNarrated
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if n, ok := e.(*sim.ActorRepairNarrated); ok {
			out = append(out, n)
		}
	}
	return out
}

// TestRepairNarration_EmitsToCoPresentPC: a keeper mending his own business while
// a PC stands in the shop emits ActorRepairNarrated naming the business — the same
// phrasing renderActionLogEntry uses for the panel backload.
func TestRepairNarration_EmitsToCoPresentPC(t *testing.T) {
	w, cancel, rec := buildRepairNarrationWorld(t, true, false)
	defer cancel()

	if _, err := w.Send(sim.StartRepair("keeper")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}

	got := repairNarrations(rec)
	if len(got) != 1 {
		t.Fatalf("emitted %d ActorRepairNarrated, want 1", len(got))
	}
	n := got[0]
	const wantText = "Josiah Thorne is mending the General Store."
	if n.Text != wantText {
		t.Errorf("Text = %q, want %q", n.Text, wantText)
	}
	if n.ActorID != "keeper" || n.ActorName != "Josiah Thorne" {
		t.Errorf("actor = %q/%q, want keeper/Josiah Thorne", n.ActorID, n.ActorName)
	}
	if n.StructureID != "store" {
		t.Errorf("StructureID = %q, want store", n.StructureID)
	}
}

// TestRepairNarration_SkippedWithNoPC: no PC in earshot, no line — the repair
// still starts and still emits its substrate seam (which drives the action-log row
// and thus the backload), but nothing is narrated to an empty room.
func TestRepairNarration_SkippedWithNoPC(t *testing.T) {
	w, cancel, rec := buildRepairNarrationWorld(t, false, false)
	defer cancel()

	if _, err := w.Send(sim.StartRepair("keeper")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}

	if got := repairNarrations(rec); len(got) != 0 {
		t.Errorf("emitted %d ActorRepairNarrated with no PC present, want 0", len(got))
	}
	if started := repairStarts(rec); started != 1 {
		t.Errorf("SourceActivityStarted(repair) count = %d, want 1", started)
	}
}

// TestRepairNarration_SkippedInPrivateRoom: mending from the store's private back
// room emits no live line even with a PC in the building. The wire frame carries
// no room_id, so a back-room narration would leak to common-room observers who
// cannot see in; that case is left to the panel backload, which IS room-scoped.
// The substrate seam still fires, so the action-log row (and the backload) survive.
func TestRepairNarration_SkippedInPrivateRoom(t *testing.T) {
	w, cancel, rec := buildRepairNarrationWorld(t, true, true)
	defer cancel()

	if _, err := w.Send(sim.StartRepair("keeper")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}

	if got := repairNarrations(rec); len(got) != 0 {
		t.Errorf("emitted %d ActorRepairNarrated from a private room, want 0 (leak guard)", len(got))
	}
	if started := repairStarts(rec); started != 1 {
		t.Errorf("SourceActivityStarted(repair) count = %d, want 1", started)
	}
}

func repairStarts(rec *eventRec) int {
	return rec.countEvents(func(e sim.Event) bool {
		s, ok := e.(*sim.SourceActivityStarted)
		return ok && s.Kind == sim.SourceActivityRepair
	})
}
