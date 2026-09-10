package sim

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// estate_rate_internal_test.go — LLM-652 and its collection slice. Internal
// (package sim) coverage for the levy's pieces: the pure rate rule, who it falls
// on, the collection at the constable's door (through the beat credit and the
// exported command), the once-a-day stamp, the records, and the wage the chest
// pays. Reaches the functions directly against a hand-built World, mirroring
// farm_upkeep_internal_test.go. The rateable-business and constable predicates
// the levy is scoped by are covered at the bottom (moved here from the retired
// LLM-557 town rate in LLM-655).

func TestEstateRateDue(t *testing.T) {
	cases := []struct {
		name              string
		coins, floor, pct int
		want              int
	}{
		// The live roster on 2026-09-08 at the defaults.
		{"Joseph 864", 864, 100, 5, 38},
		{"Prudence 486", 486, 100, 5, 19},
		{"Ezekiel 339", 339, 100, 5, 11}, // (339-100)*5/100 = 11.95 → 11
		{"Moses 47 is under the floor", 47, 100, 5, 0},
		{"exactly at the floor", 100, 100, 5, 0},
		{"one above the floor rounds to nothing", 101, 100, 5, 0},
		{"twenty above the floor is the first whole coin", 120, 100, 5, 1},
		{"pct 0 disables", 864, 100, 0, 0},
		{"negative pct disables", 864, 100, -5, 0},
		{"floor 0 taxes from the first coin", 200, 0, 5, 10},
		{"a higher rate", 864, 100, 10, 76},
		// A percentage above 100 is refused by the setter but could still arrive
		// from a malformed persisted row; it must never debit below the floor.
		{"pct above 100 is clamped to the excess", 200, 100, 101, 100},
		{"an absurd pct still takes only the excess", 200, 100, 1_000_000, 100},
		{"empty purse", 0, 100, 5, 0},
		{"a negative balance owes nothing", -5, 100, 5, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EstateRateDue(c.coins, c.floor, c.pct); got != c.want {
				t.Errorf("EstateRateDue(coins=%d, floor=%d, pct=%d) = %d, want %d",
					c.coins, c.floor, c.pct, got, c.want)
			}
		})
	}
}

// estateRateWorld builds the collection's scope matrix: every actor kind the
// village carries, each standing INSIDE the business it owns and all holding the
// same surplus, so the collection has to tell them apart by predicate and nothing
// else. The constable stands at his post. No assets, so the loiter-pin arm of
// AtBusiness is off and "at the business" means InsideStructureID == the object.
func estateRateWorld() *World {
	w := &World{
		Settings: WorldSettings{
			EstateRateFloor:     100,
			EstateRatePctPerDay: 5,
			ConstableWagePerDay: 8,
			Location:            time.UTC,
			RotationTime:        "00:00",
		},
		Actors: map[ActorID]*Actor{
			"joseph":   {ID: "joseph", DisplayName: "Joseph Scott", Kind: KindNPCShared, Coins: 864, InsideStructureID: "mill"},
			"prudence": {ID: "prudence", DisplayName: "Prudence Ward", Kind: KindNPCStateful, Coins: 486, InsideStructureID: "apothecary"},
			"moses":    {ID: "moses", DisplayName: "Moses James", Kind: KindNPCShared, Coins: 47, InsideStructureID: "farm"},
			"wendy":    {ID: "wendy", DisplayName: "Wendy", Kind: KindPC, Coins: 500, InsideStructureID: "store"},
			"cow":      {ID: "cow", DisplayName: "Villager 3", Kind: KindDecorative, Coins: 500, InsideStructureID: "pen"},
			"vstr-0a1b2c3d": {
				ID: "vstr-0a1b2c3d", DisplayName: "Jonas Penhallow the factor",
				Kind: KindNPCShared, Coins: 500, VisitorState: &VisitorState{}, InsideStructureID: "wagon",
			},
			"gideon": {
				ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: KindNPCStateful, Coins: 7,
				Attributes:        map[string][]byte{AttrConstable: nil},
				InsideStructureID: "meeting_house", WorkStructureID: "meeting_house",
			},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"mill":       {ID: "mill", DisplayName: "Mill", OwnerActorID: "joseph", Tags: []string{TagBusiness}},
			"apothecary": {ID: "apothecary", DisplayName: "PW Apothecary", OwnerActorID: "prudence", Tags: []string{TagBusiness}},
			"farm":       {ID: "farm", DisplayName: "James Farm", OwnerActorID: "moses", Tags: []string{TagBusiness}},
			"store":      {ID: "store", DisplayName: "General Store", OwnerActorID: "wendy", Tags: []string{TagBusiness}},
			"pen":        {ID: "pen", DisplayName: "Cow Pen", OwnerActorID: "cow", Tags: []string{TagBusiness}},
			"wagon":      {ID: "wagon", DisplayName: "Factor's Wagon", OwnerActorID: "vstr-0a1b2c3d", Tags: []string{TagBusiness}},
			"derelict":   {ID: "derelict", DisplayName: "Empty Shop", Tags: []string{TagBusiness}},
			"well":       {ID: "well", DisplayName: "Old Well", OwnerActorID: "joseph"},
		},
		ActiveRoutes: map[ActorID]*NPCRoute{},
	}
	return w
}

