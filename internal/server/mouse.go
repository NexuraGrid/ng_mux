package server

import (
	"bytes"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// wheelStep is how many scrollback lines (or, on the alternate screen or
// forwarded to an app, cursor-key presses) one wheel notch moves.
const wheelStep = 3

// dragState tracks an in-progress pane-border drag.
type dragState struct {
	active bool
	pane   layout.PaneID
	dir    layout.Orientation
	lastX  int
	lastY  int
}

// mouseState is one client's in-progress pointer interaction: a border drag,
// or a press being forwarded to a pane's app. It lives with the client, not
// the session, so two clients viewing the same session cannot continue or
// cancel each other's drags. It is only touched under the session lock.
type mouseState struct {
	drag dragState // in-progress mouse border drag

	// fwd is the pane a press started forwarding mouse reports to (0 when no
	// forwarded press is in progress). It is set by routePress and consulted
	// by routeDrag/routeRelease so a drag or release that strays off the
	// pane's rectangle (or off any pane at all) still reaches the same app
	// the press did, matching how a real terminal tracks a button once it
	// goes down.
	fwd layout.PaneID

	// sel is the pane a left press may start a drag selection in (0 when
	// none): a pane whose app did not ask for mouse reports. selX/selY is the
	// pressed cell, pane-local. selActive is set once the pointer has moved
	// off that cell and the selection is showing (as copy-mode).
	sel        layout.PaneID
	selX, selY int
	selActive  bool

	// copied is the text the last release copied, handed back by mouseAs.
	copied string
}

// mouse handles one mouse event (coordinates are 0-based cells) using the
// session's own pointer state, for callers with no client (tests, scripts).
// It returns true when the client needs a repaint.
func (s *session) mouse(kind string, x, y, button int) bool {
	repaint, _ := s.mouseAs(&s.mouseDefault, kind, x, y, button)
	return repaint
}

// mouseAs handles one mouse event on behalf of the client owning ms. It
// returns true when the client needs a repaint, and the text a finished drag
// selection copied (empty otherwise) for the caller to mirror to the
// client's clipboard.
func (s *session) mouseAs(ms *mouseState, kind string, x, y, button int) (repaint bool, yanked string) {
	p, data, repaint := s.routeMouse(ms, kind, x, y, button)
	if p != nil && len(data) > 0 {
		_, _ = p.pt.Write(data)
	}
	yanked, ms.copied = ms.copied, ""
	return repaint, yanked
}

// routeMouse is mouse's locked half: it decides what an event means — focus,
// a border resize, a copy-mode scroll, or a report forwarded to the pane's
// own app — and returns the pane plus the bytes to write to its pty (nil if
// nothing should be written), without writing them itself.
//
// The pty write must happen outside the session lock: a child that is not
// reading its stdin (or reading it slowly) can block a pty Write for an
// arbitrary time, and writing while s.mu is held would freeze every other
// pane's input and every frame of this session behind it. This mirrors
// session.input/routeInput's split.
func (s *session) routeMouse(ms *mouseState, kind string, x, y, button int) (p *pane, data []byte, repaint bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w := s.current()
	if w == nil {
		return nil, nil, false
	}
	cols, content := s.cols, s.contentRows()
	// While a button is held for a drag selection, a border drag or an app,
	// motion and the release may wander onto the status row or past the
	// session's edge (a client bigger than the session): keep them, clamped
	// to the content area, so the gesture always ends where it started.
	if (kind == protocol.MouseDrag || kind == protocol.MouseRelease) &&
		(ms.sel != 0 || ms.fwd != 0 || ms.drag.active) {
		x = max(0, min(x, cols-1))
		y = max(0, min(y, content-1))
	}
	if x < 0 || y < 0 || x >= cols {
		return nil, nil, false
	}
	// The status row: only a press acts, and only on a clickable region.
	if y >= content {
		if y == content && kind == protocol.MousePress {
			return nil, nil, s.statusClick(x)
		}
		return nil, nil, false
	}
	rects := layout.Compute(w.tree, w.outer(cols, content))

	switch kind {
	case protocol.MouseWheelUp, protocol.MouseWheelDown:
		return s.routeWheel(w, rects, cols, content, kind, x, y)
	case protocol.MousePress:
		return s.routePress(ms, w, rects, cols, content, x, y, button)
	case protocol.MouseDrag:
		return s.routeDrag(ms, w, rects, cols, content, x, y, button)
	case protocol.MouseRelease:
		return s.routeRelease(ms, w, rects, cols, content, x, y, button)
	}
	return nil, nil, false
}

// routeWheel handles one wheel notch on the pane under the pointer (or the
// active pane, if the pointer is on a divider): forwarded to the app if it
// asked for mouse reports, translated to cursor keys on the alternate screen
// when it did not (xterm's alternateScroll behaviour), or scrolling
// copy-mode — entering it on wheel-up if needed — otherwise. It always moves
// focus to the resolved pane, matching tmux.
func (s *session) routeWheel(w *window, rects map[layout.PaneID]layout.Rect, cols, content int, kind string, x, y int) (*pane, []byte, bool) {
	p, rect := w.paneAndRect(rects, cols, content, x, y)
	if p == nil {
		return nil, nil, false
	}
	w.active = p.id

	if p.copy == nil {
		modes := p.vt.InputModes()
		switch {
		case wantsMouseTracking(modes):
			cb := 64
			if kind == protocol.MouseWheelDown {
				cb = 65
			}
			lx, ly := localCoords(rect, x, y)
			if data, ok := encodeMouseReport(modes.MouseSGR, cb, lx, ly, false); ok {
				return p, data, true
			}
			return nil, nil, true // coordinate too large to encode: drop, still repaint (focus moved)
		case modes.AltScreen:
			return p, bytes.Repeat(arrowSeq(kind, modes.AppCursor), wheelStep), true
		}
	}

	switch kind {
	case protocol.MouseWheelUp:
		if p.copy == nil {
			w.enterCopy(cols, content)
			p = w.panes[w.active]
		}
		if p != nil && p.copy != nil {
			p.syncCopy()
			p.copy.offset += wheelStep
			if max := p.vt.HistoryLen(); p.copy.offset > max {
				p.copy.offset = max
			}
		}
	case protocol.MouseWheelDown:
		if p.copy != nil {
			p.syncCopy()
			p.copy.offset -= wheelStep
			if p.copy.offset <= 0 {
				p.copy = nil // scrolled back to the live screen: leave copy-mode
			}
		}
	}
	return nil, nil, true
}

// routePress handles a button press: focuses the pane under the pointer (or
// starts a border-drag resize when it lands on a divider), and forwards the
// press to the pane's app when it asked for mouse reports. A press that
// focuses a different pane is still forwarded — tmux does both — since an app
// that wants clicks generally wants the one that just switched focus to it.
func (s *session) routePress(ms *mouseState, w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	ms.drag = dragState{}
	ms.fwd = 0
	ms.sel, ms.selActive = 0, false

	id := w.hitPane(rects, x, y)
	if id == 0 {
		ms.drag = dragFrom(rects, x, y)
		// A press on a pane's title line focuses that pane; when the line is
		// also the divider above it, the press still starts a resize drag.
		if t := titleAt(w, rects, x, y); t != 0 && t != w.active {
			w.active = t
			return nil, nil, true
		}
		return nil, nil, false
	}
	focusChanged := id != w.active
	w.active = id

	p := w.panes[id]
	if p == nil {
		return nil, nil, focusChanged
	}
	if p.copy == nil {
		if modes := p.vt.InputModes(); forwardKindAllowed(modes, protocol.MousePress) {
			lx, ly := localCoords(w.rectOf(rects, cols, content, id), x, y)
			if data, ok := encodeMouseReport(modes.MouseSGR, button, lx, ly, false); ok {
				ms.fwd = id
				return p, data, focusChanged
			}
		}
	}
	// A left press on a pane whose app is not tracking the mouse (or one
	// already in copy-mode) may become a drag selection. Nothing happens
	// until the pointer moves, so a plain click only focuses.
	if button == 0 && (p.copy != nil || !wantsMouseTracking(p.vt.InputModes())) {
		ms.sel = id
		ms.selX, ms.selY = localCoords(w.rectOf(rects, cols, content, id), x, y)
	}
	return nil, nil, focusChanged
}

// dragSelect extends a drag selection to (x,y), clamped to the rectangle of
// the pane the press landed on so it never picks up a neighbour's cells. The
// first move off the pressed cell shows the selection by putting that pane in
// copy-mode (if it was not already) anchored at the pressed cell.
func (s *session) dragSelect(ms *mouseState, w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y int) (*pane, []byte, bool) {
	p := w.panes[ms.sel]
	if p == nil {
		ms.sel, ms.selActive = 0, false
		return nil, nil, false
	}
	lx, ly := localCoords(w.rectOf(rects, cols, content, ms.sel), x, y)
	if !ms.selActive {
		if lx == ms.selX && ly == ms.selY {
			return nil, nil, false
		}
		w.enterCopyPane(ms.sel, cols, content) // no-op if already in copy-mode
		if p.copy == nil {
			ms.sel = 0
			return nil, nil, false
		}
		p.syncCopy()
		p.copy.setAnchor(min(ms.selX, p.copy.cols-1), min(ms.selY, p.copy.rows-1))
		ms.selActive = true
	}
	if p.copy == nil { // left copy-mode some other way (a key) mid-drag
		ms.sel, ms.selActive = 0, false
		return nil, nil, true
	}
	p.syncCopy()
	p.copy.cx, p.copy.cy = min(lx, p.copy.cols-1), min(ly, p.copy.rows-1)
	return nil, nil, true
}

// finishSelect ends a drag selection on release: the selected text goes to
// the paste buffer and to ms.copied (for the client's clipboard), and the
// pane leaves copy-mode, as a copy-mode yank does. A press that never moved
// was a click and copies nothing. Caller holds mu.
func (s *session) finishSelect(ms *mouseState, w *window) bool {
	id, active := ms.sel, ms.selActive
	ms.sel, ms.selActive = 0, false
	if !active {
		return false
	}
	p := w.panes[id]
	if p == nil || p.copy == nil {
		return true
	}
	p.syncCopy()
	text := p.copy.selectedText(p.vt)
	p.copy = nil
	if text != "" {
		s.pasteBuf = text
		ms.copied = text
	}
	return true
}

// routeDrag continues a border-resize drag, or forwards pointer motion to the
// pane a press started forwarding to — only reached if that pane's mode still
// allows drag (button-event or any-event tracking; plain "normal" tracking
// has no drag at all). Coordinates are clamped to the pane's rectangle by
// localCoords, so straying off it while the button is held still reports
// something the app can use, matching a real terminal.
func (s *session) routeDrag(ms *mouseState, w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	if ms.sel != 0 {
		return s.dragSelect(ms, w, rects, cols, content, x, y)
	}
	if ms.fwd != 0 {
		p := w.panes[ms.fwd]
		if p == nil || p.copy != nil {
			return nil, nil, false
		}
		modes := p.vt.InputModes()
		if !forwardKindAllowed(modes, protocol.MouseDrag) {
			return nil, nil, false
		}
		lx, ly := localCoords(w.rectOf(rects, cols, content, ms.fwd), x, y)
		data, ok := encodeMouseReport(modes.MouseSGR, 32+button, lx, ly, false)
		if !ok {
			return nil, nil, false
		}
		return p, data, false
	}

	if !ms.drag.active {
		return nil, nil, false
	}
	outer := w.outer(cols, content)
	if ms.drag.dir == layout.Horizontal {
		if d := x - ms.drag.lastX; d != 0 {
			_ = layout.Resize(w.tree, ms.drag.pane, layout.Horizontal, d, outer)
			ms.drag.lastX = x
		}
	} else {
		if d := y - ms.drag.lastY; d != 0 {
			_ = layout.Resize(w.tree, ms.drag.pane, layout.Vertical, d, outer)
			ms.drag.lastY = y
		}
	}
	w.applyLayout(cols, content)
	return nil, nil, true
}

// routeRelease ends a border drag or a drag selection (copying it), and forwards the release to the pane a
// press started forwarding to, if its mode still reports release.
func (s *session) routeRelease(ms *mouseState, w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	ms.drag = dragState{}
	if ms.sel != 0 {
		return nil, nil, s.finishSelect(ms, w)
	}
	if ms.fwd == 0 {
		return nil, nil, false
	}
	id := ms.fwd
	ms.fwd = 0

	p := w.panes[id]
	if p == nil || p.copy != nil {
		return nil, nil, false
	}
	modes := p.vt.InputModes()
	if !forwardKindAllowed(modes, protocol.MouseRelease) {
		return nil, nil, false
	}
	lx, ly := localCoords(w.rectOf(rects, cols, content, id), x, y)
	data, ok := encodeMouseReport(modes.MouseSGR, button, lx, ly, true)
	if !ok {
		return nil, nil, false
	}
	return p, data, false
}

// paneAt returns the id of the pane whose rectangle contains (x,y), or 0.
func paneAt(rects map[layout.PaneID]layout.Rect, x, y int) layout.PaneID {
	for id, r := range rects {
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			return id
		}
	}
	return 0
}

// titleAt returns the pane whose title line covers (x,y) — the row just above
// its rectangle, across its width — or 0 when the window has no titles or the
// point is on none.
func titleAt(w *window, rects map[layout.PaneID]layout.Rect, x, y int) layout.PaneID {
	if !w.titled() {
		return 0
	}
	for id, r := range rects {
		if y == r.Y-1 && x >= r.X && x < r.X+r.W {
			return id
		}
	}
	return 0
}

// dragFrom decides whether (x,y) is on a divider between two panes and, if so,
// returns a dragState anchored to the pane whose boundary should move.
func dragFrom(rects map[layout.PaneID]layout.Rect, x, y int) dragState {
	left := paneAt(rects, x-1, y)
	right := paneAt(rects, x+1, y)
	if left != 0 && right != 0 && left != right {
		return dragState{active: true, pane: left, dir: layout.Horizontal, lastX: x, lastY: y}
	}
	up := paneAt(rects, x, y-1)
	down := paneAt(rects, x, y+1)
	if up != 0 && down != 0 && up != down {
		return dragState{active: true, pane: up, dir: layout.Vertical, lastX: x, lastY: y}
	}
	return dragState{}
}
