package vterm

import (
	"io"
	"sync"
	"sync/atomic"
)

// replyQueueCap bounds how many pending query replies a Term holds before it
// starts dropping the newest one. Replies are a handful of bytes each (a DA
// or CPR response), so this trades a small, fixed amount of memory for never
// blocking the caller — see replyQueue's doc comment for why blocking here
// would deadlock a pane rather than just slow it down.
const replyQueueCap = 32

// replyQueue is an io.Writer that decouples "answer this terminal query" from
// "actually write the answer to the pty master". vt10x calls Write while
// holding its own internal state lock (see internal/vt10x's CSI 'c'/'n'
// handlers and its OSC color-query handling), from inside a call that vterm's
// Term.Write in turn makes while holding Term.mu. A synchronous write to the
// real pty from either of those locked sections would let a child that never
// drains its stdin (or a flood of CPR queries) block the write indefinitely —
// wedging Term.mu (and, transitively, every Snapshot and every other pane
// sharing the session lock) or vt10x's own lock behind it.
//
// replyQueue.Write instead appends the payload to a small bounded channel and
// returns immediately, always reporting success: a dropped or delayed
// terminal-query reply is never an error the emulator or its caller should
// see. A single per-Term goroutine, started lazily on the first reply so a
// Term that never receives a query (most tests, most short-lived panes) never
// spins one up, drains that channel and performs the real (blocking) write to
// the pty master, in order. If the channel is full — the pty's read side
// isn't keeping up — the reply is dropped and counted: a terminal that never
// answers a query is normal and benign (see fish's ~10s DA timeout, which is
// exactly the failure mode this package works around); a deadlocked pane is
// not.
type replyQueue struct {
	out io.Writer // the real pty master; immutable after construction

	mu      sync.Mutex
	ch      chan []byte
	stopCh  chan struct{}
	doneCh  chan struct{}
	started bool
	closed  bool

	// dropped counts replies dropped because the queue was full or the queue
	// had been closed. Unexported: nothing outside tests needs it today, but
	// it is cheap to keep for diagnosing a wedged child later.
	dropped atomic.Uint64
}

func newReplyQueue(out io.Writer) *replyQueue {
	return &replyQueue{out: out}
}

// Write enqueues p for asynchronous delivery to the real writer. It never
// blocks on that writer and never fails: p is copied (the caller may reuse
// its backing array), queued if there's room, and dropped (counted) if the
// queue is full or Close has already been called.
func (q *replyQueue) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	cp := append([]byte(nil), p...)

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		q.dropped.Add(1)
		return n, nil
	}
	if !q.started {
		q.ch = make(chan []byte, replyQueueCap)
		q.stopCh = make(chan struct{})
		q.doneCh = make(chan struct{})
		q.started = true
		go q.run(q.ch, q.stopCh, q.doneCh)
	}
	ch := q.ch
	q.mu.Unlock()

	select {
	case ch <- cp:
	default:
		q.dropped.Add(1)
	}
	return n, nil
}

// run drains ch, writing each reply to the real writer in order, until told
// to stop via stopCh or the writer stops accepting writes. It never returns
// an error or panic to anything: this goroutine has no caller to report to,
// and letting a panic escape it would take down the whole daemon over a
// single misbehaving pty, not just this Term.
//
// If the writer fails (an error, or a recovered panic), run exits without
// draining ch further. That is deliberate rather than an oversight: later
// Write calls keep enqueueing (up to replyQueueCap) into a channel nothing is
// reading, so once the queue is full every reply after that is dropped by the
// same bounded-and-drop path used for a slow reader — there is no separate
// "stopped" bookkeeping to keep in sync.
func (q *replyQueue) run(ch chan []byte, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case b := <-ch:
			if !q.deliver(b) {
				return
			}
		}
	}
}

// deliver writes b to the real writer, recovering from a panic there the same
// way an error is handled: report failure so run stops attempting further
// deliveries, without letting the panic itself escape this goroutine.
func (q *replyQueue) deliver(b []byte) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	_, err := q.out.Write(b)
	return err == nil
}

// Close tells the delivery goroutine, if one was ever started, to stop, and
// makes every later Write drop its payload instead of queueing it. It does not
// wait for the goroutine: a delivery stuck in a pty write must never block the
// caller (panes are closed under the session lock). Closing the pty makes that
// write fail, and the goroutine exits on its own. Idempotent.
func (q *replyQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	if q.started {
		close(q.stopCh)
	}
}
