package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pc_repair_test.go — LLM-690 httpapi plumbing for the PC repair routes. The
// repair mechanics are covered in the sim package (pc_repair_test.go); these
// tests exercise the HTTP surface: session→PC resolution, the wire shapes and
// the status mapping.

// seedRepairPC stands a login-bound PC at a broken well, with a chest that
// can pay and a 2-step game 1 ms apart.
func seedRepairPC(t *testing.T, w *sim.World, login string) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		ip := func(v int) *int { return &v }
		zero := 0
		// The loiter resolver finds an object only through its asset.
		world.Assets["well-asset"] = &sim.Asset{ID: "well-asset", Name: "Well", States: []sim.AssetState{{ID: 1, State: "default"}}}
		world.VillageObjects["well"] = &sim.VillageObject{
			ID: "well", DisplayName: "Well", AssetID: "well-asset", CurrentState: "default", Tags: []string{sim.TagWell},
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 500, Y: 500},
			DamagedAt: time.Now().UTC(),
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -8},
				{Amount: 0, GatherItem: "water", AvailableQuantity: ip(20), MaxQuantity: ip(20)},
			},
		}
		world.Actors["pc-mender"] = &sim.Actor{
			ID: "pc-mender", DisplayName: "Mender", Kind: sim.KindPC,
			State: sim.StateIdle, LoginUsername: login,
			Coins: 60,
			Pos:   sim.WorldPos{X: 500, Y: 500}.Tile(),
			Needs: map[sim.NeedKey]int{},
		}
		world.Environment.TownChest = 100
		world.Settings.PublicWorksBounty = 12
		world.Settings.PublicWorksChestReserve = 50
		world.Settings.PCRepairWellSteps = 2
		world.Settings.PCRepairWellStepGapMs = 1
		world.Settings.PCRepairHardCoins = 120
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedRepairPC: %v", err)
	}
}

func TestHandlePCRepair_OfferStartStep(t *testing.T) {
	w := seededWorld(t)
	seedRepairPC(t, w, "tester")
	srv := NewServer(w, okAuth{})

	var offer pcRepairOfferResponse
	if err := json.Unmarshal(get(t, srv, "/api/village/pc/repair/offer").Body.Bytes(), &offer); err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if offer.Repair == nil || offer.Repair.ObjectID != "well" || offer.Repair.SiteKind != sim.PublicWorksWell ||
		offer.Repair.Bounty != 12 || !offer.Repair.ChestCanPay || offer.Repair.Steps != 2 || offer.Repair.Yours {
		t.Fatalf("offer = %+v, want an open well repair for 12 in 2 steps", offer.Repair)
	}

	rec := post(t, srv, "/api/village/pc/repair/start", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var started pcRepairOfferResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Repair == nil || !started.Repair.Yours || started.Repair.StepGapMs != 1 || started.Repair.Difficulty != 0.5 {
		t.Fatalf("start = %+v, want Yours with a 1 ms gap at difficulty 0.5 (60 of 120 coins)", started.Repair)
	}

	var step pcRepairStepResponse
	for i := 1; i <= 2; i++ {
		time.Sleep(5 * time.Millisecond) // past the 1 ms step gap
		rec = post(t, srv, "/api/village/pc/repair/step", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("step %d status = %d, want 200; body=%s", i, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &step); err != nil {
			t.Fatalf("decode step %d: %v", i, err)
		}
	}
	if !step.Done || !step.Landed || step.Paid != 12 || step.StepsDone != 2 {
		t.Errorf("last step = %+v, want done, landed, paid 12", step)
	}

	// Mended: nothing is on offer, and a further step has nothing to count.
	var after pcRepairOfferResponse
	if err := json.Unmarshal(get(t, srv, "/api/village/pc/repair/offer").Body.Bytes(), &after); err != nil {
		t.Fatalf("decode offer after: %v", err)
	}
	if after.Repair != nil {
		t.Errorf("offer after the mend = %+v, want null", after.Repair)
	}
	if rec = post(t, srv, "/api/village/pc/repair/step", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("step after the mend status = %d, want 422", rec.Code)
	}
}

func TestHandlePCRepair_StepTooSoonIs429(t *testing.T) {
	w := seededWorld(t)
	seedRepairPC(t, w, "tester")
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.PCRepairWellStepGapMs = 60000
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(w, okAuth{})
	if rec := post(t, srv, "/api/village/pc/repair/start", ""); rec.Code != http.StatusOK {
		t.Fatalf("start status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(t, srv, "/api/village/pc/repair/step", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("early step status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlePCRepair_NotAtASiteIs422(t *testing.T) {
	w := seededWorld(t)
	seedRepairPC(t, w, "tester")
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["pc-mender"].Pos = sim.TilePos{X: 1, Y: 1}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(w, okAuth{})
	var offer pcRepairOfferResponse
	if err := json.Unmarshal(get(t, srv, "/api/village/pc/repair/offer").Body.Bytes(), &offer); err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if offer.Repair != nil {
		t.Errorf("offer away from the well = %+v, want null", offer.Repair)
	}
	if rec := post(t, srv, "/api/village/pc/repair/start", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("start status = %d, want 422", rec.Code)
	}
}

func TestHandlePCRepair_NoPCIs404(t *testing.T) {
	w := seededWorld(t)
	srv := NewServer(w, okAuth{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/village/pc/repair/offer", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("offer with no PC status = %d, want 404", rec.Code)
	}
}

func TestTranslateEvent_ObjectConditionCarriesTheRepairOffer(t *testing.T) {
	frame, ok := TranslateEvent(&sim.ObjectConditionNarrated{
		ActorID: "pc", ObjectID: "well", Text: "This well is broken.",
		Offer: &sim.PCRepairOffer{ObjectID: "well", SiteKind: sim.PublicWorksWell, Bounty: 12, ChestCanPay: true,
			MenderName: "Anne Walker", Steps: 10, StepGap: 4 * time.Second},
	})
	if !ok {
		t.Fatal("ObjectConditionNarrated should translate")
	}
	d := frame.Data.(roomEventWireDTO)
	if d.Kind != "object_condition" || d.Repair == nil {
		t.Fatalf("frame = %+v, want object_condition with a repair offer", d)
	}
	if d.Repair.Bounty != 12 || d.Repair.MenderName != "Anne Walker" || d.Repair.StepGapMs != 4000 {
		t.Errorf("repair = %+v", d.Repair)
	}

	frame, ok = TranslateEvent(&sim.PCRepairNarrated{ActorID: "pc", ObjectID: "well", Text: "You finish mending the well.", Paid: 12})
	if !ok {
		t.Fatal("PCRepairNarrated should translate")
	}
	if d := frame.Data.(roomEventWireDTO); d.Kind != "repair_done" || !d.Private || d.StructureID != "well" || d.Repair != nil {
		t.Errorf("repair_done frame = %+v", d)
	}
}
