package sim

import "time"

// client_event.go — LLM-880. Client-only events the Godot client reports for
// statistics: effects that run only in the client (the figure at the treeline,
// LLM-844), which the engine never sees otherwise. The httpapi write route
// stamps At / Username / ActorID itself and stores the row through the pg
// ClientEventsRepo; the umbilical reads them back. Untrusted, client-claimed
// data — never world state and never an input to a decision.

// ClientEvent is one reported client-only event.
type ClientEvent struct {
	At       time.Time
	Username string
	// ActorID is the caller's PC; "" when the user has no PC.
	ActorID ActorID
	Kind    string
	// Detail is a JSON object, already validated by the route.
	Detail []byte
}

// ClientEventFilter narrows a client-event read. A zero value on a field means
// no filter on it. Since and Until bound At to [Since, Until).
type ClientEventFilter struct {
	Kind  string
	Since time.Time
	Until time.Time
}

// ClientEventDayCount is how many events of one kind landed on one UTC day.
type ClientEventDayCount struct {
	Day   time.Time
	Kind  string
	Count int
}
