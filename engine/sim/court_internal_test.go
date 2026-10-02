package sim

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// court_internal_test.go — LLM-695, the magistrates: filing (constable-only,
// party resolution, the daily limit, the operator path), which cases the sitting
// hears, and applying a ruling (validation, the pay order and its shortfall, the
// beats and the durable rows, the closed status).

func courtWorld() (*World, *recordingActionLogSink) {
	w := &World{
		Settings: WorldSettings{
			Location:         time.UTC,
			RotationTime:     "00:00",
			CourtSittingTime: "12:00",
		},
		Actors: map[ActorID]*Actor{
			"gideon": {
				ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: KindNPCStateful,
				Attributes: map[string][]byte{AttrConstable: nil},
			},
			"josiah": {ID: "josiah", DisplayName: "Josiah Thorne", Kind: KindNPCStateful, Coins: 17},
			"lewis":  {ID: "lewis", DisplayName: "Lewis Walker", Kind: KindNPCShared, Coins: 44},
			"anne":   {ID: "anne", DisplayName: "Anne Walker", Kind: KindNPCShared},
			"wendy":  {ID: "wendy", DisplayName: "Wendy", Kind: KindPC, Coins: 43},
			"cow":    {ID: "cow", DisplayName: "Cow", Kind: KindDecorative},
			"vstr-0a1b2c3d": {
				ID: "vstr-0a1b2c3d", DisplayName: "Roger Standish the messenger",
				Kind: KindNPCShared, VisitorState: &VisitorState{},
			},
		},
		CourtCases: map[CourtCaseID]*CourtCase{},
	}
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	return w, sink
}

// courtMorning is before the noon sitting on a UTC day.
var courtMorning = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

func fileCase(t *testing.T, w *World, filer ActorID, parties []string, at time.Time, operator bool) *CourtCase {
	t.Helper()
	res, err := FileCourtCase(filer, parties, "the ledger was taken", at, operator).Fn(w)
	if err != nil {
		t.Fatalf("FileCourtCase: %v", err)
	}
	return res.(CourtFileResult).Case
}

func TestFileCourtCase_ConstableFilesAndIsHeardAtNoon(t *testing.T) {
	w, sink := courtWorld()
	res, err := FileCourtCase("gideon", []string{"Josiah Thorne", "Gideon Marsh"}, "the ledger was taken", courtMorning, false).Fn(w)
	if err != nil {
		t.Fatalf("FileCourtCase: %v", err)
	}
	r := res.(CourtFileResult)
	if len(r.Case.Parties) != 2 || r.Case.Parties[1].ActorID != "gideon" {
		t.Fatalf("parties = %+v; want Josiah and the constable (resolved from \"Gideon Marsh\")", r.Case.Parties)
	}
	if want := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC); !r.HeardAt.Equal(want) {
		t.Fatalf("heard at %v, want %v", r.HeardAt, want)
	}
	if !strings.Contains(r.Message, "heard today at noon") {
		t.Fatalf("message %q should say it is heard today at noon", r.Message)
	}
	if len(sink.rows) != 1 || sink.rows[0].ActionType != ActionTypeBroughtCase || sink.rows[0].ActorID != "gideon" {
		t.Fatalf("durable rows = %+v; want one brought_case row for the constable", sink.rows)
	}
	if got := w.ActionLog[len(w.ActionLog)-1]; got.ActionType != ActionTypeBroughtCase {
		t.Fatalf("ring tail = %s, want brought_case", got.ActionType)
	}
}

func TestFileCourtCase_AfterNoonIsHeardTomorrow(t *testing.T) {
	w, _ := courtWorld()
	res, err := FileCourtCase("gideon", []string{"Josiah Thorne"}, "x", courtMorning.Add(5*time.Hour), false).Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC); !res.(CourtFileResult).HeardAt.Equal(want) {
		t.Fatalf("heard at %v, want tomorrow noon", res.(CourtFileResult).HeardAt)
	}
}

