package court

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// runner_test.go — a whole session against a running world and a scripted
// magistrate: he reads, the read tools answer from the world and the record, a
// ruling he gives wrongly is refused and he corrects it, and the corrected
// ruling closes the case. Plus the date window.

type fakeRecords struct {
	mu       sync.Mutex
	dealings int
	days     int
	// record, when set, is what the durable record already holds for the case.
	record  *sim.CourtRecord
	lookups int
}

func (f *fakeRecords) LoadCourtRecord(_ context.Context, _ sim.CourtCaseID) (sim.CourtRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.record == nil {
		return sim.CourtRecord{}, false, nil
	}
	return *f.record, true, nil
}

func (f *fakeRecords) LoadDayEvents(_ context.Context, _ sim.ActorID, start, _ time.Time) ([]sim.SimDayEvent, error) {
	f.mu.Lock()
	f.days++
	f.mu.Unlock()
	return []sim.SimDayEvent{{At: start.Add(time.Hour), Kind: sim.ActionTypeSpoke, Speaker: "Josiah Thorne", Payload: map[string]any{"text": "My ledger is gone!"}}}, nil
}

func (f *fakeRecords) LoadDealingsBetween(_ context.Context, _, _ sim.ActorID, _, _ string, start, _ time.Time, _ int) ([]sim.SimDayEvent, error) {
	f.mu.Lock()
	f.dealings++
	f.mu.Unlock()
	return []sim.SimDayEvent{
		{At: start.Add(2 * time.Hour), Kind: sim.ActionTypePaid, Speaker: "Josiah Thorne",
			Payload: map[string]any{"recipient": "Lewis Walker", "amount": float64(4), "for": "settlement of the whetstone debt"}},
	}, nil
}

type fakeNotes struct {
	mu    sync.Mutex
	notes map[string]string
}

func (f *fakeNotes) ReadNote(_ context.Context, ns, slug string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.notes[ns+"/"+slug]
	return v, ok, nil
}

func (f *fakeNotes) SaveNote(_ context.Context, ns, slug, _, content, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes[ns+"/"+slug] = content
	return nil
}

func call(id, name string, args any) llm.RawToolCall {
	raw, _ := json.Marshal(args)
	return llm.RawToolCall{ID: id, Name: name, Arguments: raw}
}

func courtTestWorld(t *testing.T) (*sim.World, context.Context, sim.CourtCaseID) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.Location = time.UTC
		world.Actors["gideon"] = &sim.Actor{ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: sim.KindNPCStateful,
			Role: "constable", Attributes: map[string][]byte{sim.AttrConstable: nil}}
		world.Actors["lewis"] = &sim.Actor{ID: "lewis", DisplayName: "Lewis Walker", Kind: sim.KindNPCShared, Coins: 44}
		world.Actors["josiah"] = &sim.Actor{ID: "josiah", DisplayName: "Josiah Thorne", Kind: sim.KindNPCStateful, Coins: 17,
			Inventory: map[sim.ItemKind]int{}}
		return nil, nil
	}})
	_ = res
	if err != nil {
		t.Fatal(err)
	}
	filed, err := w.Send(sim.FileCourtCase("gideon", []string{"Josiah Thorne"}, "Josiah Thorne's ledger was stolen from the Store.", time.Now().UTC().Add(-time.Hour), true))
	if err != nil {
		t.Fatalf("FileCourtCase: %v", err)
	}
	return w, ctx, filed.(sim.CourtFileResult).Case.ID
}

