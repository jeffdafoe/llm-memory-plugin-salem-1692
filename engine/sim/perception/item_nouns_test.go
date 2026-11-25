package perception

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// countedNounSections are the offer and order-book sections whose "<n> <goods>"
// counts LLM-1029 moved from the raw kind key to the catalog count noun.
var countedNounSections = []string{
	"## Offers awaiting your decision",
	"## Orders to deliver",
	"## Upcoming bookings",
	"## Orders you're waiting on",
	"## Overdue — paid but not delivered",
	"## Offers you have standing",
	"## Recently settled offers",
}

// sectionBody is the text of the section that starts at header, up to the next
// "## " header. Empty when the section is not rendered.
func sectionBody(text, header string) string {
	start := strings.Index(text, header+"\n")
	if start < 0 {
		return ""
	}
	body := text[start+len(header)+1:]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}
	return body
}

// TestGoldensOfferAndOrderLinesUseCountNouns is the LLM-1029 cross-scenario
// invariant: in the offer and order-book sections, no "<n> <kind key>" appears
// where the catalog has a different phrase for that count. "5 nail" and
// "1 fried_meat" fail; "1 nail" passes, because the catalog's singular for nail
// is "nail".
func TestGoldensOfferAndOrderLinesUseCountNouns(t *testing.T) {
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		text := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
		for kind, def := range snap.ItemKinds {
			if def == nil {
				continue
			}
			// \b on both sides, so "5 nails" never reads as "5 nail".
			re := regexp.MustCompile(`\b(\d+) ` + regexp.QuoteMeta(string(kind)) + `\b`)
			for _, header := range countedNounSections {
				for _, m := range re.FindAllStringSubmatch(sectionBody(text, header), -1) {
					qty, err := strconv.Atoi(m[1])
					if err != nil {
						continue
					}
					want := def.CountNoun(qty)
					if want == "" || want == string(kind) {
						continue
					}
					t.Errorf("%s: %q in %q names the kind key; the catalog says %q", sc.name, m[0], header, m[1]+" "+want)
				}
			}
		}
	}
}

func TestItemNounsCount(t *testing.T) {
	nouns := buildItemNouns(&sim.Snapshot{ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
		"flour":      {Name: "flour", DisplayLabelSingular: "sack of flour", DisplayLabelPlural: "sacks of flour"},
		"sage":       {Name: "sage", DisplayLabelSingular: "sage", DisplayLabelPlural: "sage"},
		"fried_meat": {Name: "fried_meat", DisplayLabel: "fried meat"},
		"unlabeled":  {Name: "unlabeled"},
	}})
	cases := []struct {
		kind       sim.ItemKind
		qty        int
		want       string
		pluralVerb bool
	}{
		{"flour", 1, "sack of flour", false},
		{"flour", 7, "sacks of flour", true},
		{"flour", 0, "sacks of flour", true},
		// Plural equals singular: a mass noun, "the sage is worth".
		{"sage", 5, "sage", false},
		// Only a DisplayLabel: Singular and Plural both fall back to it.
		{"fried_meat", 1, "fried meat", false},
		{"fried_meat", 2, "fried meat", false},
		// No phrase at all, and a kind not in the catalog: the raw key.
		{"unlabeled", 3, "unlabeled", false},
		{"ghost", 3, "ghost", false},
	}
	for _, c := range cases {
		if got := nouns.count(c.kind, c.qty); got != c.want {
			t.Errorf("count(%s, %d) = %q, want %q", c.kind, c.qty, got, c.want)
		}
		if got := nouns.isPluralVerb(c.kind, c.qty); got != c.pluralVerb {
			t.Errorf("isPluralVerb(%s, %d) = %v, want %v", c.kind, c.qty, got, c.pluralVerb)
		}
	}
	// A nil table (a hand-built payload) names every kind by its key.
	var none itemNouns
	if got := none.counted("shovel", 3); got != "3 shovel" {
		t.Errorf("nil table counted = %q, want %q", got, "3 shovel")
	}
}
