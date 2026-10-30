package sim

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func clientTriggerWorld() (*World, *pcEventRecorder) {
	w := sleepTestWorld(
		&Actor{ID: "pc", Kind: KindPC, LoginUsername: "alice"},
		&Actor{ID: "pc-nologin", Kind: KindPC},
		&Actor{ID: "npc", Kind: KindNPCShared, LLMAgent: "npc-va"},
	)
	rec := &pcEventRecorder{}
	w.Subscribe(SubscriberFunc(rec.handle))
	return w, rec
}

func TestSendClientTrigger_EmitsForPC(t *testing.T) {
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	w, rec := clientTriggerWorld()

	res, err := SendClientTrigger("pc", "treeline_figure", map[string]any{"note": "x"}, now).Fn(w)
	if err != nil {
		t.Fatalf("SendClientTrigger: %v", err)
	}
	out := res.(SendClientTriggerResult)
	if out.ActorID != "pc" || out.Login != "alice" || out.Kind != "treeline_figure" {
		t.Errorf("result = %+v", out)
	}
	if len(rec.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(rec.events))
	}
	ev, ok := rec.events[0].(*ClientTrigger)
	if !ok {
		t.Fatalf("event is %T, want *ClientTrigger", rec.events[0])
	}
	if ev.ActorID != "pc" || ev.Login != "alice" || ev.Kind != "treeline_figure" || !ev.At.Equal(now) {
		t.Errorf("event = %+v", ev)
	}
	if string(ev.Detail) != `{"note":"x"}` {
		t.Errorf("detail = %s, want {\"note\":\"x\"}", ev.Detail)
	}
}

func TestSendClientTrigger_NilDetailIsEmptyObject(t *testing.T) {
	w, rec := clientTriggerWorld()
	if _, err := SendClientTrigger("pc", "treeline_figure", nil, time.Now()).Fn(w); err != nil {
		t.Fatalf("SendClientTrigger: %v", err)
	}
	if got := string(rec.events[0].(*ClientTrigger).Detail); got != "{}" {
		t.Errorf("detail = %s, want {}", got)
	}
}

func TestSendClientTrigger_Refusals(t *testing.T) {
	big := map[string]any{"s": strings.Repeat("x", ClientTriggerMaxDetail)}
	cases := []struct {
		name   string
		actor  ActorID
		kind   string
		detail map[string]any
		want   error
	}{
		{"unknown actor", "ghost", "treeline_figure", nil, ErrActorNotFound},
		{"unknown kind", "pc", "fireworks", nil, ErrUnknownClientTriggerKind},
		{"oversize detail", "pc", "treeline_figure", big, ErrClientTriggerDetail},
		{"unencodable detail", "pc", "treeline_figure", map[string]any{"c": make(chan int)}, ErrClientTriggerDetail},
		{"PC with no login", "pc-nologin", "treeline_figure", nil, ErrClientTriggerNoPlayer},
		{"NPC", "npc", "treeline_figure", nil, ErrClientTriggerNoPlayer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, rec := clientTriggerWorld()
			_, err := SendClientTrigger(tc.actor, tc.kind, tc.detail, time.Now()).Fn(w)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if len(rec.events) != 0 {
				t.Errorf("refused trigger emitted %d events", len(rec.events))
			}
		})
	}
}

// The detail cap is on the encoded object: exactly at the cap passes.
func TestEncodeClientTriggerDetail_CapBoundary(t *testing.T) {
	// {"s":"<n x>"} is n+8 bytes.
	atCap := map[string]any{"s": strings.Repeat("x", ClientTriggerMaxDetail-8)}
	if _, err := EncodeClientTriggerDetail(atCap); err != nil {
		t.Errorf("detail at the cap refused: %v", err)
	}
	over := map[string]any{"s": strings.Repeat("x", ClientTriggerMaxDetail-7)}
	if _, err := EncodeClientTriggerDetail(over); !errors.Is(err, ErrClientTriggerDetail) {
		t.Errorf("detail one byte over the cap: err = %v, want ErrClientTriggerDetail", err)
	}
}