func TestHear_ReadsCorrectsAndRules(t *testing.T) {
	w, ctx, caseID := courtTestWorld(t)
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
			call("c1", "ask_about_goods", map[string]string{"name": "ledger"}),
			call("c2", "read_record", map[string]string{"person": "Josiah Thorne", "with": "Constable Gideon Marsh"}),
			call("c3", "look_in_purse", map[string]string{"person": "Josiah Thorne"}),
		}}},
		// A ruling for someone who is not a party is refused; he corrects it.
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
			call("c4", "rule", map[string]string{"result": "found_for", "found_for": "Lewis Walker", "words": "x"}),
		}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
			call("c5", "amend_bench_book", map[string]string{"text": "The village keeps no account books."}),
			call("c6", "rule", map[string]string{"result": "no_case", "words": "No ledger was ever kept in this village, so none could be taken. There is no case."}),
			call("c7", "read_record", map[string]string{"person": "Josiah Thorne"}),
		}}},
	)
	records := &fakeRecords{}
	notes := &fakeNotes{notes: map[string]string{Namespace + "/" + BenchBookSlug: "Begin with the goods."}}
	r := &Runner{ctx: ctx, w: w, client: client, records: records, notes: notes, lastAttempt: map[sim.CourtCaseID]time.Time{}}

	res, err := w.Send(sim.CourtCaseList())
	if err != nil {
		t.Fatal(err)
	}
	r.hear(res.([]*sim.CourtCase)[0])

	res, _ = w.Send(sim.CourtCaseList())
	c := res.([]*sim.CourtCase)[0]
	if c.ID != caseID || c.Status != sim.CourtCaseStatusRuled || c.Result != sim.CourtResultNoCase {
		t.Fatalf("case after the session = %+v; want ruled no_case", c)
	}
	reqs := client.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	if reqs[0].Model != sim.CourtMagistrateModel || reqs[0].SceneID == "" || reqs[0].SceneID != reqs[2].SceneID {
		t.Fatalf("model/scene = %q/%q..%q; want the magistrate on one scene", reqs[0].Model, reqs[0].SceneID, reqs[2].SceneID)
	}
	opening := reqs[0].StableContext
	for _, want := range []string{"Josiah Thorne's ledger was stolen", "Begin with the goods.", "Constable Gideon Marsh — constable"} {
		if !strings.Contains(opening, want) {
			t.Fatalf("opening lacks %q:\n%s", want, opening)
		}
	}
	second := reqs[1].Messages
	results := map[string]string{}
	for _, m := range second {
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = m.Content
		}
	}
	if !strings.Contains(results["c1"], `no such thing as "ledger"`) {
		t.Fatalf("ask_about_goods answered %q", results["c1"])
	}
	if !strings.Contains(results["c2"], `Josiah Thorne paid Lewis Walker 4 coins — "settlement of the whetstone debt"`) {
		t.Fatalf("read_record answered %q", results["c2"])
	}
	if !strings.Contains(results["c3"], "holds 17 coins") {
		t.Fatalf("look_in_purse answered %q", results["c3"])
	}
	third := reqs[2].Messages
	if last := third[len(third)-1]; !strings.HasPrefix(last.Content, "[refused]") {
		t.Fatalf("the wrong ruling answered %q; want a refusal", last.Content)
	}
	if got := notes.notes[Namespace+"/"+BenchBookSlug]; got != "The village keeps no account books." {
		t.Fatalf("bench book = %q", got)
	}
	// The trailing results (bench book, ruling, the skipped read after it) are
	// persisted so the scene never ends on an answered-less tool call.
	persisted := client.PersistRequests()
	if len(persisted) != 1 || len(persisted[0].Results) != 3 {
		t.Fatalf("persisted %+v; want one batch of 3", persisted)
	}
	if !strings.HasPrefix(persisted[0].Results[2].Content, "[skipped]") {
		t.Fatalf("a call after the ruling answered %q; want skipped", persisted[0].Results[2].Content)
	}
	if records.days != 0 {
		t.Fatal("a read_record after the ruling ran")
	}
}

func TestHear_ProseOnlyGivesUpAndLeavesThePendingCase(t *testing.T) {
	w, ctx, _ := courtTestWorld(t)
	var turns []llm.ScriptedTurn
	for i := 0; i <= maxNudges; i++ {
		turns = append(turns, llm.ScriptedTurn{Response: llm.Response{Content: "I must think on it."}})
	}
	client := llm.NewFakeClient(turns...)
	r := &Runner{ctx: ctx, w: w, client: client, records: &fakeRecords{}, notes: &fakeNotes{notes: map[string]string{}}, lastAttempt: map[sim.CourtCaseID]time.Time{}}
	res, _ := w.Send(sim.CourtCaseList())
	r.hear(res.([]*sim.CourtCase)[0])
	res, _ = w.Send(sim.CourtCaseList())
	if c := res.([]*sim.CourtCase)[0]; c.Status != sim.CourtCaseStatusPending {
		t.Fatalf("status = %s; a session with no ruling must leave the case pending", c.Status)
	}
	if n := client.CallCount(); n != maxNudges+1 {
		t.Fatalf("%d calls, want %d", n, maxNudges+1)
	}
}