// estateRateNoon is a fixed instant inside a UTC game-day, well clear of the
// midnight boundary.
var estateRateNoon = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

func totalCoins(w *World) int {
	sum := 0
	for _, a := range w.Actors {
		sum += a.Coins
	}
	return sum
}

func collectAt(t *testing.T, w *World, business VillageObjectID, now time.Time) {
	t.Helper()
	if _, err := CollectEstateRate("gideon", business, now).Fn(w); err != nil {
		t.Fatalf("CollectEstateRate(%s): %v", business, err)
	}
}

func TestCollectEstateRate_TakesTheDueAtTheDoor(t *testing.T) {
	w := estateRateWorld()
	w.Environment.TownChest = 300 // a chest already holding earlier days' rate
	before := totalCoins(w) + w.Environment.TownChest

	collectAt(t, w, "mill", estateRateNoon)

	if got := w.Actors["joseph"].Coins; got != 864-38 {
		t.Errorf("Joseph holds %d after the constable called, want %d", got, 864-38)
	}
	if w.Environment.TownChest != 300+38 {
		t.Errorf("TownChest = %d, want %d — the chest must gain exactly what left the purse", w.Environment.TownChest, 300+38)
	}
	if got := w.Actors["gideon"].Coins; got != 7 {
		t.Errorf("the constable holds %d, want 7 — he is the trigger, never the holder", got)
	}
	if stamp := w.Actors["joseph"].EstateRateAssessedAt; stamp == nil || !stamp.Equal(estateRateNoon) {
		t.Errorf("Joseph's EstateRateAssessedAt = %v, want the visit's instant %v", stamp, estateRateNoon)
	}
	for id, a := range w.Actors {
		if id != "joseph" && a.EstateRateAssessedAt != nil {
			t.Errorf("%s was stamped by a visit to the Mill", id)
		}
	}
	if after := totalCoins(w) + w.Environment.TownChest; after != before {
		t.Errorf("Σ purses + chest = %d after the collection, want %d — the levy must be coin-neutral", after, before)
	}
}

// The scope matrix: only a resident NPC standing at the business he owns pays,
// and only to a constable.
func TestCollectEstateRate_WhoPays(t *testing.T) {
	cases := []struct {
		name     string
		business VillageObjectID
		owner    ActorID
		paid     bool
	}{
		{"a shared-VA keeper", "mill", "joseph", true},
		{"a stateful keeper", "apothecary", "prudence", true},
		{"a keeper under the floor", "farm", "moses", false},
		{"a player", "store", "wendy", false},
		{"a decorative", "pen", "cow", false},
		{"a visitor", "wagon", "vstr-0a1b2c3d", false},
		{"an unowned business", "derelict", "", false},
		{"an owned object that is not a business", "well", "joseph", false},
		{"a stop that is no object at all", "nowhere", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := estateRateWorld()
			before := map[ActorID]int{}
			for id, a := range w.Actors {
				before[id] = a.Coins
			}
			collectAt(t, w, c.business, estateRateNoon)
			for id, a := range w.Actors {
				moved := a.Coins != before[id]
				if moved != (c.paid && id == c.owner) {
					t.Errorf("%s: coins %d → %d", id, before[id], a.Coins)
				}
			}
			if (w.Environment.TownChest > 0) != c.paid {
				t.Errorf("chest = %d", w.Environment.TownChest)
			}
		})
	}

	t.Run("a caller who is not a constable", func(t *testing.T) {
		w := estateRateWorld()
		delete(w.Actors["gideon"].Attributes, AttrConstable)
		collectAt(t, w, "mill", estateRateNoon)
		if w.Actors["joseph"].Coins != 864 || w.Environment.TownChest != 0 {
			t.Errorf("a non-constable collected: Joseph %d, chest %d", w.Actors["joseph"].Coins, w.Environment.TownChest)
		}
	})
	t.Run("a caller who does not exist", func(t *testing.T) {
		w := estateRateWorld()
		if _, err := CollectEstateRate("nobody", "mill", estateRateNoon).Fn(w); err == nil {
			t.Error("CollectEstateRate with an unknown constable returned no error")
		}
	})
}