func TestFileCourtCase_Refusals(t *testing.T) {
	cases := []struct {
		name     string
		filer    ActorID
		parties  []string
		operator bool
	}{
		{"not the constable", "lewis", []string{"Josiah Thorne"}, false},
		{"unknown villager", "gideon", []string{"Martha Nobody"}, false},
		{"a traveler cannot be a party", "gideon", []string{"Roger Standish the messenger"}, false},
		{"scenery cannot be a party", "gideon", []string{"Cow"}, false},
		{"ambiguous surname", "gideon", []string{"Walker"}, false},
		{"no parties", "gideon", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, _ := courtWorld()
			_, err := FileCourtCase(c.filer, c.parties, "x", courtMorning, c.operator).Fn(w)
			var mfe ModelFacingError
			if !errors.As(err, &mfe) {
				t.Fatalf("err = %v; want a model-facing refusal", err)
			}
			if len(w.CourtCases) != 0 {
				t.Fatalf("a refused filing left %d case(s) on the docket", len(w.CourtCases))
			}
		})
	}
}

func TestFileCourtCase_DailyLimit(t *testing.T) {
	w, _ := courtWorld()
	fileCase(t, w, "gideon", []string{"Josiah Thorne"}, courtMorning, false)
	fileCase(t, w, "gideon", []string{"Lewis Walker"}, courtMorning.Add(time.Hour), false)
	_, err := FileCourtCase("gideon", []string{"Anne Walker"}, "x", courtMorning.Add(2*time.Hour), false).Fn(w)
	if err == nil || !strings.Contains(err.Error(), "docket for this sitting is full") {
		t.Fatalf("third filing err = %v; want the docket-full refusal", err)
	}
	// The operator seeding path is not held to the limit.
	fileCase(t, w, "gideon", []string{"Anne Walker"}, courtMorning.Add(2*time.Hour), true)
	// A new game-day resets it.
	fileCase(t, w, "gideon", []string{"Anne Walker"}, courtMorning.Add(24*time.Hour), false)
}

func TestCourtCasesToHear_OnlyCasesFiledBeforeTheSitting(t *testing.T) {
	w, _ := courtWorld()
	early := fileCase(t, w, "gideon", []string{"Josiah Thorne"}, courtMorning, false)
	late := fileCase(t, w, "gideon", []string{"Lewis Walker"}, courtMorning.Add(4*time.Hour), false) // 13:00

	hear := func(now time.Time, force bool) []CourtCaseID {
		res, err := CourtCasesToHear(now, force).Fn(w)
		if err != nil {
			t.Fatal(err)
		}
		var ids []CourtCaseID
		for _, c := range res.([]*CourtCase) {
			ids = append(ids, c.ID)
		}
		return ids
	}
	if got := hear(courtMorning.Add(time.Hour), false); len(got) != 0 {
		t.Fatalf("before noon: hearing %v, want none", got)
	}
	if got := hear(courtMorning.Add(5*time.Hour), false); len(got) != 1 || got[0] != early.ID {
		t.Fatalf("after noon: hearing %v, want only the morning case %s", got, early.ID)
	}
	if got := hear(courtMorning.Add(5*time.Hour), true); len(got) != 2 {
		t.Fatalf("forced: hearing %v, want both", got)
	}
	if got := hear(courtMorning.Add(28*time.Hour), false); len(got) != 2 || got[1] != late.ID {
		t.Fatalf("next day after noon: hearing %v, want both, oldest first", got)
	}
}

