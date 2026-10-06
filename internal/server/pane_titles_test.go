package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// setupTitledSession is setupSession with pane titles on (80x24 client, so a
// 23-row content area).
func setupTitledSession(t testing.TB) (*Server, *fakeFleet, *session) {
	t.Helper()
	srv, ff := newTestServerWith(t, func(o *sessionOpts) { o.paneTitles = true })
	sess, err := srv.getOrCreateSession("0")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return srv, ff, sess
}

func paneRects(s *session) map[layout.PaneID]layout.Rect {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[s.cur]
	return layout.Compute(w.tree, w.outer(s.cols, s.contentRows()))
}

// frameRow reads row y of a composed frame as a string.
func frameRow(f *render.Frame, y int) string {
	var b strings.Builder
	for x := 0; x < f.Cols; x++ {
		c := f.Cells[y*f.Cols+x]
		if c.Width == 0 && x > 0 {
			continue
		}
		b.WriteRune(c.Ch)
	}
	return b.String()
}

// A lone pane has no title line: it keeps the whole content area.
func TestPaneTitlesSinglePaneKeepsFullHeight(t *testing.T) {
	_, ff, sess := setupTitledSession(t)
	if c, r := ff.byID(1).pt.size(); c != 80 || r != 23 {
		t.Fatalf("single pane pty = %dx%d, want 80x23", c, r)
	}
	f := sess.frame()
	if strings.ContainsRune(frameRow(f, 0), '─') {
		t.Errorf("a single-pane window should draw no title line: %q", frameRow(f, 0))
	}
}

// Side-by-side panes both give up the window's top row for their titles; the
// emulator and pty get exactly the rows below it.
func TestPaneTitlesSideBySideGeometry(t *testing.T) {
	srv, ff, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h")

	for id, r := range paneRects(sess) {
		if r.Y != 1 || r.H != 22 {
			t.Errorf("pane %d rect = %+v, want Y=1 H=22 under a title row", id, r)
		}
		fp := ff.byID(id)
		if c, rows := fp.pt.size(); c != r.W || rows != 22 {
			t.Errorf("pane %d pty = %dx%d, want %dx22", id, c, rows, r.W)
		}
		if c, rows := fp.scr.size(); c != r.W || rows != 22 {
			t.Errorf("pane %d emulator = %dx%d, want %dx22", id, c, rows, r.W)
		}
	}

	// Back to one pane: the title row is given back.
	exec(t, srv, "kill-pane")
	waitFor(t, func() bool {
		c, r := ff.byID(1).pt.size()
		return c == 80 && r == 23
	}, 2*time.Second)
}

// A pane below another takes the divider row above it as its title line, so
// stacking panes costs no more rows than the divider already did.
func TestPaneTitlesStackedUseTheDividerRow(t *testing.T) {
	srv, _, sess := setupTitledSession(t)
	exec(t, srv, "split-window -v")
	rects := paneRects(sess)
	top, bottom := rects[1], rects[2]
	if top.Y != 1 {
		t.Fatalf("top pane rect = %+v, want Y=1", top)
	}
	if bottom.Y-1 != top.Y+top.H {
		t.Fatalf("bottom pane %+v should sit right under the divider after top %+v", bottom, top)
	}
	if got := top.H + 1 + bottom.H; got != 22 {
		t.Fatalf("top+divider+bottom = %d rows, want 22", got)
	}

	f := sess.frame()
	row := frameRow(f, bottom.Y-1)
	if !strings.HasPrefix(row, "─ 1 fakesh ─") {
		t.Errorf("divider row = %q, want the bottom pane's title on it", row)
	}
	if row := frameRow(f, 0); !strings.HasPrefix(row, "─ 0 fakesh ─") {
		t.Errorf("top row = %q, want the top pane's title", row)
	}
}

