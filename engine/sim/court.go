package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// court.go — the magistrates (LLM-695).
//
// The engine does not parse conversation, so a claim made in talk is never
// checked against the record and an invented dispute has no ending: the
// constable pursued the "theft" of a ledger that never existed as an item for
// eleven days, and the dream pipeline wrote it into identity every night. The
// magistrates close that gap and nothing else. The constable brings a matter
// before them (bring_before_magistrates); the court sits once a day at
// CourtSittingTime, off the map in Salem Town; an Opus magistrate reads the
// engine's records with read-only tools (engine/sim/court) and rules. What he
// SAYS is free text, delivered as written. What HAPPENS is a closed set the
// engine applies here: no case, found for a party, a party pays another, or the
// court hears no such charge (witchcraft).
//
// The docket and the rulings are durable (court_case, checkpointed like
// recurring_visitor): Salem deploys several times a day, and a case filed in the
// morning must still be heard at noon. A case is heard once it was filed before
// the most recent sitting and is still pending, so a restart after noon hears it
// and a ruled case is never heard twice — no session stamp is needed.

// CourtCaseID is a court_case row id, case-<8hex>.
type CourtCaseID string

// Case status. A pending case waits for the next sitting; a ruled case is the
// court's record and is never reopened.
const (
	CourtCaseStatusPending = "pending"
	CourtCaseStatusRuled   = "ruled"
)

// CourtResult is the closed set of outcomes the engine applies.
type CourtResult string

const (
	// CourtResultNoCase — no case for want of proof. Also the answer to a matter
	// brought again after the court has ruled on it.
	CourtResultNoCase CourtResult = "no_case"
	// CourtResultFoundFor — the court finds for one party; nothing moves.
	CourtResultFoundFor CourtResult = "found_for"
	// CourtResultPay — one party is ordered to hand another a sum of coin.
	CourtResultPay CourtResult = "pay"
	// CourtResultNoSuchCharge — the court hears no charge of witchcraft.
	CourtResultNoSuchCharge CourtResult = "no_such_charge"
)

// Valid reports whether r is one of the four results.
func (r CourtResult) Valid() bool {
	switch r {
	case CourtResultNoCase, CourtResultFoundFor, CourtResultPay, CourtResultNoSuchCharge:
		return true
	}
	return false
}

const (
	// DefaultCourtSittingTime is when the court sits each day, HH:MM in the
	// world's timezone. Noon, so a ruling reaches the parties while they are
	// awake and the night's dream run records it the same day.
	DefaultCourtSittingTime = "12:00"
	// DefaultCourtDailyCaseLimit is how many new matters one constable may bring
	// in a game-day. Each case costs one Opus session.
	DefaultCourtDailyCaseLimit = 2

	// CourtMagistrateModel is the memory-api virtual agent the court session
	// runs on (claude-opus-5-5, scene-scoped history, court framing).
	CourtMagistrateModel = "salem-magistrate"

	// MaxCourtParties bounds the villagers one case can name.
	MaxCourtParties = 4
	// MaxCourtComplaintRunes bounds the complaint as brought.
	MaxCourtComplaintRunes = 600
	// MaxCourtWordsRunes bounds the magistrate's words in a ruling.
	MaxCourtWordsRunes = 1500
	// MaxCourtPayOrder bounds a single pay order.
	MaxCourtPayOrder = 1000
)

// CourtParty is one villager named in a case. The name is kept as it was at
// filing so the record reads right after a rename.
type CourtParty struct {
	ActorID ActorID
	Name    string
}

// CourtCase is one matter on the docket, and after the sitting the court's
// record of it.
type CourtCase struct {
	ID          CourtCaseID
	FiledAt     time.Time
	FiledByID   ActorID
	FiledByName string
	Parties     []CourtParty
	Complaint   string
	Status      string
	// Seeded marks a case the operator filed (umbilical /court/file). It does
	// not count toward the filer's daily limit.
	Seeded bool

	// The ruling. Zero until Status is ruled.
	RuledAt       time.Time
	Result        CourtResult
	FoundForID    ActorID
	PayerID       ActorID
	PayeeID       ActorID
	AmountOrdered int
	AmountPaid    int
	Words         string
}

// Clone deep-copies the case (the Parties slice is the only reference field).
func (c *CourtCase) Clone() *CourtCase {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Parties = append([]CourtParty(nil), c.Parties...)
	return &cp
}

// Involves reports whether the actor is a party to the case or brought it.
func (c *CourtCase) Involves(id ActorID) bool {
	if c == nil || id == "" {
		return false
	}
	if c.FiledByID == id {
		return true
	}
	for _, p := range c.Parties {
		if p.ActorID == id {
			return true
		}
	}
	return false
}

// PartyName returns the name a party had at filing, or "" when the actor is not
// a party.
func (c *CourtCase) PartyName(id ActorID) string {
	for _, p := range c.Parties {
		if p.ActorID == id {
			return p.Name
		}
	}
	return ""
}