// TestRecordWindow_AcrossClockChanges — seven village days are allowed and
// eight refused on both sides of a daylight-saving change, in the village's own
// zone.
func TestRecordWindow_AcrossClockChanges(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tz data: %v", err)
	}
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, loc)
	for _, c := range []struct{ name, from, to string }{
		{"spring forward", "2026-03-05", "2026-03-11"},
		{"fall back", "2026-10-29", "2026-11-04"},
	} {
		t.Run(c.name, func(t *testing.T) {
			start, end, err := recordWindow(c.from, c.to, now, loc)
			if err != nil {
				t.Fatalf("seven days refused: %v", err)
			}
			if got := start.AddDate(0, 0, 7); !got.Equal(end) {
				t.Fatalf("window %v..%v is not seven village days", start, end)
			}
			last, _ := time.ParseInLocation("2006-01-02", c.to, loc)
			if _, _, err := recordWindow(c.from, last.AddDate(0, 0, 1).Format("2006-01-02"), now, loc); err == nil {
				t.Fatal("eight days accepted")
			}
		})
	}
}

func TestRecordWindow(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name, from, to string
		start, end     time.Time
		bad            bool
	}{
		{name: "default is today and the two days before", start: day(1), end: day(4)},
		{name: "from only runs three days", from: "2026-09-20", start: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), end: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{name: "from only is cut at today", from: "2026-10-02", start: day(2), end: day(4)},
		{name: "explicit span", from: "2026-09-26", to: "2026-10-02", start: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), end: day(3)},
		{name: "eight days is too long", from: "2026-09-25", to: "2026-10-02", bad: true},
		{name: "backwards", from: "2026-10-02", to: "2026-10-01", bad: true},
		{name: "not a date", from: "last week", bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, err := recordWindow(c.from, c.to, now, time.UTC)
			if c.bad {
				if err == nil {
					t.Fatalf("want an error, got %v..%v", start, end)
				}
				return
			}
			if err != nil || !start.Equal(c.start) || !end.Equal(c.end) {
				t.Fatalf("got %v..%v err=%v; want %v..%v", start, end, err, c.start, c.end)
			}
		})
	}
}

type recordingSink struct {
	mu   sync.Mutex
	rows []sim.DurableActionLogRow
}

func (s *recordingSink) Append(_ context.Context, row sim.DurableActionLogRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

// recoveryWorld is courtTestWorld with Lewis added as a party and a recording
// sink installed, for the crash-recovery tests.
func recoveryWorld(t *testing.T) (*sim.World, context.Context, sim.CourtCaseID, *recordingSink) {
	t.Helper()
	w, ctx, caseID := courtTestWorld(t)
	sink := &recordingSink{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.SetActionLogSink(sink)
		c := world.CourtCases[caseID]
		c.Parties = append(c.Parties, sim.CourtParty{ActorID: "lewis", Name: "Lewis Walker"})
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return w, ctx, caseID, sink
}

const recordedWords = "Josiah Thorne is to hand Lewis Walker ten coins."

func recordedPayRuling(caseID sim.CourtCaseID) map[string]any {
	return map[string]any{
		"case_id": string(caseID), "result": "pay", "words": recordedWords,
		"payer": "Josiah Thorne", "payee": "Lewis Walker", "amount_ordered": float64(10), "amount_paid": float64(10),
	}
}

// hearOnce runs one hearing with a magistrate scripted to rule DIFFERENTLY from
// the record, and returns how often he was asked.
func hearOnce(t *testing.T, w *sim.World, ctx context.Context, records *fakeRecords) int {
	t.Helper()
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		call("x1", "rule", map[string]string{"result": "no_case", "words": "There is no case."}),
	}}})
	r := &Runner{ctx: ctx, w: w, client: client, records: records, notes: &fakeNotes{notes: map[string]string{}}, lastAttempt: map[sim.CourtCaseID]time.Time{}}
	res, _ := w.Send(sim.CourtCaseList())
	r.hear(res.([]*sim.CourtCase)[0])
	return client.CallCount()
}