// The keeper has to be there. An owner away buying water is simply not collected
// from on this visit — no stamp either, so the next round can still catch him.
func TestCollectEstateRate_OwnerAwayCollectsNothing(t *testing.T) {
	w := estateRateWorld()
	w.Actors["joseph"].InsideStructureID = "" // out on the road
	w.Actors["joseph"].Pos = TilePos{X: 500, Y: 500}

	collectAt(t, w, "mill", estateRateNoon)

	if w.Actors["joseph"].Coins != 864 || w.Environment.TownChest != 0 {
		t.Errorf("collected from an absent owner: Joseph %d, chest %d", w.Actors["joseph"].Coins, w.Environment.TownChest)
	}
	if w.Actors["joseph"].EstateRateAssessedAt != nil {
		t.Error("an absent owner was stamped as assessed — the afternoon round could never catch him")
	}
	if len(w.ActionLog) != 0 {
		t.Errorf("%d action-log entries written for a visit that collected nothing", len(w.ActionLog))
	}
}

func TestCollectEstateRate_OffSwitchLeavesEverythingAlone(t *testing.T) {
	w := estateRateWorld()
	w.Settings.EstateRatePctPerDay = 0
	w.Environment.TownChest = 7

	collectAt(t, w, "mill", estateRateNoon)

	if w.Actors["joseph"].Coins != 864 {
		t.Errorf("Joseph's coins = %d with the levy disabled, want 864", w.Actors["joseph"].Coins)
	}
	if w.Environment.TownChest != 7 {
		t.Errorf("TownChest = %d with the levy disabled, want 7 (untouched)", w.Environment.TownChest)
	}
	if w.Actors["joseph"].EstateRateAssessedAt != nil {
		t.Error("a disabled levy still stamped the purse")
	}
	if len(w.ActionLog) != 0 {
		t.Errorf("%d action-log entries written with the levy disabled, want 0", len(w.ActionLog))
	}
}

// Once per game-day, keyed to the rotation boundary in the world's timezone: a
// second call the same day takes nothing, the next day's call takes the rate on
// the reduced purse, and an assessment that found nothing due still counts as the
// day's assessment (no "once a day, unless you got richer").
func TestCollectEstateRate_OncePerGameDay(t *testing.T) {
	w := estateRateWorld()

	collectAt(t, w, "mill", estateRateNoon)
	collectAt(t, w, "mill", estateRateNoon.Add(2*time.Hour))
	collectAt(t, w, "mill", estateRateNoon.Add(11*time.Hour+59*time.Minute)) // 23:59, still today
	if got := w.Actors["joseph"].Coins; got != 826 {
		t.Errorf("Joseph holds %d after three same-day visits, want 826 (one collection)", got)
	}
	if w.Environment.TownChest != 38 {
		t.Errorf("chest = %d after three same-day visits, want 38", w.Environment.TownChest)
	}

	nextDay := estateRateNoon.Add(24 * time.Hour)
	collectAt(t, w, "mill", nextDay)
	// (826-100)*5/100 = 36.3 → 36
	if got := w.Actors["joseph"].Coins; got != 826-36 {
		t.Errorf("Joseph holds %d after the next day's visit, want %d", got, 826-36)
	}
	if stamp := w.Actors["joseph"].EstateRateAssessedAt; stamp == nil || !stamp.Equal(nextDay) {
		t.Errorf("stamp = %v after the next day's visit, want %v", stamp, nextDay)
	}

	// Under the floor at the morning call, rich by the afternoon: today's
	// assessment already happened.
	w.Actors["moses"].Coins = 47
	collectAt(t, w, "farm", nextDay)
	if w.Actors["moses"].EstateRateAssessedAt == nil {
		t.Fatal("a purse under the floor was not stamped as assessed")
	}
	w.Actors["moses"].Coins = 900
	collectAt(t, w, "farm", nextDay.Add(3*time.Hour))
	if w.Actors["moses"].Coins != 900 {
		t.Errorf("Moses holds %d — assessed twice in one day", w.Actors["moses"].Coins)
	}
}

