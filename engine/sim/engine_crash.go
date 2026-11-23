package sim

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"
)

// engine_crash.go — the crash an earlier engine process left behind (LLM-999).
//
// Why this exists: LLM-997 contains a panic in a world command or a delta emit,
// but some failures still end the process — a panic in republish or
// reconcileQuoteCoverage, a panic in any other goroutine (a ticker), and fatal
// runtime errors (concurrent map write, out of memory, stack overflow). systemd
// restarts the engine and the crash text goes only to journald, which an
// operator on the umbilical cannot read. Nothing on the umbilical said the
// engine had died.
//
// Now the engine asks the Go runtime (runtime/debug.SetCrashOutput) to also
// write any fatal crash to a file in its systemd state directory. The next boot
// reads that file, keeps its head, and the umbilical raises the engine_crashed
// alarm (httpapi/alarms.go) until EngineCrashAlarmWindow after the crash.
//
// The file is written only by the runtime's crash path. A clean stop (the
// deploy's SIGWINCH, or SIGTERM) and a log.Fatal exit write nothing to it. A
// boot failure such as LoadWorld failing is a log.Fatal, not a crash: the
// systemd start limit halts that one within ~25 s. A kernel OOM kill (SIGKILL)
// gives the runtime no chance to write, so it is not reported.
//
// Only the latest crash is kept. Each boot moves the previous process's crash
// to EngineCrashPrevFileName and arms a fresh empty file. A later restart
// inside the window (a deploy, say) re-reads the kept crash, so the alarm runs
// its full hour from the crash, not from the boot that found it. During a crash
// loop every boot reports the crash that ended the process before it.

// EngineCrashFileName is the crash file inside the state directory. The
// previous crash is kept beside it as EngineCrashPrevFileName, both for an
// operator with a shell and so a restart inside the alarm window can report it
// again.
const (
	EngineCrashFileName     = "crash.log"
	EngineCrashPrevFileName = "crash.prev.log"
)

// EngineCrashAlarmWindow is how long after a crash the engine_crashed alarm
// keeps firing. Same reasoning as CommandPanicAlarmWindow: the engine is up
// again, so the alarm has to stay long enough to be seen. The alarm prose says
// "an hour"; change it with this.
const EngineCrashAlarmWindow = time.Hour

// engineCrashReadCap bounds how much of the crash file a boot reads. A fatal
// runtime error can dump every goroutine; the head is all the alarm needs, and
// the cap is also what bounds the parse.
const engineCrashReadCap = 64 << 10

// engineCrashMessageCap bounds the kept message, which is one line of the crash
// but can carry an arbitrarily long panic value.
const engineCrashMessageCap = 500

// engineCrashNoHeadline is the message for a crash file with no "panic:" or
// "fatal error:" line. Go writes a fatal runtime error's headline (and its
// "runtime: ..." preamble) to stderr only, so the file starts at
// "runtime stack:" (seen with a stack overflow on go1.27).
const engineCrashNoHeadline = "fatal runtime error (the runtime wrote its message to stderr only; see journald)"

// EngineCrash is the crash a previous engine process left in the crash file.
// Immutable once read; safe to share across goroutines.
type EngineCrash struct {
	// CrashedAt is the crash file's modification time: when the runtime wrote
	// the crash. The alarm window runs from here.
	CrashedAt time.Time
	// FoundAt is when this boot read the file.
	FoundAt time.Time
	// Message is the crash's headline: the "panic: ..." or "fatal error: ..."
	// line when there is one, otherwise the first non-blank line. Capped at
	// engineCrashMessageCap.
	Message string
	// Frames are the first function frames of the crashing goroutine, innermost
	// first, as "pkg.Func (file.go:line)".
	Frames []string
}

// ReadPreviousEngineCrash returns the crash a previous process left in dir, or
// nil with no error when there is none to report.
//
//   - A non-blank crash.log is a new crash: it is moved to crash.prev.log,
//     and returned if it is younger than EngineCrashAlarmWindow. If the move
//     fails, the crash is returned WITH an error, and the caller must not call
//     ArmEngineCrashOutput (that would truncate the crash's only copy).
//   - Otherwise a crash.prev.log written less than EngineCrashAlarmWindow ago
//     is a crash an earlier boot already found; it is parsed (and left in
//     place) and returned, so a restart does not cut the alarm short.
//   - dir == "" means crash capture is disabled.
func ReadPreviousEngineCrash(dir string, now time.Time) (*EngineCrash, error) {
	if dir == "" {
		return nil, nil
	}
	current := filepath.Join(dir, EngineCrashFileName)
	prev := filepath.Join(dir, EngineCrashPrevFileName)

	data, modTime, err := readEngineCrashFile(current)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) > 0 {
		// Preserve it first (the rename keeps modTime). If that fails, report
		// the crash AND the error: the caller must then not arm, because arming
		// truncates crash.log, which is the crash's only copy on disk.
		if err := os.Rename(current, prev); err != nil {
			crash := parseEngineCrash(data)
			crash.CrashedAt = modTime
			crash.FoundAt = now
			return crash, fmt.Errorf("keep the previous crash as %s: %w", EngineCrashPrevFileName, err)
		}
		// Kept either way; reported only inside the window (an engine that
		// stayed down for an hour after the crash reports nothing).
		if now.Sub(modTime) >= EngineCrashAlarmWindow {
			return nil, nil
		}
		crash := parseEngineCrash(data)
		crash.CrashedAt = modTime
		crash.FoundAt = now
		return crash, nil
	}

	// crash.log is empty or missing, so arming destroys nothing: an unreadable
	// crash.prev.log only loses the re-report, and must not cost this run its
	// crash capture (the caller skips arming on any error). Swallow it.
	data, modTime, err = readEngineCrashFile(prev)
	if err != nil {
		return nil, nil
	}
	if len(bytes.TrimSpace(data)) == 0 || now.Sub(modTime) >= EngineCrashAlarmWindow {
		return nil, nil
	}
	crash := parseEngineCrash(data)
	crash.CrashedAt = modTime
	crash.FoundAt = now
	return crash, nil
}