func caseAndPurses(t *testing.T, w *sim.World) (*sim.CourtCase, [2]int) {
	t.Helper()
	res, _ := w.Send(sim.CourtCaseList())
	purses, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return [2]int{world.Actors["josiah"].Coins, world.Actors["lewis"].Coins}, nil
	}})
	return res.([]*sim.CourtCase)[0], purses.([2]int)
}

func sinkKinds(s *recordingSink) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, r := range s.rows {
		out[string(r.ActionType)+":"+string(r.ActorID)]++
	}
	return out
}

// TestHear_RecoversARulingGivenBeforeACrash — the record holds the whole ruling
// (every `ruled` row and the payment); the engine died before the checkpoint.
// The court finishes THAT ruling: the magistrate, scripted to rule differently,
// is never asked; the case takes the recorded result, words and time; the pay
// order moves the recorded sum again; and nothing new is written.
func TestHear_RecoversARulingGivenBeforeACrash(t *testing.T) {
	w, ctx, caseID, sink := recoveryWorld(t)
	ruledAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	records := &fakeRecords{record: &sim.CourtRecord{
		Ruling: recordedPayRuling(caseID), RuledAt: ruledAt,
		RuledFor: map[sim.ActorID]bool{"josiah": true, "lewis": true, "gideon": true},
		Payments: []sim.CourtPaymentRow{{PayerID: "josiah", PayeeID: "lewis", Amount: 10}},
	}}
	if n := hearOnce(t, w, ctx, records); n != 0 {
		t.Fatalf("the magistrate was asked %d time(s); a recorded ruling must be finished, not re-heard", n)
	}
	c, purses := caseAndPurses(t, w)
	if c.Status != sim.CourtCaseStatusRuled || c.Result != sim.CourtResultPay || c.AmountPaid != 10 ||
		c.Words != recordedWords || !c.RuledAt.Equal(ruledAt) {
		t.Fatalf("recovered case = %+v", c)
	}
	if purses != [2]int{7, 54} {
		t.Fatalf("purses josiah/lewis = %v, want 7/54", purses)
	}
	if got := sinkKinds(sink); len(got) != 0 {
		t.Fatalf("recovery wrote %v; the record already holds everything", got)
	}
}

// Crash after the first `ruled` row, before the other recipients' rows and the
// payment: recovery finishes the recorded ruling and writes exactly the rows
// that are missing — the other two `ruled` rows and the `paid` row.
func TestHear_RecoveryWritesTheRowsTheCrashLost(t *testing.T) {
	w, ctx, caseID, sink := recoveryWorld(t)
	records := &fakeRecords{record: &sim.CourtRecord{
		Ruling: recordedPayRuling(caseID), RuledAt: time.Now().UTC().Add(-time.Minute),
		RuledFor: map[sim.ActorID]bool{"josiah": true},
	}}
	if n := hearOnce(t, w, ctx, records); n != 0 {
		t.Fatalf("the magistrate was asked %d time(s)", n)
	}
	c, purses := caseAndPurses(t, w)
	if c.Status != sim.CourtCaseStatusRuled || c.Words != recordedWords || purses != [2]int{7, 54} {
		t.Fatalf("case %+v purses %v", c, purses)
	}
	want := map[string]int{"ruled:lewis": 1, "ruled:gideon": 1, "paid:josiah": 1}
	got := sinkKinds(sink)
	if len(got) != len(want) {
		t.Fatalf("recovery wrote %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("recovery wrote %v, want %v", got, want)
		}
	}
	// The payment was not on record, so the coin record is credited now.
	d, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.CoinDealingsFor("lewis", "josiah", time.Now().UTC()), nil
	}})
	if d.(sim.CoinDealings).ReceivedTotal != 10 {
		t.Fatalf("coin record shows %d received, want 10", d.(sim.CoinDealings).ReceivedTotal)
	}
}