func newCourtCaseID() CourtCaseID {
	return CourtCaseID("case-" + randomHex(4))
}

// courtSittingHM parses the sitting time, falling back to the default on a
// malformed setting (the dawn/dusk posture: string setting, parsed at use).
func courtSittingHM(spec string) (int, int) {
	if spec == "" {
		spec = DefaultCourtSittingTime
	}
	h, m, err := ParseHM(spec)
	if err != nil {
		h, m, _ = ParseHM(DefaultCourtSittingTime)
	}
	return h, m
}

func worldLocation(w *World) *time.Location {
	if w.Settings.Location != nil {
		return w.Settings.Location
	}
	return time.Local
}

// MostRecentCourtSitting is the latest sitting at or before now.
func MostRecentCourtSitting(spec string, loc *time.Location, now time.Time) time.Time {
	if loc == nil {
		loc = time.Local
	}
	h, m := courtSittingHM(spec)
	return MostRecentRotationBoundary(now.In(loc), h, m)
}

// NextCourtSittingAfter is the first sitting strictly after t — when a case
// filed at t is heard.
func NextCourtSittingAfter(spec string, loc *time.Location, t time.Time) time.Time {
	if loc == nil {
		loc = time.Local
	}
	h, m := courtSittingHM(spec)
	last := MostRecentRotationBoundary(t.In(loc), h, m)
	local := last.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, h, m, 0, 0, loc)
}

func courtDailyCaseLimit(w *World) int {
	if w.Settings.CourtDailyCaseLimit > 0 {
		return w.Settings.CourtDailyCaseLimit
	}
	return DefaultCourtDailyCaseLimit
}

// courtCasesFiledToday counts the cases one actor brought this game-day.
func courtCasesFiledToday(w *World, filerID ActorID, now time.Time) int {
	start := gameDayStart(w, now)
	n := 0
	for _, c := range w.CourtCases {
		if c != nil && !c.Seeded && c.FiledByID == filerID && !c.FiledAt.Before(start) {
			n++
		}
	}
	return n
}

// courtResidentByName resolves a villager the court can hear: a resident NPC or
// a player, never a traveler or scenery. An exact name wins; otherwise the
// unique villager whose name ends with the given words ("Gideon Marsh" for
// "Constable Gideon Marsh").
func courtResidentByName(w *World, name string) (*Actor, error) {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return nil, ModelFacingError{Msg: "a party was named with no name."}
	}
	var exact, suffix []*Actor
	for _, a := range w.Actors {
		if a == nil || !courtCanHear(a) {
			continue
		}
		full := strings.ToLower(strings.TrimSpace(a.DisplayName))
		switch {
		case full == needle:
			exact = append(exact, a)
		case strings.HasSuffix(full, " "+needle):
			suffix = append(suffix, a)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = suffix
	}
	switch len(pick) {
	case 1:
		return pick[0], nil
	case 0:
		return nil, ModelFacingError{Msg: fmt.Sprintf("no villager named %q is known to the court; name each one as the village knows them.", strings.TrimSpace(name))}
	default:
		return nil, ModelFacingError{Msg: fmt.Sprintf("more than one villager answers to %q; give the full name.", strings.TrimSpace(name))}
	}
}

// courtCanHear: residents and players. A traveler is gone before the court sits,
// and scenery (pen animals, the lamplighter) has no part in a dispute.
func courtCanHear(a *Actor) bool {
	if a.VisitorState != nil || IsVisitorActorID(a.ID) {
		return false
	}
	switch a.Kind {
	case KindNPCStateful, KindNPCShared, KindPC:
		return true
	}
	return false
}

// CourtFileResult is what FileCourtCase returns: the case as filed and when it
// will be heard.
type CourtFileResult struct {
	Case    *CourtCase
	HeardAt time.Time
	// Message is the filer's tool result.
	Message string
}

