package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestRepairSiteViewAtResolvesTheSite (LLM-698) — the hover read names the
// site a hovered object shows: the break itself, either sagging neighbour of a
// fence break (not an unrelated segment), and an overlay attached to a damaged
// site; with the fact, the pay and, once someone takes it, the mender. A
// mended site shows nothing.
func TestRepairSiteViewAtResolvesTheSite(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatalf("force the break: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["lamp"] = &sim.VillageObject{ID: "lamp", AssetID: "crate-asset", CurrentState: "default", AttachedTo: "fence-2"}
	})
	view := func(id sim.VillageObjectID) *sim.RepairSiteView {
		snap := w.Published()
		return sim.RepairSiteViewAt(snap, snap.VillageObjects[id])
	}

	site := view("fence-2")
	if site == nil || site.SiteID != "fence-2" || site.Kind != sim.PublicWorksMinor || site.Bounty != 3 || !site.ChestCanPay || site.MenderName != "" {
		t.Fatalf("fence-2 view = %+v, want the open minor work for 3", site)
	}
	if !strings.HasPrefix(site.Fact, "A rail has come down on the fence") {
		t.Errorf("fact = %q", site.Fact)
	}
	for _, id := range []sim.VillageObjectID{"fence-1", "fence-3", "lamp"} {
		if v := view(id); v == nil || v.SiteID != "fence-2" {
			t.Errorf("%s shows %+v, want the break at fence-2", id, v)
		}
	}
	if v := view("fence-lone"); v != nil {
		t.Errorf("the lone fence shows %+v, want nothing", v)
	}
	// An object attached to something sound shows nothing of its own parent,
	// and an edge that is (oddly) attached to something sound is still the
	// break's edge — the lookup runs from the hovered object, not the parent.
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["sign"] = &sim.VillageObject{ID: "sign", AssetID: "crate-asset", CurrentState: "default", AttachedTo: "fence-lone"}
		world.VillageObjects["fence-1"].AttachedTo = "fence-lone"
	})
	if v := view("sign"); v != nil {
		t.Errorf("an overlay on a sound fence shows %+v, want nothing", v)
	}
	if v := view("fence-1"); v == nil || v.SiteID != "fence-2" {
		t.Errorf("an edge attached to a sound fence shows %+v, want the break at fence-2", v)
	}
	mustSend(t, w, func(world *sim.World) {
		delete(world.VillageObjects, "sign")
		world.VillageObjects["fence-1"].AttachedTo = ""
	})

	t0 := time.Now().UTC()
	placeAt(t, w, "pat", "fence-2")
	mustSend(t, w, func(world *sim.World) { world.Actors["pat"].Pos = sim.TilePos{X: 41, Y: 11} })
	if _, err := w.Send(sim.StartPCRepair("pat", t0)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if v := view("fence-1"); v == nil || v.MenderName != "Pat" {
		t.Errorf("under way: %+v, want Pat mending it", v)
	}
	for i := 1; i <= 2; i++ {
		if _, err := pcRepairStep(t, w, "pat", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []sim.VillageObjectID{"fence-1", "fence-2", "fence-3", "lamp"} {
		if v := view(id); v != nil {
			t.Errorf("mended: %s still shows %+v", id, v)
		}
	}
}

// TestRepairSiteViewAtWell — a broken well shows its fact and the well's
// bounty; a sound object shows nothing.
func TestRepairSiteViewAtWell(t *testing.T) {
	w, cancel, _ := buildPCRepairWorld(t)
	defer cancel()
	snap := w.Published()
	v := sim.RepairSiteViewAt(snap, snap.VillageObjects["well-a"])
	if v == nil || v.Kind != sim.PublicWorksWell || v.Bounty != snap.PublicWorksBounty || !strings.Contains(v.Fact, "windlass") {
		t.Fatalf("well-a view = %+v (well bounty %d)", v, snap.PublicWorksBounty)
	}
	if _, err := w.Send(sim.SetObjectDamage("well-a", "repair")); err != nil {
		t.Fatal(err)
	}
	snap = w.Published()
	if v := sim.RepairSiteViewAt(snap, snap.VillageObjects["well-a"]); v != nil {
		t.Errorf("mended well shows %+v", v)
	}
}
