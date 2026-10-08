package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// fenceAssetID is the live Ranch Fence asset id (LLM-637).
const fenceAssetID sim.AssetID = "019e5f00-c401-7a10-9e00-000000637001"

// buildMinorWorksWorld is buildPCRepairWorld (wells sound) plus a fence asset
// carrying one three-cell break (site h-broken-1, edges -l / -r) and a run of
// three 'h' segments at tiles (40,10)-(42,10), a lone 'h' at (60,10), and a
// crate with a loose-lid state. Pat stands by the middle fence. Minor works
// open: cap 4, every roll a hit, 3 coins in 2 steps 1 s apart.
func buildMinorWorksWorld(t *testing.T) (*sim.World, context.CancelFunc, *eventRec) {
	t.Helper()
	w, cancel, rec := buildPCRepairWorld(t)
	if _, err := w.Send(sim.SetObjectDamage("well-a", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Assets[fenceAssetID] = &sim.Asset{ID: fenceAssetID, Name: "Ranch Fence", DefaultState: "h", States: []sim.AssetState{
			{ID: 100, State: "h", Tags: []string{"fence-h"}},
			{ID: 101, State: "v", Tags: []string{"fence-v"}},
			{ID: 110, State: "h-broken-1", Tags: []string{"minor-of-h", "minor-form-fence", "minor-left-h-broken-1-l", "minor-right-h-broken-1-r"}},
			{ID: 111, State: "h-broken-1-l", Tags: []string{"minor-of-h"}},
			{ID: 112, State: "h-broken-1-r", Tags: []string{"minor-of-h"}},
		}}
		world.Assets["crate-asset"] = &sim.Asset{ID: "crate-asset", Name: "Crate", DefaultState: "default", States: []sim.AssetState{
			{ID: 200, State: "default"},
			{ID: 201, State: "lid-loose", Tags: []string{"minor-of-default", "minor-form-crate"}},
		}}
		fence := func(id sim.VillageObjectID, x int) *sim.VillageObject {
			return &sim.VillageObject{ID: id, AssetID: fenceAssetID, CurrentState: "h", Pos: sim.TilePos{X: x, Y: 10}.Center()}
		}
		for _, f := range []*sim.VillageObject{fence("fence-1", 40), fence("fence-2", 41), fence("fence-3", 42), fence("fence-lone", 60)} {
			world.VillageObjects[f.ID] = f
		}
		world.Actors["pat"].Pos = sim.TilePos{X: 41, Y: 11}
		world.Settings.MinorWorksOpenCap = 4
		world.Settings.MinorWorksChancePermille = 1000
		world.Settings.MinorWorksTTLHours = 24
		world.Settings.PublicWorksMinorBounty = 3
		world.Settings.PCRepairMinorSteps = 2
		world.Settings.PCRepairMinorStepGapMs = 1000
	})
	return w, cancel, rec
}

func minorFenceStates(t *testing.T, w *sim.World) [3]string {
	t.Helper()
	var out [3]string
	mustSend(t, w, func(world *sim.World) {
		for i, id := range []sim.VillageObjectID{"fence-1", "fence-2", "fence-3"} {
			out[i] = world.VillageObjects[id].CurrentState
		}
	})
	return out
}

func rollMinor(t *testing.T, w *sim.World, at time.Time, vals ...float64) *sim.VillageObject {
	t.Helper()
	res, err := w.Send(sim.RollMinorWorks(at, &seqRoller{vals: vals}))
	if err != nil {
		t.Fatalf("RollMinorWorks: %v", err)
	}
	return res.(*sim.VillageObject)
}