// FileCourtCase puts a matter on the docket. The constable's tool calls it with
// operator=false: the filer must carry the constable attribute and is held to
// the daily limit. The umbilical seeding route calls it with operator=true: the
// filer may be any villager and the limit does not apply.
func FileCourtCase(filerID ActorID, partyNames []string, complaint string, now time.Time, operator bool) Command {
	return Command{Fn: func(w *World) (any, error) {
		filer := w.Actors[filerID]
		if filer == nil {
			return nil, fmt.Errorf("court: unknown filer %q", filerID)
		}
		if !operator && !ActorIsConstable(filer) {
			return nil, ModelFacingError{Msg: "only the constable brings matters before the magistrates."}
		}
		if operator && !courtCanHear(filer) {
			return nil, fmt.Errorf("court: %s cannot bring a matter before the court", filer.DisplayName)
		}
		complaint = strings.TrimSpace(complaint)
		if complaint == "" {
			return nil, ModelFacingError{Msg: "say what the matter is — the complaint cannot be empty."}
		}
		if utf8.RuneCountInString(complaint) > MaxCourtComplaintRunes {
			return nil, ModelFacingError{Msg: fmt.Sprintf("the complaint is too long; put it in under %d characters.", MaxCourtComplaintRunes)}
		}
		if len(partyNames) == 0 {
			return nil, ModelFacingError{Msg: "name the villagers the matter concerns."}
		}
		if len(partyNames) > MaxCourtParties {
			return nil, ModelFacingError{Msg: fmt.Sprintf("a matter may name at most %d villagers.", MaxCourtParties)}
		}
		var parties []CourtParty
		seen := map[ActorID]bool{}
		for _, n := range partyNames {
			a, err := courtResidentByName(w, n)
			if err != nil {
				return nil, err
			}
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			parties = append(parties, CourtParty{ActorID: a.ID, Name: a.DisplayName})
		}
		if !operator {
			limit := courtDailyCaseLimit(w)
			if courtCasesFiledToday(w, filerID, now) >= limit {
				return nil, ModelFacingError{Msg: "the court's docket for this sitting is full; bring it at the next."}
			}
		}
		if w.CourtCases == nil {
			w.CourtCases = make(map[CourtCaseID]*CourtCase)
		}
		id := newCourtCaseID()
		for _, exists := w.CourtCases[id]; exists; _, exists = w.CourtCases[id] {
			id = newCourtCaseID()
		}
		c := &CourtCase{
			ID:          id,
			FiledAt:     now,
			FiledByID:   filer.ID,
			FiledByName: filer.DisplayName,
			Parties:     parties,
			Complaint:   complaint,
			Status:      CourtCaseStatusPending,
			Seeded:      operator,
		}
		w.CourtCases[id] = c
		names := courtPartyNames(parties)
		if _, err := AppendActionLogEntry(ActionLogEntry{
			ActorID:          filer.ID,
			OccurredAt:       now,
			ActionType:       ActionTypeBroughtCase,
			Text:             complaint,
			HuddleID:         filer.CurrentHuddleID,
			CounterpartyName: joinCourtNames(names),
		}).Fn(w); err != nil {
			log.Printf("sim/court: append brought_case for %s: %v", filer.DisplayName, err)
		}
		w.AppendActionLogDurable(DurableActionLogRow{
			ActorID:    filer.ID,
			OccurredAt: now,
			ActionType: ActionTypeBroughtCase,
			Payload: map[string]any{
				"case_id":   string(id),
				"parties":   names,
				"complaint": complaint,
			},
			SpeakerName: filer.DisplayName,
			HuddleID:    filer.CurrentHuddleID,
			Source:      "engine",
		})
		heard := NextCourtSittingAfter(w.Settings.CourtSittingTime, worldLocation(w), now)
		log.Printf("sim/court: %s brought %s before the magistrates (%s): %q — heard %s",
			filer.DisplayName, id, joinCourtNames(names), complaint, heard.Format(time.RFC3339))
		hearing := courtHearingFor(heard, now, worldLocation(w))
		return CourtFileResult{
			Case:    c.Clone(),
			HeardAt: heard,
			Message: courtFiledMessage(hearing, CourtSittingPhrase(w.Settings.CourtSittingTime)),
		}, nil
	}}
}

func courtPartyNames(parties []CourtParty) []string {
	out := make([]string, 0, len(parties))
	for _, p := range parties {
		out = append(out, p.Name)
	}
	return out
}

// joinCourtNames joins names as prose: "A", "A and B", "A, B and C".
func joinCourtNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// CourtCasesToHear returns, oldest first, the pending cases the court should
// hear now: those filed before the most recent sitting. force returns every
// pending case regardless of the sitting (the umbilical's sit-now route).
func CourtCasesToHear(now time.Time, force bool) Command {
	return Command{Fn: func(w *World) (any, error) {
		sitting := MostRecentCourtSitting(w.Settings.CourtSittingTime, worldLocation(w), now)
		var out []*CourtCase
		for _, c := range w.CourtCases {
			if c == nil || c.Status != CourtCaseStatusPending {
				continue
			}
			if !force && !c.FiledAt.Before(sitting) {
				continue
			}
			out = append(out, c.Clone())
		}
		sort.Slice(out, func(i, j int) bool {
			if !out[i].FiledAt.Equal(out[j].FiledAt) {
				return out[i].FiledAt.Before(out[j].FiledAt)
			}
			return out[i].ID < out[j].ID
		})
		return out, nil
	}}
}

// CourtCaseList returns every case, newest first — the umbilical read.
func CourtCaseList() Command {
	return Command{Fn: func(w *World) (any, error) {
		out := make([]*CourtCase, 0, len(w.CourtCases))
		for _, c := range w.CourtCases {
			if c != nil {
				out = append(out, c.Clone())
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].FiledAt.After(out[j].FiledAt) })
		return out, nil
	}}
}