// readEngineCrashFile reads up to engineCrashReadCap bytes of path and its
// modification time. A missing file is (nil, zero, nil).
func readEngineCrashFile(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, engineCrashReadCap))
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, info.ModTime().UTC(), nil
}

// ArmEngineCrashOutput creates (or truncates) the crash file in dir and hands
// it to the Go runtime, so a fatal crash of this process is written there as
// well as to stderr. A no-op when dir is empty. Call it once, early in main,
// after ReadPreviousEngineCrash.
func ArmEngineCrashOutput(dir string) error {
	if dir == "" {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, EngineCrashFileName), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	// SetCrashOutput duplicates the descriptor, so f can be closed at once.
	defer f.Close()
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// parseEngineCrash pulls the headline and the first function frames out of Go
// crash output, for example:
//
//	panic: runtime error: integer divide by zero
//	[signal ...]            (optional)
//
//	goroutine 42 [running]:
//	github.com/x/engine/sim.payOfferShortfall(...)
//		/path/to/pay_with_item_commands.go:2746 +0x1a4
//	...
//
// A fatal runtime error can put "runtime: ..." lines before its "fatal error:"
// line and a "runtime stack:" block before the first goroutine; both are
// skipped. It works on the already-bounded byte slice line by line, so a
// truncated last line (or a single line longer than the read cap) still yields
// a message. Frames from the runtime and the panic call itself are skipped, and
// it stops at the end of the first goroutine's stack.
func parseEngineCrash(data []byte) *EngineCrash {
	crash := &EngineCrash{}
	firstLine := ""
	inStack := false
	pendingFunc := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if !inStack {
			if strings.HasPrefix(line, "goroutine ") {
				inStack = true
				continue
			}
			if firstLine == "" && trimmed != "" {
				firstLine = trimmed
			}
			if crash.Message == "" && (strings.HasPrefix(trimmed, "panic: ") || strings.HasPrefix(trimmed, "fatal error: ")) {
				crash.Message = trimmed
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(line, "goroutine ") {
			break
		}
		if strings.HasPrefix(line, "\t") {
			if pendingFunc != "" {
				crash.Frames = append(crash.Frames, pendingFunc+" ("+crashFileLine(trimmed)+")")
				pendingFunc = ""
				if len(crash.Frames) == commandPanicFrameLimit {
					break
				}
			}
			continue
		}
		full := trimmed
		if i := strings.LastIndex(full, "("); i > 0 {
			full = full[:i]
		}
		// Skip the runtime's own frames (runtime.*, internal/runtime/...), the
		// panic call, and the "created by" trailer, matching on the full name
		// before shortening.
		if strings.HasPrefix(full, "runtime.") || strings.HasPrefix(full, "internal/") ||
			full == "panic" || strings.HasPrefix(full, "created by ") {
			pendingFunc = ""
			continue
		}
		pendingFunc = shortFuncName(full)
	}
	if crash.Message == "" {
		// A fatal runtime error (a throw, e.g. a stack overflow) prints its
		// "fatal error: ..." headline to stderr only; the crash file starts at
		// "runtime stack:". Say so rather than pass that header off as the
		// message. The frames still name the code that crashed.
		if firstLine == "" || firstLine == "runtime stack:" {
			crash.Message = engineCrashNoHeadline
		} else {
			crash.Message = firstLine
		}
	}
	if len(crash.Message) > engineCrashMessageCap {
		cut := engineCrashMessageCap
		for cut > 0 && !utf8.RuneStart(crash.Message[cut]) {
			cut--
		}
		crash.Message = crash.Message[:cut] + "…"
	}
	return crash
}

// crashFileLine turns "/path/to/file.go:123 +0x1a4" into "file.go:123".
func crashFileLine(s string) string {
	if i := strings.Index(s, " "); i > 0 {
		s = s[:i]
	}
	return path.Base(filepath.ToSlash(s))
}
