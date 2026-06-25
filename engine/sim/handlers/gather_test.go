package handlers

import (
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

func gatherGatingRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	if err := RegisterGather(r); err != nil {
		t.Fatalf("RegisterGather: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	return r
}

// TestGateTools_Gather_AdvertisedOnlyAtSource — gather is advertised only when
// the perception payload carries a gatherable-here cue (Surroundings.
// GatherableItem). The same field drives the "gatherable" perception line, so
// cue and tool can't drift.
func TestGateTools_Gather_AdvertisedOnlyAtSource(t *testing.T) {
	r := gatherGatingRegistry(t)

	atSource := perception.Payload{
		ActorID:      "npc",
		Surroundings: perception.SurroundingsView{GatherableItem: "water", GatherableSource: "Old Well"},
	}
	if names := specNameSet(gateTools(r, atSource, nil)); names["gather"] != 1 {
		t.Errorf("gather should be advertised at a gatherable source; count %d", names["gather"])
	}

	notAtSource := perception.Payload{ActorID: "npc", Surroundings: speakAudience()}
	if names := specNameSet(gateTools(r, notAtSource, nil)); names["gather"] != 0 {
		t.Errorf("gather should NOT be advertised away from a source; count %d", names["gather"])
	}
	// speak/done stay advertised regardless.
	names := specNameSet(gateTools(r, notAtSource, nil))
	for _, always := range []string{"speak", "done"} {
		if names[always] != 1 {
			t.Errorf("%q should always be advertised; count %d", always, names[always])
		}
	}
}

func TestDecodeGatherArgs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		wantNil bool // expect Qty == nil (omitted)
		wantQty int  // expected *Qty when not nil
	}{
		{name: "explicit qty", raw: `{"qty": 3}`, wantQty: 3},
		{name: "omitted qty is nil", raw: `{}`, wantNil: true},
		{name: "empty raw args is a bare call (nil qty)", raw: ``, wantNil: true},
		{name: "explicit zero rejected", raw: `{"qty": 0}`, wantErr: true},
		{name: "negative qty rejected", raw: `{"qty": -1}`, wantErr: true},
		{name: "unknown field", raw: `{"qty": 1, "item": "water"}`, wantErr: true},
		{name: "trailing data", raw: `{"qty": 1}{}`, wantErr: true},
		{name: "not an object", raw: `5`, wantErr: true},
		{name: "over max", raw: `{"qty": 2147483648}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeGatherArgs(json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (parsed %+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			args, ok := got.(GatherArgs)
			if !ok {
				t.Fatalf("got %T, want GatherArgs", got)
			}
			if tc.wantNil {
				if args.Qty != nil {
					t.Errorf("Qty=%v, want nil (omitted)", *args.Qty)
				}
				return
			}
			if args.Qty == nil {
				t.Fatalf("Qty=nil, want %d", tc.wantQty)
			}
			if *args.Qty != tc.wantQty {
				t.Errorf("Qty=%d, want %d", *args.Qty, tc.wantQty)
			}
		})
	}
}

// TestHandleGather_BuildsGatherCommand — the handler is a pure builder; it
// returns a non-zero sim.Command without touching the world.
func TestHandleGather_BuildsGatherCommand(t *testing.T) {
	q := 2
	cmd, err := HandleGather(HandlerInput{ActorID: "hannah", Args: GatherArgs{Qty: &q}})
	if err != nil {
		t.Fatalf("HandleGather: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleGather returned a Command with nil Fn")
	}
	// Wrong args type → typed error, no panic.
	if _, err := HandleGather(HandlerInput{ActorID: "hannah", Args: sim.ActorID("x")}); err == nil {
		t.Error("want error for wrong args type, got nil")
	}
}