// CourtRuling is the magistrate's ruling as he gave it, names unresolved.
type CourtRuling struct {
	Result    CourtResult
	FoundFor  string
	Payer     string
	Payee     string
	Amount    int
	Words     string
	DecidedAt time.Time

	// Recovered marks a ruling read back from the durable record rather than
	// given in a session: the engine crashed after some of the ruling's rows
	// were written and before a checkpoint saved the case and the purses, so
	// the case reloaded pending. Applying it finishes the SAME ruling: the case
	// takes the recorded result, words and time; a pay order moves exactly
	// RecoveredPaid again (the purses rolled back with the case) and fails
	// rather than change the outcome if the payer can no longer cover it; and
	// only the durable rows the record is MISSING are written —
	// RecordedRulingFor and PaymentRecorded say which exist. A payment already
	// on record is not credited to the coin record again (the boot seed read it).
	Recovered         bool
	RecoveredPaid     int
	RecordedRulingFor map[ActorID]bool
	PaymentRecorded   bool
	// RecordedPayment is the payment row on record, when there is one; recovery
	// refuses unless its payer, payee and amount are the ruling's.
	RecordedPayment CourtPaymentRow
}

// CourtRecord is what the durable record holds for one case: every `ruled`
// row (the first is the canonical outcome — a ruling writes every `ruled` row
// before its `paid` row; the rest must agree with it), who has a `ruled` row,
// and every court `paid` row (there must be at most one).
type CourtRecord struct {
	Ruling   map[string]any
	Rulings  []map[string]any
	RuledAt  time.Time
	RuledFor map[ActorID]bool
	Payments []CourtPaymentRow
}

// CourtPaymentRow is a court `paid` row as recorded.
type CourtPaymentRow struct {
	PayerID ActorID
	PayeeID ActorID
	Amount  int
}

// CourtRulingFromRecord rebuilds the recorded ruling for recovery, validating
// it: a recognised result and words, the parties a result needs, whole-number
// amounts with 0 <= paid <= ordered <= MaxCourtPayOrder, every `ruled` row
// agreeing with the first, and at most one payment row, present only when the
// ruling paid something. Recovery refuses on any of these rather than finish a
// ruling the record does not agree on; the case then waits for the operator.
func CourtRulingFromRecord(rec CourtRecord) (CourtRuling, error) {
	r, err := courtRulingFromPayload(rec.Ruling)
	if err != nil {
		return CourtRuling{}, err
	}
	for _, other := range rec.Rulings {
		o, err := courtRulingFromPayload(other)
		if err != nil {
			return CourtRuling{}, err
		}
		if o.Result != r.Result || o.Words != r.Words || o.FoundFor != r.FoundFor || o.Payer != r.Payer ||
			o.Payee != r.Payee || o.Amount != r.Amount || o.RecoveredPaid != r.RecoveredPaid {
			return CourtRuling{}, errors.New("court: the recorded rulings for one case disagree")
		}
	}
	if len(rec.Payments) > 1 {
		return CourtRuling{}, fmt.Errorf("court: the record holds %d payments for one ruling", len(rec.Payments))
	}
	r.DecidedAt = rec.RuledAt
	r.RecordedRulingFor = rec.RuledFor
	r.PaymentRecorded = len(rec.Payments) == 1
	if r.PaymentRecorded {
		r.RecordedPayment = rec.Payments[0]
		if r.Result != CourtResultPay || r.RecoveredPaid != r.RecordedPayment.Amount {
			return CourtRuling{}, fmt.Errorf("court: the recorded payment (%d coins) does not match the ruling (%s, %d paid)", r.RecordedPayment.Amount, r.Result, r.RecoveredPaid)
		}
	}
	return r, nil
}

// courtRulingFromPayload validates one recorded `ruled` payload and returns it
// as a typed ruling (Recovered set; the record-level fields left for the caller).
func courtRulingFromPayload(p map[string]any) (CourtRuling, error) {
	str := func(k string) string {
		s, _ := p[k].(string)
		return strings.TrimSpace(s)
	}
	r := CourtRuling{
		Result:    CourtResult(str("result")),
		FoundFor:  str("found_for"),
		Payer:     str("payer"),
		Payee:     str("payee"),
		Words:     str("words"),
		Recovered: true,
	}
	if !r.Result.Valid() {
		return CourtRuling{}, fmt.Errorf("court: recorded result %q is not one of the four", r.Result)
	}
	if r.Words == "" {
		return CourtRuling{}, errors.New("court: recorded ruling has no words")
	}
	switch r.Result {
	case CourtResultFoundFor:
		if r.FoundFor == "" {
			return CourtRuling{}, errors.New("court: recorded found_for ruling names no party")
		}
	case CourtResultPay:
		if r.Payer == "" || r.Payee == "" {
			return CourtRuling{}, errors.New("court: recorded pay ruling lacks a payer or payee")
		}
		ordered, err := CourtRecordedAmount(p["amount_ordered"])
		if err != nil {
			return CourtRuling{}, fmt.Errorf("court: recorded amount_ordered: %w", err)
		}
		paid, err := CourtRecordedAmount(p["amount_paid"])
		if err != nil {
			return CourtRuling{}, fmt.Errorf("court: recorded amount_paid: %w", err)
		}
		if ordered < 1 || paid > ordered {
			return CourtRuling{}, fmt.Errorf("court: recorded pay order is inconsistent (ordered %d, paid %d)", ordered, paid)
		}
		r.Amount, r.RecoveredPaid = ordered, paid
	}
	return r, nil
}

