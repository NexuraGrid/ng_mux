package server

import (
	"io"
	"sync"
)

// maxPendingInput caps how many bytes may wait in one pane's input queue for a
// child that is not reading its stdin. It is far above anything typed or
// pasted by hand, and only bounds memory against a runaway paste or
// send-keys loop into a stuck pane.
const maxPendingInput = 16 << 20

// inputQueue delivers bytes headed for a pane's pty from a goroutine of its
// own, in order. A pty write blocks for as long as the child is not reading
// its stdin (a worker that only prints logs, or a Windows ConPTY whose output
// is backed up), and the callers — a client's serveClient loop, send-keys,
// paste-buffer, mouse reports — must never wait on that: blocked there, the
// client could not even detach. push only queues; the writer goroutine is the
// one that waits.
//
// The zero value is ready to use; the writer goroutine starts on the first
// push and exits on close. A failed write loses only its own chunk, as the
// old direct writes did, so a transient error never mutes the pane for good.
type inputQueue struct {
	mu      sync.Mutex
	chunks  [][]byte
	size    int // bytes queued, including a chunk the writer is writing
	wake    chan struct{}
	started bool
	closed  bool
}

// push queues a copy of data for w and reports whether it was accepted. Data
// is refused once the queue is closed, or when it would take the queue past
// maxPendingInput.
func (q *inputQueue) push(w io.Writer, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	q.mu.Lock()
	if q.closed || q.size+len(data) > maxPendingInput {
		q.mu.Unlock()
		return false
	}
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	q.chunks = append(q.chunks, append([]byte(nil), data...))
	q.size += len(data)
	if !q.started {
		q.started = true
		go q.run(w, q.wake)
	}
	q.mu.Unlock()
	q.signal()
	return true
}

func (q *inputQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// run writes queued chunks to w, oldest first, until the queue is closed.
func (q *inputQueue) run(w io.Writer, wake <-chan struct{}) {
	for {
		q.mu.Lock()
		for len(q.chunks) == 0 && !q.closed {
			q.mu.Unlock()
			<-wake
			q.mu.Lock()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		chunk := q.chunks[0]
		q.chunks[0] = nil
		q.chunks = q.chunks[1:]
		q.mu.Unlock()

		_, _ = w.Write(chunk)

		q.mu.Lock()
		q.size -= len(chunk)
		q.mu.Unlock()
	}
}

// pending is the number of bytes not yet delivered.
func (q *inputQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}

// close drops whatever is still queued and stops the writer goroutine once
// its current write (if any) returns; closing the pty is what unblocks that.
func (q *inputQueue) close() {
	q.mu.Lock()
	q.closed = true
	for _, c := range q.chunks {
		q.size -= len(c)
	}
	q.chunks = nil
	started := q.started
	q.mu.Unlock()
	if started {
		q.signal()
	}
}
