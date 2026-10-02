package court

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// render.go — what the magistrate reads: the session opening and the record
// lines. Plain village prose with names and village times; no ids, no table or
// field names, so nothing of the machinery can find its way into his words.

// renderOpening is the first message of a session: the matter, the village,
// and his bench book.
func renderOpening(info sim.CourtSessionInfo, bench string, now time.Time) string {
	loc := info.Location
	if loc == nil {
		loc = time.Local
	}
	c := info.Case
	var b strings.Builder
	b.WriteString("# A matter before the court\n\n")
	fmt.Fprintf(&b, "Brought by %s on %s:\n", c.FiledByName, c.FiledAt.In(loc).Format("Monday, January 2 at 3:04 PM"))
	b.WriteString("\"" + c.Complaint + "\"\n")
	names := make([]string, 0, len(c.Parties))
	for _, p := range c.Parties {
		names = append(names, p.Name)
	}
	b.WriteString("The villagers it concerns: " + strings.Join(names, ", ") + ".\n\n")
	fmt.Fprintf(&b, "It is now %s in the village. The court sits each day at %s.\n\n", now.In(loc).Format("Monday, January 2, 3:04 PM"), info.Sitting)

	b.WriteString("## The village\n")
	for _, e := range info.Roster {
		b.WriteString("- " + e.Name)
		var parts []string
		if e.Player {
			parts = append(parts, "a newcomer to the village")
		} else if e.Role != "" {
			parts = append(parts, e.Role)
		}
		if e.Work != "" {
			parts = append(parts, "works at "+e.Work)
		}
		if e.Home != "" && e.Home != e.Work {
			parts = append(parts, "lives at "+e.Home)
		}
		if len(parts) > 0 {
			b.WriteString(" — " + strings.Join(parts, "; "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Your bench book\n")
	if strings.TrimSpace(bench) == "" {
		b.WriteString("(Empty — you have written nothing in it yet.)\n")
	} else {
		b.WriteString(strings.TrimSpace(bench) + "\n")
	}
	b.WriteString("\nRead what you need, then give your ruling with the rule tool. It is final.\n")
	return b.String()
}

// renderEvents writes record rows as dated lines.
func renderEvents(events []sim.SimDayEvent, loc *time.Location) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		line := renderEvent(e)
		if line == "" {
			continue
		}
		out = append(out, e.At.In(loc).Format("Jan 2, 3:04 PM")+" — "+line)
	}
	return out
}

func renderEvent(e sim.SimDayEvent) string {
	p := e.Payload
	who := e.Speaker
	if who == "" {
		who = "Someone"
	}
	switch e.Kind {
	case sim.ActionTypeSpoke:
		return fmt.Sprintf("%s said: %q", who, str(p, "text"))
	case sim.ActionTypePaid:
		line := who + " paid " + orSomeone(str(p, "recipient"))
		amount := num(p, "amount")
		goods := goodsList(p["pay_items"])
		switch {
		case amount > 0 && goods != "":
			line += " " + coins(amount) + " and " + goods
		case amount > 0:
			line += " " + coins(amount)
		case goods != "":
			line += " in goods: " + goods
		default:
			line += " nothing"
		}
		if f := str(p, "for"); f != "" {
			line += fmt.Sprintf(" — %q", f)
		}
		return line
	case sim.ActionTypeDelivered:
		return fmt.Sprintf("%s handed over %s to %s", who, qtyItem(p), orSomeone(str(p, "recipient")))
	case sim.ActionTypeConsumed:
		return fmt.Sprintf("%s ate or used %s", who, qtyItem(p))
	case sim.ActionTypeGathered:
		line := fmt.Sprintf("%s gathered %s", who, qtyItem(p))
		if s := str(p, "source"); s != "" {
			line += " from " + s
		}
		return line
	case sim.ActionTypeWalked:
		if d := str(p, "destination"); d != "" {
			return who + " went to " + d
		}
		return who + " walked somewhere"
	case sim.ActionTypeHired:
		return fmt.Sprintf("%s hired %s for %s", who, orSomeone(str(p, "worker")), coins(num(p, "amount")))
	case sim.ActionTypeLabored:
		return fmt.Sprintf("%s finished work for %s and was paid %s", who, orSomeone(str(p, "employer")), coins(num(p, "amount")))
	case sim.ActionTypeSolicitedWork:
		return fmt.Sprintf("%s asked %s for work at %s", who, orSomeone(str(p, "employer")), coins(num(p, "amount")))
	case sim.ActionTypeTookBreak:
		return withReason(who+" took a break", str(p, "reason"))
	case sim.ActionTypeStayedOpen:
		return withReason(who+" kept the shop open late", str(p, "reason"))
	case sim.ActionTypeRepairing:
		return who + " began mending " + orSomething(str(p, "business"))
	case sim.ActionTypeCollected:
		if f := str(p, "for"); f != "" {
			return fmt.Sprintf("%s collected %s — %s", who, coins(num(p, "amount")), f)
		}
		return fmt.Sprintf("%s collected %s", who, coins(num(p, "amount")))
	case sim.ActionTypeOffered:
		return fmt.Sprintf("%s offered %s %s for %s", who, orSomeone(str(p, "seller")), coins(num(p, "amount")), qtyItem(p))
	case sim.ActionTypeDeclined:
		return fmt.Sprintf("%s turned down %s's offer for %s", who, orSomeone(str(p, "buyer")), qtyItem(p))
	case sim.ActionTypeCountered:
		return fmt.Sprintf("%s asked %s %s for %s", who, orSomeone(str(p, "buyer")), coins(num(p, "amount")), qtyItem(p))
	case sim.ActionTypeBroughtCase:
		return fmt.Sprintf("%s brought a matter before the magistrates: %q", who, str(p, "complaint"))
	case sim.ActionTypeRuled:
		return fmt.Sprintf("%s heard the magistrates' ruling: %q", who, str(p, "words"))
	}
	if t := str(p, "text"); t != "" {
		return who + ": " + t
	}
	return ""
}

func withReason(line, reason string) string {
	if reason == "" {
		return line
	}
	return fmt.Sprintf("%s — %q", line, reason)
}

func str(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// num reads a JSON number (float64 after decode) or a numeric string.
func num(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func coins(n int) string {
	if n == 1 {
		return "1 coin"
	}
	return fmt.Sprintf("%d coins", n)
}

func qtyItem(p map[string]any) string {
	item := str(p, "item")
	if item == "" {
		item = "goods"
	}
	if q := num(p, "qty"); q > 0 {
		return fmt.Sprintf("%d %s", q, item)
	}
	return item
}

func goodsList(v any) string {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, el := range list {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		parts = append(parts, qtyItem(m))
	}
	return strings.Join(parts, ", ")
}

func orSomeone(s string) string {
	if s == "" {
		return "someone"
	}
	return s
}

func orSomething(s string) string {
	if s == "" {
		return "something"
	}
	return s
}