func TestApplyCourtRuling_NoCaseClosesAndReachesEveryone(t *testing.T) {
	w, sink := courtWorld()
	c := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
	sink.rows = nil
	before := totalCoins(w)
	if _, err := ApplyCourtRuling(c.ID, CourtRuling{Result: CourtResultNoCase, Words: "No ledger was ever kept."}, courtMorning.Add(4*time.Hour)).Fn(w); err != nil {
		t.Fatal(err)
	}
	got := w.CourtCases[c.ID]
	if got.Status != CourtCaseStatusRuled || got.Result != CourtResultNoCase || got.RuledAt.IsZero() {
		t.Fatalf("case after ruling = %+v", got)
	}
	if totalCoins(w) != before {
		t.Fatal("no_case moved coin")
	}
	// Two parties plus the filer, who is not a party: three ruled rows.
	recipients := map[ActorID]bool{}
	for _, row := range sink.rows {
		if row.ActionType != ActionTypeRuled {
			t.Fatalf("unexpected durable row %s", row.ActionType)
		}
		recipients[row.ActorID] = true
		text, _ := row.Payload["text"].(string)
		if !strings.HasSuffix(text, "The matter is closed.") {
			t.Fatalf("ruled row text %q must end with the matter closed", text)
		}
	}
	if len(recipients) != 3 || !recipients["gideon"] || !recipients["josiah"] || !recipients["lewis"] {
		t.Fatalf("ruled rows reached %v; want gideon, josiah, lewis", recipients)
	}
	if n := len(w.Actors["josiah"].Warrants); n != 1 {
		t.Fatalf("josiah has %d warrant(s); want the ruling beat", n)
	}
	if _, ok := w.Actors["josiah"].Warrants[0].Reason.(CourtRuledWarrantReason); !ok {
		t.Fatalf("josiah's warrant is %T, want CourtRuledWarrantReason", w.Actors["josiah"].Warrants[0].Reason)
	}
	// A ruled case is never ruled again.
	if _, err := ApplyCourtRuling(c.ID, CourtRuling{Result: CourtResultNoCase, Words: "again"}, courtMorning.Add(5*time.Hour)).Fn(w); err == nil {
		t.Fatal("a second ruling on a ruled case was accepted")
	}
}

