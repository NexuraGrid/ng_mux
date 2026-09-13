package server

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/render"
)

const (
	// ctrlQueueSize bounds queued control messages (exec replies, clipboard,
	// bye). They are small and rare, so this only fills if the client stopped
	// reading entirely.
	ctrlQueueSize = 64
	// sendTimeout is how long send waits for room in a full control queue
	// before giving up on a stuck client.
	sendTimeout = 2 * time.Second
	// flushTimeout bounds how long a detaching client waits for queued control
	// messages (the bye) to reach the wire before its connection is closed.
	flushTimeout = 2 * time.Second
)

// client is the server's view of one attached client: a control-message queue
// and a single-slot frame mailbox, both drained by one writer goroutine.
//
// Frames are never queued behind each other. The broadcaster paints a diff
// only when the writer is idle, against a private copy of the last frame this
// client was handed (shown). When the writer is still busy it marks the client
// stale instead, and a later tick paints one diff from shown to whatever is
// current. A slow client therefore receives fewer, larger diffs instead of
// overflowing a queue and falling into repeated full repaints.
type client struct {
	pc *protocol.Conn

	ctrl       chan protocol.Message
	frameReady chan struct{} // capacity 1: a frame is waiting in pending

	closeOnce  sync.Once
	closed     chan struct{}
	drainOnce  sync.Once
	draining   chan struct{} // closed to ask the writer to flush ctrl and exit
	writerDone chan struct{}
	started    atomic.Bool

	mu       sync.Mutex
	pending  []byte       // painted frame bytes not yet taken by the writer
	busy     bool         // the writer is putting a frame on the wire
	shown    render.Frame // copy of the last frame handed to the writer
	hasShown bool         // false forces the next frame to be a full repaint
	stale    bool         // a frame was skipped while the writer was busy
	sess     string       // name of the session this client is currently viewing
	cols     int          // last terminal size this client reported
	rows     int
}

func newClient(pc *protocol.Conn, session string) *client {
	return &client{
		pc:         pc,
		ctrl:       make(chan protocol.Message, ctrlQueueSize),
		frameReady: make(chan struct{}, 1),
		closed:     make(chan struct{}),
		draining:   make(chan struct{}),
		writerDone: make(chan struct{}),
		sess:       session,
	}
}

// session returns the name of the session this client is viewing.
func (c *client) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

// setSession points the client at a different session and forces a repaint.
func (c *client) setSession(name string) {
	c.mu.Lock()
	c.sess = name
	c.hasShown = false
	c.mu.Unlock()
}

// setSize records the client's last reported terminal size.
func (c *client) setSize(cols, rows int) {
	c.mu.Lock()
	c.cols, c.rows = cols, rows
	c.mu.Unlock()
}

// size returns the client's last reported terminal size (0,0 if unknown).
func (c *client) size() (cols, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cols, c.rows
}

// send queues a control message and reports whether it was accepted. It waits
// up to sendTimeout for room, so a burst of replies is not lost, but a client
// that stopped reading cannot stall the caller forever.
func (c *client) send(m protocol.Message) bool {
	select {
	case c.ctrl <- m:
		return true
	case <-c.closed:
		return false
	default:
	}
	t := time.NewTimer(sendTimeout)
	defer t.Stop()
	select {
	case c.ctrl <- m:
		return true
	case <-c.closed:
		return false
	case <-t.C:
		return false
	}
}

// needsFrame reports whether this client must be sent a frame even if its
// session did not change: it has nothing on screen yet (attach, reset, resize)
// or a frame was skipped while its writer was busy.
func (c *client) needsFrame() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.hasShown || c.stale
}

// offerFrame hands frame to this client. If the writer is idle it paints the
// diff from the last frame the client was handed and puts it in the mailbox;
// otherwise it marks the client stale and sends nothing. frame may be one of a
// session's reusable buffers: it is copied, never retained. Only the broadcast
// goroutine calls it.
func (c *client) offerFrame(frame *render.Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil || c.busy {
		c.stale = true
		return
	}
	var prev *render.Frame
	if c.hasShown {
		prev = &c.shown
	}
	data := render.Paint(prev, frame)
	c.shown.CopyFrom(frame)
	c.hasShown = true
	c.stale = false
	if len(data) == 0 {
		return
	}
	c.pending = data
	select {
	case c.frameReady <- struct{}{}:
	default:
	}
}

// takeFrame moves the pending frame to the writer and marks it busy.
func (c *client) takeFrame() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := c.pending
	c.pending = nil
	c.busy = data != nil
	return data
}

func (c *client) frameWritten() {
	c.mu.Lock()
	c.busy = false
	c.mu.Unlock()
}

// writeLoop is the only goroutine that writes to the wire for this client.
// Control messages go first so a reply or a bye is never stuck behind a large
// frame that has not been painted yet.
func (c *client) writeLoop() {
	c.started.Store(true)
	defer close(c.writerDone)
	for {
		select {
		case m := <-c.ctrl:
			if !c.write(m) {
				return
			}
			continue
		default:
		}
		select {
		case <-c.closed:
			return
		case <-c.draining:
			c.flushCtrl()
			return
		case m := <-c.ctrl:
			if !c.write(m) {
				return
			}
		case <-c.frameReady:
			data := c.takeFrame()
			if data == nil {
				continue
			}
			ok := c.write(protocol.Message{Type: protocol.TypeFrame, Data: data})
			c.frameWritten()
			if !ok {
				return
			}
		}
	}
}

func (c *client) write(m protocol.Message) bool {
	if err := c.pc.Write(m); err != nil {
		c.close()
		return false
	}
	return true
}

// flushCtrl writes every control message already queued, then stops.
func (c *client) flushCtrl() {
	for {
		select {
		case m := <-c.ctrl:
			if !c.write(m) {
				return
			}
		default:
			return
		}
	}
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.pc.Close()
	})
}

// closeAfterFlush lets the writer put already-queued control messages (such
// as the bye a detach just sent) on the wire, waiting at most d, then closes
// the connection.
func (c *client) closeAfterFlush(d time.Duration) {
	if c.started.Load() {
		c.drainOnce.Do(func() { close(c.draining) })
		t := time.NewTimer(d)
		select {
		case <-c.writerDone:
		case <-t.C:
		}
		t.Stop()
	}
	c.close()
}

// reset forces the next frame sent to this client to be a full repaint.
func (c *client) reset() {
	c.mu.Lock()
	c.hasShown = false
	c.mu.Unlock()
}
