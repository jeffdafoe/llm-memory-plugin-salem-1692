package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// court_notices_test.go — LLM-706 on a running world: a filing posts the
// hearing notice to the board and tells the client, the ruling replaces it, the
// minute sync takes it down after its notice days, the ticker carries both, and
// a board too small for every town notice drops whole notices.

func fileCourtCase(t *testing.T, w *sim.World, at time.Time) sim.CourtCaseID {
	t.Helper()
	res, err := w.Send(sim.FileCourtCase("gideon", []string{"Anne Walker"}, "Anne took the bread", at, false))
	if err != nil {
		t.Fatalf("FileCourtCase: %v", err)
	}
	return res.(sim.CourtFileResult).Case.ID
}

func TestCourtNoticesOnTheBoardAndTicker(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	news := 0
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if _, ok := evt.(*sim.DamageNewsChanged); ok {
				news++
			}
		}))
	})
	now := time.Now().UTC()
	id := fileCourtCase(t, w, now)

	mustSend(t, w, func(world *sim.World) {
		c := world.NoticeboardContent["board"]
		if c == nil || c.Pinned != 2 || !strings.Contains(c.Text, "A matter concerning Anne Walker goes before the magistrates") {
			t.Errorf("board after filing = %+v, want the hearing notice pinned", c)
			return
		}
		if strings.Contains(c.Text, "bread") {
			t.Errorf("the complaint reached the board: %q", c.Text)
		}
		if news != 1 {
			t.Errorf("news events after filing = %d, want 1", news)
		}
	})
	if lines := sim.CourtTickerLines(w.Published()); len(lines) != 1 || lines[0].CaseID != id || !strings.HasPrefix(lines[0].Text, "A matter concerning Anne Walker") {
		t.Errorf("ticker after filing = %+v", lines)
	}

	ruledAt := now.Add(time.Minute)
	if _, err := w.Send(sim.ApplyCourtRuling(id, sim.CourtRuling{Result: sim.CourtResultNoCase, Words: "Anne Walker: no bread was taken."}, ruledAt)); err != nil {
		t.Fatalf("ApplyCourtRuling: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		c := world.NoticeboardContent["board"]
		if c == nil || c.Pinned != 2 || !strings.Contains(c.Text, "They found no case. The matter is closed.") || strings.Contains(c.Text, "goes before") {
			t.Errorf("board after the ruling = %+v, want the ruling in place of the hearing", c)
			return
		}
	})
	if lines := sim.CourtTickerLines(w.Published()); len(lines) != 1 || !strings.Contains(lines[0].Text, "have ruled on the matter concerning Anne Walker. They found no case.") {
		t.Errorf("ticker after the ruling = %+v", lines)
	}

	// The minute sync inside the window changes nothing; past it, the notice comes down.
	if _, err := w.Send(sim.SyncTownNotices(ruledAt.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if news != 2 {
			t.Errorf("news events = %d after a no-change sync, want 2", news)
		}
	})
	if _, err := w.Send(sim.SyncTownNotices(ruledAt.AddDate(0, 0, sim.CourtRulingNoticeDays).Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if c := world.NoticeboardContent["board"]; c != nil && c.Text != "" {
			t.Errorf("board after the notice days = %+v, want it cleared", c)
		}
		if got := world.VillageObjects["board"].CurrentState; got != "empty" {
			t.Errorf("board state = %q, want empty", got)
		}
		if news != 3 {
			t.Errorf("news events = %d after expiry, want 3", news)
		}
	})
}

// TestTownNoticesDropWholeNoticesWhenTheBoardIsFull — a broken well and a
// hearing make four pinned lines on a board whose largest frame holds three:
// the board keeps the well's two lines, never three lines with half a notice.
func TestTownNoticesDropWholeNoticesWhenTheBoardIsFull(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	breakWell(t, w, "well-a")
	fileCourtCase(t, w, time.Now().UTC())
	mustSend(t, w, func(world *sim.World) {
		if got := len(sim.TownNoticeLines(world, time.Now().UTC())); got != 4 {
			t.Errorf("town notice lines = %d, want 4 (well + hearing)", got)
			return
		}
		c := world.NoticeboardContent["board"]
		if c == nil || c.Pinned != 2 || len(strings.Split(c.Text, "\n")) != 2 {
			t.Errorf("board = %+v, want exactly the well's two lines", c)
			return
		}
		if strings.Contains(c.Text, "magistrates") {
			t.Errorf("half a court notice reached the board: %q", c.Text)
		}
		if got := world.VillageObjects["board"].CurrentState; got != "two" {
			t.Errorf("board state = %q, want the two-slip frame", got)
		}
	})
}