// The game-day is the rotation's day, not the calendar's: with a 06:00 rotation, a
// 05:00 visit belongs to the previous game-day and a 07:00 visit to the next.
func TestCollectEstateRate_GameDayFollowsTheRotationBoundary(t *testing.T) {
	w := estateRateWorld()
	w.Settings.RotationTime = "06:00"
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tz database unavailable")
	}
	w.Settings.Location = loc
	// 2026-09-09 05:00 ET and 07:00 ET, expressed in UTC — the collection converts
	// to the world's location itself.
	five := time.Date(2026, 9, 9, 5, 0, 0, 0, loc).UTC()
	seven := time.Date(2026, 9, 9, 7, 0, 0, 0, loc).UTC()

	collectAt(t, w, "mill", five)
	collectAt(t, w, "mill", seven)
	if got := w.Actors["joseph"].Coins; got != 826-36 {
		t.Errorf("Joseph holds %d, want %d — 05:00 and 07:00 straddle a 06:00 rotation and are two game-days", got, 826-36)
	}
	collectAt(t, w, "mill", seven.Add(10*time.Hour))
	if got := w.Actors["joseph"].Coins; got != 826-36 {
		t.Errorf("Joseph holds %d — 17:00 is the same game-day as 07:00", got)
	}
}

// The collection rides the beat credit: the rounds machinery recording the
// constable at a business stop is what fires it. Through advanceBeatRoute itself,
// with the constable inside the business (the enter arm of ActorAtRouteStopPlace —
// the real circuit enters every open business) and the owner inside too.
func TestBeatCredit_CollectsTheEstateRate(t *testing.T) {
	w := estateRateWorld()
	gideon := w.Actors["gideon"]
	gideon.InsideStructureID = "mill" // he has just walked in
	route := &NPCRoute{
		NPCID: "gideon", Label: AttrConstable, Phase: RoutePhaseBeat,
		Stops: []RouteStop{
			{ObjectID: "mill", EnterStructureID: "mill"},
			{ObjectID: "apothecary", EnterStructureID: "apothecary"},
		},
		Visited: make([]bool, 2),
	}
	w.ActiveRoutes["gideon"] = route

	res, err := advanceBeatRoute(w, route, estateRateNoon)
	if err != nil {
		t.Fatalf("advanceBeatRoute: %v", err)
	}
	if res.Reason != "beat_stop_called" {
		t.Errorf("Reason = %q, want beat_stop_called", res.Reason)
	}
	if got := w.Actors["joseph"].Coins; got != 826 {
		t.Errorf("Joseph holds %d after the constable was credited at the Mill, want 826", got)
	}
	if w.Environment.TownChest != 38 {
		t.Errorf("chest = %d, want 38", w.Environment.TownChest)
	}
	if w.Actors["prudence"].Coins != 486 {
		t.Errorf("Prudence holds %d — the apothecary was not called at", w.Actors["prudence"].Coins)
	}

	// A duplicate or stale arrival at the stop he was just credited at — the
	// cascade re-running the beat advance for the same visit — must not collect
	// again, and the once-a-day stamp is not what stops it: this repeat is
	// processed AFTER the game-day boundary, when a fresh assessment would be
	// owed. The beat credit itself is the guard (reachedStopIndex answers only
	// with an unvisited stop).
	w.Actors["joseph"].Coins = 900
	res, err = advanceBeatRoute(w, route, estateRateNoon.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("advanceBeatRoute (repeat): %v", err)
	}
	if res.Reason != "beat_elsewhere" {
		t.Errorf("Reason = %q on a repeat arrival at a credited stop, want beat_elsewhere", res.Reason)
	}
	if w.Actors["joseph"].Coins != 900 || w.Environment.TownChest != 38 {
		t.Errorf("a repeat arrival collected again: Joseph %d, chest %d", w.Actors["joseph"].Coins, w.Environment.TownChest)
	}

	// Standing somewhere that is no stop credits nothing and collects nothing.
	gideon.InsideStructureID = "tavern"
	res, err = advanceBeatRoute(w, route, estateRateNoon.Add(time.Minute))
	if err != nil {
		t.Fatalf("advanceBeatRoute (elsewhere): %v", err)
	}
	if res.Reason != "beat_elsewhere" {
		t.Errorf("Reason = %q, want beat_elsewhere", res.Reason)
	}
	if w.Environment.TownChest != 38 {
		t.Errorf("chest = %d after an off-stop arrival, want 38", w.Environment.TownChest)
	}

	// Through the exported command the cascade uses, with the arrival's own
	// timestamp: the last stop completes the round and still collects.
	gideon.InsideStructureID = "apothecary"
	at := estateRateNoon.Add(2 * time.Hour)
	out, err := AdvanceNPCRouteSkipFlipAt("gideon", at).Fn(w)
	if err != nil {
		t.Fatalf("AdvanceNPCRouteSkipFlipAt: %v", err)
	}
	if r := out.(AdvanceNPCRouteResult); r.Reason != "beat_complete" {
		t.Errorf("Reason = %q, want beat_complete", r.Reason)
	}
	if got := w.Actors["prudence"].Coins; got != 486-19 {
		t.Errorf("Prudence holds %d after the round's last stop, want %d", got, 486-19)
	}
	if stamp := w.Actors["prudence"].EstateRateAssessedAt; stamp == nil || !stamp.Equal(at) {
		t.Errorf("Prudence's stamp = %v, want the arrival instant %v", stamp, at)
	}
	if _, still := w.ActiveRoutes["gideon"]; still {
		t.Error("the completed round was not cleared")
	}
}

