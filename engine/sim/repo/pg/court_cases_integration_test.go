package pg

// Real-pg integration tests for the magistrates (LLM-695): the docket and its
// rulings round-trip through SaveWorld → LoadWorld against the real court_case
// columns and CHECKs, and LoadDealingsBetween returns what passed between two
// villagers and nothing else.

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestIntegration_CourtCases_RoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	w := checkpointableWorld(repo)
	w.CourtCases = map[sim.CourtCaseID]*sim.CourtCase{
		"case-0000aa01": {
			ID: "case-0000aa01", FiledAt: now.Add(-3 * time.Hour), FiledByID: "gideon", FiledByName: "Constable Gideon Marsh",
			Parties:   []sim.CourtParty{{ActorID: "josiah", Name: "Josiah Thorne"}},
			Complaint: "the ledger was taken", Status: sim.CourtCaseStatusPending, Seeded: true,
		},
		"case-0000aa02": {
			ID: "case-0000aa02", FiledAt: now.Add(-30 * time.Hour), FiledByID: "gideon", FiledByName: "Constable Gideon Marsh",
			Parties: []sim.CourtParty{
				{ActorID: "josiah", Name: "Josiah Thorne"}, {ActorID: "lewis", Name: "Lewis Walker"},
			},
			Complaint: "the whetstone debt", Status: sim.CourtCaseStatusRuled,
			RuledAt: now.Add(-26 * time.Hour), Result: sim.CourtResultPay,
			PayerID: "josiah", PayeeID: "lewis", AmountOrdered: 30, AmountPaid: 17,
			Words: "Josiah Thorne is to hand Lewis Walker thirty coins.",
		},
		"case-0000aa03": {
			ID: "case-0000aa03", FiledAt: now.Add(-50 * time.Hour), FiledByID: "gideon", FiledByName: "Constable Gideon Marsh",
			Parties:   []sim.CourtParty{{ActorID: "moses", Name: "Moses James"}},
			Complaint: "a hex on the cows", Status: sim.CourtCaseStatusRuled,
			RuledAt: now.Add(-48 * time.Hour), Result: sim.CourtResultNoSuchCharge, Words: "This court hears no such charge.",
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("SaveWorld: %v", err)
	}
	// A second checkpoint after the pending case is ruled updates it in place.
	w.CourtCases["case-0000aa01"].Status = sim.CourtCaseStatusRuled
	w.CourtCases["case-0000aa01"].RuledAt = now
	w.CourtCases["case-0000aa01"].Result = sim.CourtResultNoCase
	w.CourtCases["case-0000aa01"].Words = "No ledger was ever kept."
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err != nil {
		t.Fatalf("second SaveWorld: %v", err)
	}

	loaded, err := LoadWorld(ctx, repo, true /*requireAllImpl*/)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if len(loaded.CourtCases) != 3 {
		t.Fatalf("loaded %d cases, want 3", len(loaded.CourtCases))
	}
	c1 := loaded.CourtCases["case-0000aa01"]
	if !c1.Seeded || c1.Status != sim.CourtCaseStatusRuled || c1.Result != sim.CourtResultNoCase || !c1.RuledAt.Equal(now) || c1.Words != "No ledger was ever kept." {
		t.Errorf("updated case = %+v", c1)
	}
	c2 := loaded.CourtCases["case-0000aa02"]
	if c2.AmountOrdered != 30 || c2.AmountPaid != 17 || c2.PayerID != "josiah" || c2.PayeeID != "lewis" ||
		len(c2.Parties) != 2 || c2.Parties[1].Name != "Lewis Walker" || !c2.FiledAt.Equal(now.Add(-30*time.Hour)) {
		t.Errorf("pay case = %+v", c2)
	}
	if c3 := loaded.CourtCases["case-0000aa03"]; c3.Result != sim.CourtResultNoSuchCharge || c3.AmountOrdered != 0 {
		t.Errorf("witchcraft case = %+v", c3)
	}
}

func TestIntegration_CourtCases_StatusCheckRefusesARuledCaseWithoutAResult(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	repo := NewRepository(f.Pool)
	w := checkpointableWorld(repo)
	w.CourtCases = map[sim.CourtCaseID]*sim.CourtCase{
		"case-0000aa09": {
			ID: "case-0000aa09", FiledAt: time.Now().UTC(), FiledByName: "x",
			Complaint: "x", Status: sim.CourtCaseStatusRuled, RuledAt: time.Now().UTC(),
		},
	}
	if err := SaveWorld(ctx, repo, w.BuildCheckpointSnapshot()); err == nil {
		t.Fatal("a ruled case with no result was saved; the table CHECK should refuse it")
	}
}

