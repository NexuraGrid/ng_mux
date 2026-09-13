package server

import (
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// TestHiddenWindowDoesNotKeepSessionDirty is the regression test for sessions
// with several windows recomposing every tick: frame() only snapshots the
// current window, so a background pane's dirty flag was never cleared.
func TestHiddenWindowDoesNotKeepSessionDirty(t *testing.T) {
	srv, ff, sess := setupSession(t)
	exec(t, srv, "new-window") // window 1 with pane 2 is now current
	sess.frame()
	if sess.dirty() {
		t.Fatal("an idle session with a background window should be clean after a frame")
	}

	_, _ = ff.byID(1).scr.Write([]byte("background worker output"))
	if sess.dirty() {
		t.Fatal("output in a background window made the session dirty")
	}

	_, _ = ff.byID(2).scr.Write([]byte("visible output"))
	if !sess.dirty() {
		t.Fatal("output in the current window should make the session dirty")
	}
	sess.frame()

	exec(t, srv, "select-window 0")
	if !sess.dirty() {
		t.Fatal("switching to the background window should repaint")
	}
	sess.frame()
	if sess.dirty() {
		t.Fatal("session should be clean once the switched-to window is drawn")
	}
}

func TestZoomedWindowIgnoresHiddenPanes(t *testing.T) {
	srv, ff, sess := setupSession(t)
	exec(t, srv, "split-window -h") // pane 2 is active
	exec(t, srv, "resize-pane -Z")  // zoom pane 2; pane 1 is hidden
	sess.frame()
	if sess.dirty() {
		t.Fatal("zoomed session should be clean after a frame")
	}

	_, _ = ff.byID(1).scr.Write([]byte("hidden pane output"))
	if sess.dirty() {
		t.Fatal("output in a pane hidden by zoom made the session dirty")
	}
	_, _ = ff.byID(2).scr.Write([]byte("zoomed pane output"))
	if !sess.dirty() {
		t.Fatal("output in the zoomed pane should make the session dirty")
	}
	sess.frame()

	exec(t, srv, "resize-pane -Z") // unzoom shows pane 1 again
	if !sess.dirty() {
		t.Fatal("unzooming should repaint")
	}
}

// TestIdleMultiWindowSessionSendsNoFrames checks the whole path: an idle
// session with a background window must not stream frames every tick.
func TestIdleMultiWindowSessionSendsNoFrames(t *testing.T) {
	srv, _, sess := setupSession(t)
	exec(t, srv, "new-window")
	c, peer := pipeClient(t, sess.name)
	srv.addClient(c)
	col := collect(peer, 0)

	for i := 0; i < 15; i++ {
		srv.tick()
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, func() bool { return !c.writerBusy() }, time.Second)

	// The first frame, plus at most one more if the status clock's minute
	// rolled over during the loop.
	if n := col.count(protocol.TypeFrame); n < 1 || n > 2 {
		t.Fatalf("idle session sent %d frames over 15 ticks, want 1 (or 2 across a minute boundary)", n)
	}
}