// CourtRecordedAmount parses a recorded coin amount strictly: a JSON number
// that is a finite whole number in 0..MaxCourtPayOrder. Anything else — a
// string, a fraction, a missing value — is an error, never a silent zero.
func CourtRecordedAmount(v any) (int, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("not a number: %v", v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > MaxCourtPayOrder {
		return 0, fmt.Errorf("not a whole number in 0..%d: %v", MaxCourtPayOrder, f)
	}
	return int(f), nil
}

// CourtRulingApplied is what ApplyCourtRuling returns.
type CourtRulingApplied struct {
	Case *CourtCase
}

// casePartyByName resolves a name against the case's parties (exact, then the
// unique name ending with the given words).
func casePartyByName(c *CourtCase, name string) (CourtParty, error) {
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return CourtParty{}, ModelFacingError{Msg: "name the party."}
	}
	var exact, suffix []CourtParty
	for _, p := range c.Parties {
		full := strings.ToLower(p.Name)
		switch {
		case full == needle:
			exact = append(exact, p)
		case strings.HasSuffix(full, " "+needle):
			suffix = append(suffix, p)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = suffix
	}
	if len(pick) == 1 {
		return pick[0], nil
	}
	return CourtParty{}, ModelFacingError{Msg: fmt.Sprintf("%q is not a party to this matter; the parties are %s.", strings.TrimSpace(name), joinCourtNames(courtPartyNames(c.Parties)))}
}

// ApplyCourtRuling records the ruling and carries it out: a pay order moves
// min(ordered, payer's coins) and no debt is carried; each party and the filer
// get the ruling as a beat (their next prompt), a ring entry (the talk panel and
// a shared NPC's consolidation) and a durable `ruled` row (the night's dream).
// A ruling the engine cannot apply comes back as a ModelFacingError so the
// magistrate can correct it in the same session.
func ApplyCourtRuling(caseID CourtCaseID, r CourtRuling, now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		c := w.CourtCases[caseID]
		if c == nil {
			return nil, fmt.Errorf("court: unknown case %q", caseID)
		}
		if c.Status != CourtCaseStatusPending {
			return nil, fmt.Errorf("court: case %s is already ruled", caseID)
		}
		if !r.Result.Valid() {
			return nil, ModelFacingError{Msg: "the result must be one of no_case, found_for, pay or no_such_charge."}
		}
		words := strings.TrimSpace(r.Words)
		if words == "" {
			return nil, ModelFacingError{Msg: "give the ruling's words — what the parties will hear."}
		}
		if utf8.RuneCountInString(words) > MaxCourtWordsRunes {
			return nil, ModelFacingError{Msg: fmt.Sprintf("the ruling is too long; keep it under %d characters.", MaxCourtWordsRunes)}
		}
		var foundFor, payer, payee CourtParty
		switch r.Result {
		case CourtResultFoundFor:
			p, err := casePartyByName(c, r.FoundFor)
			if err != nil {
				return nil, err
			}
			foundFor = p
		case CourtResultPay:
			var err error
			if payer, err = casePartyByName(c, r.Payer); err != nil {
				return nil, err
			}
			if payee, err = casePartyByName(c, r.Payee); err != nil {
				return nil, err
			}
			if payer.ActorID == payee.ActorID {
				return nil, ModelFacingError{Msg: "the payer and the payee must be different parties."}
			}
			if r.Amount <= 0 || r.Amount > MaxCourtPayOrder {
				return nil, ModelFacingError{Msg: fmt.Sprintf("a pay order must be between 1 and %d coins.", MaxCourtPayOrder)}
			}
		}

		// The sum a pay order moves is settled before anything is written, so
		// every `ruled` row can name it and be written BEFORE the payment: the
		// record then never holds a payment without the ruling behind it, and the
		// first `ruled` row is the canonical outcome recovery reads.
		paid := 0
		if r.Result == CourtResultPay {
			payerActor := w.Actors[payer.ActorID]
			if r.Recovered {
				paid = r.RecoveredPaid
				if paid > 0 && (payerActor == nil || w.Actors[payee.ActorID] == nil || payerActor.Coins < paid) {
					return nil, fmt.Errorf("court: recovering %s: the payer can no longer cover the recorded %d coins", caseID, paid)
				}
				if r.PaymentRecorded && (r.RecordedPayment.PayerID != payer.ActorID || r.RecordedPayment.PayeeID != payee.ActorID) {
					return nil, fmt.Errorf("court: recovering %s: the recorded payment is between other villagers than the ruling names", caseID)
				}
			} else if payerActor != nil && w.Actors[payee.ActorID] != nil {
				paid = min(r.Amount, max(payerActor.Coins, 0))
			}
		}

		c.Status = CourtCaseStatusRuled
		c.RuledAt = now
		if r.Recovered && !r.DecidedAt.IsZero() {
			c.RuledAt = r.DecidedAt
		}
		c.Result = r.Result
		c.Words = words
		c.FoundForID = foundFor.ActorID
		if r.Result == CourtResultPay {
			c.PayerID = payer.ActorID
			c.PayeeID = payee.ActorID
			c.AmountOrdered = r.Amount
			c.AmountPaid = paid
		}

		for _, id := range courtRecipients(c) {
			courtDeliverRuling(w, c, id, now, !r.RecordedRulingFor[id])
		}
		if r.Result == CourtResultPay && paid > 0 {
			courtCarryOutPayOrder(w, c, now, !r.PaymentRecorded)
		}
		verb := "ruled"
		if r.Recovered {
			verb = "recovered the ruling on"
		}
		log.Printf("sim/court: %s %s (%s) result=%s paid=%d/%d: %q",
			verb, c.ID, joinCourtNames(courtPartyNames(c.Parties)), c.Result, c.AmountPaid, c.AmountOrdered, words)
		return CourtRulingApplied{Case: c.Clone()}, nil
	}}
}

