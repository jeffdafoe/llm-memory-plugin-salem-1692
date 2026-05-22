package handlers

// register_move_to.go — production registration helper for the move_to tool
// (ZBBS-HOME-285). Same opt-in-piecewise pattern as register_take_break.go —
// the entrypoint composes the tool surface it wants (see cmd/engine/main.go
// registerTools).

// RegisterMoveTo adds the move_to tool to r as a ClassCommit entry,
// AvailabilityAvailable. The commit handler is HandleMoveTo; the decoder is
// DecodeMoveToArgs; both live in move_to.go.
//
// terminalOnSuccess is TRUE: move_to ends the tick. A walk is "decide to go,
// then go" — the NPC has nothing left to do this turn once the walk is in
// flight, and any departure line is said with a non-terminal speak BEFORE the
// move_to (the speak-then-move ordering v1 enforced via ZBBS-HOME-237, so a
// post-move speak doesn't broadcast at the room the actor just left). This is
// the one place move_to diverges from take_break, which is non-terminal.
//
// Returns an error on registration failure (duplicate name, malformed schema
// bytes) — a startup wiring bug the caller should fail loudly on.
func RegisterMoveTo(r *Registry) error {
	return r.RegisterCommit(
		"move_to",
		moveToSchema,
		DecodeMoveToArgs,
		HandleMoveTo,
		true, // terminal: walking ends the tick (say your piece first, then move)
		WithDescription(moveToDescription),
	)
}
