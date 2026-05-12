package sim

// Command is one operation against the World. The world goroutine processes
// commands one at a time in FIFO order — no other access to World state is
// permitted. The Fn closure runs synchronously inside the world goroutine
// with exclusive access; it must NOT block on I/O (LLM HTTP calls happen
// outside the world goroutine — see World docs).
//
// If Reply is non-nil, the world goroutine sends a CommandResult on it
// after Fn returns. Fire-and-forget commands leave Reply nil.
type Command struct {
	Fn    func(w *World) (any, error)
	Reply chan CommandResult
}

// CommandResult is what a command's Fn returns, packaged for the reply
// channel. Value is whatever the Fn produces (typically a snapshot or a
// small outcome struct). Err is non-nil only on command-level rejection
// such as state validation — channel-send failures don't bubble through
// here.
type CommandResult struct {
	Value any
	Err   error
}
