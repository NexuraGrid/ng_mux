package server

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// --- test helpers ---

func paneCopyActive(s *session, id layout.PaneID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.windows[s.cur].panes[id]
	return p != nil && p.copy != nil
}

// otherPane returns a pane id in the current window other than active, or 0.
func otherPane(s *session, active layout.PaneID) layout.PaneID {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.windows[s.cur].panes {
		if id != active {
			return id
		}
	}
	return 0
}

func rectOfID(s *session, id layout.PaneID) layout.Rect {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[s.cur]
	return layout.Compute(w.tree, w.outer(s.cols, s.contentRows()))[id]
}

// --- wheel: forwarded to an app that asked for mouse reports ---

func TestWheelForwardedAsSGRWhenAppWantsMouseReports(t *testing.T) {
	srv, ff, sess := setupSession(t)
	_ = srv
	id := activePane(sess)
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})
	rect := activeRect(sess)

	if repaint := sess.mouse(protocol.MouseWheelUp, rect.X, rect.Y, 0); !repaint {
		t.Fatal("wheel should still request a repaint (focus can move)")
	}

	want := "\x1b[<64;1;1M"
	if got := ff.byID(id).pt.sentToChild(); got != want {
		t.Fatalf("sentToChild = %q, want %q", got, want)
	}
	if paneCopyActive(sess, id) {
		t.Fatal("a wheel forwarded to the app must not also enter copy-mode")
	}
}

func TestWheelForwardedAsX10WithoutSGR(t *testing.T) {
	srv, ff, sess := setupSession(t)
	_ = srv
	id := activePane(sess)
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true}) // no MouseSGR
	rect := activeRect(sess)

	sess.mouse(protocol.MouseWheelDown, rect.X, rect.Y, 0)

	want := string([]byte{0x1b, '[', 'M', byte(32 + 65), byte(32 + 1), byte(32 + 1)})
	if got := ff.byID(id).pt.sentToChild(); got != want {
		t.Fatalf("sentToChild = %q, want %q", got, want)
	}
}

// --- drag: gated by tracking mode ---

func TestDragForwardingRespectsTrackingMode(t *testing.T) {
	tests := []struct {
		name          string
		modes         vterm.InputModes
		wantForwarded bool
	}{
		{"button mode: no drag", vterm.InputModes{MouseButton: true, MouseSGR: true}, false},
		{"motion mode: drag forwarded", vterm.InputModes{MouseMotion: true, MouseSGR: true}, true},
		{"any mode: drag forwarded", vterm.InputModes{MouseAny: true, MouseSGR: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, ff, sess := setupSession(t)
			_ = srv
			id := activePane(sess)
			ff.byID(id).scr.setInputModes(tt.modes)
			rect := activeRect(sess)

			sess.mouse(protocol.MousePress, rect.X, rect.Y, 0)
			sess.mouse(protocol.MouseDrag, rect.X+2, rect.Y+1, 0)

			sent := ff.byID(id).pt.sentToChild()
			hasDrag := strings.Contains(sent, "<32;")
			if hasDrag != tt.wantForwarded {
				t.Fatalf("drag forwarded = %v, want %v (sent=%q)", hasDrag, tt.wantForwarded, sent)
			}
		})
	}
}

func TestReleaseForwardedAfterForwardedPress(t *testing.T) {
	srv, ff, sess := setupSession(t)
	_ = srv
	id := activePane(sess)
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})
	rect := activeRect(sess)

	sess.mouse(protocol.MousePress, rect.X, rect.Y, 1)
	sess.mouse(protocol.MouseRelease, rect.X, rect.Y, 1)

	sent := ff.byID(id).pt.sentToChild()
	if !strings.Contains(sent, "\x1b[<1;1;1m") {
		t.Fatalf("sentToChild = %q, want it to contain a release report", sent)
	}
}

// --- alternate screen: arrow keys, no copy-mode ---

