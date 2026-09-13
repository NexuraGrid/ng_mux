package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestRecoverAndLogSwallowsPanicAndLogs(t *testing.T) {
	var mu sync.Mutex
	var got string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		got = fmt.Sprintf(format, args...)
	}

	func() {
		defer recoverAndLog(logf, "test place")
		panic("kaboom")
	}()

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got, "test place") || !strings.Contains(got, "kaboom") {
		t.Fatalf("log message = %q, want it to mention both the location and the panic value", got)
	}
}

// TestRecoverAndLogNilLogfDoesNotPanic asserts recoverAndLog is nil-safe: a
// caller with no logger configured (a bare *pane in a test, say) must not
// crash trying to report the very panic it exists to swallow.
func TestRecoverAndLogNilLogfDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recoverAndLog with a nil logf let a panic re-escape: %v", r)
		}
	}()
	func() {
		defer recoverAndLog(nil, "test place")
		panic("kaboom")
	}()
}

func TestRecoverAndLogNoPanicIsNoop(t *testing.T) {
	called := false
	logf := func(format string, args ...any) { called = true }
	func() {
		defer recoverAndLog(logf, "test place")
	}()
	if called {
		t.Fatal("recoverAndLog logged even though nothing panicked")
	}
}