func TestLoadDealingsBetween(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const (
		josiah = "11111111-1111-1111-1111-111111111111"
		lewis  = "22222222-2222-2222-2222-222222222222"
		anne   = "33333333-3333-3333-3333-333333333333"
	)
	seedActorForLog(t, ctx, f.Pool, josiah, "Josiah Thorne")
	seedActorForLog(t, ctx, f.Pool, lewis, "Lewis Walker")
	seedActorForLog(t, ctx, f.Pool, anne, "Anne Walker")
	at := func(h int) time.Time { return loadDayStart.Add(time.Duration(h) * time.Hour) }

	// A conversation both were in: all of it, Anne's line included.
	insertAgentActionRow(t, ctx, f.Pool, josiah, "spoke", "Josiah Thorne", "hud-1", `{"text":"I owe you nothing"}`, at(9), "ok")
	insertAgentActionRow(t, ctx, f.Pool, lewis, "spoke", "Lewis Walker", "hud-1", `{"text":"You owe me a whetstone"}`, at(9), "ok")
	insertAgentActionRow(t, ctx, f.Pool, anne, "spoke", "Anne Walker", "hud-1", `{"text":"Peace, both of you"}`, at(9), "ok")
	// Josiah pays Lewis away from any shared conversation: by id, and an older row by name only.
	insertAgentActionRow(t, ctx, f.Pool, josiah, "paid", "Josiah Thorne", "", `{"recipient":"Lewis Walker","recipient_actor_id":"`+lewis+`","amount":4,"for":"settlement of the whetstone debt"}`, at(10), "ok")
	insertAgentActionRow(t, ctx, f.Pool, josiah, "paid", "Josiah Thorne", "", `{"recipient":"Lewis Walker","amount":2}`, at(11), "ok")
	// Not between them: Josiah pays Anne; Anne and Lewis talk alone; Josiah talks elsewhere.
	insertAgentActionRow(t, ctx, f.Pool, josiah, "paid", "Josiah Thorne", "", `{"recipient":"Anne Walker","recipient_actor_id":"`+anne+`","amount":9}`, at(12), "ok")
	insertAgentActionRow(t, ctx, f.Pool, lewis, "spoke", "Lewis Walker", "hud-2", `{"text":"Anne, a word"}`, at(13), "ok")
	insertAgentActionRow(t, ctx, f.Pool, josiah, "spoke", "Josiah Thorne", "hud-3", `{"text":"Good morning"}`, at(14), "ok")
	// Outside the window.
	insertAgentActionRow(t, ctx, f.Pool, josiah, "paid", "Josiah Thorne", "", `{"recipient":"Lewis Walker","recipient_actor_id":"`+lewis+`","amount":50}`, loadDayEnd.Add(time.Hour), "ok")

	repo := &ActionLogRepo{pool: f.Pool}
	got, err := repo.LoadDealingsBetween(ctx, josiah, lewis, "Josiah Thorne", "Lewis Walker", loadDayStart, loadDayEnd, 100)
	if err != nil {
		t.Fatalf("LoadDealingsBetween: %v", err)
	}
	var summary []string
	for _, e := range got {
		summary = append(summary, e.Speaker+":"+string(e.Kind))
	}
	want := []string{
		"Josiah Thorne:spoke", "Lewis Walker:spoke", "Anne Walker:spoke",
		"Josiah Thorne:paid", "Josiah Thorne:paid",
	}
	if len(summary) != len(want) {
		t.Fatalf("got %v, want %v", summary, want)
	}
	for i := range want {
		if summary[i] != want[i] {
			t.Fatalf("got %v, want %v", summary, want)
		}
	}
	if got[3].Payload["for"] != "settlement of the whetstone debt" {
		t.Fatalf("payload not decoded: %+v", got[3].Payload)
	}
	// The cap holds.
	capped, err := repo.LoadDealingsBetween(ctx, josiah, lewis, "Josiah Thorne", "Lewis Walker", loadDayStart, loadDayEnd, 2)
	if err != nil || len(capped) != 2 {
		t.Fatalf("capped = %d rows, err %v; want 2", len(capped), err)
	}
}
