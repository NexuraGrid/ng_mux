package vterm

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/vt10x"
)

// asyncReplyTimeout bounds how long tests wait for replyQueue's background
// goroutine to deliver a reply. Generous relative to how fast delivery
// actually happens (microseconds), to keep the suite reliable under load
// (-race, CI, a busy dev machine) without ever masking a real deadlock: a
// wedged pane would still be caught by writeWithTimeout/snapshotWithTimeout
// (see panic_test.go), which this package also uses.
const asyncReplyTimeout = 2 * time.Second

// syncBuffer is a bytes.Buffer safe for concurrent use: replyQueue delivers
// to the real writer from its own goroutine, so any test writer a test polls
// from its own goroutine must guard against a concurrent Write, or -race
// flags it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// waitUntil polls cond until it reports true or asyncReplyTimeout elapses,
// failing the test in the latter case rather than hanging the suite.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(asyncReplyTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", asyncReplyTimeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitUntilReply waits for buf to contain exactly want, the common case for a
// single expected reply.
func waitUntilReply(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	waitUntil(t, func() bool { return buf.String() == want })
	if got := buf.String(); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

// assertReplyStaysAt gives the delivery goroutine a brief moment to (wrongly)
// deliver something extra, then asserts buf is still exactly want. Used after
// waitUntilReply to catch a double-delivery bug that a plain one-shot check
// right after waitUntilReply could otherwise race past.
func assertReplyStaysAt(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	if got := buf.String(); got != want {
		t.Fatalf("reply = %q after settling, want it to stay %q (unexpected extra delivery)", got, want)
	}
}

// panicTerm wraps a vt10x.Terminal and panics on its very first Write call,
// then delegates normally afterward. It forces Term.Write's panic-recovery
// path deterministically now that a misbehaving reply writer can no longer
// make vt10x itself panic synchronously: replyQueue (see reply_queue.go)
// makes every write to it non-blocking and never propagates a panic back into
// the caller, so the old "panicyWriter as the reply writer" trick no longer
// reaches vt10x's own Write at all.
type panicTerm struct {
	vt10x.Terminal
	tripped bool
}

func (p *panicTerm) Write(b []byte) (int, error) {
	if !p.tripped {
		p.tripped = true
		panic("boom: emulator exploded")
	}
	return p.Terminal.Write(b)
}

// forcePanicOnNextWrite swaps term's underlying vt10x.Terminal for one that
// panics on its very next Write, so the following Write call exercises
// Term.Write's panic-recovery path. Caller holds no lock; this takes term.mu
// itself, matching every other Term method.
func forcePanicOnNextWrite(term *Term) {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.t = &panicTerm{Terminal: term.t}
}

// blockingWriter is an io.Writer whose Write never returns until unblock is
// closed, simulating a pty whose child never drains its stdin (or is
// otherwise wedged). Used to prove replyQueue.Write and, transitively,
// Term.Write never block on it.
type blockingWriter struct {
	unblock chan struct{}
	mu      sync.Mutex
	writes  int
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{unblock: make(chan struct{})}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	<-w.unblock
	return len(p), nil
}

func (w *blockingWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}