// Both parties' records are written in the same command as the debit, and they
// say what the coin was (the LLM-572 lesson): the payer's ring entry names the
// constable, the constable's names the payer as a `collected` beat, the payer's
// durable row carries the estate_rate marker and NO recipient actor id, and the
// shared-VA payer gets a relationship fact that closes the door on a refund.
func TestCollectEstateRate_WritesTheRecords(t *testing.T) {
	w := estateRateWorld()
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	w.Actors["joseph"].CurrentHuddleID = "hud-1"

	collectAt(t, w, "mill", estateRateNoon)

	if len(w.ActionLog) != 2 {
		t.Fatalf("ring entries = %d, want 2 (payer + collector)", len(w.ActionLog))
	}
	byActor := map[ActorID]ActionLogEntry{}
	for _, e := range w.ActionLog {
		byActor[e.ActorID] = e
		if e.OccurredAt != estateRateNoon || e.Amount != 38 {
			t.Errorf("%s: ring entry stamped %v for %d, want %v / 38", e.ActorID, e.OccurredAt, e.Amount, estateRateNoon)
		}
	}
	if p := byActor["joseph"]; p.ActionType != ActionTypePaid || p.CounterpartyName != "Constable Gideon Marsh" || p.Text != "the rate on your estate" || p.HuddleID != "hud-1" {
		t.Errorf("payer ring entry = %+v", p)
	}
	if c := byActor["gideon"]; c.ActionType != ActionTypeCollected || c.CounterpartyName != "Joseph Scott" || c.Text != "the rate on their estate" {
		t.Errorf("collector ring entry = %+v", c)
	}

	if len(sink.rows) != 2 {
		t.Fatalf("durable rows = %d, want 2", len(sink.rows))
	}
	for _, row := range sink.rows {
		if row.Source != "engine" || row.Payload["estate_rate"] != true || row.Payload["amount"] != 38 || row.Payload["chest_after"] != 38 {
			t.Errorf("%s: durable row = %s/%s %v", row.ActorID, row.ActionType, row.Source, row.Payload)
		}
		if row.SpeakerName != w.Actors[row.ActorID].DisplayName {
			t.Errorf("%s: SpeakerName = %q", row.ActorID, row.SpeakerName)
		}
		switch row.ActorID {
		case "joseph":
			if row.ActionType != ActionTypePaid || row.Payload["recipient"] != "the town" || row.Payload["for"] != "the rate on your estate" || row.Payload["collected_by"] != "Constable Gideon Marsh" || row.Payload["collected_by_actor_id"] != "gideon" {
				t.Errorf("payer durable row = %v", row.Payload)
			}
			if _, has := row.Payload["recipient_actor_id"]; has {
				t.Error("payer durable row names a recipient actor — the chest is not an actor")
			}
		case "gideon":
			if row.ActionType != ActionTypeCollected || row.Payload["payer"] != "Joseph Scott" || row.Payload["payer_actor_id"] != "joseph" || row.Payload["for"] != "the rate on their estate" {
				t.Errorf("collector durable row = %v", row.Payload)
			}
		default:
			t.Errorf("durable row for %s", row.ActorID)
		}
	}

	// No coin-pair record: the chest is not a peer and the constable never held it.
	if pairs := countCoinPairs(w.CoinRecord); pairs != 0 {
		t.Errorf("coin record holds %d pair(s) after the collection, want 0", pairs)
	}

	// The shared-VA payer's memory of the constable says what the coin was.
	rel := w.Actors["joseph"].Relationships["gideon"]
	if rel == nil || len(rel.SalientFacts) != 1 {
		t.Fatalf("Joseph's relationship with the constable = %+v, want one salient fact", rel)
	}
	fact := rel.SalientFacts[0].Text
	for _, want := range []string{"I paid Constable Gideon Marsh the rate on my estate, 38 coins", "taken into the town chest", "none are owed in return"} {
		if !strings.Contains(fact, want) {
			t.Errorf("fact %q lacks %q", fact, want)
		}
	}
	// The collector's wording names the payer's estate and the chest, never "paid
	// me" — the constable did not receive it. He is stateful, so RecordInteraction
	// stores nothing for him; the text is pinned directly.
	got := estateRateCollectedFactText("Joseph Scott", 38)
	for _, want := range []string{"Joseph Scott paid the rate on their estate, 38 coins", "I collected it into the town chest", "none are owed in return"} {
		if !strings.Contains(got, want) {
			t.Errorf("collector fact %q lacks %q", got, want)
		}
	}
	for _, bad := range []string{"paid me", "my estate"} {
		if strings.Contains(got, bad) {
			t.Errorf("collector fact %q says %q", got, bad)
		}
	}
}

