package sim

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// command_panic_health.go — panic containment for the world command loop
// (LLM-997).
//
// Why this exists: before LLM-997 a panic in any command Fn killed the whole
// engine. The LLM-995 divide by zero did that 36 times in a day: one NPC's goods
// offer panicked about 90 s after each boot, systemd restarted the engine, and
// every restart rolled the world back to its last checkpoint. The crashes came
// too late after boot for the unit's start limit to trip, so nothing anywhere
// said the village was looping.
//
// Now World.Run recovers a panic in a command Fn. The command fails with a
// *CommandPanicError, the world goroutine keeps serving, and the panic is
// recorded here so the umbilical's command_panic alarm (httpapi/alarms.go)
// shouts about it on every umbilical response and on the operator's ticker.
//
// THE PRICE (Jeff, 2026-11-23): a command that panics partway through may leave
// a change half done (coins moved and goods not). Nothing rolls it back. A
// restart would roll back to the last checkpoint instead, but it also throws
// away every good change since then, and a panic that comes back on every boot
// turns into a crash loop. The alarm is what makes the trade acceptable: the
// operator learns within a minute which code path panicked and can check the
// actors it touched.
//
// Two phases are covered: the command Fn, and the post-command delta emitters
// (their subscribers run game logic per changed actor, the same kind of code as
// a command). reconcileQuoteCoverage and republish are NOT covered: they walk
// the whole world after every command, so a panic there would come back on the
// next command, and a recovered republish would leave clients on a stale
// snapshot with the engine looking alive. Those still end the process, and the
// systemd start limit catches the loop. Fatal runtime errors (concurrent map
// write, out of memory, stack overflow) cannot be recovered at all.
//
// Live-only and in-memory, like the other alarm recorders: the alarm reflects
// what panicked recently and clears itself CommandPanicAlarmWindow after the last
// panic. No durable row and no restart survival.

// CommandPanicAlarmWindow is how long the command_panic alarm keeps firing after
// the most recent caught panic. It also bounds a "run": a panic that arrives more
// than this long after the previous one starts a new run, with a fresh first-seen
// time and count.
//
// An hour, because a caught panic is not a condition that is still visibly
// broken the way a failed checkpoint is. The engine is serving again the moment
// the command fails, so the alarm has to stay up long enough for an operator who
// touches the umbilical, or glances at the ticker, to see it. The alarm prose
// says "an hour"; change it with this.
const CommandPanicAlarmWindow = time.Hour

// commandPanicFrameLimit caps how many stack frames a recorded panic keeps. The
// alarm prose needs the panicking function and the command that called it; the
// full stack goes to the engine log.
const commandPanicFrameLimit = 4

// CommandPanicError is the error a command returns when its Fn panicked and
// World.Run recovered it.
type CommandPanicError struct {
	// Value is the panic value, formatted with %v.
	Value string
	// Frames are the innermost non-runtime frames at the panic, innermost first,
	// as "pkg.Func (file.go:line)".
	Frames []string
}

func (e *CommandPanicError) Error() string {
	if len(e.Frames) == 0 {
		return "command panicked: " + e.Value
	}
	return "command panicked: " + e.Value + " at " + e.Frames[0]
}

// CommandPanicHealth records the panics World.Run has recovered. Safe for
// concurrent use: the world goroutine writes it and umbilical handlers read it.
// Every method is nil-safe.
type CommandPanicHealth struct {
	mu         sync.Mutex
	count      int
	firstAt    time.Time
	lastAt     time.Time
	lastValue  string
	lastFrames []string
}

// CommandPanicHealthSnapshot is an immutable copy of a CommandPanicHealth.
// Count, FirstAt and LastAt describe the current run (see
// CommandPanicAlarmWindow). A zero Count means no panic has been caught since
// the engine started.
type CommandPanicHealthSnapshot struct {
	Count      int       `json:"count"`
	FirstAt    time.Time `json:"first_at"`
	LastAt     time.Time `json:"last_at"`
	LastPanic  string    `json:"last_panic,omitempty"`
	LastFrames []string  `json:"last_frames,omitempty"`
}

// Record notes one recovered panic at now. A panic more than
// CommandPanicAlarmWindow after the previous one starts a new run.
func (h *CommandPanicHealth) Record(now time.Time, perr *CommandPanicError) {
	if h == nil || perr == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 || now.Sub(h.lastAt) > CommandPanicAlarmWindow {
		h.count = 0
		h.firstAt = now
	}
	h.count++
	h.lastAt = now
	h.lastValue = perr.Value
	h.lastFrames = append([]string(nil), perr.Frames...)
}

