package sim_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtime/debug"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// engine_crash_test.go — LLM-999: the crash file a previous engine process
// left behind, read at boot for the engine_crashed alarm.

// The subprocess half of the crash tests. When the test binary is re-run with
// this variable set, TestMain arms the crash file in that directory and then
// does what the mode says, instead of running the tests.
const (
	crashTestDirEnv  = "SALEM_ENGINE_CRASH_TEST_DIR"
	crashTestModeEnv = "SALEM_ENGINE_CRASH_TEST_MODE"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(crashTestDirEnv); dir != "" {
		runCrashTestChild(dir, os.Getenv(crashTestModeEnv))
		return
	}
	os.Exit(m.Run())
}

// crashTestBoom is the frame the crash file must name.
func crashTestBoom() {
	panic("crash test boom")
}

// crashTestRecurse overflows the stack: a fatal runtime error, not a panic.
func crashTestRecurse(n int) int {
	var pad [256]byte
	pad[n%256] = byte(n)
	return crashTestRecurse(n+1) + int(pad[0])
}

func runCrashTestChild(dir, mode string) {
	if err := sim.ArmEngineCrashOutput(dir); err != nil {
		os.Exit(3)
	}
	switch mode {
	case "panic-goroutine":
		// An unrecovered panic on a goroutine that is not a world command:
		// the kind LLM-997 cannot contain.
		// The main goroutine just waits: the panic ends the process long before
		// the sleep does. (A deferred signal from the panicking goroutine would
		// race the crash: deferred calls run before the runtime kills the
		// process, so main could exit first.)
		go crashTestBoom()
		time.Sleep(30 * time.Second)
		os.Exit(4)
	case "stack-overflow":
		debug.SetMaxStack(1 << 20)
		crashTestRecurse(0)
		os.Exit(4)
	case "clean-exit":
		os.Exit(0)
	}
	os.Exit(5)
}

func runCrashTestSubprocess(t *testing.T, dir, mode string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), crashTestDirEnv+"="+dir, crashTestModeEnv+"="+mode)
	return cmd.Run()
}

func TestEngineCrash_RealPanicIsCapturedAndReadNextBoot(t *testing.T) {
	dir := t.TempDir()
	err := runCrashTestSubprocess(t, dir, "panic-goroutine")
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("child err = %v, want a crash (exit status 2)", err)
	}

	now := time.Now().UTC()
	crash, err := sim.ReadPreviousEngineCrash(dir, now)
	if err != nil {
		t.Fatalf("ReadPreviousEngineCrash: %v", err)
	}
	if crash == nil {
		t.Fatal("no crash read after a real panic")
	}
	if crash.Message != "panic: crash test boom" {
		t.Errorf("message = %q, want \"panic: crash test boom\"", crash.Message)
	}
	if len(crash.Frames) == 0 || !strings.Contains(crash.Frames[0], "crashTestBoom") {
		t.Errorf("frames = %v, want crashTestBoom first", crash.Frames)
	}
	if crash.CrashedAt.IsZero() || crash.CrashedAt.After(now) {
		t.Errorf("crashed at = %v, want the file's write time before %v", crash.CrashedAt, now)
	}
	if !crash.FoundAt.Equal(now) {
		t.Errorf("found at = %v, want %v", crash.FoundAt, now)
	}
	if _, err := os.Stat(filepath.Join(dir, sim.EngineCrashFileName)); !os.IsNotExist(err) {
		t.Errorf("crash file still in place after the read (err=%v); it should move to %s", err, sim.EngineCrashPrevFileName)
	}
	if _, err := os.Stat(filepath.Join(dir, sim.EngineCrashPrevFileName)); err != nil {
		t.Errorf("previous crash file missing: %v", err)
	}
	// A restart inside the window (crash.log now empty, as the arm leaves it)
	// reports the kept crash again, with the same crash time.
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	later := now.Add(10 * time.Minute)
	again, err := sim.ReadPreviousEngineCrash(dir, later)
	if err != nil || again == nil {
		t.Fatalf("restart inside the window: (%+v, %v), want the kept crash", again, err)
	}
	if again.Message != crash.Message || !again.CrashedAt.Equal(crash.CrashedAt) || !again.FoundAt.Equal(later) {
		t.Errorf("restart read = %+v, want the same crash found at %v", again, later)
	}
	// Past the window from the crash, a restart reports nothing.
	if gone, err := sim.ReadPreviousEngineCrash(dir, crash.CrashedAt.Add(sim.EngineCrashAlarmWindow)); err != nil || gone != nil {
		t.Errorf("restart past the window: (%+v, %v), want (nil, nil)", gone, err)
	}
}

