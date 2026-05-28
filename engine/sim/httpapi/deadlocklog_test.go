package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// TestUmbilicalDeadlocks_DumpsRingOldestFirst injects two DeadlockEntries
// via World.RecordDeadlock and verifies the umbilical /deadlocks read
// route serializes them oldest→newest (the Snapshot contract) with the
// mover, occupant, destination, and replan_failed fields all preserved.
func TestUmbilicalDeadlocks_DumpsRingOldestFirst(t *testing.T) {
	w := seededWorld(t)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.RecordDeadlock(sim.DeadlockEntry{
			MoverID:         "walker",
			MoverName:       "Sarah Warren",
			MoverPos:        sim.Position{X: 73, Y: 38},
			DestinationKind: sim.MoveDestinationStructureEnter,
			DestStructureID: "warren-house",
			OccupantID:      "abraham",
			OccupantName:    "Abraham",
			OccupantTile:    sim.Position{X: 73, Y: 39},
			ReplanFailed:    true,
		})
		world.RecordDeadlock(sim.DeadlockEntry{
			MoverID:         "patience",
			MoverName:       "Patience",
			MoverPos:        sim.Position{X: 106, Y: 84},
			DestinationKind: sim.MoveDestinationPosition,
			DestPosition:    sim.Position{X: 110, Y: 84},
			OccupantID:      "lewis",
			OccupantName:    "Lewis",
			OccupantTile:    sim.Position{X: 107, Y: 84},
			ReplanFailed:    false,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("RecordDeadlock seed: %v", err)
	}

	rec := req(t, h, "/api/village/umbilical/deadlocks", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("/umbilical/deadlocks = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var entries []sim.DeadlockEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries length = %d, want 2; body=%s", len(entries), rec.Body.String())
	}
	if entries[0].MoverID != "walker" || entries[0].OccupantName != "Abraham" || !entries[0].ReplanFailed {
		t.Errorf("entry[0] = %+v, want walker/Abraham/replan_failed=true", entries[0])
	}
	if entries[0].DestinationKind != sim.MoveDestinationStructureEnter {
		t.Errorf("entry[0].DestinationKind = %q, want structure_enter", entries[0].DestinationKind)
	}
	if entries[0].DestStructureID != "warren-house" {
		t.Errorf("entry[0].DestStructureID = %q, want warren-house", entries[0].DestStructureID)
	}
	if entries[1].MoverID != "patience" || entries[1].OccupantName != "Lewis" || entries[1].ReplanFailed {
		t.Errorf("entry[1] = %+v, want patience/Lewis/replan_failed=false", entries[1])
	}
	if entries[1].DestinationKind != sim.MoveDestinationPosition {
		t.Errorf("entry[1].DestinationKind = %q, want position", entries[1].DestinationKind)
	}
	if entries[1].DestPosition != (sim.Position{X: 110, Y: 84}) {
		t.Errorf("entry[1].DestPosition = %+v, want {110 84}", entries[1].DestPosition)
	}
}

// TestUmbilicalDeadlocks_OperatorGated covers the auth posture: an
// authenticated caller without plugins/administer gets 403, matching the
// rest of the umbilical read surface.
func TestUmbilicalDeadlocks_OperatorGated(t *testing.T) {
	h := umbilicalServer(t, nil, telemetry.New(8))
	if rec := req(t, h, "/api/village/umbilical/deadlocks", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("deadlocks as non-operator = %d, want 403", rec.Code)
	}
}

// TestUmbilicalDeadlocks_OffByDefault: without SetTelemetry the whole
// umbilical surface is unregistered — /deadlocks 404s even for an
// operator. Mirrors TestUmbilical_OffByDefault.
func TestUmbilicalDeadlocks_OffByDefault(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	h := srv.Handler() // SetTelemetry NOT called
	if rec := req(t, h, "/api/village/umbilical/deadlocks", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("deadlocks with umbilical off = %d, want 404", rec.Code)
	}
}

// TestUmbilicalDeadlocks_ManifestListsRoute: the deadlock route is in the
// self-describing manifest when the umbilical is armed. This is the
// contract umbilical.go enforces — a new route can't be added without it
// appearing in the manifest, and a manifest can't claim a route that
// isn't registered.
func TestUmbilicalDeadlocks_ManifestListsRoute(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))
	rec := req(t, h, "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	var found *UmbilicalRouteDTO
	for i := range got.Routes {
		if got.Routes[i].Path == "/api/village/umbilical/deadlocks" {
			found = &got.Routes[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("/deadlocks not in manifest; got routes=%+v", got.Routes)
	}
	if found.Method != http.MethodGet {
		t.Errorf("/deadlocks method = %q, want GET", found.Method)
	}
	if found.Control {
		t.Error("/deadlocks marked as control route; should be read-only")
	}
}
