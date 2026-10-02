package court

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// tools.go — the magistrate's tools. Every one reads, except `rule` (the
// closed set the engine validates and applies) and `amend_bench_book` (his own
// note). There is no tool that moves a coin, an item or a villager.

const (
	// maxSpanDays caps one read_record window.
	maxSpanDays = 7
	// defaultSpanDays is the window when he gives no dates: today and the two
	// days before.
	defaultSpanDays = 3
	// maxRecordLines caps one read_record answer.
	maxRecordLines = 250
)

var toolSpecs = []llm.ToolSpec{
	{
		Name:        "read_record",
		Description: "Read the village's record for one villager over a span of days: everything they did (what they paid, sold, handed over, gathered, ate, where they went) and every word said in the conversations they were part of, by them and by others. With `with`, only what passed between the two of them: the conversations both were in, and each one's dealings naming the other. Dates are village dates, YYYY-MM-DD, both days included; at most 7 days at a time. With no dates, today and the two days before.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "person": {"type": "string", "description": "The villager whose record to read."},
                "with":   {"type": "string", "description": "Optional. Another villager: read only what passed between the two."},
                "from":   {"type": "string", "description": "Optional. First day, YYYY-MM-DD."},
                "to":     {"type": "string", "description": "Optional. Last day, YYYY-MM-DD."}
            },
            "required": ["person"],
            "additionalProperties": false
        }`),
	},
	{
		Name:        "look_in_purse",
		Description: "See what a villager holds right now: their coin and their goods. It says nothing of what they held before.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {"person": {"type": "string"}},
            "required": ["person"],
            "additionalProperties": false
        }`),
	},
	{
		Name:        "ask_about_goods",
		Description: "Ask whether a thing exists among the village's goods at all, and who holds any of it now. The village's goods are a fixed list: a thing that is not on it has never been made, bought, sold, held, carried or taken by anyone here.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {"name": {"type": "string", "description": "The thing, as it was named (e.g. \"ledger\", \"whetstone\")."}},
            "required": ["name"],
            "additionalProperties": false
        }`),
	},
	{
		Name:        "earlier_rulings",
		Description: "Read the court's earlier rulings on matters a villager was party to or brought.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {"person": {"type": "string"}},
            "required": ["person"],
            "additionalProperties": false
        }`),
	},
	{
		Name:        "amend_bench_book",
		Description: "Rewrite your bench book — the one note you keep from sitting to sitting. Give the WHOLE new text; it replaces what was there. Keep what will help you judge later matters: how the village's records answer questions, what you have learned about its people and its disputes. At most 4000 characters.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {"text": {"type": "string"}},
            "required": ["text"],
            "additionalProperties": false
        }`),
	},
	{
		Name:        "rule",
		Description: "Give the court's ruling. It is final and ends the hearing. `result` is one of: no_case (no case for want of proof — also the answer to a matter brought again after the court has ruled on it); found_for (the court finds for one party; name them in found_for); pay (one party is to hand another a sum of coin; give payer, payee and amount — order within what the payer holds); no_such_charge (a charge of witchcraft, which this court does not hear). `words` is the ruling as everyone the matter concerns will hear it, in the court's own voice.",
		Schema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "result":    {"type": "string", "enum": ["no_case", "found_for", "pay", "no_such_charge"]},
                "found_for": {"type": "string"},
                "payer":     {"type": "string"},
                "payee":     {"type": "string"},
                "amount":    {"type": "integer", "minimum": 1},
                "words":     {"type": "string", "maxLength": 1500}
            },
            "required": ["result", "words"],
            "additionalProperties": false
        }`),
	},
}

