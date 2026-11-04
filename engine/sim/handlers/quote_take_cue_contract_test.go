package handlers_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// quote_take_cue_contract_test.go — LLM-905: the quote cue and the
// pay_with_item contract must agree. Each case posts a real quote, renders the
// buyer's cue for it, builds the pay_with_item call from exactly the arguments
// the cue names (nothing more), and runs it through decode → handler → world.
// The take must settle. The live failure this pins: the bundle cue named only
// quote_id and amount while the handler required seller, item and qty, so a
// buyer who followed the cue word for word could never take the quote.

var (
	cueCallRe   = regexp.MustCompile(`call pay_with_item with ([^—]+?) — it settles at once`)
	cueQuoteRe  = regexp.MustCompile(`quote_id (\d+)`)
	cueItemRe   = regexp.MustCompile(`item "([^"]+)"`)
	cueQtyRe    = regexp.MustCompile(`qty (\d+)`)
	cueAmountRe = regexp.MustCompile(`amount (\d+)`)
)

// cueTakeArgs extracts the take call the cue spells out and returns it as the
// raw tool arguments a model following it would send.
func cueTakeArgs(t *testing.T, cue string) json.RawMessage {
	t.Helper()
	m := cueCallRe.FindStringSubmatch(cue)
	if m == nil {
		t.Fatalf("no quote-take instruction in the cue:\n%s", cue)
	}
	clause := m[1]
	args := map[string]any{"say": "Here you are."}
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("cue number %q: %v", s, err)
		}
		return n
	}
	if q := cueQuoteRe.FindStringSubmatch(clause); q != nil {
		args["quote_id"] = atoi(q[1])
	}
	if it := cueItemRe.FindStringSubmatch(clause); it != nil {
		args["item"] = it[1]
	}
	if q := cueQtyRe.FindStringSubmatch(clause); q != nil {
		args["qty"] = atoi(q[1])
	}
	if a := cueAmountRe.FindStringSubmatch(clause); a != nil {
		args["amount"] = atoi(a[1])
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal cue args: %v", err)
	}
	return raw
}

func TestQuoteTakeCue_CallItNamesSettles(t *testing.T) {
	cases := []struct {
		name  string
		lines []handlers.SceneQuoteLineArg
	}{
		{"single_good", []handlers.SceneQuoteLineArg{{ItemKind: "bread", Qty: 1}}},
		{"bundle", []handlers.SceneQuoteLineArg{{ItemKind: "bread", Qty: 1}, {ItemKind: "water", Qty: 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildIntegrationWorld(t)
			defer stop()
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.Actors["bob"].Inventory["water"] = 5
				return nil, nil
			}}); err != nil {
				t.Fatalf("stock bob: %v", err)
			}

			quoteCmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
				ActorID: "bob", AttemptID: "tk-1",
				Args: handlers.SceneQuoteArgs{Lines: tc.lines, Amount: 6, TargetBuyer: "Alice"},
			})
			if err != nil {
				t.Fatalf("HandleSceneQuote: %v", err)
			}
			qRes, err := w.Send(quoteCmd)
			if err != nil {
				t.Fatalf("SceneQuote send: %v", err)
			}
			quoteID := qRes.(sim.SceneQuoteCreateResult).QuoteID

			lines := make([]sim.QuoteLine, 0, len(tc.lines))
			for _, ln := range tc.lines {
				lines = append(lines, sim.QuoteLine{ItemKind: sim.ItemKind(ln.ItemKind), Qty: ln.Qty})
			}
			rendered := perception.Render(perception.Payload{
				ActorID: "alice",
				Warrants: []sim.WarrantMeta{{
					TriggerActorID: "bob",
					Reason: sim.SceneQuoteTargetedWarrantReason{
						QuoteID: quoteID, SellerID: "bob", Lines: lines, Amount: 6,
					},
					SourceEventID: 1,
				}},
				WarrantActorNames: map[sim.ActorID]string{"bob": "Bob"},
				Baseline:          perception.BaselinePresent,
			}, perception.DefaultRenderConfig())
			cue := strings.Join([]string{rendered.StableText, rendered.Text, rendered.EphemeralText}, "\n\n")

			raw := cueTakeArgs(t, cue)
			decoded, err := handlers.DecodePayWithItemArgs(raw)
			if err != nil {
				t.Fatalf("decode of the cue's call %s: %v", raw, err)
			}
			payCmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
				ActorID: "alice", AttemptID: "tk-2", Args: decoded,
			})
			if err != nil {
				t.Fatalf("handler on the cue's call %s: %v", raw, err)
			}
			res, err := w.Send(payCmd)
			if err != nil {
				t.Fatalf("the cue's call %s did not settle: %v", raw, err)
			}
			out := res.(sim.PayWithItemResult)
			if !out.FastPath || out.State != sim.PayLedgerStateAccepted {
				t.Fatalf("result = %+v, want fast-path accepted", out)
			}
			if !out.Announced {
				t.Errorf("the buyer's say did not go out (SayRefused=%q)", out.SayRefused)
			}
			if got := w.Published().Actors["alice"].Coins; got != 44 {
				t.Errorf("alice.Coins = %d, want 44", got)
			}
		})
	}
}
