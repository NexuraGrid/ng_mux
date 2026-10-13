package server

import (
	"net"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// attachCollect attaches a client to srv over an in-memory pipe and records
// every message the server sends it. It returns once the first frame, the full
// repaint every attach starts with, has arrived.
func attachCollect(t *testing.T, srv *Server, session string) (*protocol.Conn, *collector) {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() { srvConn.Close(); cliConn.Close() })
	go srv.serveClient(protocol.NewConn(srvConn),
		protocol.Message{Type: protocol.TypeAttach, Name: session, Cols: 80, Rows: 24})
	cli := protocol.NewConn(cliConn)
	col := collect(cli, 0)
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.clients) == 1
	}, 2*time.Second)
	settle(srv)
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) >= 1 }, 2*time.Second)
	if first := col.snapshot()[0]; !isFullRepaint(first) {
		t.Fatalf("first message = %v, want a full-repaint frame", first.Type)
	}
	return cli, col
}

// settle runs a few broadcaster ticks, pausing between them so the client's
// writer drains, so every repaint the messages handled so far asked for has
// been sent. Tests drive ticks by hand: newTestServer starts no broadcaster.
func settle(srv *Server) {
	for range 3 {
		srv.tick()
		time.Sleep(frameInterval)
	}
}

// framesAfter waits until more than n frames have arrived and returns those
// past the first n.
func framesAfter(t *testing.T, col *collector, n int) []protocol.Message {
	t.Helper()
	waitFor(t, func() bool { return col.count(protocol.TypeFrame) > n }, 2*time.Second)
	var frames []protocol.Message
	for _, m := range col.snapshot() {
		if m.Type == protocol.TypeFrame {
			frames = append(frames, m)
		}
	}
	return frames[n:]
}

// A mouse drag selection repaints on every motion. Those repaints must be
// diffs against what the client already shows: a clear-screen plus full redraw
// per motion is what made selecting text flicker.
func TestMouseDragRepaintIsADiff(t *testing.T) {
	srv, ff, sess := setupSession(t)
	left, _ := splitLeftRight(t, srv, ff, sess)
	lr := rectOfID(sess, left)
	cli, col := attachCollect(t, srv, sess.name)

	n := col.count(protocol.TypeFrame)
	writeMsg(t, cli, protocol.Message{Type: protocol.TypeMouse, Name: protocol.MousePress, MX: lr.X + 1, MY: lr.Y})
	for i := 2; i < 6; i++ {
		writeMsg(t, cli, protocol.Message{Type: protocol.TypeMouse, Name: protocol.MouseDrag, MX: lr.X + i, MY: lr.Y + 1})
	}
	waitFor(t, func() bool { return paneCopyActive(sess, left) }, 2*time.Second)
	settle(srv) // send every repaint the drag asked for
	for _, f := range framesAfter(t, col, n) {
		if isFullRepaint(f) {
			t.Fatalf("a drag repaint cleared the screen: %q", f.Data)
		}
	}
}

// Refresh and resize still force a full repaint: the client asks for them when
// its screen no longer matches the last frame (a local popup was drawn over it,
// or the terminal changed size).
func TestRefreshAndResizeRepaintFully(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  protocol.Message
	}{
		{"refresh", protocol.Message{Type: protocol.TypeRefresh}},
		{"resize", protocol.Message{Type: protocol.TypeResize, Cols: 70, Rows: 20}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, sess := setupSession(t)
			cli, col := attachCollect(t, srv, sess.name)

			n := col.count(protocol.TypeFrame)
			writeMsg(t, cli, tt.msg)
			full := false
			waitFor(t, func() bool {
				settle(srv)
				for _, f := range framesAfter(t, col, n) {
					full = full || isFullRepaint(f)
				}
				return full
			}, 2*time.Second)
		})
	}
}