// A nameless actor still gets a named counterparty in the trail — the same
// fallback the durable row uses, so the two records never disagree on who.
func TestCollectEstateRate_RingNamesFallBackToIDs(t *testing.T) {
	w := estateRateWorld()
	w.Actors["joseph"].DisplayName = ""
	w.Actors["gideon"].DisplayName = ""

	collectAt(t, w, "mill", estateRateNoon)

	for _, e := range w.ActionLog {
		want := map[ActorID]string{"joseph": "gideon", "gideon": "joseph"}[e.ActorID]
		if e.CounterpartyName != want {
			t.Errorf("%s: ring counterparty = %q, want the id %q", e.ActorID, e.CounterpartyName, want)
		}
	}
}

func TestEstateRateAssessable(t *testing.T) {
	if estateRateAssessable(nil) {
		t.Error("nil actor is assessable")
	}
	w := estateRateWorld()
	want := map[ActorID]bool{
		"joseph": true, "prudence": true, "moses": true,
		"wendy": false, "cow": false, "vstr-0a1b2c3d": false, "gideon": false,
	}
	for id, ok := range want {
		if got := estateRateAssessable(w.Actors[id]); got != ok {
			t.Errorf("estateRateAssessable(%s) = %v, want %v", id, got, ok)
		}
	}
	// A visitor is excluded by id shape alone, even before VisitorState is stamped.
	bare := &Actor{ID: "vstr-deadbeef", Kind: KindNPCShared, Coins: 500}
	if estateRateAssessable(bare) {
		t.Error("a vstr- id with no VisitorState is assessable")
	}
}