// Every `ruled` row durable but the payment row lost (crash, or the payment
// append failing): only the `paid` row is written.
func TestHear_RecoveryWritesALostPayment(t *testing.T) {
	w, ctx, caseID, sink := recoveryWorld(t)
	records := &fakeRecords{record: &sim.CourtRecord{
		Ruling: recordedPayRuling(caseID), RuledAt: time.Now().UTC().Add(-time.Minute),
		RuledFor: map[sim.ActorID]bool{"josiah": true, "lewis": true, "gideon": true},
	}}
	hearOnce(t, w, ctx, records)
	if got := sinkKinds(sink); len(got) != 1 || got["paid:josiah"] != 1 {
		t.Fatalf("recovery wrote %v, want only the paid row", got)
	}
}

// A payment on record with no ruling behind it (only possible if the writer
// dropped rows) is never heard again: the case stays pending for the operator.
func TestHear_APaymentWithNoRulingIsLeftForTheOperator(t *testing.T) {
	w, ctx, _, sink := recoveryWorld(t)
	records := &fakeRecords{record: &sim.CourtRecord{Payments: []sim.CourtPaymentRow{{PayerID: "josiah", PayeeID: "lewis", Amount: 10}}, RuledFor: map[sim.ActorID]bool{}}}
	if n := hearOnce(t, w, ctx, records); n != 0 {
		t.Fatalf("the magistrate was asked %d time(s)", n)
	}
	c, purses := caseAndPurses(t, w)
	if c.Status != sim.CourtCaseStatusPending || purses != [2]int{17, 44} || len(sinkKinds(sink)) != 0 {
		t.Fatalf("case %s purses %v rows %v; want untouched", c.Status, purses, sinkKinds(sink))
	}
}

// A recorded payment larger than the restored purse is not quietly reduced:
// recovery refuses, and nothing changes.
func TestHear_RecoveryRefusesWhenThePayerCannotCoverTheRecordedSum(t *testing.T) {
	w, ctx, caseID, sink := recoveryWorld(t)
	ruling := recordedPayRuling(caseID)
	ruling["amount_ordered"], ruling["amount_paid"] = float64(30), float64(30) // Josiah holds 17
	records := &fakeRecords{record: &sim.CourtRecord{Ruling: ruling, RuledAt: time.Now().UTC(), RuledFor: map[sim.ActorID]bool{"josiah": true}}}
	hearOnce(t, w, ctx, records)
	c, purses := caseAndPurses(t, w)
	if c.Status != sim.CourtCaseStatusPending || purses != [2]int{17, 44} || len(sinkKinds(sink)) != 0 {
		t.Fatalf("case %s purses %v rows %v; want untouched", c.Status, purses, sinkKinds(sink))
	}
}

// A payment on record between other villagers than the ruling names is not
// finished: the case waits for the operator.
func TestHear_RecoveryRefusesAPaymentThatDoesNotMatchTheRuling(t *testing.T) {
	w, ctx, caseID, sink := recoveryWorld(t)
	records := &fakeRecords{record: &sim.CourtRecord{
		Ruling: recordedPayRuling(caseID), RuledAt: time.Now().UTC(),
		RuledFor: map[sim.ActorID]bool{"josiah": true, "lewis": true, "gideon": true},
		Payments: []sim.CourtPaymentRow{{PayerID: "lewis", PayeeID: "josiah", Amount: 10}},
	}}
	hearOnce(t, w, ctx, records)
	c, purses := caseAndPurses(t, w)
	if c.Status != sim.CourtCaseStatusPending || purses != [2]int{17, 44} || len(sinkKinds(sink)) != 0 {
		t.Fatalf("case %s purses %v rows %v; want untouched", c.Status, purses, sinkKinds(sink))
	}
}