// The title is the select-pane index plus the program's own title, falling
// back to the window name; the focused pane's text is the active pill.
func TestPaneTitlesRenderTextAndStyle(t *testing.T) {
	srv, ff, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h") // pane 2 (right) is focused
	ff.byID(2).scr.setTitle("vim notes.txt")

	rects := paneRects(sess)
	left, right := rects[1], rects[2]
	f := sess.frame()
	row := frameRow(f, 0)
	if got := row[:left.W]; !strings.HasPrefix(got, "─ 0 fakesh ─") {
		t.Errorf("left title = %q, want the window name", got)
	}
	if got := string([]rune(row)[right.X:]); !strings.HasPrefix(got, "─ 1 vim notes.txt ─") {
		t.Errorf("right title = %q, want the program's title", got)
	}

	dark := theme.Dark()
	active := f.Cells[0*f.Cols+right.X+2] // the "1"
	if active.Ch != '1' || active.FG != uint32(dark.TitleActive.FG) || active.BG != uint32(dark.TitleActive.BG) ||
		active.Attr&vterm.AttrBold == 0 {
		t.Errorf("focused title cell = %+v, want the active pill", active)
	}
	inactive := f.Cells[0*f.Cols+left.X+2] // the "0"
	if inactive.Ch != '0' || inactive.Attr != 0 || inactive.BG != vterm.ColorDefault {
		t.Errorf("unfocused title cell = %+v, want plain text", inactive)
	}
	if line := f.Cells[0*f.Cols+left.X]; line.Ch != '─' || line.FG != uint32(dark.BorderDim.FG) {
		t.Errorf("unfocused title line = %+v, want a dim ─", line)
	}
}

// A zoomed pane fills the content area with no title line.
func TestPaneTitlesHiddenWhileZoomed(t *testing.T) {
	srv, ff, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h")
	exec(t, srv, "resize-pane -Z")

	if c, r := ff.byID(2).pt.size(); c != 80 || r != 23 {
		t.Fatalf("zoomed pane pty = %dx%d, want 80x23", c, r)
	}
	sess.mu.Lock()
	views, _ := sess.windows[sess.cur].views(80, 23, false, nil, nil)
	sess.mu.Unlock()
	if len(views) != 1 || views[0].ShowTitle || views[0].Rect.Y != 0 {
		t.Fatalf("zoomed views = %+v, want one untitled full-area pane", views)
	}
}

// Mouse reports are pane-local: the title row above the pane is not row 0.
func TestPaneTitlesMouseCoordinatesSkipTitleRow(t *testing.T) {
	srv, ff, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h")
	id := activePane(sess)
	rect := activeRect(sess)
	ff.byID(id).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	sess.mouse(protocol.MousePress, rect.X+2, rect.Y, 0) // first content row
	want := fmt.Sprintf("\x1b[<0;%d;%dM", 2+1, 0+1)
	if got := ff.byID(id).pt.sentToChild(); got != want {
		t.Fatalf("sentToChild = %q, want %q", got, want)
	}
}

// Clicking a pane's title line focuses it without sending the app anything.
func TestPaneTitlesClickFocusesPane(t *testing.T) {
	srv, ff, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h") // pane 2 focused
	left := paneRects(sess)[1]
	ff.byID(1).scr.setInputModes(vterm.InputModes{MouseButton: true, MouseSGR: true})

	if repaint := sess.mouse(protocol.MousePress, left.X+3, left.Y-1, 0); !repaint {
		t.Error("focusing a pane from its title should repaint")
	}
	if got := activePane(sess); got != 1 {
		t.Fatalf("active pane = %d, want 1 after clicking its title", got)
	}
	if got := ff.byID(1).pt.sentToChild(); got != "" {
		t.Errorf("a title click must not reach the app, sent %q", got)
	}
}

// The cursor is placed relative to the pane's content, below its title.
func TestPaneTitlesCopyModeAndCursorUseContentRect(t *testing.T) {
	srv, _, sess := setupTitledSession(t)
	exec(t, srv, "split-window -h")
	exec(t, srv, "copy-mode")
	rect := activeRect(sess)

	f := sess.frame()
	if !f.CurVisible || f.CurY < rect.Y || f.CurY >= rect.Y+rect.H || f.CurX < rect.X {
		t.Fatalf("copy-mode cursor at (%d,%d), want inside the content rect %+v", f.CurX, f.CurY, rect)
	}
	sess.mu.Lock()
	cs := sess.windows[sess.cur].panes[sess.windows[sess.cur].active].copy
	rows := cs.rows
	sess.mu.Unlock()
	if rows != rect.H {
		t.Fatalf("copy-mode rows = %d, want the content height %d", rows, rect.H)
	}
}