// A setter-side guard for the same invariant: the registry refuses a percentage
// above 100 outright, so a live tune cannot put the levy into the state
// EstateRateDue clamps against. The wage refuses a negative.
func TestEstateRateSettingsAreBounded(t *testing.T) {
	ws := WorldSettings{}
	if _, err := ApplySetting(&ws, "estate_rate_pct_per_day", "101"); err == nil {
		t.Error("ApplySetting accepted estate_rate_pct_per_day=101")
	}
	if _, err := ApplySetting(&ws, "estate_rate_pct_per_day", "-1"); err == nil {
		t.Error("ApplySetting accepted estate_rate_pct_per_day=-1")
	}
	for _, ok := range []string{"0", "5", "100"} {
		if _, err := ApplySetting(&ws, "estate_rate_pct_per_day", ok); err != nil {
			t.Errorf("ApplySetting rejected estate_rate_pct_per_day=%s: %v", ok, err)
		}
	}
	if ws.EstateRatePctPerDay != 100 {
		t.Errorf("EstateRatePctPerDay = %d after the last accepted write, want 100", ws.EstateRatePctPerDay)
	}
	if _, err := ApplySetting(&ws, "constable_wage_per_day", "-1"); err == nil {
		t.Error("ApplySetting accepted constable_wage_per_day=-1")
	}
	if _, err := ApplySetting(&ws, "constable_wage_per_day", "8"); err != nil {
		t.Errorf("ApplySetting rejected constable_wage_per_day=8: %v", err)
	}
	if ws.ConstableWagePerDay != 8 {
		t.Errorf("ConstableWagePerDay = %d, want 8", ws.ConstableWagePerDay)
	}
}

// A positive-balance actor never ends a collection below the floor, whatever the
// (possibly malformed) percentage — the invariant the floor exists for.
func TestCollectEstateRate_NeverDebitsBelowTheFloor(t *testing.T) {
	for _, pct := range []int{1, 5, 50, 100, 101, 5000} {
		w := estateRateWorld()
		w.Settings.EstateRatePctPerDay = pct
		before := map[ActorID]int{}
		for id, a := range w.Actors {
			before[id] = a.Coins
		}
		for id := range w.VillageObjects {
			collectAt(t, w, id, estateRateNoon)
		}
		for id, a := range w.Actors {
			floor := w.Settings.EstateRateFloor
			switch {
			case before[id] <= floor && a.Coins != before[id]:
				t.Errorf("pct %d: %s started at %d (at or under the floor) and was debited to %d", pct, id, before[id], a.Coins)
			case before[id] > floor && a.Coins < floor:
				t.Errorf("pct %d: %s ended at %d coins, below the floor %d", pct, id, a.Coins, floor)
			}
		}
	}
}

// Day after day the purse decays geometrically toward floor + income/rate and
// never crosses the floor.
func TestCollectEstateRate_ConvergesOnTheFloor(t *testing.T) {
	w := estateRateWorld()
	prev := w.Actors["joseph"].Coins
	for day := 0; day < 400; day++ {
		collectAt(t, w, "mill", estateRateNoon.Add(time.Duration(day)*24*time.Hour))
		got := w.Actors["joseph"].Coins
		if got > prev {
			t.Fatalf("day %d: coins rose %d → %d", day, prev, got)
		}
		if got < w.Settings.EstateRateFloor {
			t.Fatalf("day %d: coins %d fell below the floor %d", day, got, w.Settings.EstateRateFloor)
		}
		prev = got
	}
	// 5% of the excess, floored: the last whole coin is taken when the excess is
	// 20, so the purse settles at floor + 19.
	if got := w.Actors["joseph"].Coins; got != 119 {
		t.Errorf("after 400 days Joseph holds %d, want 119 (floor 100 + the 19 the rounding leaves)", got)
	}
}