// run executes one tool call and returns its result text.
func (s *session) run(ctx context.Context, call llm.RawToolCall) string {
	switch call.Name {
	case "read_record":
		var a struct{ Person, With, From, To string }
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.readRecord(ctx, a.Person, a.With, a.From, a.To)
	case "look_in_purse":
		var a struct{ Person string }
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.worldText(ctx, sim.CourtPurse(a.Person))
	case "ask_about_goods":
		var a struct{ Name string }
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.worldText(ctx, sim.CourtGoodsLookup(a.Name))
	case "earlier_rulings":
		var a struct{ Person string }
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.earlierRulings(ctx, a.Person)
	case "amend_bench_book":
		var a struct{ Text string }
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.amendBenchBook(ctx, a.Text)
	case "rule":
		var a struct {
			Result   string `json:"result"`
			FoundFor string `json:"found_for"`
			Payer    string `json:"payer"`
			Payee    string `json:"payee"`
			Amount   int    `json:"amount"`
			Words    string `json:"words"`
		}
		if err := decodeArgs(call.Arguments, &a); err != nil {
			return err.Error()
		}
		return s.rule(ctx, sim.CourtRuling{
			Result:   sim.CourtResult(strings.TrimSpace(a.Result)),
			FoundFor: a.FoundFor,
			Payer:    a.Payer,
			Payee:    a.Payee,
			Amount:   a.Amount,
			Words:    a.Words,
		})
	}
	return fmt.Sprintf("[error] There is no tool named %q.", call.Name)
}

func decodeArgs(raw json.RawMessage, into any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("[error] The arguments could not be read: %v", err)
	}
	return nil
}

// worldText runs a read command that answers with text; a model-facing refusal
// comes back as its message.
func (s *session) worldText(ctx context.Context, cmd sim.Command) string {
	res, err := s.r.w.SendContext(ctx, cmd)
	if err != nil {
		return refusal(err)
	}
	text, _ := res.(string)
	return text
}

func refusal(err error) string {
	var mfe sim.ModelFacingError
	if errors.As(err, &mfe) {
		return "[refused] " + capitalize(mfe.Msg)
	}
	return "[error] " + err.Error()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func (s *session) resolve(ctx context.Context, name string) (sim.CourtVillager, string) {
	res, err := s.r.w.SendContext(ctx, sim.CourtResolveVillager(name))
	if err != nil {
		return sim.CourtVillager{}, refusal(err)
	}
	v, _ := res.(sim.CourtVillager)
	return v, ""
}

// readRecord answers read_record from the durable record.
func (s *session) readRecord(ctx context.Context, person, with, from, to string) string {
	loc := s.info.Location
	if loc == nil {
		loc = time.Local
	}
	who, msg := s.resolve(ctx, person)
	if msg != "" {
		return msg
	}
	start, end, err := recordWindow(from, to, time.Now(), loc)
	if err != nil {
		return "[refused] " + err.Error()
	}
	var events []sim.SimDayEvent
	header := ""
	if strings.TrimSpace(with) != "" {
		other, msg := s.resolve(ctx, with)
		if msg != "" {
			return msg
		}
		events, err = s.r.records.LoadDealingsBetween(ctx, who.ID, other.ID, who.Name, other.Name, start, end, maxRecordLines+1)
		header = fmt.Sprintf("What passed between %s and %s", who.Name, other.Name)
	} else {
		events, err = s.r.records.LoadDayEvents(ctx, who.ID, start, end)
		header = "The record of " + who.Name
	}
	if err != nil {
		log.Printf("court: %s: read_record: %v", s.info.Case.ID, err)
		return "[error] The record could not be read just now."
	}
	lastDay := end.Add(-time.Nanosecond).In(loc)
	span := fmt.Sprintf("%s to %s", start.In(loc).Format("Jan 2"), lastDay.Format("Jan 2"))
	lines := renderEvents(events, loc)
	if len(lines) == 0 {
		return fmt.Sprintf("%s, %s: nothing is recorded.", header, span)
	}
	more := ""
	if len(lines) > maxRecordLines {
		more = fmt.Sprintf("\n(The record goes on past this point; read a shorter span to see the rest.)")
		lines = lines[:maxRecordLines]
	}
	return fmt.Sprintf("%s, %s:\n%s%s", header, span, strings.Join(lines, "\n"), more)
}

// recordWindow turns village dates into [start, end) instants. The record
// reaches back to April 2026.
func recordWindow(from, to string, now time.Time, loc *time.Location) (time.Time, time.Time, error) {
	day := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	parse := func(s string) (time.Time, error) {
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("%q is not a date — give it as YYYY-MM-DD", s)
		}
		return t, nil
	}
	last := day(now)
	if strings.TrimSpace(to) != "" {
		t, err := parse(to)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		last = t
	}
	first := last.AddDate(0, 0, -(defaultSpanDays - 1))
	if strings.TrimSpace(from) != "" {
		t, err := parse(from)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		first = t
		if strings.TrimSpace(to) == "" {
			last = first.AddDate(0, 0, defaultSpanDays-1)
			if last.After(day(now)) {
				last = day(now)
			}
		}
	}
	if last.Before(first) {
		return time.Time{}, time.Time{}, errors.New("the last day is before the first")
	}
	// Calendar days, not elapsed hours: across a clock change a day is 23 or 25
	// hours long.
	if !last.Before(first.AddDate(0, 0, maxSpanDays)) {
		return time.Time{}, time.Time{}, fmt.Errorf("read at most %d days at a time", maxSpanDays)
	}
	return first, last.AddDate(0, 0, 1), nil
}

