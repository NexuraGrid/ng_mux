package vterm

import (
	"errors"
	"testing"
	"time"
)

// TestWriteRecoversEmulatorPanic forces a panic inside Term.Write (via an
// emulator that panics on its next Write, see forcePanicOnNextWrite) and
// asserts: the panic never escapes Write, the returned error matches
// ErrEmulatorPanic via errors.Is, scrollback history survives the emulator
// swap, and the Term keeps working afterwards (proving t.mu was not left
// locked).
func TestWriteRecoversEmulatorPanic(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(100)

	// Scroll some lines into history before forcing the panic, so we can
	// confirm the panic recovery does not lose it.
	for i := 1; i <= 8; i++ {
		if _, err := writeWithTimeout(t, term, []byte("line"+itoa(i)+"\r\n")); err != nil {
			t.Fatalf("setup write %d: %v", i, err)
		}
	}
	histBefore := term.HistoryLen()
	if histBefore == 0 {
		t.Fatal("setup: expected some scrollback before forcing a panic")
	}

	forcePanicOnNextWrite(term)
	_, err := writeWithTimeout(t, term, []byte("boom"))
	if err == nil {
		t.Fatal("Write returned no error after the emulator panicked")
	}
	if !errors.Is(err, ErrEmulatorPanic) {
		t.Fatalf("err = %v, want it to match ErrEmulatorPanic via errors.Is", err)
	}

	if got := term.HistoryLen(); got != histBefore {
		t.Fatalf("history len after recovery = %d, want unchanged %d", got, histBefore)
	}

	// A stuck t.mu would hang here forever; writeWithTimeout/snapshotWithTimeout
	// fail the test instead of blocking the suite.
	if _, err := writeWithTimeout(t, term, []byte("hello\r\n")); err != nil {
		t.Fatalf("Write after recovery: %v", err)
	}
	snap := snapshotWithTimeout(t, term)
	if !containsRow(snap, "hello") {
		t.Fatalf("snapshot after recovery missing new content:\n%s", dump(snap))
	}
}

const panicTestTimeout = 2 * time.Second

// writeWithTimeout calls term.Write on its own goroutine so the test fails
// with a clear message instead of hanging if a recovered panic ever left
// t.mu locked.
func writeWithTimeout(t *testing.T, term *Term, p []byte) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := term.Write(p)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(panicTestTimeout):
		t.Fatal("Write did not return within timeout: t.mu is likely deadlocked")
		return 0, nil
	}
}

// snapshotWithTimeout is writeWithTimeout's counterpart for Snapshot.
func snapshotWithTimeout(t *testing.T, term *Term) Snapshot {
	t.Helper()
	done := make(chan Snapshot, 1)
	go func() {
		done <- term.Snapshot()
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(panicTestTimeout):
		t.Fatal("Snapshot did not return within timeout: t.mu is likely deadlocked")
		return Snapshot{}
	}
}