// The chest pays the constable: the wage, capped by the chest, with the records.
func TestPayConstableWage(t *testing.T) {
	t.Run("a full chest pays the wage", func(t *testing.T) {
		w := estateRateWorld()
		w.Environment.TownChest = 100
		sink := &recordingActionLogSink{}
		w.SetActionLogSink(sink)
		before := totalCoins(w) + w.Environment.TownChest

		payConstableWage(w, estateRateNoon)

		if got := w.Actors["gideon"].Coins; got != 7+8 {
			t.Errorf("the constable holds %d, want 15", got)
		}
		if w.Environment.TownChest != 92 {
			t.Errorf("chest = %d, want 92", w.Environment.TownChest)
		}
		if after := totalCoins(w) + w.Environment.TownChest; after != before {
			t.Errorf("Σ purses + chest = %d after the wage, want %d — the wage must mint nothing", after, before)
		}
		for id, a := range w.Actors {
			if id != "gideon" && a.Coins != estateRateWorld().Actors[id].Coins {
				t.Errorf("%s: coins moved on payday", id)
			}
		}
		if len(w.ActionLog) != 1 {
			t.Fatalf("ring entries = %d, want 1", len(w.ActionLog))
		}
		if e := w.ActionLog[0]; e.ActorID != "gideon" || e.ActionType != ActionTypeCollected || e.CounterpartyName != "the town" || e.Text != "your wage as constable" || e.Amount != 8 {
			t.Errorf("ring entry = %+v", e)
		}
		if len(sink.rows) != 1 {
			t.Fatalf("durable rows = %d, want 1", len(sink.rows))
		}
		if r := sink.rows[0]; r.ActorID != "gideon" || r.ActionType != ActionTypeCollected || r.Source != "engine" || r.Payload["town_wage"] != true || r.Payload["amount"] != 8 || r.Payload["payer"] != "the town" || r.Payload["chest_after"] != 92 {
			t.Errorf("durable row = %s/%s %v", r.ActionType, r.Source, r.Payload)
		}
		if pairs := countCoinPairs(w.CoinRecord); pairs != 0 {
			t.Errorf("coin record holds %d pair(s) after the wage, want 0", pairs)
		}
	})

	t.Run("a thin chest pays what it has", func(t *testing.T) {
		w := estateRateWorld()
		w.Environment.TownChest = 5
		payConstableWage(w, estateRateNoon)
		if w.Actors["gideon"].Coins != 7+5 || w.Environment.TownChest != 0 {
			t.Errorf("constable %d, chest %d; want 12 / 0", w.Actors["gideon"].Coins, w.Environment.TownChest)
		}
	})

	t.Run("an empty chest pays nothing and writes nothing", func(t *testing.T) {
		w := estateRateWorld()
		payConstableWage(w, estateRateNoon)
		if w.Actors["gideon"].Coins != 7 || len(w.ActionLog) != 0 {
			t.Errorf("constable %d, ring entries %d; want 7 / 0", w.Actors["gideon"].Coins, len(w.ActionLog))
		}
	})

	t.Run("the off-switch", func(t *testing.T) {
		w := estateRateWorld()
		w.Settings.ConstableWagePerDay = 0
		w.Environment.TownChest = 100
		payConstableWage(w, estateRateNoon)
		if w.Actors["gideon"].Coins != 7 || w.Environment.TownChest != 100 {
			t.Errorf("constable %d, chest %d with the wage disabled; want 7 / 100", w.Actors["gideon"].Coins, w.Environment.TownChest)
		}
	})

	t.Run("two constables share a thin chest in id order", func(t *testing.T) {
		w := estateRateWorld()
		w.Actors["abel"] = &Actor{ID: "abel", DisplayName: "Constable Abel Frost", Kind: KindNPCStateful, Attributes: map[string][]byte{AttrConstable: nil}}
		w.Environment.TownChest = 10
		payConstableWage(w, estateRateNoon)
		if w.Actors["abel"].Coins != 8 || w.Actors["gideon"].Coins != 7+2 || w.Environment.TownChest != 0 {
			t.Errorf("abel %d, gideon %d, chest %d; want 8 / 9 / 0", w.Actors["abel"].Coins, w.Actors["gideon"].Coins, w.Environment.TownChest)
		}
	})
}

// The umbilical force-rotate calls ApplyDailyRotation directly; the wage is bound
// to the boundary crossing in checkAndRotate instead, so a forced rotation must
// never pay it (the same seam the farm-upkeep and day's-rate passes use), and the
// levy itself is not the rotation's to take at all.
func TestApplyDailyRotation_NeitherCollectsNorPays(t *testing.T) {
	w := estateRateWorld()
	w.Environment.TownChest = 11
	before := map[ActorID]int{}
	for id, a := range w.Actors {
		before[id] = a.Coins
	}
	if _, err := ApplyDailyRotation(RotationTickInputs{Now: estateRateNoon, Rand: rand.New(rand.NewSource(1))}, RotationScope{}).Fn(w); err != nil {
		t.Fatalf("ApplyDailyRotation: %v", err)
	}
	if w.Environment.TownChest != 11 {
		t.Errorf("TownChest = %d after a bare rotation, want 11 — the rotation itself must move nothing", w.Environment.TownChest)
	}
	for id, a := range w.Actors {
		if a.Coins != before[id] {
			t.Errorf("%s: coins %d → %d across a bare rotation", id, before[id], a.Coins)
		}
	}
}