// TestMinorWorkFenceBreaksAcrossThreeSegments — the only fence that can break
// is the middle of the run (the lone one has no neighbours to sag into); it
// flips with both edges, and it is a repair site but not a posted one.
func TestMinorWorkFenceBreaksAcrossThreeSegments(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) { delete(world.VillageObjects, "crate") })
	// First roll hits the chance, the second picks the only candidate.
	got := rollMinor(t, w, time.Now().UTC(), 0, 0.99, 0)
	if got == nil || got.ID != "fence-2" {
		t.Fatalf("broke %v, want fence-2", got)
	}
	if s := minorFenceStates(t, w); s != [3]string{"h-broken-1-l", "h-broken-1", "h-broken-1-r"} {
		t.Errorf("fence states = %v, want the three-cell break", s)
	}
	mustSend(t, w, func(world *sim.World) {
		mid := world.VillageObjects["fence-2"]
		if !mid.HasTag(sim.TagMinorWork) {
			t.Error("the site does not carry the minor-work tag")
		}
		if !sim.IsRepairSite(mid) || sim.IsDamagedSite(mid) || sim.PublicWorksKind(mid) != sim.PublicWorksMinor {
			t.Errorf("fence-2: repair site %v, posted site %v, kind %q — want a minor work only",
				sim.IsRepairSite(mid), sim.IsDamagedSite(mid), sim.PublicWorksKind(mid))
		}
		for _, edge := range []sim.VillageObjectID{"fence-1", "fence-3"} {
			if world.VillageObjects[edge].Damaged() {
				t.Errorf("edge %s is damaged; only the site is a minor work", edge)
			}
		}
		if lines := sim.PublicWorksNoticeLines(world); len(lines) != 0 {
			t.Errorf("the boards carry a minor work: %q", lines)
		}
	})
	if lines := sim.DamageTickerLines(w.Published()); len(lines) != 1 || lines[0].ObjectID != "fence-2" {
		t.Errorf("ticker = %+v, want the fence's line alone", lines)
	}
	// Nothing left to break: the edges are not 'h', and the lone fence has no neighbours.
	if again := rollMinor(t, w, time.Now().UTC(), 0, 0); again != nil {
		t.Errorf("a second break landed on %s", again.ID)
	}
}

// TestMinorWorkOnTheTickerNotTheBoards — a minor work's break, a chest too poor
// for its bounty, and its mend each tell the client (DamageNewsChanged) and
// change its ticker line, and none of them reposts a board: the crier reads the
// boards aloud, and a minor work is a player's job only.
func TestMinorWorkOnTheTickerNotTheBoards(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	posted := time.Now().UTC().Add(-time.Hour)
	news := 0
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if _, ok := evt.(*sim.DamageNewsChanged); ok {
				news++
			}
		}))
		sim.SetVillageObjectState("board", "two").Fn(world)
		if _, err := sim.SaveNoticeboardContent("board", "The crier's first line\nThe crier's second line", "two", posted).Fn(world); err != nil {
			t.Fatal(err)
		}
	})
	boardUntouched := func(when string) {
		t.Helper()
		mustSend(t, w, func(world *sim.World) {
			if c := world.NoticeboardContent["board"]; c == nil || !c.PostedAt.Equal(posted) || c.Pinned != 0 {
				t.Errorf("%s: board = %+v, want the crier's post untouched", when, c)
			}
		})
	}
	tickerLine := func() string {
		t.Helper()
		lines := sim.DamageTickerLines(w.Published())
		if len(lines) == 0 {
			return ""
		}
		return lines[0].Text
	}

	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatal(err)
	}
	if news != 1 {
		t.Errorf("news events after the break = %d, want 1", news)
	}
	if got := tickerLine(); !strings.HasPrefix(got, "A rail has come down on the fence") || !strings.HasSuffix(got, " — the town pays 3 coins to the hand who mends it.") {
		t.Errorf("ticker after the break = %q", got)
	}
	boardUntouched("after the break")

	mustSend(t, w, func(world *sim.World) {
		world.Environment.TownChest = 0
		sim.SyncPublicWorksNews(world, time.Now().UTC())
	})
	if news != 2 {
		t.Errorf("news events after the chest emptied = %d, want 2", news)
	}
	if got := tickerLine(); !strings.HasSuffix(got, " — the town cannot pay for the mending just now.") {
		t.Errorf("ticker with an empty chest = %q", got)
	}
	boardUntouched("after the chest emptied")

	if _, err := w.Send(sim.SetObjectDamage("fence-2", "repair")); err != nil {
		t.Fatal(err)
	}
	if news != 3 {
		t.Errorf("news events after the mend = %d, want 3", news)
	}
	if got := tickerLine(); got != "" {
		t.Errorf("ticker after the mend = %q, want nothing", got)
	}
	boardUntouched("after the mend")
}

