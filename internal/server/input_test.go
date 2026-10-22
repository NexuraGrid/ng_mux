package server

import (
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// TestStuckPaneDoesNotBlockClient: a pane whose child stopped reading stdin
// must not freeze the client that typed into it. Its later messages (here a
// resize, in practice a detach or a pane switch) are still handled, and the
// queued input reaches the child, in order, once it reads again.
func TestStuckPaneDoesNotBlockClient(t *testing.T) {
	srv, ff, sess := setupSession(t)
	fp := ff.last()
	fp.pt.stall = make(chan struct{})

	cli := attachPipe(t, srv, "0", 80, 24)
	waitSessSize(t, sess, 80, 24)

	// Sent from a goroutine: the pipe is unbuffered, so if the server is stuck
	// on the pane, these writes block too and the size check below fails
	// instead of hanging the test.
	go func() {
		_ = cli.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte("first ")})
		_ = cli.Write(protocol.Message{Type: protocol.TypeInput, Data: []byte("second")})
		_ = cli.Write(protocol.Message{Type: protocol.TypeResize, Cols: 100, Rows: 30})
	}()
	waitSessSize(t, sess, 100, 30)

	close(fp.pt.stall)
	waitFor(t, func() bool { return fp.pt.sentToChild() == "first second" }, 2*time.Second)
}

// TestPaneInputQueueIsBounded: input piling up for a child that never reads
// is capped, so a huge paste into a stuck pane cannot grow the daemon without
// limit. What does not fit is dropped and logged.
func TestPaneInputQueueIsBounded(t *testing.T) {
	pt := newFakePty(80, 24)
	pt.stall = make(chan struct{})
	var logged atomic.Int32
	p := &pane{id: 1, pt: pt, logf: func(string, ...any) { logged.Add(1) }}
	t.Cleanup(func() { p.in.close(); pt.Close() })

	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < maxPendingInput>>20+4; i++ {
		p.write(chunk)
	}
	if got := p.in.pending(); got > maxPendingInput {
		t.Fatalf("queued %d bytes, want at most %d", got, maxPendingInput)
	}
	if logged.Load() == 0 {
		t.Fatal("dropped input was not logged")
	}

	close(pt.stall)
	waitFor(t, func() bool { return p.in.pending() == 0 }, 5*time.Second)
	if sent := pt.sentToChild(); len(sent) > maxPendingInput+len(chunk) || strings.Trim(sent, "x") != "" {
		t.Fatalf("child got %d bytes, want at most %d of 'x'", len(sent), maxPendingInput+len(chunk))
	}
}

// TestPaneInputSurvivesWriteError: one failed pty write (a transient ConPTY
// hiccup) loses only that chunk; later input still reaches the child.
func TestPaneInputSurvivesWriteError(t *testing.T) {
	pt := newFakePty(80, 24)
	pt.failNext = errors.New("transient")
	p := &pane{id: 1, pt: pt}
	pt.queued = p.in.pending
	t.Cleanup(func() { p.in.close(); pt.Close() })

	p.write([]byte("lost"))
	waitFor(t, func() bool { return p.in.pending() == 0 }, time.Second)
	p.write([]byte("kept"))
	if got := pt.sentToChild(); got != "kept" {
		t.Fatalf("child got %q, want %q", got, "kept")
	}
}
