package server

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// attachReader attaches a client over a pipe and returns its connection,
// leaving reads to the test.
func attachReader(t *testing.T, srv *Server) *protocol.Conn {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() { srvConn.Close(); cliConn.Close() })
	go srv.serveClient(protocol.NewConn(srvConn),
		protocol.Message{Type: protocol.TypeAttach, Name: "0", Cols: 80, Rows: 24})
	return protocol.NewConn(cliConn)
}

// Text an app in a pane copies with OSC 52 reaches the real terminal's
// clipboard of every client viewing that session, and lands in the paste
// buffer like a copy-mode yank.
func TestInnerAppClipboardReachesViewers(t *testing.T) {
	srv, ff, sess := setupSession(t)
	cli := attachReader(t, srv)
	waitFor(t, func() bool { return srv.hasClients() }, time.Second)

	ff.byID(1).scr.setClipboard("from claude")
	go srv.tick()

	msg := readUntil(t, cli, protocol.TypeSetClipboard, 2*time.Second)
	if !bytes.Equal(msg.Data, osc52("from claude")) {
		t.Fatalf("clipboard message = %q, want %q", msg.Data, osc52("from claude"))
	}
	if got := pasteBuffer(sess); got != "from claude" {
		t.Fatalf("paste buffer = %q, want %q", got, "from claude")
	}
}

// With set-clipboard off, an app's OSC 52 is ignored entirely.
func TestInnerAppClipboardIgnoredWhenSetClipboardOff(t *testing.T) {
	srv, ff, sess := setupSession(t)
	srv.opts.setClipboard = false
	cli := attachReader(t, srv)
	waitFor(t, func() bool { return srv.hasClients() }, time.Second)

	ff.byID(1).scr.setClipboard("secret")
	got := make(chan protocol.Message, 16)
	go func() {
		for {
			m, err := cli.Read()
			if err != nil {
				return
			}
			got <- m
		}
	}()
	srv.tick()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case m := <-got:
			if m.Type == protocol.TypeSetClipboard {
				t.Fatal("received a clipboard message while set-clipboard is off")
			}
		case <-deadline:
			if buf := pasteBuffer(sess); buf != "" {
				t.Fatalf("paste buffer = %q, want it untouched", buf)
			}
			return
		}
	}
}
