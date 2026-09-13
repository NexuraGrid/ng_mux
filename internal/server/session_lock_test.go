package server

import (
	"testing"
	"time"
)

// TestFramePanicDoesNotLeaveSessionLocked guards the pairing of the broadcast
// tick's recover with frame(): a panic while building a view is recovered one
// level up, so frame must release the session lock on the way out. Otherwise
// the session would freeze for good — no more frames, no more input.
func TestFramePanicDoesNotLeaveSessionLocked(t *testing.T) {
	_, ff, sess := setupSession(t)
	ff.byID(1).scr.panicNextSnapshot()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("frame did not panic; the injected fault never fired")
			}
		}()
		sess.frame()
	}()

	done := make(chan struct{})
	go func() {
		sess.input([]byte("x"))
		_ = sess.dirty()
		sess.frame()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session stayed locked after a recovered frame panic")
	}
}