func TestApplyCourtRuling_PayOrderAndShortfall(t *testing.T) {
	w, sink := courtWorld()
	c := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
	sink.rows = nil
	// Josiah holds 17; ordered 30 → 17 move, the rest forgiven.
	res, err := ApplyCourtRuling(c.ID, CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Lewis Walker", Amount: 30, Words: "Josiah Thorne owes Lewis Walker for the whetstone."}, courtMorning.Add(4*time.Hour)).Fn(w)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(CourtRulingApplied).Case
	if got.AmountOrdered != 30 || got.AmountPaid != 17 {
		t.Fatalf("ordered/paid = %d/%d, want 30/17", got.AmountOrdered, got.AmountPaid)
	}
	if w.Actors["josiah"].Coins != 0 || w.Actors["lewis"].Coins != 61 {
		t.Fatalf("purses josiah=%d lewis=%d, want 0 and 61", w.Actors["josiah"].Coins, w.Actors["lewis"].Coins)
	}
	paid := 0
	for _, row := range sink.rows {
		if row.ActionType == ActionTypePaid {
			paid++
			if row.ActorID != "josiah" || row.Payload["recipient_actor_id"] != "lewis" || row.Payload["court_case_id"] != string(c.ID) {
				t.Fatalf("paid row = %+v", row)
			}
		}
	}
	if paid != 1 {
		t.Fatalf("%d paid rows, want 1", paid)
	}
	if d := w.CoinDealingsFor("lewis", "josiah", courtMorning.Add(4*time.Hour)); d.ReceivedTotal != 17 {
		t.Fatalf("coin record: lewis received %d from josiah, want 17", d.ReceivedTotal)
	}
	payer := CourtRulingNarration(got, "josiah")
	if !strings.Contains(payer, "you had only 17 coins") || !strings.Contains(payer, "The court asks no more.") {
		t.Fatalf("payer narration %q should name the shortfall in the second person", payer)
	}
}

func TestApplyCourtRuling_RefusalsLeaveTheCasePending(t *testing.T) {
	cases := []struct {
		name string
		r    CourtRuling
	}{
		{"unknown result", CourtRuling{Result: "acquit", Words: "x"}},
		{"no words", CourtRuling{Result: CourtResultNoCase}},
		{"found for a stranger", CourtRuling{Result: CourtResultFoundFor, FoundFor: "Anne Walker", Words: "x"}},
		{"pay to oneself", CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Josiah Thorne", Amount: 3, Words: "x"}},
		{"pay nothing", CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Lewis Walker", Words: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, _ := courtWorld()
			cs := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
			_, err := ApplyCourtRuling(cs.ID, c.r, courtMorning.Add(4*time.Hour)).Fn(w)
			var mfe ModelFacingError
			if !errors.As(err, &mfe) {
				t.Fatalf("err = %v; want a model-facing refusal the magistrate can correct", err)
			}
			if w.CourtCases[cs.ID].Status != CourtCaseStatusPending {
				t.Fatal("a refused ruling changed the case")
			}
		})
	}
}

func TestCourtSittingPhrase(t *testing.T) {
	for spec, want := range map[string]string{
		"12:00": "noon", "": "noon", "nonsense": "noon",
		"09:00": "9 o'clock in the morning", "15:30": "3:30 in the afternoon", "19:00": "7 o'clock in the evening",
	} {
		if got := CourtSittingPhrase(spec); got != want {
			t.Errorf("CourtSittingPhrase(%q) = %q, want %q", spec, got, want)
		}
	}
}

// A case the operator seeds in the constable's name does not use up his day:
// he can still bring two of his own.
func TestFileCourtCase_SeededCaseDoesNotCountTowardTheLimit(t *testing.T) {
	w, _ := courtWorld()
	seeded := fileCase(t, w, "gideon", []string{"Josiah Thorne"}, courtMorning, true)
	if !seeded.Seeded {
		t.Fatal("an operator filing is not marked seeded")
	}
	fileCase(t, w, "gideon", []string{"Lewis Walker"}, courtMorning.Add(time.Hour), false)
	fileCase(t, w, "gideon", []string{"Anne Walker"}, courtMorning.Add(2*time.Hour), false)
	if _, today := courtDocketForSnapshot(w, courtMorning.Add(3*time.Hour)); today["gideon"] != 2 {
		t.Fatalf("snapshot counts %d filings for gideon, want 2 (the seeded one excluded)", today["gideon"])
	}
}

// Every `ruled` row is written before the payment: the record can then never
// hold a payment without the ruling behind it.
func TestApplyCourtRuling_RulingRowsPrecedeThePayment(t *testing.T) {
	w, sink := courtWorld()
	c := fileCase(t, w, "gideon", []string{"Josiah Thorne", "Lewis Walker"}, courtMorning, false)
	sink.rows = nil
	if _, err := ApplyCourtRuling(c.ID, CourtRuling{Result: CourtResultPay, Payer: "Josiah Thorne", Payee: "Lewis Walker", Amount: 5, Words: "x"}, courtMorning.Add(4*time.Hour)).Fn(w); err != nil {
		t.Fatal(err)
	}
	var kinds []ActionType
	for _, r := range sink.rows {
		kinds = append(kinds, r.ActionType)
	}
	want := []ActionType{ActionTypeRuled, ActionTypeRuled, ActionTypeRuled, ActionTypePaid}
	if len(kinds) != len(want) {
		t.Fatalf("rows %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("rows %v, want %v", kinds, want)
		}
	}
}

func TestCourtRulingFromRecord_Validates(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{"result": "pay", "words": "w", "payer": "A", "payee": "B",
			"amount_ordered": float64(10), "amount_paid": float64(4)}
	}
	if r, err := CourtRulingFromRecord(CourtRecord{Ruling: good()}); err != nil || r.Amount != 10 || r.RecoveredPaid != 4 || !r.Recovered {
		t.Fatalf("valid record: %+v %v", r, err)
	}
	bad := map[string]func(map[string]any){
		"unknown result":     func(p map[string]any) { p["result"] = "acquit" },
		"no words":           func(p map[string]any) { delete(p, "words") },
		"no payer":           func(p map[string]any) { delete(p, "payer") },
		"negative paid":      func(p map[string]any) { p["amount_paid"] = float64(-3) },
		"fractional paid":    func(p map[string]any) { p["amount_paid"] = 2.5 },
		"paid over ordered":  func(p map[string]any) { p["amount_paid"] = float64(11) },
		"ordered zero":       func(p map[string]any) { p["amount_ordered"] = float64(0); p["amount_paid"] = float64(0) },
		"ordered as string":  func(p map[string]any) { p["amount_ordered"] = "10" },
		"over the order cap": func(p map[string]any) { p["amount_ordered"] = float64(MaxCourtPayOrder + 1) },
	}
	for name, mutate := range bad {
		p := good()
		mutate(p)
		if _, err := CourtRulingFromRecord(CourtRecord{Ruling: p}); err == nil {
			t.Errorf("%s: accepted %v", name, p)
		}
	}
	if _, err := CourtRulingFromRecord(CourtRecord{Ruling: map[string]any{"result": "found_for", "words": "w"}}); err == nil {
		t.Error("found_for with no party accepted")
	}
}