// courtCarryOutPayOrder moves c.AmountPaid (settled by ApplyCourtRuling) and
// records it like any other payment: the payer's ring entry, and — unless the
// record already holds it (recovery) — the coin record and the durable `paid`
// row. court_case_id marks the row as the court's; the coin record has no court
// kind, so it reads Unstated, the safe zero.
func courtCarryOutPayOrder(w *World, c *CourtCase, now time.Time, durable bool) {
	payer := w.Actors[c.PayerID]
	payee := w.Actors[c.PayeeID]
	amount := c.AmountPaid
	if payer == nil || payee == nil || amount <= 0 {
		return
	}
	payer.Coins -= amount
	payee.Coins += amount
	forText := "by order of the magistrates"
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          payer.ID,
		OccurredAt:       now,
		ActionType:       ActionTypePaid,
		Text:             forText,
		HuddleID:         payer.CurrentHuddleID,
		CounterpartyName: payee.DisplayName,
		Amount:           amount,
	}).Fn(w); err != nil {
		log.Printf("sim/court: append paid for %s: %v", payer.DisplayName, err)
	}
	if !durable {
		return
	}
	w.RecordCoinPaid(payer.ID, payee.ID, amount, now, CoinPaymentUnstated)
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    payer.ID,
		OccurredAt: now,
		ActionType: ActionTypePaid,
		Payload: map[string]any{
			"recipient":          payee.DisplayName,
			"recipient_actor_id": string(payee.ID),
			"amount":             amount,
			"for":                forText,
			"court_case_id":      string(c.ID),
		},
		SpeakerName: payer.DisplayName,
		HuddleID:    payer.CurrentHuddleID,
		Source:      "engine",
	})
}

// courtRecipients is everyone the ruling reaches: the parties, then the filer
// when he is not one of them.
func courtRecipients(c *CourtCase) []ActorID {
	out := make([]ActorID, 0, len(c.Parties)+1)
	seen := map[ActorID]bool{}
	for _, p := range c.Parties {
		if !seen[p.ActorID] {
			seen[p.ActorID] = true
			out = append(out, p.ActorID)
		}
	}
	if c.FiledByID != "" && !seen[c.FiledByID] {
		out = append(out, c.FiledByID)
	}
	return out
}

// courtDeliverRuling gives one recipient the beat and the ring entry, and —
// unless the ruling is being recovered, whose rows the record already holds —
// the durable `ruled` row.
func courtDeliverRuling(w *World, c *CourtCase, id ActorID, now time.Time, durable bool) {
	a := w.Actors[id]
	if a == nil {
		return
	}
	narration := CourtRulingNarration(c, id)
	tryStampWarrant(w, a, WarrantMeta{
		TriggerActorID: c.FiledByID,
		Reason:         CourtRuledWarrantReason{CaseID: c.ID, FiledBy: c.FiledByID, NarrationText: narration},
		OccurredAt:     now,
	}, now)
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:          a.ID,
		OccurredAt:       now,
		ActionType:       ActionTypeRuled,
		Text:             narration,
		HuddleID:         a.CurrentHuddleID,
		CounterpartyName: c.FiledByName,
	}).Fn(w); err != nil {
		log.Printf("sim/court: append ruled for %s: %v", a.DisplayName, err)
	}
	if !durable {
		return
	}
	payload := map[string]any{
		"case_id":    string(c.ID),
		"result":     string(c.Result),
		"complaint":  c.Complaint,
		"brought_by": c.FiledByName,
		"parties":    courtPartyNames(c.Parties),
		"words":      c.Words,
		"text":       narration,
	}
	if c.Result == CourtResultPay {
		payload["amount_ordered"] = c.AmountOrdered
		payload["amount_paid"] = c.AmountPaid
		payload["payer"] = c.PartyName(c.PayerID)
		payload["payee"] = c.PartyName(c.PayeeID)
	}
	if c.Result == CourtResultFoundFor {
		payload["found_for"] = c.PartyName(c.FoundForID)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:     a.ID,
		OccurredAt:  now,
		ActionType:  ActionTypeRuled,
		Payload:     payload,
		SpeakerName: a.DisplayName,
		HuddleID:    a.CurrentHuddleID,
		Source:      "engine",
	})
}

