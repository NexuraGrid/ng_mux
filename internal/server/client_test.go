package server

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/render"
)

// pipeClient returns a client whose writer is running over an in-memory pipe,
// plus the peer end. net.Pipe is unbuffered, so the writer blocks until the
// test reads: that is how these tests simulate a slow or stalled client.
func pipeClient(t *testing.T, session string) (*client, *protocol.Conn) {
	t.Helper()
	srvSide, cliSide := net.Pipe()
	c := newClient(protocol.NewConn(srvSide), session)
	go c.writeLoop()
	t.Cleanup(func() {
		c.close()
		cliSide.Close()
	})
	return c, protocol.NewConn(cliSide)
}

// textFrame is a cols x rows frame whose first cells spell s.
func textFrame(cols, rows int, s string) *render.Frame {
	f := render.NewFrame(cols, rows)
	i := 0
	for _, r := range s {
		f.Cells[i].Ch = r
		i++
	}
	return f
}

// writerBusy reports whether a painted frame is waiting for or held by the
// writer.
func (c *client) writerBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy || c.pending != nil
}

// writerHolding reports whether the writer has taken a frame and is putting it
// on the wire (as opposed to a frame merely waiting in the mailbox).
func (c *client) writerHolding() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy
}

// collector reads messages from peer until the connection ends.
type collector struct {
	mu   sync.Mutex
	msgs []protocol.Message
	done chan struct{}
}

func collect(peer *protocol.Conn, delay time.Duration) *collector {
	col := &collector{done: make(chan struct{})}
	go func() {
		defer close(col.done)
		for {
			m, err := peer.Read()
			if err != nil {
				return
			}
			col.mu.Lock()
			col.msgs = append(col.msgs, m)
			col.mu.Unlock()
			time.Sleep(delay)
		}
	}()
	return col
}

func (col *collector) snapshot() []protocol.Message {
	col.mu.Lock()
	defer col.mu.Unlock()
	return append([]protocol.Message(nil), col.msgs...)
}

func (col *collector) count(typ protocol.Type) int {
	n := 0
	for _, m := range col.snapshot() {
		if m.Type == typ {
			n++
		}
	}
	return n
}

func isFullRepaint(m protocol.Message) bool {
	return m.Type == protocol.TypeFrame && bytes.Contains(m.Data, []byte("\x1b[2J"))
}

func TestOfferFrameWhileWriterBusyMarksStale(t *testing.T) {
	c, peer := pipeClient(t, "0")

	first := textFrame(10, 2, "aaaa")
	c.offerFrame(first)
	waitFor(t, c.writerHolding, time.Second) // nobody reads: the writer is stuck on it

	c.offerFrame(textFrame(10, 2, "bbbb"))
	latest := textFrame(10, 2, "cccc")
	c.offerFrame(latest)

	c.mu.Lock()
	queued := c.pending != nil
	c.mu.Unlock()
	if queued {
		t.Fatal("a second frame was queued behind the one still being written")
	}
	if !c.needsFrame() {
		t.Fatal("a client skipped while busy must ask for a later frame")
	}

	col := collect(peer, 0)
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) == 1 && !c.writerBusy() }, time.Second)

	c.offerFrame(latest)
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) == 2 }, time.Second)

	msgs := col.snapshot()
	if !isFullRepaint(msgs[0]) {
		t.Fatal("the first frame should be a full repaint")
	}
	if want := render.Paint(first, latest); !bytes.Equal(msgs[1].Data, want) {
		t.Fatalf("second frame = %q, want the diff from the frame actually shown: %q", msgs[1].Data, want)
	}
	if c.needsFrame() {
		t.Fatal("client still stale after receiving the latest frame")
	}
}

// TestSlowClientGetsNoRepeatedFullRepaints is the regression test for the
// drop-and-repaint livelock: a client that reads slowly while its session
// changes every tick must keep receiving diffs, not full repaints.
func TestSlowClientGetsNoRepeatedFullRepaints(t *testing.T) {
	c, peer := pipeClient(t, "0")
	col := collect(peer, 3*time.Millisecond)

	for i := 0; i < 60; i++ {
		c.offerFrame(textFrame(80, 24, fmt.Sprintf("tick %d", i)))
		time.Sleep(time.Millisecond)
	}
	waitFor(t, func() bool { return !c.writerBusy() }, 2*time.Second)
	c.offerFrame(textFrame(80, 24, "final"))
	waitFor(t, func() bool { return !c.writerBusy() }, 2*time.Second)

	msgs := col.snapshot()
	full := 0
	for _, m := range msgs {
		if isFullRepaint(m) {
			full++
		}
	}
	if full != 1 {
		t.Fatalf("slow client got %d full repaints across %d frames, want exactly 1 (the first)", full, len(msgs))
	}
	if len(msgs) < 2 {
		t.Fatalf("slow client got %d frames, want the first plus later diffs", len(msgs))
	}
}

func TestControlRepliesNotDroppedWhileFrameBusy(t *testing.T) {
	c, peer := pipeClient(t, "0")
	c.offerFrame(textFrame(80, 24, "big"))
	waitFor(t, c.writerHolding, time.Second)

	const replies = 20
	for i := 0; i < replies; i++ {
		if !c.send(protocol.Message{Type: protocol.TypeExecReply, Name: fmt.Sprint(i)}) {
			t.Fatalf("reply %d was rejected while a frame was being written", i)
		}
	}

	col := collect(peer, 0)
	waitFor(t, func() bool { return col.count(protocol.TypeExecReply) == replies }, 2*time.Second)
}

func TestDetachFlushesByeBeforeClosing(t *testing.T) {
	c, peer := pipeClient(t, "0")
	c.offerFrame(textFrame(80, 24, "frame on the wire"))
	waitFor(t, c.writerHolding, time.Second)

	if !c.send(protocol.Message{Type: protocol.TypeBye}) {
		t.Fatal("bye was rejected")
	}
	closed := make(chan struct{})
	go func() {
		c.closeAfterFlush(flushTimeout)
		close(closed)
	}()

	col := collect(peer, 0)
	select {
	case <-col.done:
	case <-time.After(3 * time.Second):
		t.Fatal("connection was never closed")
	}
	<-closed

	msgs := col.snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Type != protocol.TypeBye {
		t.Fatalf("messages before close = %v, want the bye last", msgs)
	}
}

// TestTickRepaintsStaleClientWhenSessionIdle checks the broadcaster side: a
// client skipped while busy is repainted on a later tick even though its
// session has produced nothing new since.
func TestTickRepaintsStaleClientWhenSessionIdle(t *testing.T) {
	srv, _, sess := setupSession(t)
	c, peer := pipeClient(t, sess.name)
	srv.addClient(c)

	srv.tick()
	waitFor(t, c.writerHolding, time.Second)

	// A visible change (the window name in the status bar) that does not reset
	// the client, so the next tick must take the stale path.
	sess.mu.Lock()
	sess.windows[sess.cur].name = "renamed"
	sess.needsRepaint = true
	sess.mu.Unlock()
	srv.tick()
	if sess.dirty() {
		t.Fatal("setup: the session should be clean after the second tick")
	}
	if !c.needsFrame() {
		t.Fatal("setup: the client should be stale after a tick while busy")
	}

	col := collect(peer, 0)
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) == 1 && !c.writerBusy() }, time.Second)

	srv.tick()
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) == 2 }, time.Second)
}