func TestCourtRulingFromRecord_RefusesARecordThatDisagreesWithItself(t *testing.T) {
	ruling := func() map[string]any {
		return map[string]any{"result": "pay", "words": "w", "payer": "A", "payee": "B",
			"amount_ordered": float64(10), "amount_paid": float64(4)}
	}
	pay := CourtPaymentRow{PayerID: "a", PayeeID: "b", Amount: 4}
	if _, err := CourtRulingFromRecord(CourtRecord{Ruling: ruling(), Rulings: []map[string]any{ruling(), ruling()}, Payments: []CourtPaymentRow{pay}}); err != nil {
		t.Fatalf("a consistent record was refused: %v", err)
	}
	other := ruling()
	other["words"] = "something else"
	cases := map[string]CourtRecord{
		"rulings disagree":            {Ruling: ruling(), Rulings: []map[string]any{ruling(), other}},
		"two payments":                {Ruling: ruling(), Payments: []CourtPaymentRow{pay, pay}},
		"payment amount differs":      {Ruling: ruling(), Payments: []CourtPaymentRow{{PayerID: "a", PayeeID: "b", Amount: 9}}},
		"payment for a no-pay ruling": {Ruling: map[string]any{"result": "no_case", "words": "w"}, Payments: []CourtPaymentRow{pay}},
	}
	for name, rec := range cases {
		if _, err := CourtRulingFromRecord(rec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Recorded amounts are parsed strictly, and every ruling row is validated and
// compared as typed values: a string "10" is not the number 10.
func TestCourtRecordedAmounts_AreStrict(t *testing.T) {
	for _, bad := range []any{"10", 10.5, -1.0, float64(MaxCourtPayOrder + 1), nil, true} {
		if _, err := CourtRecordedAmount(bad); err == nil {
			t.Errorf("CourtRecordedAmount(%#v) accepted", bad)
		}
	}
	if n, err := CourtRecordedAmount(float64(10)); err != nil || n != 10 {
		t.Fatalf("10 = %d, %v", n, err)
	}
	first := map[string]any{"result": "pay", "words": "w", "payer": "A", "payee": "B",
		"amount_ordered": float64(10), "amount_paid": float64(10)}
	stringy := map[string]any{"result": "pay", "words": "w", "payer": "A", "payee": "B",
		"amount_ordered": "10", "amount_paid": float64(10)}
	if _, err := CourtRulingFromRecord(CourtRecord{Ruling: first, Rulings: []map[string]any{first, stringy}}); err == nil {
		t.Fatal("a second ruling row with a string amount was accepted")
	}
}