// CourtRuledWarrantReason is a party's (or the filer's) beat for a ruling. The
// court sits off the map with no turn of the recipient's in flight, so this is
// how the ruling reaches the next prompt. Narration pre-rendered per recipient.
type CourtRuledWarrantReason struct {
	CaseID        CourtCaseID
	FiledBy       ActorID
	NarrationText string
}

func (CourtRuledWarrantReason) isWarrantReason()           {}
func (CourtRuledWarrantReason) Kind() WarrantKind          { return WarrantKindCourtRuled }
func (CourtRuledWarrantReason) DedupDiscriminator() uint64 { return 0 }

// CourtRulingNarration is one recipient's line: which matter, the magistrate's
// words as given, what the engine did about a pay order, and that the matter is
// closed. The last sentence is the point — it is what the dream records in place
// of an open matter. No pronouns: the village does not model gender.
func CourtRulingNarration(c *CourtCase, recipient ActorID) string {
	return courtRulingText(c, recipient, "Word has come from the magistrates in Salem Town on the matter ")
}

// CourtRulingStandingLine is the same ruling as a standing fact, for the days
// after it ("On 3 October the magistrates in Salem Town ruled on the matter
// …"). The beat reaches only the next turn and a sleeping villager's beats are
// dropped once stale, so the closed matter also stands in the prompt of every
// party and the filer for CourtRulingNoticeDays (LLM-695).
func CourtRulingStandingLine(c *CourtCase, recipient ActorID, day string) string {
	return courtRulingText(c, recipient, "On "+day+" the magistrates in Salem Town ruled on the matter ")
}

func courtRulingText(c *CourtCase, recipient ActorID, lead string) string {
	var b strings.Builder
	b.WriteString(lead)
	if recipient != "" && recipient == c.FiledByID {
		b.WriteString("you brought before them")
	} else {
		b.WriteString(c.FiledByName + " brought before them")
	}
	b.WriteString(" (\"" + elideCourtText(c.Complaint, 200) + "\"): \"" + c.Words + "\"")
	if c.Result == CourtResultPay {
		b.WriteString(" " + courtPayClause(c, recipient))
	}
	b.WriteString(" The matter is closed.")
	return b.String()
}

func courtPayClause(c *CourtCase, recipient ActorID) string {
	payer := c.PartyName(c.PayerID)
	payee := c.PartyName(c.PayeeID)
	payerSubj, payeeObj := payer, payee
	switch recipient {
	case c.PayerID:
		payerSubj = "you"
	case c.PayeeID:
		payeeObj = "you"
	}
	ordered := coinCount(c.AmountOrdered)
	switch {
	case c.AmountPaid >= c.AmountOrdered:
		return fmt.Sprintf("By their order %s handed %s %s.", payerSubj, payeeObj, ordered)
	case c.AmountPaid <= 0:
		have := payer + " had"
		if recipient == c.PayerID {
			have = "you had"
		}
		return fmt.Sprintf("By their order %s was to hand %s %s, but %s no coin to hand over, and the court asks no more.",
			payerSubj, payeeObj, ordered, have)
	default:
		have := payer + " had"
		if recipient == c.PayerID {
			have = "you had"
		}
		return fmt.Sprintf("By their order %s was to hand %s %s; %s only %s, and those were handed over. The court asks no more.",
			payerSubj, payeeObj, ordered, have, coinCount(c.AmountPaid))
	}
}

func coinCount(n int) string {
	if n == 1 {
		return "1 coin"
	}
	return fmt.Sprintf("%d coins", n)
}

func elideCourtText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max])) + "…"
}

// CourtDocketEntry is one pending case as perception sees it.
type CourtDocketEntry struct {
	Case    *CourtCase
	HeardAt time.Time
	// Hearing is when, in the village's calendar, the case is heard — computed
	// here because the snapshot carries no time.Location.
	Hearing CourtHearing
}

// CourtHearing places a pending case's sitting relative to now.
type CourtHearing string