func TestEngineCrash_RealStackOverflowIsCaptured(t *testing.T) {
	dir := t.TempDir()
	err := runCrashTestSubprocess(t, dir, "stack-overflow")
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("child err = %v, want a fatal error (exit status 2)", err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err != nil || crash == nil {
		t.Fatalf("read = (%+v, %v)", crash, err)
	}
	// Go writes a throw's "fatal error: stack overflow" headline to stderr only,
	// so the file has no headline; the message says so, and must not be the
	// "runtime stack:" header.
	if !strings.HasPrefix(crash.Message, "fatal runtime error") {
		t.Errorf("message = %q, want the no-headline fatal runtime error message", crash.Message)
	}
	if len(crash.Frames) == 0 || !strings.Contains(crash.Frames[0], "crashTestRecurse") {
		t.Errorf("frames = %v, want crashTestRecurse first", crash.Frames)
	}
}

// TestEngineCrash_LongPanicMessageKeepsFrames: a panic value spanning many
// lines must not crowd the stack out of the parse.
func TestEngineCrash_LongPanicMessageKeepsFrames(t *testing.T) {
	var b strings.Builder
	b.WriteString("panic: first line of a long value\n")
	for i := 0; i < 200; i++ {
		b.WriteString("more of the panic value\n")
	}
	b.WriteString("\ngoroutine 7 [running]:\n")
	b.WriteString("runtime.gopanic({0x1, 0x2})\n\t/usr/local/go/src/runtime/panic.go:791 +0x132\n")
	b.WriteString("github.com/x/engine/sim.(*World).republish(0x1)\n\t/var/www/zbbs/engine/sim/world.go:2701 +0x1a4\n")
	b.WriteString("\ngoroutine 1 [select]:\nmain.other()\n\t/x/main.go:1 +0x1\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte(b.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err != nil || crash == nil {
		t.Fatalf("read = (%+v, %v)", crash, err)
	}
	if crash.Message != "panic: first line of a long value" {
		t.Errorf("message = %q", crash.Message)
	}
	if len(crash.Frames) != 1 || crash.Frames[0] != "sim.(*World).republish (world.go:2701)" {
		t.Errorf("frames = %v", crash.Frames)
	}
}

// TestEngineCrash_OversizedFirstLineIsCappedNotLost: one line longer than the
// read cap (truncated by it, no newline) still yields a bounded message.
func TestEngineCrash_OversizedFirstLineIsCappedNotLost(t *testing.T) {
	line := "panic: " + strings.Repeat("x", 100<<10)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte(line), 0o640); err != nil {
		t.Fatal(err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err != nil || crash == nil {
		t.Fatalf("read = (%+v, %v)", crash, err)
	}
	if !strings.HasPrefix(crash.Message, "panic: xxx") || len(crash.Message) > 510 {
		t.Errorf("message length %d, want a non-empty message capped near 500 bytes", len(crash.Message))
	}
}

func TestEngineCrash_CleanExitLeavesNoCrash(t *testing.T) {
	dir := t.TempDir()
	// A stale crash from an earlier process must be cleared by the arm.
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte("panic: old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := runCrashTestSubprocess(t, dir, "clean-exit"); err != nil {
		t.Fatalf("child err = %v, want a clean exit", err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err != nil || crash != nil {
		t.Errorf("after a clean exit: (%+v, %v), want (nil, nil)", crash, err)
	}
}

func TestEngineCrash_NoDirOrNoFileIsNoCrash(t *testing.T) {
	if crash, err := sim.ReadPreviousEngineCrash("", time.Now()); crash != nil || err != nil {
		t.Errorf("empty dir: (%+v, %v)", crash, err)
	}
	if err := sim.ArmEngineCrashOutput(""); err != nil {
		t.Errorf("arm with empty dir: %v", err)
	}
	if crash, err := sim.ReadPreviousEngineCrash(t.TempDir(), time.Now()); crash != nil || err != nil {
		t.Errorf("missing file: (%+v, %v)", crash, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte("\n  \n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if crash, err := sim.ReadPreviousEngineCrash(dir, time.Now()); crash != nil || err != nil {
		t.Errorf("blank file: (%+v, %v)", crash, err)
	}
}

// TestEngineCrash_ParsesAFatalErrorAndStopsAtTheFirstGoroutine uses the shape
// of a Go fatal runtime error: a message, a blank line, the crashing
// goroutine's stack with runtime frames, then other goroutines.
func TestEngineCrash_ParsesAFatalErrorAndStopsAtTheFirstGoroutine(t *testing.T) {
	text := `fatal error: concurrent map writes

goroutine 42 gp=0xc000102a80 m=5 mp=0xc000100008 [running]:
runtime.fatal({0x1234, 0x15})
	/usr/local/go/src/runtime/panic.go:1180 +0x18
internal/runtime/maps.fatal({0x1234?, 0x0?})
	/usr/local/go/src/runtime/panic.go:1000 +0x18
github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim.(*World).republish(0xc0001c4000)
	/var/www/zbbs/engine/sim/world.go:2701 +0x1a4
github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim.(*World).Run(0xc0001c4000, {0x1, 0x2})
	/var/www/zbbs/engine/sim/world.go:2425 +0x2f0
created by github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cmd/engine.run in goroutine 1
	/var/www/zbbs/engine/sim/cmd/engine/main.go:640 +0x99

goroutine 1 gp=0xc000002380 m=nil [select]:
main.otherStuff()
	/var/www/zbbs/engine/sim/cmd/engine/main.go:700 +0x10
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err != nil || crash == nil {
		t.Fatalf("read = (%+v, %v)", crash, err)
	}
	if crash.Message != "fatal error: concurrent map writes" {
		t.Errorf("message = %q", crash.Message)
	}
	want := []string{"sim.(*World).republish (world.go:2701)", "sim.(*World).Run (world.go:2425)"}
	if len(crash.Frames) != len(want) || crash.Frames[0] != want[0] || crash.Frames[1] != want[1] {
		t.Errorf("frames = %v, want %v (runtime frames skipped, second goroutine ignored)", crash.Frames, want)
	}
}

// TestEngineCrash_FailedRotationKeepsTheCrashAndReportsAnError: when
// crash.log cannot be moved aside, the crash is still reported, an error says
// so (main then skips arming), and crash.log is left intact.
func TestEngineCrash_FailedRotationKeepsTheCrashAndReportsAnError(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory where crash.prev.log should go makes the rename fail.
	if err := os.MkdirAll(filepath.Join(dir, sim.EngineCrashPrevFileName, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sim.EngineCrashFileName), []byte("panic: kept\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	crash, err := sim.ReadPreviousEngineCrash(dir, time.Now().UTC())
	if err == nil {
		t.Fatal("no error for a failed rotation")
	}
	if crash == nil || crash.Message != "panic: kept" {
		t.Errorf("crash = %+v, want the crash reported despite the failed rotation", crash)
	}
	data, rerr := os.ReadFile(filepath.Join(dir, sim.EngineCrashFileName))
	if rerr != nil || !strings.Contains(string(data), "panic: kept") {
		t.Errorf("crash.log = (%q, %v), want it left intact", data, rerr)
	}
}

// TestEngineCrash_UnreadCrashOlderThanTheWindowIsKeptNotReported: an engine
// that stayed down for the whole window reports nothing on its next boot, but
// still moves the crash aside.
func TestEngineCrash_UnreadCrashOlderThanTheWindowIsKeptNotReported(t *testing.T) {
	for _, age := range []time.Duration{sim.EngineCrashAlarmWindow, 2 * sim.EngineCrashAlarmWindow} {
		dir := t.TempDir()
		path := filepath.Join(dir, sim.EngineCrashFileName)
		if err := os.WriteFile(path, []byte("panic: old\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		crashedAt := now.Add(-age)
		if err := os.Chtimes(path, crashedAt, crashedAt); err != nil {
			t.Fatal(err)
		}
		crash, err := sim.ReadPreviousEngineCrash(dir, now)
		if err != nil || crash != nil {
			t.Errorf("age %v: (%+v, %v), want (nil, nil)", age, crash, err)
		}
		if _, err := os.Stat(filepath.Join(dir, sim.EngineCrashPrevFileName)); err != nil {
			t.Errorf("age %v: crash not kept as %s: %v", age, sim.EngineCrashPrevFileName, err)
		}
	}
}
