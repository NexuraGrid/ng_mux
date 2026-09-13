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

// mouse handles one mouse event (coordinates are 0-based cells). It returns
// true when the client needs a repaint.
func (s *session) mouse(kind string, x, y, button int) bool {
	p, data, repaint := s.routeMouse(kind, x, y, button)
	if p != nil && len(data) > 0 {
		_, _ = p.pt.Write(data)
	}
	return repaint
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
func (s *session) routeMouse(kind string, x, y, button int) (p *pane, data []byte, repaint bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w := s.current()
	if w == nil {
		return nil, nil, false
	}
	cols, content := s.cols, s.contentRows()
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
		return s.routePress(w, rects, cols, content, x, y, button)
	case protocol.MouseDrag:
		return s.routeDrag(w, rects, cols, content, x, y, button)
	case protocol.MouseRelease:
		return s.routeRelease(w, rects, cols, content, x, y, button)
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
func (s *session) routePress(w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	s.drag = dragState{}
	s.mouseFwd = 0

	id := w.hitPane(rects, x, y)
	if id == 0 {
		s.drag = dragFrom(rects, x, y)
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
				s.mouseFwd = id
				return p, data, focusChanged
			}
		}
	}
	return nil, nil, focusChanged
}

// routeDrag continues a border-resize drag, or forwards pointer motion to the
// pane a press started forwarding to — only reached if that pane's mode still
// allows drag (button-event or any-event tracking; plain "normal" tracking
// has no drag at all). Coordinates are clamped to the pane's rectangle by
// localCoords, so straying off it while the button is held still reports
// something the app can use, matching a real terminal.
func (s *session) routeDrag(w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	if s.mouseFwd != 0 {
		p := w.panes[s.mouseFwd]
		if p == nil || p.copy != nil {
			return nil, nil, false
		}
		modes := p.vt.InputModes()
		if !forwardKindAllowed(modes, protocol.MouseDrag) {
			return nil, nil, false
		}
		lx, ly := localCoords(w.rectOf(rects, cols, content, s.mouseFwd), x, y)
		data, ok := encodeMouseReport(modes.MouseSGR, 32+button, lx, ly, false)
		if !ok {
			return nil, nil, false
		}
		return p, data, false
	}

	if !s.drag.active {
		return nil, nil, false
	}
	outer := w.outer(cols, content)
	if s.drag.dir == layout.Horizontal {
		if d := x - s.drag.lastX; d != 0 {
			_ = layout.Resize(w.tree, s.drag.pane, layout.Horizontal, d, outer)
			s.drag.lastX = x
		}
	} else {
		if d := y - s.drag.lastY; d != 0 {
			_ = layout.Resize(w.tree, s.drag.pane, layout.Vertical, d, outer)
			s.drag.lastY = y
		}
	}
	w.applyLayout(cols, content)
	return nil, nil, true
}

// routeRelease ends a border drag, and forwards the release to the pane a
// press started forwarding to, if its mode still reports release.
func (s *session) routeRelease(w *window, rects map[layout.PaneID]layout.Rect, cols, content, x, y, button int) (*pane, []byte, bool) {
	s.drag = dragState{}
	if s.mouseFwd == 0 {
		return nil, nil, false
	}
	id := s.mouseFwd
	s.mouseFwd = 0

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