func TestWheelOnAltScreenSendsArrowKeys(t *testing.T) {
	tests := []struct {
		name      string
		appCursor bool
		want      string
	}{
		{"normal cursor keys", false, strings.Repeat("\x1b[A", wheelStep)},
		{"application cursor keys", true, strings.Repeat("\x1bOA", wheelStep)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, ff, sess := setupSession(t)
			_ = srv
			id := activePane(sess)
			ff.byID(id).scr.setInputModes(vterm.InputModes{AltScreen: true, AppCursor: tt.appCursor})
			rect := activeRect(sess)

			sess.mouse(protocol.MouseWheelUp, rect.X, rect.Y, 0)

			if got := ff.byID(id).pt.sentToChild(); got != tt.want {
				t.Fatalf("sentToChild = %q, want %q", got, tt.want)
			}
			if paneCopyActive(sess, id) {
				t.Fatal("alt-screen wheel scrolling must not enter copy-mode")
			}
		})
	}
}

// --- plain shell: unchanged copy-mode behaviour ---

func TestWheelOnPlainShellEntersCopyMode(t *testing.T) {
	_, ff, sess := setupSession(t)
	id := activePane(sess)
	ff.byID(id).scr.setHistoryLen(50)
	rect := activeRect(sess)

	sess.mouse(protocol.MouseWheelUp, rect.X, rect.Y, 0)

	if !paneCopyActive(sess, id) {
		t.Fatal("wheel-up on a plain shell (no mouse mode, no alt screen) should enter copy-mode")
	}
	if sent := ff.byID(id).pt.sentToChild(); sent != "" {
		t.Fatalf("nothing should be written to the pty, got %q", sent)
	}
}

// --- border drag: unaffected by the routing rewrite ---

func TestBorderDragStillResizesPanes(t *testing.T) {
	srv, _, sess := setupSession(t)
	exec(t, srv, "split-window -h")

	sess.mu.Lock()
	w := sess.windows[sess.cur]
	rects := layout.Compute(w.tree, w.outer(sess.cols, sess.contentRows()))
	var leftID layout.PaneID
	for id, r := range rects {
		if r.X == 0 {
			leftID = id
		}
	}
	before := rects[leftID]
	sess.mu.Unlock()

	dividerX := before.X + before.W
	sess.mouse(protocol.MousePress, dividerX, before.Y, 0)
	sess.mouse(protocol.MouseDrag, dividerX+3, before.Y, 0)

	after := rectOfID(sess, leftID)
	if after.W <= before.W {
		t.Fatalf("left pane width = %d after drag, want > %d (border drag should still resize)", after.W, before.W)
	}
}

// --- press: focus-follows plus forwarding ---

func TestPressOnOtherPaneFocusesAndForwardsPress(t *testing.T) {
	srv, ff, sess := setupSession(t)
	exec(t, srv, "split-window -h")

	active := activePane(sess) // the new (right) pane
	left := otherPane(sess, active)
	if left == 0 {
		t.Fatal("expected a second pane")
	}
	ff.byID(left).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	rect := rectOfID(sess, left)
	px, py := rect.X+1, rect.Y+1

	if repaint := sess.mouse(protocol.MousePress, px, py, 0); !repaint {
		t.Fatal("a press that changes focus should request a repaint")
	}
	if got := activePane(sess); got != left {
		t.Fatalf("active pane = %d, want %d (focus should follow the press)", got, left)
	}
	want := "\x1b[<0;2;2M"
	if got := ff.byID(left).pt.sentToChild(); got != want {
		t.Fatalf("left pane pty = %q, want %q (the focusing press should still be forwarded)", got, want)
	}
}

// --- pane-local coordinates ---

func TestPaneLocalCoordinatesForSplitPane(t *testing.T) {
	srv, ff, sess := setupSession(t)
	exec(t, srv, "split-window -h")

	id := activePane(sess) // the new (right, non-origin) pane
	rect := activeRect(sess)
	if rect.X == 0 {
		t.Fatalf("expected the split-created pane to be offset from the origin, rect=%+v", rect)
	}
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	px, py := rect.X+2, rect.Y+1
	sess.mouse(protocol.MousePress, px, py, 0)

	want := fmt.Sprintf("\x1b[<0;%d;%dM", 2+1, 1+1) // pane-local (2,1), 1-based on the wire
	if got := ff.byID(id).pt.sentToChild(); got != want {
		t.Fatalf("sentToChild = %q, want %q", got, want)
	}
}

