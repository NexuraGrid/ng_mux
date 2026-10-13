package server

import (
	"bytes"
	"strings"

	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// copyState is a pane's scrollback/selection mode. While it is non-nil the
// pane's keystrokes drive the scroller instead of the child process.
//
//	arrows / PageUp / PageDown   move the copy cursor; at an edge, scroll
//	g / G                        jump to oldest / newest
//	Space                        start (or restart) a selection at the cursor
//	Enter or y                   copy the selection to the paste buffer, exit
//	Esc or q                     exit without copying
type copyState struct {
	offset int // scrollback lines above the live bottom
	rows   int // viewport height this mode was entered with
	cols   int

	// seen is vterm.Term.ScrolledTotal() as of the last sync (or as of
	// entering copy-mode). See sync for why this anchors the view.
	seen uint64

	cx, cy int // copy cursor, in viewport coordinates

	hasSel bool
	ax     int   // selection anchor column
	aLine  int64 // selection anchor line, as an absolute line number (see lineAt)
}

// lineAt converts viewport row y to an absolute line number: the count of
// lines ever pushed into history (seen) plus the row's position relative to
// the top of the live screen. Scrolling the view (offset) or a sync (which
// moves seen and offset together) leaves a given line's number unchanged, so
// an anchor stored this way stays on the same text. Callers sync first.
func (c *copyState) lineAt(y int) int64 {
	return int64(c.seen) + int64(y) - int64(c.offset)
}

// rowOf is lineAt's inverse: the viewport row showing absolute line n, which
// may be outside [0, rows) when that line is scrolled out of view.
func (c *copyState) rowOf(n int64) int {
	return int(n - int64(c.seen) + int64(c.offset))
}

// setAnchor starts a selection at viewport cell (x, y).
func (c *copyState) setAnchor(x, y int) {
	c.hasSel = true
	c.ax, c.aLine = x, c.lineAt(y)
}

func newCopyState(cols, rows int) *copyState {
	return &copyState{rows: rows, cols: cols, cx: 0, cy: rows - 1}
}

// sync re-anchors offset against however many lines were pushed into history
// since the last sync, so the rows the user is looking at stay fixed while a
// worker keeps printing (bug: without this, offset counts lines above the
// live bottom, so it silently points further and further into the past as
// the live bottom moves down). This holds even when offset is 0 at the
// moment of syncing: entering copy-mode freezes the exact view the user had,
// the same way tmux's copy-mode does, rather than continuing to track new
// output like the live screen does.
//
// If the history ring is at capacity and eviction claims lines the user was
// viewing, offset clamps to histLen and the view pins at the oldest line
// still retained — a graceful degradation, not a crash, but the view can no
// longer track the exact original lines past that point.
func (c *copyState) sync(total uint64, histLen int) {
	if total <= c.seen {
		c.seen = total
		return
	}
	delta := total - c.seen
	c.seen = total
	c.offset += int(delta)
	if c.offset > histLen {
		c.offset = histLen
	}
	if c.offset < 0 {
		c.offset = 0
	}
}

// selection returns the render selection for the current viewport, or nil.
func (c *copyState) selection() *render.Selection {
	if !c.hasSel {
		return nil
	}
	ax, ay := c.ax, c.rowOf(c.aLine)
	// An anchor scrolled out of view: the selection runs to the edge of the
	// viewport on that side.
	switch {
	case ay < 0:
		ax, ay = 0, 0
	case ay >= c.rows:
		ax, ay = c.cols-1, c.rows-1
	}
	return &render.Selection{X0: ax, Y0: ay, X1: c.cx, Y1: c.cy}
}

// selectedText extracts the selection, anchor to cursor, from vt. The anchor
// may be scrolled out of view, so it reads one scrollback window spanning
// both ends rather than just the visible rows. Callers sync first.
func (c *copyState) selectedText(vt screen) string {
	if !c.hasSel {
		return ""
	}
	lo, hi := c.aLine, c.lineAt(c.cy)
	ax, cx := c.ax, c.cx
	if lo > hi {
		lo, hi = hi, lo
	}
	// The window's bottom row is line hi: below counts the rows between hi
	// and the bottom of the live screen. ScrollbackView clamps its offset to
	// the history length, so when hi is on the live screen with less history
	// than that above it (a fresh or cleared pane), the window instead ends at
	// the screen bottom and grows downward by the rest; its top stays line lo.
	below := int(int64(c.seen) + int64(c.rows) - 1 - hi)
	offset := min(below, vt.HistoryLen())
	snap := vt.ScrollbackView(offset, int(hi-lo)+1+below-offset)
	ay := int(c.aLine - lo)
	cy := int(c.lineAt(c.cy) - lo)
	return extractText(snap, ax, ay, cx, cy)
}

func (c *copyState) cursor() *[2]int { return &[2]int{c.cx, c.cy} }

// key applies one key chunk to the scroller state. It returns exit=true when
// copy-mode should end, and yank=true when the caller should copy the current
// selection to the paste buffer before exiting.
func (c *copyState) key(data []byte, histLen int) (exit, yank bool) {
	switch {
	case bytes.Equal(data, []byte{0x1b}), bytes.Equal(data, []byte("q")), bytes.Equal(data, []byte("Q")):
		return true, false

	case bytes.Equal(data, []byte("g")):
		c.offset = histLen
		c.cy = 0
	case bytes.Equal(data, []byte("G")):
		c.offset = 0
		c.cy = c.rows - 1
	case bytes.Equal(data, []byte(" ")):
		c.setAnchor(c.cx, c.cy)
	case bytes.Equal(data, []byte{0x0d}), bytes.Equal(data, []byte("\n")), bytes.Equal(data, []byte("y")):
		return true, c.hasSel

	case isSeq(data, 'A'): // up
		c.moveVertical(-1, histLen)
	case isSeq(data, 'B'): // down
		c.moveVertical(+1, histLen)
	case isSeq(data, 'C'): // right
		if c.cx < c.cols-1 {
			c.cx++
		}
	case isSeq(data, 'D'): // left
		if c.cx > 0 {
			c.cx--
		}
	case isPage(data, true): // PageUp
		c.offset += c.rows
		if c.offset > histLen {
			c.offset = histLen
		}
	case isPage(data, false): // PageDown
		c.offset -= c.rows
		if c.offset < 0 {
			c.offset = 0
		}
	}
	return false, false
}

func (c *copyState) moveVertical(dir, histLen int) {
	ny := c.cy + dir
	switch {
	case ny < 0:
		if c.offset < histLen {
			c.offset++
		}
	case ny >= c.rows:
		if c.offset > 0 {
			c.offset--
		}
	default:
		c.cy = ny
	}
}

// isSeq reports whether data is the CSI arrow sequence ending in final.
func isSeq(data []byte, final byte) bool {
	return len(data) == 3 && data[0] == 0x1b && data[1] == '[' && data[2] == final
}

// isPage reports whether data is ESC[5~ (up) or ESC[6~ (down).
func isPage(data []byte, up bool) bool {
	if len(data) != 4 || data[0] != 0x1b || data[1] != '[' || data[3] != '~' {
		return false
	}
	if up {
		return data[2] == '5'
	}
	return data[2] == '6'
}

// extractText builds the selected string from a viewport snapshot. Endpoints
// are inclusive and given in viewport coordinates.
func extractText(snap vterm.Snapshot, ax, ay, bx, by int) string {
	a := ay*snap.Cols + ax
	b := by*snap.Cols + bx
	if a > b {
		a, b = b, a
	}
	ax, ay = a%snap.Cols, a/snap.Cols
	bx, by = b%snap.Cols, b/snap.Cols

	var out strings.Builder
	for y := ay; y <= by; y++ {
		x0, x1 := 0, snap.Cols-1
		if y == ay {
			x0 = ax
		}
		if y == by {
			x1 = bx
		}
		var line strings.Builder
		for x := x0; x <= x1 && x < snap.Cols; x++ {
			ch := snap.At(x, y).Ch
			if ch == 0 {
				ch = ' '
			}
			line.WriteRune(ch)
		}
		out.WriteString(strings.TrimRight(line.String(), " "))
		if y != by {
			out.WriteByte('\n')
		}
	}
	return out.String()
}
