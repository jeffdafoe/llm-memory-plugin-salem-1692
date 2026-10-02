package sim

import (
	"strings"
	"testing"
	"time"
)

// messenger_news_test.go — LLM-700: the messenger carries one item of outside news,
// cleaned to the clause the preface completes and refused when it names a villager
// or touches witchcraft or Salem.

func messengerNewsTestWorld() *World {
	return &World{
		Actors: map[ActorID]*Actor{
			"prudence": {ID: "prudence", DisplayName: "Prudence Ward", Kind: KindNPCStateful},
			"john":     {ID: "john", DisplayName: "John Ellis", Kind: KindNPCShared},
			"pc":       {ID: "pc", DisplayName: "Thomas Penhallow", Kind: KindPC},
			"grace":    {ID: "grace", DisplayName: "Grace Edwards", Kind: KindDecorative},
			"roger": {ID: "roger", DisplayName: "Roger Standish the messenger", Kind: KindNPCShared,
				VisitorState: &VisitorState{Archetype: MessengerArchetype, Phase: VisitorPhaseArriving}},
			"musician": {ID: "musician", DisplayName: "Ephraim Pollard the itinerant musician", Kind: KindNPCShared,
				VisitorState: &VisitorState{Archetype: "itinerant musician", Phase: VisitorPhaseArriving}},
		},
	}
}

func TestCleanMessengerNews(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Port Royal in Jamaica was swallowed by a great earthquake in June", "Port Royal in Jamaica was swallowed by a great earthquake in June"},
		{"that Port Royal sank into the sea.", "Port Royal sank into the sea"},
		{"That a stone fort was raised at Pemaquid", "a stone fort was raised at Pemaquid"},
		{"\"the French fleet was beaten off La Hogue\"", "the French fleet was beaten off La Hogue"},
		{"\n\n- a ship from London came into Boston with salt\nsecond line ignored", "a ship from London came into Boston with salt"},
		{"1. the frost came early at Ipswich", "the frost came early at Ipswich"},
		// A clause that opens on a number keeps it — only a list marker is dropped.
		{"300 men marched to the Maine frontier", "300 men marched to the Maine frontier"},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := CleanMessengerNews(c.in); got != c.want {
			t.Errorf("CleanMessengerNews(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCarriesOutsideNews(t *testing.T) {
	w := messengerNewsTestWorld()
	if !w.Actors["roger"].VisitorState.CarriesOutsideNews() {
		t.Error("a passer messenger should carry outside news")
	}
	if w.Actors["musician"].VisitorState.CarriesOutsideNews() {
		t.Error("a musician carries no news")
	}
	merchant := &VisitorState{Archetype: MessengerArchetype, Trade: &TradeErrand{Direction: TradeDirectionSell}}
	if merchant.CarriesOutsideNews() {
		t.Error("a merchant never carries the messenger's news, whatever his label")
	}
	var none *VisitorState
	if none.CarriesOutsideNews() {
		t.Error("a resident (nil VisitorState) carries no news")
	}
}

func TestSetMessengerNews(t *testing.T) {
	cases := []struct {
		name    string
		actor   ActorID
		prep    func(w *World)
		text    string
		want    MessengerNewsOutcome
		payload string
	}{
		{name: "installs cleaned news", actor: "roger",
			text: "that Port Royal in Jamaica was swallowed by a great earthquake in June.", want: MessengerNewsInstalled,
			payload: "Port Royal in Jamaica was swallowed by a great earthquake in June"},
		{name: "not a messenger", actor: "musician", text: "a ship came into Boston", want: MessengerNewsGone},
		{name: "unknown actor", actor: "nobody", text: "a ship came into Boston", want: MessengerNewsGone},
		{name: "already departing", actor: "roger", prep: func(w *World) { w.Actors["roger"].VisitorState.Phase = VisitorPhaseDeparting },
			text: "a ship came into Boston", want: MessengerNewsGone},
		{name: "never overwrites news he carries", actor: "roger", prep: func(w *World) { w.Actors["roger"].VisitorState.Payload = "a fort rose at Pemaquid" },
			text: "a ship came into Boston", want: MessengerNewsGone, payload: "a fort rose at Pemaquid"},
		{name: "empty reply", actor: "roger", text: "  \n ", want: MessengerNewsEmpty},
		{name: "too long", actor: "roger", text: strings.Repeat("a ship came in ", 30), want: MessengerNewsTooLong},
		{name: "witchcraft", actor: "roger", text: "the Governor halted the witch court at Boston", want: MessengerNewsBannedWord},
		{name: "Salem", actor: "roger", text: "word came from Salem Town of a fire", want: MessengerNewsBannedWord},
		{name: "names a villager by surname", actor: "roger", text: "Goodman Ward was seen at Boston selling cloth", want: MessengerNewsNamesVillager},
		{name: "names the player", actor: "roger", text: "a Penhallow ship was lost off Cape Ann", want: MessengerNewsNamesVillager},
		// Whole words only: "toward" holds "ward"; a decorative's name is not a villager's.
		{name: "surname inside another word is fine", actor: "roger", text: "the troops marched toward the Maine frontier", want: MessengerNewsInstalled,
			payload: "the troops marched toward the Maine frontier"},
		{name: "a decorative's name is not a villager's", actor: "roger", text: "a sloop under Captain Edwards came into Boston", want: MessengerNewsInstalled,
			payload: "a sloop under Captain Edwards came into Boston"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := messengerNewsTestWorld()
			if c.prep != nil {
				c.prep(w)
			}
			res, err := SetMessengerNews(c.actor, c.text).Fn(w)
			if err != nil {
				t.Fatalf("SetMessengerNews: %v", err)
			}
			if got := res.(MessengerNewsOutcome); got != c.want {
				t.Errorf("outcome = %q, want %q", got, c.want)
			}
			if a := w.Actors[c.actor]; a != nil && a.VisitorState != nil && a.VisitorState.Payload != c.payload {
				t.Errorf("payload = %q, want %q", a.VisitorState.Payload, c.payload)
			}
		})
	}
}

func TestMessengerNewsDate_UsesVillageTimeZone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	w := &World{Settings: WorldSettings{Location: loc}}
	// 02:30 UTC on October 3 is still the evening of October 2 in the village.
	month, day := MessengerNewsDate(w, time.Date(2026, 10, 3, 2, 30, 0, 0, time.UTC))
	if month != time.October || day != 2 {
		t.Errorf("date = %s %d, want October 2 (village local)", month, day)
	}
}