// TestMinorWorkPlayerMendsTheFence — Pat sees the fence offer (form fence,
// 3 coins, 2 steps), plays it, and all three segments mend.
func TestMinorWorkPlayerMendsTheFence(t *testing.T) {
	w, cancel, rec := buildMinorWorksWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatalf("force the break: %v", err)
	}
	offer := pcRepairOffer(t, w, "pat")
	if offer == nil || offer.ObjectID != "fence-2" || offer.SiteKind != sim.PublicWorksMinor || offer.Form != sim.MinorFormFence ||
		offer.Bounty != 3 || offer.Steps != 2 || !offer.ChestCanPay {
		t.Fatalf("offer = %+v, want the fence for 3 in 2 steps", offer)
	}
	if !strings.HasPrefix(offer.Fact, "A rail has come down on the fence") {
		t.Errorf("fact = %q", offer.Fact)
	}
	t0 := time.Now().UTC()
	if _, err := w.Send(sim.StartPCRepair("pat", t0)); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	if _, err := pcRepairStep(t, w, "pat", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	last, err := pcRepairStep(t, w, "pat", t0.Add(2*time.Second))
	if err != nil || !last.Done || !last.Landed || last.Paid != 3 {
		t.Fatalf("last step = %+v, %v; want landed for 3", last, err)
	}
	if s := minorFenceStates(t, w); s != [3]string{"h", "h", "h"} {
		t.Errorf("fence states after the mend = %v, want all h", s)
	}
	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects["fence-2"].Damaged() || world.VillageObjects["fence-2"].HasTag(sim.TagMinorWork) {
			t.Error("fence-2 still damaged or still tagged a minor work")
		}
		if got := world.Actors["pat"].Coins; got != 3 {
			t.Errorf("pat coins = %d, want 3", got)
		}
	})
	if n := rec.countEvents(func(e sim.Event) bool {
		p, ok := e.(*sim.PCRepairNarrated)
		return ok && strings.Contains(p.Text, "ranch fence") && strings.Contains(p.Text, "3 coins")
	}); n != 1 {
		t.Errorf("PCRepairNarrated naming the ranch fence and 3 coins = %d, want 1", n)
	}
}

// TestMinorWorkIsNeverAHandsJob — a worker at the broken fence finds no town's
// work there, and the town's-works gate refuses a minor site to a non-player.
func TestMinorWorkIsNeverAHandsJob(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["anne"].Pos = sim.TilePos{X: 41, Y: 11} })
	if _, err := w.Send(sim.StartRepair("anne")); err == nil {
		t.Error("a hand started a minor work")
	}
	mustSend(t, w, func(world *sim.World) {
		if world.Actors["anne"].SourceActivity != nil {
			t.Errorf("anne holds a window: %+v", world.Actors["anne"].SourceActivity)
		}
	})
	if _, err := w.Send(sim.StartPCRepair("anne", time.Now().UTC())); err == nil {
		t.Error("StartPCRepair accepted an NPC at a minor work")
	}
	// The pay is gated too: a hand's window on a minor work (which the start
	// gate never opens) lands nothing.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].SourceActivity = &sim.SourceActivity{
			Kind: sim.SourceActivityRepair, ObjectID: "fence-2", Bounty: 3, PublicWorks: true,
			StartedAt: time.Now().UTC().Add(-time.Hour), Until: time.Now().UTC().Add(-time.Second),
		}
		sim.CompleteDueSourceActivities(world, time.Now().UTC())
		if got := world.Actors["anne"].Coins; got != 0 {
			t.Errorf("a hand was paid %d for a minor work", got)
		}
		if !world.VillageObjects["fence-2"].Damaged() {
			t.Error("a hand's window mended a minor work")
		}
	})
}