const (
	CourtHearingToday    CourtHearing = "today"
	CourtHearingTomorrow CourtHearing = "tomorrow"
	// CourtHearingAwaiting — the sitting has passed and no ruling is recorded
	// yet (the session is running, or failed and will retry).
	CourtHearingAwaiting CourtHearing = "awaiting"
)

func courtHearingFor(heardAt, now time.Time, loc *time.Location) CourtHearing {
	if !heardAt.After(now) {
		return CourtHearingAwaiting
	}
	hy, hm, hd := heardAt.In(loc).Date()
	ny, nm, nd := now.In(loc).Date()
	if hy == ny && hm == nm && hd == nd {
		return CourtHearingToday
	}
	return CourtHearingTomorrow
}

// courtDocketForSnapshot clones the pending cases with their hearing time, and
// counts each filer's cases this game-day, for the constable's section.
// CourtRulingNoticeDays is how long a ruling stands in the prompt of the
// villagers it concerns.
const CourtRulingNoticeDays = 3

// CourtRecentRuling is a ruling inside the notice window, with the village date
// it was given ("3 October") — computed here because the snapshot carries no
// time.Location.
type CourtRecentRuling struct {
	Case *CourtCase
	Day  string
}

func courtDocketForSnapshot(w *World, now time.Time) ([]CourtDocketEntry, map[ActorID]int, []CourtRecentRuling) {
	if len(w.CourtCases) == 0 {
		return nil, nil, nil
	}
	start := gameDayStart(w, now)
	loc := worldLocation(w)
	noticeFrom := now.AddDate(0, 0, -CourtRulingNoticeDays)
	var docket []CourtDocketEntry
	var today map[ActorID]int
	var recent []CourtRecentRuling
	for _, c := range w.CourtCases {
		if c == nil {
			continue
		}
		if c.Status == CourtCaseStatusRuled && c.RuledAt.After(noticeFrom) {
			recent = append(recent, CourtRecentRuling{Case: c.Clone(), Day: c.RuledAt.In(loc).Format("2 January")})
		}
		if c.FiledByID != "" && !c.Seeded && !c.FiledAt.Before(start) {
			if today == nil {
				today = make(map[ActorID]int)
			}
			today[c.FiledByID]++
		}
		if c.Status == CourtCaseStatusPending {
			heard := NextCourtSittingAfter(w.Settings.CourtSittingTime, loc, c.FiledAt)
			docket = append(docket, CourtDocketEntry{
				Case:    c.Clone(),
				HeardAt: heard,
				Hearing: courtHearingFor(heard, now, loc),
			})
		}
	}
	sort.Slice(docket, func(i, j int) bool { return docket[i].Case.FiledAt.Before(docket[j].Case.FiledAt) })
	sort.Slice(recent, func(i, j int) bool { return recent[i].Case.RuledAt.Before(recent[j].Case.RuledAt) })
	return docket, today, recent
}

// rehydrateCourtCasesOnLoad loads the docket and the rulings at boot. A missing
// repo (a test Repository without the tier) leaves an empty docket.
func (w *World) rehydrateCourtCasesOnLoad(ctx context.Context) error {
	if w.repo.CourtCases == nil {
		if w.CourtCases == nil {
			w.CourtCases = make(map[CourtCaseID]*CourtCase)
		}
		return nil
	}
	cases, err := w.repo.CourtCases.LoadAll(ctx)
	if err != nil {
		return err
	}
	if cases == nil {
		cases = make(map[CourtCaseID]*CourtCase)
	}
	w.CourtCases = cases
	pending := 0
	for _, c := range cases {
		if c != nil && c.Status == CourtCaseStatusPending {
			pending++
		}
	}
	if len(cases) > 0 {
		log.Printf("sim: rehydrated %d court case(s), %d pending", len(cases), pending)
	}
	return nil
}

// CourtSittingPhrase says an HH:MM sitting time the way the village would
// ("noon", "3 o'clock in the afternoon").
func CourtSittingPhrase(spec string) string {
	h, m := courtSittingHM(spec)
	if h == 12 && m == 0 {
		return "noon"
	}
	hour12 := h % 12
	if hour12 == 0 {
		hour12 = 12
	}
	part := "in the morning"
	switch {
	case h >= 18:
		part = "in the evening"
	case h >= 12:
		part = "in the afternoon"
	}
	if m == 0 {
		return fmt.Sprintf("%d o'clock %s", hour12, part)
	}
	return fmt.Sprintf("%d:%02d %s", hour12, m, part)
}

// courtFiledMessage is the filer's tool result: where the matter went and when
// it is heard.
func courtFiledMessage(hearing CourtHearing, sitting string) string {
	when := "tomorrow at " + sitting
	if hearing == CourtHearingToday {
		when = "today at " + sitting
	}
	return "The matter is sent to the magistrates in Salem Town; it is heard " + when + ", and their word will come to everyone it concerns."
}
