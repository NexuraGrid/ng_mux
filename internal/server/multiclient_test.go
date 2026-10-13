package server

import (
	"net"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// attachPipe attaches a client to srv over an in-memory pipe at the given size
// and drains everything the server sends, so its writer never stalls. Closing
// the returned conn disconnects the client.
func attachPipe(t *testing.T, srv *Server, session string, cols, rows int) *protocol.Conn {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() { srvConn.Close(); cliConn.Close() })
	go srv.serveClient(protocol.NewConn(srvConn),
		protocol.Message{Type: protocol.TypeAttach, Name: session, Cols: cols, Rows: rows})
	cli := protocol.NewConn(cliConn)
	go func() {
		for {
			if _, err := cli.Read(); err != nil {
				return
			}
		}
	}()
	return cli
}

func sessSize(s *session) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func waitSessSize(t *testing.T, s *session, cols, rows int) {
	t.Helper()
	waitFor(t, func() bool {
		c, r := sessSize(s)
		return c == cols && r == rows
	}, 2*time.Second)
}

// TestSessionSizeIsSmallestAttachedClient: two clients of different sizes on
// one session must not fight over it. The session takes the smallest width and
// height among its viewers, so the frame fits in every terminal, and it grows
// back once the smaller client leaves.
func TestSessionSizeIsSmallestAttachedClient(t *testing.T) {
	srv, _, sess := setupSession(t)

	attachPipe(t, srv, "0", 120, 40)
	waitSessSize(t, sess, 120, 40)

	small := attachPipe(t, srv, "0", 80, 24)
	waitSessSize(t, sess, 80, 24)

	// The big client reporting a resize (or a refresh) must not grow the
	// session past the small client.
	writeMsg(t, small, protocol.Message{Type: protocol.TypeResize, Cols: 100, Rows: 50})
	waitSessSize(t, sess, 100, 40)

	small.Close()
	waitSessSize(t, sess, 120, 40)
}

// TestSessionSizePerDimension: the minimum is taken per dimension, so a wide
// short terminal and a narrow tall one yield narrow and short.
func TestSessionSizePerDimension(t *testing.T) {
	srv, _, sess := setupSession(t)
	attachPipe(t, srv, "0", 200, 20)
	attachPipe(t, srv, "0", 90, 60)
	waitSessSize(t, sess, 90, 20)
}

// TestRefreshReappliesClientSize: a refresh recomputes the session size from
// the attached clients, undoing any drift.
func TestRefreshReappliesClientSize(t *testing.T) {
	srv, _, sess := setupSession(t)
	cli := attachPipe(t, srv, "0", 100, 30)
	waitSessSize(t, sess, 100, 30)

	sess.resize(60, 10) // something else shrank it
	writeMsg(t, cli, protocol.Message{Type: protocol.TypeRefresh})
	waitSessSize(t, sess, 100, 30)
}

// TestMouseStateIsPerClient: a border drag started by one client must not be
// continued (or cancelled) by another client's events.
func TestMouseStateIsPerClient(t *testing.T) {
	_, _, sess := setupSession(t)
	var a, b mouseState
	a.drag = dragState{active: true}
	sess.mouseAs(&b, protocol.MouseRelease, 1, 1, 0)
	if !a.drag.active {
		t.Fatal("client b's release cancelled client a's drag")
	}
}
