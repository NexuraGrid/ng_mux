package vterm

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestWriteDoesNotBlockOnStuckReplyWriter is the regression test for the
// deadlock this queue fixes: a child that never reads its stdin used to block
// the reply write inside Term.Write, which stopped the pump from draining the
// child's output and froze the pane with Term.mu held.
func TestWriteDoesNotBlockOnStuckReplyWriter(t *testing.T) {
	w := newBlockingWriter()
	defer close(w.unblock)
	term := New(20, 5, w)
	defer term.Close()

	flood := []byte(strings.Repeat("\x1b[6n\x1b[c", 200))
	if _, err := writeWithTimeout(t, term, flood); err != nil {
		t.Fatalf("Write: %v", err)
	}
	snapshotWithTimeout(t, term)

	if term.replyQ.dropped.Load() == 0 {
		t.Fatal("no replies were dropped although the reply writer never accepted one")
	}
}

func TestRepliesDeliveredInOrder(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	defer term.Close()

	// DA, a CPR at the origin, a cursor move, then a second CPR.
	if _, err := term.Write([]byte("\x1b[c\x1b[6n\x1b[2;3H\x1b[6n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitUntilReply(t, &reply, "\x1b[?1;2c\x1b[1;1R\x1b[2;3R")
}

func TestCloseStopsDeliveryAndDropsLaterReplies(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	if _, err := term.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitUntilReply(t, &reply, "\x1b[?1;2c")

	term.Close()
	term.Close() // idempotent

	select {
	case <-term.replyQ.doneCh:
	case <-time.After(asyncReplyTimeout):
		t.Fatal("delivery goroutine still running after Close")
	}

	if _, err := term.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write after Close: %v", err)
	}
	assertReplyStaysAt(t, &reply, "\x1b[?1;2c")
	if term.replyQ.dropped.Load() == 0 {
		t.Fatal("a reply after Close was not counted as dropped")
	}
}

// TestCloseDoesNotWaitForStuckDelivery guards pane teardown: panes are closed
// under the session lock, so Close must return even while a delivery is stuck
// in a pty write.
func TestCloseDoesNotWaitForStuckDelivery(t *testing.T) {
	w := newBlockingWriter()
	term := New(20, 5, w)
	if _, err := term.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitUntil(t, func() bool { return w.writeCount() == 1 })

	closed := make(chan struct{})
	go func() {
		term.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(asyncReplyTimeout):
		t.Fatal("Close blocked on a stuck reply write")
	}

	close(w.unblock)
	select {
	case <-term.replyQ.doneCh:
	case <-time.After(asyncReplyTimeout):
		t.Fatal("delivery goroutine did not exit once the stuck write returned")
	}
}

func TestRepliesSurviveEmulatorPanic(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	defer term.Close()

	forcePanicOnNextWrite(term)
	if _, err := term.Write([]byte("boom")); !errors.Is(err, ErrEmulatorPanic) {
		t.Fatalf("err = %v, want ErrEmulatorPanic", err)
	}
	if _, err := term.Write([]byte("\x1b[c")); err != nil {
		t.Fatalf("Write after recovery: %v", err)
	}
	waitUntilReply(t, &reply, "\x1b[?1;2c")
}

func TestNilReplyWriterDisablesReplies(t *testing.T) {
	term := New(20, 5, nil)
	if _, err := term.Write([]byte("\x1b[c\x1b[>c\x1b[6n\x1b[5n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	term.Close()
}
