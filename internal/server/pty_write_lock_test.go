package server

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/layout"
)

// stuckPty stands in for the pty of a child that never reads its stdin:
// every Write blocks until the test releases it or the pty is closed.
type stuckPty struct {
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
	writes  atomic.Int32
}

func newStuckPty() *stuckPty {
	return &stuckPty{release: make(chan struct{}), closed: make(chan struct{})}
}

func (b *stuckPty) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *stuckPty) Write(p []byte) (int, error) {
	b.writes.Add(1)
	select {
	case <-b.release:
	case <-b.closed:
	}
	return len(p), nil
}

func (b *stuckPty) Resize(int, int) error { return nil }

func (b *stuckPty) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestPtyWritingCommandsDoNotHoldSessionLock guards send-keys and paste-buffer:
// a pty write can block for as long as the child ignores its stdin, and doing
// it under the session lock would freeze that session's frames and input.
func TestPtyWritingCommandsDoNotHoldSessionLock(t *testing.T) {
	for _, line := range []string{`send-keys -t 0 "hello" Enter`, "paste-buffer"} {
		t.Run(line, func(t *testing.T) {
			bp := newStuckPty()
			srv := newServer(ipc.Endpoint{Name: "lock"}, 80, 24, nil, sessionOpts{
				historyLimit: 10,
				defaultShell: "/bin/fakesh",
				newPane: func(id layout.PaneID, cols, rows int, _ string, _ int) (*pane, error) {
					return &pane{id: id, pt: bp, vt: newFakeScreen(cols, rows)}, nil
				},
			})
			t.Cleanup(srv.shutdownAll)
			t.Cleanup(func() { close(bp.release) }) // runs first: unblock the write

			sess, err := srv.getOrCreateSession("0")
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			sess.mu.Lock()
			sess.pasteBuf = "pasted text"
			sess.mu.Unlock()

			go func() { _, _ = srv.execCommand(nil, line) }()
			waitFor(t, func() bool { return bp.writes.Load() == 1 }, time.Second)

			done := make(chan struct{})
			go func() {
				_ = sess.dirty()
				sess.frame()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("%s held the session lock while its pty write was blocked", line)
			}
		})
	}
}