// Snapshot returns an immutable copy. Nil-safe.
func (h *CommandPanicHealth) Snapshot() CommandPanicHealthSnapshot {
	if h == nil {
		return CommandPanicHealthSnapshot{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return CommandPanicHealthSnapshot{
		Count:      h.count,
		FirstAt:    h.firstAt,
		LastAt:     h.lastAt,
		LastPanic:  h.lastValue,
		LastFrames: append([]string(nil), h.lastFrames...),
	}
}

// CommandPanicHealthSnapshot returns the world's recovered-panic record for the
// umbilical alarm evaluator. Safe from any goroutine.
func (w *World) CommandPanicHealthSnapshot() CommandPanicHealthSnapshot {
	return w.cmdPanicHealth.Snapshot()
}

// containPanic is deferred directly (so its recover() stops the panic) by the
// two world-loop phases that are contained: a command's Fn and the
// post-command delta emitters. It logs the full stack, records the panic for
// the command_panic alarm, and hands the error to onPanic when the phase has a
// result to fail. A no-op when nothing panicked.
func (w *World) containPanic(phase string, onPanic func(*CommandPanicError)) {
	r := recover()
	if r == nil {
		return
	}
	perr := &CommandPanicError{
		Value:  fmt.Sprint(r),
		Frames: panicFrames(),
	}
	log.Printf("sim: RECOVERED PANIC in %s: %s\n%s", phase, perr.Value, debug.Stack())
	w.cmdPanicHealth.Record(time.Now().UTC(), perr)
	if onPanic != nil {
		onPanic(perr)
	}
}

// emitPostCommandDeltas runs the per-command delta emitters for World.Run.
// They dispatch NPCNeedsChanged / NPCDormancyChanged / NPCCoinsChanged to the
// subscribers, which run game logic for the actors that changed, so a panic
// here is the same kind of bug as a panic in a command and is contained the
// same way. A panic skips the remaining emitters for this command: those
// deltas are lost, since the next command diffs against the snapshot this one
// published. The command's own result stands.
func (w *World) emitPostCommandDeltas(prev *Snapshot) {
	defer w.containPanic("post-command delta emit", nil)
	w.emitNeedsDeltas(prev)
	w.emitDormancyDeltas(prev)
	w.emitCoinsDeltas(prev)
}

// runCommandFn runs one command's Fn for World.Run and turns a panic in it into
// a *CommandPanicError. A rooted command runs under its inherited cascade root;
// withRoot restores the ambient root on the way out, panic or not.
func (w *World) runCommandFn(cmd Command) (value any, err error) {
	defer w.containPanic("world command", func(perr *CommandPanicError) {
		value = nil
		err = perr
	})
	if cmd.inheritedRoot != 0 {
		// Cross-boundary command (PR 3's worker tool-call): run the whole
		// handler under the inherited cascade root so events it emits continue
		// that root and it cannot bleed into the next command. See
		// newRootedCommand.
		w.withRoot(cmd.inheritedRoot, func() {
			value, err = cmd.Fn(w)
		})
		return value, err
	}
	return cmd.Fn(w)
}

// panicFrames returns the innermost frames of the panicking stack, for a call
// made from containPanic. While a deferred function runs during a panic, the
// panicking frames are still on the stack below it, so the walk sees
// containPanic, the runtime's panic machinery, and then the code that panicked.
// It keeps the first commandPanicFrameLimit frames that are not dispatch
// plumbing, and stops at the contained phase's own function, since everything
// past it is World.Run.
func panicFrames() []string {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		f, more := frames.Next()
		name := f.Function
		if strings.HasSuffix(name, ".(*World).runCommandFn") || strings.HasSuffix(name, ".(*World).emitPostCommandDeltas") {
			break
		}
		skip := strings.HasPrefix(name, "runtime.") ||
			strings.HasSuffix(name, ".(*World).containPanic") ||
			strings.Contains(name, ".(*World).runCommandFn.") ||
			strings.HasSuffix(name, ".(*World).withRoot")
		if !skip && name != "" {
			out = append(out, shortFuncName(name)+" ("+filepath.Base(f.File)+":"+fmt.Sprint(f.Line)+")")
			if len(out) == commandPanicFrameLimit {
				break
			}
		}
		if !more {
			break
		}
	}
	return out
}

// shortFuncName drops the module path from a runtime function name:
// "github.com/x/engine/sim.payOfferShortfall" becomes "sim.payOfferShortfall".
func shortFuncName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