func (s *session) earlierRulings(ctx context.Context, person string) string {
	res, err := s.r.w.SendContext(ctx, sim.CourtEarlierRulings(person))
	if err != nil {
		return refusal(err)
	}
	cases, _ := res.([]*sim.CourtCase)
	if len(cases) == 0 {
		return "The court has never ruled on a matter concerning " + strings.TrimSpace(person) + "."
	}
	loc := s.info.Location
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&b, "On %s the court ruled on the matter %s brought (%q), concerning %s: %s. The court said: %q\n",
			c.RuledAt.In(loc).Format("January 2"), c.FiledByName, c.Complaint, partyNames(c), resultPhrase(c), c.Words)
	}
	return strings.TrimRight(b.String(), "\n")
}

func partyNames(c *sim.CourtCase) string {
	names := make([]string, 0, len(c.Parties))
	for _, p := range c.Parties {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

func resultPhrase(c *sim.CourtCase) string {
	switch c.Result {
	case sim.CourtResultNoCase:
		return "no case"
	case sim.CourtResultFoundFor:
		return "found for " + c.PartyName(c.FoundForID)
	case sim.CourtResultPay:
		return fmt.Sprintf("%s to hand %s %s (%s handed over)", c.PartyName(c.PayerID), c.PartyName(c.PayeeID), coins(c.AmountOrdered), coins(c.AmountPaid))
	case sim.CourtResultNoSuchCharge:
		return "the court hears no such charge"
	}
	return string(c.Result)
}

func (s *session) amendBenchBook(ctx context.Context, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "[refused] The bench book cannot be emptied; give the whole text you want kept."
	}
	if n := utf8.RuneCountInString(text); n > MaxBenchBookRunes {
		return fmt.Sprintf("[refused] That is %d characters; the bench book holds at most %d. Shorten it and give the whole text again.", n, MaxBenchBookRunes)
	}
	if err := s.r.notes.SaveNote(ctx, Namespace, BenchBookSlug, "Bench book", text, ""); err != nil {
		log.Printf("court: %s: save bench book: %v", s.info.Case.ID, err)
		return "[error] The bench book could not be written just now."
	}
	return "[ok] Your bench book is rewritten."
}

func (s *session) rule(ctx context.Context, r sim.CourtRuling) string {
	r.DecidedAt = time.Now().UTC()
	res, err := s.r.w.SendContext(ctx, sim.ApplyCourtRuling(s.info.Case.ID, r, r.DecidedAt))
	if err != nil {
		var mfe sim.ModelFacingError
		if errors.As(err, &mfe) {
			return "[refused] " + capitalize(mfe.Msg) + " The ruling is not given; correct it and rule again."
		}
		log.Printf("court: %s: apply ruling: %v", s.info.Case.ID, err)
		s.aborted = true
		return "[error] The ruling could not be recorded."
	}
	s.ruled = true
	applied, _ := res.(sim.CourtRulingApplied)
	if applied.Case != nil && applied.Case.Result == sim.CourtResultPay && applied.Case.AmountPaid < applied.Case.AmountOrdered {
		return fmt.Sprintf("[ok] The ruling is given and sent to everyone the matter concerns. %s held only %s, which was handed over; the rest is forgiven.",
			applied.Case.PartyName(applied.Case.PayerID), coins(applied.Case.AmountPaid))
	}
	return "[ok] The ruling is given and sent to everyone the matter concerns."
}
