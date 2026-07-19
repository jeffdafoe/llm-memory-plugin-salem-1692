package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// craft_say_test.go — LLM-468. `produce` carries an optional `say` so a producer
// can voice its beat on the acting call instead of buying a whole extra LLM
// round for `speak` (120 measured produce→speak continuations a day, each
// re-shipping the full ephemeral body).
//
// The terminality half — a produce that SPOKE ends the tick, a silent one does
// not — is proven in craft_terminal_say_test.go against the harness dispatch.

func TestDecodeCraftArgs_AcceptsOptionalSay(t *testing.T) {
	got, err := DecodeCraftArgs(json.RawMessage(`{"item":"porridge","say":"I'll get another pot on"}`))
	if err != nil {
		t.Fatalf("decode with say: %v", err)
	}
	args, ok := got.(CraftArgs)
	if !ok {
		t.Fatalf("decode returned %T, want CraftArgs", got)
	}
	if args.Item != "porridge" {
		t.Errorf("Item = %q, want %q", args.Item, "porridge")
	}
	if args.Say != "I'll get another pot on" {
		t.Errorf("Say = %q, want the announcement", args.Say)
	}
}

func TestDecodeCraftArgs_SayIsOptional(t *testing.T) {
	// Working quietly stays the default and must not become an error — the vast
	// majority of production is unremarked.
	got, err := DecodeCraftArgs(json.RawMessage(`{"item":"nails"}`))
	if err != nil {
		t.Fatalf("decode without say: %v", err)
	}
	if args := got.(CraftArgs); args.Say != "" {
		t.Errorf("Say = %q, want empty", args.Say)
	}
}

func TestDecodeCraftArgs_RejectsOversizedSay(t *testing.T) {
	long := strings.Repeat("a", MaxCraftSayChars+1)
	if _, err := DecodeCraftArgs(json.RawMessage(`{"item":"nails","say":"` + long + `"}`)); err == nil {
		t.Fatalf("expected a rejection for a say over the %d-character cap", MaxCraftSayChars)
	}
}

func TestDecodeCraftArgs_SayCapCountsRunesNotBytes(t *testing.T) {
	// The cap is a legibility bound on an utterance, so it counts characters —
	// a multi-byte say at exactly the cap must pass, matching bake's decoder.
	atCap := strings.Repeat("é", MaxCraftSayChars)
	if _, err := DecodeCraftArgs(json.RawMessage(`{"item":"nails","say":"` + atCap + `"}`)); err != nil {
		t.Fatalf("a multi-byte say of exactly %d characters must pass: %v", MaxCraftSayChars, err)
	}
}

func TestDecodeCraftArgs_StillRequiresItem(t *testing.T) {
	// say does not become a substitute for naming the good.
	if _, err := DecodeCraftArgs(json.RawMessage(`{"say":"something's cooking"}`)); err == nil {
		t.Fatalf("expected a rejection when item is absent")
	}
}

func TestHandleCraft_RejectsControlCharacterInSay(t *testing.T) {
	args := CraftArgs{Item: "nails", Say: "hot iron\x07now"}
	if _, err := HandleCraft(HandlerInput{ActorID: "ezekiel", Args: args}); err == nil {
		t.Fatalf("expected a rejection for a say carrying a control character")
	}
}