// TestMinorWorkNarrationIsPlayerOnly — the arrival thought, offer and all,
// never reaches an NPC that walks straight to a broken fence.
func TestMinorWorkNarrationIsPlayerOnly(t *testing.T) {
	w, cancel, rec := buildMinorWorksWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].Pos = sim.TilePos{X: 41, Y: 11}
		sim.EmitDamagedObjectNarration(world, world.Actors["anne"], &sim.ActorArrived{DestObjectID: "fence-2"}, time.Now().UTC())
	})
	if n := rec.countEvents(func(e sim.Event) bool { _, ok := e.(*sim.ObjectConditionNarrated); return ok }); n != 0 {
		t.Errorf("an NPC got %d minor-work narration(s)", n)
	}
}

// TestMinorWorkMendsItselfAfterTheTTL — an untaken minor work mends itself at
// the first roll past the TTL; one a player is mending does not.
func TestMinorWorkMendsItselfAfterTheTTL(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	t0 := time.Now().UTC()
	if _, err := w.Send(sim.SetObjectDamage("fence-2", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) { world.Settings.MinorWorksChancePermille = 0 })

	rollMinor(t, w, t0.Add(23*time.Hour))
	if s := minorFenceStates(t, w); s[1] != "h-broken-1" {
		t.Fatalf("mended before the TTL: %v", s)
	}
	if _, err := w.Send(sim.StartPCRepair("pat", t0.Add(24*time.Hour))); err != nil {
		t.Fatalf("StartPCRepair: %v", err)
	}
	rollMinor(t, w, t0.Add(25*time.Hour))
	if s := minorFenceStates(t, w); s[1] != "h-broken-1" {
		t.Errorf("a minor work mended itself under the player's hands: %v", s)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["pat"].SourceActivity = nil })
	rollMinor(t, w, t0.Add(25*time.Hour))
	if s := minorFenceStates(t, w); s != [3]string{"h", "h", "h"} {
		t.Errorf("after the TTL = %v, want all mended", s)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.Actors["pat"].Coins; got != 0 {
			t.Errorf("an expiry paid %d", got)
		}
	})
}

// TestMinorWorksOpenCap — no new break while the cap is reached; a cap or
// chance of 0 turns the spawn off.
func TestMinorWorksOpenCap(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["crate"] = &sim.VillageObject{ID: "crate", AssetID: "crate-asset", CurrentState: "default", Pos: sim.TilePos{X: 80, Y: 10}.Center()}
		world.Settings.MinorWorksOpenCap = 1
	})
	if got := rollMinor(t, w, time.Now().UTC(), 0, 0); got == nil {
		t.Fatal("the first roll broke nothing")
	}
	if got := rollMinor(t, w, time.Now().UTC(), 0, 0); got != nil {
		t.Errorf("broke %s past the cap of 1", got.ID)
	}
	mustSend(t, w, func(world *sim.World) { world.Settings.MinorWorksOpenCap = 0 })
	if got := rollMinor(t, w, time.Now().UTC(), 0, 0); got != nil {
		t.Errorf("broke %s with the cap at 0", got.ID)
	}
}

// TestMinorWorkCrateFormAndNarration — the crate's loose lid is a single-cell
// minor work; the arrival thought names it and carries the offer.
func TestMinorWorkCrateFormAndNarration(t *testing.T) {
	w, cancel, rec := buildMinorWorksWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["crate"] = &sim.VillageObject{ID: "crate", AssetID: "crate-asset", CurrentState: "default", Pos: sim.TilePos{X: 80, Y: 10}.Center()}
		world.Actors["pat"].Pos = sim.TilePos{X: 80, Y: 11}
	})
	if _, err := w.Send(sim.SetObjectDamage("crate", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["crate"].CurrentState; got != "lid-loose" {
			t.Errorf("crate state = %q, want lid-loose", got)
		}
		sim.EmitDamagedObjectNarration(world, world.Actors["pat"], &sim.ActorArrived{}, time.Now().UTC())
	})
	var got *sim.ObjectConditionNarrated
	rec.countEvents(func(e sim.Event) bool {
		if n, ok := e.(*sim.ObjectConditionNarrated); ok {
			got = n
		}
		return false
	})
	if got == nil || got.Offer == nil || got.Offer.Form != sim.MinorFormCrate || got.Offer.Bounty != 3 {
		t.Fatalf("narration = %+v, want the crate offer for 3", got)
	}
	if !strings.Contains(got.Text, "lid has been knocked loose on the crate") || !strings.Contains(got.Text, "3 coins") {
		t.Errorf("narration text = %q", got.Text)
	}
	if _, err := w.Send(sim.SetObjectDamage("crate", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["crate"].CurrentState; got != "default" {
			t.Errorf("crate state after repair = %q, want default", got)
		}
	})
}

