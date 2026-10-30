package sim

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// client_trigger.go — LLM-881. The reverse channel: the engine tells ONE
// player's client to run a client-only effect (the figure at the treeline,
// LLM-844). LLM-880's client event beacon is the other direction.
//
// A ClientTrigger is the only way to reach a client with a client-only event.
// Engine features emit it with emitClientTrigger; the umbilical's
// client-trigger route issues SendClientTrigger, which calls the same helper.
// The hub delivers the frame only to the sockets of the PC's login.
//
// Not world state: the event is never persisted, replayed or written to the
// action log, and a player with no live socket misses it.

// ClientTriggerMaxDetail caps the encoded detail object, in bytes.
const ClientTriggerMaxDetail = 1 << 10

// clientTriggerKinds is the allowlist of trigger kinds. Adding a kind is a code
// change here and a handler in the client (event_client.gd); a client that does
// not know a kind ignores it.
var clientTriggerKinds = map[string]struct{}{
	// LLM-844: show the figure at the treeline now (forced: no cooldown, no
	// roll, no time-since-dusk check).
	"treeline_figure": {},
}

var (
	// ErrUnknownClientTriggerKind refuses a kind not on the allowlist.
	ErrUnknownClientTriggerKind = errors.New("unknown client trigger kind")
	// ErrClientTriggerDetail refuses a detail that does not encode to a JSON
	// object of at most ClientTriggerMaxDetail bytes.
	ErrClientTriggerDetail = fmt.Errorf("client trigger detail must be a JSON object of at most %d bytes", ClientTriggerMaxDetail)
	// ErrClientTriggerNoPlayer refuses a target that is not a PC with a login:
	// there is no client to send to.
	ErrClientTriggerNoPlayer = errors.New("client trigger target is not a player with a login")
)

// IsClientTriggerKind reports whether kind is on the allowlist.
func IsClientTriggerKind(kind string) bool {
	_, ok := clientTriggerKinds[kind]
	return ok
}

// EncodeClientTriggerDetail returns detail as a JSON object, "{}" when nil, or
// ErrClientTriggerDetail when it does not encode or is over the cap.
func EncodeClientTriggerDetail(detail map[string]any) (json.RawMessage, error) {
	if detail == nil {
		return json.RawMessage("{}"), nil
	}
	out, err := json.Marshal(detail)
	if err != nil || len(out) > ClientTriggerMaxDetail {
		return nil, ErrClientTriggerDetail
	}
	return out, nil
}

// ClientTrigger is emitted to run a client-only effect on one player's client.
// Login is the PC's LoginUsername at emit time, so the translator can target
// the frame without reading world state. Detail is an encoded JSON object.
type ClientTrigger struct {
	EventBase
	ActorID ActorID
	Login   string
	Kind    string
	Detail  json.RawMessage
	At      time.Time
}

func (ClientTrigger) isSimEvent() {}

// emitClientTrigger validates and emits a ClientTrigger for actor. Refuses an
// unknown kind, a bad detail, and an actor that is not a PC with a login. Must
// run on the world goroutine.
func (w *World) emitClientTrigger(actor *Actor, kind string, detail map[string]any, now time.Time) error {
	if !IsClientTriggerKind(kind) {
		return fmt.Errorf("%w: %q", ErrUnknownClientTriggerKind, kind)
	}
	encoded, err := EncodeClientTriggerDetail(detail)
	if err != nil {
		return err
	}
	if actor == nil || actor.Kind != KindPC || actor.LoginUsername == "" {
		return ErrClientTriggerNoPlayer
	}
	w.emit(&ClientTrigger{
		ActorID: actor.ID,
		Login:   actor.LoginUsername,
		Kind:    kind,
		Detail:  encoded,
		At:      now,
	})
	return nil
}

// SendClientTriggerResult is the typed reply from SendClientTrigger.
type SendClientTriggerResult struct {
	ActorID ActorID
	Login   string
	Kind    string
}

// SendClientTrigger returns a Command that emits a ClientTrigger for the actor.
// ErrActorNotFound for an unknown actor; otherwise the emitClientTrigger
// refusals.
func SendClientTrigger(actorID ActorID, kind string, detail map[string]any, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, ErrActorNotFound
			}
			if err := w.emitClientTrigger(actor, kind, detail, now); err != nil {
				return nil, err
			}
			return SendClientTriggerResult{ActorID: actor.ID, Login: actor.LoginUsername, Kind: kind}, nil
		},
	}
}
