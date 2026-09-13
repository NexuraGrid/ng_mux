package server

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func TestPanePumpForwardsOutputThenSignalsExit(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	pt.feed([]byte("hello world"))
	waitFor(t, func() bool { return sc.consumedBytes() == "hello world" }, time.Second)

	pt.Close()
	select {
	case got := <-exited:
		if got != p {
			t.Fatalf("onExit called with %v, want the pane itself", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

// TestPanePumpSurvivesEmulatorPanicError is the regression test for the crash
// this change fixes: a pane whose screen reports a recovered emulator panic
// (vterm.Term.Write wraps ErrEmulatorPanic instead of letting the panic climb
// out) must log it once, with its own pane id, and keep pumping — never take
// the shell, let alone the daemon, down with it.
func TestPanePumpSurvivesEmulatorPanicError(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)

	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	p := &pane{id: 7, pt: pt, vt: sc, logf: logf}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	sc.setWriteErr(fmt.Errorf("boom: %w", vterm.ErrEmulatorPanic))
	pt.feed([]byte("trigger"))

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(logs) > 0
	}, time.Second)

	mu.Lock()
	got := append([]string(nil), logs...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected exactly one logged occurrence, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "pane 7") {
		t.Fatalf("log entry %q does not mention the pane id", got[0])
	}

	// pump must keep consuming pty output after the error.
	pt.feed([]byte("still alive"))
	waitFor(t, func() bool { return strings.Contains(sc.consumedBytes(), "still alive") }, time.Second)

	pt.Close()
	select {
	case got := <-exited:
		if got != p {
			t.Fatalf("onExit called with %v, want the pane itself", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

func TestPaneResizePropagatesToPtyAndScreen(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc}

	p.resize(40, 10)

	if c, r := pt.size(); c != 40 || r != 10 {
		t.Errorf("pty size = %dx%d, want 40x10", c, r)
	}
	if c, r := sc.size(); c != 40 || r != 10 {
		t.Errorf("screen size = %dx%d, want 40x10", c, r)
	}
}

func TestPaneResizeClampsCopyCursor(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc, copy: newCopyState(80, 24)}
	p.copy.cy = 23

	p.resize(80, 10)

	if p.copy.cy != 9 {
		t.Errorf("copy cursor y = %d, want it clamped to 9", p.copy.cy)
	}
	if p.copy.rows != 10 || p.copy.cols != 80 {
		t.Errorf("copy viewport = %dx%d, want 80x10", p.copy.cols, p.copy.rows)
	}
}
