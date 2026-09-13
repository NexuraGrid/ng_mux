package server

import (
	"fmt"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// window is one window of a session: a binary split tree of panes with exactly
// one focused pane. Every method assumes the owning session's mutex is held.
type window struct {
	id     int
	name   string
	tree   *layout.Node
	panes  map[layout.PaneID]*pane
	active layout.PaneID

	// zoom is the pane currently shown full-screen, or 0 when the window is
	// tiled normally. A zoomed pane keeps its place in the tree; only the
	// rendering and sizing treat it as if it owned the whole content area.
	zoom layout.PaneID

	shell     string
	histLimit int
	newPane   paneFactory

	// logf reports unusual daemon-side conditions from panes owned by this
	// window (a recovered emulator panic, a saved goroutine panic); see
	// sessionOpts.logf. Propagated to every pane the window creates or adopts.
	logf func(format string, args ...any)
}

// newWindow creates a window with a single pane sized to the given content area.
func newWindow(id int, name string, paneID layout.PaneID, cols, rows int, shell string, histLimit int, newPane paneFactory, logf func(format string, args ...any)) (*window, error) {
	if newPane == nil {
		newPane = startPane
	}
	p, err := newPane(paneID, cols, rows, shell, histLimit)
	if err != nil {
		return nil, err
	}
	// Safe to set directly: p was just created and its pump goroutine has not
	// started yet (the caller starts it after newWindow returns), so there is
	// no concurrent reader of p.logf. wrapWindow deliberately does not touch
	// p.logf itself, because it is also used by break-pane to wrap a pane
	// whose pump IS already running — mutating p.logf there would race with
	// pump's read of it.
	p.logf = logf
	return wrapWindow(id, name, p, shell, histLimit, newPane, logf), nil
}

// wrapWindow builds a window around an already-running pane. break-pane uses it
// to move a live pane into a window of its own without restarting its shell.
// It does not set p.logf: for a pane that already has a running pump goroutine
// (break-pane's use case), doing so would race with pump's reads of it. logf
// is server-wide and never actually changes across a pane's lifetime, so the
// value the pane got at creation remains correct.
func wrapWindow(id int, name string, p *pane, shell string, histLimit int, newPane paneFactory, logf func(format string, args ...any)) *window {
	w := &window{
		id:        id,
		name:      name,
		tree:      layout.NewLeaf(p.id),
		panes:     map[layout.PaneID]*pane{p.id: p},
		active:    p.id,
		shell:     shell,
		histLimit: histLimit,
		newPane:   newPane,
		logf:      logf,
	}
	p.win = w
	return w
}

// resizeStep is how many cells one resize keystroke moves a boundary.
const resizeStep = 2

func (w *window) outer(cols, rows int) layout.Rect {
	return layout.Rect{X: 0, Y: 0, W: cols, H: rows}
}

// applyLayout pushes each pane's computed rectangle to its pty and emulator.
// While a pane is zoomed only that pane is sized, to the whole content area;
// the hidden panes keep their last size until the window is un-zoomed.
func (w *window) applyLayout(cols, rows int) {
	if zp := w.zoomedPane(); zp != nil {
		zp.resize(max1(cols), max1(rows))
		return
	}
	for id, r := range layout.Compute(w.tree, w.outer(cols, rows)) {
		if p := w.panes[id]; p != nil {
			p.resize(max1(r.W), max1(r.H))
		}
	}
}

// zoomedPane returns the pane shown full-screen, or nil when the window is not
// zoomed. If the zoomed pane has since closed it self-heals by clearing zoom.
func (w *window) zoomedPane() *pane {
	if w.zoom == 0 {
		return nil
	}
	p := w.panes[w.zoom]
	if p == nil {
		w.zoom = 0
	}
	return p
}

// toggleZoom flips full-screen zoom for the active pane and re-sizes panes to
// match. Zooming a window with a single pane is a no-op. It reports whether the
// window is zoomed afterwards.
func (w *window) toggleZoom(cols, rows int) bool {
	if w.zoom != 0 {
		w.zoom = 0
	} else if len(w.panes) > 1 {
		w.zoom = w.active
	}
	w.applyLayout(cols, rows)
	return w.zoom != 0
}

// unzoom clears zoom and restores the tiled layout, if the window was zoomed.
func (w *window) unzoom(cols, rows int) {
	if w.zoom == 0 {
		return
	}
	w.zoom = 0
	w.applyLayout(cols, rows)
}

// split adds a pane beside the active one, sizing it to the new layout.
func (w *window) split(sess *session, dir layout.Orientation, cols, rows int) error {
	newID := sess.nextPane()
	newTree, err := layout.Split(w.tree, w.active, newID, dir, w.outer(cols, rows))
	if err != nil {
		return err
	}
	r := layout.Compute(newTree, w.outer(cols, rows))[newID]
	p, err := w.newPane(newID, max1(r.W), max1(r.H), w.shell, w.histLimit)
	if err != nil {
		return err
	}
	p.win = w
	p.logf = w.logf
	w.zoom = 0 // a new pane is only useful visible; drop zoom like tmux does
	w.tree = newTree
	w.panes[newID] = p
	w.active = newID
	go p.pump(sess.reportExit)
	w.applyLayout(cols, rows)
	return nil
}

func (w *window) focus(delta int) {
	w.active = layout.Neighbor(w.tree, w.active, delta)
}

func (w *window) resizeActive(dir layout.Orientation, delta, cols, rows int) {
	if w.zoom != 0 {
		return // a zoomed pane already fills the area; nothing to resize
	}
	if err := layout.Resize(w.tree, w.active, dir, delta, w.outer(cols, rows)); err == nil {
		w.applyLayout(cols, rows)
	}
}

// enterCopy puts the active pane into copy-mode sized to its current rectangle.
func (w *window) enterCopy(cols, rows int) {
	p := w.panes[w.active]
	if p == nil || p.copy != nil {
		return
	}
	r := layout.Compute(w.tree, w.outer(cols, rows))[w.active]
	if w.zoom == w.active {
		r = w.outer(cols, rows) // the zoomed pane owns the whole content area
	}
	p.copy = newCopyState(max1(r.W), max1(r.H))
}

// removePane drops a pane, collapses the tree, moves focus to its sibling, and
// re-flows the surviving panes into the freed space (cols/rows is the current
// content area). It reports whether the window is now empty.
func (w *window) removePane(id layout.PaneID, cols, rows int) (empty bool) {
	p, ok := w.panes[id]
	if !ok {
		return len(w.panes) == 0
	}
	p.close()
	delete(w.panes, id)
	if len(w.panes) == 0 {
		w.tree = nil
		return true
	}
	if newTree, focus, err := layout.Remove(w.tree, id); err == nil {
		w.tree = newTree
		if w.active == id {
			w.active = focus
		}
	}
	w.applyLayout(cols, rows)
	return false
}

// swapActive exchanges the active pane with its neighbour (delta -1 previous,
// +1 next) in traversal order, keeping focus on the pane that was active. The
// two slots may differ in size, so the layout is re-applied.
func (w *window) swapActive(delta, cols, rows int) {
	other := layout.Neighbor(w.tree, w.active, delta)
	if other == w.active {
		return
	}
	if layout.SwapPanes(w.tree, w.active, other) {
		w.zoom = 0
		w.applyLayout(cols, rows)
	}
}

// detachPane removes a pane from the window WITHOUT closing it and re-flows the
// rest, returning the still-running pane so a caller can move it to another
// window. It returns nil if the pane is unknown or is the window's last one
// (breaking out the only pane would just leave an empty window).
func (w *window) detachPane(id layout.PaneID, cols, rows int) *pane {
	p, ok := w.panes[id]
	if !ok || len(w.panes) <= 1 {
		return nil
	}
	delete(w.panes, id)
	if newTree, focus, err := layout.Remove(w.tree, id); err == nil {
		w.tree = newTree
		if w.active == id {
			w.active = focus
		}
	}
	w.zoom = 0
	w.applyLayout(cols, rows)
	return p
}

// adoptPane splices an existing, already-running pane in beside the active one,
// splitting dir. It returns ErrNoRoom (via layout.Split) without changing
// anything when the split will not fit. It does not touch p.logf: the pane's
// pump goroutine is already running (join-pane's use case), and mutating
// p.logf here would race with pump's reads of it; logf is server-wide and
// does not change across a pane's lifetime anyway.
func (w *window) adoptPane(p *pane, dir layout.Orientation, cols, rows int) error {
	newTree, err := layout.Split(w.tree, w.active, p.id, dir, w.outer(cols, rows))
	if err != nil {
		return err
	}
	w.zoom = 0
	w.tree = newTree
	w.panes[p.id] = p
	w.active = p.id
	p.win = w
	w.applyLayout(cols, rows)
	return nil
}

func (w *window) closeAll() {
	for id, p := range w.panes {
		p.close()
		delete(w.panes, id)
	}
	w.tree = nil
}

// views builds the per-pane render input for the given content area, appending
// into the caller-owned views slice and using snaps as reusable snapshot
// storage (one entry per pane). Both are returned so the session can keep the
// grown backing arrays for the next frame. A pane in copy-mode is drawn from
// its scrollback view with the selection highlighted. When showNums is set each
// pane is badged with its select-pane index (display-panes).
func (w *window) views(cols, rows int, showNums bool, views []render.PaneView, snaps []vterm.Snapshot) ([]render.PaneView, []vterm.Snapshot) {
	numOf := map[layout.PaneID]int{}
	if showNums {
		for i, id := range layout.Panes(w.tree) {
			numOf[id] = i
		}
	}

	if zp := w.zoomedPane(); zp != nil {
		if cap(snaps) < 1 {
			snaps = make([]vterm.Snapshot, 1)
		} else {
			snaps = snaps[:1]
		}
		sn := &snaps[0]
		full := w.outer(cols, rows)
		pv := render.PaneView{ID: w.zoom, Rect: full, Active: true}
		if showNums {
			pv.Badge = fmt.Sprintf(" %d ", numOf[w.zoom])
		}
		if zp.copy != nil {
			*sn = zp.vt.ScrollbackView(zp.copy.offset, max1(full.H))
			pv.Sel = zp.copy.selection()
			pv.CopyCur = zp.copy.cursor()
			total := zp.vt.HistoryLen()
			pv.Overlay = fmt.Sprintf(" COPY %d/%d ", total-zp.copy.offset, total)
		} else {
			zp.vt.SnapshotInto(sn)
		}
		pv.Snap = sn
		return append(views, pv), snaps
	}

	rects := layout.Compute(w.tree, w.outer(cols, rows))

	// Size snaps to the pane count up front: taking &snaps[i] below is only
	// safe if the backing array cannot move mid-loop.
	n := len(w.panes)
	if cap(snaps) < n {
		snaps = make([]vterm.Snapshot, n)
	} else {
		snaps = snaps[:n]
	}

	i := 0
	for id, p := range w.panes {
		sn := &snaps[i]
		i++
		pv := render.PaneView{ID: id, Rect: rects[id], Active: id == w.active}
		if showNums {
			pv.Badge = fmt.Sprintf(" %d ", numOf[id])
		}
		if p.copy != nil {
			*sn = p.vt.ScrollbackView(p.copy.offset, max1(rects[id].H))
			pv.Sel = p.copy.selection()
			pv.CopyCur = p.copy.cursor()
			total := p.vt.HistoryLen()
			pv.Overlay = fmt.Sprintf(" COPY %d/%d ", total-p.copy.offset, total)
		} else {
			p.vt.SnapshotInto(sn)
		}
		pv.Snap = sn
		views = append(views, pv)
	}
	return views, snaps
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