// TestMinorWorkNotDamageableWithoutAVariant — the operator control still
// refuses an object with no minor-work state for its current state.
func TestMinorWorkNotDamageableWithoutAVariant(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("fence-lone", "damage")); !errors.Is(err, sim.ErrNotDamageable) {
		t.Errorf("damage a lone fence: err = %v, want ErrNotDamageable (no neighbours to sag into)", err)
	}
}

// TestMinorWorksToldAlikeShareOneTickerLine — two fence breaks by the Mansion
// read the same, so the ticker tells them as one line under the lower id,
// counted; a break with no landmark near reads differently and keeps its own
// singular line (LLM-718). Mending one of the pair puts the other back on the
// single break's wording.
func TestMinorWorksToldAlikeShareOneTickerLine(t *testing.T) {
	w, cancel, _ := buildMinorWorksWorld(t)
	defer cancel()
	mustSend(t, w, func(world *sim.World) {
		fence := func(id sim.VillageObjectID, x int) *sim.VillageObject {
			return &sim.VillageObject{ID: id, AssetID: fenceAssetID, CurrentState: "h", Pos: sim.TilePos{X: x, Y: 10}.Center()}
		}
		// A second run beside the first, and a third far from any building.
		for _, f := range []*sim.VillageObject{fence("fence-4", 44), fence("fence-5", 45), fence("fence-6", 46),
			fence("fence-7", 90), fence("fence-8", 91), fence("fence-9", 92)} {
			world.VillageObjects[f.ID] = f
		}
		world.Assets["mansion-asset"] = &sim.Asset{ID: "mansion-asset", Name: "Mansion"}
		world.VillageObjects["mansion"] = &sim.VillageObject{ID: "mansion", DisplayName: "Mansion", AssetID: "mansion-asset", Pos: sim.TilePos{X: 43, Y: 14}.Center()}
		world.Structures["mansion"] = &sim.Structure{ID: "mansion", DisplayName: "Mansion"}
		world.Environment.TownChest = 100
	})
	for _, id := range []sim.VillageObjectID{"fence-5", "fence-2", "fence-8"} {
		if _, err := w.Send(sim.SetObjectDamage(id, "damage")); err != nil {
			t.Fatalf("break %s: %v", id, err)
		}
	}
	lines := sim.DamageTickerLines(w.Published())
	want := []sim.DamageTickerLine{
		{ObjectID: "fence-2", Text: "Rails have come down in two places on the fence by the Mansion — the town pays 3 coins for each one mended."},
		{ObjectID: "fence-8", Text: "A rail has come down on the fence — the town pays 3 coins to the hand who mends it."},
	}
	if len(lines) != len(want) {
		t.Fatalf("ticker = %+v, want %+v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("ticker[%d] = %+v, want %+v", i, lines[i], want[i])
		}
	}

	if _, err := w.Send(sim.SetObjectDamage("fence-2", "repair")); err != nil {
		t.Fatal(err)
	}
	lines = sim.DamageTickerLines(w.Published())
	if len(lines) != 2 || lines[0].ObjectID != "fence-5" ||
		lines[0].Text != "A rail has come down on the fence by the Mansion — the town pays 3 coins to the hand who mends it." {
		t.Errorf("ticker after one mend = %+v, want fence-5 alone in the single wording", lines)
	}
}