func TestPaneLocalCoordinatesForZoomedPane(t *testing.T) {
	srv, ff, sess := setupSession(t)
	exec(t, srv, "split-window -h")
	exec(t, srv, "resize-pane -Z") // zooms the active (right) pane over the whole content area

	id := activePane(sess)
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	// This point would fall inside the LEFT pane's rect in the tiled layout;
	// while zoomed it must map to the zoomed pane at these same coordinates.
	px, py := 1, 1
	sess.mouse(protocol.MousePress, px, py, 0)

	want := fmt.Sprintf("\x1b[<0;%d;%dM", px+1, py+1)
	if got := ff.byID(id).pt.sentToChild(); got != want {
		t.Fatalf("sentToChild = %q, want %q (zoomed pane should receive unshifted local coordinates)", got, want)
	}
}

// --- the pty write happens outside the session lock ---

// blockingPty is a pty fake whose Write blocks until the test releases it, to
// prove the mouse-forwarding write happens after session.mu is released (see
// routeMouse's doc comment). It mirrors blockingScreen's role for
// TestSessionDirtyDoesNotBlockOnBusyPaneScreen, but for the pty side.
type blockingPty struct {
	started atomic.Bool
	release chan struct{}
}

func newBlockingPty() *blockingPty { return &blockingPty{release: make(chan struct{})} }

func (p *blockingPty) Write(b []byte) (int, error) {
	p.started.Store(true)
	<-p.release
	return len(b), nil
}

func (p *blockingPty) Read([]byte) (int, error) {
	<-p.release
	return 0, io.EOF
}

func (p *blockingPty) Close() error                { return nil }
func (p *blockingPty) Resize(cols, rows int) error { return nil }
func (p *blockingPty) attempted() bool             { return p.started.Load() }

func TestMouseForwardWritesOutsideSessionLock(t *testing.T) {
	// The pane's pty must be blockingPty from construction: pump() reads
	// p.pt on every loop iteration with no locking of its own (by design —
	// see pane.pump), so swapping it in after the pump goroutine is already
	// running would itself be a data race, independent of anything this test
	// is trying to prove about session.mu.
	bp := newBlockingPty()
	scr := newFakeScreen(80, 23)
	scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	srv := newServer(ipc.Endpoint{Name: "test"}, 80, 24, nil, sessionOpts{
		historyLimit: 100,
		newPane: func(id layout.PaneID, cols, rows int, shell string, hist int) (*pane, error) {
			return &pane{id: id, pt: bp, vt: scr}, nil
		},
	})
	t.Cleanup(srv.shutdownAll)

	sess, err := srv.getOrCreateSession("0")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	rect := activeRect(sess)

	writeDone := make(chan struct{})
	go func() {
		sess.mouse(protocol.MousePress, rect.X, rect.Y, 0)
		close(writeDone)
	}()
	waitFor(t, bp.attempted, time.Second)

	// dirty() and info() only ever need s.mu, never a pane's pty: with the
	// write correctly moved outside the lock, both must return promptly even
	// while that write is stuck on bp.release. (A second call that itself
	// wrote to the same busy pty would block on the pty, not the lock, and
	// would prove nothing new here.)
	dirtyDone := make(chan bool, 1)
	go func() { dirtyDone <- sess.dirty() }()
	select {
	case <-dirtyDone:
	case <-time.After(time.Second):
		t.Fatal("session.dirty() blocked while a pane's pty Write was stuck")
	}

	infoDone := make(chan struct{})
	go func() {
		sess.info(true)
		close(infoDone)
	}()
	select {
	case <-infoDone:
	case <-time.After(time.Second):
		t.Fatal("session.info() blocked while a pane's pty Write was stuck")
	}

	close(bp.release)
	<-writeDone
}
