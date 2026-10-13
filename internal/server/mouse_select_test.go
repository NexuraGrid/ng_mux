package server

import (
	"bytes"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// splitLeftRight splits the session's window left/right and fills the left
// pane with 'L' and the right one with 'R'. It returns both pane ids.
func splitLeftRight(t *testing.T, srv *Server, ff *fakeFleet, sess *session) (left, right layout.PaneID) {
	t.Helper()
	first := activePane(sess)
	exec(t, srv, "split-window -h")
	second := otherPane(sess, first)
	if rectOfID(sess, first).X < rectOfID(sess, second).X {
		left, right = first, second
	} else {
		left, right = second, first
	}
	ff.byID(left).scr.fillCh = 'L'
	ff.byID(right).scr.fillCh = 'R'
	return left, right
}

func pasteBuffer(s *session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pasteBuf
}

// A plain drag in a shell pane selects text inside that pane only: dragging
// past the divider into the neighbour is clamped to the pane's edge. The
// release copies the selection and leaves no mode behind.
func TestDragSelectsOnlyThePaneItStartedIn(t *testing.T) {
	srv, ff, sess := setupSession(t)
	left, _ := splitLeftRight(t, srv, ff, sess)
	lr := rectOfID(sess, left)

	var ms mouseState
	sess.mouseAs(&ms, protocol.MousePress, lr.X+2, lr.Y, 0)
	sess.mouseAs(&ms, protocol.MouseDrag, lr.X+lr.W+5, lr.Y+1, 0) // well into the right pane
	if !paneCopyActive(sess, left) {
		t.Fatal("dragging should show a selection (copy-mode) on the left pane")
	}
	_, yank := sess.mouseAs(&ms, protocol.MouseRelease, lr.X+lr.W+5, lr.Y+1, 0)

	want := strings.Repeat("L", lr.W-2) + "\n" + strings.Repeat("L", lr.W)
	if yank != want {
		t.Fatalf("yank = %q, want %q", yank, want)
	}
	if strings.Contains(yank, "R") {
		t.Fatal("selection leaked into the right pane")
	}
	if got := pasteBuffer(sess); got != want {
		t.Fatalf("paste buffer = %q, want %q", got, want)
	}
	if paneCopyActive(sess, left) {
		t.Fatal("the release should leave the selection mode")
	}
}

// A click without a drag must not copy anything or leave a mode behind.
func TestClickWithoutDragCopiesNothing(t *testing.T) {
	srv, ff, sess := setupSession(t)
	left, _ := splitLeftRight(t, srv, ff, sess)
	lr := rectOfID(sess, left)

	var ms mouseState
	sess.mouseAs(&ms, protocol.MousePress, lr.X+1, lr.Y+1, 0)
	_, yank := sess.mouseAs(&ms, protocol.MouseRelease, lr.X+1, lr.Y+1, 0)
	if yank != "" || pasteBuffer(sess) != "" {
		t.Fatalf("a click copied %q (paste buffer %q)", yank, pasteBuffer(sess))
	}
	if paneCopyActive(sess, left) {
		t.Fatal("a click left the pane in copy-mode")
	}
	if activePane(sess) != left {
		t.Fatal("a click should still focus the pane")
	}
}

// An app that asked for mouse reports gets the drag; ngmux must not select.
func TestDragOnMouseTrackingAppDoesNotSelect(t *testing.T) {
	_, ff, sess := setupSession(t)
	id := activePane(sess)
	ff.byID(id).scr.fillCh = 'x'
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseAny: true, MouseSGR: true})
	rect := activeRect(sess)

	var ms mouseState
	sess.mouseAs(&ms, protocol.MousePress, rect.X, rect.Y, 0)
	sess.mouseAs(&ms, protocol.MouseDrag, rect.X+3, rect.Y+1, 0)
	_, yank := sess.mouseAs(&ms, protocol.MouseRelease, rect.X+3, rect.Y+1, 0)
	if yank != "" || paneCopyActive(sess, id) {
		t.Fatalf("selected over a mouse-tracking app: yank=%q copy=%v", yank, paneCopyActive(sess, id))
	}
}

// End to end: a drag from an attached client puts the text on the client's
// clipboard with OSC 52.
func TestDragSendsClipboardToClient(t *testing.T) {
	srv, ff, sess := setupSession(t)
	id := activePane(sess)
	ff.byID(id).scr.fillCh = 'z'

	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() { srvConn.Close(); cliConn.Close() })
	go srv.serveClient(protocol.NewConn(srvConn),
		protocol.Message{Type: protocol.TypeAttach, Name: "0", Cols: 80, Rows: 24})
	cli := protocol.NewConn(cliConn)
	rect := activeRect(sess)

	writeMsg(t, cli, protocol.Message{Type: protocol.TypeMouse, Name: protocol.MousePress, MX: rect.X, MY: rect.Y})
	writeMsg(t, cli, protocol.Message{Type: protocol.TypeMouse, Name: protocol.MouseDrag, MX: rect.X + 2, MY: rect.Y})
	writeMsg(t, cli, protocol.Message{Type: protocol.TypeMouse, Name: protocol.MouseRelease, MX: rect.X + 2, MY: rect.Y})

	msg := readUntil(t, cli, protocol.TypeSetClipboard, 2*time.Second)
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("zzz")) + "\a"; !bytes.Equal(msg.Data, []byte(want)) {
		t.Fatalf("clipboard = %q, want %q", msg.Data, want)
	}
}

// The copy-mode selection anchor stays on the same text when the view scrolls
// (the cursor pushing past the top edge), instead of staying at the same
// screen row.
func TestCopySelectionAnchorSurvivesScrolling(t *testing.T) {
	cs := newCopyState(10, 5)
	cs.seen = 100
	cs.cx, cs.cy = 3, 0
	cs.key([]byte(" "), 50)      // anchor at the top row
	cs.key([]byte("\x1b[A"), 50) // cursor up past the top: the view scrolls one line
	if cs.offset != 1 {
		t.Fatalf("offset = %d, want 1", cs.offset)
	}
	sel := cs.selection()
	if sel == nil || sel.Y0 != 1 || sel.X0 != 3 || sel.Y1 != 0 {
		t.Fatalf("selection = %+v, want anchor (3,1) and cursor on row 0", sel)
	}
}

// A release that lands on the status row (or past the session's edge) still
// finishes the selection instead of leaving the pane stuck in copy-mode.
func TestDragReleasedOnStatusRowStillCopies(t *testing.T) {
	_, ff, sess := setupSession(t)
	id := activePane(sess)
	ff.byID(id).scr.fillCh = 'q'
	rect := activeRect(sess)
	_, rows := sessSize(sess)

	var ms mouseState
	sess.mouseAs(&ms, protocol.MousePress, rect.X, rect.Y+rect.H-1, 0)
	sess.mouseAs(&ms, protocol.MouseDrag, rect.X+2, rows-1, 0)
	_, yank := sess.mouseAs(&ms, protocol.MouseRelease, rect.X+2, rows-1, 0)
	if yank != "qqq" {
		t.Fatalf("yank = %q, want %q", yank, "qqq")
	}
	if paneCopyActive(sess, id) {
		t.Fatal("pane left in copy-mode")
	}
}
